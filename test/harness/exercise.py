#!/usr/bin/env python3
"""Explicit real-native controls; excluded from portable CI discovery."""

import argparse
import copy
import json
from pathlib import Path
import subprocess
import sys
import time
import signal
import shutil
import os
import math

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import admit, load, reference, require
from native import Native, new_output


def save(path, value):
    path.write_text(json.dumps(value, sort_keys=True, indent=2) + "\n")


def exercise(request_path, output):
    output = new_output(output)
    original = load(request_path)
    fixture = load(original["fixture"]["path"])
    start = time.monotonic()
    summary = {"schema_version": 1, "kind": "explicit-native-controls", "result": "FAIL",
               "source_request": reference(request_path), "controls": [], "quick_suite": "NOT_RUN"}
    try:
        for name in ("positive", "native-corruption", "wrong-oracle", "wrong-genesis-hash", "wrong-block-hash",
                     "genesis-tamper", "rlp-tamper", "binary-tamper"):
            require(time.monotonic() - start < 900, "Control campaign deadline expired")
            inputs = output / (name + "-inputs")
            inputs.mkdir()
            request = copy.deepcopy(original)
            changed = copy.deepcopy(fixture)
            if name in ("native-corruption", "rlp-tamper"):
                body = Path(fixture["blocks"]["path"]).read_bytes()[:-1]
                (inputs / "blocks.rlp").write_bytes(body)
                if name == "native-corruption":
                    changed["blocks"] = reference(inputs / "blocks.rlp")
                else:
                    changed["blocks"]["path"] = str(inputs / "blocks.rlp")
            elif name == "wrong-oracle":
                expected = load(fixture["expected"]["path"])
                expected["blocks"][0]["accounts"]["1000000000000000000000000000000000000000"]["balance"] = "999"
                save(inputs / "expected.json", expected)
                changed["expected"] = reference(inputs / "expected.json")
            elif name == "wrong-genesis-hash":
                changed["identity"]["genesis_hash"] = "0x" + "0" * 64
            elif name == "wrong-block-hash":
                changed["prepared_blocks"][0]["hash"] = "0x" + "0" * 64
            elif name == "genesis-tamper":
                (inputs / "genesis.json").write_bytes(Path(fixture["genesis"]["path"]).read_bytes() + b" ")
                changed["genesis"]["path"] = str(inputs / "genesis.json")
            elif name == "binary-tamper":
                # This derived negative input is not an accepted BuildManifest.
                # It keeps the admitted binary digest while changing its bytes.
                manifest = load(original["build_manifest"]["path"])
                manifest["builds"]["geth"]["binary"]["path"] = "geth"
                (inputs / "geth").write_bytes(b"invalid native binary")
                save(inputs / "manifest.json", manifest)
                request["build_manifest"] = reference(inputs / "manifest.json")
            save(inputs / "fixture.json", changed)
            request["fixture"] = reference(inputs / "fixture.json")
            save(inputs / "request.json", request)
            run_output = output / name
            result = subprocess.run([sys.executable, "-I", str(Path(__file__).with_name("run.py")), "run",
                                     str(inputs / "request.json"), "--output", str(run_output)],
                                    capture_output=True, text=True, timeout=min(330, max(1, 900 - (time.monotonic() - start))))
            (inputs / "stdout.txt").write_text(result.stdout)
            (inputs / "stderr.txt").write_text(result.stderr)
            row = {"name": name, "exit_code": result.returncode, "stdout": reference(inputs / "stdout.txt"),
                   "stderr": reference(inputs / "stderr.txt"), "request": reference(inputs / "request.json")}
            if name.endswith("tamper"):
                require(result.returncode == 2 and not run_output.exists() and "Artifact bytes changed" in result.stdout,
                        "Admission tamper did not reject before native dispatch")
                row["native_dispatched"] = False
            else:
                proof = load(run_output / "proof.json")
                row["proof"] = reference(run_output / "proof.json")
                require(proof["cleanup_complete"] and proof["suite_result"] == "NOT_RUN", "Control cleanup/scope failed")
                if name == "positive":
                    require(result.returncode == 0 and proof["result"] == "PASS" and len(proof["commands"]) == 6,
                            "Positive native control failed")
                elif name == "native-corruption":
                    require(result.returncode == 2 and proof["result"] == "FAIL" and len(proof["commands"]) == 2 and
                            load(proof["commands"][-1]["result"]["path"])["reason"] == "NATIVE_EXIT",
                            "Corrupt input did not reach and fail native import")
                elif name == "wrong-oracle":
                    require(result.returncode == 2 and proof["result"] == "FAIL" and len(proof["commands"]) == 6 and
                            proof.get("error", "").startswith("Account state mismatch"), "Wrong oracle was not detected")
                else:
                    require(result.returncode == 2 and proof["result"] == "FAIL" and len(proof["commands"]) == 6 and
                            proof.get("error", "").startswith("Native header identity"), "Wrong native identity was not detected")
            row["control_result"] = "PASS"
            summary["controls"].append(row)
        # This checks the actual resource boundary, not an Ethereum scenario.
        timeout_output = output / "container-timeout"
        timeout_output.mkdir()
        runner = Native(timeout_output, time.monotonic() + 45)
        try:
            runner.command(admit(request_path)["image"], ["/bin/sleep", "30"], [], cap=36)
            raise ValueError("Expected the real container deadline to fail")
        except ValueError:
            require(len(runner.commands) == 1, "Timeout did not dispatch exactly one owned command")
            timeout_result = load(runner.commands[0]["result"]["path"])
            require(timeout_result["reason"] == "DEADLINE" and timeout_result["cleanup_complete"],
                    "Actual container deadline or cleanup failed")
        summary["controls"].append({"name": "container-timeout", "control_result": "PASS",
                                    "native_owner_nonce": runner.nonce, "commands": runner.commands})
        for number, group in ((signal.SIGTERM, False), (signal.SIGKILL, False),
                              (signal.SIGTERM, True), (signal.SIGKILL, True)):
            label = ("parent-group-stop-" if group else "parent-stop-") + str(number)
            child_output = output / label
            child_output.mkdir()
            image = admit(request_path)["image"]
            child = subprocess.Popen([sys.executable, "-I", str(Path(__file__)), "--owned-sleep",
                                      str(child_output), image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                     start_new_session=True)
            try:
                wait_until = time.monotonic() + 10
                request_log = child_output / "logs/000.request.json"
                name = None
                while time.monotonic() < wait_until:
                    if request_log.exists():
                        try:
                            name = load(request_log)["container"]
                        except ValueError:
                            continue
                        state = subprocess.run([shutil.which("docker"), "inspect", "--format", "{{.State.Running}}", name],
                                               capture_output=True, timeout=3)
                        if state.returncode == 0 and state.stdout.strip() == b"true":
                            break
                    time.sleep(0.05)
                else:
                    raise ValueError("Owned real container did not start")
                if group:
                    os.killpg(child.pid, number)
                else:
                    child.send_signal(number)
                child.wait(timeout=5)
                result_log = child_output / "logs/000.result.json"
                wait_until = time.monotonic() + 35
                while not result_log.exists() and time.monotonic() < wait_until:
                    time.sleep(0.05)
                result = load(result_log)
                require(result["reason"] == "PARENT_EXIT" and result["cleanup_complete"], "Parent-loss cleanup failed")
                summary["controls"].append({"name": label, "control_result": "PASS",
                                            "termination_scope": "process-group" if group else "process",
                                            "parent_exit_code": child.returncode, "request": reference(request_log),
                                            "result": reference(result_log), "container": name})
            finally:
                if child.poll() is None:
                    child.terminate()
                    child.wait(timeout=5)
        summary["result"] = "PASS"
    finally:
        summary["elapsed_seconds"] = time.monotonic() - start
        save(output / "controls.json", summary)
    return output / "controls.json"


if __name__ == "__main__":
    if len(sys.argv) in (4, 5) and sys.argv[1] == "--owned-sleep":
        deadline = time.monotonic() + 90
        if len(sys.argv) == 5:
            inherited = float(sys.argv[4])
            require(math.isfinite(inherited), "Invalid inherited resource deadline")
            deadline = min(deadline, inherited)
        require(time.monotonic() + 35 < deadline, "Insufficient inherited resource cleanup budget")
        Native(Path(sys.argv[2]), deadline).command(sys.argv[3], ["/bin/sleep", "30"], [], cap=90)
        raise SystemExit(0)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("request", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    print(exercise(args.request, args.output))
