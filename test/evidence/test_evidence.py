"""Independent synthetic conformance tests; these are not native runtime runs."""

import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import random
import subprocess
import sys
import unittest


ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("evidence_validator", ROOT / "validate.py")
validator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(validator)

AXES = ("ac_id", "network", "profile", "profile_manifest_digest", "fork_variant",
        "fixture_kind", "suite", "variant_id")
PROFILES = {
    "ethereum-mainnet": "ETH", "ethereum-sepolia": "ETH", "ethereum-hoodi": "ETH",
    "bsc-mainnet": "BSC", "bsc-testnet": "BSC", "polygon-pos-mainnet": "POLY", "polygon-pos-amoy": "POLY",
}


def sha(text):
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def hash_document(document):
    # Deliberately do not call the product's canonical/digest helpers.
    encoded = json.dumps(document, ensure_ascii=True, sort_keys=True, separators=(",", ":")) + "\n"
    return sha(encoded)


def case_identity(case):
    return {key: case[key] for key in AXES}


def make_case(profile="DOC", variant="rules-and-fixtures"):
    return {
        "schema_version": 3, "ac_id": "AC-DEV-01-01", "network": PROFILES.get(profile, "DOC"),
        "profile": profile, "profile_manifest_digest": "NOT_APPLICABLE" if profile == "DOC" else sha("fixture-profile:" + profile),
        "fork_variant": "NOT_APPLICABLE" if profile == "DOC" else "test-interval-before-activation",
        "fixture_kind": "document" if profile == "DOC" else "native-seam", "suite": "functional",
        "variant_id": variant, "task_id": "E01-01.evidence", "fixture_id": "independent-conformance-input",
        "fixture_version": "7.2", "applicability": "APPLICABLE",
    }


def make_context(cases=None):
    return {
        "registry": {"schema_version": 3, "version": "independent-definition-7", "cases": cases or [make_case()]},
        "task_ids": ["E01-01.evidence", "E01-01.harness"], "case_owners": {"AC-DEV-01-01": "E01-01.evidence"},
        "profiles": dict(PROFILES), "rules": {}, "manifests": {}, "child_manifests": {},
        "artifacts": {"inputs/plan.txt": "Synthetic ordered inputs: A -> B\n",
                      "expected/ledger.txt": "Independent expected assertion: preserve A and B.\n",
                      "actual/observed.txt": "Synthetic observation used only to test schema conformance.\n",
                      "logs/trace.txt": "Synthetic log: caf\u00e9, \u6771\u4eac, checksum input.\n"},
    }


def make_manifest(context, cases=None, run_id="unit-conformance-run", verdict="PASS"):
    cases = cases or context["registry"]["cases"]
    case = cases[0]
    refs = {path: {"path": path, "sha256": sha(text)} for path, text in context["artifacts"].items()}
    manifest = {
        "schema_version": 3, "run_id": run_id, "case_registry_version": context["registry"]["version"],
        "case_registry_digest": hash_document(context["registry"]),
        "case_identities": [case_identity(item) for item in cases], "task_id": case["task_id"],
        "fixture_id": case["fixture_id"], "fixture_version": case["fixture_version"],
        "requirement_ids": ["REQ-DEV-01"], "audit_finding_ids": [],
        **{key: case[key] for key in AXES if key not in ("ac_id", "variant_id")},
        "native_commits": {}, "patch_commits": {}, "image_digests": {},
        "build_applicability": {"reason": "Document-only conformance fixture has no native build.", "rule_ref": "EVID:DOC"},
        "dependency_lock_digest": sha("synthetic dependency lock"), "compiler_versions": {}, "generator_versions": {},
        "architecture": "document-only", "input_artifacts": [refs["inputs/plan.txt"]],
        "expected_artifacts": [refs["expected/ledger.txt"]], "expected_source": "Separately authored contract assertions",
        "expected_method": "Handwritten positive controls and semantic counterexamples",
        "scenario": "conformance-contract-assertions", "seed": 193,
        "fault_boundary": "none", "storage_model": "document", "cache_model": "not-applicable",
        "run_command": ["synthetic-command-not-executed"], "environment_versions": {"fixture": "7.2"},
        "case_outcomes": [{"identity": case_identity(item), "verdict": verdict} for item in cases],
        "actual_artifacts": [refs["actual/observed.txt"]], "logs": [refs["logs/trace.txt"]],
        "comparator_version": "independent-fixture-v1", "outcome_reason": "Synthetic evidence for validator tests only.",
        "started_at": "2024-02-29T02:00:00+02:00", "finished_at": "2024-02-29T02:00:40+02:00",
        "evidence_producer": "independent-unit-test", "parent_integration_run": None,
        "resource_deadline_manifest_digest": sha("synthetic finite deadlines"),
        "phases": {
            "preparation": {"started_at": "2024-02-29T00:00:00Z", "finished_at": "2024-02-29T00:00:10Z", "deadline_seconds": 20},
            "execution": {"started_at": "2024-02-29T00:00:10Z", "finished_at": "2024-02-29T00:00:30Z", "deadline_seconds": 30},
            "teardown": {"started_at": "2024-02-29T00:00:30Z", "finished_at": "2024-02-29T00:00:40Z", "deadline_seconds": 15},
        },
    }
    if case["profile"] != "DOC":
        manifest.update(native_commits={"execution": sha("synthetic native source")[:40]},
                        compiler_versions={"compiler": "test-version-7"}, architecture="aarch64",
                        image_digests={"execution": "sha256:" + sha("synthetic image")})
        del manifest["build_applicability"]
    return manifest


