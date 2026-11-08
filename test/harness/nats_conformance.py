#!/usr/bin/env python3
"""Prepare and execute five bounded source-native NATS conformance cases."""

import argparse
import base64
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import tarfile
import time
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import (artifact, captured_json, check_deadline, closed, copy_artifact, json_artifact,
                       load, pinned_json, read_bytes, reference, require, source_snapshot, sources_unchanged)
from native import Native, new_output
from nats_fence import validate_frontier
from nats_inputs import COMPILER_IMAGE, SOURCE, inspect_build

CASES = ("TestExtractorFenceStandalone", "TestExtractorFencePreProposal", "TestExtractorFenceDelayedApply",
         "TestExtractorFenceLostAck", "TestExtractorFenceLeaderChange")
CASE_TOPOLOGIES = {name: "standalone" if index == 0 else "R3" for index, name in enumerate(CASES)}
PAYLOAD = b"source-fence-payload"
OVERLAY = Path(__file__).with_name("fixtures") / "nats" / "fence_test.go"
ORACLE = OVERLAY.with_name("ORACLE.md")
SOURCES = ("nats_conformance.py", "nats_inputs.py", "nats_fence.py", "artifacts.py", "native.py", "owned.py")
PREPARATION_COMMANDS = (
    ["/usr/local/go/bin/go", "mod", "verify"],
    ["/usr/local/go/bin/go", "test", "-c", "-p", "2", "-trimpath", "-buildvcs=false", "-o", "/work/generated/nats-fence.test", "./server"],
    ["/work/generated/nats-fence.test", "-test.list=^TestExtractorFence"],
)


def save(path, value):
    with path.open("x") as stream:
        stream.write(json.dumps(value, sort_keys=True, indent=2) + "\n")


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def selected_test(output, name):
    text = output.decode("utf-8", "strict")
    starts = re.findall(r"^=== RUN   (\S+)$", text, re.M)
    passes = re.findall(r"^--- PASS: (\S+) \([^\n]+\)$", text, re.M)
    require(starts == [name] and passes == [name] and text.rstrip().endswith("PASS") and
            not re.search(r"^(?:--- (?:FAIL|SKIP):|FAIL|testing: warning: no tests)", text, re.M),
            "Missing, skipped, failed or extra selected native case")


def exclude_upstream_tests(source):
    """Only call on the fresh owned copy; never change the accepted source tree."""
    result = []
    for path in sorted((source / "server").glob("*_test.go")):
        row = reference(path, 16 * 1024 * 1024)
        result.append({"path": str(path.relative_to(source)), "sha256": row["sha256"], "size_bytes": row["size_bytes"]})
        path.unlink()
    require(result, "Native test inventory is unexpectedly empty")
    return result


def production_inventory(source, deadline):
    paths = sorted(path for path in source.rglob("*") if not path.is_dir() and
                   not (path.parent == source / "server" and path.name.endswith("_test.go")))
    return [{"path": str(path.relative_to(source)), "sha256": reference(path, 32 * 1024 * 1024, deadline)["sha256"]}
            for path in paths]


