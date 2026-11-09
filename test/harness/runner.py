#!/usr/bin/env python3
"""Admit and run a frozen local native-seam RunPlan through the accepted family slices.

Admission covers only private test-only fixtures. It never grants public-profile
credit or an AC verdict; every CaseResult it writes stays NOT_RUN.
"""

import argparse
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import sys
import time
import uuid

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from artifacts import artifact, check_deadline, json_artifact, read_bytes, reference, require
from owned import group_absent
import profile_manifest
import run_plan

evidence = run_plan.evidence
JSON_LIMIT = 16 * 1024 * 1024
ARTIFACT_LIMIT = 1024 * 1024 * 1024
LABEL = "extractor.harness"
CHILD_GRACE_SECONDS = 30
CHILDREN = {
    "geth-import": {"script": "run.py", "proof": "local-native-import", "tool": False},
    "bsc-parlia-import": {"script": "bsc_run.py", "proof": "bsc-local-native-import", "tool": True},
    "bor-import": {"script": "bor_run.py", "proof": "bor-local-native-import", "tool": True},
}
REMAINING_BLOCKERS = ["private_identity_has_no_public_profile_credit", "independent_oracle_review_required",
                      "harness_owns_no_ac_verdict", "delivery_reader_boundary_not_run_for_native_seam"]


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


def canonical_ref(path, value):
    data = evidence.canonical(value)
    with open(path, "xb") as handle:
        handle.write(data)
    return {"path": str(path), "sha256": hashlib.sha256(data).hexdigest(), "size_bytes": len(data)}


def frozen_plan(directory, resolution_sha256, deadline):
    """Re-authenticate a run_plan freeze directory and return its plan, registry and report."""
    require(directory.is_absolute(), "Frozen plan directory must be absolute")
    raw = read_bytes(directory / "resolution.json", JSON_LIMIT, deadline)
    require(hashlib.sha256(raw).hexdigest() == resolution_sha256, "Resolution bytes do not match the external pin")
    report = evidence.parse_json(raw.decode("utf-8"))
    require(report.get("kind") == "offline-run-plan-resolution" and report.get("plan_valid") is True and
            report.get("runtime_admitted") is False, "Not a valid offline RunPlan resolution")
    unsigned = {key: value for key, value in report.items() if key != "resolution_digest"}
    require(report.get("resolution_digest") == evidence.digest(unsigned), "Resolution digest changed")
    values = {}
    for name, key in (("plan.json", "plan"), ("registry.json", "registry_bundle"), (run_plan.SCHEMA_NAME, "schema")):
        data = read_bytes(directory / name, JSON_LIMIT, deadline)
        require(hashlib.sha256(data).hexdigest() == report[key]["sha256"] and len(data) == report[key]["size_bytes"],
                "Frozen " + name + " differs from the resolution")
        values[key] = evidence.parse_json(data.decode("utf-8"))
    for ref in report["artifacts"]:
        artifact(ref, ARTIFACT_LIMIT, deadline=deadline)
    require(evidence.digest(values["registry_bundle"]["document"]) == report["registry_digest"], "Registry digest changed")
    return values["plan"], values["registry_bundle"], report


def memory_bytes():
    if sys.platform == "darwin":
        value = subprocess.run(["sysctl", "-n", "hw.memsize"], capture_output=True, text=True, timeout=5, check=True).stdout
        return int(value.strip())
    return os.sysconf("SC_PAGE_SIZE") * os.sysconf("SC_PHYS_PAGES")


def docker_state():
    info = subprocess.run(["docker", "info", "--format", "{{json .}}"], capture_output=True, text=True, timeout=20)
    require(info.returncode == 0, "Docker is unavailable")
    value = json.loads(info.stdout)
    owned = subprocess.run(["docker", "ps", "-aq", "--filter", "label=" + LABEL], capture_output=True, text=True, timeout=20)
    require(owned.returncode == 0, "Owned container query failed")
    return {"cpus": value["NCPU"], "memory_bytes": value["MemTotal"], "architecture": value["Architecture"],
            "harness_containers": sorted(owned.stdout.split())}


def host_resources(output_parent):
    machine = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(platform.machine().lower())
    return {"architecture": machine, "cpus": os.cpu_count(), "memory_bytes": memory_bytes(),
            "free_disk_bytes": shutil.disk_usage(output_parent).free, "docker": docker_state()}


