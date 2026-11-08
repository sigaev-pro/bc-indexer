"""Admission for BSC scenario inputs and a separately caller-pinned observer."""

import hashlib
import json
import re
import time

from artifacts import (artifact, closed, load, reference, require, check_deadline,
                       json_artifact, pinned_json, read_artifact)
from bsc import validate_oracle


def canonical_digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def build_input(value, deadline):
    path, manifest = json_artifact(value, deadline=deadline)
    require(manifest.get("evidence_kind") == "actual-build" and manifest.get("task_id") == "E01-01.builds", "Actual build required")
    build, closure = manifest["builds"]["bsc"], manifest["module_closures"]["bsc"]
    require(re.fullmatch(r"[0-9a-f]{40}", build["source_commit"]) and closure["source_commit"] == build["source_commit"], "BSC source mismatch")
    profiles = [row for row in manifest["profile_bindings"] if row["id"] == "bsc-mainnet"]
    require(len(profiles) == 1 and profiles[0]["native_component"] == "bsc" and
            profiles[0]["native_binary_sha256"] == build["binary"]["sha256"], "BSC profile association mismatch")
    binary = artifact(build["binary"], 256 * 1024 * 1024, path.parent, deadline)
    runtime = manifest["image_inputs"]["debian-runtime-image"]["reference"]
    require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime")
    return manifest, binary, runtime


