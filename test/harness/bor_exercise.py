#!/usr/bin/env python3
"""Explicit private Bor controls. Portable CI never starts native resources."""

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
from artifacts import (Rejected, copy_artifact, decode_json, json_artifact, load, read_artifact,
                       read_bytes, reference, require, source_snapshot, sources_unchanged)
from bor import compare_native
from bor_inputs import admit, revalidate_authority
from bsc_exercise import first_rlp_item
from native import Native, new_output


def save(path, value):
    with path.open("x") as stream:
        stream.write(json.dumps(value, sort_keys=True, indent=2) + "\n")


def rpc_rows(ref, deadline):
    _, raw = read_artifact(ref, 16 * 1024 * 1024, deadline=deadline)
    lines = raw.splitlines()
    require(0 < len(lines) <= 64, "Unexpected IPC message count")
    rows = [decode_json(line, deadline) for line in lines]
    require(all(isinstance(row, dict) and row.get("jsonrpc") == "2.0" and
                type(row.get("id")) is int for row in rows), "Invalid IPC message identity")
    require(len({row["id"] for row in rows}) == len(rows), "Duplicate IPC message identity")
    return rows


def validate_native_rejection(name, proof, deadline=None):
    """Require the actual IPC failure stage, response identity and clean server exit."""
    require(name in ("native-corruption", "incomplete-chain"), "Unknown native rejection")
    require(proof["result"] == "FAIL" and len(proof["commands"]) == 1, "Native rejection occurred at the wrong stage")
    command = proof["commands"][0]
    _, request = json_artifact(command["request"], 1024 * 1024, deadline=deadline)
    _, result = json_artifact(command["result"], 1024 * 1024, deadline=deadline)
    _, stderr = read_artifact(command["stderr"], 8 * 1024 * 1024, deadline=deadline)
    require(result["argv"] == request["argv"] and request["argv"][-5:] ==
            ["/inputs/helper", "observe", "/inputs/genesis.json", "/inputs/blocks.rlp", "/work/runtime"] and
            type(result["exit_code"]) is int and result["exit_code"] == 2 and result["reason"] == "NATIVE_EXIT" and
            result["cleanup_complete"] is True and result["process_group_absent"] is True,
            "Native rejection command or cleanup mismatch")
    observations = proof["observations"]
    requests, responses = (rpc_rows(observations[key], deadline) for key in ("rpc.requests", "rpc.responses"))
    methods = ["eth_blockNumber", "debug_getRawBlock", "admin_nodeInfo", "eth_call", "admin_importChain"]
    if name == "incomplete-chain":
        methods.append("eth_blockNumber")
    require([row.get("method") for row in requests] == methods and
            [row["id"] for row in responses] == [row["id"] for row in requests] and
            requests[4].get("params") == ["/inputs/blocks.rlp"], "Native failure RPC stage or correlation mismatch")
    require(all("result" in row and "error" not in row for row in responses[:4]) and
            responses[0]["result"] == "0x0", "Native readiness RPC failed")
    _, readiness = json_artifact(observations["readiness.json"], 1024 * 1024, deadline=deadline)
    _, cleanup = json_artifact(observations["server-cleanup.json"], 1024 * 1024, deadline=deadline)
    require(type(readiness.get("head")) is int and readiness["head"] == 0 and
            cleanup.get("wait_completed") is True and cleanup.get("unexpected_exit") is False and
            cleanup.get("forced_kill") is False and type(cleanup.get("exit_code")) is int and
            cleanup["exit_code"] == 0 and cleanup.get("exit") == "", "Native readiness or server cleanup failed")
    if name == "native-corruption":
        error = responses[4].get("error")
        require("result" not in responses[4] and isinstance(error, dict) and type(error.get("code")) is int and
                "unexpected EOF" in str(error.get("message")) and b"native IPC import:" in stderr and
                b"unexpected EOF" in stderr, "Corruption failed outside native IPC decoding")
    else:
        require(responses[4].get("result") is True and "error" not in responses[4] and
                responses[5].get("result") == "0x1" and "error" not in responses[5] and
                b"native import terminal head is not two" in stderr, "Prefix failed outside terminal-head validation")


