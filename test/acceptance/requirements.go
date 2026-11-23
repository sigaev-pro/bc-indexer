// The E07-09.requirement_verdict registry: every parent requirement of
// ACCEPTANCE_REQUIREMENTS.md with its owning episode, the merged evidence
// owners of E01–E06 and the acceptance suites that exercise it at runtime.
// The verdict test cross-checks this table against the actual documents
// and repository, and runs the runtime probes the acceptance environment
// can observe offline (TASK_DECOMPOSITION E07-09.requirement_verdict;
// IMPLEMENTATION_GATES EVID; REQ-DEV-04, REQ-DEV-05).
package acceptance

// requirementRow is one registry row of the final verdict matrix.
type requirementRow struct {
	// Episode lists the owning episodes of the requirement's AC work as
	// the requirements document assigns them (the этап column).
	Episode string
	// Owners lists the merged repository evidence of the owning tasks:
	// modules, harnesses, schemas and documents that exist on disk.
	Owners []string
	// Suites lists the acceptance suite labels of this module that
	// exercise the requirement at runtime.
	Suites []string
	// Probe names the runtime probe of the verdict test that observes the
	// requirement's behavior offline, or "" when only the structural
	// checks apply.
	Probe string
}

// requirementRegistry is the full 63-row matrix. The verdict test fails
// if it and ACCEPTANCE_REQUIREMENTS.md disagree by a single row.
var requirementRegistry = map[string]requirementRow{
	// Scope and result.
	"REQ-SCP-01": {Episode: "E01+E06", Owners: []string{"protocol/profiles", "test/harness/profile-manifest.schema.json", "test/evidence/profile-registry.json"}, Suites: []string{"requirement_verdict"}, Probe: "profiles"},
	"REQ-SCP-02": {Episode: "E01+E07", Owners: []string{"adapters/geth", "adapters/bsc", "adapters/bor", "core/capture"}, Suites: []string{"e2e", "scope"}},
	"REQ-SCP-03": {Episode: "E01+E05+E06", Owners: []string{"core/capture", "adapters/recovery_gc"}, Suites: []string{"scope"}, Probe: "capture_window"},
	"REQ-SCP-04": {Episode: "E04", Owners: []string{"core/normalization/eth", "core/normalization/bsc", "core/normalization/poly"}, Suites: []string{"e2e", "nats_acceptance"}, Probe: "empty_block"},
	"REQ-SCP-05": {Episode: "E04", Owners: []string{"core/normalization/eth", "core/normalization/bsc", "core/normalization/poly"}, Suites: []string{"e2e"}, Probe: "optional_scope"},
	"REQ-SCP-06": {Episode: "E07", Owners: []string{"gateway/publisher"}, Suites: []string{"scope", "nats_acceptance"}},

	// Native capture.
	"REQ-CAP-01": {Episode: "E01", Owners: []string{"build/native", "adapters/geth", "adapters/bsc", "adapters/bor"}, Suites: []string{"requirement_verdict"}, Probe: "profiles"},
	"REQ-CAP-02": {Episode: "E01+E05", Owners: []string{"core/capture", "test/native_faults"}, Suites: []string{"e2e"}},
	"REQ-CAP-03": {Episode: "E01+E05", Owners: []string{"adapters/recovery_gc", "test/native_faults"}, Suites: []string{"e2e"}},
	"REQ-CAP-04": {Episode: "E01+E05", Owners: []string{"adapters/bor/finality_observer", "test/native_faults"}, Suites: []string{"e2e"}},
	"REQ-CAP-05": {Episode: "E01+E04", Owners: []string{"core/capture", "core/normalization/eth"}, Suites: []string{"e2e", "nats_acceptance"}, Probe: "record_roundtrip"},
	"REQ-CAP-06": {Episode: "E01+E03+E04", Owners: []string{"core/capture", "gateway/journal"}, Suites: []string{"e2e"}, Probe: "bounded_frames"},
	"REQ-CAP-07": {Episode: "E01+E05", Owners: []string{"core/capture", "test/composed_faults"}, Suites: []string{"e2e"}},
	"REQ-CAP-08": {Episode: "E02+E03+E05", Owners: []string{"protocol/semantic", "core/delivery"}, Suites: []string{"e2e", "nats_acceptance"}, Probe: "one_decision_one_batch"},
	"REQ-CAP-09": {Episode: "E01+E04", Owners: []string{"core/normalization/bsc", "core/normalization/poly"}, Suites: []string{"e2e"}},
	"REQ-CAP-10": {Episode: "E03+E05+E07", Owners: []string{"adapters/maintenance", "core/delivery"}, Suites: []string{"isolation", "runbook"}},

	// Data and normalization.
	"REQ-DAT-01": {Episode: "E02", Owners: []string{"protocol/proto", "protocol/codec", "test/lifecycle_validator"}, Suites: []string{"nats_acceptance"}, Probe: "record_roundtrip"},
	"REQ-DAT-02": {Episode: "E02+E04", Owners: []string{"protocol/proto", "core/normalization/eth"}, Suites: []string{"nats_acceptance"}, Probe: "record_roundtrip"},
	"REQ-DAT-03": {Episode: "E02+E04", Owners: []string{"protocol/proto", "core/normalization/eth"}, Suites: []string{"nats_acceptance"}},
	"REQ-DAT-04": {Episode: "E04", Owners: []string{"core/normalization/eth", "core/normalization/bsc", "core/normalization/poly"}, Suites: []string{"e2e", "nats_acceptance"}},
	"REQ-DAT-05": {Episode: "E02+E04", Owners: []string{"protocol/semantic", "test/lifecycle_validator"}, Suites: []string{"nats_acceptance"}, Probe: "lifecycle_oracle"},
	"REQ-DAT-06": {Episode: "E02+E04", Owners: []string{"core/normalization/eth"}, Suites: []string{"e2e"}},
	"REQ-DAT-07": {Episode: "E04", Owners: []string{"core/normalization/eth"}, Suites: []string{"e2e", "nats_acceptance"}, Probe: "record_roundtrip"},
	"REQ-DAT-08": {Episode: "E04", Owners: []string{"core/normalization/eth", "core/normalization/bsc"}, Suites: []string{"e2e"}},
	"REQ-DAT-09": {Episode: "E02+E04", Owners: []string{"protocol/proto", "core/normalization/poly"}, Suites: []string{"nats_acceptance"}},
	"REQ-DAT-10": {Episode: "E02+E04", Owners: []string{"protocol/codec"}, Suites: []string{"e2e", "scope", "nats_acceptance"}, Probe: "empty_block"},

	// Lifecycle, finality and GC.
	"REQ-LIF-01": {Episode: "E01+E05", Owners: []string{"adapters/lifecycle_registry", "test/native_lifecycle"}, Suites: []string{"e2e"}, Probe: "reorg_lifecycle"},
	"REQ-LIF-02": {Episode: "E02+E05", Owners: []string{"test/lifecycle_validator", "test/composed_faults"}, Suites: []string{"e2e"}, Probe: "reorg_lifecycle"},
	"REQ-LIF-03": {Episode: "E02+E05", Owners: []string{"test/lifecycle_validator"}, Suites: []string{"nats_acceptance"}, Probe: "lifecycle_oracle"},
	"REQ-LIF-04": {Episode: "E01+E05", Owners: []string{"adapters/lifecycle_registry", "adapters/maintenance"}, Suites: []string{"e2e", "runbook"}},
	"REQ-LIF-05": {Episode: "E01+E05", Owners: []string{"adapters/geth", "adapters/recovery_gc"}, Suites: []string{"e2e"}, Probe: "finality_only"},
	"REQ-LIF-06": {Episode: "E01+E05", Owners: []string{"adapters/bsc", "adapters/recovery_gc"}, Suites: []string{"e2e"}},
	"REQ-LIF-07": {Episode: "E01+E02+E05", Owners: []string{"adapters/bor/finality_observer", "protocol/proto"}, Suites: []string{"e2e"}},
	"REQ-LIF-08": {Episode: "E05", Owners: []string{"adapters/recovery_gc"}, Suites: []string{"e2e"}, Probe: "finality_only"},

	// Delivery and maintenance.
	"REQ-DLV-01": {Episode: "E03", Owners: []string{"core/journal/producer", "core/journal/storage", "gateway/journal"}, Suites: []string{"e2e"}},
	"REQ-DLV-02": {Episode: "E02+E03", Owners: []string{"protocol/semantic", "core/delivery"}, Suites: []string{"e2e"}, Probe: "one_decision_one_batch"},
	"REQ-DLV-03": {Episode: "E03", Owners: []string{"gateway/ingest", "gateway/journal"}, Suites: []string{"e2e"}},
	"REQ-DLV-04": {Episode: "E02+E03", Owners: []string{"protocol/codec", "gateway/publisher"}, Suites: []string{"nats_acceptance"}, Probe: "bounded_frames"},
	"REQ-DLV-05": {Episode: "E03", Owners: []string{"gateway/publisher", "core/delivery"}, Suites: []string{"e2e"}, Probe: "one_decision_one_batch"},
	"REQ-DLV-06": {Episode: "E03", Owners: []string{"core/delivery/release", "gateway/journal"}, Suites: []string{"e2e"}},
	"REQ-DLV-07": {Episode: "E03+E05", Owners: []string{"gateway/journal", "core/delivery"}, Suites: []string{"e2e"}},
	"REQ-DLV-08": {Episode: "E03+E05", Owners: []string{"gateway/session", "core/delivery"}, Suites: []string{"e2e"}},
	"REQ-DLV-09": {Episode: "E02+E03+E06", Owners: []string{"gateway/destination", "protocol/proto"}, Suites: []string{"isolation", "nats_acceptance"}},
	"REQ-DLV-10": {Episode: "E05+E06", Owners: []string{"adapters/maintenance", "core/delivery/maintenance"}, Suites: []string{"runbook"}, Probe: "drain_resume"},
	"REQ-DLV-11": {Episode: "E03+E06", Owners: []string{"core/delivery/maintenance", "gateway/destination"}, Suites: []string{"runbook"}, Probe: "drain_resume"},
	"REQ-DLV-12": {Episode: "E05+E06", Owners: []string{"core/delivery/maintenance"}, Suites: []string{"runbook"}},
	"REQ-DLV-13": {Episode: "E03+E05", Owners: []string{"core/delivery", "test/delivery_faults"}, Suites: []string{"e2e"}},
	"REQ-DLV-14": {Episode: "E03+E05+E06", Owners: []string{"protocol/compatibility", "test/compatibility_recovery"}, Suites: []string{"requirement_verdict"}, Probe: "compatibility"},

	// Configuration and environment.
	"REQ-OPS-01": {Episode: "E01+E06", Owners: []string{"config", "gateway/destination"}, Suites: []string{"requirement_verdict"}, Probe: "config_strict"},
	"REQ-OPS-02": {Episode: "E06", Owners: []string{"deploy", "deploy/config"}, Suites: []string{"isolation"}, Probe: "isolation_writers"},
	"REQ-OPS-03": {Episode: "E01+E06", Owners: []string{"deploy/bootstrap"}, Suites: []string{"runbook"}},
	"REQ-OPS-04": {Episode: "E06", Owners: []string{"config", "deploy/internal"}, Suites: []string{"requirement_verdict"}, Probe: "config_strict"},
	"REQ-OPS-05": {Episode: "E03+E06", Owners: []string{"gateway/session", "deploy"}, Suites: []string{"requirement_verdict"}},
	"REQ-OPS-06": {Episode: "E03+E06", Owners: []string{"gateway/destination", "test/harness/nats_conformance.py"}, Suites: []string{"nats_acceptance"}},
	"REQ-OPS-07": {Episode: "E06", Owners: []string{"deploy/config", ".project/docs/NETWORK_PORTS.md"}, Suites: []string{"requirement_verdict"}},
	"REQ-OPS-08": {Episode: "E06", Owners: []string{"deploy", "adapters/maintenance"}, Suites: []string{"runbook"}, Probe: "drain_resume"},
	"REQ-OPS-09": {Episode: "E06+E07", Owners: []string{"config/logs.go", "test/acceptance/runbook.md"}, Suites: []string{"runbook"}},
	"REQ-OPS-10": {Episode: "E01+E06", Owners: []string{"build/native", "deploy/bootstrap"}, Suites: []string{"requirement_verdict"}, Probe: "profiles"},

	// Development and readiness boundary.
	"REQ-DEV-01": {Episode: "E01-E07", Owners: []string{".project/docs/TASK_DECOMPOSITION.md", ".project/docs/DEVELOPMENT_PLAN.md"}, Suites: []string{"requirement_verdict"}, Probe: "episode_order"},
	"REQ-DEV-02": {Episode: "E01+E02+E07", Owners: []string{"test/harness", "test/acceptance/reader.go"}, Suites: []string{"e2e", "scope", "nats_acceptance"}, Probe: "independent_oracle"},
	"REQ-DEV-03": {Episode: "E01", Owners: []string{"protocol/proto/go.mod", "core/delivery/go.mod", "gateway/publisher/go.mod"}, Suites: []string{"requirement_verdict"}, Probe: "module_boundary"},
	"REQ-DEV-04": {Episode: "E01+E07", Owners: []string{"test/evidence/case-registry.schema.json", "test/evidence/case-result.schema.json", "test/evidence/run-manifest.schema.json"}, Suites: []string{"requirement_verdict"}, Probe: "evidence_schemas"},
	"REQ-DEV-05": {Episode: "E07", Owners: []string{"test/acceptance"}, Suites: []string{"e2e", "scope", "runbook", "isolation", "nats_acceptance", "requirement_verdict"}, Probe: "all_slots"},
}
