#!/usr/bin/env python3
"""Prepare local input blocks offline. Preparation is not a measured test run."""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import sys
import time
import tarfile
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import artifact, build_input, load, reference, require, source_snapshot, sources_unchanged
from native import Native, new_output


def prepare(manifest_path, expected_sha, output, previous=None, correction=None):
    manifest_ref = reference(manifest_path)
    require(manifest_ref["sha256"] == expected_sha, "BuildManifest digest mismatch")
    _, manifest, _, _ = build_input(manifest_ref, "ethereum-mainnet")
    previous_ref = None
    attempt = 1
    if previous is not None:
        old = load(previous)
        require(old["kind"] == "fixture-preparation" and old["result"] == "FAIL" and
                old["build_manifest"] == manifest_ref and correction, "Invalid preparation retry")
        previous_ref = reference(previous)
        attempt = old.get("attempt_index", 1) + 1
    else:
        require(correction is None, "Correction needs a previous failed attempt")
    require(not output.is_relative_to(manifest_path.parent), "Preparation cannot write inside accepted build storage")
    output = new_output(output)
    started = time.monotonic()
    runner = Native(output, started + 900)
    proof = {"schema_version": 1, "kind": "fixture-preparation", "result": "FAIL",
             "build_manifest": manifest_ref, "native_import": "NOT_RUN", "run_id": uuid.uuid4().hex,
             "attempt_index": attempt, "previous_attempt": previous_ref, "correction": correction}
    origins, proof["harness_sources"] = source_snapshot(output, ("prepare.py", "artifacts.py", "native.py", "owned.py"))
    proof["native_owner_nonce"] = runner.nonce
    try:
        fixture_dir = Path(__file__).with_name("fixtures")
        for filename in ("genesis.json", "expected.json", "ORACLE.md", "generate.go"):
            shutil.copyfile(fixture_dir / filename, output / filename)
        # Freeze the independent oracle before running the native input generator.
        proof["expected_before_generation"] = reference(output / "expected.json")
        proof["oracle_notes"] = reference(output / "ORACLE.md")
        closure = manifest["module_closures"]["geth"]
        archive = artifact(closure["source_archive"], 256 * 1024 * 1024, manifest_path.parent)
        require(closure["source_commit"] == manifest["builds"]["geth"]["source_commit"], "Source closure mismatch")
        source = output / "source"
        source.mkdir()
        with tarfile.open(archive) as stream:
            stream.extractall(source, filter="data")
        proof["source_archive"] = closure["source_archive"]
        for row in closure["artifacts"]:
            require(time.monotonic() < started + 850, "Preparation deadline expired")
            for field in ("info", "mod", "zip"):
                artifact(row[field], 1024 * 1024 * 1024, manifest_path.parent)
        metadata = output / "metadata"
        metadata.mkdir()
        for filename in ("go.mod", "go.sum"):
            shutil.copyfile(source / filename, metadata / filename)
        compiler = manifest["image_inputs"]["golang-build-image"]["reference"]
        require(compiler.startswith("docker.io/library/golang@sha256:"), "Unpinned compiler")
        mounts = [(source, "/source", True), (manifest_path.parent / "cache/geth/mod", "/modules", True),
                  (output, "/work", True)]
        for directory in ("generated", "build-cache", "gopath"):
            (output / directory).mkdir()
        for directory in ("generated", "build-cache", "gopath", "metadata"):
            mounts.append((output / directory, "/work/" + directory, False))
        environment = {"CGO_ENABLED": "0", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64",
                                    "GOCACHE": "/work/build-cache", "GOMODCACHE": "/modules", "GOPATH": "/work/gopath",
                                    "GOFLAGS": "-mod=readonly -modfile=/work/metadata/go.mod", "GOPROXY": "off",
                                    "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOWORK": "off"}
        runner.command(compiler, ["/usr/local/go/bin/go", "mod", "verify"], mounts,
                       cap=180, memory="4g", cwd="/source", environment=environment)
        runner.command(compiler, ["/usr/local/go/bin/go", "build", "-p", "2", "-trimpath",
                                  "-o", "/work/generated/generate", "/work/generate.go"], mounts,
                       cap=700, memory="4g", cwd="/source", environment=environment)
        proof["generator_binary"] = reference(output / "generated/generate")
        runner.command(compiler, ["/work/generated/generate", "/work/genesis.json", "/work/generated"],
                       mounts, cap=120, memory="1g")
        require(reference(output / "expected.json") == proof["expected_before_generation"], "Oracle changed during preparation")
        metadata_value = load(output / "generated/generated.json")
        genesis = load(output / "genesis.json")
        config_sha = hashlib.sha256(json.dumps(genesis["config"], sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        fixture = {"schema_version": 1, "identity": {"kind": "private-local", "chain_id": 31337,
                   "genesis_hash": metadata_value["genesis_hash"], "config_sha256": config_sha},
                   "prepared_blocks": metadata_value["blocks"], "generator": reference(output / "generate.go")}
        for field, filename in {"genesis": "genesis.json", "blocks": "generated/blocks.rlp", "full_chain": "generated/full-chain.rlp",
                                "expected": "expected.json"}.items():
            fixture[field] = reference(output / filename)
        (output / "fixture.json").write_text(json.dumps(fixture, sort_keys=True, indent=2) + "\n")
        request = {"schema_version": 1, "build_manifest": manifest_ref,
                   "profile_association": "ethereum-mainnet", "fixture": reference(output / "fixture.json"),
                   "deadline_seconds": 300}
        (output / "request.json").write_text(json.dumps(request, sort_keys=True, indent=2) + "\n")
        sources_unchanged(origins)
        proof.update(result="PASS", fixture=reference(output / "fixture.json"), request=reference(output / "request.json"))
    finally:
        proof.update(commands=runner.commands, elapsed_seconds=time.monotonic() - started)
        (output / "preparation.json").write_text(json.dumps(proof, sort_keys=True, indent=2) + "\n")
    return output / "request.json"


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--build-manifest", type=Path, required=True)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--previous-attempt", type=Path)
    parser.add_argument("--correction")
    args = parser.parse_args()
    try:
        print(prepare(args.build_manifest, args.sha256, args.output, args.previous_attempt, args.correction))
    except (ValueError, OSError, KeyError, TypeError) as error:
        print(json.dumps({"valid": False, "error": str(error)}))
        raise SystemExit(2)
