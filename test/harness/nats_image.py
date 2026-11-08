#!/usr/bin/env python3
"""Prepare a pinned coordinator and measure unchanged-image NATS fences."""

import argparse
import json
import os
from pathlib import Path
import sys
import time
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import (artifact, check_deadline, closed, copy_artifact, json_artifact, pinned_json,
                       read_artifact, read_bytes, reference, require, source_snapshot, sources_unchanged)
from native import Native, new_output
from nats_inputs import COMPILER_IMAGE, SOURCE, inspect_build
from nats_conformance import CASES, CASE_TOPOLOGIES, command_provenance, validate_case
from nats_image_checks import expected_config, validate_plan
from nats_image_proof import validate_report

HERE = Path(__file__).resolve().parent
FIXTURE = HERE / "fixtures" / "nats-image"
GO_FILES = ("main.go", "protocol.go", "process.go", "monitor.go", "driver_test.go")
SOURCES = ("nats_image.py", "nats_image_checks.py", "nats_image_collection.py", "nats_image_proof.py",
           "nats_inputs.py", "nats_fence.py", "nats_conformance.py", "artifacts.py", "native.py", "owned.py")
ENV = {"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64", "GO111MODULE": "off", "GOPROXY": "off",
       "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOWORK": "off", "GOCACHE": "/work/cache",
       "GOTMPDIR": "/work/build-temp", "TMPDIR": "/work/build-temp"}
COMMANDS = (["/usr/local/go/bin/go", "test", "-count=1", "-timeout=30s", "-v", "."],
            ["/usr/local/go/bin/go", "build", "-trimpath", "-buildvcs=false", "-o", "/work/driver", "."])
IMAGE = "docker.io/library/nats@sha256:afae2fdbe2e326c65e1246cb83b9d05d319188734f3bd577ac8072daae78a10f"
COMPATIBILITY = {
    "shared": ["same accepted NATS source and unchanged runtime image", "ordinary PUB", "FileStorage", "Limits retention",
               "default persistence", "no consumers, mirrors, sources, schedules, batches, transforms, or direct reads"],
    "different": ["source tests use embedded servers with NoSigs; image runs use CLI child processes and native signals",
                  "source tests use $G with $SYS transport; image runs use APP with SYS transport",
                  "explicit CLI client ACLs, ports, names, bounded stores and original-store process restart"],
    "source_coverage": dict(CASE_TOPOLOGIES),
    "image_obligations": ["standalone and R3 ACK loss", "native process exit", "same-store restart", "complete terminal fence and export"],
}


