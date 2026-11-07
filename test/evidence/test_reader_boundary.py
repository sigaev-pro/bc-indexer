"""Independent D87 artifact tests, with no native or NATS execution claims."""

import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import unittest


ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("evidence_fixture_helpers", ROOT / "test_evidence.py")
fixtures = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fixtures)
validator = fixtures.validator

CONDITIONS = ("source_closure", "delivery_closure", "terminal_fence", "stream_coverage",
              "reconstruction", "semantic_comparison")
PHASES = ("startup", "observation", "shutdown_drain", "stream_closure", "reconstruction", "comparison")
RUN_PATH = "runs/reader-conformance.json"


def reseal(request):
    """Update synthetic artifact bytes after a mutation, independently of the SUT."""
    boundary, context = request["document"], request["context"]
    text = json.dumps(boundary, sort_keys=True, ensure_ascii=True, separators=(",", ":")) + "\n"
    context["artifacts"][boundary["artifact_path"]] = text
    if RUN_PATH in context["manifests"]:
        manifest = context["manifests"][RUN_PATH]
        observed_path = "actual/observed.txt"
        manifest["actual_artifacts"] = [
            {"path": observed_path, "sha256": fixtures.sha(context["artifacts"][observed_path])},
            {"path": boundary["artifact_path"], "sha256": fixtures.sha(text)},
        ]
    return request


def reader_bundle(started=True, verdict="PASS", mode="positive_delivery"):
    case = fixtures.make_case("polygon-pos-mainnet", variant="reader-boundary-conditions")
    context = fixtures.make_context([case])

    def artifact(name, text=None):
        path = "reader-chunks/" + name + ".json"
        content = text if text is not None else json.dumps({"synthetic_evidence": name, "execution_claim": False}) + "\n"
        context["artifacts"][path] = content
        return {"path": path, "sha256": fixtures.sha(content)}

    document = {
        "schema_version": 1, "boundary_id": "reader-contract-fixture-29", "artifact_path": "actual/reader-boundary.json",
        **{field: case[field] for field in fixtures.AXES if field not in ("ac_id", "variant_id")},
        "case_identities": [fixtures.case_identity(case)], "run_started": started,
        "run_id": "reader-conformance-run" if started else None,
        "run_manifest_path": RUN_PATH if started else None,
        "run_plan_ref": artifact("frozen-run-plan"), "oracle_ref": artifact("independent-oracle-rules"),
        "expectation": {"mode": mode, "predicate_ref": artifact("predeclared-negative-predicate") if mode == "declared_negative" else None},
        "source_boundaries": [{"source_id": "native-source-one", **{name: artifact("source-" + name) for name in
                              ("baseline_ref", "admission_set_ref", "terminal_frontier_ref", "transitions_ref")}}] if started else [],
        "delivery_boundaries": [{"destination_id": "isolated-destination", "binding_digest": fixtures.sha("synthetic binding"),
                                 "episode_id": "live-episode-before-drain", "generation": 4,
                                 "manifest_refs": [artifact("immutable-delivery-manifest")],
                                 **{name: artifact("delivery-" + name) for name in
                                    ("highwater_ref", "seal_ref", "puback_ref", "attempt_census_ref", "attempt_classifications_ref")}}] if started else [],
        "streams": [{"stream_id": "stream-identity-one", "destination_id": "isolated-destination", "generation": 4,
                     "config_ref": artifact("sealed-stream-config"), "lower_sequence": 8, "upper_sequence": 10,
                     "seal_ref": artifact("stream-seal"), "coverage_ref": artifact("full-interval-coverage"),
                     "raw_message_refs": [artifact("physical-message-9"), artifact("physical-message-10")],
                     "terminal_fence": {name: artifact("fence-" + name) for name in
                                        ("implementation_ref", "conformance_ref", "process_events_ref", "ingress_restrictions_ref", "recovered_frontiers_ref")}}] if started else [],
        "logical_comparison": {"expected_ref": artifact("expected-logical-ledger") if started else None,
                               "actual_ref": artifact("actual-logical-ledger") if started else None,
                               "diff_ref": artifact("logical-diff", "[]\n") if started else None},
        "phase_times": {name: {"started_at": "2024-02-29T00:00:11Z" if started else None,
                               "finished_at": "2024-02-29T00:00:29Z" if started else None,
                               "deadline_seconds": 20} for name in PHASES},
        "conditions": {name: {"status": "SATISFIED" if started else "NOT_RUN",
                              "evidence_refs": [artifact("condition-" + name)] if started else [],
                              "reason": "Synthetic condition record for structural testing only.",
                              "missing_obligations": []} for name in CONDITIONS},
        "reader_complete": started, "comparison_passed": True if started else None,
    }
    if started:
        context["manifests"][RUN_PATH] = fixtures.make_manifest(context, run_id=document["run_id"], verdict=verdict)
    return reseal(fixtures.bundle("ReaderBoundaryManifest", document, context))


