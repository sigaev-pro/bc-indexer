// E07-09.requirement_verdict (#77): the comprehensive requirement
// verdict. Every parent requirement of ACCEPTANCE_REQUIREMENTS.md — the
// full 63-row registry — is checked for its address across the merged
// E01–E06 episodes: the requirement exists in the registry exactly once
// with its owning episode, its REQ/AC row of TASK_DECOMPOSITION maps to
// defined child tasks, its evidence owners exist in the repository, an
// acceptance suite of this module exercises it, and the runtime probes
// observe offline what the acceptance environment can observe. The
// verdict matrix is printed in full; a single missing, extra or failed
// row fails the gate (TASK_DECOMPOSITION E07-09.requirement_verdict; EVID;
// REQ-DEV-01, REQ-DEV-04, REQ-DEV-05).
package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/adapters/maintenance"
	"github.com/sigaev-pro/bc-indexer/config"
	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
	lifecycle "github.com/sigaev-pro/bc-indexer/test/lifecycle_validator"
)

// acceptanceSuitesOf pins the suite labels the registry references to the
// real test functions of this module: a label exists only if its tests
// compile here. It is built inside the verdict test so the
// requirement_verdict row can reference the running gate itself without a
// package-level initialization cycle.
func acceptanceSuitesOf() map[string][]func(*testing.T) {
	return map[string][]func(*testing.T){
		"e2e": {TestE2EFullPipelinePerNetwork, TestE2ECrashAtEveryBoundaryRecovers,
			TestE2EReorgRemovalSurvivesPublish},
		"scope":               {TestScopeNoDataOutsideCaptureWindow, TestScopeNoInventedTransactions, TestScopeMetadataIsNotACompletenessClaim},
		"runbook":             {TestRunbookStartupTableMatchesImplementation, TestRunbookDocumentedProceduresRun, TestRunbookDiagnosableStatesAreObservable},
		"isolation":           {TestIsolationAddNetworkLeavesNeighborsUntouched, TestIsolationStopAndRestartOneNetwork, TestIsolationNoSharedWALWriters, TestIsolationConcurrentNetworksDeliverIndependently},
		"nats_acceptance":     {TestNatsAcceptanceFinalRecordsPerNetwork, TestNatsAcceptanceRejectsLifecycleViolations},
		"requirement_verdict": {TestE07RequirementVerdictMatrix},
	}
}

// e07ChildSuites maps every mandatory E07 child of the decomposition to
// its owning suite label.
var e07ChildSuites = map[string]string{
	"task-e07-02-e2e":                 "e2e",
	"task-e07-02-scope":               "scope",
	"task-e07-03-runbook":             "runbook",
	"task-e07-08-isolation":           "isolation",
	"task-e07-09-nats_acceptance":     "nats_acceptance",
	"task-e07-09-requirement_verdict": "requirement_verdict",
}