def save(path, value):
    data = (json.dumps(value, sort_keys=True, indent=2) + "\n").encode()
    with path.open("xb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def source_evidence(path, pin, build_ref, deadline):
    ref, proof = pinned_json(path, pin, 4 * 1024 * 1024, deadline)
    require(proof.get("kind") == "nats-source-conformance-run" and proof.get("result") == "PASS" and
            proof.get("cleanup_complete") is True and proof.get("case_topologies") == CASE_TOPOLOGIES and
            set(proof.get("cases", {})) == set(CASES), "Incomplete accepted source-native conformance")
    _, tool = json_artifact(proof["binding"], deadline=deadline)
    require(tool.get("kind") == "nats-source-conformance-tool" and tool.get("source_commit") == SOURCE and
            tool.get("build_manifest") == build_ref and tool.get("compiler") == COMPILER_IMAGE and
            tool.get("case_topologies") == CASE_TOPOLOGIES, "Source-native and image build bindings differ")
    command_provenance(tool, deadline)
    artifact(proof["binding"], deadline=deadline)
    for name in CASES:
        case = proof["cases"][name]
        require(case.get("result") == "PASS", "Required source-native case did not pass")
        _, report = json_artifact(case["report"], deadline=deadline)
        validate_case(report, name)
        for row in case["artifacts"]: artifact(row, deadline=deadline)
        artifact(case["stdout"], deadline=deadline)
    for row in proof["harness_sources"]: artifact(row, deadline=deadline)
    return ref


def command_checks(commands, deadline):
    require(type(commands) is list and len(commands) == len(COMMANDS), "Incomplete coordinator preparation")
    for command, expected in zip(commands, COMMANDS):
        closed(command, {"request", "result", "stdout", "stderr", "cleanup"}, "Coordinator preparation command")
        for ref in command.values(): artifact(ref, 8 * 1024 * 1024, deadline=deadline)
        _, request = json_artifact(command["request"], deadline=deadline)
        _, result = json_artifact(command["result"], deadline=deadline)
        args = request["argv"]
        require(type(result["exit_code"]) is int and result["exit_code"] == 0 and result["reason"] is None and
                result["cleanup_complete"] is True and result["argv"] == args, "Coordinator preparation failed")
        require(args.count(COMPILER_IMAGE) == 1 and args[args.index(COMPILER_IMAGE) + 1:] == expected, "Coordinator compile command changed")
        for flag, value in (("--network", "none"), ("--platform", "linux/amd64"), ("--pull", "never"), ("--workdir", "/source")):
            require(args.count(flag) == 1 and args[args.index(flag) + 1] == value, "Coordinator preparation isolation changed")
        require(args.count("--read-only") == 1, "Coordinator root must be read-only")
        environment = [args[index + 1] for index, value in enumerate(args) if value == "--env"]
        require(sorted(environment) == sorted(key + "=" + value for key, value in ENV.items()), "Coordinator environment changed")


def prior(path, pin, correction, kind, build_ref, deadline):
    if path is None:
        require(pin is None and correction is None, "Retry metadata without a previous attempt")
        return None, 1
    require(type(correction) is str and 10 <= len(correction) <= 2000 and type(pin) is str, "Explicit pinned failure and correction are required")
    ref, proof = pinned_json(path, pin, 8 * 1024 * 1024, deadline)
    require(proof.get("kind") == kind and proof.get("result") == "FAIL" and proof.get("build_manifest") == build_ref and
            proof.get("cleanup_complete") is True and type(proof.get("attempt_index")) is int, "Invalid or unresolved failed predecessor")
    if kind == "nats-image-run":
        _, old_tool = json_artifact(proof.get("tool"), deadline=deadline)
        require(old_tool.get("kind") == "nats-image-coordinator-tool" and old_tool.get("build_manifest") == build_ref and
                old_tool.get("source_commit") == SOURCE and old_tool.get("image") == IMAGE,
                "Failed predecessor used a different or missing accepted build/source")
        command_checks(old_tool["commands"], deadline)
        artifact(old_tool["binary"], deadline=deadline)
        for row in old_tool["driver_sources"]: artifact(row, deadline=deadline)
        source_evidence(Path(old_tool["source_conformance"]["path"]), old_tool["source_conformance"]["sha256"], build_ref, deadline)
    return ref, proof["attempt_index"] + 1


def prepare(manifest_path, pin, source_path, source_pin, output, previous=None, previous_pin=None, correction=None):
    start = time.monotonic()
    deadline = start + 600
    manifest_ref, manifest = pinned_json(manifest_path, pin, 8 * 1024 * 1024, deadline)
    inspection = inspect_build(manifest_path, pin)
    source_ref = source_evidence(source_path, source_pin, manifest_ref, deadline)
    previous_ref, ordinal = prior(previous, previous_pin, correction, "nats-image-preparation", manifest_ref, deadline)
    output = new_output(output)
    origins, snapshots = source_snapshot(output, SOURCES, deadline)
    source = output / "driver-source"
    source.mkdir()
    go_refs = []
    for name in GO_FILES:
        ref = reference(FIXTURE / name, 1024 * 1024, deadline)
        go_refs.append(copy_artifact(FIXTURE / name, source / name, ref, deadline=deadline))
    oracle_ref = reference(FIXTURE / "ORACLE.md", 1024 * 1024, deadline)
    oracle_copy = copy_artifact(FIXTURE / "ORACLE.md", output / "ORACLE.md", oracle_ref, deadline=deadline)
    work = output / "work"
    work.mkdir()
    (work / "build-temp").mkdir()
    native = Native(output, deadline)
    proof = {"schema_version": 1, "kind": "nats-image-preparation", "run_id": uuid.uuid4().hex,
             "native_owner_nonce": native.nonce, "build_manifest": manifest_ref, "source_conformance": source_ref,
             "attempt_index": ordinal, "previous_attempt": previous_ref, "correction": correction, "result": "FAIL",
             "harness_sources": snapshots, "driver_sources": go_refs, "commands": [], "cleanup_complete": False}
    try:
        for command in COMMANDS:
            native.command(COMPILER_IMAGE, command, [(source, "/source", True), (work, "/work", False)],
                           cap=260, memory="2g", environment=ENV, cwd="/source")
        command_checks(native.commands, deadline)
        binary = reference(work / "driver", 64 * 1024 * 1024, deadline)
        sources_unchanged(origins, deadline)
        for name, ref in zip(GO_FILES, go_refs):
            artifact(ref, deadline=deadline)
            require(reference(FIXTURE / name, 1024 * 1024, deadline)["sha256"] == ref["sha256"], "Driver source changed during preparation")
        artifact(manifest_ref, 8 * 1024 * 1024, deadline=deadline)
        artifact(source_ref, deadline=deadline)
        artifact(oracle_ref, deadline=deadline)
        tool = {"schema_version": 1, "kind": "nats-image-coordinator-tool", "build_manifest": manifest_ref,
                "source_conformance": source_ref, "compiler": COMPILER_IMAGE, "image": IMAGE, "source_commit": SOURCE,
                "binary": binary, "driver_sources": go_refs, "oracle": oracle_copy, "commands": native.commands,
                "compatibility": COMPATIBILITY, "input_inspection": inspection}
        check_deadline(deadline)
        save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)
        proof["result"] = "PASS"
    except Exception as error:
        proof["error"] = str(error)
    finally:
        proof.update(commands=native.commands, cleanup_complete=native.cleanup_complete, elapsed_seconds=time.monotonic() - start)
        save(output / "preparation.json", proof)
    return proof


