package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/protocol/compatibility"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// Maintenance tests (E06-08, AC-OPS-04-01/02): a configuration
// generation replacement changes only the YAML file, keeps the
// persistent state untouched, routes a destination change through a
// completed DRAIN, and rejects incompatible changes before any write.
// The mode gate and the version preflight follow the same contract.

// nextBinding returns the same destination one generation later with a
// changed stream and prefix (a D54 cutover).
func nextBinding(t *testing.T, b destination.Binding) destination.Binding {
	t.Helper()
	n := b
	n.Generation = b.Generation + 1
	n.Stream = "EXTRACTOR_ETH_V4"
	n.SubjectPrefix = "evm.v4.eth"
	digest, err := n.DescriptorDigestComputed()
	if err != nil {
		t.Fatalf("DescriptorDigestComputed: %v", err)
	}
	n.DescriptorDigest = digest
	return n
}

// liveSavedState builds the saved facts of a quiet live store running
// the given binding.
func liveSavedState(b destination.Binding) StateFacts {
	return StateFacts{
		HasState:      true,
		Profile:       Profile{Name: "ethereum-mainnet", Kind: NativeGeth},
		Role:          RoleLive,
		SchemaVersion: 1,
		Binding:       b,
		Mode:          compatibility.ModeLive,
	}
}

func TestPlanRestartOnlyLogLevel(t *testing.T) {
	b := testBinding(t)
	_, oldPath := setupLiveDir(t, validLiveYAML, b)
	old, err := CheckLive(oldPath)
	if err != nil {
		t.Fatalf("CheckLive old: %v", err)
	}
	dir := t.TempDir()
	writeBindingFile(t, dir, b)
	writeTLSMaterial(t, dir)
	nextBody := setField(validLiveYAML, "log_level", "debug")
	nextPath := writeYAML(t, dir, nextBody)
	next, err := CheckLive(nextPath)
	if err != nil {
		t.Fatalf("CheckLive next: %v", err)
	}

	res := PlanReplacement(old, next, liveSavedState(b))
	if res.Class != ReplaceRestartOnly {
		t.Fatalf("class = %s, want restart-only (%v)", res.Class, res.Reasons)
	}
	if res.FrameLimitHalt {
		t.Fatalf("frame limit flagged without a change")
	}
	if len(res.Reasons) == 0 {
		t.Fatalf("no reasons listed")
	}
}

