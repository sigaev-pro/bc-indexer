#!/usr/bin/env python3
"""Explicit real BSC controls. Portable CI never launches these resources."""

import argparse
import copy
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import (Rejected, artifact, copy_artifact, json_artifact, load, read_artifact,
                       reference, require, source_snapshot, sources_unchanged)
from bsc import compare_native
from bsc_inputs import admit
from bsc_run import observer_mounts
from native import Native, new_output


def save(path, value):
    with path.open("x") as stream:
        stream.write(json.dumps(value, sort_keys=True, indent=2) + "\n")


def first_rlp_item(data):
    require(data and data[0] >= 0xC0, "Expected a native RLP list")
    if data[0] <= 0xF7:
        offset, size = 1, data[0] - 0xC0
    else:
        width = data[0] - 0xF7
        require(len(data) > width and data[1] != 0, "Invalid RLP length")
        offset, size = 1 + width, int.from_bytes(data[1:1 + width], "big")
        require(size > 55, "Noncanonical RLP length")
    require(offset + size <= len(data), "Incomplete first RLP item")
    return data[:offset + size]


def resource_controls(image, output, summary, deadline):
    require(time.monotonic() + 100 < deadline, "Insufficient control cleanup budget")
    target = output / "container-timeout"
    target.mkdir()
    runner = Native(target, time.monotonic() + 45)
    try:
        runner.command(image, ["/bin/sleep", "30"], [], cap=36)
        raise AssertionError("Expected owned timeout")
    except Rejected:
        require(len(runner.commands) == 1, "Timeout did not dispatch one command")
        result = load(runner.commands[0]["result"]["path"])
        require(result["reason"] == "DEADLINE" and result["cleanup_complete"], "Timeout cleanup failed")
    summary["controls"].append({"name": "container-timeout", "control_result": "PASS",
                                "native_owner_nonce": runner.nonce, "commands": runner.commands})
    for number, group in ((signal.SIGTERM, False), (signal.SIGKILL, False), (signal.SIGTERM, True), (signal.SIGKILL, True)):
        name = ("parent-group-stop-" if group else "parent-stop-") + str(number)
        target = output / name
        target.mkdir()
        child = subprocess.Popen([sys.executable, "-I", str(Path(__file__).with_name("exercise.py")), "--owned-sleep", str(target), image],
                                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        request_path, result_path = target / "logs/000.request.json", target / "logs/000.result.json"
        try:
            until = time.monotonic() + 10
            while time.monotonic() < until:
                if request_path.exists():
                    request = load(request_path)
                    state = subprocess.run([shutil.which("docker"), "inspect", "--format", "{{.State.Running}}", request["container"]],
                                           capture_output=True, timeout=3)
                    if state.returncode == 0 and state.stdout.strip() == b"true":
                        break
                time.sleep(0.05)
            else:
                raise Rejected("Owned real container did not start")
            os.killpg(child.pid, number) if group else child.send_signal(number)
            child.wait(timeout=5)
            until = time.monotonic() + 35
            while not result_path.exists() and time.monotonic() < until:
                time.sleep(0.05)
            result = load(result_path)
            require(result["reason"] == "PARENT_EXIT" and result["cleanup_complete"] and result["process_group_absent"], "Parent-loss cleanup failed")
            summary["controls"].append({"name": name, "control_result": "PASS", "request": reference(request_path),
                                        "result": reference(result_path), "container": request["container"],
                                        "termination_scope": "process-group" if group else "process", "parent_exit_code": child.returncode})
        finally:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait(timeout=5)
            # The separate guardian owns cleanup after any parent exit.
            if request_path.exists() and not result_path.exists():
                until = time.monotonic() + 35
                while not result_path.exists() and time.monotonic() < until:
                    time.sleep(0.05)
                require(result_path.exists() and load(result_path)["cleanup_complete"], "Owned cleanup is unresolved")


def incomplete_hot_store(admitted, output, summary, deadline):
    target = new_output(output / "incomplete-hot-store")
    inputs, work, observed = target / "inputs", target / "state", target / "observations"
    for path in (inputs, work, observed):
        path.mkdir()
    copy_artifact(admitted["binary"], inputs / "bsc", admitted["manifest"]["builds"]["bsc"]["binary"], 256 * 1024 * 1024, deadline)
    copy_artifact(Path(admitted["tool"]["binary"]["path"]), inputs / "observer", admitted["tool"]["binary"], 256 * 1024 * 1024, deadline)
    copy_artifact(admitted["paths"]["genesis"], inputs / "genesis", admitted["fixture"]["genesis"], 16 * 1024 * 1024, deadline)
    for name in ("bsc", "observer"):
        (inputs / name).chmod(0o500)
    runner = Native(target, min(deadline, time.monotonic() + 180))
    try:
        runner.command(admitted["image"], ["/inputs/bsc", "--log.format=json", "--datadir", "/work/data", "--networkid", "714",
                                         "--state.scheme", "hash", "init", "/inputs/genesis"], [(inputs, "/inputs", True), (work, "/work", False)])
        mounts, locks = observer_mounts(inputs, work, observed, deadline)
        runner.command(admitted["tool"]["compiler"], ["/inputs/observer", "observe", "/work/data", "/observations"], mounts)
        raise AssertionError("Incomplete hot store was accepted")
    except Rejected:
        require(len(runner.commands) == 2 and runner.cleanup_complete, "Incomplete hot-store control failed before observer")
        result = load(runner.commands[-1]["result"]["path"])
        _, stderr = read_artifact(runner.commands[-1]["stderr"], 8 * 1024 * 1024, deadline=deadline)
        require(result["reason"] == "NATIVE_EXIT" and b"Wrong imported head" in stderr, "Incomplete hot store failed for another reason")
        for lock in locks:
            artifact(lock, 0, deadline=deadline)
    summary["controls"].append({"name": "incomplete-hot-store", "control_result": "PASS", "native_owner_nonce": runner.nonce,
                                "commands": runner.commands, "native_result": "FAIL", "observer_locks": locks})


def exercise(request_path, tool_path, tool_sha, output):
    started = time.monotonic()
    deadline = started + 900
    admitted = admit(request_path, tool_path, tool_sha, started, deadline)
    output = new_output(output)
    origins, sources = source_snapshot(output, ("bsc_exercise.py", "bsc_run.py", "bsc_inputs.py", "bsc.py", "artifacts.py",
                                                "compare.py", "run.py", "native.py", "owned.py", "exercise.py"), deadline)
    summary = {"schema_version": 1, "kind": "explicit-bsc-native-controls", "result": "FAIL", "quick_suite": "NOT_RUN",
               "source_request": admitted["request_ref"], "observer_tool": admitted["tool_ref"], "external_observer_tool_sha256": tool_sha,
               "harness_sources": sources, "controls": []}
    positive = None
    try:
        names = ("positive", "native-corruption", "incomplete-chain", "wrong-oracle", "wrong-genesis-hash", "wrong-block-hash",
                 "genesis-tamper", "rlp-tamper", "config-identity", "binary-tamper", "observer-pin-tamper")
        for name in names:
            require(time.monotonic() + 340 < deadline, "Insufficient remaining measured/cleanup budget")
            inputs = output / (name + "-inputs")
            inputs.mkdir()
            request, scenario = copy.deepcopy(admitted["request"]), copy.deepcopy(admitted["fixture"])
            selected_tool, selected_pin = tool_path, tool_sha
            if name in ("native-corruption", "incomplete-chain", "rlp-tamper"):
                _, data = read_artifact(scenario["blocks"], 16 * 1024 * 1024, deadline=deadline)
                body = first_rlp_item(data) if name == "incomplete-chain" else data[:-1]
                require(len(body) < len(data), "Negative input did not remove data")
                (inputs / "blocks.rlp").write_bytes(body)
                scenario["blocks"] = reference(inputs / "blocks.rlp") if name != "rlp-tamper" else {**scenario["blocks"], "path": str(inputs / "blocks.rlp")}
            elif name == "wrong-oracle":
                expected = copy.deepcopy(admitted["expected"])
                expected["accounts"][expected["user_transaction"]["to"]]["balance"] = "999"
                save(inputs / "expected.json", expected)
                scenario["expected"] = reference(inputs / "expected.json")
            elif name in ("wrong-genesis-hash", "config-identity"):
                scenario["identity"]["genesis_hash" if name == "wrong-genesis-hash" else "config_sha256"] = ("0x" if name == "wrong-genesis-hash" else "") + "0" * 64
            elif name == "wrong-block-hash":
                scenario["prepared_blocks"][0]["hash"] = "0x" + "0" * 64
            elif name == "genesis-tamper":
                _, data = read_artifact(scenario["genesis"], 16 * 1024 * 1024, deadline=deadline)
                (inputs / "genesis.json").write_bytes(data + b" ")
                scenario["genesis"]["path"] = str(inputs / "genesis.json")
            elif name in ("binary-tamper", "observer-pin-tamper"):
                tool = copy.deepcopy(admitted["tool"])
                if name == "binary-tamper":
                    manifest = copy.deepcopy(admitted["manifest"])
                    manifest["builds"]["bsc"]["binary"]["path"] = "invalid-bsc"
                    (inputs / "invalid-bsc").write_bytes(b"invalid native binary")
                    save(inputs / "manifest.json", manifest)
                    request["build_manifest"] = reference(inputs / "manifest.json")
                    tool["build_manifest"] = request["build_manifest"]
                else:
                    (inputs / "fake-observer").write_bytes(b"fake observer returning expected values")
                    tool["binary"] = reference(inputs / "fake-observer")
                save(inputs / "tool.json", tool)
                selected_tool = inputs / "tool.json"
                if name == "binary-tamper":
                    # Explicit invalid authority for this negative only, never an accepted build/tool.
                    selected_pin = reference(selected_tool)["sha256"]
            save(inputs / "fixture.json", scenario)
            request["fixture"] = reference(inputs / "fixture.json")
            save(inputs / "request.json", request)
            target = output / name
            result = subprocess.run([sys.executable, "-I", str(Path(__file__).with_name("bsc_run.py")), "run", str(inputs / "request.json"),
                                     "--observer-tool", str(selected_tool), "--observer-tool-sha256", selected_pin, "--output", str(target)],
                                    capture_output=True, text=True, timeout=330)
            (inputs / "stdout.txt").write_text(result.stdout)
            (inputs / "stderr.txt").write_text(result.stderr)
            row = {"name": name, "exit_code": result.returncode, "request": reference(inputs / "request.json"),
                   "stdout": reference(inputs / "stdout.txt"), "stderr": reference(inputs / "stderr.txt")}
            if name in ("genesis-tamper", "rlp-tamper", "config-identity", "binary-tamper", "observer-pin-tamper"):
                reason = "Private config identity mismatch" if name == "config-identity" else (
                    "external trusted pin" if name == "observer-pin-tamper" else "Artifact bytes changed")
                require(result.returncode == 2 and not target.exists() and reason in result.stdout, "Admission control failed for another reason")
                row["native_dispatched"] = False
                if name == "binary-tamper":
                    row["authority_kind"] = "explicit-invalid-build-negative"
            else:
                proof = load(target / "proof.json")
                row["proof"] = reference(target / "proof.json")
                require(proof["cleanup_complete"] and proof["suite_result"] == "NOT_RUN", "Measured control cleanup/scope failed")
                if name == "positive":
                    require(result.returncode == 0 and proof["result"] == "PASS" and len(proof["commands"]) == 7, "Positive BSC control failed")
                    positive = proof
                else:
                    require(result.returncode == 2 and proof["result"] == "FAIL", "Negative native control did not retain FAIL")
                    if name in ("native-corruption", "incomplete-chain"):
                        require(load(proof["commands"][-1]["result"]["path"])["reason"] == "NATIVE_EXIT", "Incomplete/corrupt chain did not fail native validation")
                    else:
                        require(len(proof["commands"]) == 7 and ("Account " if name == "wrong-oracle" else "Native header identity") in proof.get("error", ""), "Comparison control failed for another reason")
            row["control_result"] = "PASS"
            summary["controls"].append(row)
        require(positive is not None, "Missing positive native evidence")
        _, observation = json_artifact(positive["observations"]["database"], 16 * 1024 * 1024, deadline=deadline)
        states = [json_artifact(ref, 16 * 1024 * 1024, deadline=deadline)[1] for ref in positive["observations"]["states"]]
        identities = [admitted["fixture"]["identity"]["genesis_hash"], *[row["hash"] for row in admitted["fixture"]["prepared_blocks"]]]
        compare_native(admitted["expected"], observation, states, identities, admitted["fixture"]["prepared_blocks"])
        for name in ("incomplete-state", "missing-receipt"):
            changed_states, changed_observation = copy.deepcopy(states), copy.deepcopy(observation)
            if name == "incomplete-state":
                address = next(key for key in changed_states[0]["accounts"] if key.lower() == admitted["expected"]["user_transaction"]["to"])
                del changed_states[0]["accounts"][address]
            else:
                changed_observation["blocks"][0]["receipts"].pop()
            save(output / (name + ".json"), {"states": changed_states, "database": changed_observation})
            try:
                compare_native(admitted["expected"], changed_observation, changed_states, identities, admitted["fixture"]["prepared_blocks"])
                raise AssertionError("Incomplete observation accepted")
            except Rejected as error:
                require(("Expected account omitted" if name == "incomplete-state" else "count mismatch") in str(error), "Incomplete evidence failed for another reason")
                summary["controls"].append({"name": name, "control_result": "PASS", "scope": "retained-native-artifact-comparison",
                                            "derived_input": reference(output / (name + ".json")), "error": str(error)})
        incomplete_hot_store(admitted, output, summary, deadline)
        resource_controls(admitted["image"], output, summary, deadline)
        sources_unchanged(origins, deadline)
        require(time.monotonic() < deadline, "Control deadline expired")
        summary["result"] = "PASS"
    finally:
        summary["elapsed_seconds"] = time.monotonic() - started
        save(output / "controls.json", summary)
    return output / "controls.json"


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("request", type=Path)
    parser.add_argument("--observer-tool", type=Path, required=True)
    parser.add_argument("--observer-tool-sha256", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    print(exercise(args.request, args.observer_tool, args.observer_tool_sha256, args.output))
