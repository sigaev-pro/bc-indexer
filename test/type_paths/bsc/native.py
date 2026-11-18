#!/usr/bin/env python3
"""Bounded, source-pinned native BSC decoder/signer/classifier proof."""

import argparse
import json
from pathlib import Path
import re
import sys
import time
import uuid

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
sys.path.insert(0, str(ROOT / "test/harness"))
import bsc_source_inputs as source  # noqa: E402
import pins  # noqa: E402
from artifacts import (Rejected, artifact, copy_artifact, load, pinned_json, read_bytes,  # noqa: E402
                       reference, require, source_snapshot, sources_unchanged)

COMMIT = "54afaff4da31ac9e49ba646a4f65104edac5a2f3"
ARCHIVE = "f140cf8a9e6fd231ebc1b6764ca4404875a16aa5cd5e1abff86cdb93992ad810"
TARGET = "consensus/parlia/zz_extractor_type_paths_test.go"
TEST = "TestExtractorBSCTypePaths"
BINARY = "bsc-type-paths.test"
HARNESS = source.HARNESS


def keep_code(output, deadline):
    origins, retained = source_snapshot(output, HARNESS, deadline)
    for name in ("native.py", "native_test.go"):
        origin = reference(HERE / name, deadline=deadline)
        origins.append(origin)
        retained.append(copy_artifact(HERE / name, output / ("probe-" + name), origin, deadline=deadline))
    return origins, retained


def record(kind, seconds):
    return {"schema_version": 1, "kind": kind, "source_commit": pins.source_commit(),
            "run_id": uuid.uuid4().hex, "result": "FAIL", "deadline_seconds": seconds,
            "public_profile_credit": False, "import_observation": "NOT_RUN",
            "system_transaction_state": "NOT_RUN", "error": None}


def prepare(manifest_path, sha, output):
    started = time.monotonic()
    deadline = started + 3600
    require(manifest_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    manifest_ref, manifest = pinned_json(manifest_path, sha, 64 * 1024 * 1024, deadline)
    require(not output.is_relative_to(manifest_path.parent), "Cannot write into accepted build storage")
    closure = manifest["module_closures"]["bsc"]
    require(closure["source_commit"] == COMMIT and closure["source_archive"]["sha256"] == ARCHIVE,
            "Unexpected native source")
    proof = record("bsc-type-paths-preparation", 3600)
    proof["build_manifest"] = manifest_ref

    def body(output, runner):
        origins, proof["code"] = keep_code(output, deadline)
        root = output / "bsc"
        root.mkdir()
        _, archive, tree = source.extract_source(manifest, manifest_path, root, deadline)
        target = tree / TARGET
        require(not target.exists(), "Probe target already exists")
        probe_ref = reference(HERE / "native_test.go", deadline=deadline)
        copy_artifact(HERE / "native_test.go", target, probe_ref, deadline=deadline)
        for name in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (root / name).mkdir()
        for name in ("go.mod", "go.sum"):
            copy_artifact(tree / name, root / "metadata" / name, reference(tree / name), deadline=deadline)
        compiler = manifest["builds"]["bsc"]["compiler_image"]
        require(compiler == manifest["image_inputs"]["golang-build-image"]["reference"], "Compiler mismatch")
        mounts = source.build_mounts(tree, manifest_path.parent / "cache/bsc/mod", root)
        env = pins.environment()
        stdout = runner.command(compiler, ["/usr/local/go/bin/gofmt", "-l", TARGET], mounts,
                                cap=60, memory="2g", cwd="/source", environment=env)
        require(not read_bytes(stdout, 1024 * 1024).strip(), "Probe is not gofmt formatted")
        runner.command(compiler, ["/usr/local/go/bin/go", "vet", "./consensus/parlia"], mounts,
                       cap=1500, memory="6g", cwd="/source", environment=env)
        runner.command(compiler, ["/usr/local/go/bin/go", "test", "-c", "-trimpath", "-o",
                                  "/work/generated/" + BINARY, "./consensus/parlia"], mounts,
                       cap=1500, memory="6g", cwd="/source", environment=env)
        sources_unchanged(origins, deadline)
        tool = {"schema_version": 1, "kind": "bsc-type-paths-tool", "native_source_commit": COMMIT,
                "source_archive": archive, "compiler": compiler, "probe": probe_ref,
                "driver": reference(HERE / "native.py"), "build_manifest": manifest_ref,
                "binary": reference(root / "generated" / BINARY, 1024 * 1024 * 1024, deadline)}
        pins.save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "preparation.json", body)