def tool_input(path, pin, deadline):
    ref, tool = pinned_json(path, pin, 8 * 1024 * 1024, deadline)
    closed(tool, {"schema_version", "kind", "build_manifest", "source_conformance", "compiler", "image", "source_commit", "binary",
                  "driver_sources", "oracle", "commands", "compatibility", "input_inspection"}, "Image coordinator tool")
    require(type(tool["schema_version"]) is int and tool["schema_version"] == 1 and tool["kind"] == "nats-image-coordinator-tool" and
            tool["image"] == IMAGE and tool["compiler"] == COMPILER_IMAGE and tool["source_commit"] == SOURCE and
            tool["compatibility"] == COMPATIBILITY, "Unsupported coordinator/source/image binding")
    artifact(tool["build_manifest"], 8 * 1024 * 1024, deadline=deadline)
    source_evidence(Path(tool["source_conformance"]["path"]), tool["source_conformance"]["sha256"], tool["build_manifest"], deadline)
    command_checks(tool["commands"], deadline)
    require(type(tool["driver_sources"]) is list and len(tool["driver_sources"]) == len(GO_FILES), "Coordinator source inventory missing")
    for name, row in zip(GO_FILES, tool["driver_sources"]):
        artifact(row, 1024 * 1024, deadline=deadline)
        require(Path(row["path"]).name == name and reference(FIXTURE / name, 1024 * 1024, deadline)["sha256"] == row["sha256"],
                "Prepared coordinator differs from reviewed source")
    artifact(tool["oracle"], deadline=deadline)
    require(reference(FIXTURE / "ORACLE.md", 1024 * 1024, deadline)["sha256"] == tool["oracle"]["sha256"], "Prepared oracle changed")
    artifact(tool["binary"], 64 * 1024 * 1024, deadline=deadline)
    return ref, tool


def inventory(directory, deadline):
    result, total = [], 0
    for root, dirs, files in os.walk(directory, followlinks=False):
        check_deadline(deadline)
        require(all(not (Path(root) / name).is_symlink() for name in dirs), "Evidence directory symlink")
        for name in sorted(files):
            require(len(result) < 8192, "Evidence file count exceeded")
            row = reference(Path(root) / name, 128 * 1024 * 1024, deadline)
            total += row["size_bytes"]
            require(total <= 512 * 1024 * 1024, "Evidence byte count exceeded")
            result.append(row)
    return result