def check_resources(plan, observed):
    wanted = plan["resources"]
    docker = observed["docker"]
    docker_arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(docker["architecture"], docker["architecture"])
    require(observed["architecture"] == wanted["architecture"] == docker_arch, "Host or Docker architecture differs from the plan")
    require(min(observed["cpus"], docker["cpus"]) >= wanted["cpus"], "Fewer CPUs than the plan declares")
    require(min(observed["memory_bytes"], docker["memory_bytes"]) >= wanted["ram_bytes"], "Less memory than the plan declares")
    require(observed["free_disk_bytes"] >= wanted["disk_bytes"], "Less free disk than the plan declares")
    require(wanted["parallelism"] == 1, "This runner dispatches fixtures serially; parallelism must be 1")
    require(docker["harness_containers"] == [], "Another harness run owns containers; one writer is required")


def bind_fixtures(plan, registry, deadline):
    """Verify each declared test-only ProfileManifest and bind it to one plan fixture."""
    require(plan["fixture_kind"] == "native-seam", "Only local native-seam plans can be admitted")
    require(plan["mode"] == "standard" and plan["terminal_fence"] is None, "Native-seam admission needs a standard plan without a fence")
    profiles = set(plan["profiles"])
    rows = {row["profile"]: row for row in plan["profile_manifests"]}
    require(rows.keys() == profiles, "Every selected profile needs one test-only ProfileManifest")
    fixtures = {row["profile"]: [] for row in plan["fixtures"]}
    for row in plan["fixtures"]:
        fixtures[row["profile"]].append(row)
    bound = []
    for profile in sorted(profiles):
        row = rows[profile]
        _, manifest = json_artifact(row["artifact"], JSON_LIMIT, deadline=deadline)
        digest = profile_manifest.verify(manifest, deadline)
        require(digest == row["profile_manifest_digest"] and manifest["profile"] == profile,
                "Declared ProfileManifest digest or profile differs from the verified manifest")
        require(len(fixtures.get(profile, [])) == 1, "Each profile needs exactly one local fixture")
        fixture = fixtures[profile][0]
        identity = fixture["identity"]
        require(identity["kind"] == "test-only" and fixture["public_correspondence"] is None, "Only test-only fixtures can be admitted")
        require(all(fixture[key] == manifest["fixture"][key] for key in ("fixture_id", "fixture_version", "fork_variant")),
                "Plan fixture identity differs from the ProfileManifest")
        require(identity["chain_id"] == manifest["identity"]["chain_id"] and identity["genesis_hash"] == manifest["identity"]["genesis_hash"],
                "Plan chain identity differs from the ProfileManifest")
        require(fixture["preparation"] == manifest["preparation"] and fixture["oracle"] == manifest["oracle"] and
                sorted(map(evidence.canonical, fixture["inputs"])) == sorted(map(evidence.canonical, manifest["inputs"])),
                "Plan preparation, oracle or inputs differ from the ProfileManifest")
        actions = [action for action in plan["actions"] if action["profile"] == profile]
        require(len(actions) == 1 and actions[0]["kind"] == "native-import" and actions[0]["count"] == 1 and
                actions[0]["input"] == manifest["runner"]["request"] and actions[0]["expected"] == manifest["oracle"],
                "Each profile needs one native-import action bound to its runner request and oracle")
        _, request = json_artifact(manifest["runner"]["request"], JSON_LIMIT, deadline=deadline)
        cases = [case for case in plan["case_identities"] if case["profile"] == profile]
        require(cases and all(case["profile_manifest_digest"] == digest for case in cases), "Case identities use another ProfileManifest")
        bound.append({"profile": profile, "manifest": row["artifact"], "manifest_digest": digest, "fixture_id": fixture["fixture_id"],
                      "slice": manifest["runner"]["slice"], "request": manifest["runner"]["request"],
                      "observer_tool": manifest["runner"]["observer_tool"], "deadline_seconds": request["deadline_seconds"]})
    budget = sum(row["deadline_seconds"] + CHILD_GRACE_SECONDS for row in bound)
    require(budget <= plan["deadlines"]["measured_seconds"] <= 1800, "Child deadlines exceed the parent measured budget")
    require(plan["faults"] == [] and plan["stop"]["driver_actions"] == len(bound), "Local native-seam admission has no fault actions")
    return bound