def reference(manifest, path="runs/conformance.json"):
    return {"path": path, "run_id": manifest["run_id"], "sha256": hash_document(manifest)}


def result_bundle(verdict="NOT_RUN"):
    context = make_context()
    result = dict(context["registry"]["cases"][0], verdict=verdict)
    if verdict != "NOT_RUN":
        manifest = make_manifest(context, verdict=verdict)
        result["run_manifest_ref"] = reference(manifest)
        context["manifests"][result["run_manifest_ref"]["path"]] = manifest
    return bundle("CaseResult", result, context)


def bundle(kind, document, context):
    return {"contract": "evidence-artifacts-v1", "kind": kind, "document": document, "context": context}


def composite_bundle():
    cases = [make_case(profile) for profile in ("ethereum-hoodi", "polygon-pos-amoy", "bsc-testnet")]
    context = make_context(cases)
    children = []
    for index, case in enumerate(cases):
        child = make_manifest(context, [case], run_id=f"independent-child-{index}")
        child["parent_integration_run"] = "independent-parent"
        child["correlation_id"] = "joint-isolation-check"
        path = f"runs/{case['profile']}.json"
        context["child_manifests"][path] = child
        children.append(dict(reference(child, path), profile=case["profile"], verdict="PASS"))
    parent = {"schema_version": 3, "run_id": "independent-parent", "correlation_id": "joint-isolation-check",
              "required_profiles": [case["profile"] for case in cases], "children": children, "verdict": "PASS",
              "integration_outcome": {"scenario": "TC08", "verdict": "PASS"}}
    return bundle("CompositeManifest", parent, context)


