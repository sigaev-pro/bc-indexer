#!/usr/bin/env python3
"""Prepare a separately pinned Bor tool and private Bor inputs offline."""

import argparse
import hashlib
import io
import json
from pathlib import Path
import signal
import sys
import tarfile
import time
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import (artifact, check_deadline, copy_artifact, load, read_bytes,
                       reference, require, source_snapshot, sources_unchanged, captured_json, json_artifact)
from native import Native, new_output
from bor_inputs import build_input, canonical_digest

KEY = "tests/bor/helper.go"


def save(path, value):
    with path.open("x") as stream:
        stream.write(json.dumps(value, sort_keys=True, indent=2) + "\n")


def prepare(manifest_path, expected_sha, output, previous=None, correction=None):
    started = time.monotonic()
    deadline = started + 1800
    manifest_ref = reference(manifest_path, 64 * 1024 * 1024, deadline)
    require(manifest_ref["sha256"] == expected_sha, "BuildManifest digest mismatch")
    manifest, _, _ = build_input(manifest_ref, deadline)
    previous_ref, attempt = None, 1
    if previous is not None:
        previous_ref, old = captured_json(previous, deadline=deadline)
        require(old["kind"] == "bor-fixture-preparation" and old["result"] in ("FAIL", "PASS") and
                old["build_manifest"] == manifest_ref and correction, "Invalid preparation retry")
        if old["result"] == "PASS":
            _, previous_tool = json_artifact(old["observer_tool"], deadline=deadline)
            current = reference(Path(__file__).with_name("fixtures") / "bor/helper.go", 1024 * 1024, deadline)
            require(current["sha256"] != previous_tool["source"]["sha256"],
                    "A successful preparation can only be followed by an explicitly changed tool")
        attempt = old["attempt_index"] + 1
    else:
        require(correction is None, "Correction needs an earlier failed preparation")
    require(not output.is_relative_to(manifest_path.parent), "Preparation cannot modify accepted build storage")
    output = new_output(output)
    runner = Native(output, deadline)
    proof = {"schema_version": 1, "kind": "bor-fixture-preparation", "result": "FAIL",
             "build_manifest": manifest_ref, "run_id": uuid.uuid4().hex, "native_owner_nonce": runner.nonce,
             "attempt_index": attempt, "previous_attempt": previous_ref, "correction": correction,
             "runtime_import": "NOT_RUN", "deadline_seconds": 1800}
    origins, proof["harness_sources"] = source_snapshot(output, (
        "prepare_bor.py", "bor_inputs.py", "bor.py", "artifacts.py", "native.py", "owned.py"), deadline)
    try:
        fixture_dir = Path(__file__).with_name("fixtures") / "bor"
        for filename in ("expected.json", "ORACLE.md", "helper.go"):
            ref = reference(fixture_dir / filename, 1024 * 1024, deadline)
            copy_artifact(fixture_dir / filename, output / filename, ref, 1024 * 1024, deadline)
        proof["expected_before_generation"] = reference(output / "expected.json", deadline=deadline)
        proof["oracle_notes"] = reference(output / "ORACLE.md", deadline=deadline)
        closure = manifest["module_closures"]["bor"]
        archive = artifact(closure["source_archive"], 256 * 1024 * 1024, manifest_path.parent, deadline)
        source = output / "source"
        source.mkdir()
        proof["source_archive"] = copy_artifact(archive, output / "source.tar", closure["source_archive"],
                                                256 * 1024 * 1024, deadline)
        data = read_bytes(output / "source.tar", closure["source_archive"]["size_bytes"], deadline)
        with tarfile.open(fileobj=io.BytesIO(data)) as stream:
            for member in stream:
                check_deadline(deadline)
                stream.extract(member, source, filter="data")
        del data
        for row in closure["artifacts"]:
            for field in ("info", "mod", "zip"):
                artifact(row[field], 1024 * 1024 * 1024, manifest_path.parent, deadline)
        original = source / "tests/bor/testdata/genesis.json"
        proof["native_genesis"] = reference(original, deadline=deadline)
        require(proof["native_genesis"]["sha256"] == "aff8d66c9e67bfdacad48372c4f26d1bcb58207fa6fcae09dc28bc5c1c83e483",
                "Unexpected pinned native genesis")
        genesis = load(original, deadline=deadline)
        sender = "7e5f4552091a69125d5dfcb7b8c2659029395bdf"
        require(sender not in genesis["alloc"] and "0x" + sender not in genesis["alloc"], "Sender already allocated")
        genesis["alloc"][sender] = {"balance": "10000000000000000000"}
        save(output / "genesis.json", genesis)
        copy_artifact(source / KEY, output / "validator-source.go", reference(source / KEY, deadline=deadline), deadline=deadline)
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (output / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            copy_artifact(source / name, output / "metadata" / name, reference(source / name, deadline=deadline), deadline=deadline)
        compiler = manifest["image_inputs"]["golang-build-image"]["reference"]
        mounts = [(source, "/source", True), (manifest_path.parent / "cache/bor/mod", "/modules", True), (output, "/work", True)]
        mounts += [(output / name, "/work/" + name, False) for name in ("metadata", "generated", "build-cache", "build-temp", "gopath")]
        environment = {"CGO_ENABLED": "1", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64",
                       "GOCACHE": "/work/build-cache", "GOMODCACHE": "/modules", "GOPATH": "/work/gopath",
                       "GOTMPDIR": "/work/build-temp", "TMPDIR": "/work/build-temp",
                       "GOFLAGS": "-mod=readonly -modfile=/work/metadata/go.mod", "GOPROXY": "off", "GOSUMDB": "off",
                       "GOTOOLCHAIN": "local", "GOWORK": "off"}
        runner.command(compiler, ["/usr/local/go/bin/go", "mod", "verify"], mounts, cap=240, memory="4g", cwd="/source", environment=environment)
        runner.command(compiler, ["/usr/local/go/bin/go", "build", "-p", "2", "-trimpath", "-o", "/work/generated/bor-helper", "/work/helper.go"],
                       mounts, cap=1200, memory="4g", cwd="/source", environment=environment)
        public = runner.command(compiler, ["/work/generated/bor-helper", "verify-key", "/work/validator-source.go"], mounts, cap=60, memory="1g")
        require(read_bytes(public, 1024, deadline).decode().strip() == "0x71562b71999873db5b286df957af199ec94617f7", "Validator verification failed")
        tool = {"schema_version": 1, "kind": "bor-native-observer", "build_manifest": manifest_ref,
                "source_commit": closure["source_commit"], "source_archive": proof["source_archive"],
                "module_closure_sha256": canonical_digest(closure), "compiler": compiler,
                "source": reference(output / "helper.go", deadline=deadline),
                "binary": reference(output / "generated/bor-helper", deadline=deadline),
                "public_validator": "0x71562b71999873db5b286df957af199ec94617f7", "commands": list(runner.commands),
                "metadata": [reference(output / "metadata" / name, deadline=deadline) for name in ("go.mod", "go.sum")]}
        save(output / "observer-tool.json", tool)
        proof["observer_tool"] = reference(output / "observer-tool.json", deadline=deadline)
        runner.command(compiler, ["/work/generated/bor-helper", "generate", "/work/genesis.json", "/work/validator-source.go", "/work/generated"],
                       mounts, cap=180, memory="2g")
        require(reference(output / "expected.json", deadline=deadline) == proof["expected_before_generation"], "Oracle changed")
        generated = load(output / "generated/generated.json", deadline=deadline)
        fixture = {"schema_version": 1, "kind": "bor-private", "identity": {"kind": "private-local", "chain_id": 15001,
                   "genesis_hash": generated["genesis_hash"], "config_sha256": canonical_digest(genesis["config"])},
                   "native_config": generated["config"], "prepared_blocks": generated["prepared_blocks"]}
        for name, path in {"genesis": "genesis.json", "blocks": "generated/blocks.rlp", "full_chain": "generated/full-chain.rlp",
                           "expected": "expected.json"}.items():
            fixture[name] = reference(output / path, deadline=deadline)
        save(output / "fixture.json", fixture)
        request = {"schema_version": 1, "kind": "bor-native-import", "build_manifest": manifest_ref,
                   "profile_association": "polygon-pos-mainnet", "fixture": reference(output / "fixture.json", deadline=deadline), "deadline_seconds": 300}
        save(output / "request.json", request)
        sources_unchanged(origins, deadline)
        proof.update(result="PASS", request=reference(output / "request.json", deadline=deadline), fixture=reference(output / "fixture.json", deadline=deadline))
    finally:
        proof.update(commands=runner.commands, elapsed_seconds=time.monotonic() - started, cleanup_complete=runner.cleanup_complete)
        save(output / "preparation.json", proof)
    return output / "request.json"


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--build-manifest", type=Path, required=True)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--previous-attempt", type=Path)
    parser.add_argument("--correction")
    args = parser.parse_args()
    def interrupt(*_):
        raise InterruptedError("Preparation interrupted")
    for number in (signal.SIGINT, signal.SIGTERM):
        signal.signal(number, interrupt)
    print(prepare(args.build_manifest, args.sha256, args.output, args.previous_attempt, args.correction))