def admit(directory, resolution_sha256, output, deadline):
    started = now()
    plan, bundle, report = frozen_plan(directory, resolution_sha256, deadline)
    bound = bind_fixtures(plan, bundle["document"], deadline)
    require(output.is_absolute() and not output.exists() and output.parent.is_dir(), "Output must be a new absolute directory")
    observed = host_resources(output.parent)
    check_resources(plan, observed)
    os.mkdir(output, 0o700)
    blockers = [item for item in report["blockers"] if item not in (
        "runtime_execution_unimplemented", "profile_manifest_verification_unimplemented",
        "native_and_supporting_readiness_unverified", "resource_availability_unverified",
        "build_fixture_compatibility_unverified", "profile_manifest_missing")]
    record = {"schema_version": 1, "kind": "local-native-seam-admission", "admission_id": uuid.uuid4().hex,
              "runtime_admitted": True, "scope": "private-local-native-seam", "public_profile_credit": False,
              "plan": report["plan"], "resolution_digest": report["resolution_digest"], "registry_digest": report["registry_digest"],
              "fixtures": bound, "resources": {"declared": plan["resources"], "observed": observed},
              "deadlines": plan["deadlines"], "started_at": started, "finished_at": now(),
              "blockers": sorted(set(blockers) | set(REMAINING_BLOCKERS))}
    record["admission_digest"] = evidence.digest(record)
    canonical_ref(output / "admission.json", record)
    return plan, bundle, record


def child_command(row, out):
    rules = CHILDREN[row["slice"]]
    script = Path(rules["script"]) if os.path.isabs(rules["script"]) else HERE / rules["script"]
    command = [sys.executable, "-I", str(script), "run", row["request"]["path"]]
    if rules["tool"]:
        command += ["--observer-tool", row["observer_tool"]["path"], "--observer-tool-sha256", row["observer_tool"]["sha256"]]
    return command + ["--output", str(out)]


def run_child(row, out, parent_deadline):
    """Run one family slice in its own process group; kill the group at the parent deadline."""
    command = child_command(row, out)
    limit = min(row["deadline_seconds"] + CHILD_GRACE_SECONDS, parent_deadline - time.monotonic())
    require(limit > 0, "Parent measured budget expired before dispatch")
    started, start = now(), time.monotonic()
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    reason = None
    try:
        stdout, stderr = process.communicate(timeout=limit)
    except subprocess.TimeoutExpired:
        reason = "PARENT_DEADLINE"
        for number in (signal.SIGTERM, signal.SIGKILL):
            try:
                os.killpg(process.pid, number)
            except ProcessLookupError:
                pass
            try:
                stdout, stderr = process.communicate(timeout=10)
                break
            except subprocess.TimeoutExpired:
                continue
    absent = group_absent(process.pid)
    record = {"profile": row["profile"], "slice": row["slice"], "command": command, "started_at": started, "finished_at": now(),
              "elapsed_seconds": time.monotonic() - start, "exit_code": process.returncode, "reason": reason,
              "process_group_absent": absent, "stdout": stdout.decode("utf-8", "replace")[-4096:],
              "stderr": stderr.decode("utf-8", "replace")[-4096:], "result": "FAIL", "proof": None}
    proof_path = out / "proof.json"
    if reason is None and proof_path.is_file():
        record["proof"] = reference(proof_path, JSON_LIMIT)
        proof = json.loads(proof_path.read_bytes())
        passed = (proof.get("kind") == CHILDREN[row["slice"]]["proof"] and proof.get("result") == "PASS" and
                  proof.get("cleanup_complete") is True and proof.get("request") == row["request"] and process.returncode == 0)
        record["result"] = "PASS" if passed and absent else "FAIL"
    return record


def teardown():
    owned = docker_state()["harness_containers"]
    forced = False
    if owned:
        forced = True
        subprocess.run(["docker", "rm", "-f", *owned], capture_output=True, timeout=60)
        owned = docker_state()["harness_containers"]
    return {"forced_container_removal": forced, "remaining_harness_containers": owned, "cleanup_complete": owned == []}