def validate_case(report, name):
    closed(report, {"schema_version", "kind", "case", "outcome", "cleanup", "events", "observations"}, "Native case report")
    require(type(report["schema_version"]) is int and report["schema_version"] == 1 and
            report["kind"] == "nats-source-conformance" and report["case"] == name and name in CASES and
            report["outcome"] == "PASS", "Native case did not complete")
    closed(report["cleanup"], {"shutdown_complete"}, "Native cleanup")
    require(report["cleanup"]["shutdown_complete"] is True, "Native shutdown incomplete")
    events = report["events"]
    require(isinstance(events, list) and all(isinstance(event, dict) and set(event) == {"ordinal", "name"} and
            type(event["ordinal"]) is int and event["ordinal"] == index + 1
            for index, event in enumerate(events)), "Invalid native event order")
    names = [event["name"] for event in events]
    require(len(names) == len(set(names)), "Repeated native event")
    observations = report["observations"]
    expected = {
        CASES[0]: ["storage_entered", "seal_started", "storage_released", "seal_completed"],
        CASES[1]: ["proposal_entered", "seal_completed", "proposal_released", "publish_completed"],
        CASES[2]: ["apply_entered", "blocked_sample", "apply_released", "recovered_sample"],
        CASES[3]: ["request_forwarded", "response_observed", "response_dropped", "application_timeout"],
        CASES[4]: ["initial_sample", "stepdown_requested", "changed_sample", "stable_sample"],
    }
    require(names == expected[name], "Wrong native fault boundary sequence")
    if name in CASES[:2]:
        config = observations["stream"]["config"]
        require(config["name"] == "FENCE" and config.get("sealed") is True and config.get("discard") == "new" and
                config.get("deny_delete") is True and config.get("deny_purge") is True and
                config.get("allow_rollup_hdrs", False) is False and type(config.get("max_age", 0)) is int and config.get("max_age", 0) == 0,
                "Native sealed configuration mismatch")
    if name == CASES[0]:
        require(observations.get("mutex_locked") is True and observations.get("seal_after_release") is True,
                "Standalone seal did not wait for native storage")
    if name == CASES[1]:
        validate_frontier(observations["binding"], observations["recovered"], observations["recovered"])
    if name in (CASES[0], CASES[1], CASES[3]):
        message = observations["message"]
        require(message["subject"] == "fence.input" and type(message["sequence"]) is int and message["sequence"] == 1 and
                base64.b64decode(message["data"], validate=True) == PAYLOAD, "Native stored message differs")
    if name == CASES[2]:
        binding, blocked = observations["binding"], observations["blocked"]
        rows = [raw[binding["account"]][binding["group"]] for raw in blocked.values()]
        require(any(row["committed"] > row["applied"] and all(row[key] == 0 for key in
                    ("ipq_proposal_len", "ipq_entry_len", "ipq_apply_len")) for row in rows),
                "Delayed apply did not expose the empty-queue counterexample")
        try:
            validate_frontier(binding, blocked, blocked)
        except ValueError:
            pass
        else:
            raise ValueError("Unapplied native sample was accepted")
        validate_frontier(binding, observations["recovered_before"], observations["recovered_after"])
    if name == CASES[3]:
        validate_frontier(observations["binding"], observations["recovered"], observations["recovered"])
        request = base64.b64decode(observations["request_frame"], validate=True)
        response = base64.b64decode(observations["response_frame"], validate=True)
        require(request == b"PUB fence.input _INBOX.EXTRACTOR " + str(len(PAYLOAD)).encode() + b"\r\n" + PAYLOAD + b"\r\n",
                "Unexpected publication frame")
        header, body = response.split(b"\r\n", 1)
        fields = header.split(b" ")
        require(len(fields) == 4 and fields[:3] == [b"MSG", b"_INBOX.EXTRACTOR", b"1"] and fields[3].isdigit() and
                len(body) == int(fields[3]) + 2 and body.endswith(b"\r\n"), "Uncorrelated or malformed response frame")
        ack = json.loads(body[:-2])
        require(ack.get("stream") == "FENCE" and type(ack.get("seq")) is int and ack["seq"] == 1 and
                "error" not in ack and observations.get("ack_forwarded") is False,
                "Missing actual acknowledgment before reply loss")
    if name == CASES[4]:
        binding = observations["binding"]
        initial = validate_frontier(binding, observations["initial"], observations["initial"])
        changed = validate_frontier(binding, observations["changed"], observations["changed"])
        require(initial["leader"] != changed["leader"] and initial["term"] < changed["term"], "No native leader change occurred")
        try:
            validate_frontier(binding, observations["initial"], observations["changed"])
        except ValueError:
            pass
        else:
            raise ValueError("Crossing native collection was accepted")
        validate_frontier(binding, observations["changed"], observations["stable"])