def owned_server(target, image, deadline, cap):
    runner = Native(target, deadline)
    try:
        runner.command(image, load(target / "server.argv.json", 1024 * 1024, deadline),
                       [(target / "inputs", "/inputs", True), (target / "state", "/work", False)], cap=cap, memory="2g")
    except Rejected:
        return 2
    return 0


def server_stop_control(admitted, positive, output, summary, deadline, number, group):
    dispatch = time.monotonic()
    require(dispatch + 100 <= deadline, "Insufficient server execution/cleanup budget")
    operation_deadline = min(deadline, dispatch + 90)
    name = "server-timeout" if number is None else ("server-parent-group-stop-" if group else "server-parent-stop-") + str(number)
    target = output / name
    target.mkdir()
    (target / "inputs").mkdir()
    (target / "state/runtime").mkdir(parents=True)
    copy_artifact(admitted["binary"], target / "inputs/bor", admitted["manifest"]["builds"]["bor"]["binary"], 256 * 1024 * 1024, deadline)
    (target / "inputs/bor").chmod(0o500)
    copy_artifact(admitted["paths"]["genesis"], target / "inputs/genesis.json", admitted["fixture"]["genesis"], 16 * 1024 * 1024, deadline)
    _, argv = json_artifact(positive["observations"]["server.argv.json"], 1024 * 1024, deadline=deadline)
    save(target / "server.argv.json", argv)
    require(time.monotonic() + 80 <= operation_deadline, "Insufficient server dispatch budget")
    child = subprocess.Popen([sys.executable, "-I", str(Path(__file__)), "--owned-server", str(target), admitted["image"],
                              str(operation_deadline), "65" if number is None else "85"],
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
    request_path, result_path = target / "logs/000.request.json", target / "logs/000.result.json"
    ready = None
    try:
        until = min(operation_deadline - 40, time.monotonic() + 25)
        while time.monotonic() < until:
            require(child.poll() is None, "Owned Bor parent exited before readiness")
            stderr_path = target / "logs/000.stderr"
            if request_path.exists() and stderr_path.exists():
                raw = read_bytes(stderr_path, 8 * 1024 * 1024, deadline)
                for line in raw.splitlines():
                    try:
                        event = decode_json(line, deadline)
                    except (ValueError, Rejected):
                        continue
                    if event.get("msg") == "IPC endpoint opened" and event.get("url") == "/work/runtime/bor.ipc":
                        ready = event
                if ready is not None:
                    request = load(request_path, 1024 * 1024, deadline)
                    state = subprocess.run([shutil.which("docker"), "inspect", "--format", "{{.State.Running}}", request["container"]],
                                           capture_output=True, timeout=min(3, deadline - time.monotonic()))
                    require(child.poll() is None and state.returncode == 0 and state.stdout.strip() == b"true" and not result_path.exists(),
                            "Owned Bor server is not live at injection")
                    break
            time.sleep(0.05)
        require(ready is not None, "Owned Bor IPC did not become ready")
        save(target / "readiness.json", {"event": ready, "fresh_datadir": str(target / "state/runtime/data"),
                                         "container": request["container"], "parent_pid": child.pid,
                                         "injection_monotonic": time.monotonic(), "signal": number,
                                         "scope": "process-group" if group else "process"})
        if number is not None:
            os.killpg(child.pid, number) if group else child.send_signal(number)
            child.wait(timeout=5)
        else:
            child.wait(timeout=max(1, operation_deadline - time.monotonic()))
        until = min(deadline, time.monotonic() + 35)
        while not result_path.exists() and time.monotonic() < until:
            time.sleep(0.05)
        result = load(result_path, 1024 * 1024, deadline)
        require(result["reason"] == ("DEADLINE" if number is None else "PARENT_EXIT") and
                result["cleanup_complete"] is True and result["process_group_absent"] is True,
                "Owned Bor server termination did not prove cleanup")
        require(time.monotonic() <= deadline, "Shared campaign deadline expired during cleanup")
        summary["controls"].append({"name": name, "control_result": "PASS", "parent_exit_code": child.returncode,
                                    "dispatch_monotonic": dispatch, "operation_deadline_monotonic": operation_deadline,
                                    "request": reference(request_path, deadline=deadline), "result": reference(result_path, deadline=deadline),
                                    "readiness": reference(target / "readiness.json", deadline=deadline),
                                    "stdout": reference(target / "logs/000.stdout", 8 * 1024 * 1024, deadline),
                                    "stderr": reference(target / "logs/000.stderr", 8 * 1024 * 1024, deadline),
                                    "cleanup": reference(target / "logs/000.cleanup.json", deadline=deadline)})
    finally:
        if child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)
        # Parent death delegates cleanup to the existing guardian. Do not return
        # while its terminal evidence is still pending, including error paths.
        until = time.monotonic() + 35
        while not result_path.exists() and time.monotonic() < until:
            time.sleep(0.05)
        final = load(result_path, 1024 * 1024)
        require(final["cleanup_complete"] is True and final["process_group_absent"] is True, "Bor resource cleanup is unresolved")