class EvidenceTests(unittest.TestCase):
    def valid(self, request):
        validator.validate_bundle(copy.deepcopy(request))

    def invalid(self, request):
        with self.assertRaises(validator.InvalidEvidence):
            validator.validate_bundle(copy.deepcopy(request))

    def test_all_execution_states_have_independent_positive_controls(self):
        for verdict in ("NOT_RUN", "PASS", "FAIL"):
            with self.subTest(verdict=verdict):
                self.valid(result_bundle(verdict))

    def test_exceptions_have_no_verdict_or_execution(self):
        for applicability in ("NOT_APPLICABLE", "NOT_ENABLED", "DEFERRED"):
            with self.subTest(applicability=applicability):
                case = dict(make_case("ethereum-hoodi"), applicability=applicability,
                            reason="Accepted synthetic case-scoped rule.", rule_ref="reviewed-rule")
                context = make_context([case])
                context["rules"]["reviewed-rule"] = {"accepted": True, "applicability": applicability,
                                                      "optional_extension": True, "profile": case["profile"]}
                request = bundle("CaseResult", case, context)
                self.valid(request)
                for field, value in (("verdict", "NOT_RUN"), ("run_manifest_ref", reference(make_manifest(make_context())))):
                    changed = copy.deepcopy(request)
                    changed["document"][field] = value
                    self.invalid(changed)
                for field in ("reason", "rule_ref"):
                    changed = copy.deepcopy(request)
                    del changed["document"][field]
                    self.invalid(changed)
                for field, value in (("accepted", False), ("applicability", "APPLICABLE"), ("profile", "bsc-testnet")):
                    changed = copy.deepcopy(request)
                    changed["context"]["rules"]["reviewed-rule"][field] = value
                    self.invalid(changed)
                if applicability == "NOT_ENABLED":
                    changed = copy.deepcopy(request)
                    del changed["context"]["rules"]["reviewed-rule"]["optional_extension"]
                    self.invalid(changed)

    def test_every_identity_axis_survives_uniqueness(self):
        base = make_case("ethereum-sepolia")
        cases = [base]
        for field, value in (("ac_id", "AC-DEV-01-02"), ("profile_manifest_digest", sha("changed profile")),
                             ("fork_variant", "test-interval-after-activation"), ("fixture_kind", "local-e2e"),
                             ("suite", "fault"), ("variant_id", "independent-second-variant")):
            cases.append(dict(base, **{field: value}))
        cases.append(make_case("ethereum-hoodi"))
        cases.append(make_case("bsc-testnet"))
        context = make_context(cases)
        context["case_owners"]["AC-DEV-01-02"] = base["task_id"]
        self.valid(bundle("CaseRegistry", context["registry"], context))
        context["registry"]["cases"].append(dict(base, fixture_version="8"))
        self.invalid(bundle("CaseRegistry", context["registry"], context))

    def test_all_profiles_forks_and_suites_are_distinct(self):
        cases = [dict(make_case(profile), fork_variant=f"controlled-interval-{fork}", suite=suite)
                 for profile in PROFILES for fork in ("before", "after") for suite in ("quick", "functional")]
        context = make_context(cases)
        self.assertEqual(len(cases), 28)
        self.valid(bundle("CaseRegistry", context["registry"], context))

    def test_profile_registry_cannot_be_redefined_by_context(self):
        request = result_bundle()
        request["context"]["profiles"]["bsc-testnet"] = "ETH"
        self.invalid(request)

    def test_doc_fields_are_closed_as_a_group(self):
        for field, value in (("network", "COMMON"), ("profile", "ethereum-mainnet"),
                             ("profile_manifest_digest", sha("invented")), ("fork_variant", "active"),
                             ("fixture_kind", "native-seam")):
            with self.subTest(field=field):
                context = make_context([dict(make_case(), **{field: value})])
                self.invalid(bundle("CaseRegistry", context["registry"], context))

    def test_runtime_identity_rejects_placeholders_and_unknown_values(self):
        for field, value in (("profile", "private-chain"), ("network", "POLY"),
                             ("profile_manifest_digest", "NOT_APPLICABLE"), ("fork_variant", "NOT_APPLICABLE"),
                             ("fixture_kind", "synthetic"), ("suite", "everything"), ("variant_id", "   ")):
            with self.subTest(field=field):
                context = make_context([dict(make_case("ethereum-hoodi"), **{field: value})])
                self.invalid(bundle("CaseRegistry", context["registry"], context))

    def test_public_smoke_requires_a_public_runtime_fixture(self):
        case = dict(make_case("ethereum-hoodi"), suite="public-smoke")
        context = make_context([case])
        self.invalid(bundle("CaseRegistry", context["registry"], context))
        case["fixture_kind"] = "public"
        self.valid(bundle("CaseRegistry", context["registry"], context))

    def test_seed_must_be_a_reproducible_integer_or_nonempty_label(self):
        context = make_context()
        for seed in ("", " ", None, True, 1.25):
            manifest = make_manifest(context)
            manifest["seed"] = seed
            self.invalid(bundle("RunManifest", manifest, context))

    def test_task_and_owner_are_both_binding(self):
        for task in ("E01-01.harness", "E01-01.evidence.ETH", "unknown-task"):
            context = make_context([dict(make_case(), task_id=task)])
            self.invalid(bundle("CaseRegistry", context["registry"], context))

    def test_results_reject_all_fabricated_execution_fields(self):
        for name in ("actual_artifacts", "actual", "logs", "started_at", "finished_at", "phases", "case_outcomes"):
            request = result_bundle()
            request["document"][name] = "invented"
            self.invalid(request)

    def test_not_run_cannot_point_at_a_real_manifest(self):
        request = result_bundle("PASS")
        request["document"]["verdict"] = "NOT_RUN"
        self.invalid(request)

    def test_unknown_and_diagnostic_verdicts_are_not_case_verdicts(self):
        for verdict in ("UNSTABLE", "PARTIAL", "SKIP", None, True, 0):
            request = result_bundle()
            request["document"]["verdict"] = verdict
            self.invalid(request)

    def test_result_binds_all_registry_fields(self):
        for field in AXES + ("task_id", "fixture_id", "fixture_version", "applicability"):
            with self.subTest(field=field):
                request = result_bundle("PASS")
                request["document"][field] = "changed"
                self.invalid(request)

    def test_manifest_references_require_path_id_digest_and_matching_outcome(self):
        for field in ("path", "run_id", "sha256"):
            request = result_bundle("PASS")
            request["document"]["run_manifest_ref"][field] = sha("wrong")
            self.invalid(request)
        request = result_bundle("PASS")
        request["document"]["verdict"] = "FAIL"
        self.invalid(request)

    def test_every_required_manifest_field_is_enforced(self):
        context = make_context()
        baseline = make_manifest(context)
        required = json.loads((ROOT / "run-manifest.schema.json").read_text())["required"]
        self.valid(bundle("RunManifest", baseline, context))
        for field in required:
            with self.subTest(field=field):
                manifest = copy.deepcopy(baseline)
                del manifest[field]
                self.invalid(bundle("RunManifest", manifest, context))

    def test_manifest_empty_evidence_and_unknown_fields_are_rejected(self):
        context = make_context()
        for field in ("input_artifacts", "expected_artifacts", "actual_artifacts", "logs", "case_identities", "case_outcomes", "requirement_ids", "run_command"):
            manifest = make_manifest(context)
            manifest[field] = []
            self.invalid(bundle("RunManifest", manifest, context))
        manifest = make_manifest(context)
        manifest["global_pass"] = True
        self.invalid(bundle("RunManifest", manifest, context))

    def test_exact_artifact_bytes_include_unicode_and_newlines(self):
        context = make_context()
        manifest = make_manifest(context)
        self.valid(bundle("RunManifest", manifest, context))
        for path in context["artifacts"]:
            changed = copy.deepcopy(context)
            changed["artifacts"][path] += "\n"
            self.invalid(bundle("RunManifest", manifest, changed))
        missing = copy.deepcopy(context)
        del missing["artifacts"]["logs/trace.txt"]
        self.invalid(bundle("RunManifest", manifest, missing))

    def test_registry_version_digest_and_case_content_are_binding(self):
        context = make_context()
        manifest = make_manifest(context)
        for field, value in (("version", "new-definition"), ("schema_version", 2)):
            changed = copy.deepcopy(context)
            changed["registry"][field] = value
            self.invalid(bundle("RunManifest", manifest, changed))
        changed = copy.deepcopy(context)
        changed["registry"]["cases"][0]["fixture_version"] = "8"
        self.invalid(bundle("RunManifest", manifest, changed))

    def test_run_requires_each_outcome_and_allows_recorded_failure(self):
        context = make_context([make_case(), make_case(variant="second-assertion")])
        manifest = make_manifest(context)
        manifest["case_outcomes"][1]["verdict"] = "FAIL"
        self.valid(bundle("RunManifest", manifest, context))
        changed = copy.deepcopy(manifest)
        changed["case_outcomes"].pop()
        self.invalid(bundle("RunManifest", changed, context))
        changed = copy.deepcopy(manifest)
        changed["case_outcomes"][1] = dict(changed["case_outcomes"][0], verdict="FAIL")
        self.invalid(bundle("RunManifest", changed, context))
        changed = copy.deepcopy(manifest)
        changed["case_outcomes"][1]["identity"]["variant_id"] = "undeclared-assertion"
        self.invalid(bundle("RunManifest", changed, context))

    def test_a_run_cannot_mix_profiles_or_forks(self):
        for field, value in (("profile", "ethereum-sepolia"), ("fork_variant", "another-interval"), ("suite", "fault")):
            first = make_case("ethereum-hoodi")
            second = dict(first, variant_id="other-case", **{field: value})
            context = make_context([first, second])
            self.invalid(bundle("RunManifest", make_manifest(context), context))

    def test_native_builds_and_doc_exclusions_are_not_interchangeable(self):
        context = make_context([make_case("polygon-pos-amoy")])
        for field in ("native_commits", "compiler_versions", "image_digests"):
            manifest = make_manifest(context)
            manifest[field] = {}
            self.invalid(bundle("RunManifest", manifest, context))
        context = make_context()
        manifest = make_manifest(context)
        del manifest["build_applicability"]
        self.invalid(bundle("RunManifest", manifest, context))

    def test_source_refs_and_hashes_have_exact_formats(self):
        context = make_context([make_case("bsc-mainnet")])
        for field, value in (("native_commits", {"execution": "latest"}),
                             ("image_digests", {"execution": "image:stable"}),
                             ("dependency_lock_digest", "pending"), ("resource_deadline_manifest_digest", "0" * 63)):
            manifest = make_manifest(context)
            manifest[field] = value
            self.invalid(bundle("RunManifest", manifest, context))

    def test_hashes_reject_trailing_newlines_even_with_matching_context(self):
        context = make_context([make_case("bsc-mainnet")])
        for field in ("dependency_lock_digest", "resource_deadline_manifest_digest"):
            manifest = make_manifest(context)
            manifest[field] += "\n"
            self.invalid(bundle("RunManifest", manifest, context))
        for field in ("native_commits", "image_digests"):
            manifest = make_manifest(context)
            manifest[field]["execution"] += "\n"
            self.invalid(bundle("RunManifest", manifest, context))
        context["registry"]["cases"][0]["profile_manifest_digest"] += "\n"
        self.invalid(bundle("CaseRegistry", context["registry"], context))

    def test_phase_order_containment_and_declared_duration(self):
        context = make_context()
        for change in ("reversed", "outside", "overlap", "duration", "zero-deadline", "bad-time"):
            manifest = make_manifest(context)
            phase = manifest["phases"]["execution"]
            if change == "reversed": phase["finished_at"] = "2024-02-29T00:00:05Z"
            if change == "outside": phase["finished_at"] = "2024-02-29T01:00:00Z"
            if change == "overlap": phase["started_at"] = "2024-02-29T00:00:05Z"
            if change == "duration": phase["duration_seconds"] = 19
            if change == "zero-deadline": phase["deadline_seconds"] = 0
            if change == "bad-time": phase["started_at"] = "2024-02-30T00:00:10Z"
            self.invalid(bundle("RunManifest", manifest, context))
        manifest = make_manifest(context)
        manifest["phases"]["execution"]["duration_seconds"] = 20
        self.valid(bundle("RunManifest", manifest, context))

    def test_deadline_failure_is_valid_evidence_but_not_pass(self):
        context = make_context()
        manifest = make_manifest(context)
        manifest["phases"]["execution"]["deadline_seconds"] = 19
        self.invalid(bundle("RunManifest", manifest, context))
        manifest["case_outcomes"][0]["verdict"] = "FAIL"
        self.valid(bundle("RunManifest", manifest, context))

    def test_standard_and_extended_deadlines_are_distinct(self):
        context = make_context()
        manifest = make_manifest(context)
        manifest["phases"]["execution"]["deadline_seconds"] = 1801
        self.invalid(bundle("RunManifest", manifest, context))
        manifest["run_mode"] = "extended"
        self.valid(bundle("RunManifest", manifest, context))

    def test_retry_metadata_cannot_replace_the_case_verdict(self):
        context = make_context()
        manifest = make_manifest(context)
        manifest["attempt_index"] = 2
        self.invalid(bundle("RunManifest", manifest, context))
        manifest.update(first_run_id="first-failed-attempt", retry_reason="One diagnostic retry on clean inputs.")
        self.valid(bundle("RunManifest", manifest, context))
        manifest["first_run_id"] = manifest["run_id"]
        self.invalid(bundle("RunManifest", manifest, context))

    def test_composite_positive_and_failed_child_evidence(self):
        request = composite_bundle()
        self.valid(request)
        child_ref = request["document"]["children"][0]
        child = request["context"]["child_manifests"][child_ref["path"]]
        child["case_outcomes"][0]["verdict"] = "FAIL"
        child_ref.update(verdict="FAIL", sha256=hash_document(child))
        self.invalid(request)
        request["document"]["verdict"] = "FAIL"
        self.valid(request)

    def test_integration_outcome_cannot_be_hidden(self):
        request = composite_bundle()
        request["document"]["integration_outcome"]["verdict"] = "FAIL"
        self.invalid(request)
        request["document"]["verdict"] = "FAIL"
        self.valid(request)

    def test_composite_exact_child_closure_and_parent_linkage(self):
        for change in ("missing", "duplicate", "extra-profile", "wrong-parent", "wrong-profile", "wrong-correlation", "changed-bytes"):
            with self.subTest(change=change):
                request = composite_bundle()
                parent = request["document"]
                reference_value = parent["children"][0]
                child = request["context"]["child_manifests"][reference_value["path"]]
                if change == "missing": parent["children"].pop()
                if change == "duplicate": parent["children"].append(copy.deepcopy(reference_value))
                if change == "extra-profile": parent["required_profiles"].append("ethereum-mainnet")
                if change == "wrong-parent": child["parent_integration_run"] = "other-parent"
                if change == "wrong-profile": reference_value["profile"] = "ethereum-mainnet"
                if change == "wrong-correlation": child["correlation_id"] = "different-scenario"
                if change == "changed-bytes": child["outcome_reason"] = "Changed after sealing."
                else: reference_value["sha256"] = hash_document(child)
                self.invalid(request)

    def test_composite_parent_timing_contains_every_child(self):
        request = composite_bundle()
        sample = make_manifest(make_context())
        for field in ("started_at", "finished_at", "phases", "resource_deadline_manifest_digest"):
            request["document"][field] = copy.deepcopy(sample[field])
        self.valid(request)
        request["document"]["phases"]["execution"]["started_at"] = "2024-02-29T00:00:11Z"
        self.invalid(request)

    def test_partial_prerequisite_plans_still_reject_forward_edges(self):
        document = {"stages": [{"stage": "E01", "dependencies": [], "evidence": ["native-seam"]},
                               {"stage": "E05", "dependencies": ["E01"], "evidence": ["composed"]}]}
        self.valid(bundle("PrerequisitePlan", document, {}))
        document["stages"][1]["dependencies"] = []
        self.valid(bundle("PrerequisitePlan", document, {}))
        document["stages"][0]["dependencies"] = ["E05"]
        self.invalid(bundle("PrerequisitePlan", document, {}))

    def test_prerequisite_unknown_duplicate_missing_and_cyclic_edges(self):
        for change in ("unknown", "duplicate", "self", "cycle", "missing-evidence", "absent"):
            document = {"stages": [{"stage": "E01", "dependencies": [], "evidence": ["native-seam"]},
                                   {"stage": "E03", "dependencies": ["E01"], "evidence": ["delivery"]}]}
            if change == "unknown": document["stages"][1]["dependencies"] = ["E08"]
            if change == "duplicate": document["stages"].append(copy.deepcopy(document["stages"][0]))
            if change == "self": document["stages"][0]["dependencies"] = ["E01"]
            if change == "cycle": document["stages"][0]["dependencies"] = ["E03"]
            if change == "missing-evidence": document["stages"][1]["evidence"] = []
            if change == "absent": document["stages"][1]["dependencies"] = ["E02"]
            self.invalid(bundle("PrerequisitePlan", document, {}))

    def test_schema_versions_are_not_migrated_into_pass(self):
        for version in (2, "3", True, None, 4):
            request = result_bundle("PASS")
            request["document"]["schema_version"] = version
            self.invalid(request)

    def test_bundle_synthetic_label_does_not_affect_validity(self):
        request = result_bundle()
        for label in (True, False, "arbitrary-label", None):
            request["synthetic"] = label
            self.valid(request)
        request["document"]["verdict"] = "UNKNOWN"
        for label in (True, False):
            request["synthetic"] = label
            self.invalid(request)

    def test_schema_engine_rejects_unsupported_keywords(self):
        schema = validator.SchemaValidator()
        with self.assertRaises(validator.InvalidEvidence):
            schema.check_vocabulary({"type": "object", "unevaluatedProperties": False})

    def test_canonical_digest_is_order_independent_and_ascii_encoded(self):
        first = {"b": "\u6771\u4eac", "a": [3, {"x": "caf\u00e9"}]}
        second = {"a": [3, {"x": "caf\u00e9"}], "b": "\u6771\u4eac"}
        self.assertEqual(validator.digest(first), hash_document(second))

    def test_validity_does_not_depend_on_fixture_names_or_input_order(self):
        generator = random.Random(810257)
        for _ in range(12):
            context = make_context()
            case = context["registry"]["cases"][0]
            suffix = str(generator.getrandbits(96))
            case.update(variant_id="variant-" + suffix, fixture_id="fixture-" + suffix, fixture_version=suffix)
            manifest = make_manifest(context, run_id="run-" + suffix)
            manifest = dict(reversed(list(manifest.items())))
            result = dict(case, verdict="PASS", run_manifest_ref=reference(manifest, "ref-" + suffix))
            context["manifests"][result["run_manifest_ref"]["path"]] = manifest
            self.valid(bundle("CaseResult", result, context))
            result["fixture_version"] = "wrong-" + suffix
            self.invalid(bundle("CaseResult", result, context))

    def test_schema_numeric_types_and_uniqueness_follow_json_semantics(self):
        schemas = validator.SchemaValidator()
        filename = "evidence-definitions.schema.json"
        schemas.check(3.0, {"type": "integer", "const": 3}, filename, "value")
        schemas.check(10 ** 400, {"type": "integer"}, filename, "value")
        schemas.check([True, 1], {"uniqueItems": True}, filename, "value")
        for value, schema in ((True, {"type": "integer"}), (True, {"const": 1}),
                              ([1, 1.0], {"uniqueItems": True}),
                              ([{"seed": 1}, {"seed": 1.0}], {"uniqueItems": True})):
            with self.assertRaises(validator.InvalidEvidence):
                schemas.check(value, schema, filename, "value")


