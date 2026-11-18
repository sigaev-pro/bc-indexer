#!/usr/bin/env python3
"""Bounded Polygon fixture diagnostics; acceptance stays pending the strict judge."""

import argparse
import copy
import importlib.util
import json
from pathlib import Path
import re
import signal
import sys
import time
import uuid

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
HARNESS = ROOT / "test/harness"
CORPUS = ROOT / "test/native_corpus/poly"
sys.path[:0] = [str(HARNESS), str(ROOT / "test/native_corpus"), str(CORPUS)]
import admission
import bor_admission_tree
import capture_probe
import pins
import poly_corpus
from artifacts import (artifact, copy_artifact, load, pinned_json, read_bytes,
                       reference, require, sources_unchanged)


def local_module(name):
    spec = importlib.util.spec_from_file_location("type_paths_" + name, HERE / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


profile, support = local_module("poly_profile"), local_module("poly_support")
FILES = ("poly_native.py", "poly_profile.py", "poly_support.py", "probe.go", "ORACLE.md")
LIMIT = 512 * 1024 * 1024


def keep_code(output, deadline):
    origins = []
    copies = []
    for directory, names, destination in (
        (HERE, FILES, "fixture"),
        (CORPUS, ("main.go", "poly_corpus.py"), "corpus"),
        (HARNESS, ("pins.py", "native.py", "owned.py", "artifacts.py", "capture_probe.py",
                   "bor_admission_tree.py", "admission.py"), "harness"),
    ):
        for name in names:
            origin = reference(directory / name, deadline=deadline)
            origins.append(origin)
            copies.append(copy_artifact(directory / name, output / destination / name, origin, deadline=deadline))
    return origins, copies


def new_record(kind, seconds):
    return {"schema_version": 1, "kind": kind, "source_commit": pins.source_commit(),
            "result": "FAIL", "run_id": uuid.uuid4().hex, "deadline_seconds": seconds,
            "public_profile_credit": False, "ac_verdict": None, "error": None,
            "scope": "private-fixture-diagnostic-only"}


def prepare(manifest_path, sha, output):
    started = time.monotonic()
    deadline = started + 3600
    require(manifest_path.is_absolute() and output.is_absolute(), "Absolute paths required")
    manifest_ref, manifest = pinned_json(manifest_path, sha, 64 * 1024 * 1024, deadline)
    require(manifest.get("evidence_kind") == "actual-build" and manifest.get("task_id") == "E01-01.builds",
            "Accepted actual build required")
    capture_probe.refuse_inside(output, (manifest_path.parent, ROOT))
    closure = manifest["module_closures"]["bor"]
    require(closure["source_commit"] == profile.SOURCE_COMMIT and
            closure["source_archive"]["sha256"] == profile.SOURCE_ARCHIVE_SHA256, "Unexpected Bor source")
    compiler = manifest["image_inputs"]["golang-build-image"]["reference"]
    runtime = manifest["image_inputs"]["debian-runtime-image"]["reference"]
    require(manifest["builds"]["bor"]["source_commit"] == profile.SOURCE_COMMIT and
            manifest["builds"]["bor"]["compiler_image"] == compiler, "Build identity mismatch")
    require(re.fullmatch(r"docker.io/library/golang@sha256:[0-9a-f]{64}", compiler) and
            re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned images")
    proof = new_record("poly-type-paths-preparation", 3600)
    proof["build_manifest"] = manifest_ref

    def body(output, runner):
        origins, proof["code"] = keep_code(output, deadline)
        directory = output / "bor"
        directory.mkdir()
        archive = artifact(closure["source_archive"], LIMIT, manifest_path.parent, deadline)
        source = directory / "source"
        source.mkdir()
        poly_corpus.extract(read_bytes(archive, LIMIT, deadline), source, deadline)
        proof["installed"] = bor_admission_tree.install(source)
        probe = source / poly_corpus.PROBE_DIR
        probe.mkdir(parents=True)
        accepted = read_bytes(CORPUS / "main.go", 4 * 1024 * 1024, deadline)
        (probe / "corpus_support.go").write_bytes(support.render_support(accepted))
        copy_artifact(HERE / "probe.go", probe / "main.go", reference(HERE / "probe.go"), deadline=deadline)
        capture_probe.copy_sources(("stub.go",), poly_corpus.STUB_SOURCES, probe / "heimdallstub", deadline)
        for name in ("bin", "build-cache", "gopath", "gotmp", "inputs", "bodies"):
            (directory / name).mkdir()
        mounts = [(directory, "/work", True), (manifest_path.parent / "cache/bor/mod", "/modules", True)]
        mounts += [(directory / name, "/work/" + name, False) for name in ("bin", "build-cache", "gopath", "gotmp")]
        for argv, cap in ((["mod", "verify"], 300), (["vet", "./" + poly_corpus.PROBE_DIR + "/..."], 1200),
                          (["build", "-p", "2", "-trimpath", "-o", "/work/bin/probe", "./" + poly_corpus.PROBE_DIR], 1800)):
            runner.command(compiler, ["/usr/local/go/bin/go", *argv], mounts, cap=cap, memory="6g",
                           cwd="/work/source", environment=poly_corpus.go_environment())
        source_genesis = (source / "tests/bor/testdata/genesis.json").read_bytes()
        genesis, _ = poly_corpus.derived_inputs(source_genesis, load(poly_corpus.EXPECTED))
        document = profile.private_genesis(json.loads(genesis))
        (directory / "inputs/genesis.json").write_bytes(admission.canonical(json.dumps(document).encode()))
        (directory / "inputs/chain-config.json").write_bytes(admission.canonical(json.dumps(document["config"]).encode()))
        capture_probe.copy_sources(("helper.go",), source / "tests/bor", directory / "inputs", deadline)
        capture_probe.copy_sources(poly_corpus.BODY_FILES, CORPUS / "stub", directory / "bodies", deadline)
        events_path = directory / "bodies/clerk-corpus-from-1.json"
        events = load(events_path)
        second = copy.deepcopy(events["event_records"][0])
        second.update(id="2", contract="0x3000000000000000000000000000000000000002",
                      tx_hash="0x" + "00" * 31 + "02", log_index="2")
        events["event_records"].append(second)
        events_path.write_text(json.dumps(events, indent=2) + "\n")
        tool = {"kind": "poly-type-paths-diagnostic-tool", "native_source": profile.SOURCE_COMMIT,
                "source_archive_sha256": profile.SOURCE_ARCHIVE_SHA256, "runtime": runtime,
                "binary": reference(directory / "bin/probe", LIMIT, deadline),
                "fixture": {name: reference(HERE / name, deadline=deadline) for name in FILES},
                "inputs": [reference(p, deadline=deadline) for p in sorted((directory / "inputs").iterdir())],
                "bodies": [reference(p, deadline=deadline) for p in sorted((directory / "bodies").iterdir())],
                "support": reference(probe / "corpus_support.go", deadline=deadline)}
        sources_unchanged(origins, deadline)
        pins.save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "preparation.json", body)


def run(tool_path, sha, output):
    started = time.monotonic()
    deadline = started + 300
    require(tool_path.is_absolute() and output.is_absolute(), "Absolute paths required")
    tool_ref, tool = pinned_json(tool_path, sha, deadline=deadline)
    capture_probe.refuse_inside(output, (tool_path.parent, ROOT))
    require(tool.get("kind") == "poly-type-paths-diagnostic-tool" and
            tool.get("native_source") == profile.SOURCE_COMMIT and
            tool.get("source_archive_sha256") == profile.SOURCE_ARCHIVE_SHA256, "Wrong native tool")
    require(set(tool["fixture"]) == set(FILES) and all(
        tool["fixture"][name] == reference(HERE / name) for name in FILES), "Prepared fixture source mismatch")
    require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", tool["runtime"]), "Unpinned runtime")
    binary = artifact(tool["binary"], LIMIT, deadline=deadline)
    for field, expected in (("inputs", {"genesis.json", "chain-config.json", "helper.go"}),
                            ("bodies", set(poly_corpus.BODY_FILES))):
        require({Path(item["path"]).name for item in tool[field]} == expected and len(tool[field]) == len(expected),
                "Incomplete or duplicate prepared " + field)
        require(all(Path(item["path"]).parent == tool_path.parent / "bor" / field for item in tool[field]),
                "Prepared inputs escape owned preparation")
        for item in tool[field]:
            artifact(item, 4 * 1024 * 1024, deadline=deadline)
    inputs = Path(tool["inputs"][0]["path"]).parent
    bodies = Path(tool["bodies"][0]["path"]).parent
    proof = new_record("poly-type-paths-diagnostic-run", 300)
    proof["tool"] = tool_ref

    def body(output, runner):
        origins, proof["code"] = keep_code(output, deadline)
        destination = output / "probe"
        destination.mkdir()
        runner.command(tool["runtime"], ["/bin/probe", "/inputs", "/bodies", "/inputs/helper.go",
                                        "/out/native", "/out/observation.json"],
                       [(binary.parent, "/bin", True), (inputs, "/inputs", True), (bodies, "/bodies", True),
                        (destination, "/out", False)], cap=240, memory="2g")
        proof["observation"] = reference(destination / "observation.json", 16 * 1024 * 1024, deadline)
        proof["acceptance"] = "NOT_EVALUATED: diagnostic only; strict judge and negative controls pending"
        sources_unchanged(origins, deadline)

    return pins.owned_attempt(output, started, deadline, proof, "run.json", body)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("prepare", "run"))
    parser.add_argument("input", type=Path)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    def stop(number, frame):
        raise InterruptedError(signal.Signals(number).name)
    for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(number, stop)
    result = (prepare if args.phase == "prepare" else run)(args.input, args.sha256, args.output)
    print(json.dumps({"result": result["result"], "error": result["error"], "ac_verdict": None}))
    return int(result["result"] != "PASS")


if __name__ == "__main__":
    raise SystemExit(main())