def self_check(kind, document, bundle, artifacts=None):
    """Validate one written evidence document against the frozen registry and its owners."""
    context = {**bundle["context"], "registry": bundle["document"], "artifacts": artifacts or {}}
    evidence.validate_bundle({"contract": bundle["contract"], "kind": kind, "document": document, "context": context})


def case_results(plan, bundle, output):
    known = {evidence.identity(case): case for case in bundle["document"]["cases"]}
    rows = []
    os.mkdir(output / "case-results", 0o700)
    for index, identity in enumerate(plan["case_identities"]):
        case = known[evidence.identity(identity)]
        result = {"schema_version": 3, **{field: case[field] for field in evidence.IDENTITY_FIELDS},
                  "task_id": case["task_id"], "fixture_id": case["fixture_id"], "fixture_version": case["fixture_version"],
                  "applicability": "APPLICABLE", "verdict": "NOT_RUN",
                  "reason": "Harness runner evidence only; the owning task issues this AC verdict"}
        self_check("CaseResult", result, bundle)
        rows.append(canonical_ref(output / "case-results" / f"{index:04d}.json", result))
    return rows


def text_ref(path):
    data = Path(path).read_bytes()
    text = data.decode("utf-8")
    return {"path": str(path), "sha256": hashlib.sha256(data).hexdigest()}, text


def reader_boundaries(plan, bundle, frozen, output):
    """Write one D87 boundary per profile. A native-seam run has no delivery, so run_started is false."""
    os.mkdir(output / "reader-boundaries", 0o700)
    plan_ref, plan_text = text_ref(frozen / "plan.json")
    deadlines = plan["deadlines"]
    caps = {"startup": deadlines["startup_seconds"], "observation": deadlines["event_seconds"],
            "shutdown_drain": deadlines["teardown_seconds"], "stream_closure": deadlines["teardown_seconds"],
            "reconstruction": deadlines["comparison_seconds"], "comparison": deadlines["comparison_seconds"]}
    reason = "Native-seam plan: no product capture, Gateway or NATS delivery exists at E01"
    rows = []
    for profile in sorted(set(plan["profiles"])):
        cases = [case for case in plan["case_identities"] if case["profile"] == profile]
        oracle = next(row["oracle"] for row in plan["fixtures"] if row["profile"] == profile)
        oracle_ref, oracle_text = text_ref(oracle["path"])
        path = output / "reader-boundaries" / f"{profile}.json"
        boundary = {"schema_version": 1, "boundary_id": f"{plan['plan_id']}-{profile}", "artifact_path": str(path),
                    **{field: cases[0][field] for field in ("network", "profile", "profile_manifest_digest", "fork_variant", "fixture_kind", "suite")},
                    "case_identities": cases, "run_started": False, "run_id": None, "run_manifest_path": None,
                    "run_plan_ref": plan_ref, "oracle_ref": oracle_ref,
                    "expectation": {"mode": "positive_delivery", "predicate_ref": None},
                    "source_boundaries": [], "delivery_boundaries": [], "streams": [],
                    "logical_comparison": {"expected_ref": None, "actual_ref": None, "diff_ref": None},
                    "phase_times": {name: {"started_at": None, "finished_at": None, "deadline_seconds": cap} for name, cap in caps.items()},
                    "conditions": {name: {"status": "NOT_RUN", "evidence_refs": [], "reason": reason, "missing_obligations": []}
                                   for name in ("source_closure", "delivery_closure", "terminal_fence", "stream_coverage",
                                                "reconstruction", "semantic_comparison")},
                    "reader_complete": False, "comparison_passed": None}
        self_check("ReaderBoundaryManifest", boundary, bundle, {plan_ref["path"]: plan_text, oracle_ref["path"]: oracle_text})
        rows.append(canonical_ref(path, boundary))
    return rows