// TestE07RequirementVerdictMatrix is the final gate check: the complete
// registry against the live documents, repository and runtime.
func TestE07RequirementVerdictMatrix(t *testing.T) {
	acceptanceSuites := acceptanceSuitesOf()
	root := repoRoot(t)
	requirements := readRepoFile(t, root, ".project/docs/ACCEPTANCE_REQUIREMENTS.md")
	decomposition := readRepoFile(t, root, ".project/docs/TASK_DECOMPOSITION.md")
	testingContract := readRepoFile(t, root, ".project/docs/TESTING_CONTRACT.md")

	// 1. The registry and the requirements document agree exactly.
	documented := uniqueREQIDs(t, requirements)
	if len(documented) != 63 {
		t.Fatalf("ACCEPTANCE_REQUIREMENTS.md carries %d parent requirements, want 63", len(documented))
	}
	for id := range documented {
		if _, ok := requirementRegistry[id]; !ok {
			t.Fatalf("requirement %s is documented but absent from the verdict registry", id)
		}
	}
	for id := range requirementRegistry {
		if _, ok := documented[id]; !ok {
			t.Fatalf("requirement %s is registered but absent from ACCEPTANCE_REQUIREMENTS.md", id)
		}
	}

	// 2. Every requirement maps to defined child tasks of E01–E06/E07.
	taskRows := parseTaskRows(t, decomposition)
	definedAnchors := parseTaskAnchors(t, decomposition)
	covered := make(map[string]bool, len(requirementRegistry))
	for id := range requirementRegistry {
		rows, ok := taskRows[id]
		if !ok || len(rows) == 0 {
			t.Fatalf("requirement %s has no REQ/AC → task row in TASK_DECOMPOSITION.md", id)
		}
		for _, row := range rows {
			if _, defined := definedAnchors[row.anchor]; !defined {
				t.Fatalf("requirement %s references task %s (%s), which the decomposition does not define",
					id, row.task, row.anchor)
			}
		}
		covered[id] = true
	}

	// 3. Every E07 child of the decomposition is owned by a live suite.
	for anchor, suite := range e07ChildSuites {
		if _, defined := definedAnchors[anchor]; !defined {
			t.Fatalf("the decomposition lost the E07 child %s", anchor)
		}
		if _, live := acceptanceSuites[suite]; !live {
			t.Fatalf("the E07 child %s names suite %q, which has no tests in this module", anchor, suite)
		}
	}
	for anchor := range definedAnchors {
		if !strings.HasPrefix(anchor, "task-e07-") {
			continue
		}
		if _, owned := e07ChildSuites[anchor]; !owned {
			t.Fatalf("E07 child %s has no acceptance suite assignment", anchor)
		}
	}

	// 4. Every merged evidence owner exists in the repository.
	for id, row := range requirementRegistry {
		if len(row.Owners) == 0 {
			t.Fatalf("requirement %s lists no merged evidence owner", id)
		}
		for _, owner := range row.Owners {
			if _, err := os.Stat(filepath.Join(root, owner)); err != nil {
				t.Fatalf("requirement %s owner %s is missing from the repository", id, owner)
			}
		}
		for _, suite := range row.Suites {
			if _, live := acceptanceSuites[suite]; !live {
				t.Fatalf("requirement %s names suite %q, which has no tests in this module", id, suite)
			}
		}
	}

	// 5. The seven profiles of D82 exist in the closed registries.
	for _, profile := range []string{"ethereum-mainnet", "ethereum-sepolia", "ethereum-hoodi",
		"bsc-mainnet", "bsc-testnet", "polygon-pos-mainnet", "polygon-pos-amoy"} {
		if !strings.Contains(testingContract, profile) {
			t.Fatalf("TESTING_CONTRACT.md lost the profile %s", profile)
		}
	}

	// 6. The runtime probes: every registered probe exists and runs.
	probes := requirementProbes()
	ran := make(map[string]bool)
	for id, row := range requirementRegistry {
		if row.Probe == "" {
			continue
		}
		probe, ok := probes[row.Probe]
		if !ok {
			t.Fatalf("requirement %s names probe %q, which does not exist", id, row.Probe)
		}
		if !ran[row.Probe] {
			ran[row.Probe] = true
			t.Run("probe/"+row.Probe, probe)
		}
	}

	// 7. The verdict matrix, printed in full.
	keys := make([]string, 0, len(requirementRegistry))
	for id := range requirementRegistry {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	t.Logf("requirement verdict matrix: %d rows, %d probes, %d suites", len(keys), len(ran), len(acceptanceSuites))
	for _, id := range keys {
		row := requirementRegistry[id]
		verdict := "PASS"
		if row.Probe != "" && !ran[row.Probe] {
			verdict = "FAIL" // unreachable: a failed probe aborts the run
		}
		t.Logf("%s %-12s owners=%d suites=%v probe=%s -> %s", id, row.Episode,
			len(row.Owners), row.Suites, row.Probe, verdict)
	}
}

// taskRow is one REQ/AC → task row of the decomposition.
type taskRow struct {
	task   string
	anchor string
}

// reqIDPattern matches the parent requirement identifiers.
var reqIDPattern = regexp.MustCompile(`REQ-[A-Z]+-[0-9]+`)

// taskRowPattern matches a REQ/AC → task table row.
var taskRowPattern = regexp.MustCompile(`^\|\s*(REQ-[A-Z]+-[0-9]+)\s*\|[^|]*\|\s*\[([^\]]+)\]\(#(task-[a-z0-9_-]+)\)`)

// anchorPattern matches a child task definition anchor.
var anchorPattern = regexp.MustCompile(`<a id="(task-[a-z0-9_-]+)"></a>`)

// uniqueREQIDs extracts the parent requirement identifiers of a document.
func uniqueREQIDs(t *testing.T, doc string) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	for _, match := range reqIDPattern.FindAllString(doc, -1) {
		out[match] = true
	}
	return out
}

