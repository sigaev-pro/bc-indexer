"""Trusted E01 isolated build execution and updater acceptance, never a generic PASS."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

# -I removes the script directory. Add only this frozen trusted source directory.
if __name__ == "__main__":
    sys.path.insert(0, str(Path(__file__).resolve().parent))

import build_contract as contract
import build_execution
import build_materials
from build_product_commands import Commands
import build_resources
import build_update_execution
import build_vectors


TASK, CONTRACT, OUTPUTS = contract.TASK, contract.CONTRACT, contract.OUTPUTS
PACK_MODULES = ("acceptance", "acceptance_receipts", "build_contract", "build_execution", "build_materials", "build_os",
                "build_product_commands", "build_resources", "build_task_verifier", "build_update_execution", "build_update_vectors",
                "build_updates", "build_vectors", "check_docs", "check_environment", "ci", "controller", "controller_backend",
                "controller_handoff", "evidence", "evidence_task_verifier", "launch_control", "reader_evidence_vectors", "session_runner", "task_proofs")
PACK_FILES = tuple(".project/automation/" + name + ".py" for name in PACK_MODULES) + (
    ".project/automation/build-task-contract.md", ".project/automation/task-recipes.json",
    ".project/automation/evidence-task-contract.md", ".project/automation/reader-evidence-contract.md",
    ".project/bootstrap/components/registry.json", ".project/bootstrap/components/candidate-lock.json",
    ".project/bootstrap/components/verify.py", ".project/agency/roles.py",
    ".project/agency/sources.lock.json", ".project/agency/toolchain.lock.json")
MANIFEST_KEYS = {"schema_version", "task_id", "contract", "evidence_kind", "plan", "plan_sha256", "builds", "identity", "image_inputs", "host_tools",
                 "module_closures", "os_closures", "profile_bindings", "updater", "cleanup", "commands", "runtime"}
REPORT_KEYS = {"contract", "task_id", "scope", "runtime", "evidence_kind", "control_inputs", "identity", "platform_results",
               "inputs", "trusted_pack", "outputs", "outcomes", "manifest", "manifest_sha256", "manifest_ref", "materialized_root",
               "cleanup", "updater_authentication"}


def pack(source):
    return {name: contract.file_hash(Path(source) / name) for name in PACK_FILES}


def validate_outputs(candidate):
    result = {}
    for name in OUTPUTS:
        path = contract.safe_file(candidate, name)
        contract.require(path.stat().st_size <= 1024 * 1024, "task output exceeds its source-file bound: " + name)
        result[name] = contract.file_hash(path)
    schema = contract.parse_json((Path(candidate) / "build/native/build-manifest.schema.json").read_bytes())
    version = schema.get("properties", {}).get("schema_version", {}).get("const")
    contract.require(schema.get("type") == "object" and schema.get("additionalProperties") is False
                     and set(schema.get("required", [])) == MANIFEST_KEYS and type(version) is int and version == 1,
                     "build manifest schema does not declare the closed versioned contract")
    return result


def final_preflights(commands, manifest, root, source):
    for profile in manifest["plan"]["profiles"]:
        commands.preflight("actual-accept-" + profile["id"], manifest, root, profile["id"])
    # Candidate commands and their process groups are finished before this last
    # trusted read of every retained materialized artifact.
    build_materials.validate_materialized(manifest, root, source, time.time())


def expected_outcomes():
    expected = {"plan-amd64": 0, "plan-arm64": 0, "plan-bounded-resources": 0}
    expected.update({"plan-reject-" + name: 2 for name in ("unknown-platform", "boolean-cpu", "unbounded-cpu", "unknown-field")})
    profiles = ("ethereum-mainnet", "ethereum-sepolia", "ethereum-hoodi", "bsc-mainnet", "bsc-testnet", "polygon-pos-mainnet", "polygon-pos-amoy")
    expected.update({"synthetic-accept-" + profile: 0 for profile in profiles})
    expected.update({"actual-accept-" + profile: 0 for profile in profiles})
    expected.update({"synthetic-reject-" + name: 2 for name, _ in build_vectors.mutations()})
    expected.update({"synthetic-reject-identity-" + key: 2 for key in ("head_sha", "base_sha", "verifier_source_commit")})
    expected.update({"synthetic-reject-" + name: 2 for name in ("profile", "manifest-digest", "symlink")})
    expected["synthetic-cannot-be-actual"] = 2
    expected.update({"synthetic-update-check": 0, "synthetic-update-create": 0, "live-update-check": 0, "live-update-create": 0})
    expected.update({"synthetic-update-reject-" + name: 1 for name in ("unavailable", "malformed", "unstable-only")})
    expected.update({"synthetic-update-reject-" + name: 2 for name in ("active-target", "stale-lock")})
    return expected


def cleanup_history(spec, history, image_inputs=None):
    contract.require(type(history) is list and history, "build cleanup history is missing")
    removed = set()
    for report in history:
        build_resources.validate_cleanup(spec, report)
        for row in report["resources"]:
            contract.require(row["id"] not in removed, "build cleanup repeats an already removed immutable container")
            removed.add(row["id"])
            if image_inputs is not None:
                allowed = {item["reference"]: "sha256:" + item["config"]["sha256"] for item in image_inputs.values()}
                contract.require(row["image_id"] == allowed[row["image_reference"]], "owned container used another platform image config")
    intents = history[-1]["creation_inventory"]["intents"]
    steps = {row["name"].removeprefix("extractor-" + spec["nonce"][:24] + "-") for row in intents}
    contract.require(steps == {"go-" + name for name in contract.MODULES} | {"runtime-" + name for name in contract.NATIVE}
                     and len(intents) == 15 and removed == {row["container_id"] for row in intents},
                     "build cleanup did not account for every task-owned compiler and runtime container")


def static_references(value, references=None):
    references = {} if references is None else references
    if type(value) is dict:
        if set(value) == {"path", "sha256", "size_bytes"}:
            name = value["path"]
            contract.require(isinstance(name, str) and name and not name.startswith("/") and "\\" not in name
                             and all(part not in {"", ".", ".."} for part in name.split("/"))
                             and isinstance(value["sha256"], str) and contract.HEX.fullmatch(value["sha256"])
                             and type(value["size_bytes"]) is int and value["size_bytes"] >= 0, "retained proof has an invalid artifact reference")
            contract.require(name not in references or references[name] == value, "retained proof gives different identities to one artifact path")
            references[name] = value
        else:
            for child in value.values():
                static_references(child, references)
    elif type(value) is list:
        for child in value:
            static_references(child, references)
    return references


def validate_report(report):
    contract.require(type(report) is dict and set(report) == REPORT_KEYS and report["contract"] == CONTRACT and report["task_id"] == TASK
                     and report["scope"] == "isolated-native-builds" and report["runtime"] == "NOT_RUN"
                     and report["evidence_kind"] == "actual-build" and report["control_inputs"] == "synthetic-protocol-only"
                     and report["platform_results"] == {"linux/amd64": "BUILT", "linux/arm64": "NOT_RUN"}, "build report identity or actual execution scope differs")
    contract.require(set(report["trusted_pack"]) == set(PACK_FILES)
                     and all(isinstance(value, str) and contract.HEX.fullmatch(value) for value in report["trusted_pack"].values()), "build report trusted dependency pack is incomplete")
    contract.require(set(report["outputs"]) == set(OUTPUTS) and all(contract.HEX.fullmatch(value) for value in report["outputs"].values()), "build report omits a required product output")
    contract.require(type(report["inputs"]) is dict and set(report["inputs"]) == {"registry_json", "lock_json"}, "build report lacks retained exact input bytes")
    registry, lock = (contract.parse_json(report["inputs"][key]) for key in ("registry_json", "lock_json"))
    registry_hash = hashlib.sha256(report["inputs"]["registry_json"].encode()).hexdigest()
    lock_hash = hashlib.sha256(report["inputs"]["lock_json"].encode()).hexdigest()
    contract.require(registry_hash == report["trusted_pack"][".project/bootstrap/components/registry.json"]
                     and lock_hash == report["trusted_pack"][".project/bootstrap/components/candidate-lock.json"]
                     and lock["registry_sha256"] == registry_hash and len(registry["components"]) == len(lock["components"]) == 41
                     and {row["id"] for row in registry["components"]} == set(lock["components"]), "build report source inputs differ from its frozen pack")
    manifest = report["manifest"]
    contract.validate_manifest(manifest, None, None, input_snapshot=(registry, lock, registry_hash, lock_hash),
                               reference_check=lambda root, value: contract.reference_descriptor(value))
    contract.require(type(manifest) is dict and set(manifest) == MANIFEST_KEYS and type(manifest["schema_version"]) is int
                     and manifest["schema_version"] == 1 and manifest["task_id"] == TASK and manifest["contract"] == CONTRACT
                     and manifest["evidence_kind"] == "actual-build" and manifest["runtime"] == "NOT_RUN"
                     and contract.digest(manifest) == report["manifest_sha256"] == report["manifest_ref"]["sha256"], "retained build manifest differs or is synthetic")
    plan = contract.expected_plan(registry, lock, registry_hash, lock_hash, "linux/amd64", manifest["plan"]["limits"])
    contract.require(manifest["plan"] == plan and manifest["plan_sha256"] == contract.digest(plan)
                     and manifest["identity"] == report["identity"], "build manifest plan or execution identity differs")
    contract.require(set(manifest["builds"]) == set(contract.NATIVE) and set(manifest["module_closures"]) == set(contract.MODULES)
                     and set(manifest["os_closures"]) == set(contract.OS_IMAGES) and set(manifest["image_inputs"]) == set(contract.IMAGES), "build proof omits a required output or closure")
    for name, row in manifest["builds"].items():
        contract.require(row["source_commit"] == plan["sources"][name]["commit"]
                         and row["compiler_image"] == plan["images"]["golang-build-image"]["reference"]
                         and row["repeat_build"] == "NOT_RUN" and row["binary"]["size_bytes"] > 64
                         and row["oci_layer"]["size_bytes"] > row["binary"]["size_bytes"], "build proof lacks materialized native/OCI output identity")
    contract.require(manifest["profile_bindings"] == build_vectors.bindings(plan, manifest["builds"]), "build proof swaps or omits a profile binding")
    static_references(manifest)
    for row in manifest["commands"]:
        contract.require(type(row["exit_code"]) is int and row["exit_code"] == 0 and isinstance(row["argv"], list) and row["argv"], "build proof hides a failed or unexecuted native command")
    builds = [row for row in manifest["commands"] if "/usr/local/go/bin/go" in row["argv"]
              and row["argv"][row["argv"].index("/usr/local/go/bin/go") + 1:][:1] == ["build"]]
    contract.require(len(builds) == 5 and all(any(build_execution.TARGETS[name] == row["argv"][-1]
                     and plan["sources"][name]["commit"] in row["argv"][row["argv"].index("-ldflags") + 1] for row in builds) for name in contract.NATIVE),
                     "build proof does not contain every actual fixed native build")
    expected = expected_outcomes()
    contract.require(type(report["outcomes"]) is list and len(report["outcomes"]) == len(expected)
                     and {row["id"] for row in report["outcomes"]} == set(expected)
                     and all(type(row["actual_exit"]) is int and type(row["expected_exit"]) is int
                             and row["actual_exit"] == row["expected_exit"] == expected[row["id"]]
                             and row.get("cleanup_confirmed") is True
                             and type(row.get("process_group")) is int and row["process_group"] > 0
                             for row in report["outcomes"]), "build report is missing independent checks or candidate process cleanup")
    cleanup = report["cleanup"]
    contract.require(set(cleanup) == {"spec", "history"} and cleanup["spec"]["nonce"] == report["identity"]["nonce"]
                     and cleanup["spec"]["run_id"] == report["identity"]["run_id"]
                     and cleanup["spec"]["operation_id"] == report["identity"]["operation_id"], "cleanup proof belongs to another build execution")
    cleanup_history(cleanup["spec"], cleanup["history"], manifest["image_inputs"])
    authentication = report["updater_authentication"]
    build_update_execution.validate_authentication(authentication, None, manifest["commands"], None, retained=True)
    static_references(authentication, static_references(manifest))
    contract.reference_descriptor(report["manifest_ref"])
    contract.require(report["manifest_ref"]["size_bytes"] == len(contract.canonical(manifest)), "retained manifest size differs")
    contract.require(authentication["synthetic"] is False and authentication["verified"] is True
                     and len(authentication["bindings"]) == 6
                     and contract.digest(authentication) == manifest["updater"]["signature_proof"]["sha256"]
                     and manifest["updater"]["active_lock_before"] == manifest["updater"]["active_lock_after"] == lock_hash
                     and manifest["updater"]["live_sources"] is True, "build proof lacks complete live updater/signature evidence")
    return report


def execute(candidate, request_path):
    source = Path(__file__).resolve().parents[2]
    candidate, request_path = Path(candidate).resolve(), Path(request_path).resolve()
    outputs, frozen = validate_outputs(candidate), pack(source)
    registry, lock, registry_hash, lock_hash = contract.inputs(source)
    plan = contract.expected_plan(registry, lock, registry_hash, lock_hash)
    runner = build_execution.Runner(request_path.parent / "build-artifacts", source, candidate, request_path, plan)
    commands = Commands(candidate, source, runner.root / "controls", runner.deadline)
    try:
        contract.require(commands.plans() == plan, "candidate plan did not match the actual selected build")
        commands.preflight_controls()
        commands.updater_controls()
        host_tools = {}
        for key, command in (("docker", ["docker", "version", "--format", "{{json .}}"]), ("git", ["git", "--version"]),
                             ("python", [sys.executable, "--version"]), ("github_cli", ["gh", "--version"])):
            _, path, _ = runner.run(command, timeout=30)
            host_tools[key] = contract.reference(runner.root, path)
        check = commands.updater("live-update-check", "check")
        update = commands.updater("live-update-create", "update")
        build_update_execution.refresh(runner, check, registry, lock, registry_hash, lock_hash)
        independent = build_update_execution.refresh(runner, update, registry, lock, registry_hash, lock_hash)
        images = runner.acquire_images()
        _, daemon, _ = runner.run(["docker", "info", "--format", "{{json .}}"], timeout=30)
        host = contract.parse_json(daemon.read_bytes())
        contract.require(host.get("OSType") == "linux" and host.get("Architecture") in {"x86_64", "amd64"}
                         and host.get("MemTotal", 0) >= 7 * 1024 ** 3, "native build host does not satisfy the selected platform and memory bound")
        _, common, _ = runner.run(["git", "-C", str(candidate), "rev-parse", "--path-format=absolute", "--git-common-dir"], timeout=30)
        caches = Path(common.read_text().strip()).parent / ".project/cache/native-sources"
        signature = {}
        def authenticate(container):
            signature["path"] = build_update_execution.authenticate(runner, container, independent)
        builds, modules, os_closures = build_execution.native_builds(runner, caches, authenticate)
        runner.cleanup()
        cleanup_history(runner.spec, runner.cleanups, images)
        _, verifier_source, _ = runner.run(["git", "-C", str(source), "rev-parse", "HEAD"], timeout=30)
        identity = {key: runner.spec[key] for key in ("run_id", "operation_id", "nonce")}
        identity.update(head_sha=os.environ["BOOTSTRAP_HEAD_SHA"], base_sha=os.environ["BOOTSTRAP_BASE_SHA"],
                        verifier_source_commit=verifier_source.read_text().strip())
        authentication = contract.parse_json(signature["path"].read_bytes())
        build_update_execution.validate_authentication(authentication, runner.root, runner.commands, time.time())
        updater = {"report": contract.reference(runner.root, update["path"]), "candidate": contract.reference(runner.root, update["candidate"]),
                   "check_report": contract.reference(runner.root, check["path"]), "independent_report": contract.reference(runner.root, independent["path"]),
                   "signature_proof": contract.reference(runner.root, signature["path"]), "active_lock_before": lock_hash,
                   "active_lock_after": lock_hash, "live_sources": True}
        manifest = {"schema_version": 1, "task_id": TASK, "contract": CONTRACT, "evidence_kind": "actual-build", "identity": identity,
                    "plan": plan, "plan_sha256": contract.digest(plan), "image_inputs": images, "host_tools": host_tools, "builds": builds, "module_closures": modules,
                    "os_closures": os_closures, "profile_bindings": build_vectors.bindings(plan, builds), "updater": updater,
                    "cleanup": contract.reference(runner.root, runner.root / "cleanup-history.json"), "commands": runner.commands, "runtime": "NOT_RUN"}
        build_materials.validate_materialized(manifest, runner.root, source, time.time())
        final_preflights(commands, manifest, runner.root, source)
        contract.require(validate_outputs(candidate) == outputs and pack(source) == frozen, "verification changed product outputs or trusted inputs")
        path = runner.root / "manifest.json"
        path.write_bytes(contract.canonical(manifest))
        report = {"contract": CONTRACT, "task_id": TASK, "scope": "isolated-native-builds", "runtime": "NOT_RUN",
                  "evidence_kind": "actual-build", "control_inputs": "synthetic-protocol-only", "identity": identity,
                  "platform_results": {"linux/amd64": "BUILT", "linux/arm64": "NOT_RUN"}, "trusted_pack": frozen,
                  "inputs": {"registry_json": (source / ".project/bootstrap/components/registry.json").read_text(),
                             "lock_json": (source / ".project/bootstrap/components/candidate-lock.json").read_text()},
                  "outputs": outputs, "outcomes": commands.outcomes, "manifest": manifest, "manifest_sha256": contract.digest(manifest),
                  "manifest_ref": contract.reference(runner.root, path), "materialized_root": str(runner.root),
                  "cleanup": {"spec": runner.spec, "history": runner.cleanups}, "updater_authentication": authentication}
        return validate_report(report)
    except BaseException:
        runner.cleanup()
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", required=True, type=Path)
    parser.add_argument("--request", required=True, type=Path)
    args = parser.parse_args()
    try:
        print(json.dumps(execute(args.candidate, args.request), sort_keys=True, allow_nan=False))
        return 0
    except (ValueError, KeyError, TypeError, OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(json.dumps({"contract": CONTRACT, "task_id": TASK, "status": "FAIL", "reason": str(error), "runtime": "NOT_RUN"}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