def retry_attempt(previous, previous_sha256, reason, resolution_sha256, deadline):
    """D83 allows one diagnostic retry, linked to a retained first FAIL of the same frozen plan."""
    if previous is None:
        require(previous_sha256 is None and reason is None, "Retry metadata without a previous run")
        return {"attempt_index": 1}
    require(type(reason) is str and 10 <= len(reason) <= 2000 and previous_sha256 is not None, "A pinned first run and retry reason are required")
    raw = read_bytes(previous, JSON_LIMIT, deadline)
    require(hashlib.sha256(raw).hexdigest() == previous_sha256, "Previous run record does not match its pin")
    prior = evidence.parse_json(raw.decode("utf-8"))
    require(prior.get("kind") == "local-native-seam-run" and prior.get("result") == "FAIL" and
            prior.get("attempt") == {"attempt_index": 1} and prior.get("resolution_sha256") == resolution_sha256,
            "Only a first FAIL of the same frozen plan can be retried once")
    return {"attempt_index": 2, "first_run_id": prior["run_id"], "first_run_sha256": previous_sha256, "retry_reason": reason}


def run(directory, resolution_sha256, output, previous=None, previous_sha256=None, retry_reason=None):
    """Admit, dispatch every fixture serially, then write the parent record and NOT_RUN CaseResults."""
    attempt = retry_attempt(previous, previous_sha256, retry_reason, resolution_sha256, time.monotonic() + 30)
    preparation_start = now()
    admission_deadline = time.monotonic() + 120
    plan, bundle, admission = admit(directory, resolution_sha256, output, admission_deadline)
    preparation_end = now()
    execution_start = now()
    parent_deadline = time.monotonic() + plan["deadlines"]["measured_seconds"]
    os.mkdir(output / "children", 0o700)
    children = []
    for row in admission["fixtures"]:
        children.append(run_child(row, output / "children" / row["fixture_id"], parent_deadline))
        if children[-1]["result"] != "PASS":
            break
    execution_end = now()
    teardown_start = now()
    cleanup = teardown()
    teardown_end = now()
    dispatched = {row["profile"] for row in children}
    complete = len(children) == len(admission["fixtures"])
    passed = complete and all(row["result"] == "PASS" for row in children) and cleanup["cleanup_complete"]
    record = {"schema_version": 1, "kind": "local-native-seam-run", "run_id": uuid.uuid4().hex,
              "admission": reference(output / "admission.json", JSON_LIMIT), "admission_digest": admission["admission_digest"],
              "result": "PASS" if passed else "FAIL", "public_profile_credit": False,
              "children": children, "not_dispatched": sorted({row["profile"] for row in admission["fixtures"]} - dispatched),
              "teardown": cleanup, "attempt": attempt, "resolution_sha256": resolution_sha256,
              "phases": {"preparation": {"started_at": preparation_start, "finished_at": preparation_end},
                         "execution": {"started_at": execution_start, "finished_at": execution_end},
                         "teardown": {"started_at": teardown_start, "finished_at": teardown_end}},
              "measured_seconds_limit": plan["deadlines"]["measured_seconds"], "blockers": admission["blockers"]}
    record["case_results"] = case_results(plan, bundle, output)
    record["reader_boundaries"] = reader_boundaries(plan, bundle, directory, output)
    canonical_ref(output / "run-record.json", record)
    return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("admit", "run"))
    parser.add_argument("frozen_plan", type=Path, help="absolute run_plan.py freeze directory")
    parser.add_argument("--resolution-sha256", required=True)
    parser.add_argument("--output", type=Path, required=True, help="new absolute directory owned by this run")
    parser.add_argument("--previous-run", type=Path, help="retained first FAIL run-record.json for the single D83 retry")
    parser.add_argument("--previous-sha256")
    parser.add_argument("--retry-reason")
    args = parser.parse_args()
    try:
        require(args.command == "run" or args.previous_run is None, "Only run accepts retry metadata")
        if args.command == "admit":
            _, _, record = admit(args.frozen_plan, args.resolution_sha256, args.output, time.monotonic() + 120)
            print(json.dumps({"runtime_admitted": True, "public_profile_credit": False, "admission": str(args.output / "admission.json")}))
            return 0
        record = run(args.frozen_plan, args.resolution_sha256, args.output, args.previous_run, args.previous_sha256, args.retry_reason)
        print(json.dumps({"result": record["result"], "public_profile_credit": False, "record": str(args.output / "run-record.json")}))
        return 0 if record["result"] == "PASS" else 2
    except (OSError, ValueError, KeyError, TypeError, RecursionError, subprocess.SubprocessError) as error:
        print(json.dumps({"runtime_admitted": False, "public_profile_credit": False, "error": str(error)}))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
