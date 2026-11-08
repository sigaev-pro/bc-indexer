#!/usr/bin/env python3
"""Offline native import slice. See README.md for its partial scope."""

import argparse
import json
from pathlib import Path
import sys
import signal
import time
import uuid
import subprocess

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import admit, reference, require, load, source_snapshot, sources_unchanged
from compare import compare
from native import Native, copy_verified, new_output


PROFILES = ("ethereum-mainnet", "ethereum-sepolia", "ethereum-hoodi", "bsc-mainnet", "bsc-testnet",
            "polygon-pos-mainnet", "polygon-pos-amoy")


def measured(request_path, output, start):
    admitted = admit(request_path, started=start)
    deadline = admitted["deadline"]
    require(time.monotonic() + 35 < deadline, "Insufficient run time after admission")
    require(output is not None, "A new output directory is required")
    require(not output.is_relative_to(admitted["build_path"].parent) and
            not output.is_relative_to(Path(admitted["request"]["fixture"]["path"]).parent),
            "Output cannot be inside admitted build or fixture storage")
    output = new_output(output)
    runner = Native(output, deadline)
    origins, source_refs = source_snapshot(output, ("run.py", "artifacts.py", "compare.py", "native.py", "owned.py"), deadline)
    proof = {"schema_version": 1, "kind": "local-native-import", "scope": "partial-native-seam",
             "run_id": uuid.uuid4().hex, "suite": "quick", "suite_result": "NOT_RUN", "result": "FAIL",
             "profile_results": {profile: "NOT_RUN" for profile in PROFILES},
             "case_results": [], "remaining_cases": "All public profile/case cells are NOT_RUN",
             "profile_association": admitted["request"]["profile_association"],
             "fixture_identity": admitted["fixture"]["identity"], "request": reference(request_path, deadline=deadline),
             "build_manifest": admitted["request"]["build_manifest"], "fixture": admitted["request"]["fixture"],
             "deadline_seconds": admitted["request"]["deadline_seconds"], "scenarios": [], "cleanup_complete": False}
    proof["harness_sources"] = source_refs
    proof["native_owner_nonce"] = runner.nonce
    try:
        inputs = output / "inputs"
        work = output / "state"
        inputs.mkdir()
        work.mkdir()
        copy_verified(admitted["binary"], inputs / "geth", admitted["manifest"]["builds"]["geth"]["binary"], deadline)
        (inputs / "geth").chmod(0o500)
        for key, path in admitted["paths"].items():
            copy_verified(path, inputs / key, admitted["fixture"][key], deadline)
        proof["inputs"] = {path.name: reference(path, deadline=deadline) for path in inputs.iterdir()}
        mounts = [(inputs, "/inputs", True), (work, "/work", False)]
        flags = ["/inputs/geth", "--log.format=json", "--datadir", "/work/data", "--networkid", "31337", "--cache", "64",
                 "--cache.preimages", "--state.scheme", "hash", "--gcmode", "archive"]
        runner.command(admitted["image"], flags + ["init", "/inputs/genesis"], mounts)
        runner.command(admitted["image"], flags + ["import", "/inputs/blocks"], mounts)
        dumps = []
        header_logs = []
        for number in (0, 1, 2):
            state = runner.command(admitted["image"], flags + ["dump", "--iterative=false", "--limit", "0", str(number)], mounts)
            header_logs.append(state.with_suffix(".stderr"))
            if number:
                dumps.append(state)
        admitted["header_observations"] = header_logs
        runner.command(admitted["image"], flags + ["export", "/work/export.rlp"], mounts)
        require(time.monotonic() < deadline, "Run deadline expired before comparison")
        proof["observations"] = {"states": [reference(path, deadline=deadline) for path in dumps],
                                 "chain": reference(work / "export.rlp", deadline=deadline),
                                 "headers": [reference(path, 8 * 1024 * 1024, deadline) for path in header_logs]}
        proof["scenarios"] = compare(admitted, dumps, work / "export.rlp")
        require(time.monotonic() < deadline, "Run deadline expired during comparison")
        sources_unchanged(origins, deadline)
        proof["result"] = "PASS"
    except (ValueError, OSError, KeyError, TypeError, InterruptedError, subprocess.SubprocessError) as error:
        proof["error"] = str(error)
    finally:
        proof["commands"] = runner.commands
        proof["cleanup_complete"] = runner.cleanup_complete
        proof["elapsed_seconds"] = time.monotonic() - start
        if not proof["cleanup_complete"] or proof["elapsed_seconds"] > proof["deadline_seconds"]:
            proof["result"] = "FAIL"
        (output / "proof.json").write_text(json.dumps(proof, sort_keys=True, indent=2) + "\n")
    return proof


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("check", "run"))
    parser.add_argument("request", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    def interrupt(*_):
        raise InterruptedError("Run interrupted")
    for number in (signal.SIGINT, signal.SIGTERM):
        signal.signal(number, interrupt)
    started = time.monotonic()
    try:
        if args.command == "run":
            proof = measured(args.request.absolute(), args.output, started)
            print(json.dumps({"result": proof["result"], "proof": str(args.output / "proof.json"),
                              "suite_result": proof["suite_result"], "cleanup_complete": proof["cleanup_complete"]}))
            return 0 if proof["result"] == "PASS" else 2
        admit(args.request.absolute(), started=started)
        print(json.dumps({"valid": True, "native_dispatched": False}))
        return 0
    except (ValueError, OSError, KeyError, TypeError) as error:
        print(json.dumps({"valid": False, "error": str(error)}))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