func TestApplyRestartOnlyWritesOnlyTheYAML(t *testing.T) {
	b := testBinding(t)
	dir, oldPath := setupLiveDir(t, validLiveYAML, b)
	old, err := CheckLive(oldPath)
	if err != nil {
		t.Fatalf("CheckLive old: %v", err)
	}

	// Persistent state marker inside the live_dir: a replacement must
	// not touch it.
	liveDir := filepath.Clean(filepath.Join(dir, "..", "wal", "eth"))
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatalf("mkdir live dir: %v", err)
	}
	marker := filepath.Join(liveDir, "STATE.bin")
	if err := os.WriteFile(marker, []byte("durable state"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	nextBody := setField(validLiveYAML, "log_level", "debug")
	nextPath := writeYAML(t, dir, nextBody)
	next, err := CheckLive(nextPath)
	if err != nil {
		t.Fatalf("CheckLive next: %v", err)
	}
	// The apply must write the next generation into the same file the
	// next config names; use it directly.
	nextBytes := []byte(nextBody)
	res, err := ApplyReplacement(old, next, liveSavedState(b), ApplyOptions{NextBytes: nextBytes})
	if err != nil {
		t.Fatalf("ApplyReplacement: %v (%v)", err, res.Reasons)
	}
	if res.Class != ReplaceRestartOnly {
		t.Fatalf("class = %s", res.Class)
	}
	got, err := os.ReadFile(nextPath)
	if err != nil || string(got) != nextBody {
		t.Fatalf("the next YAML was not applied")
	}
	// The state marker survives untouched.
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "durable state" {
		t.Fatalf("persistent state was overwritten: %q %v", data, err)
	}
	// Nothing else appeared inside the persistent root.
	entries, err := os.ReadDir(liveDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("persistent root gained entries: %v", entries)
	}
}

func TestApplyRefusesToWriteInsidePersistentRoot(t *testing.T) {
	b := testBinding(t)
	dir, oldPath := setupLiveDir(t, validLiveYAML, b)
	old, err := CheckLive(oldPath)
	if err != nil {
		t.Fatalf("CheckLive old: %v", err)
	}
	liveDir := filepath.Clean(filepath.Join(dir, "..", "wal", "eth"))
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatalf("mkdir live dir: %v", err)
	}
	// The inside config needs its own referenced files to stay valid.
	writeBindingFile(t, liveDir, b)
	writeTLSMaterial(t, liveDir)
	// A next config whose YAML path sits inside the persistent root.
	inside := filepath.Join(liveDir, "network.yml")
	if err := os.WriteFile(inside, []byte(validLiveYAML), 0o644); err != nil {
		t.Fatalf("write inside yaml: %v", err)
	}
	next, err := CheckLive(inside)
	if err != nil {
		t.Fatalf("CheckLive inside: %v", err)
	}
	_, err = ApplyReplacement(old, next, liveSavedState(b), ApplyOptions{NextBytes: []byte(validLiveYAML)})
	if err == nil || !errors.Is(err, ErrReplacementBlocked) {
		t.Fatalf("write inside the persistent root accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(liveDir, "."+filepath.Base(inside)+".tmp")); err == nil {
		t.Fatalf("a temporary file leaked into the persistent root")
	}
}

func TestPlanEndpointChangeIsRestartOnly(t *testing.T) {
	b := testBinding(t)
	_, oldPath := setupLiveDir(t, validLiveYAML, b)
	old, err := CheckLive(oldPath)
	if err != nil {
		t.Fatalf("CheckLive old: %v", err)
	}
	dir := t.TempDir()
	writeBindingFile(t, dir, b)
	writeTLSMaterial(t, dir)
	nextBody := setField(validLiveYAML, "gateway_endpoint", "https://gw2.internal:50051")
	next, err := CheckLive(writeYAML(t, dir, nextBody))
	if err != nil {
		t.Fatalf("CheckLive next: %v", err)
	}
	res := PlanReplacement(old, next, liveSavedState(b))
	if res.Class != ReplaceRestartOnly {
		t.Fatalf("class = %s (%v)", res.Class, res.Reasons)
	}
	found := false
	for _, r := range res.Reasons {
		if strings.Contains(r, "gateway_endpoint") {
			found = true
		}
	}
	if !found {
		t.Fatalf("endpoint change not listed: %v", res.Reasons)
	}
}

func TestPlanDestinationCutoverNeedsDrain(t *testing.T) {
	b := testBinding(t)
	n := nextBinding(t, b)

	_, oldPath := setupLiveDir(t, validLiveYAML, b)
	old, err := CheckLive(oldPath)
	if err != nil {
		t.Fatalf("CheckLive old: %v", err)
	}
	dir := t.TempDir()
	writeTLSMaterial(t, dir)
	writeBindingFile(t, dir, n)
	next, err := CheckLive(writeYAML(t, dir, validLiveYAML))
	if err != nil {
		t.Fatalf("CheckLive next: %v", err)
	}

	saved := liveSavedState(b)
	res := PlanReplacement(old, next, saved)
	if res.Class != ReplaceNeedsDrain {
		t.Fatalf("class = %s (%v)", res.Class, res.Reasons)
	}
	// Applying before the drain completes is rejected and writes
	// nothing.
	_, err = ApplyReplacement(old, next, saved, ApplyOptions{NextBytes: []byte(validLiveYAML)})
	if err == nil || !errors.Is(err, ErrReplacementBlocked) {
		t.Fatalf("cutover before DRAINED accepted: %v", err)
	}

	// After the durable drain the same plan applies.
	saved.Mode = compatibility.ModeDrained
	saved.PendingObligations = 0
	saved.MaxDelivery = compatibility.StateNone
	res, err = ApplyReplacement(old, next, saved, ApplyOptions{NextBytes: []byte(validLiveYAML)})
	if err != nil {
		t.Fatalf("cutover after DRAINED rejected: %v (%v)", err, res.Reasons)
	}
	if res.Class != ReplaceNeedsDrain {
		t.Fatalf("class = %s", res.Class)
	}
}

func TestPlanIncompatibleChanges(t *testing.T) {
	b := testBinding(t)

	makeOld := func(t *testing.T) *LiveConfig {
		_, path := setupLiveDir(t, validLiveYAML, b)
		c, err := CheckLive(path)
		if err != nil {
			t.Fatalf("CheckLive old: %v", err)
		}
		return c
	}
	old := makeOld(t)
	saved := liveSavedState(b)

	t.Run("profile", func(t *testing.T) {
		dir := t.TempDir()
		writeBindingFile(t, dir, b)
		writeTLSMaterial(t, dir)
		body := setField(validLiveYAML, "profile", "bsc-mainnet")
		body = setField(body, "kind", "bsc")
		next, err := CheckLive(writeYAML(t, dir, body))
		if err != nil {
			t.Fatalf("CheckLive next: %v", err)
		}
		res := PlanReplacement(old, next, saved)
		if res.Class != ReplaceIncompatible {
			t.Fatalf("profile change class = %s", res.Class)
		}
		_, err = ApplyReplacement(old, next, saved, ApplyOptions{NextBytes: []byte(body)})
		if err == nil {
			t.Fatalf("profile change applied")
		}
	})

	t.Run("role", func(t *testing.T) {
		dir := t.TempDir()
		writeBindingFile(t, dir, b)
		writeTLSMaterial(t, dir)
		next, err := CheckGateway(writeYAML(t, dir, validGatewayYAML))
		if err != nil {
			t.Fatalf("CheckGateway next: %v", err)
		}
		res := PlanReplacement(old, next, saved)
		if res.Class != ReplaceIncompatible {
			t.Fatalf("role change class = %s", res.Class)
		}
	})

	t.Run("schema-version", func(t *testing.T) {
		dir := t.TempDir()
		writeBindingFile(t, dir, b)
		writeTLSMaterial(t, dir)
		body := setField(validLiveYAML, "schema_version", "2")
		f, err := Parse([]byte(body))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		next := &LiveConfig{Path: writeYAML(t, dir, body), File: f, Profile: old.Profile, Binding: b}
		res := PlanReplacement(old, next, saved)
		if res.Class != ReplaceIncompatible {
			t.Fatalf("schema change class = %s", res.Class)
		}
	})

	t.Run("generation-jump", func(t *testing.T) {
		jump := b
		jump.Generation = b.Generation + 3
		jump.Stream = "EXTRACTOR_ETH_V9"
		digest, err := jump.DescriptorDigestComputed()
		if err != nil {
			t.Fatalf("digest: %v", err)
		}
		jump.DescriptorDigest = digest
		dir := t.TempDir()
		writeTLSMaterial(t, dir)
		writeBindingFile(t, dir, jump)
		next, err := CheckLive(writeYAML(t, dir, validLiveYAML))
		if err != nil {
			t.Fatalf("CheckLive next: %v", err)
		}
		res := PlanReplacement(old, next, saved)
		if res.Class != ReplaceIncompatible {
			t.Fatalf("generation jump class = %s (%v)", res.Class, res.Reasons)
		}
	})

	t.Run("different-destination", func(t *testing.T) {
		other := b
		var id proto.ID16
		for i := range id {
			id[i] = byte(0x0b)
		}
		other.DestinationID = id
		other.Generation = b.Generation + 1
		digest, err := other.DescriptorDigestComputed()
		if err != nil {
			t.Fatalf("digest: %v", err)
		}
		other.DescriptorDigest = digest
		dir := t.TempDir()
		writeTLSMaterial(t, dir)
		writeBindingFile(t, dir, other)
		next, err := CheckLive(writeYAML(t, dir, validLiveYAML))
		if err != nil {
			t.Fatalf("CheckLive next: %v", err)
		}
		res := PlanReplacement(old, next, saved)
		if res.Class != ReplaceIncompatible {
			t.Fatalf("different destination class = %s", res.Class)
		}
	})

	t.Run("different-network", func(t *testing.T) {
		other := b
		var chain [32]byte
		chain[0] = 0x99
		other.Network.ChainID = chain
		other.Generation = b.Generation + 1
		digest, err := other.DescriptorDigestComputed()
		if err != nil {
			t.Fatalf("digest: %v", err)
		}
		other.DescriptorDigest = digest
		dir := t.TempDir()
		writeTLSMaterial(t, dir)
		writeBindingFile(t, dir, other)
		next, err := CheckLive(writeYAML(t, dir, validLiveYAML))
		if err != nil {
			t.Fatalf("CheckLive next: %v", err)
		}
		res := PlanReplacement(old, next, saved)
		if res.Class != ReplaceIncompatible {
			t.Fatalf("network change class = %s (%v)", res.Class, res.Reasons)
		}
	})

}

func TestPlanStateMismatch(t *testing.T) {
	b := testBinding(t)
	_, oldPath := setupLiveDir(t, validLiveYAML, b)
	old, err := CheckLive(oldPath)
	if err != nil {
		t.Fatalf("CheckLive old: %v", err)
	}
	// The saved binding is not the one the old generation loaded.
	saved := liveSavedState(nextBinding(t, b))
	res := PlanReplacement(old, old, saved)
	if res.Class != ReplaceIncompatible {
		t.Fatalf("state mismatch class = %s", res.Class)
	}
	found := false
	for _, r := range res.Reasons {
		if strings.Contains(r, "saved state") {
			found = true
		}
	}
	if !found {
		t.Fatalf("state mismatch not explained: %v", res.Reasons)
	}
}

func TestPlanFrameLimitHalt(t *testing.T) {
	b := testBinding(t)
	_, oldPath := setupGatewayDir(t, validGatewayYAML, b)
	old, err := CheckGateway(oldPath)
	if err != nil {
		t.Fatalf("CheckGateway old: %v", err)
	}
	dir := t.TempDir()
	writeBindingFile(t, dir, b)
	writeTLSMaterial(t, dir)
	nextBody := validGatewayYAML + "transport:\n  max_frame_bytes: 1048576\n"
	next, err := CheckGateway(writeYAML(t, dir, nextBody))
	if err != nil {
		t.Fatalf("CheckGateway next: %v", err)
	}
	saved := StateFacts{
		HasState: true, Profile: Profile{Name: "ethereum-mainnet", Kind: NativeGeth},
		Role: RoleGateway, SchemaVersion: 1, Binding: b,
		Mode: compatibility.ModeLive, LargestPendingFrame: 3 * 1024 * 1024,
	}
	res := PlanReplacement(old, next, saved)
	if res.Class != ReplaceRestartOnly {
		t.Fatalf("class = %s", res.Class)
	}
	if !res.FrameLimitHalt {
		t.Fatalf("frame limit halt not flagged: %v", res.Reasons)
	}
}

func TestModeTransition(t *testing.T) {
	cases := []struct {
		saved    compatibility.Mode
		request  ModeRequest
		wantMode compatibility.Mode
		wantErr  bool
		resumes  bool
		resume   bool
	}{
		{compatibility.ModeLive, RequestLive, compatibility.ModeLive, false, false, false},
		{compatibility.ModeLive, RequestDrain, compatibility.ModeDrain, false, false, false},
		{compatibility.ModeDrain, RequestDrain, compatibility.ModeDrain, false, true, false},
		{compatibility.ModeDrain, RequestLive, compatibility.ModeUnset, true, false, false},
		{compatibility.ModeDrained, RequestDrain, compatibility.ModeDrained, false, false, false},
		{compatibility.ModeDrained, RequestLive, compatibility.ModeLive, false, false, true},
		{compatibility.ModeBlocked, RequestLive, compatibility.ModeUnset, true, false, false},
		{compatibility.ModeBlocked, RequestDrain, compatibility.ModeUnset, true, false, false},
		{compatibility.ModeUnset, RequestLive, compatibility.ModeUnset, true, false, false},
	}
	for _, tc := range cases {
		d, err := ModeTransition(tc.saved, tc.request)
		if tc.wantErr {
			if err == nil || !errors.Is(err, ErrModeConflict) {
				t.Fatalf("%s + %d: expected a conflict, got %v", tc.saved, tc.request, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s + %d: %v", tc.saved, tc.request, err)
		}
		if d.Mode != tc.wantMode {
			t.Fatalf("%s + %d: mode = %s, want %s", tc.saved, tc.request, d.Mode, tc.wantMode)
		}
		if d.ResumesSavedDrain != tc.resumes {
			t.Fatalf("%s + %d: resumes = %v", tc.saved, tc.request, d.ResumesSavedDrain)
		}
		if d.RequiresResume != tc.resume {
			t.Fatalf("%s + %d: requires resume = %v", tc.saved, tc.request, d.RequiresResume)
		}
	}
	if d, err := ModeTransition(compatibility.ModeDrained, RequestLive); err != nil {
		t.Fatalf("drained live: %v", err)
	} else if len(d.Requirements) != 4 {
		t.Fatalf("resume requirements = %v", d.Requirements)
	}
	// An unset request is rejected.
	if _, err := ModeTransition(compatibility.ModeLive, 0); err == nil {
		t.Fatalf("unset request accepted")
	}
}

// ver is a test encoding version for the preflight matrix.
type ver struct {
	id    compatibility.VersionID
	reads []compatibility.VersionID
	wr    bool
}

func (v ver) ID() compatibility.VersionID             { return v.id }
func (v ver) Reads(from compatibility.VersionID) bool { return from == v.id || contains(v.reads, from) }
func (v ver) WritesNew() bool                         { return v.wr }

func contains(list []compatibility.VersionID, id compatibility.VersionID) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

func TestPreflightVersions(t *testing.T) {
	cur, _ := compatibility.NewProtocolVersion("build-v2", []compatibility.VersionID{"build-v1"}, true)
	old, _ := compatibility.NewProtocolVersion("build-v1", nil, false)
	foreign, _ := compatibility.NewProtocolVersion("store-x", nil, true)
	m, err := compatibility.NewMatrix(cur, old, foreign)
	if err != nil {
		t.Fatalf("NewMatrix: %v", err)
	}
	policy := compatibility.NewPolicy(m)

	t.Run("fresh-store", func(t *testing.T) {
		if err := PreflightVersions(policy, StateFacts{}); err != nil {
			t.Fatalf("fresh store rejected: %v", err)
		}
	})

	t.Run("compatible-same-version", func(t *testing.T) {
		saved := liveSavedState(testBinding(t))
		saved.StoreVersion, saved.WireVersion = "build-v2", "build-v2"
		if err := PreflightVersions(policy, saved); err != nil {
			t.Fatalf("same version rejected: %v", err)
		}
	})

	t.Run("compatible-old-records", func(t *testing.T) {
		saved := liveSavedState(testBinding(t))
		saved.WireVersion = "build-v1"
		saved.StoreVersion = "build-v2"
		if err := PreflightVersions(policy, saved); err != nil {
			t.Fatalf("readable old version rejected: %v", err)
		}
	})

	t.Run("incompatible-store-format", func(t *testing.T) {
		saved := liveSavedState(testBinding(t))
		saved.StoreVersion = "store-x"
		err := PreflightVersions(policy, saved)
		if err == nil {
			t.Fatalf("incompatible store format accepted")
		}
		if Exit(err) != ExitUnsupported {
			t.Fatalf("exit = %d, want 4", Exit(err))
		}
	})

	t.Run("wire-upgrade-needs-drain", func(t *testing.T) {
		saved := liveSavedState(testBinding(t))
		saved.WireVersion = "store-x"
		saved.StoreVersion = "build-v2"
		err := PreflightVersions(policy, saved)
		if err == nil {
			t.Fatalf("incompatible wire upgrade accepted")
		}
		if Exit(err) != ExitState {
			t.Fatalf("exit = %d, want 3", Exit(err))
		}
		saved.Mode = compatibility.ModeDrained
		if err := PreflightVersions(policy, saved); err != nil {
			t.Fatalf("drained wire upgrade rejected: %v", err)
		}
	})

	t.Run("pending-unreadable", func(t *testing.T) {
		saved := liveSavedState(testBinding(t))
		saved.WireVersion = "build-v2"
		saved.StoreVersion = "store-x" // unreadable store with pending
		saved.PendingObligations = 2
		saved.MaxDelivery = compatibility.StateSealed
		err := PreflightVersions(policy, saved)
		if err == nil {
			t.Fatalf("unreadable pending accepted")
		}
		if Exit(err) != ExitUnsupported {
			t.Fatalf("exit = %d, want 4", Exit(err))
		}
	})

	t.Run("no-policy", func(t *testing.T) {
		saved := liveSavedState(testBinding(t))
		if err := PreflightVersions(nil, saved); err == nil {
			t.Fatalf("missing policy accepted")
		}
	})
}

func TestPersistentRoots(t *testing.T) {
	b := testBinding(t)
	_, path := setupLiveDir(t, validLiveYAML, b)
	c, err := CheckLive(path)
	if err != nil {
		t.Fatalf("CheckLive: %v", err)
	}
	roots := PersistentRoots(c)
	if len(roots) != 1 || !strings.HasSuffix(roots[0], "wal/eth") {
		t.Fatalf("live persistent roots = %v", roots)
	}
	_, gpath := setupGatewayDir(t, validGatewayYAML, b)
	g, err := CheckGateway(gpath)
	if err != nil {
		t.Fatalf("CheckGateway: %v", err)
	}
	roots = PersistentRoots(g)
	if len(roots) != 1 || !strings.HasSuffix(roots[0], "journal/eth") {
		t.Fatalf("gateway persistent roots = %v", roots)
	}
}

func TestReplacementClassNames(t *testing.T) {
	if ReplaceRestartOnly.String() != "restart-only" || ReplaceNeedsDrain.String() != "needs-drain" ||
		ReplaceIncompatible.String() != "incompatible" {
		t.Fatalf("class names drifted")
	}
}