// parseTaskRows builds the requirement → task rows mapping.
func parseTaskRows(t *testing.T, doc string) map[string][]taskRow {
	t.Helper()
	out := make(map[string][]taskRow)
	for _, line := range strings.Split(doc, "\n") {
		match := taskRowPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		out[match[1]] = append(out[match[1]], taskRow{task: match[2], anchor: match[3]})
	}
	return out
}

// parseTaskAnchors collects the defined child task anchors.
func parseTaskAnchors(t *testing.T, doc string) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	for _, match := range anchorPattern.FindAllStringSubmatch(doc, -1) {
		out[match[1]] = true
	}
	if len(out) == 0 {
		t.Fatalf("TASK_DECOMPOSITION.md defines no child task anchors")
	}
	return out
}

// readRepoFile loads a repository file.
func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// repoRoot locates the repository root from this module's directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("cannot locate the module directory")
	}
	moduleDir := filepath.Dir(thisFile)
	root := filepath.Clean(filepath.Join(moduleDir, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.work")); err != nil {
		// No workspace file is required; the root is simply two levels up.
		_ = err
	}
	return root
}

// requirementProbes returns the runtime probes of the verdict: every one
// observes product behavior offline through the merged modules.
func requirementProbes() map[string]func(*testing.T) {
	return map[string]func(*testing.T){
		"profiles":               probeProfilesRegistry,
		"capture_window":         probeCaptureWindow,
		"empty_block":            probeEmptyBlock,
		"optional_scope":         probeOptionalScope,
		"record_roundtrip":       probeRecordRoundtrip,
		"bounded_frames":         probeBoundedFrames,
		"one_decision_one_batch": probeOneDecisionOneBatch,
		"lifecycle_oracle":       probeLifecycleOracle,
		"reorg_lifecycle":        probeReorgLifecycle,
		"finality_only":          probeFinalityOnly,
		"drain_resume":           probeDrainResume,
		"isolation_writers":      probeIsolationWriters,
		"episode_order":          probeEpisodeOrder,
		"independent_oracle":     probeIndependentOracle,
		"module_boundary":        probeModuleBoundary,
		"evidence_schemas":       probeEvidenceSchemas,
		"all_slots":              probeAllSlots,
		"config_strict":          probeConfigStrict,
		"compatibility":          probeCompatibility,
	}
}

