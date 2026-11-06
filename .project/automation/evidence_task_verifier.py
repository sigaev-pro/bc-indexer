"""Trusted conformance inputs for the future evidence artifact task, not its implementation."""

import argparse
import copy
import hashlib
import json
from pathlib import Path
import resource
import subprocess
import sys
import tempfile

TASK = "E01-01.evidence"
CONTRACT = "evidence-artifacts-v1"
PROFILES = {"ethereum-mainnet": "ETH", "ethereum-sepolia": "ETH", "ethereum-hoodi": "ETH",
            "bsc-mainnet": "BSC", "bsc-testnet": "BSC", "polygon-pos-mainnet": "POLY", "polygon-pos-amoy": "POLY"}
IDENTITY = ("ac_id", "network", "profile", "profile_manifest_digest", "fork_variant", "fixture_kind", "suite", "variant_id")
OUTPUTS = tuple("test/evidence/" + name for name in (
    "case-registry.schema.json", "case-result.schema.json", "run-manifest.schema.json",
    "profile-registry.json", "validate.py", "README.md"))
PACK_FILES = (".project/automation/evidence_task_verifier.py", ".project/automation/evidence-task-contract.md",
              ".project/automation/task_proofs.py", ".project/automation/task-recipes.json")


def canonical(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False) + "\n").encode()


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def check(condition, message):
    if not condition:
        raise ValueError(message)


def pack(root):
    return {name: hashlib.sha256((Path(root) / name).read_bytes()).hexdigest() for name in PACK_FILES}


