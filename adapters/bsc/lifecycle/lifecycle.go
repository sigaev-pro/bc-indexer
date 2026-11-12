// Package lifecycle is the client-independent caller map and gate of
// E01-02.lifecycle_seams. It holds the map of every required native
// execution, system, canonical-chain and finality caller of Geth, BSC and Bor
// (callers.json), checks that each one has a route through the observation
// guard, and gives the native patches one gate: a mapped caller enters it
// with the held guard token, native write choke points require an open pass
// of a caller with that effect, and an unmapped write fails closed. It also
// records the live boundary H and the evidence rules of the finality callers.
// The design is in README.md. The package imports only the standard library
// and the shared admission and decision packages.
package lifecycle

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
)

//go:embed callers.json
var callersJSON []byte

// keyPrefix starts every key. Every key is longer than 33 bytes and starts
// with "e", so the native offline pruner never deletes it.
const keyPrefix = "extractor-lifecycle-v1/"

// tagBoundary is the domain tag of the boundary digest, followed by one zero
// byte.
const tagBoundary = "extractor-lifecycle-boundary-v1"

var (
	// ErrInvalid rejects a malformed argument.
	ErrInvalid = errors.New("invalid lifecycle argument")
	// ErrMap rejects a caller map that breaks a map rule.
	ErrMap = errors.New("invalid caller map")
	// ErrUnmapped rejects a caller ID that the map of this client does not
	// list.
	ErrUnmapped = errors.New("caller is not in the caller map")
	// ErrRefused is the fail-closed answer to a caller that the capture
	// profile forbids.
	ErrRefused = errors.New("caller is refused in the capture profile")
	// ErrPhase rejects a step that the gate phase does not allow.
	ErrPhase = errors.New("gate phase forbids this step")
	// ErrKind rejects a decision kind that the caller may not make.
	ErrKind = errors.New("decision kind not allowed for this caller")
	// ErrOrder rejects a pass that is used or closed out of order.
	ErrOrder = errors.New("pass used out of order")
	// ErrBypass reports a native mutation without an open pass of a caller
	// with that effect. It stops the gate.
	ErrBypass = errors.New("native mutation bypassed the gate")
	// ErrRoot rejects a boundary whose native head state is not durable.
	ErrRoot = errors.New("boundary state root is not durable")
	// ErrCorrupt reports a damaged boundary record.
	ErrCorrupt = errors.New("corrupt lifecycle record")
	// ErrInvariant reports a boundary record of another store or producer.
	ErrInvariant = errors.New("source invariant error")
	// ErrFailed reports a gate that stopped after an error.
	ErrFailed = errors.New("lifecycle gate stopped after an error")
)

var (
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	storeIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._:-]{0,127}$`)
	idPattern      = regexp.MustCompile(`^(geth|bsc|bor)\.[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	sourcePattern  = regexp.MustCompile(`^[A-Za-z0-9_./-]+\.go:[A-Za-z_][A-Za-z0-9_.]*$`)
	ownerPattern   = regexp.MustCompile(`^E[0-9]{2}-[0-9]{2}\.[a-z_]+$`)
)

// Clients are the client names of the map, in map order.
var Clients = []string{"geth", "bsc", "bor"}

// Effect is a native mutation class that a caller may cause.
type Effect string

// Effects.
const (
	// Execution executes block transactions against chain state.
	Execution Effect = "EXECUTION"
	// System runs a system call or system transaction against chain state.
	System Effect = "SYSTEM"
	// Canonical changes the head or the canonical number to hash mapping.
	Canonical Effect = "CANONICAL"
	// Finality changes the safe, justified or finalized block.
	Finality Effect = "FINALITY"
)

var effects = []Effect{Execution, System, Canonical, Finality}

// Route is how a caller passes the observation guard.
type Route string

