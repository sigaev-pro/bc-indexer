#!/usr/bin/env python3
"""Bounded preparation/import of the coherent private modern BSC fixture."""
import argparse
import importlib.util
import json
from pathlib import Path
import sys
import time
import uuid

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[3]
sys.path.insert(0, str(ROOT / "test/harness"))
import bsc_decision
import bsc_source_inputs as source
import pins
from artifacts import (Rejected, artifact, copy_artifact, load, pinned_json, read_bytes,
                       reference, require, source_snapshot, sources_unchanged)

spec = importlib.util.spec_from_file_location("bsc_full_import_judge", HERE / "judge.py")
judge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(judge)

COMMIT = "54afaff4da31ac9e49ba646a4f65104edac5a2f3"
ARCHIVE = "f140cf8a9e6fd231ebc1b6764ca4404875a16aa5cd5e1abff86cdb93992ad810"
TEST = "TestBSCFullTypeImport"
BINARY = "bsc-full-type-import.test"
PACKAGE = "extractor/bsc/decision"
INPUTS = {"native.py": HERE / "native.py", "native_test.go": HERE / "native_test.go", "judge.py": HERE / "judge.py",
          "wire.py": HERE / "wire.py",
          "ledger.py": HERE / "ledger.py",
          "oracle_sources.json": HERE / "oracle_sources.json",
          "corpus_test.go": ROOT / "test/native_corpus/bsc/corpus_test.go",
          "provenance.json": HERE.parent / "modern_init/provenance.json",
          "eth_type_judge.py": ROOT / "test/type_paths/eth/eth_type_judge.py"}


def keep_code(output, deadline):
    origins, retained = source_snapshot(output, bsc_decision.HARNESS, deadline)
    for name, path in INPUTS.items():
        origin = reference(path, deadline=deadline)
        origins.append(origin)
        retained.append(copy_artifact(path, output / ("probe-" + name), origin, deadline=deadline))
    return origins, retained


def record(kind, seconds):
    return {"schema_version": 1, "kind": kind, "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex,
            "result": "FAIL", "deadline_seconds": seconds, "public_profile_credit": False, "ac_verdict": None, "error": None}


def prepare(manifest_path, sha, output):
    started = time.monotonic(); deadline = started + 3600
    require(manifest_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    manifest_ref, manifest = pinned_json(manifest_path, sha, 64 * 1024 * 1024, deadline)
    require(not output.is_relative_to(manifest_path.parent), "Cannot write into accepted storage")
    closure = manifest["module_closures"]["bsc"]
    require(closure["source_commit"] == COMMIT and closure["source_archive"]["sha256"] == ARCHIVE, "Unexpected native source")
    proof = record("bsc-full-type-import-preparation", 3600); proof["build_manifest"] = manifest_ref

    def body(output, runner):
        origins, proof["code"] = keep_code(output, deadline)
        root = output / "bsc"; root.mkdir()
        _, archive, tree = source.extract_source(manifest, manifest_path, root, deadline)
        patched, installed = bsc_decision.install(tree, root, deadline)
        for name in ("corpus_test.go", "native_test.go"):
            target = tree / PACKAGE / ("modern_type_paths_test.go" if name == "native_test.go" else name)
            require(not target.exists(), "Probe target exists")
            copy_artifact(INPUTS[name], target, reference(INPUTS[name]), deadline=deadline)
        for name in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (root / name).mkdir()
        for name in ("go.mod", "go.sum"):
            copy_artifact(tree / name, root / "metadata" / name, reference(tree / name), deadline=deadline)
        compiler = manifest["builds"]["bsc"]["compiler_image"]
        require(compiler == manifest["image_inputs"]["golang-build-image"]["reference"], "Compiler mismatch")
        mounts = source.build_mounts(tree, manifest_path.parent / "cache/bsc/mod", root)
        bsc_decision.build(runner, compiler, mounts, BINARY, sorted({row["path"] for item in patched for row in item["files"]}))
        sources_unchanged(origins, deadline)
        for row in installed:
            require(reference(Path(row["source"]["path"]), deadline=deadline) == row["source"], "Installed source changed")
        tool = {"schema_version": 1, "kind": "bsc-full-type-import-tool", "native_source_commit": COMMIT,
                "source_archive": archive, "compiler": compiler, "build_manifest": manifest_ref,
                "inputs": {name: reference(path) for name, path in INPUTS.items()}, "patched": patched, "installed": installed,
                "binary": reference(root / "generated" / BINARY, 1024 * 1024 * 1024, deadline)}
        pins.save(output / "tool.json", tool); proof["tool"] = reference(output / "tool.json", deadline=deadline)
    return pins.owned_attempt(output, started, deadline, proof, "preparation.json", body)


def run(tool_path, sha, output, diagnostic=False):
    started = time.monotonic(); deadline = started + 900
    require(tool_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, sha, deadline=deadline)
    require(tool.get("kind") == "bsc-full-type-import-tool" and tool.get("native_source_commit") == COMMIT, "Unexpected tool")
    require(tool["inputs"] == {name: reference(path) for name, path in INPUTS.items()}, "Prepared source differs")
    binary = artifact(tool["binary"], 1024 * 1024 * 1024, deadline=deadline)
    proof = record("bsc-full-type-import-run", 900); proof["tool"] = tool_ref
    proof["diagnostic_only"] = diagnostic

    def body(output, runner):
        origins, proof["code"] = keep_code(output, deadline)
        work, scratch = output / "probe", output / "scratch"; work.mkdir(); scratch.mkdir()
        stdout = runner.command(tool["compiler"], ["/probe", "-test.v", "-test.run", "^" + TEST + "$", "-test.count=1", "-test.timeout", "700s"],
                                [(binary, "/probe", True), (work, "/work", False), (scratch, "/scratch", False)],
                                cap=750, memory="3g", cwd="/scratch", environment={"EXTRACTOR_MODERN_WORK": "/work",
                                "EXTRACTOR_DECISION_NATIVE_COMMIT": COMMIT, "HOME": "/scratch", "TMPDIR": "/scratch"})
        proof["tests"] = pins.parse_go_test(read_bytes(stdout, 4 * 1024 * 1024).decode(), [TEST])
        proof["outputs"] = {p.name: reference(p, 64 * 1024 * 1024, deadline) for p in work.iterdir() if p.is_file()}
        checked = judge.check(load(work / "report.json"), load(work / "generated.json"), load(work / "genesis.json"))
        pins.save(output / "judgement.json", checked); proof["judgement"] = reference(output / "judgement.json")
        require(diagnostic or checked["result"] == "PASS", "Independent judge failed: " + str(checked["errors"]))
        sources_unchanged(origins, deadline); artifact(tool["binary"], 1024 * 1024 * 1024, deadline=deadline)
    return pins.owned_attempt(output, started, deadline, proof, "run.json", body)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("prepare", "run", "diagnose")); parser.add_argument("input", type=Path)
    parser.add_argument("--sha256", required=True); parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        result = prepare(args.input, args.sha256, args.output) if args.phase == "prepare" else run(args.input, args.sha256, args.output, diagnostic=args.phase == "diagnose")
        print(json.dumps(result, indent=2)); return int(result["result"] != "PASS")
    except (Rejected, OSError, ValueError, KeyError, TypeError) as error:
        print(f"Rejected: {error}", file=sys.stderr); return 2


if __name__ == "__main__":
    raise SystemExit(main())
