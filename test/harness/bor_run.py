#!/usr/bin/env python3
"""Run a finite private Bor server and IPC import with caller-pinned tools."""

import argparse
import json
from pathlib import Path
import signal
import sys
import time
import tomllib
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import (Rejected, check_deadline, copy_artifact, load, read_bytes, read_artifact,
                       reference, require, source_snapshot, sources_unchanged)
from bor import compare_native
from bor_inputs import admit, canonical_digest, revalidate_authority
from native import Native, new_output
from run import PROFILES

RUNTIME_FILES = ("server.argv.json", "server-config.toml", "config.stderr", "server.stdout", "server.stderr",
                 "rpc.requests", "rpc.responses", "readiness.json", "server-cleanup.json", "full-chain.rlp",
                 "receipts-1.json", "receipts-2.json", "state-1.json", "state-2.json", "observed.json")


def validate_runtime(directory, admitted, deadline):
    config = tomllib.loads(read_bytes(directory / "server-config.toml", 1024 * 1024, deadline).decode())
    for section, field in (("pipeline", "enable-import-src"), ("parallelevm", "enable"),
                           ("witness", "parallel-stateless-import"), ("relay", "enable-preconfs"),
                           ("jsonrpc", "accept-preconf-tx")):
        require(config.get(section, {}).get(field) is False, "Accelerated native path is not disabled")
    require(config.get("devfakeauthor") is False and config.get("heimdall", {}).get("bor.without") is True and
            config.get("syncmode") == "full" and config.get("gcmode") == "archive" and
            config.get("cache", {}).get("preimages") is True, "Unexpected native execution config")
    args = load(directory / "server.argv.json", deadline=deadline)
    expected = ["/inputs/bor", "server", "--chain=/inputs/genesis.json", "--datadir=/work/runtime/data",
                "--ipcpath=/work/runtime/bor.ipc", "--ipcdisable=false", "--syncmode=full", "--gcmode=archive",
                "--cache=256", "--state.scheme=hash", "--db.engine=pebble", "--cache.preimages=true",
                "--bor.withoutheimdall=true", "--bor.devfakeauthor=false", "--mine=false", "--maxpeers=0", "--port=0",
                "--nodiscover=true", "--v5disc=false", "--http=false", "--ws=false", "--pipeline.enable-import-src=false",
                "--parallelevm.enable=false", "--witness.parallelstatelessimport=false", "--relay.enable-preconfs=false",
                "--accept-preconf-tx=false", "--log.json=true"]
    require(args == expected, "Server command changed")
    ready = load(directory / "readiness.json", deadline=deadline)
    require(type(ready.get("head")) is int and ready["head"] == 0 and
            ready.get("genesis_hash") == admitted["fixture"]["identity"]["genesis_hash"], "Fresh genesis readiness mismatch")
    require(canonical_digest(ready["node_info"]["protocols"]["eth"]["config"]) ==
            canonical_digest(admitted["fixture"]["native_config"]), "Effective native chain config mismatch")
    require(type(ready["node_info"]["protocols"]["eth"].get("network")) is int and
            ready["node_info"]["protocols"]["eth"]["network"] == 15001 and isinstance(ready.get("validators"), list) and
            len(ready["validators"]) == 1 and ready["validators"][0]["address"].lower() == admitted["expected"]["validator"],
            "Native private network or genesis validator mismatch")
    cleanup = load(directory / "server-cleanup.json", deadline=deadline)
    require(cleanup.get("wait_completed") is True and cleanup.get("unexpected_exit") is False and
            cleanup.get("forced_kill") is False and type(cleanup.get("exit_code")) is int and cleanup["exit_code"] == 0 and
            cleanup.get("exit") == "" and type(cleanup.get("pid")) is int and cleanup["pid"] > 0,
            "Native server wait is not confirmed")
    _, full = read_artifact(admitted["fixture"]["full_chain"], 16 * 1024 * 1024, deadline=deadline)
    require(read_bytes(directory / "full-chain.rlp", 16 * 1024 * 1024, deadline) == full,
            "Imported full native chain differs from prepared bytes")
    observed = load(directory / "observed.json", 16 * 1024 * 1024, deadline)
    states = [load(directory / ("state-%d.json" % n), 16 * 1024 * 1024, deadline) for n in (1, 2)]
    require(all(not row.get("next") for row in states), "Partial state dump")
    identities = [ready["genesis_hash"], *[row["hash"] for row in observed["blocks"]]]
    compare_native(admitted["expected"], observed, states, identities, admitted["fixture"]["prepared_blocks"],
                   admitted["fixture"]["identity"]["config_sha256"])


