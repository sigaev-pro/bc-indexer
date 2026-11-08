#!/usr/bin/env python3
"""Run the bounded private BSC import slice using an externally pinned observer."""

import argparse
import json
from pathlib import Path
import signal
import subprocess
import sys
import time
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import (artifact, copy_artifact, load, reference, require,
                       source_snapshot, sources_unchanged, check_deadline, revalidate_authority)
from bsc import compare_native
from bsc_inputs import admit
from compare import compare_headers
from native import Native, new_output
from run import PROFILES


def measured(request_path, tool_path, tool_sha, output, started):
    admitted = admit(request_path, tool_path, tool_sha, started)
    deadline = admitted["deadline"]
    protected = [Path(admitted["request"]["build_manifest"]["path"]).parent, tool_path.parent,
                 Path(admitted["request"]["fixture"]["path"]).parent]
    require(not any(output.is_relative_to(path) for path in protected), "Output cannot modify admitted storage")
    output = new_output(output)
    runner = Native(output, deadline)
    origins, sources = source_snapshot(output, ("bsc_run.py", "bsc_inputs.py", "bsc.py", "artifacts.py", "compare.py", "run.py", "native.py", "owned.py"), deadline)
    proof = {"schema_version": 1, "kind": "bsc-local-native-import", "scope": "partial-native-seam",
             "run_id": uuid.uuid4().hex, "native_owner_nonce": runner.nonce, "result": "FAIL", "suite": "quick", "suite_result": "NOT_RUN",
             "profile_results": {profile: "NOT_RUN" for profile in PROFILES}, "case_results": [],
             "remaining_cases": "All public profile/case cells are NOT_RUN", "harness_sources": sources,
             "request": admitted["request_ref"], "build_manifest": admitted["request"]["build_manifest"],
             "fixture": admitted["request"]["fixture"], "fixture_identity": admitted["fixture"]["identity"],
             "profile_association": "bsc-mainnet", "observer_tool": admitted["tool_ref"], "external_observer_tool_sha256": tool_sha,
             "deadline_seconds": admitted["request"]["deadline_seconds"], "cleanup_complete": False}
    try:
        inputs, work, observations = output / "inputs", output / "state", output / "observations"
        for path in (inputs, work, observations):
            path.mkdir()
        copy_artifact(admitted["binary"], inputs / "bsc", admitted["manifest"]["builds"]["bsc"]["binary"], 256 * 1024 * 1024, deadline)
        copy_artifact(Path(admitted["tool"]["binary"]["path"]), inputs / "observer", admitted["tool"]["binary"], 256 * 1024 * 1024, deadline)
        for name in ("bsc", "observer"):
            (inputs / name).chmod(0o500)
        for name, path in admitted["paths"].items():
            copy_artifact(path, inputs / name, admitted["fixture"][name], 16 * 1024 * 1024, deadline)
        proof["inputs"] = {path.name: reference(path, deadline=deadline) for path in inputs.iterdir()}
        mounts = [(inputs, "/inputs", True), (work, "/work", False)]
        flags = ["/inputs/bsc", "--log.format=json", "--datadir", "/work/data", "--networkid", "714", "--cache", "64",
                 "--cache.preimages", "--state.scheme", "hash", "--gcmode", "archive"]
        runner.command(admitted["image"], flags + ["init", "/inputs/genesis"], mounts)
        runner.command(admitted["image"], flags + ["import", "/inputs/blocks"], mounts)
        dumps, headers = [], []
        for number in (0, 1, 2):
            path = runner.command(admitted["image"], flags + ["dump", "--iterative=false", "--limit", "0", str(number)], mounts)
            headers.append(path.with_suffix(".stderr"))
            if number:
                dumps.append(path)
        runner.command(admitted["image"], flags + ["export", "/work/export.rlp"], mounts)
        runner.command(admitted["tool"]["compiler"], ["/inputs/observer", "observe", "/work/data", "/observations"],
                       [(inputs, "/inputs", True), (work, "/work", True), (observations, "/observations", False)], memory="1g")
        admitted["header_observations"] = headers
        compare_headers(admitted)
        export = reference(work / "export.rlp", 16 * 1024 * 1024, deadline)
        require(all(export[key] == admitted["fixture"]["full_chain"][key] for key in ("sha256", "size_bytes")), "Full native chain export mismatch")
        config = load(observations / "native-config.json", deadline=deadline)
        require(config == admitted["fixture"]["native_config"], "Imported native configuration mismatch")
        identities = [admitted["fixture"]["identity"]["genesis_hash"], *[row["hash"] for row in admitted["fixture"]["prepared_blocks"]]]
        proof["comparison"] = compare_native(admitted["expected"], load(observations / "observation.json", 16 * 1024 * 1024, deadline),
                                             [load(path, 16 * 1024 * 1024, deadline) for path in dumps], identities, admitted["fixture"]["prepared_blocks"])
        proof["observations"] = {"states": [reference(path, 16 * 1024 * 1024, deadline) for path in dumps],
                                 "headers": [reference(path, 8 * 1024 * 1024, deadline) for path in headers], "chain": export,
                                 "database": reference(observations / "observation.json", 16 * 1024 * 1024, deadline),
                                 "native_config": reference(observations / "native-config.json", deadline=deadline)}
        check_deadline(deadline)
        sources_unchanged(origins, deadline)
        revalidate_authority(admitted)
        proof["result"] = "PASS"
    except (ValueError, OSError, KeyError, TypeError, InterruptedError, subprocess.SubprocessError) as error:
        proof["error"] = str(error)
    finally:
        proof.update(commands=runner.commands, cleanup_complete=runner.cleanup_complete, elapsed_seconds=time.monotonic() - started)
        if not proof["cleanup_complete"] or proof["elapsed_seconds"] > proof["deadline_seconds"]:
            proof["result"] = "FAIL"
        with (output / "proof.json").open("x") as stream:
            stream.write(json.dumps(proof, sort_keys=True, indent=2) + "\n")
    return proof


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("check", "run"))
    parser.add_argument("request", type=Path)
    parser.add_argument("--observer-tool", type=Path, required=True)
    parser.add_argument("--observer-tool-sha256", required=True)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    def interrupt(*_):
        raise InterruptedError("BSC run interrupted")
    for number in (signal.SIGINT, signal.SIGTERM):
        signal.signal(number, interrupt)
    started = time.monotonic()
    try:
        if args.command == "check":
            admit(args.request, args.observer_tool, args.observer_tool_sha256, started)
            print(json.dumps({"valid": True, "native_dispatched": False}))
            return 0
        require(args.output is not None, "Run requires a new output directory")
        proof = measured(args.request, args.observer_tool, args.observer_tool_sha256, args.output, started)
        print(json.dumps({"result": proof["result"], "proof": str(args.output / "proof.json")}))
        return 0 if proof["result"] == "PASS" else 2
    except (ValueError, OSError, KeyError, TypeError) as error:
        print(json.dumps({"valid": False, "native_dispatched": False, "error": str(error)}))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