// Routes. A map has no bypass route: every caller that mutates chain state
// is OBSERVATION, DECISION or REFUSE.
const (
	// Observation executes new blocks under an admission observation; the
	// decision that follows closes it.
	Observation Route = "OBSERVATION"
	// Decision changes the canonical chain or finality without a new
	// execution, through a decision intent.
	Decision Route = "DECISION"
	// Refuse is a caller that the capture profile forbids. The gate, the
	// profile check or a native guard refuses it; it must fail closed.
	Refuse Route = "REFUSE"
	// ReadOnly re-executes or reads state without changing chain state. It
	// needs no pass and is never a capture point.
	ReadOnly Route = "READ_ONLY"
)

// Trigger is the native event that reaches a caller.
type Trigger string

// Triggers.
const (
	NewBlock      Trigger = "NEW_BLOCK"
	NoNewBlock    Trigger = "NO_NEW_BLOCK"
	KnownBranch   Trigger = "KNOWN_BRANCH"
	SideChain     Trigger = "SIDE_CHAIN"
	Reorg         Trigger = "REORG"
	Startup       Trigger = "STARTUP"
	SyncMode      Trigger = "SYNC_MODE"
	Sealing       Trigger = "SEALING"
	RPC           Trigger = "RPC"
	ManualCommand Trigger = "MANUAL_COMMAND"
)

var triggers = []Trigger{NewBlock, NoNewBlock, KnownBranch, SideChain, Reorg, Startup, SyncMode, Sealing, RPC,
	ManualCommand}

// Status is the state of the guard on a caller.
type Status string

// Statuses.
const (
	// Guarded callers already pass the guard through a merged patch.
	Guarded Status = "GUARDED"
	// Planned callers get their route from the owner task.
	Planned Status = "PLANNED"
)

// Caller is one native caller of the map.
type Caller struct {
	// ID is "<client>.<name>", unique in the map.
	ID     string `json:"id"`
	Client string `json:"client"`
	// Source is "<file>.go:<function>" at the pinned source commit.
	Source string `json:"source"`
	// Entry lists the native entry points that reach the caller.
	Entry   []string        `json:"entry"`
	Effects []Effect        `json:"effects"`
	Trigger Trigger         `json:"trigger"`
	Route   Route           `json:"route"`
	Kinds   []decision.Kind `json:"kinds"`
	Status  Status          `json:"status"`
	// Owner is the task that guards the caller (Guarded) or must (Planned).
	Owner string `json:"owner"`
	// Patch names the merged native edit of a Guarded caller.
	Patch string `json:"patch"`
	// Refusal names the fail-closed mechanism of a Refuse caller.
	Refusal string `json:"refusal"`
	Note    string `json:"note"`
}