def set_condition(request, name, status):
    document = request["document"]
    condition = document["conditions"][name]
    condition["status"] = status
    condition["missing_obligations"] = ["Required test observation is incomplete."] if status == "INCOMPLETE" else []
    if status == "NOT_RUN":
        condition["evidence_refs"] = []
    document["reader_complete"] = all(document["conditions"][item]["status"] == "SATISFIED" for item in CONDITIONS[:5])
    document["comparison_passed"] = {"SATISFIED": True, "FAILED": False, "INCOMPLETE": None, "NOT_RUN": None}[
        document["conditions"]["semantic_comparison"]["status"]]


class ReaderBoundaryTests(unittest.TestCase):
    def valid(self, request, seal=True):
        validator.validate_bundle(reseal(request) if seal else request)

    def invalid(self, request, seal=True):
        if seal:
            reseal(request)
        with self.assertRaises(validator.InvalidEvidence):
            validator.validate_bundle(request)

    def test_positive_completion_has_six_separate_conditions(self):
        request = reader_bundle()
        self.assertEqual(set(request["document"]["conditions"]), set(CONDITIONS))
        self.valid(request)

    def test_preparation_not_run_needs_no_fabricated_run(self):
        request = reader_bundle(started=False)
        request["context"]["manifests"] = {}
        request["document"]["run_plan_ref"] = None
        request["document"]["oracle_ref"] = None
        request["context"]["artifacts"] = {}
        self.valid(request, seal=False)

    def test_preparation_rejects_every_actual_execution_field(self):
        positive = reader_bundle()["document"]
        for field in ("run_id", "run_manifest_path", "source_boundaries", "delivery_boundaries", "streams",
                      "logical_comparison", "phase_times", "conditions", "reader_complete", "comparison_passed"):
            with self.subTest(field=field):
                request = reader_bundle(started=False)
                request["document"][field] = copy.deepcopy(positive[field])
                self.invalid(request)

    def test_preparation_still_requires_resolved_runtime_identity(self):
        for field, value in (("profile", "DOC"), ("network", "COMMON"), ("profile_manifest_digest", None),
                             ("fork_variant", "NOT_APPLICABLE"), ("fixture_kind", "document")):
            request = reader_bundle(started=False)
            request["document"][field] = value
            self.invalid(request)

    def test_failed_run_keeps_all_condition_states_distinct(self):
        for name in CONDITIONS:
            for status in ("FAILED", "INCOMPLETE", "NOT_RUN"):
                with self.subTest(condition=name, status=status):
                    request = reader_bundle(verdict="FAIL")
                    set_condition(request, name, status)
                    self.valid(request)

    def test_positive_pass_cannot_hide_any_unsatisfied_condition(self):
        for name in CONDITIONS:
            for status in ("FAILED", "INCOMPLETE", "NOT_RUN"):
                request = reader_bundle()
                set_condition(request, name, status)
                self.invalid(request)

    def test_semantic_failure_does_not_undo_reader_completion(self):
        request = reader_bundle(verdict="FAIL")
        set_condition(request, "semantic_comparison", "FAILED")
        self.assertTrue(request["document"]["reader_complete"])
        self.assertFalse(request["document"]["comparison_passed"])
        self.valid(request)
        request["document"]["reader_complete"] = False
        self.invalid(request)

    def test_declared_negative_can_preserve_failed_positive_closure(self):
        request = reader_bundle(mode="declared_negative")
        set_condition(request, "delivery_closure", "FAILED")
        set_condition(request, "reconstruction", "INCOMPLETE")
        set_condition(request, "semantic_comparison", "NOT_RUN")
        self.valid(request)
        request["document"]["expectation"]["predicate_ref"] = None
        self.invalid(request)

    def test_positive_mode_cannot_add_a_posthoc_negative_predicate(self):
        request = reader_bundle()
        request["document"]["expectation"]["predicate_ref"] = request["document"]["oracle_ref"]
        self.invalid(request)

    def test_all_top_level_fields_are_required_and_unknown_fields_fail(self):
        required = json.loads((ROOT / "reader-boundary-manifest.schema.json").read_text())["required"]
        for field in required:
            with self.subTest(field=field):
                request = reader_bundle()
                del request["document"][field]
                self.invalid(request, seal=False)
        request = reader_bundle()
        request["document"]["native_execution_proven"] = True
        self.invalid(request)

    def test_all_condition_objects_are_closed_and_typed(self):
        for name in CONDITIONS:
            for field, value in (("status", "PASS"), ("reason", " "), ("evidence_refs", []),
                                 ("missing_obligations", ["unresolved"]), ("invented", True)):
                request = reader_bundle()
                request["document"]["conditions"][name][field] = value
                self.invalid(request)
        request = reader_bundle(verdict="FAIL")
        set_condition(request, "source_closure", "FAILED")
        request["document"]["conditions"]["source_closure"]["evidence_refs"] = []
        self.invalid(request)
        request = reader_bundle(verdict="FAIL")
        set_condition(request, "source_closure", "INCOMPLETE")
        request["document"]["conditions"]["source_closure"]["missing_obligations"] = []
        self.invalid(request)

    def test_completion_and_comparison_flags_are_consistent(self):
        for field, value in (("reader_complete", False), ("comparison_passed", False), ("comparison_passed", None)):
            request = reader_bundle()
            request["document"][field] = value
            self.invalid(request)
        request = reader_bundle(verdict="FAIL")
        set_condition(request, "terminal_fence", "FAILED")
        request["document"]["reader_complete"] = True
        self.invalid(request)

    def test_source_closure_requires_every_boundary_reference(self):
        for field in ("baseline_ref", "admission_set_ref", "terminal_frontier_ref", "transitions_ref"):
            request = reader_bundle()
            request["document"]["source_boundaries"][0][field] = None
            self.invalid(request)
        request = reader_bundle()
        request["document"]["source_boundaries"] = []
        self.invalid(request)

    def test_delivery_closure_requires_every_ack_and_census_reference(self):
        for field in ("manifest_refs", "highwater_ref", "seal_ref", "puback_ref", "attempt_census_ref", "attempt_classifications_ref"):
            request = reader_bundle()
            request["document"]["delivery_boundaries"][0][field] = [] if field == "manifest_refs" else None
            self.invalid(request)
        request = reader_bundle()
        request["document"]["delivery_boundaries"] = []
        self.invalid(request)

    def test_terminal_fence_requires_all_five_evidence_references(self):
        fields = ("implementation_ref", "conformance_ref", "process_events_ref", "ingress_restrictions_ref", "recovered_frontiers_ref")
        for field in fields:
            request = reader_bundle()
            request["document"]["streams"][0]["terminal_fence"][field] = None
            self.invalid(request)

    def test_coverage_bounds_and_nonempty_interval_need_physical_evidence(self):
        for field, value in (("lower_sequence", -1), ("upper_sequence", 7), ("upper_sequence", None),
                             ("generation", True), ("raw_message_refs", []), ("config_ref", None),
                             ("seal_ref", None), ("coverage_ref", None)):
            request = reader_bundle()
            request["document"]["streams"][0][field] = value
            self.invalid(request)

    def test_empty_interval_remains_possible_without_claiming_empty_native_work(self):
        request = reader_bundle()
        stream = request["document"]["streams"][0]
        stream["upper_sequence"] = stream["lower_sequence"]
        stream["raw_message_refs"] = []
        self.valid(request)

    def test_semantic_comparison_requires_expected_actual_and_hashed_diff(self):
        for field in ("expected_ref", "actual_ref", "diff_ref"):
            request = reader_bundle()
            request["document"]["logical_comparison"][field] = None
            self.invalid(request)

    def test_failed_conditions_can_preserve_nullable_incomplete_boundaries(self):
        request = reader_bundle(verdict="FAIL")
        for name in CONDITIONS:
            set_condition(request, name, "INCOMPLETE")
        for source in request["document"]["source_boundaries"]:
            for field in ("baseline_ref", "admission_set_ref", "terminal_frontier_ref", "transitions_ref"):
                source[field] = None
        request["document"]["delivery_boundaries"] = []
        request["document"]["streams"] = []
        request["document"]["logical_comparison"] = {name: None for name in ("expected_ref", "actual_ref", "diff_ref")}
        self.valid(request)

    def test_duplicate_source_episode_and_stream_identities_are_rejected(self):
        for field in ("source_boundaries", "delivery_boundaries", "streams"):
            request = reader_bundle()
            second = copy.deepcopy(request["document"][field][0])
            request["document"][field].append(second)
            self.invalid(request)
        request = reader_bundle()
        next_episode = copy.deepcopy(request["document"]["delivery_boundaries"][0])
        next_episode["episode_id"] = "resumed-episode"
        request["document"]["delivery_boundaries"].append(next_episode)
        self.valid(request)

    def test_every_reference_family_is_checked_by_exact_hash(self):
        for family in ("run_plan_ref", "oracle_ref", "source", "delivery", "raw", "fence", "logical", "condition"):
            request = reader_bundle()
            document = request["document"]
            refs = {"run_plan_ref": document["run_plan_ref"], "oracle_ref": document["oracle_ref"],
                    "source": document["source_boundaries"][0]["baseline_ref"],
                    "delivery": document["delivery_boundaries"][0]["puback_ref"],
                    "raw": document["streams"][0]["raw_message_refs"][0],
                    "fence": document["streams"][0]["terminal_fence"]["process_events_ref"],
                    "logical": document["logical_comparison"]["diff_ref"],
                    "condition": document["conditions"]["reconstruction"]["evidence_refs"][0]}
            request["context"]["artifacts"][refs[family]["path"]] += "changed\n"
            self.invalid(request)

    def test_chunk_artifact_paths_and_bytes_cannot_be_replaced(self):
        for change in ("missing", "path", "hash"):
            request = reader_bundle()
            ref = request["document"]["streams"][0]["raw_message_refs"][0]
            if change == "missing": del request["context"]["artifacts"][ref["path"]]
            if change == "path": ref["path"] = "unavailable-chunk"
            if change == "hash": ref["sha256"] = fixtures.sha("wrong bytes")
            self.invalid(request)

    def test_run_output_reference_is_non_circular_and_exact(self):
        request = reader_bundle()
        document = request["document"]
        self.assertNotIn("run_manifest_digest", document)
        self.valid(request)
        for change in ("missing", "duplicate", "wrong-digest", "different-bytes"):
            request = reader_bundle()
            manifest = request["context"]["manifests"][RUN_PATH]
            ref = manifest["actual_artifacts"][-1]
            if change == "missing": manifest["actual_artifacts"].pop()
            if change == "duplicate": manifest["actual_artifacts"].append(copy.deepcopy(ref))
            if change == "wrong-digest": ref["sha256"] = fixtures.sha("another artifact")
            if change == "different-bytes":
                text = json.dumps(request["document"], indent=2) + "\n"
                request["context"]["artifacts"][ref["path"]] = text
                ref["sha256"] = fixtures.sha(text)
            self.invalid(request, seal=False)

    def test_boundary_and_run_bind_the_exact_case_set(self):
        for field in fixtures.AXES:
            request = reader_bundle()
            request["document"]["case_identities"][0][field] = "different"
            self.invalid(request)
        request = reader_bundle()
        second = fixtures.make_case("polygon-pos-mainnet", variant="additional-required-reader-case")
        request["context"]["registry"]["cases"].append(second)
        manifest = request["context"]["manifests"][RUN_PATH]
        manifest["case_registry_digest"] = fixtures.hash_document(request["context"]["registry"])
        manifest["case_identities"].append(fixtures.case_identity(second))
        manifest["case_outcomes"].append({"identity": fixtures.case_identity(second), "verdict": "PASS"})
        self.invalid(request)
        request["document"]["case_identities"].append(fixtures.case_identity(second))
        self.valid(request)

    def test_run_id_path_and_top_level_axes_cannot_be_substituted(self):
        for field in ("run_id", "run_manifest_path", "network", "profile", "profile_manifest_digest", "fork_variant", "fixture_kind", "suite"):
            request = reader_bundle()
            request["document"][field] = "different"
            self.invalid(request)

    def test_reader_phases_may_overlap_inside_execution(self):
        request = reader_bundle()
        self.assertEqual(len({phase["started_at"] for phase in request["document"]["phase_times"].values()}), 1)
        self.valid(request)

    def test_phase_order_containment_and_finite_deadlines(self):
        for field, value in (("started_at", None), ("started_at", "2024-02-29T00:00:09Z"),
                             ("finished_at", "2024-02-29T00:00:31Z"), ("finished_at", "2024-02-29T00:00:10Z"),
                             ("deadline_seconds", 0), ("deadline_seconds", True), ("deadline_seconds", float("inf"))):
            request = reader_bundle()
            request["document"]["phase_times"]["comparison"][field] = value
            self.invalid(request, seal=value != float("inf"))

    def test_positive_completion_requires_every_phase_to_finish_on_time(self):
        for name in PHASES:
            request = reader_bundle()
            request["document"]["phase_times"][name]["finished_at"] = None
            self.invalid(request)
            request = reader_bundle()
            request["document"]["phase_times"][name]["deadline_seconds"] = 17
            self.invalid(request)

    def test_recorded_fail_preserves_unstarted_and_overdue_phases(self):
        request = reader_bundle(verdict="FAIL")
        set_condition(request, "semantic_comparison", "INCOMPLETE")
        request["document"]["phase_times"]["comparison"].update(started_at=None, finished_at=None)
        request["document"]["phase_times"]["stream_closure"]["deadline_seconds"] = 1
        self.valid(request)

    def test_submicrosecond_timestamps_are_not_truncated(self):
        request = reader_bundle()
        phase = request["document"]["phase_times"]["comparison"]
        phase.update(started_at="2024-02-29T00:00:11.000000001Z", finished_at="2024-02-29T00:00:11.000000009Z", deadline_seconds=0.000000008)
        self.valid(request)
        phase["deadline_seconds"] = 0.000000007
        self.invalid(request)
        phase["started_at"] = "2024-02-29T00:00:11.000000010Z"
        self.invalid(request)

    def test_cli_validates_reader_states_under_isolated_python(self):
        requests = [reader_bundle(), reader_bundle(started=False), reader_bundle(verdict="FAIL")]
        set_condition(requests[2], "semantic_comparison", "FAILED")
        for request in requests:
            reseal(request)
            result = subprocess.run([sys.executable, "-I", str(ROOT / "validate.py")],
                                    input=json.dumps(request).encode(), capture_output=True, timeout=5, cwd="/tmp")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, b"")
        request = reader_bundle()
        set_condition(request, "semantic_comparison", "FAILED")
        reseal(request)
        result = subprocess.run([sys.executable, "-I", str(ROOT / "validate.py")],
                                input=json.dumps(request).encode(), capture_output=True, timeout=5, cwd="/tmp")
        self.assertEqual(result.returncode, 2)
        self.assertLess(len(result.stderr), 1024)

    def test_nested_unknown_fields_and_extra_conditions_are_invalid(self):
        for path in (("expectation",), ("source_boundaries", 0), ("delivery_boundaries", 0),
                     ("streams", 0), ("streams", 0, "terminal_fence"), ("logical_comparison",),
                     ("phase_times", "comparison"), ("conditions", "semantic_comparison")):
            request = reader_bundle()
            target = request["document"]
            for key in path:
                target = target[key]
            target["invented_field"] = "invalid"
            self.invalid(request)
        request = reader_bundle()
        request["document"]["conditions"]["global_pass"] = copy.deepcopy(request["document"]["conditions"]["reconstruction"])
        self.invalid(request)


if __name__ == "__main__":
    unittest.main()
