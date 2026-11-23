package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/protocol/compatibility"
)

// The E06-08 replacement and maintenance preflight: a configuration
// generation replacement changes only allowlisted files and applies
// after a restart of the component, never overwriting persistent state,
// keys or genesis data; the DRAIN/Resume mode gate of
// LIVE_CONFIG_SPEC ("Валидация и остановка"); and the version rules of
// the compatibility policy, where an incompatible persistent store
// format is rejected before any write and an incompatible wire/plan
// upgrade waits for a completed drain by the old build.
//
// Everything here is a read-only decision over validated configs and a
// read-only view of the saved state. The one write is ApplyReplacement,
// and it writes exactly the next YAML file after the plan passes.

// StateFacts is the read-only view of what the persistent owner saved:
// its identity, its binding generation, the durable producer mode and
// the version ids the store and the wire carry. A fresh store reports
// HasState false and every decision starts from LIVE defaults.
type StateFacts struct {
	HasState      bool
	Profile       Profile
	Role          Role
	SchemaVersion int
	Binding       destination.Binding

	StoreVersion compatibility.VersionID
	WireVersion  compatibility.VersionID

	Mode                compatibility.Mode
	PendingObligations  int
	MaxDelivery         compatibility.DeliveryState
	LargestPendingFrame uint64
	AdmissionGeneration uint64
}

// ReplacementClass classifies a configuration replacement.
type ReplacementClass uint8

// The replacement classes.
const (
	// ReplaceRestartOnly applies the new generation after a restart of
	// the component; no durable obligation changes.
	ReplaceRestartOnly ReplacementClass = iota + 1
	// ReplaceNeedsDrain is a destination cutover: the change is legal
	// only after the old binding finished a durable DRAIN, and then
	// through the switch and resume protocol (D54/D57/D59).
	ReplaceNeedsDrain
	// ReplaceIncompatible is rejected before any write; the saved state
	// keeps its previous configuration.
	ReplaceIncompatible
)

// String returns the class name.
func (c ReplacementClass) String() string {
	switch c {
	case ReplaceRestartOnly:
		return "restart-only"
	case ReplaceNeedsDrain:
		return "needs-drain"
	case ReplaceIncompatible:
		return "incompatible"
	}
	return fmt.Sprintf("class(%d)", int(c))
}

// ReplacementResult is the reviewed plan for one generation change.
type ReplacementResult struct {
	Class ReplacementClass
	// Reasons explain the class in operational terms.
	Reasons []string
	// FrameLimitHalt marks a lowered transport.max_frame_bytes below
	// the largest saved pending frame: D52 keeps the pending frames and
	// stops publication with an explicit configuration error until the
	// limit is sufficient again.
	FrameLimitHalt bool
}

// replacementView is the common projection of one validated role
// configuration. The two role views implement it.
type replacementView interface {
	replacementFacts() replacementFacts
}

// replacementFacts carries what a replacement decision needs from one
// loaded config.
type replacementFacts struct {
	path       string
	role       Role
	profile    Profile
	file       *File
	binding    destination.Binding
	liveDir    string
	gatewayDir string
}

// replacementFacts projects the live view.
func (c *LiveConfig) replacementFacts() replacementFacts {
	return replacementFacts{path: c.Path, role: RoleLive, profile: c.Profile, file: c.File,
		binding: c.Binding, liveDir: c.LiveDir}
}

// replacementFacts projects the gateway view.
func (c *GatewayConfig) replacementFacts() replacementFacts {
	return replacementFacts{path: c.Path, role: RoleGateway, profile: c.Profile, file: c.File,
		binding: c.Binding, gatewayDir: c.GatewayDir}
}

// ErrStateMismatch rejects a decision whose inputs disagree with the
// saved state: the caller passed the wrong old generation, or the state
// belongs to another owner.
var ErrStateMismatch = errors.New("config: the saved state does not match the loaded configuration")