// Map is the caller map.
type Map struct {
	SchemaVersion int `json:"schema_version"`
	// Sources maps each client to its pinned source commit.
	Sources map[string]string `json:"sources"`
	Callers []Caller          `json:"callers"`
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// callerFields are the JSON names of the Caller fields; every caller must
// carry all of them.
var callerFields = []string{"id", "client", "source", "entry", "effects", "trigger", "route", "kinds", "status",
	"owner", "patch", "refusal", "note"}

// present checks that every caller carries every field, so a missing field
// is not read as its zero value. A map without its schema version, sources or
// callers fails Validate.
func present(data []byte) error {
	var raw struct {
		Callers []map[string]json.RawMessage `json:"callers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for i, c := range raw.Callers {
		for _, name := range callerFields {
			if _, ok := c[name]; !ok {
				return fmt.Errorf("caller %d has no %q", i, name)
			}
		}
	}
	return nil
}

// ParseMap decodes and validates a caller map. Every field must be present.
func ParseMap(data []byte) (Map, error) {
	var m Map
	if err := strictDecode(data, &m); err != nil {
		return Map{}, fmt.Errorf("%w: %v", ErrMap, err)
	}
	if err := present(data); err != nil {
		return Map{}, fmt.Errorf("%w: %v", ErrMap, err)
	}
	if err := Validate(m); err != nil {
		return Map{}, err
	}
	return m, nil
}

// Callers returns the validated caller map shipped with the package.
func Callers() (Map, error) { return ParseMap(callersJSON) }

func unique[T comparable](values []T) bool {
	seen := map[T]bool{}
	for _, v := range values {
		if seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

func mapErr(c Caller, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrMap, c.ID, fmt.Sprintf(format, args...))
}

// validCaller checks one caller against the map rules in README.md.
func validCaller(c Caller) error {
	if !idPattern.MatchString(c.ID) || !strings.HasPrefix(c.ID, c.Client+".") {
		return mapErr(c, "id does not match its client %q", c.Client)
	}
	if !sourcePattern.MatchString(c.Source) {
		return mapErr(c, "source %q", c.Source)
	}
	if len(c.Entry) == 0 || slices.Contains(c.Entry, "") {
		return mapErr(c, "no entry point")
	}
	if !slices.Contains(triggers, c.Trigger) {
		return mapErr(c, "trigger %q", c.Trigger)
	}
	if !unique(c.Effects) || !unique(c.Kinds) {
		return mapErr(c, "repeated effect or kind")
	}
	for _, e := range c.Effects {
		if !slices.Contains(effects, e) {
			return mapErr(c, "effect %q", e)
		}
	}
	if c.Refusal != "" && c.Route != Refuse {
		return mapErr(c, "refusal on a %s route", c.Route)
	}
	switch c.Route {
	case ReadOnly:
		if len(c.Effects) != 0 || len(c.Kinds) != 0 {
			return mapErr(c, "READ_ONLY with effects or kinds")
		}
	case Observation:
		if !slices.Contains(c.Effects, Execution) || len(c.Kinds) == 0 {
			return mapErr(c, "OBSERVATION needs EXECUTION and a decision kind")
		}
		for _, k := range c.Kinds {
			if k != decision.Import && k != decision.Side && k != decision.Canonical {
				return mapErr(c, "OBSERVATION kind %q", k)
			}
		}
	case Decision:
		if len(c.Effects) == 0 || len(c.Effects) != len(c.Kinds) {
			return mapErr(c, "DECISION needs one kind per effect")
		}
		for _, e := range c.Effects {
			want := map[Effect]decision.Kind{Canonical: decision.Canonical, Finality: decision.Finality}[e]
			if want == "" || !slices.Contains(c.Kinds, want) {
				return mapErr(c, "DECISION effect %q without its kind", e)
			}
		}
	case Refuse:
		if len(c.Effects) == 0 || len(c.Kinds) != 0 || c.Refusal == "" {
			return mapErr(c, "REFUSE needs effects, a refusal and no kinds")
		}
	default:
		return mapErr(c, "route %q", c.Route)
	}
	switch c.Status {
	case Guarded:
		if c.Patch == "" {
			return mapErr(c, "GUARDED without a patch")
		}
	case Planned:
		if c.Patch != "" {
			return mapErr(c, "PLANNED with a patch")
		}
	default:
		return mapErr(c, "status %q", c.Status)
	}
	if !ownerPattern.MatchString(c.Owner) {
		return mapErr(c, "owner %q", c.Owner)
	}
	return nil
}

// Validate checks the caller map rules in README.md: every client is pinned
// and has callers, every caller has a valid route, and the routed callers of
// each client cover every effect.
func Validate(m Map) error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("%w: schema version %d", ErrMap, m.SchemaVersion)
	}
	if len(m.Sources) != len(Clients) {
		return fmt.Errorf("%w: sources %v", ErrMap, m.Sources)
	}
	for _, client := range Clients {
		if !commitPattern.MatchString(m.Sources[client]) {
			return fmt.Errorf("%w: source commit of %q", ErrMap, client)
		}
	}
	ids := map[string]bool{}
	covered := map[string]map[Effect]bool{}
	for _, c := range m.Callers {
		// The id pattern names one of the clients, and validCaller checks the
		// id prefix against the client.
		if err := validCaller(c); err != nil {
			return err
		}
		if ids[c.ID] {
			return fmt.Errorf("%w: caller %q listed twice", ErrMap, c.ID)
		}
		ids[c.ID] = true
		if c.Route == Observation || c.Route == Decision {
			if covered[c.Client] == nil {
				covered[c.Client] = map[Effect]bool{}
			}
			for _, e := range c.Effects {
				covered[c.Client][e] = true
			}
		}
	}
	for _, client := range Clients {
		for _, e := range effects {
			if !covered[client][e] {
				return fmt.Errorf("%w: no routed %s caller of %s", ErrMap, e, client)
			}
		}
	}
	return nil
}

// Phase is the gate phase of a producer run.
type Phase string

// Phases.
const (
	// Bootstrap is the initial native sync before the live boundary exists.
	// Mapped callers pass without capture.
	Bootstrap Phase = "BOOTSTRAP"
	// Recovery is a restart before GoLive. Only the startup driver and the
	// callers nested in its pass may enter.
	Recovery Phase = "RECOVERY"
	// Live captures every mapped caller.
	Live Phase = "LIVE"
	// Drain restores saved obligations and never goes live.
	Drain Phase = "DRAIN"
)

// Checker is the admission check of the held guard token;
// *admission.Admission implements it.
type Checker interface {
	Check(t *admission.Token) error
	Producer() admission.Producer
}

// Intender records a transition intent; *decision.Bridge implements it.
type Intender interface {
	Intend(t *admission.Token, in decision.Intent) (decision.IntentRecord, error)
}

// LiveSwitcher switches the decision bridge to LIVE; *decision.Bridge
// implements it.
type LiveSwitcher interface {
	GoLive(t *admission.Token) error
}

// Boundary is the durable live boundary H of a producer run: the native head
// read under the guard when the run first went live. Capture starts with the
// first mutation after it.
type Boundary struct {
	NativeStoreID string        `json:"native_store_id"`
	Producer      string        `json:"producer"`
	Head          decision.Head `json:"head"`
	Digest        string        `json:"digest"`
}

func u64(value uint64) []byte { return binary.BigEndian.AppendUint64(nil, value) }

func str(value string) []byte { return append(u64(uint64(len(value))), value...) }

func digestOf(tag string, parts ...[]byte) string {
	h := sha256.New()
	h.Write(append([]byte(tag), 0))
	for _, part := range parts {
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func rawHash(value string) ([]byte, error) {
	if !digestPattern.MatchString(value) {
		return nil, fmt.Errorf("%w: hash %q", ErrInvalid, value)
	}
	return hex.DecodeString(value)
}

func headBytes(h decision.Head) ([]byte, error) {
	hash, err := rawHash(h.Hash)
	if err != nil {
		return nil, err
	}
	return append(u64(h.Number), hash...), nil
}

// BoundaryDigest returns the digest of a boundary record.
func BoundaryDigest(b Boundary) (string, error) {
	head, err := headBytes(b.Head)
	if err != nil {
		return "", err
	}
	return digestOf(tagBoundary, str(b.NativeStoreID), str(b.Producer), head), nil
}

// Reach counts how often a caller entered the gate, how often it recorded a
// decision intent through its pass, and how often the gate refused it.
type Reach struct {
	Passes   uint64
	Intents  uint64
	Refusals uint64
}

// Gate is the lifecycle gate of one client and producer run. Enter, Exit,
// Intend and GoLive need the held guard token; Require may be called from a
// native write path without it. A Gate is safe for concurrent use.
type Gate struct {
	mu       sync.Mutex
	client   string
	callers  map[string]Caller
	adm      Checker
	db       admission.Store
	key      []byte
	storeID  string
	producer string
	phase    Phase
	boundary *Boundary
	stack    []*Pass
	reached  map[string]Reach
	err      error
}

// Pass is one open entry of a mapped caller into the gate. Methods on a nil
// Pass panic.
type Pass struct {
	gate    *Gate
	caller  Caller
	token   *admission.Token
	observe bool
	// granted lists the effects that an intent (or an unchanged finality
	// setter) of this pass allows at the choke points.
	granted map[Effect]bool
}

// Open syncs the store and opens the gate of client for the producer run of
// adm, with the caller map shipped in the package. Without a stored
// boundary the gate starts in Bootstrap; with one, in Recovery. With drain
// it starts in Drain and never goes live.
func Open(t *admission.Token, db admission.Store, adm Checker, storeID, client string, drain bool) (*Gate, error) {
	m, err := Callers()
	if err != nil {
		return nil, err
	}
	return OpenMap(t, db, adm, storeID, client, drain, m)
}

// OpenMap is Open with a given caller map.
func OpenMap(t *admission.Token, db admission.Store, adm Checker, storeID, client string, drain bool,
	m Map) (*Gate, error) {
	if adm == nil || db == nil {
		return nil, fmt.Errorf("%w: missing admission or store", ErrInvalid)
	}
	if err := adm.Check(t); err != nil {
		return nil, err
	}
	if !storeIDPattern.MatchString(storeID) {
		return nil, fmt.Errorf("%w: native store ID %q", ErrInvalid, storeID)
	}
	if !slices.Contains(Clients, client) {
		return nil, fmt.Errorf("%w: client %q", ErrInvalid, client)
	}
	if err := Validate(m); err != nil {
		return nil, err
	}
	p := adm.Producer()
	g := &Gate{client: client, callers: map[string]Caller{}, adm: adm, db: db,
		key: []byte(keyPrefix + p.ProducerID + "." + p.RunID + "/boundary"), storeID: storeID,
		producer: p.ProducerID + "." + p.RunID, reached: map[string]Reach{}}
	for _, c := range m.Callers {
		if c.Client == client {
			g.callers[c.ID] = c
		}
	}
	if err := db.SyncKeyValue(); err != nil {
		return nil, err
	}
	ok, err := db.Has(g.key)
	if err != nil {
		return nil, err
	}
	if ok {
		data, err := db.Get(g.key)
		if err != nil {
			return nil, err
		}
		var b Boundary
		if err := strictDecode(data, &b); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, g.key, err)
		}
		digest, err := BoundaryDigest(b)
		if err != nil || digest != b.Digest {
			return nil, fmt.Errorf("%w: boundary digest", ErrCorrupt)
		}
		if b.NativeStoreID != storeID || b.Producer != g.producer {
			return nil, fmt.Errorf("%w: boundary of %s %s, not %s %s", ErrInvariant, b.NativeStoreID, b.Producer,
				storeID, g.producer)
		}
		g.boundary = &b
	}
	switch {
	case drain:
		g.phase = Drain
	case g.boundary != nil:
		g.phase = Recovery
	default:
		g.phase = Bootstrap
	}
	return g, nil
}

// Phase returns the gate phase.
func (g *Gate) Phase() Phase {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.phase
}

// Boundary returns the live boundary, if the run has one.
func (g *Gate) Boundary() (Boundary, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.boundary == nil {
		return Boundary{}, false
	}
	return *g.boundary, true
}

func (g *Gate) enter(t *admission.Token) error {
	if err := g.adm.Check(t); err != nil {
		return err
	}
	if g.err != nil {
		return fmt.Errorf("%w: %v", ErrFailed, g.err)
	}
	return nil
}

// Enter opens a pass for the mapped caller id. The caller holds the guard
// and passes its token; a nested native call enters again with the same
// token, and its pass must be closed first. A Refuse caller is refused with
// ErrRefused, and the refusal is counted. In Bootstrap the pass does not
// observe: the native mutation proceeds without capture. In Recovery and
// Drain only a Startup caller, or a caller nested in a Startup pass, may
// enter.
func (g *Gate) Enter(t *admission.Token, id string) (*Pass, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.enter(t); err != nil {
		return nil, err
	}
	c, ok := g.callers[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q of %s", ErrUnmapped, id, g.client)
	}
	r := g.reached[id]
	switch c.Route {
	case Refuse:
		r.Refusals++
		g.reached[id] = r
		return nil, fmt.Errorf("%w: %s (%s)", ErrRefused, id, c.Refusal)
	case ReadOnly:
		return nil, fmt.Errorf("%w: read-only caller %s needs no pass", ErrInvalid, id)
	}
	// The phase changes only in GoLive, with no pass open. So in Recovery
	// and Drain the outermost open pass is always a Startup pass.
	if (g.phase == Recovery || g.phase == Drain) && c.Trigger != Startup && len(g.stack) == 0 {
		return nil, fmt.Errorf("%w: %s in %s", ErrPhase, id, g.phase)
	}
	r.Passes++
	g.reached[id] = r
	p := &Pass{gate: g, caller: c, token: t, observe: g.phase != Bootstrap, granted: map[Effect]bool{}}
	g.stack = append(g.stack, p)
	return p, nil
}

// Caller returns the mapped caller of the pass.
func (p *Pass) Caller() Caller { return p.caller }

// Observe reports whether the native mutation of the pass is captured: true
// unless the gate was in Bootstrap when the pass opened.
func (p *Pass) Observe() bool { return p.observe }

func (p *Pass) innermost() error {
	g := p.gate
	if len(g.stack) == 0 || g.stack[len(g.stack)-1] != p {
		return fmt.Errorf("%w: %s is not the innermost open pass", ErrOrder, p.caller.ID)
	}
	return nil
}

// Exit closes the pass. Passes close in reverse order of Enter.
func (p *Pass) Exit() error {
	g := p.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.adm.Check(p.token); err != nil {
		return err
	}
	if err := p.innermost(); err != nil {
		return err
	}
	g.stack = g.stack[:len(g.stack)-1]
	return nil
}

// Intend records a decision intent for the innermost observing pass. The
// kind must be one the caller may make. The intent itself is checked and
// recorded by the bridge.
func (p *Pass) Intend(bridge Intender, in decision.Intent) (decision.IntentRecord, error) {
	g := p.gate
	g.mu.Lock()
	if err := g.enter(p.token); err != nil {
		g.mu.Unlock()
		return decision.IntentRecord{}, err
	}
	if err := p.innermost(); err != nil {
		g.mu.Unlock()
		return decision.IntentRecord{}, err
	}
	if !p.observe {
		g.mu.Unlock()
		return decision.IntentRecord{}, fmt.Errorf("%w: %s does not observe in %s", ErrPhase, p.caller.ID,
			Bootstrap)
	}
	if !slices.Contains(p.caller.Kinds, in.Kind) {
		g.mu.Unlock()
		return decision.IntentRecord{}, fmt.Errorf("%w: %s may not make %s", ErrKind, p.caller.ID, in.Kind)
	}
	if bridge == nil {
		g.mu.Unlock()
		return decision.IntentRecord{}, fmt.Errorf("%w: missing bridge", ErrInvalid)
	}
	// The bridge writes and syncs the intent; the gate lock is not held
	// across that I/O, so Require from a native write path does not wait on
	// it. The guard token still serializes the patched callers.
	g.mu.Unlock()
	rec, err := bridge.Intend(p.token, in)
	if err != nil {
		return decision.IntentRecord{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.reached[p.caller.ID]
	r.Intents++
	g.reached[p.caller.ID] = r
	if effect, ok := kindEffects[in.Kind]; ok {
		p.granted[effect] = true
	}
	return rec, nil
}

// kindEffects maps the kinds of a Decision route to the effect they allow.
var kindEffects = map[decision.Kind]Effect{decision.Canonical: Canonical, decision.Finality: Finality}

// grant records that the innermost pass may write effect without an intent:
// the native setter rewrites an unchanged reference.
func (p *Pass) grant(effect Effect) error {
	g := p.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.enter(p.token); err != nil {
		return err
	}
	if err := p.innermost(); err != nil {
		return err
	}
	if !slices.Contains(p.caller.Kinds, decision.Finality) {
		return fmt.Errorf("%w: %s may not make %s", ErrKind, p.caller.ID, decision.Finality)
	}
	p.granted[effect] = true
	return nil
}

// Require is called by a native write choke point before a mutation with
// this effect. The innermost open pass must belong to a caller with the
// effect; otherwise the mutation bypassed the gate, the gate stops, and
// ErrBypass is returned so the native write fails closed.
func (g *Gate) Require(effect Effect) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return fmt.Errorf("%w: %v", ErrFailed, g.err)
	}
	if !slices.Contains(effects, effect) {
		return fmt.Errorf("%w: effect %q", ErrInvalid, effect)
	}
	if len(g.stack) == 0 {
		g.err = fmt.Errorf("%w: %s without an open pass", ErrBypass, effect)
		return g.err
	}
	top := g.stack[len(g.stack)-1]
	if !slices.Contains(top.caller.Effects, effect) {
		g.err = fmt.Errorf("%w: %s under %s", ErrBypass, effect, top.caller.ID)
		return g.err
	}
	// An observing Decision pass must record its intent before it mutates.
	if top.caller.Route == Decision && top.observe && !top.granted[effect] {
		g.err = fmt.Errorf("%w: %s under %s without an intent", ErrBypass, effect, top.caller.ID)
		return g.err
	}
	return nil
}

// GoLive records the live boundary H, if the run has none, and switches the
// bridge and the gate to Live. The caller holds the guard, has closed every
// pass (the current native mutations are finished) and passes the native head
// it read under the guard with its state root. That root must be durable in
// the native store (flushed through the hash provider and synced), so native
// startup repair cannot rewind below H. The boundary record is synced before
// the bridge goes live. A run that already has a boundary keeps it: a restart
// never sets a new H. Drain never goes live.
func (g *Gate) GoLive(t *admission.Token, head decision.Head, root string, roots decision.Roots,
	bridge LiveSwitcher) (Boundary, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.enter(t); err != nil {
		return Boundary{}, err
	}
	if bridge == nil || roots == nil {
		return Boundary{}, fmt.Errorf("%w: missing bridge or roots", ErrInvalid)
	}
	if len(g.stack) != 0 {
		return Boundary{}, fmt.Errorf("%w: GoLive under the open pass of %s", ErrOrder, g.stack[0].caller.ID)
	}
	switch g.phase {
	case Drain:
		return Boundary{}, fmt.Errorf("%w: GoLive in %s", ErrPhase, g.phase)
	case Live:
		return *g.boundary, nil
	}
	if g.boundary == nil {
		b := Boundary{NativeStoreID: g.storeID, Producer: g.producer, Head: head}
		digest, err := BoundaryDigest(b)
		if err != nil {
			return Boundary{}, err
		}
		if !digestPattern.MatchString(root) {
			return Boundary{}, fmt.Errorf("%w: root %q", ErrInvalid, root)
		}
		durable, err := roots.Durable(root)
		if err != nil {
			return Boundary{}, fmt.Errorf("%w: %s: %w", ErrRoot, root, err)
		}
		if !durable {
			return Boundary{}, fmt.Errorf("%w: %s", ErrRoot, root)
		}
		b.Digest = digest
		data, err := json.Marshal(b)
		if err != nil {
			return Boundary{}, err
		}
		if err := g.db.Put(g.key, data); err != nil {
			g.err = err
			return Boundary{}, err
		}
		if err := g.db.SyncKeyValue(); err != nil {
			g.err = err
			return Boundary{}, err
		}
		g.boundary = &b
		g.phase = Recovery
	}
	if err := bridge.GoLive(t); err != nil {
		return Boundary{}, err
	}
	g.phase = Live
	return *g.boundary, nil
}

// Coverage returns how often each caller of the client entered or was
// refused.
func (g *Gate) Coverage() map[string]Reach {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]Reach{}
	for id, r := range g.reached {
		out[id] = r
	}
	return out
}

// Gaps returns, in order, the callers of client in m that a run did not
// reach as required: an OBSERVATION caller without a pass, a DECISION caller
// without an intent recorded through its pass, and a REFUSE caller without a
// refusal. READ_ONLY callers need nothing. An empty result means no required
// caller was missed. A client block may add refusals that happen outside the
// gate (the profile check, a refused command) to coverage before the call.
func Gaps(m Map, client string, coverage map[string]Reach) []string {
	gaps := []string{}
	for _, c := range m.Callers {
		if c.Client != client {
			continue
		}
		r := coverage[c.ID]
		switch {
		case c.Route == Observation && r.Passes == 0:
			gaps = append(gaps, c.ID)
		case c.Route == Decision && r.Intents == 0:
			gaps = append(gaps, c.ID)
		case c.Route == Refuse && r.Refusals == 0:
			gaps = append(gaps, c.ID)
		}
	}
	sort.Strings(gaps)
	return gaps
}