// probeStack builds a minimal one-decision stack for a probe.
func probeStack(t *testing.T, slot networkSlot, windowStart, windowEnd uint64) (*acceptanceStack, *publisher.MemoryJetStream) {
	t.Helper()
	server, err := publisher.NewMemoryJetStream(publisher.StreamConfig{
		Name: slot.stream, Subjects: []string{slot.prefix + ".>"},
		MaxMsgSize: acceptanceBudget,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	return newStack(slot, server, windowStart, windowEnd), server
}

// probeProfilesRegistry observes REQ-SCP-01/REQ-OPS-10: the closed
// profile registry of seven identities and the rejected outsider.
func probeProfilesRegistry(t *testing.T) {
	want := []string{"ethereum-mainnet", "ethereum-sepolia", "ethereum-hoodi",
		"bsc-mainnet", "bsc-testnet", "polygon-pos-mainnet", "polygon-pos-amoy"}
	got := config.ProfileNames()
	if len(got) != len(want) {
		t.Fatalf("the closed profile registry lists %d profiles, want 7", len(got))
	}
	set := make(map[string]bool, len(got))
	for _, name := range got {
		set[name] = true
	}
	for _, name := range want {
		if !set[name] {
			t.Fatalf("the closed profile registry lost %s", name)
		}
		if _, ok := config.LookupProfile(name); !ok {
			t.Fatalf("LookupProfile rejected the registered %s", name)
		}
	}
	for _, outsider := range []string{"base-mainnet", "arbitrum-mainnet", "ethereum-goerli"} {
		if _, ok := config.LookupProfile(outsider); ok {
			t.Fatalf("LookupProfile accepted the unregistered %s", outsider)
		}
	}
	for _, family := range []profiles.Family{profiles.FamilyEth, profiles.FamilyBsc, profiles.FamilyBor} {
		if _, err := profiles.Default(family); err != nil {
			t.Fatalf("Default(%s): %v", family, err)
		}
	}
	if _, err := profiles.Default(profiles.Family(99)); err == nil {
		t.Fatalf("Default accepted an unknown family")
	}
}

// probeCaptureWindow observes REQ-SCP-03: history heights produce no
// obligation.
func probeCaptureWindow(t *testing.T) {
	h, _ := probeStack(t, networkSlots[0], 100, 110)
	if _, err := h.source.admitLive(fixtureBlock(99, 1, 0)); !errors.Is(err, ErrOutsideWindow) {
		t.Fatalf("history height admitted: %v", err)
	}
	if _, err := h.source.admitLive(fixtureBlock(111, 1, 0)); !errors.Is(err, ErrOutsideWindow) {
		t.Fatalf("height beyond the window admitted: %v", err)
	}
}

// probeEmptyBlock observes REQ-SCP-04/REQ-DAT-10: the empty block
// publishes its own complete record.
func probeEmptyBlock(t *testing.T) {
	slot := networkSlots[0]
	h, server := probeStack(t, slot, 100, 110)
	d, err := h.source.admitLive(emptyFixtureBlock(100))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	h.deliverDecision(t, server, d)
	report := verifyStream(t, server, slot, h.source)
	report.mustBeClean(t, slot)
	assertEmptyBlockMetadata(t, slot, report, d)
}

// probeOptionalScope observes REQ-SCP-05: the optional family scope is
// mandatory where the native source has it and default-OFF for ETH.
func probeOptionalScope(t *testing.T) {
	if got := slotOptionalEvents(slotByNameOrFatal(t, "ETH")); got != 0 {
		t.Fatalf("the ETH optional scope is not default-OFF: %d events", got)
	}
	for _, name := range []string{"BSC", "POLY"} {
		if got := slotOptionalEvents(slotByNameOrFatal(t, name)); got == 0 {
			t.Fatalf("the %s native optional scope produced no events", name)
		}
	}
}

// probeRecordRoundtrip observes REQ-CAP-05/REQ-DAT-01/02/07: a delivered
// record reassembles through the independent reader into the exact
// fixture tree.
func probeRecordRoundtrip(t *testing.T) {
	slot := networkSlots[1]
	h, server := probeStack(t, slot, 100, 110)
	d, err := h.source.admitLive(systemFixtureBlock(100, 3))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	h.deliverDecision(t, server, d)
	verifyStream(t, server, slot, h.source).mustBeClean(t, slot)
}

// probeBoundedFrames observes REQ-CAP-06/REQ-DLV-04: every emitted frame
// fits the transport budget, header included.
func probeBoundedFrames(t *testing.T) {
	h, _ := probeStack(t, networkSlots[0], 100, 110)
	d, err := h.source.admitLive(fixtureBlock(100, 6, 1))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	planned, err := h.planDecision(d)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	forms := map[proto.Form]int{}
	for _, frame := range planned.frames {
		if uint64(len(frame.Header))+uint64(len(frame.Payload)) > acceptanceBudget {
			t.Fatalf("frame %s exceeds the transport budget", frame.Key)
		}
		if frame.Key.Complete {
			forms[proto.FormComplete]++
		} else {
			forms[proto.FormPart]++
		}
	}
	if forms[proto.FormPart] == 0 {
		t.Fatalf("the large-blob corpus produced no PART frames")
	}
}

// probeOneDecisionOneBatch observes REQ-CAP-08/REQ-DLV-02/05: one native
// decision allocates exactly one batch, idempotently, and it retires.
func probeOneDecisionOneBatch(t *testing.T) {
	h, server := probeStack(t, networkSlots[2], 100, 110)
	var last uint64
	for i := 0; i < 3; i++ {
		d, err := h.source.admitLive(fixtureBlock(100+uint64(i), 2, 0))
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		h.deliverDecision(t, server, d)
		last = d.sequence
	}
	if got := h.mem.RetiredPrefix(); got != 3 {
		t.Fatalf("retired prefix %d, want one batch per decision (3)", got)
	}
	if _, alive := h.prod.Batch(last); alive {
		t.Fatalf("the last decision's batch is not retired")
	}
	batch, fresh, err := h.source.allocator.EnsureBatch(2)
	if err != nil || fresh {
		t.Fatalf("a repeated marker allocated a second batch: %v fresh=%v", err, fresh)
	}
	if _, _, seq := batch.Decode(); seq != 2 {
		t.Fatalf("the repeated marker moved the batch sequence to %d", seq)
	}
}

// probeLifecycleOracle observes REQ-DAT-05/REQ-LIF-03: the independent
// automaton accepts the legal revision order and rejects violations.
func probeLifecycleOracle(t *testing.T) {
	slot := networkSlots[0]
	h, _ := probeStack(t, slot, 100, 110)
	d, err := h.source.admitLive(fixtureBlock(100, 1, 0))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	descs, err := h.descriptors(d)
	if err != nil {
		t.Fatalf("descriptors: %v", err)
	}
	automaton := lifecycle.New()
	digestOf := func(i int) lifecycle.Digest {
		raw, err := descs[i].Digest()
		if err != nil {
			t.Fatalf("digest: %v", err)
		}
		return lifecycle.Digest(raw)
	}
	for i := range descs {
		if _, err := automaton.Apply(descs[i], digestOf(i)); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	// A zero-revision lifecycle event is invalid input.
	mutated := descs[0]
	mutated.Kind, mutated.Revision = semantic.KindStatus, 0
	if err := mutated.Validate(); err == nil {
		t.Fatalf("a zero-revision StatusChanged passed validation")
	}
}

// probeReorgLifecycle observes REQ-LIF-01/02: the reorg publishes the
// Removed events of the replaced application before the replacement.
func probeReorgLifecycle(t *testing.T) {
	slot := networkSlots[0]
	h, server := probeStack(t, slot, 100, 110)
	applied, err := h.source.admitLive(fixtureBlock(100, 2, 0))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	h.deliverDecision(t, server, applied)
	pair, err := h.source.reorgAt(100, reorgFixtureBlock(100, 1, 0))
	if err != nil {
		t.Fatalf("reorg: %v", err)
	}
	h.deliverDecision(t, server, pair[0])
	h.deliverDecision(t, server, pair[1])
	verifyStream(t, server, slot, h.source).mustBeClean(t, slot)
	assertReorgRoutes(t, server, h, pair[0], pair[1])
}

// probeFinalityOnly observes REQ-LIF-04/08: a finality-only decision
// advances the decision head, never the block head.
func probeFinalityOnly(t *testing.T) {
	h, _ := probeStack(t, networkSlots[0], 100, 110)
	if _, err := h.source.admitLive(fixtureBlock(100, 1, 0)); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if _, err := h.source.finalizeAt(100); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	decisionHead, blockHead := h.source.heads()
	if decisionHead != 2 || blockHead != 100 {
		t.Fatalf("heads (%d, %d): the finality-only decision moved the block head", decisionHead, blockHead)
	}
}

// probeDrainResume observes REQ-DLV-10/11 and REQ-OPS-08: the startup
// decision table of the drain mode.
func probeDrainResume(t *testing.T) {
	if action, err := startupDecision("live", "LIVE"); err != nil || action != "LIVE" {
		t.Fatalf("live over LIVE: %v err %v", action, err)
	}
	if action, err := startupDecision("live", "DRAINED"); err != nil || action != "RESUME" {
		t.Fatalf("live over DRAINED: %v err %v", action, err)
	}
	if action, err := startupDecision("drain", "DRAIN"); err != nil || action != "DRAIN" {
		t.Fatalf("drain over DRAIN: %v err %v", action, err)
	}
	if _, err := startupDecision("live", "DRAIN"); err == nil {
		t.Fatalf("live over an unfinished drain was accepted")
	}
}

// probeIsolationWriters observes REQ-OPS-02: the three networks own
// distinct writer identities.
func probeIsolationWriters(t *testing.T) {
	seen := make(map[proto.ID16]string)
	for _, slot := range networkSlots {
		if other, held := seen[slot.producer]; held {
			t.Fatalf("%s and %s share the writer identity", other, slot.name)
		}
		seen[slot.producer] = slot.name
	}
}

// probeEpisodeOrder observes REQ-DEV-01: the decomposition keeps the
// ordered episode sections and gates E01→E07.
func probeEpisodeOrder(t *testing.T) {
	doc := readRepoFile(t, repoRoot(t), ".project/docs/TASK_DECOMPOSITION.md")
	position := -1
	for episode := 1; episode <= 7; episode++ {
		heading := fmt.Sprintf("## E%02d", episode)
		at := strings.Index(doc, heading)
		if at < 0 {
			t.Fatalf("TASK_DECOMPOSITION.md lost the section %s", heading)
		}
		if at <= position {
			t.Fatalf("TASK_DECOMPOSITION.md orders %s out of sequence", heading)
		}
		position = at
		if !strings.Contains(doc, fmt.Sprintf("`G.E%02d`", episode)) {
			t.Fatalf("TASK_DECOMPOSITION.md lost the gate G.E%02d", episode)
		}
	}
}

// probeIndependentOracle observes REQ-DEV-02: the expected set is derived
// independently and the comparator fails on a mutation.
func probeIndependentOracle(t *testing.T) {
	slot := networkSlots[0]
	h, server := probeStack(t, slot, 100, 110)
	d, err := h.source.admitLive(fixtureBlock(100, 2, 0))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	h.deliverDecision(t, server, d)
	report := verifyStream(t, server, slot, h.source)
	report.mustBeClean(t, slot)
	expected, err := oracleRecords(slot, h.source)
	if err != nil {
		t.Fatalf("oracle: %v", err)
	}
	for id := range expected {
		delete(expected, id)
		break
	}
	if diff := compareRecords(slot, expected, readStream(t, server, slot)); len(diff.unexpected) != 1 {
		t.Fatalf("the independent comparator missed a dropped expectation: %+v", diff)
	}
}

// probeModuleBoundary observes REQ-DEV-03: no core, protocol or adapter
// module carries the NATS SDK; the NATS-facing code stays behind the
// gateway/deploy boundary.
func probeModuleBoundary(t *testing.T) {
	root := repoRoot(t)
	bounded := []string{"core", "protocol", "adapters", "gateway", "config"}
	for _, area := range bounded {
		err := filepath.WalkDir(filepath.Join(root, area), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || entry.Name() != "go.mod" {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(raw), "nats-io") {
				return fmt.Errorf("%s carries the NATS SDK outside the deploy boundary", path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%v", err)
		}
	}
	deploy := readRepoFile(t, root, "deploy/go.mod")
	if !strings.Contains(deploy, "nats-io") {
		t.Fatalf("the deploy boundary lost the NATS SDK wiring")
	}
}

// probeEvidenceSchemas observes REQ-DEV-04: the evidence schemas exist,
// parse and pin the profile registry.
func probeEvidenceSchemas(t *testing.T) {
	root := repoRoot(t)
	for _, schema := range []string{"case-registry.schema.json", "case-result.schema.json",
		"run-manifest.schema.json", "reader-boundary-manifest.schema.json"} {
		raw, err := os.ReadFile(filepath.Join(root, "test/evidence", schema))
		if err != nil {
			t.Fatalf("evidence schema %s: %v", schema, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("evidence schema %s does not parse: %v", schema, err)
		}
	}
	registryRaw, err := os.ReadFile(filepath.Join(root, "test/evidence/profile-registry.json"))
	if err != nil {
		t.Fatalf("profile registry: %v", err)
	}
	var registry struct {
		SchemaVersion int               `json:"schema_version"`
		Profiles      map[string]string `json:"profiles"`
	}
	if err := json.Unmarshal(registryRaw, &registry); err != nil {
		t.Fatalf("profile registry does not parse: %v", err)
	}
	if registry.SchemaVersion != 3 {
		t.Fatalf("the evidence registry pins schema version %d, want 3", registry.SchemaVersion)
	}
	if len(registry.Profiles) != 7 {
		t.Fatalf("the evidence profile registry lists %d profiles, want 7", len(registry.Profiles))
	}
}

// probeAllSlots observes REQ-DEV-05: every family slot passes a full
// deliver-and-verify cycle.
func probeAllSlots(t *testing.T) {
	for _, slot := range networkSlots {
		h, server := probeStack(t, slot, 100, 110)
		d, err := h.source.admitLive(fixtureBlock(100, 2, 1))
		if err != nil {
			t.Fatalf("%s: admit: %v", slot.name, err)
		}
		h.deliverDecision(t, server, d)
		verifyStream(t, server, slot, h.source).mustBeClean(t, slot)
	}
}

// probeConfigStrict observes REQ-OPS-01/04: the strict role YAML accepts
// the closed document and rejects unknown keys.
func probeConfigStrict(t *testing.T) {
	valid := "schema_version: 1\nnetwork:\n  profile: ethereum-mainnet\n"
	file, err := config.Parse([]byte(valid))
	if err != nil {
		t.Fatalf("the closed live document was rejected: %v", err)
	}
	if file.Network.Profile != "ethereum-mainnet" {
		t.Fatalf("profile = %s", file.Network.Profile)
	}
	if _, err := config.Parse([]byte("schema_version: 1\nbogus: 1\n")); err == nil {
		t.Fatalf("an unknown root key was accepted")
	}
	if _, err := config.Parse([]byte("network:\n  profile: [a, b]\n")); err == nil {
		t.Fatalf("a sequence where a scalar is declared was accepted")
	}
}

// probeCompatibility observes REQ-DLV-14: the compatibility module and
// its recovery proof exist as merged evidence.
func probeCompatibility(t *testing.T) {
	root := repoRoot(t)
	for _, evidence := range []string{"protocol/compatibility/matrix.go",
		"protocol/compatibility/policy.go", "test/compatibility_recovery/recovery_test.go"} {
		if _, err := os.Stat(filepath.Join(root, evidence)); err != nil {
			t.Fatalf("compatibility evidence %s is missing: %v", evidence, err)
		}
	}
}

// startupDecision answers the drain-mode startup table through the real
// implementation.
func startupDecision(flag, mode string) (string, error) {
	var durable delivery.ProducerMode
	switch mode {
	case "LIVE":
		durable = delivery.ModeLive
	case "BLOCKED":
		durable = delivery.ModeBlocked
	case "DRAIN":
		durable = delivery.ModeDrain
	case "DRAINED":
		durable = delivery.ModeDrained
	}
	action, err := maintenance.Startup(maintenance.StartupMode(flag), durable)
	if err != nil {
		return "", err
	}
	return action.String(), nil
}