// PlanReplacement reviews one generation change. old is the generation
// in force (the file the running process was started with), next is the
// desired generation, saved is the persistent owner's durable view.
//
// The rules, in order:
//
//  1. The schema version, the profile and the role never change for the
//     same store: the network cannot be switched by keeping the old
//     directory (LIVE_CONFIG_SPEC, "Состояние и запуск" step 3).
//  2. A changed binding digest is a destination cutover. It needs the
//     next binding generation of the same destination, the same network
//     identity, and a completed DRAIN before it applies (D54).
//  3. Everything else — endpoints, TLS files, credentials, polygon
//     sources, log level, frame limit — is restart-only: the
//     environment replaces the file and restarts the component.
//  4. A lowered frame limit below the largest saved pending frame is
//     flagged: pending frames survive and publication halts with an
//     explicit error (D52).
func PlanReplacement(old, next replacementView, saved StateFacts) ReplacementResult {
	o, n := old.replacementFacts(), next.replacementFacts()
	res := ReplacementResult{Class: ReplaceRestartOnly}

	if o.file.SchemaVersion != n.file.SchemaVersion {
		res.Class = ReplaceIncompatible
		res.Reasons = append(res.Reasons, fmt.Sprintf("schema_version %d cannot change to %d for the same store",
			o.file.SchemaVersion, n.file.SchemaVersion))
		return res
	}
	if o.role != n.role {
		res.Class = ReplaceIncompatible
		res.Reasons = append(res.Reasons, fmt.Sprintf("role %s cannot change to %s for the same store",
			o.role, n.role))
		return res
	}
	if o.profile.Name != n.profile.Name {
		res.Class = ReplaceIncompatible
		res.Reasons = append(res.Reasons, fmt.Sprintf("network profile %s cannot change to %s while keeping the store directory",
			o.profile.Name, n.profile.Name))
		return res
	}
	if saved.HasState {
		if saved.SchemaVersion != 0 && saved.SchemaVersion != o.file.SchemaVersion {
			res.Class = ReplaceIncompatible
			res.Reasons = append(res.Reasons, ErrStateMismatch.Error()+": saved schema version differs from the loaded generation")
			return res
		}
		if saved.Profile.Name != "" && saved.Profile.Name != o.profile.Name {
			res.Class = ReplaceIncompatible
			res.Reasons = append(res.Reasons, ErrStateMismatch.Error()+": saved profile differs from the loaded generation")
			return res
		}
		if saved.Role != "" && saved.Role != o.role {
			res.Class = ReplaceIncompatible
			res.Reasons = append(res.Reasons, ErrStateMismatch.Error()+": saved role differs from the loaded generation")
			return res
		}
		if (saved.Binding != destination.Binding{}) && saved.Binding.DescriptorDigest != o.binding.DescriptorDigest {
			res.Class = ReplaceIncompatible
			res.Reasons = append(res.Reasons, ErrStateMismatch.Error()+": saved binding digest differs from the loaded generation")
			return res
		}
	}

	// The binding comparison decides restart-only versus cutover.
	if o.binding.DescriptorDigest != n.binding.DescriptorDigest {
		switch {
		case o.binding.Network != n.binding.Network:
			res.Class = ReplaceIncompatible
			res.Reasons = append(res.Reasons, "the binding network identity never changes for the same store")
		case o.binding.DestinationID != n.binding.DestinationID:
			res.Class = ReplaceIncompatible
			res.Reasons = append(res.Reasons, "a different destination_id is a different destination; a cutover keeps the destination_id and bumps the generation")
		case n.binding.Generation != o.binding.Generation+1:
			res.Class = ReplaceIncompatible
			res.Reasons = append(res.Reasons, fmt.Sprintf("binding generation must advance one step at a time, got %d after %d",
				n.binding.Generation, o.binding.Generation))
		default:
			res.Class = ReplaceNeedsDrain
			res.Reasons = append(res.Reasons, fmt.Sprintf("the destination changes from generation %d to %d: drain the old binding to DRAINED, replace the descriptor of both roles and resume",
				o.binding.Generation, n.binding.Generation))
			if saved.HasState && saved.Mode != compatibility.ModeDrained {
				res.Reasons = append(res.Reasons, fmt.Sprintf("the saved mode is %s, not DRAINED; the cutover is rejected until the old obligations finish", saved.Mode))
			}
		}
		if res.Class == ReplaceIncompatible {
			return res
		}
	}

	// Restart-only differences are listed for the operator; none of
	// them touches durable obligations.
	for _, d := range restartScopedDiffs(o, n) {
		res.Reasons = append(res.Reasons, d)
	}

	// The frame-limit rule of D52.
	if n.file.Transport.MaxFrameBytes != nil && saved.LargestPendingFrame != 0 &&
		*n.file.Transport.MaxFrameBytes < saved.LargestPendingFrame {
		res.FrameLimitHalt = true
		res.Reasons = append(res.Reasons, fmt.Sprintf("transport.max_frame_bytes %d is below the largest saved pending frame %d: pending frames are kept and publication stops with an explicit configuration error",
			*n.file.Transport.MaxFrameBytes, saved.LargestPendingFrame))
	}
	if res.Class == ReplaceRestartOnly && len(res.Reasons) == 0 {
		res.Reasons = append(res.Reasons, "no effective difference")
	}
	return res
}