def vectors():
    """D82/EVID examples are synthetic oracle inputs; none are runtime evidence."""
    case = {"schema_version": 3, "ac_id": "AC-DEV-04-02", "network": "DOC", "profile": "DOC",
            "profile_manifest_digest": "NOT_APPLICABLE", "fork_variant": "NOT_APPLICABLE",
            "fixture_kind": "document", "suite": "functional", "variant_id": "synthetic-applicability",
            "task_id": TASK, "fixture_id": "synthetic-schema-input", "fixture_version": "1",
            "applicability": "APPLICABLE"}
    identity = {key: case[key] for key in IDENTITY}
    registry = {"schema_version": 3, "version": "synthetic-1", "cases": [case]}
    artifact = {"path": "synthetic.txt", "sha256": hashlib.sha256(b"synthetic fixture only\n").hexdigest()}
    manifest = {"schema_version": 3, "run_id": "synthetic-doc-run", "case_registry_version": registry["version"],
                "case_registry_digest": digest(registry), "case_identities": [identity], "task_id": TASK,
                "fixture_id": case["fixture_id"], "fixture_version": "1", "requirement_ids": ["REQ-DEV-04"],
                "audit_finding_ids": [], **{key: case[key] for key in IDENTITY if key not in {"ac_id", "variant_id"}},
                "native_commits": {}, "patch_commits": {}, "image_digests": {},
                "build_applicability": {"reason": "Synthetic DOC fixture; no native build or image.", "rule_ref": "EVID:DOC"},
                "dependency_lock_digest": "a" * 64, "compiler_versions": {}, "generator_versions": {},
                "architecture": "synthetic", "input_artifacts": [artifact], "expected_artifacts": [artifact],
                "expected_source": "bootstrap vector", "expected_method": "independently fixed contract example",
                "scenario": "synthetic-schema-input", "seed": 0, "fault_boundary": "none",
                "storage_model": "synthetic-document", "cache_model": "not-applicable",
                "run_command": ["synthetic-only"], "environment_versions": {"fixture": "1"},
                "case_outcomes": [{"identity": identity, "verdict": "PASS"}],
                "actual_artifacts": [artifact], "logs": [artifact], "comparator_version": "synthetic-1",
                "outcome_reason": "Synthetic schema input, not an executed product case.",
                "started_at": "2000-01-01T00:00:00Z", "finished_at": "2000-01-01T00:00:03Z",
                "evidence_producer": "bootstrap-synthetic-fixture", "parent_integration_run": None,
                "resource_deadline_manifest_digest": "b" * 64,
                "phases": {name: {"started_at": f"2000-01-01T00:00:0{number}Z", "finished_at": f"2000-01-01T00:00:0{number + 1}Z",
                                  "deadline_seconds": 2} for number, name in enumerate(("preparation", "execution", "teardown"))}}
    reference = {"path": "synthetic-run.json", "run_id": manifest["run_id"], "sha256": digest(manifest)}
    context = {"registry": registry, "manifests": {reference["path"]: manifest},
               "artifacts": {artifact["path"]: "synthetic fixture only\n"},
               "rules": {"scope-example": {"applicability": "DEFERRED", "accepted": True},
                         "fork-example": {"applicability": "NOT_APPLICABLE", "accepted": True},
                         "optional-example": {"applicability": "NOT_ENABLED", "accepted": True, "optional_extension": True}},
               "task_ids": [TASK, "E04-02.optional_scope", "E07-09.requirement_verdict"], "profiles": PROFILES,
               "case_owners": {"AC-DEV-04-02": TASK, "AC-SCP-05-02": "E04-02.optional_scope",
                               "AC-DEV-04-01": "E07-09.requirement_verdict"}}
    rows = []

    def add(name, kind, document, valid, ctx=None):
        rows.append({"id": name, "request": {"contract": CONTRACT, "synthetic": True, "kind": kind,
                     "document": copy.deepcopy(document), "context": copy.deepcopy(context if ctx is None else ctx)},
                     "valid": valid})

    add("registry", "CaseRegistry", registry, True)
    add("manifest", "RunManifest", manifest, True)
    not_run = dict(case, verdict="NOT_RUN")
    passed = dict(case, verdict="PASS", run_manifest_ref=reference)
    add("not-run", "CaseResult", not_run, True)
    add("pass", "CaseResult", passed, True)
    failed_context = copy.deepcopy(context)
    failed_manifest = failed_context["manifests"][reference["path"]]
    failed_manifest["case_outcomes"][0]["verdict"] = "FAIL"
    add("fail", "CaseResult", dict(case, verdict="FAIL", run_manifest_ref=dict(reference, sha256=digest(failed_manifest))), True, failed_context)
    for applicability, rule in (("NOT_APPLICABLE", "fork-example"), ("NOT_ENABLED", "optional-example"), ("DEFERRED", "scope-example")):
        exception = dict(case, ac_id="AC-SCP-05-02", task_id="E04-02.optional_scope", network="ETH", profile="ethereum-sepolia",
                         profile_manifest_digest="d" * 64, fork_variant="synthetic-fork", fixture_kind="native-seam",
                         applicability=applicability, reason="Accepted rule for this synthetic optional-branch fixture.", rule_ref=rule)
        exception_context = copy.deepcopy(context)
        exception_context["registry"]["cases"] = [copy.deepcopy(exception)]
        add(applicability, "CaseResult", exception, True, exception_context)
        add(applicability + "-with-verdict", "CaseResult", dict(exception, verdict="PASS"), False, exception_context)
        add(applicability + "-with-run", "CaseResult", dict(exception, run_manifest_ref=reference), False, exception_context)
        add(applicability + "-no-reason", "CaseResult", {k: v for k, v in exception.items() if k != "reason"}, False, exception_context)
        add(applicability + "-unknown-rule", "CaseResult", dict(exception, rule_ref="unaccepted"), False, exception_context)
    for field, value in (("schema_version", 2), ("applicability", "UNKNOWN"), ("verdict", "SKIPPED"),
                         ("task_id", "E01-01.invented"), ("profile", "unknown-network"), ("fixture_version", "wrong")):
        add("invalid-" + field, "CaseResult", dict(not_run, **{field: value}), False)
    for field, value in (("run_manifest_ref", reference), ("actual_artifacts", [artifact]), ("started_at", manifest["started_at"])):
        add("not-run-fabricated-" + field, "CaseResult", dict(not_run, **{field: value}), False)
    add("pass-without-manifest", "CaseResult", dict(case, verdict="PASS"), False)
    add("pass-wrong-manifest-hash", "CaseResult", dict(passed, run_manifest_ref=dict(reference, sha256="f" * 64)), False)
    add("pass-outcome-mismatch", "CaseResult", dict(passed, verdict="FAIL"), False)
    for field in ("actual_artifacts", "expected_artifacts", "case_outcomes", "resource_deadline_manifest_digest", "phases"):
        add("manifest-missing-" + field, "RunManifest", {k: v for k, v in manifest.items() if k != field}, False)
    changed = copy.deepcopy(context)
    changed["artifacts"][artifact["path"]] = "changed bytes"
    add("manifest-changed-artifact", "RunManifest", manifest, False, changed)
    add("registry-duplicate", "CaseRegistry", dict(registry, cases=[case, case]), False)
    multidimensional = []
    for profile, family in PROFILES.items():
        for fork in ("synthetic-fork-a", "synthetic-fork-b"):
            for suite in ("quick", "functional"):
                multidimensional.append(dict(case, ac_id="AC-DEV-04-01", task_id="E07-09.requirement_verdict", network=family, profile=profile,
                    profile_manifest_digest=hashlib.sha256(profile.encode()).hexdigest(),
                    fork_variant=fork, fixture_kind="native-seam", suite=suite))
    add("registry-seven-profiles-two-forks-two-suites", "CaseRegistry", dict(registry, cases=multidimensional), True)
    wrong_family = copy.deepcopy(multidimensional)
    wrong_family[0]["network"] = "BSC"
    add("registry-family-mismatch", "CaseRegistry", dict(registry, cases=wrong_family), False)
    composite_cases = [next(row for row in multidimensional if row["profile"] == profile)
                       for profile in ("ethereum-sepolia", "bsc-testnet")]
    composite_registry = dict(registry, cases=composite_cases)
    composite_context = dict(context, registry=composite_registry, child_manifests={})
    children = []
    for number, child_case in enumerate(composite_cases):
        child_identity = {key: child_case[key] for key in IDENTITY}
        child_manifest = copy.deepcopy(manifest)
        child_manifest.update({key: child_case[key] for key in ("network", "profile", "profile_manifest_digest", "fork_variant", "fixture_kind", "suite", "task_id")})
        child_manifest.update(run_id="synthetic-child-" + str(number), parent_integration_run="synthetic-parent",
                              case_registry_digest=digest(composite_registry), case_identities=[child_identity],
                              case_outcomes=[{"identity": child_identity, "verdict": "PASS"}],
                              native_commits={"execution": "e" * 40}, image_digests={"execution": "sha256:" + "e" * 64},
                              compiler_versions={"go": "go1.25.0"}, architecture="x86_64")
        child_manifest.pop("build_applicability")
        path = "synthetic-child-" + str(number) + ".json"
        composite_context["child_manifests"][path] = child_manifest
        children.append({"path": path, "run_id": child_manifest["run_id"], "profile": child_case["profile"],
                         "sha256": digest(child_manifest), "verdict": "PASS"})
    child = children[0]
    parent = {"schema_version": 3, "run_id": "synthetic-parent", "verdict": "PASS", "correlation_id": "synthetic-correlation",
              "required_profiles": [row["profile"] for row in children],
              "children": children, "integration_outcome": {"scenario": "TC08", "verdict": "PASS"}}
    add("composite", "CompositeManifest", parent, True, composite_context)
    add("composite-missing-child", "CompositeManifest", dict(parent, children=[]), False, composite_context)
    add("composite-duplicate-child", "CompositeManifest", dict(parent, children=children + [child]), False, composite_context)
    failed_child_context = copy.deepcopy(composite_context)
    failed_child_manifest = failed_child_context["child_manifests"][child["path"]]
    failed_child_manifest["case_outcomes"][0]["verdict"] = "FAIL"
    failed_child = dict(child, verdict="FAIL", sha256=digest(failed_child_manifest))
    add("composite-recorded-failure", "CompositeManifest", dict(parent, verdict="FAIL", children=[failed_child, children[1]]), True, failed_child_context)
    add("composite-parent-cannot-hide-fail", "CompositeManifest", dict(parent, children=[failed_child, children[1]]), False, failed_child_context)
    add("composite-child-outcome-mismatch", "CompositeManifest", dict(parent, children=[dict(child, verdict="NOT_RUN"), children[1]]), False, composite_context)
    add("composite-missing-integration", "CompositeManifest", {k: v for k, v in parent.items() if k != "integration_outcome"}, False, composite_context)
    add("composite-wrong-child-hash", "CompositeManifest", dict(parent, children=[dict(child, sha256="f" * 64), children[1]]), False, composite_context)
    obligations = {"E01": ["native-seam"], "E03": ["delivery"], "E05": ["composed"]}
    plan = {"stages": [{"stage": f"E0{number}", "dependencies": [f"E0{number - 1}"] if number > 1 else [],
                        "evidence": obligations.get(f"E0{number}", [])} for number in range(1, 8)]}
    add("prerequisite-stages", "PrerequisitePlan", plan, True, {})
    missing_early = copy.deepcopy(plan)
    missing_early["stages"][0]["evidence"] = []
    add("missing-early-native-seam", "PrerequisitePlan", missing_early, False, {})
    future_dependency = copy.deepcopy(plan)
    future_dependency["stages"][0]["dependencies"] = ["E05"]
    add("future-prerequisite", "PrerequisitePlan", future_dependency, False, {})
    partial = {"stages": [copy.deepcopy(plan["stages"][0]), dict(plan["stages"][4], dependencies=[])]}
    add("acyclic-stage-control", "PrerequisitePlan", partial, True, {})
    forward = copy.deepcopy(partial)
    forward["stages"][0]["dependencies"] = ["E05"]
    add("acyclic-forward-prerequisite", "PrerequisitePlan", forward, False, {})
    return rows