def retry(previous, correction, kind, binding, deadline):
    if previous is None:
        require(correction is None, "Correction requires a retained earlier failure")
        return None, 1
    ref, old = captured_json(previous, deadline=deadline)
    require(old["kind"] == kind and old["result"] == "FAIL" and old["binding"] == binding and
            isinstance(correction, str) and correction.strip(), "Invalid retry lineage")
    return ref, old["attempt_index"] + 1


def command_provenance(tool, deadline):
    require(isinstance(tool["commands"], list) and len(tool["commands"]) == 3, "Incomplete native preparation commands")
    for index, row in enumerate(tool["commands"]):
        closed(row, {"request", "result", "stdout", "stderr", "cleanup"}, "Preparation command")
        for ref in row.values(): artifact(ref, 8 * 1024 * 1024, deadline=deadline)
        _, result = json_artifact(row["result"], deadline=deadline)
        _, request = json_artifact(row["request"], deadline=deadline)
        command = request["argv"]
        require(type(result["exit_code"]) is int and result["exit_code"] == 0 and result["reason"] is None and
                result["cleanup_complete"] is True and result["argv"] == command, "Native command failed or changed")
        require(isinstance(command, list) and all(isinstance(value, str) for value in command) and
                command.count(COMPILER_IMAGE) == 1 and command[command.index(COMPILER_IMAGE) + 1:] == PREPARATION_COMMANDS[index],
                "Unexpected native preparation command")
        for flag, value in (("--network", "none"), ("--platform", "linux/amd64"), ("--pull", "never")):
            require(command.count(flag) == 1 and command[command.index(flag) + 1] == value, "Native isolation mismatch")
        require(command.count("--read-only") == 1, "Native preparation root must be read-only")
        if index < 2:
            environment = [command[position + 1] for position, flag in enumerate(command) if flag == "--env"]
            names = [value.split("=", 1)[0] for value in environment]
            require(len(names) == len(set(names)) and all(value in environment for value in (
                "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off",
                "GOFLAGS=-mod=readonly -modfile=/work/metadata/go.mod", "GOMODCACHE=/modules",
                "GOTMPDIR=/work/build-temp", "TMPDIR=/work/build-temp")), "Native Go isolation mismatch")
            require(command.count("--workdir") == 1 and command[command.index("--workdir") + 1] == "/source", "Wrong native compile root")
        else:
            listing = read_bytes(Path(row["stdout"]["path"]), 65536, deadline).decode().splitlines()
            require(sorted(listing) == sorted(CASES), "Compiled native case list differs")