// restartScopedDiffs lists the changed restart-scoped fields.
func restartScopedDiffs(o, n replacementFacts) []string {
	var out []string
	if o.file.Delivery != nil && n.file.Delivery != nil &&
		o.file.Delivery.GatewayEndpoint != n.file.Delivery.GatewayEndpoint {
		out = append(out, "delivery.gateway_endpoint changes; the same destination is a new address, verified online at startup")
	}
	if o.file.Runtime.LogLevel != n.file.Runtime.LogLevel {
		out = append(out, "runtime.log_level changes")
	}
	if o.file.Delivery != nil && n.file.Delivery != nil && o.file.Delivery.TLS != n.file.Delivery.TLS {
		out = append(out, "delivery.tls changes")
	}
	if o.file.Gateway != nil && n.file.Gateway != nil {
		if o.file.Gateway.Listen != n.file.Gateway.Listen {
			out = append(out, "gateway.listen changes")
		}
		if o.file.Gateway.TLS != n.file.Gateway.TLS {
			out = append(out, "gateway.tls changes")
		}
		if o.file.Gateway.NATS.Server != n.file.Gateway.NATS.Server {
			out = append(out, "gateway.nats.server changes; the account, stream and subjects stay verified by the D77 target check")
		}
		if o.file.Gateway.NATS.TLS != n.file.Gateway.NATS.TLS {
			out = append(out, "gateway.nats.tls changes")
		}
		if o.file.Gateway.NATS.CredentialsFile != n.file.Gateway.NATS.CredentialsFile {
			out = append(out, "gateway.nats.credentials_file changes")
		}
	}
	if !frameLimitEqual(o.file.Transport.MaxFrameBytes, n.file.Transport.MaxFrameBytes) {
		out = append(out, "transport.max_frame_bytes changes; the new limit applies to new publication plans only")
	}
	if o.file.Finality != nil && n.file.Finality != nil && o.file.Finality.Polygon != nil && n.file.Finality.Polygon != nil &&
		*o.file.Finality.Polygon != *n.file.Finality.Polygon {
		out = append(out, "finality.polygon changes; the adapter compares online responses against the configured values")
	}
	return out
}