def admit(request_path, tool_path, tool_sha, started=None, deadline=None):
    started = time.monotonic() if started is None else started
    deadline = min(deadline or started + 1800, started + 1800)
    request = load(request_path, 1024 * 1024, deadline)
    closed(request, {"schema_version", "kind", "build_manifest", "profile_association", "fixture", "deadline_seconds"}, "BSC request")
    require(type(request["schema_version"]) is int and request["schema_version"] == 1 and request["kind"] == "bsc-native-import" and
            request["profile_association"] == "bsc-mainnet", "Wrong BSC request")
    require(type(request["deadline_seconds"]) is int and 1 <= request["deadline_seconds"] <= 1800, "Invalid measured deadline")
    deadline = min(deadline, started + request["deadline_seconds"])
    check_deadline(deadline)
    # This digest comes from the caller, never from reauthorizable scenario bytes.
    require(isinstance(tool_sha, str) and re.fullmatch(r"[0-9a-f]{64}", tool_sha), "External observer-tool digest required")
    tool_ref, tool = pinned_json(tool_path, tool_sha, 1024 * 1024, deadline)
    closed(tool, {"schema_version", "kind", "build_manifest", "source_commit", "source_archive", "module_closure_sha256",
                  "compiler", "source", "binary", "public_validator", "commands", "metadata"}, "Observer tool")
    require(type(tool["schema_version"]) is int and tool["schema_version"] == 1 and tool["kind"] == "bsc-native-observer" and
            tool["build_manifest"] == request["build_manifest"], "Observer build binding mismatch")
    manifest, binary, image = build_input(request["build_manifest"], deadline)
    closure = manifest["module_closures"]["bsc"]
    require(tool["source_commit"] == closure["source_commit"] and tool["module_closure_sha256"] == canonical_digest(closure) and
            tool["compiler"] == manifest["image_inputs"]["golang-build-image"]["reference"] and
            tool["public_validator"] == "0x03735c2ed70a56cd221e0024eb4bf90243c9d6e9", "Observer native provenance mismatch")
    require(all(tool["source_archive"][field] == closure["source_archive"][field] for field in ("sha256", "size_bytes")), "Observer source archive mismatch")
    for name in ("source_archive", "source", "binary"):
        artifact(tool[name], 256 * 1024 * 1024, deadline=deadline)
    require(isinstance(tool["metadata"], list) and len(tool["metadata"]) == 2, "Missing Go metadata")
    for ref in tool["metadata"]:
        artifact(ref, 1024 * 1024, deadline=deadline)
    require(isinstance(tool["commands"], list) and len(tool["commands"]) == 3, "Missing preparation command provenance")
    tails = (["/usr/local/go/bin/go", "mod", "verify"],
             ["/usr/local/go/bin/go", "build", "-p", "2", "-trimpath", "-o", "/work/generated/bsc-helper", "/work/helper.go"],
             ["/work/generated/bsc-helper", "verify-key", "/work/validator-keystore.json"])
    for index, row in enumerate(tool["commands"]):
        closed(row, {"request", "result", "stdout", "stderr", "cleanup"}, "Tool command")
        paths = {key: artifact(ref, 8 * 1024 * 1024, deadline=deadline) for key, ref in row.items()}
        _, result = json_artifact(row["result"], 8 * 1024 * 1024, deadline=deadline)
        _, command_record = json_artifact(row["request"], 8 * 1024 * 1024, deadline=deadline)
        command = command_record["argv"]
        require(type(result["exit_code"]) is int and result["exit_code"] == 0 and result["reason"] is None and
                result["cleanup_complete"] is True and result["argv"] == command, "Observer command failed or changed")
        require(isinstance(command, list) and all(isinstance(value, str) for value in command) and
                command.count(tool["compiler"]) == 1 and command[command.index(tool["compiler"]) + 1:] == tails[index],
                "Unexpected observer preparation command")
        for flag, value in (("--network", "none"), ("--platform", "linux/amd64"), ("--pull", "never")):
            require(command.count(flag) == 1 and command[command.index(flag) + 1] == value, "Observer isolation mismatch")
        require(command.count("--read-only") == 1, "Observer preparation root must be read-only")
        if index < 2:
            environment = [command[position + 1] for position, value in enumerate(command) if value == "--env"]
            names = [value.split("=", 1)[0] for value in environment]
            require(len(names) == len(set(names)) and all(value in environment for value in (
                "CGO_ENABLED=1", "GOOS=linux", "GOARCH=amd64", "GOPROXY=off", "GOSUMDB=off",
                "GOFLAGS=-mod=readonly -modfile=/work/metadata/go.mod", "GOMODCACHE=/modules",
                "GOTMPDIR=/work/build-temp", "TMPDIR=/work/build-temp")), "Observer Go isolation mismatch")
            require(command.count("--workdir") == 1 and command[command.index("--workdir") + 1] == "/source", "Wrong BSC compile root")
        else:
            _, public = read_artifact(row["stdout"], 1024, deadline=deadline)
            require(public.decode().strip() == tool["public_validator"], "Native public key verification mismatch")
    fixture_path, fixture = json_artifact(request["fixture"], 1024 * 1024, deadline=deadline)
    closed(fixture, {"schema_version", "kind", "identity", "native_config", "prepared_blocks", "genesis", "blocks", "full_chain", "expected"}, "BSC fixture")
    require(type(fixture["schema_version"]) is int and fixture["schema_version"] == 1 and fixture["kind"] == "bsc-parlia", "Wrong BSC fixture")
    paths = {key: artifact(fixture[key], 16 * 1024 * 1024, deadline=deadline) for key in ("genesis", "blocks", "full_chain", "expected")}
    _, genesis = json_artifact(fixture["genesis"], 16 * 1024 * 1024, deadline=deadline)
    identity = fixture["identity"]
    closed(identity, {"kind", "chain_id", "genesis_hash", "config_sha256"}, "Private identity")
    require(identity["kind"] == "private-local" and type(identity["chain_id"]) is int and identity["chain_id"] == genesis["config"]["chainId"] == 714 and
            identity["config_sha256"] == canonical_digest(genesis["config"]) and re.fullmatch(r"0x[0-9a-f]{64}", identity["genesis_hash"]), "Private config identity mismatch")
    _, expected = json_artifact(fixture["expected"], 16 * 1024 * 1024, deadline=deadline)
    validate_oracle(expected)
    require(isinstance(fixture["prepared_blocks"], list) and len(fixture["prepared_blocks"]) == 2, "Two prepared blocks required")
    for number, block in enumerate(fixture["prepared_blocks"], 1):
        closed(block, {"number", "hash", "state_root", "timestamp", "transactions", "gas_used"}, "Prepared block")
        require(type(block["number"]) is int and block["number"] == number and
                all(type(block[key]) is int and block[key] >= 0 for key in ("timestamp", "transactions", "gas_used")) and
                all(re.fullmatch(r"0x[0-9a-f]{64}", block[key]) for key in ("hash", "state_root")), "Invalid prepared block")
    return {"request": request, "manifest": manifest, "binary": binary, "image": image, "fixture": fixture,
            "paths": paths, "expected": expected, "deadline": deadline, "tool": tool, "tool_ref": tool_ref}