def validate_outputs(candidate):
    root = Path(candidate).resolve()
    hashes = {}
    for name in OUTPUTS:
        path = root / name
        check(path.is_file() and not path.is_symlink() and path.resolve().is_relative_to(root), "missing or unsafe task output: " + name)
        check(path.stat().st_size <= 1024 * 1024, "task output exceeds the artifact bound")
        hashes[name] = hashlib.sha256(path.read_bytes()).hexdigest()
    profiles = json.loads((root / "test/evidence/profile-registry.json").read_text())
    check(profiles == {"schema_version": 3, "profiles": PROFILES}, "profile registry differs from the seven accepted profiles")
    for filename, fields in ((OUTPUTS[0], {"schema_version", "version", "cases"}),
                             (OUTPUTS[1], set(IDENTITY) | {"schema_version", "task_id", "fixture_id", "fixture_version", "applicability"}),
                             (OUTPUTS[2], {"schema_version", "run_id", "case_identities", "case_outcomes", "profile", "phases"})):
        schema = json.loads((root / filename).read_text())
        check(schema.get("type") == "object" and fields <= set(schema.get("required", []))
              and schema.get("properties", {}).get("schema_version", {}).get("const") == 3,
              "schema does not declare the required v3 fields: " + filename)
    return hashes


def candidate_call(command, request):
    def limits():
        resource.setrlimit(resource.RLIMIT_FSIZE, (65536, 65536))
    with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
        result = subprocess.run(command, input=canonical(request), stdout=stdout, stderr=stderr,
                                timeout=5, preexec_fn=limits)
        stdout.seek(0)
        stderr.seek(0)
        output, errors = stdout.read(65536), stderr.read(65536)
        check(len(output) < 65536 and len(errors) < 65536,
              "candidate output limit reached on stdout or stderr")
        return result.returncode, output, errors