def resource_controls(admitted, positive, output, summary, deadline):
    for number, group in ((None, False), (signal.SIGTERM, False), (signal.SIGKILL, False), (signal.SIGTERM, True), (signal.SIGKILL, True)):
        require(time.monotonic() + 100 <= deadline, "Insufficient remaining resource budget")
        server_stop_control(admitted, positive, output, summary, deadline, number, group)


def exercise(request_path, tool_path, tool_sha, output):
    started = time.monotonic()
    deadline = started + 900
    admitted = admit(request_path, tool_path, tool_sha, started)
    # Admission retains the scenario's <=1,800 s limit. The campaign itself has
    # one separate, fixed 900 s deadline and passes each child its own clipped cap.
    protected = [Path(admitted["request"]["build_manifest"]["path"]).parent, tool_path.parent,
                 Path(admitted["request"]["fixture"]["path"]).parent]
    require(not any(output.is_relative_to(path) for path in protected), "Campaign output cannot modify admitted storage")
    output = new_output(output)
    origins, sources = source_snapshot(output, ("bor_exercise.py", "bor_run.py", "bor_inputs.py", "bor.py", "bsc_exercise.py",
                                                 "artifacts.py", "native.py", "owned.py", "run.py"), deadline)
    summary = {"schema_version": 1, "kind": "bor-private-controls", "scope": "partial-native-seam", "result": "FAIL",
               "suite": "quick", "suite_result": "NOT_RUN", "deadline_seconds": 900, "controls": [],
               "request": admitted["request_ref"], "observer_tool": admitted["tool_ref"], "external_observer_tool_sha256": tool_sha,
               "harness_sources": sources, "public_profiles": "All seven profiles and all assigned case cells remain NOT_RUN"}
    positive = None
    try:
        names = ("positive", "native-corruption", "incomplete-chain", "wrong-oracle", "wrong-genesis-hash", "wrong-block-hash",
                 "genesis-tamper", "rlp-tamper", "config-identity", "binary-tamper", "observer-pin-tamper")
        for name in names:
            require(time.monotonic() + 340 < deadline, "Insufficient measured execution/cleanup budget")
            inputs = output / (name + "-inputs")
            inputs.mkdir()
            request, scenario = copy.deepcopy(admitted["request"]), copy.deepcopy(admitted["fixture"])
            selected_tool, selected_pin = tool_path, tool_sha
            if name in ("native-corruption", "incomplete-chain", "rlp-tamper"):
                _, data = read_artifact(scenario["blocks"], 16 * 1024 * 1024, deadline=deadline)
                body = first_rlp_item(data) if name == "incomplete-chain" else data[:-1]
                require(len(body) < len(data), "Negative input did not remove data")
                (inputs / "blocks.rlp").write_bytes(body)
                scenario["blocks"] = reference(inputs / "blocks.rlp", deadline=deadline) if name != "rlp-tamper" else {**scenario["blocks"], "path": str(inputs / "blocks.rlp")}
            elif name == "wrong-oracle":
                expected = copy.deepcopy(admitted["expected"])
                expected["accounts"][expected["user_transaction"]["to"]]["balance"] = "999"
                save(inputs / "expected.json", expected)
                scenario["expected"] = reference(inputs / "expected.json", deadline=deadline)
            elif name in ("wrong-genesis-hash", "config-identity"):
                scenario["identity"]["genesis_hash" if name == "wrong-genesis-hash" else "config_sha256"] = ("0x" if name == "wrong-genesis-hash" else "") + "0" * 64
                if name == "wrong-genesis-hash":
                    scenario["prepared_blocks"][0]["parent_hash"] = scenario["identity"]["genesis_hash"]
            elif name == "wrong-block-hash":
                scenario["prepared_blocks"][0]["hash"] = "0x" + "0" * 64
                scenario["prepared_blocks"][1]["parent_hash"] = scenario["prepared_blocks"][0]["hash"]
            elif name == "genesis-tamper":
                _, data = read_artifact(scenario["genesis"], 16 * 1024 * 1024, deadline=deadline)
                (inputs / "genesis.json").write_bytes(data + b" ")
                scenario["genesis"]["path"] = str(inputs / "genesis.json")
            elif name in ("binary-tamper", "observer-pin-tamper"):
                tool = copy.deepcopy(admitted["tool"])
                if name == "binary-tamper":
                    manifest = copy.deepcopy(admitted["manifest"])
                    manifest["builds"]["bor"]["binary"]["path"] = "invalid-bor"
                    (inputs / "invalid-bor").write_bytes(b"invalid native binary")
                    save(inputs / "manifest.json", manifest)
                    request["build_manifest"] = reference(inputs / "manifest.json", deadline=deadline)
                    tool["build_manifest"] = request["build_manifest"]
                else:
                    (inputs / "fake-observer").write_bytes(b"fake observer returning expected values")
                    tool["binary"] = reference(inputs / "fake-observer", deadline=deadline)
                save(inputs / "tool.json", tool)
                selected_tool = inputs / "tool.json"
                if name == "binary-tamper":
                    selected_pin = reference(selected_tool, deadline=deadline)["sha256"]
            save(inputs / "fixture.json", scenario)
            request["fixture"] = reference(inputs / "fixture.json", deadline=deadline)
            save(inputs / "request.json", request)
            target = output / name
            result = subprocess.run([sys.executable, "-I", str(Path(__file__).with_name("bor_run.py")), "run", str(inputs / "request.json"),
                                     "--observer-tool", str(selected_tool), "--observer-tool-sha256", selected_pin, "--output", str(target)],
                                    capture_output=True, text=True, timeout=min(330, deadline - time.monotonic()))
            (inputs / "stdout.txt").write_text(result.stdout)
            (inputs / "stderr.txt").write_text(result.stderr)
            row = {"name": name, "exit_code": result.returncode, "request": reference(inputs / "request.json", deadline=deadline),
                   "stdout": reference(inputs / "stdout.txt", deadline=deadline), "stderr": reference(inputs / "stderr.txt", deadline=deadline)}
            if name in ("genesis-tamper", "rlp-tamper", "config-identity", "binary-tamper", "observer-pin-tamper"):
                reason = "Private config identity mismatch" if name == "config-identity" else (
                    "external trusted pin" if name == "observer-pin-tamper" else "Artifact bytes changed")
                require(result.returncode == 2 and not target.exists() and reason in result.stdout, "Admission control failed for another reason")
                row["native_dispatched"] = False
                if name == "binary-tamper":
                    row["authority_kind"] = "explicit-invalid-build-negative"
            else:
                proof = load(target / "proof.json", deadline=deadline)
                row["proof"] = reference(target / "proof.json", deadline=deadline)
                require(proof["cleanup_complete"] is True and proof["suite_result"] == "NOT_RUN", "Measured cleanup/scope failed")
                if name == "positive":
                    require(result.returncode == 0 and proof["result"] == "PASS" and len(proof["commands"]) == 1, "Positive Bor control failed")
                    positive = proof
                else:
                    require(result.returncode == 2 and proof["result"] == "FAIL", "Native negative did not retain FAIL")
                    if name in ("native-corruption", "incomplete-chain"):
                        validate_native_rejection(name, proof, deadline)
                    else:
                        message = {"wrong-oracle": "Account mismatch", "wrong-genesis-hash": "Fresh genesis readiness mismatch", "wrong-block-hash": "Prepared block mismatch"}[name]
                        require(len(proof["commands"]) == 1 and message in proof.get("error", {}).get("message", ""), "Comparison failed for another reason")
                        _, native = json_artifact(proof["commands"][0]["result"], 1024 * 1024, deadline=deadline)
                        require(native["exit_code"] == 0 and native["cleanup_complete"] is True, "Comparison did not follow successful native observation")
            row["control_result"] = "PASS"
            summary["controls"].append(row)
        require(positive is not None, "Missing actual positive")
        _, observation = json_artifact(positive["observations"]["observed.json"], 16 * 1024 * 1024, deadline=deadline)
        states = [json_artifact(positive["observations"]["state-%d.json" % n], 16 * 1024 * 1024, deadline=deadline)[1] for n in (1, 2)]
        identities = [admitted["fixture"]["identity"]["genesis_hash"], *[row["hash"] for row in observation["blocks"]]]
        for name in ("incomplete-state", "missing-receipt", "missing-bor-log"):
            changed_states, changed = copy.deepcopy(states), copy.deepcopy(observation)
            if name == "incomplete-state":
                address = admitted["expected"]["user_transaction"]["to"]
                changed_states[0]["accounts"] = {key: value for key, value in changed_states[0]["accounts"].items() if key.removeprefix("0x").lower() != address[2:]}
            elif name == "missing-receipt":
                changed["blocks"][0]["receipts"] = []
            else:
                changed["blocks"][0]["receipts"][0]["logs"].pop()
            derived = output / (name + ".json")
            save(derived, {"observed": changed, "states": changed_states})
            try:
                compare_native(admitted["expected"], changed, changed_states, identities, admitted["fixture"]["prepared_blocks"], admitted["fixture"]["identity"]["config_sha256"])
                raise AssertionError("Expected comparison rejection")
            except Rejected as error:
                summary["controls"].append({"name": name, "control_result": "PASS", "scope": "retained-observation comparison; no new native execution",
                                            "derived_input": reference(derived, deadline=deadline), "error": str(error)})
        resource_controls(admitted, positive, output, summary, deadline)
        sources_unchanged(origins, deadline)
        # Revalidate every original authority before the final campaign claim.
        admitted["deadline"] = deadline
        revalidate_authority(admitted)
        require(time.monotonic() <= deadline, "Campaign exceeded its total deadline")
        summary["result"] = "PASS"
    except BaseException as error:
        summary["error"] = {"type": type(error).__name__, "message": str(error)}
        raise
    finally:
        summary["elapsed_seconds"] = time.monotonic() - started
        save(output / "controls.json", summary)
    return output / "controls.json"


if __name__ == "__main__":
    if len(sys.argv) == 6 and sys.argv[1] == "--owned-server":
        raise SystemExit(owned_server(Path(sys.argv[2]), sys.argv[3], float(sys.argv[4]), float(sys.argv[5])))
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("request", type=Path)
    parser.add_argument("--observer-tool", type=Path, required=True)
    parser.add_argument("--observer-tool-sha256", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    print(exercise(args.request, args.observer_tool, args.observer_tool_sha256, args.output))