// frameLimitEqual compares two optional frame limits.
func frameLimitEqual(a, b *uint64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ErrReplacementBlocked rejects an apply whose plan did not pass. It
// carries the busy-or-incompatible-state exit class (LIVE_CONFIG_SPEC:
// exit code 3).
var ErrReplacementBlocked = &Error{Field: "replacement", Code: ExitState, Reason: "the replacement is blocked"}

// ApplyOptions parameterizes ApplyReplacement. Write persists one file;
// the default writes through a same-directory temporary file and a
// rename, so a crash never leaves a half-written YAML.
type ApplyOptions struct {
	Write func(path string, data []byte) error
	// NextBytes are the YAML bytes of the next generation. When empty
	// the caller must pass them; an apply never renders state it was
	// not given.
	NextBytes []byte
}

// defaultWrite persists one file atomically.
func defaultWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// PersistentRoots lists the directories a configuration replacement
// must never write: the role's store, whose state, keys and genesis
// data survive every generation change (AC-OPS-04-01).
func PersistentRoots(c replacementView) []string {
	f := c.replacementFacts()
	var out []string
	if f.liveDir != "" {
		out = append(out, f.liveDir)
	}
	if f.gatewayDir != "" {
		out = append(out, f.gatewayDir)
	}
	return out
}

// ApplyReplacement plans the generation change and, when the plan is
// applicable, writes exactly the next YAML file. An incompatible plan,
// or a cutover whose old obligations have not finished, writes nothing.
// The persistent roots of the old generation are never written, and
// nothing outside the YAML path is touched.
func ApplyReplacement(old, next replacementView, saved StateFacts, opts ApplyOptions) (ReplacementResult, error) {
	plan := PlanReplacement(old, next, saved)
	switch plan.Class {
	case ReplaceIncompatible:
		return plan, fmt.Errorf("%w: %s", ErrReplacementBlocked, strings.Join(plan.Reasons, "; "))
	case ReplaceNeedsDrain:
		if saved.HasState && saved.Mode != compatibility.ModeDrained {
			return plan, fmt.Errorf("%w: the destination changes while the mode is %s, not DRAINED", ErrReplacementBlocked, saved.Mode)
		}
	}

	n := next.replacementFacts()
	for _, root := range PersistentRoots(old) {
		if n.path != "" && containsPath(resolveSymlinks(root), resolveSymlinks(n.path)) {
			return plan, fmt.Errorf("%w: the next configuration file %s sits inside the persistent root %s",
				ErrReplacementBlocked, n.path, root)
		}
	}
	if len(opts.NextBytes) == 0 {
		return plan, fmt.Errorf("%w: the next generation bytes are empty; an apply never renders state it was not given",
			ErrReplacementBlocked)
	}

	write := opts.Write
	if write == nil {
		write = defaultWrite
	}
	if err := write(n.path, opts.NextBytes); err != nil {
		return plan, err
	}
	return plan, nil
}

// ModeRequest is the explicit mode argument of the native build:
// --extractor-mode=live or --extractor-mode=drain.
type ModeRequest uint8

// The mode requests.
const (
	RequestLive ModeRequest = iota + 1
	RequestDrain
)

// ModeDecision is the preflight verdict of one mode request against
// the durable mode.
type ModeDecision struct {
	// Mode is the mode the process runs in after the decision.
	Mode compatibility.Mode
	// ResumesSavedDrain marks the crash path: an unfinished DRAIN is
	// resumed in DRAIN, never flipped back to LIVE silently.
	ResumesSavedDrain bool
	// RequiresResume marks the only path back to LIVE after DRAINED:
	// an explicit live request plus the seal, the applicable switch and
	// the resume of the next admission generation, committed through
	// the maintenance engine before any new admission.
	RequiresResume bool
	Requirements   []string
}

// ErrModeConflict rejects a mode request the durable mode forbids. It
// carries the busy-or-incompatible-state exit class (LIVE_CONFIG_SPEC:
// exit code 3).
var ErrModeConflict = &Error{Field: "mode", Code: ExitState, Reason: "the mode request conflicts with the durable mode"}

// ModeTransition decides one explicit mode request against the saved
// mode (LIVE_CONFIG_SPEC, "Валидация и остановка"). A new store starts
// in LIVE; an interrupted DRAIN resumes; a live request against an
// unfinished DRAIN is rejected; and LIVE after DRAINED goes through the
// explicit resume requirements instead of a silent flip.
func ModeTransition(saved compatibility.Mode, request ModeRequest) (ModeDecision, error) {
	if saved == compatibility.ModeUnset {
		return ModeDecision{}, fmt.Errorf("%w: the saved mode is unknown", ErrModeConflict)
	}
	switch request {
	case RequestDrain:
		switch saved {
		case compatibility.ModeLive:
			return ModeDecision{Mode: compatibility.ModeDrain,
				Requirements: []string{"finish every saved obligation, then seal the drain"}}, nil
		case compatibility.ModeDrain:
			// The crash path: a stored unfinished DRAIN resumes.
			return ModeDecision{Mode: compatibility.ModeDrain, ResumesSavedDrain: true}, nil
		case compatibility.ModeDrained:
			return ModeDecision{Mode: compatibility.ModeDrained}, nil
		case compatibility.ModeBlocked:
			return ModeDecision{}, fmt.Errorf("%w: the store is BLOCKED; clear the block before changing the mode", ErrModeConflict)
		}
	case RequestLive:
		switch saved {
		case compatibility.ModeLive:
			return ModeDecision{Mode: compatibility.ModeLive}, nil
		case compatibility.ModeDrain:
			return ModeDecision{}, fmt.Errorf("%w: an unfinished DRAIN is resumed in DRAIN; an explicit live request is rejected until it completes", ErrModeConflict)
		case compatibility.ModeDrained:
			return ModeDecision{Mode: compatibility.ModeLive, RequiresResume: true,
				Requirements: []string{
					"explicit mode=live",
					"the seal of the drained generation",
					"the applicable switch commit",
					"the resume ack of the next admission generation",
				}}, nil
		case compatibility.ModeBlocked:
			return ModeDecision{}, fmt.Errorf("%w: the store is BLOCKED; clear the block before changing the mode", ErrModeConflict)
		}
	default:
		return ModeDecision{}, fmt.Errorf("%w: the request is unset", ErrModeConflict)
	}
	return ModeDecision{}, fmt.Errorf("%w: unknown saved mode %s", ErrModeConflict, saved)
}

// PreflightVersions applies the upgrade preflight of the compatibility
// policy to the saved versions: pending obligations must stay readable
// and replayable, the persistent store format must never change
// incompatibly (rejected before any write, whatever the drain state),
// and an incompatible wire/plan upgrade requires the DRAINED mode of
// the old build (LIVE_CONFIG_SPEC, "Валидация и остановка"; CONTEXT
// "saved-state compatibility").
//
// A fresh store has nothing to check. The returned error carries the
// exit-code class: ExitUnsupported for a store the build cannot open,
// ExitState for a timing the durable mode forbids.
func PreflightVersions(policy *compatibility.Policy, saved StateFacts) error {
	if !saved.HasState {
		return nil
	}
	if policy == nil {
		return &Error{Field: "versions", Code: ExitFatal, Reason: "no compatibility policy was provided"}
	}
	current := policy.Matrix().Current()
	if saved.StoreVersion != "" {
		if err := preflightOne(policy, saved, saved.StoreVersion, current, compatibility.ClaimStoreFormat); err != nil {
			return err
		}
	}
	if saved.WireVersion != "" {
		if err := preflightOne(policy, saved, saved.WireVersion, current, compatibility.ClaimWirePlan); err != nil {
			return err
		}
	}
	return nil
}

// preflightOne runs one claim-family preflight and classifies its
// failure.
func preflightOne(policy *compatibility.Policy, saved StateFacts, from, target compatibility.VersionID, claim compatibility.ClaimType) error {
	d := compatibility.DurableState{
		Version:     from,
		Mode:        saved.Mode,
		Pending:     saved.PendingObligations,
		MaxDelivery: saved.MaxDelivery,
	}
	if saved.PendingObligations > 0 {
		if err := policy.ContinuePending(d); err != nil {
			return &Error{Field: "versions", Code: ExitUnsupported,
				Reason: fmt.Sprintf("pending obligations of %s cannot be continued by this build (%s); finish them with the old build", from, err)}
		}
	}
	err := policy.PreflightTransition(d, target, claim)
	if err == nil {
		return nil
	}
	var blocked *compatibility.ErrTransitionBlocked
	if errors.As(err, &blocked) {
		return &Error{Field: "versions", Code: ExitState,
			Reason: fmt.Sprintf("the %s upgrade to %s waits for the drain rules: %v", claim, target, err)}
	}
	return &Error{Field: "versions", Code: ExitUnsupported,
		Reason: fmt.Sprintf("the %s upgrade from %s to %s is rejected before any write: %v", claim, from, target, err)}
}