def measured(request_path, tool_path, tool_sha, output, started):
    admitted = admit(request_path, tool_path, tool_sha, started)
    deadline = admitted["deadline"]
    protected = [Path(admitted["request"]["build_manifest"]["path"]).parent, tool_path.parent,
                 Path(admitted["request"]["fixture"]["path"]).parent]
    require(not any(output.is_relative_to(path) for path in protected), "Output cannot modify admitted storage")
    output = new_output(output)
    runner = Native(output, deadline)
    origins, sources = source_snapshot(output, ("bor_run.py", "bor_inputs.py", "bor.py", "artifacts.py", "run.py", "native.py", "owned.py"), deadline)
    proof = {"schema_version": 1, "kind": "bor-local-native-import", "scope": "partial-native-seam",
             "run_id": uuid.uuid4().hex, "native_owner_nonce": runner.nonce, "result": "FAIL", "suite": "quick", "suite_result": "NOT_RUN",
             "profile_results": {profile: "NOT_RUN" for profile in PROFILES}, "case_results": [],
             "remaining_cases": "All public profile/case cells are NOT_RUN", "harness_sources": sources,
             "request": admitted["request_ref"], "build_manifest": admitted["request"]["build_manifest"],
             "fixture": admitted["request"]["fixture"], "fixture_identity": admitted["fixture"]["identity"],
             "profile_association": "polygon-pos-mainnet", "observer_tool": admitted["tool_ref"], "external_observer_tool_sha256": tool_sha,
             "deadline_seconds": admitted["request"]["deadline_seconds"], "cleanup_complete": False}
    runtime = output / "state/runtime"
    try:
        inputs, work = output / "inputs", output / "state"
        inputs.mkdir(); work.mkdir(); runtime.mkdir()
        copy_artifact(admitted["binary"], inputs / "bor", admitted["manifest"]["builds"]["bor"]["binary"], 256 * 1024 * 1024, deadline)
        copy_artifact(Path(admitted["tool"]["binary"]["path"]), inputs / "helper", admitted["tool"]["binary"], 256 * 1024 * 1024, deadline)
        for name in ("bor", "helper"):
            (inputs / name).chmod(0o500)
        for name in ("genesis", "blocks"):
            filename = "genesis.json" if name == "genesis" else "blocks.rlp"
            copy_artifact(admitted["paths"][name], inputs / filename, admitted["fixture"][name], 16 * 1024 * 1024, deadline)
        runner.command(admitted["image"], ["/inputs/helper", "observe", "/inputs/genesis.json", "/inputs/blocks.rlp", "/work/runtime"],
                       [(inputs, "/inputs", True), (work, "/work", False)], cap=240, memory="2g")
        proof["observations"] = {name: reference(runtime / name, 16 * 1024 * 1024, deadline) for name in RUNTIME_FILES}
        validate_runtime(runtime, admitted, deadline)
        sources_unchanged(origins, deadline)
        revalidate_authority(admitted)
        check_deadline(deadline)
        proof["result"] = "PASS"
    except BaseException as error:
        proof["error"] = {"type": type(error).__name__, "message": str(error)}
        raise
    finally:
        if "observations" not in proof:
            proof["observations"] = {}
            capture_deadline = time.monotonic() + 10
            try:
                for name in RUNTIME_FILES:
                    if (runtime / name).is_file():
                        proof["observations"][name] = reference(runtime / name, 16 * 1024 * 1024, capture_deadline)
            except Exception as error:
                proof["failure_capture_error"] = str(error)
        proof.update(commands=runner.commands, elapsed_seconds=time.monotonic() - started, cleanup_complete=runner.cleanup_complete)
        with (output / "proof.json").open("x") as stream:
            stream.write(json.dumps(proof, sort_keys=True, indent=2) + "\n")
    return output / "proof.json"


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("check", "run"))
    parser.add_argument("request", type=Path)
    parser.add_argument("--observer-tool", type=Path, required=True)
    parser.add_argument("--observer-tool-sha256", required=True)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    started = time.monotonic()
    def interrupt(*_):
        raise InterruptedError("Measured run interrupted")
    for number in (signal.SIGINT, signal.SIGTERM):
        signal.signal(number, interrupt)
    try:
        if args.mode == "check":
            admit(args.request, args.observer_tool, args.observer_tool_sha256, started)
            print(json.dumps({"valid": True, "native_dispatched": False}))
        else:
            require(args.output is not None, "Output required")
            print(measured(args.request, args.observer_tool, args.observer_tool_sha256, args.output, started))
    except (Exception, KeyboardInterrupt) as error:
        print(json.dumps({"valid": False, "error": str(error)}))
        raise SystemExit(2)