def check_report(report):
    require(type(report) is dict, "Native report is not an object")
    require(type(report.get("schema_version")) is int and report["schema_version"] == 1 and
            report.get("scope") == "native-decoder-signer-classifier",
            "Wrong native report")
    for field in ("import_observation", "system_transaction_state", "public_profiles"):
        require(report.get(field) == "NOT_RUN", "Native component proof cannot grant import or public credit")
    rows = report.get("rows")
    require(type(rows) is list and len(rows) == 9, "Incomplete native rows")
    keys = [(row.get("case"), row.get("type")) for row in rows if type(row) is dict]
    expected = [("roundtrip-current-signer", i) for i in range(5)] + [
        ("pre-activation-signer-refusal", i) for i in range(1, 5)]
    require(len(keys) == len(rows) and sorted(keys) == sorted(expected), "Missing or repeated native case")
    for row in rows:
        require(type(row["type"]) is int, "Type ID is not an integer")
        if row["case"] == "roundtrip-current-signer":
            require(re.fullmatch(r"0x[0-9a-fA-F]{64}", row.get("hash", "")) is not None,
                    "Missing native transaction hash")
            require(type(row.get("sender")) is str and
                    row["sender"].lower() == "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf", "Wrong signer")
        else:
            require(type(row.get("block")) is str and row["block"].isdigit() and
                    type(row.get("time")) is int and row["time"] >= 0, "Missing activation inputs")
            boundary = {1: ("31302047", 1795046400), 2: ("31302047", 1795046400),
                        3: ("100000000", 1718863499), 4: ("100000000", 1742436599)}[row["type"]]
            require((row["block"], row["time"]) == boundary, "Wrong native activation boundary")
    require(type(report.get("unknown_typed_ids_rejected")) is int and report["unknown_typed_ids_rejected"] == 123,
            "Incomplete native unknown-type rejection")
    classification = report.get("classification")
    require(type(classification) is list and len(classification) == 5, "Incomplete native classification")
    hashes = {row["type"]: row["hash"] for row in rows if row["case"] == "roundtrip-current-signer"}
    require(all(type(row) is dict and type(row.get("type")) is int for row in classification), "Bad classification row")
    require(sorted(row["type"] for row in classification) == list(range(5)), "Repeated classifier type")
    require(all(row.get("system") is True and row.get("hash") == hashes[row["type"]] for row in classification),
            "Classifier changed transaction identity or rejected a system transaction")
    require(type(report.get("classification_controls")) is int and report["classification_controls"] == 5,
            "Missing classification negative controls")


def run(tool_path, sha, output):
    started = time.monotonic()
    deadline = started + 180
    require(tool_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, sha, deadline=deadline)
    require(tool.get("kind") == "bsc-type-paths-tool" and tool.get("native_source_commit") == COMMIT,
            "Unexpected native tool")
    require(tool["probe"] == reference(HERE / "native_test.go") and tool["driver"] == reference(HERE / "native.py"),
            "Prepared source differs from current probe")
    binary = artifact(tool["binary"], 1024 * 1024 * 1024, deadline=deadline)
    proof = record("bsc-type-paths-run", 180)
    proof["tool"] = tool_ref

    def body(output, runner):
        origins, proof["code"] = keep_code(output, deadline)
        destination = output / "probe"
        destination.mkdir()
        stdout = runner.command(tool["compiler"], ["/probe", "-test.v", "-test.run", "^" + TEST + "$",
                                                  "-test.timeout", "120s"],
                                [(binary, "/probe", True), (destination, "/run", False)], cap=150, memory="2g",
                                cwd="/run", environment={"EXTRACTOR_BSC_TYPE_REPORT": "/run/report.json"})
        proof["tests"] = pins.parse_go_test(read_bytes(stdout, 4 * 1024 * 1024).decode(), [TEST])
        native = destination / "report.json"
        check_report(load(native, 1024 * 1024, deadline))
        proof["native_report"] = reference(native, deadline=deadline)
        artifact(tool["binary"], 1024 * 1024 * 1024, deadline=deadline)
        sources_unchanged(origins, deadline)

    return pins.owned_attempt(output, started, deadline, proof, "run.json", body)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("prepare", "run"))
    parser.add_argument("input", type=Path)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        result = (prepare if args.phase == "prepare" else run)(args.input, args.sha256, args.output)
        print(json.dumps(result, indent=2))
        return 0 if result["result"] == "PASS" else 1
    except (Rejected, OSError, ValueError, KeyError, TypeError) as error:
        print(f"Rejected: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