def prepare(manifest_path, pin, output, previous=None, correction=None):
    started = time.monotonic()
    deadline = started + 1800
    inspection = inspect_build(manifest_path, pin)
    manifest_ref, manifest = pinned_json(manifest_path, pin, 64 * 1024 * 1024, deadline)
    previous_ref, attempt = retry(previous, correction, "nats-source-preparation", manifest_ref, deadline)
    require(not output.is_relative_to(manifest_path.parent), "Preparation cannot modify accepted artifact storage")
    output = new_output(output)
    runner = Native(output, deadline)
    proof = {"schema_version": 1, "kind": "nats-source-preparation", "result": "FAIL", "binding": manifest_ref,
             "run_id": uuid.uuid4().hex, "native_owner_nonce": runner.nonce, "attempt_index": attempt,
             "previous_attempt": previous_ref, "correction": correction, "runtime_admitted": False}
    origins, proof["harness_sources"] = source_snapshot(output, SOURCES, deadline)
    try:
        save(output / "input-inspection.json", inspection)
        proof["input_inspection"] = reference(output / "input-inspection.json", deadline=deadline)
        oracle_ref = reference(ORACLE, 1024 * 1024, deadline)
        proof["oracle_notes"] = copy_artifact(ORACLE, output / "ORACLE.md", oracle_ref, 1024 * 1024, deadline)
        closure = manifest["module_closures"]["nats-server"]
        archive = artifact(closure["source_archive"], 128 * 1024 * 1024, manifest_path.parent, deadline)
        proof["source_archive"] = copy_artifact(archive, output / "source.tar", closure["source_archive"], 128 * 1024 * 1024, deadline)
        source = output / "source"
        source.mkdir()
        with tarfile.open(fileobj=io.BytesIO(read_bytes(output / "source.tar", 128 * 1024 * 1024, deadline)), mode="r:") as archived:
            for member in archived:
                check_deadline(deadline)
                archived.extract(member, source, filter="data")
        original = production_inventory(source, deadline)
        save(output / "production-sources.json", original)
        save(output / "excluded-tests.json", exclude_upstream_tests(source))
        require(production_inventory(source, deadline) == original, "Production source changed while excluding tests")
        overlay_ref = reference(OVERLAY, 1024 * 1024, deadline)
        proof["overlay"] = copy_artifact(OVERLAY, source / "server/extractor_fence_test.go", overlay_ref, 1024 * 1024, deadline)
        for name in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (output / name).mkdir()
        for name in ("go.mod", "go.sum"):
            copy_artifact(source / name, output / "metadata" / name, reference(source / name, deadline=deadline), deadline=deadline)
        mounts = [(source, "/source", True), (manifest_path.parent / "cache/nats-server/mod", "/modules", True), (output, "/work", True)]
        mounts += [(output / name, "/work/" + name, False) for name in ("metadata", "generated", "build-cache", "build-temp", "gopath")]
        environment = {"CGO_ENABLED": "0", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64", "GOCACHE": "/work/build-cache",
                       "GOMODCACHE": "/modules", "GOPATH": "/work/gopath", "GOTMPDIR": "/work/build-temp", "TMPDIR": "/work/build-temp",
                       "GOFLAGS": "-mod=readonly -modfile=/work/metadata/go.mod", "GOPROXY": "off", "GOSUMDB": "off",
                       "GOTOOLCHAIN": "local", "GOWORK": "off"}
        runner.command(COMPILER_IMAGE, PREPARATION_COMMANDS[0], mounts, cap=240, memory="4g", cwd="/source", environment=environment)
        runner.command(COMPILER_IMAGE, PREPARATION_COMMANDS[1], mounts, cap=1200, memory="4g", cwd="/source", environment=environment)
        listing = runner.command(COMPILER_IMAGE, PREPARATION_COMMANDS[2], mounts, cap=60, memory="1g")
        require(sorted(read_bytes(listing, 65536, deadline).decode().splitlines()) == sorted(CASES), "Compiled native case list differs")
        require(production_inventory(source, deadline) == original, "Production source changed during compilation")
        for name in ("go.mod", "go.sum"):
            require(read_bytes(source / name, 1024 * 1024, deadline) == read_bytes(output / "metadata" / name, 1024 * 1024, deadline), "Go module metadata changed")
        sources_unchanged(origins, deadline)
        require(reference(OVERLAY, deadline=deadline) == overlay_ref, "Overlay source changed during preparation")
        require(reference(ORACLE, deadline=deadline) == oracle_ref, "Independent expectations changed during preparation")
        artifact(proof["overlay"], 1024 * 1024, deadline=deadline)
        artifact(manifest_ref, 64 * 1024 * 1024, deadline=deadline)
        tool = {"schema_version": 1, "kind": "nats-source-conformance-tool", "build_manifest": manifest_ref,
                "source_commit": SOURCE, "source_archive": proof["source_archive"], "module_closure_sha256": digest(closure),
                "compiler": COMPILER_IMAGE, "overlay": proof["overlay"], "oracle_notes": proof["oracle_notes"],
                "binary": reference(output / "generated/nats-fence.test", deadline=deadline),
                "production_sources": reference(output / "production-sources.json", deadline=deadline),
                "excluded_tests": reference(output / "excluded-tests.json", deadline=deadline), "cases": list(CASES), "case_topologies": CASE_TOPOLOGIES,
                "commands": runner.commands, "metadata": [reference(output / "metadata" / name, deadline=deadline) for name in ("go.mod", "go.sum")]}
        save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)
        proof["result"] = "PASS"
    except Exception as error:
        proof["error"] = str(error)
    finally:
        proof.update(commands=runner.commands, cleanup_complete=runner.cleanup_complete, elapsed_seconds=time.monotonic() - started)
        save(output / "preparation.json", proof)
    return proof