def execute(candidate):
    root = Path(candidate).resolve()
    outputs = validate_outputs(root)
    command = [sys.executable, "-I", str(root / "test/evidence/validate.py")]
    outcomes = []
    for vector in vectors():
        code, stdout, stderr = candidate_call(command, vector["request"])
        expected = 0 if vector["valid"] else 2
        outcomes.append({"id": vector["id"], "expected_exit": expected, "actual_exit": code,
                         "stdout_sha256": hashlib.sha256(stdout).hexdigest(), "stderr_sha256": hashlib.sha256(stderr).hexdigest()})
        check(code == expected, "candidate validation disagreed with trusted vector: " + vector["id"])
    for scope in ("ALL", "ETH", "BSC", "POLY"):
        code, stdout, stderr = candidate_call(command, {"contract": CONTRACT, "synthetic": True, "kind": "ExpandProfiles", "scope": scope})
        expected = sorted(profile for profile, family in PROFILES.items() if scope in {"ALL", family})
        check(code == 0 and json.loads(stdout) == expected, "candidate did not expand profile scope: " + scope)
        outcomes.append({"id": "expand-" + scope, "expected_exit": 0, "actual_exit": code,
                         "stdout_sha256": hashlib.sha256(stdout).hexdigest(), "stderr_sha256": hashlib.sha256(stderr).hexdigest()})
    check(validate_outputs(root) == outputs, "validation changed candidate outputs")
    return {"contract": CONTRACT, "task_id": TASK, "scope": "artifact-conformance", "runtime": "NOT_RUN",
            "synthetic_inputs": True, "vector_sha256": digest(vectors()), "outputs": outputs, "outcomes": outcomes}


def validate_report(report):
    check(report["contract"] == CONTRACT and report["task_id"] == TASK and report["scope"] == "artifact-conformance"
          and report["runtime"] == "NOT_RUN" and report["synthetic_inputs"] is True
          and report["vector_sha256"] == digest(vectors()), "task report identity or vector pack differs")
    expected = {row["id"]: 0 if row["valid"] else 2 for row in vectors()}
    expected.update({"expand-" + scope: 0 for scope in ("ALL", "ETH", "BSC", "POLY")})
    rows = report["outcomes"]
    check(len(rows) == len(expected) and {row["id"] for row in rows} == set(expected), "task report is missing or duplicates a required check")
    check(all(row["actual_exit"] == row["expected_exit"] == expected[row["id"]] for row in rows), "task report contains a failed check")
    check(set(report["outputs"]) == set(OUTPUTS), "task report omits required output hashes")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", required=True, type=Path)
    args = parser.parse_args()
    try:
        print(json.dumps(execute(args.candidate), sort_keys=True))
        return 0
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError) as error:
        print(json.dumps({"contract": CONTRACT, "status": "FAIL", "reason": str(error), "runtime": "NOT_RUN"}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