def run(tool_path, pin, plan_path, plan_pin, output, previous=None, previous_pin=None, correction=None):
    start = time.monotonic()
    # Admission and all measured phases share this ceiling; the selected plan
    # can only shorten it. A per-command cap never extends this parent budget.
    deadline = start + 1800
    plan_ref, plan = pinned_json(plan_path, plan_pin, 65536, deadline)
    validate_plan(plan)
    deadline = start + plan["measured_seconds"]
    tool_ref, tool = tool_input(tool_path, pin, deadline)
    previous_ref, ordinal = prior(previous, previous_pin, correction, "nats-image-run", tool["build_manifest"], deadline)
    output = new_output(output)
    origins, sources = source_snapshot(output, SOURCES, deadline)
    binary_dir = output / "tool"
    binary_dir.mkdir()
    binary = copy_artifact(Path(tool["binary"]["path"]), binary_dir / "driver", tool["binary"], 64 * 1024 * 1024, deadline)
    (binary_dir / "driver").chmod(0o755)
    native = Native(output, deadline)
    proof = {"schema_version": 1, "kind": "nats-image-run", "run_id": uuid.uuid4().hex, "native_owner_nonce": native.nonce,
             "tool": tool_ref, "plan": plan_ref, "build_manifest": tool["build_manifest"], "source_conformance": tool["source_conformance"],
             "compatibility": COMPATIBILITY, "binary_snapshot": binary, "harness_sources": sources,
             "attempt_index": ordinal, "previous_attempt": previous_ref, "correction": correction,
             "result": "FAIL", "cases": {name: {"result": "NOT_RUN"} for name in plan["topologies"]},
             "runtime_admitted": False, "public_profiles": "NOT_RUN", "cleanup_complete": False, "commands": []}
    try:
        for topology in plan["topologies"]:
            check_deadline(deadline)
            remaining = deadline - time.monotonic()
            require(remaining > 110, "Insufficient parent budget for native work and cleanup")
            cap = min(600, int(remaining) - 10)
            case = output / topology
            case.mkdir()
            count = 1 if topology == "standalone" else 3
            request = {"schema_version": 1, "topology": topology, "plan": plan, "seconds": cap - 70,
                       "configs": [[expected_config(topology, generation, index) for index in range(count)] for generation in (0, 1)]}
            save(case / "input.json", request)
            request_ref = reference(case / "input.json", 1024 * 1024, deadline)
            proof["cases"][topology] = {"result": "FAIL", "input": request_ref}
            native.command(IMAGE, ["/tool/driver", "/proof/input.json"], [(binary_dir, "/tool", True), (case, "/proof", False)], cap=cap, memory="1536m")
            report_ref = reference(case / "native.json", 16 * 1024 * 1024, deadline)
            _, report = json_artifact(report_ref, 16 * 1024 * 1024, deadline=deadline)
            require(report["basic"]["plan"] == plan and report["basic"]["topology"] == topology, "Native plan or topology substituted")
            comparison = validate_report(report, case, deadline)
            refs = inventory(case, deadline)
            artifact(request_ref, deadline=deadline)
            proof["cases"][topology] = {"result": "PASS", "input": request_ref, "report": report_ref, "comparison": comparison, "artifacts": refs}
        sources_unchanged(origins, deadline)
        for ref in (tool_ref, plan_ref, tool["build_manifest"], tool["source_conformance"]): artifact(ref, 8 * 1024 * 1024, deadline=deadline)
        for case in proof["cases"].values():
            for ref in case["artifacts"]: artifact(ref, 128 * 1024 * 1024, deadline=deadline)
        check_deadline(deadline)
        proof["result"] = "PASS"
    except Exception as error:
        proof["error"] = str(error)
        # Keep available failed native bytes. Failure cleanup may consume the
        # final budget; in that case retain the directory and say why hashing
        # was incomplete instead of inventing a complete evidence set.
        if "case" in locals():
            proof["cases"][topology]["evidence_directory"] = str(case)
            try:
                proof["cases"][topology]["artifacts"] = inventory(case, deadline)
            except Exception as evidence_error:
                proof["cases"][topology]["evidence_inventory_error"] = str(evidence_error)
    finally:
        proof.update(commands=native.commands, cleanup_complete=native.cleanup_complete, elapsed_seconds=time.monotonic() - start)
        save(output / "image-fence.json", proof)
    return proof


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("prepare", "run"):
        command = commands.add_parser(name)
        command.add_argument("input", type=Path)
        command.add_argument("--sha256", required=True)
        command.add_argument("--output", type=Path, required=True)
        command.add_argument("--previous", type=Path)
        command.add_argument("--previous-sha256")
        command.add_argument("--correction")
        if name == "prepare":
            command.add_argument("--source-conformance", type=Path, required=True)
            command.add_argument("--source-sha256", required=True)
        else:
            command.add_argument("--plan", type=Path, required=True)
            command.add_argument("--plan-sha256", required=True)
    args = parser.parse_args()
    try:
        require(args.input.is_absolute() and args.output.is_absolute(), "Input and owned output must be absolute paths")
        extra = (args.previous, args.previous_sha256, args.correction)
        if args.command == "prepare":
            result = prepare(args.input, args.sha256, args.source_conformance, args.source_sha256, args.output, *extra)
        else:
            result = run(args.input, args.sha256, args.plan, args.plan_sha256, args.output, *extra)
        print(json.dumps({"result": result["result"], "output": str(args.output), "native_owner_nonce": result["native_owner_nonce"]}))
        return 0 if result["result"] == "PASS" else 2
    except Exception as error:
        print(json.dumps({"result": "REJECTED", "error": str(error)}))
        return 2


if __name__ == "__main__": raise SystemExit(main())