def run(tool_path, pin, output, previous=None, correction=None):
    started = time.monotonic()
    deadline = started + 1800
    tool_ref, tool = pinned_json(tool_path, pin, 1024 * 1024, deadline)
    closed(tool, {"schema_version", "kind", "build_manifest", "source_commit", "source_archive", "module_closure_sha256",
                  "compiler", "overlay", "oracle_notes", "binary", "production_sources", "excluded_tests", "cases", "case_topologies", "commands", "metadata"}, "Native tool")
    require(type(tool["schema_version"]) is int and tool["schema_version"] == 1 and tool["kind"] == "nats-source-conformance-tool" and tool["cases"] == list(CASES) and
            tool["source_commit"] == SOURCE and tool["compiler"] == COMPILER_IMAGE, "Unsupported caller-pinned native tool")
    require(tool["case_topologies"] == CASE_TOPOLOGIES, "Native case topology coverage changed")
    _, manifest = json_artifact(tool["build_manifest"], deadline=deadline)
    closure = manifest["module_closures"]["nats-server"]
    require(manifest["evidence_kind"] == "actual-build" and manifest["task_id"] == "E01-01.builds" and
            closure["source_commit"] == SOURCE and digest(closure) == tool["module_closure_sha256"], "Native module closure changed")
    require(all(tool["source_archive"][field] == closure["source_archive"][field] for field in ("sha256", "size_bytes")),
            "Native source archive mismatch")
    for name in ("source_archive", "overlay", "oracle_notes", "production_sources", "excluded_tests"):
        artifact(tool[name], 128 * 1024 * 1024, deadline=deadline)
    require(tool["overlay"]["sha256"] == reference(OVERLAY, deadline=deadline)["sha256"], "Native tool overlay differs from reviewed source")
    require(tool["oracle_notes"]["sha256"] == reference(ORACLE, deadline=deadline)["sha256"], "Native tool expectations differ from reviewed source")
    binary = artifact(tool["binary"], 256 * 1024 * 1024, deadline=deadline)
    require(isinstance(tool["metadata"], list) and len(tool["metadata"]) == 2, "Native Go metadata missing")
    for ref in tool["metadata"]: artifact(ref, deadline=deadline)
    command_provenance(tool, deadline)
    previous_ref, attempt = retry(previous, correction, "nats-source-conformance-run", tool_ref, deadline)
    require(not output.is_relative_to(tool_path.parent) and not output.is_relative_to(Path(tool["build_manifest"]["path"]).parent),
            "Measured run cannot modify preparation or accepted storage")
    output = new_output(output)
    runner = Native(output, deadline)
    proof = {"schema_version": 1, "kind": "nats-source-conformance-run", "result": "FAIL", "binding": tool_ref,
             "run_id": uuid.uuid4().hex, "native_owner_nonce": runner.nonce, "attempt_index": attempt,
             "previous_attempt": previous_ref, "correction": correction, "runtime_admitted": False,
             "unchanged_image_restart": "NOT_RUN", "standalone_ack_loss": "NOT_RUN", "complete_terminal_fence": "NOT_RUN",
             "case_topologies": CASE_TOPOLOGIES, "cases": {name: {"result": "NOT_RUN"} for name in CASES}}
    origins, proof["harness_sources"] = source_snapshot(output, SOURCES, deadline)
    try:
        proof["binary_snapshot"] = copy_artifact(binary, output / "nats-fence.test", tool["binary"], 256 * 1024 * 1024, deadline)
        (output / "nats-fence.test").chmod(0o500)
        for name in CASES:
            check_deadline(deadline)
            artifact(proof["binary_snapshot"], 256 * 1024 * 1024, deadline=deadline)
            directory = output / name
            directory.mkdir()
            proof["cases"][name] = {"result": "FAIL"}
            stdout = runner.command(COMPILER_IMAGE, ["/tool/nats-fence.test", "-test.v", "-test.count=1", "-test.timeout=120s", "-test.run=^" + name + "$"],
                                    [(output / "nats-fence.test", "/tool/nats-fence.test", True), (directory, "/proof", False)],
                                    cap=180, memory="2g", environment={"EXTRACTOR_FENCE_OUTPUT": "/proof", "GOMAXPROCS": "2"})
            selected_test(read_bytes(stdout, 8 * 1024 * 1024, deadline), name)
            report_ref, report = captured_json(directory / "report.json", 8 * 1024 * 1024, deadline)
            validate_case(report, name)
            identities = load(directory / "identities.json", 1024 * 1024, deadline)
            replicas = 1 if CASE_TOPOLOGIES[name] == "standalone" else 3
            require(isinstance(identities, list) and len(identities) == replicas and
                    {row["server_name"] for row in identities} == {f"fence-S-{index}" for index in range(1, replicas + 1)} and
                    len({row["info_id"] for row in identities}) == replicas and
                    all(isinstance(row["info_id"], str) and re.fullmatch(r"N[A-Z2-7]{55}", row["info_id"]) and row["no_signals"] is True
                        for row in identities), "Native server identities or declared topology differ")
            retained = [reference(path, 128 * 1024 * 1024, deadline) for path in sorted(directory.rglob("*")) if not path.is_dir()]
            proof["cases"][name] = {"result": "PASS", "report": report_ref, "stdout": reference(stdout, deadline=deadline), "artifacts": retained}
        sources_unchanged(origins, deadline)
        artifact(tool_ref, deadline=deadline)
        artifact(tool["binary"], 256 * 1024 * 1024, deadline=deadline)
        artifact(proof["binary_snapshot"], 256 * 1024 * 1024, deadline=deadline)
        for field in ("build_manifest", "source_archive", "overlay", "oracle_notes", "production_sources", "excluded_tests"):
            artifact(tool[field], 128 * 1024 * 1024, deadline=deadline)
        for ref in tool["metadata"]: artifact(ref, deadline=deadline)
        command_provenance(tool, deadline)
        for case in proof["cases"].values():
            for ref in case["artifacts"]: artifact(ref, 128 * 1024 * 1024, deadline=deadline)
        check_deadline(deadline)
        proof["result"] = "PASS"
    except Exception as error:
        proof["error"] = str(error)
    finally:
        proof.update(commands=runner.commands, cleanup_complete=runner.cleanup_complete, elapsed_seconds=time.monotonic() - started)
        save(output / "conformance.json", proof)
    return proof


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("prepare", "run"))
    parser.add_argument("input", type=Path)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--previous", type=Path)
    parser.add_argument("--correction")
    args = parser.parse_args()
    try:
        require(args.input.is_absolute() and args.output.is_absolute(), "Absolute input/output paths required")
        result = (prepare if args.mode == "prepare" else run)(args.input, args.sha256, args.output, args.previous, args.correction)
        print(json.dumps(result, sort_keys=True))
        return 0 if result["result"] == "PASS" else 2
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(json.dumps({"result": "FAIL", "error": str(error), "runtime_admitted": False}))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