class CommandLineTests(unittest.TestCase):
    def call(self, value, raw=False):
        text = value if raw else json.dumps(value, ensure_ascii=False)
        return subprocess.run([sys.executable, "-I", str(ROOT / "validate.py")],
                              input=text.encode("utf-8"), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              timeout=5, cwd="/tmp")

    def test_cli_runs_isolated_from_any_working_directory(self):
        for verdict in ("NOT_RUN", "PASS", "FAIL"):
            result = self.call(result_bundle(verdict))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, b"")
            self.assertEqual(result.stderr, b"")

    def test_expansion_returns_only_sorted_concrete_profiles(self):
        for scope in ("ALL", "ETH", "BSC", "POLY"):
            result = self.call({"contract": "evidence-artifacts-v1", "kind": "ExpandProfiles", "scope": scope})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout), sorted(profile for profile, family in PROFILES.items() if scope in ("ALL", family)))

    def test_unknown_expansion_scope_is_invalid(self):
        for scope in ("COMMON", "DOC", "ethereum-mainnet", "all", [], None):
            result = self.call({"contract": "evidence-artifacts-v1", "kind": "ExpandProfiles", "scope": scope})
            self.assertEqual(result.returncode, 2, result.stderr)

    def test_invalid_json_and_duplicate_keys_have_bounded_errors(self):
        for text in ("", "null", "[]", "{", '{"contract":"x","contract":"y"}',
                     '{"value":NaN}', '{"value":Infinity}', "{} {}", "[" * 2000 + "]" * 2000):
            result = self.call(text, raw=True)
            self.assertEqual(result.returncode, 2)
            self.assertLess(len(result.stderr), 1024)
            self.assertEqual(result.stdout, b"")
            self.assertNotIn(b"Traceback", result.stderr)

    def test_malformed_nested_types_do_not_crash(self):
        for field in ("document", "context"):
            for value in (None, False, "text", 7, [], {}):
                request = result_bundle()
                request[field] = value
                result = self.call(request)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertNotIn(b"Traceback", result.stderr)

    def test_bad_artifact_does_not_echo_private_content(self):
        request = result_bundle("PASS")
        private_marker = "DO_NOT_ECHO_PRIVATE_ARTIFACT"
        request["context"]["artifacts"]["logs/trace.txt"] = private_marker * 10000
        result = self.call(request)
        self.assertEqual(result.returncode, 2)
        self.assertNotIn(private_marker.encode(), result.stderr)
        self.assertLess(len(result.stderr), 1024)

    def test_diagnostics_do_not_echo_unknown_property_names(self):
        request = result_bundle()
        marker = "DO_NOT_ECHO_PRIVATE_PROPERTY"
        request["document"][marker] = "private-value"
        result = self.call(request)
        self.assertEqual(result.returncode, 2)
        self.assertNotIn(marker.encode(), result.stderr)


if __name__ == "__main__":
    unittest.main()
