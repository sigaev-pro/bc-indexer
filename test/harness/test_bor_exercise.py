"""Portable controls for real Bor campaign classification and shared budgets."""

import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

from artifacts import Rejected, reference
import bor_exercise


class BorCampaignTests(unittest.TestCase):
    def test_live_log_growth_is_bounded_without_requiring_immutable_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary).resolve() / "live.log"
            path.write_bytes(b"ready\n")
            read = os.read
            def growing(descriptor, size):
                with path.open("ab") as stream:
                    stream.write(b"later\n")
                return read(descriptor, size)
            with patch.object(bor_exercise.os, "read", side_effect=growing):
                self.assertEqual(bor_exercise.read_live_prefix(path, time.monotonic() + 2, 12), b"ready\n")
            with patch.object(bor_exercise.os, "read") as reader:
                with self.assertRaisesRegex(Rejected, "byte limit"):
                    bor_exercise.read_live_prefix(path, time.monotonic() + 2, 11)
                reader.assert_not_called()
            with self.assertRaisesRegex(Rejected, "deadline"):
                bor_exercise.read_live_prefix(path, time.monotonic() - 1)
            path.write_bytes(b"ready\n")
            with patch.object(bor_exercise.os, "read", side_effect=growing):
                with self.assertRaisesRegex(Rejected, "byte limit"):
                    bor_exercise.read_live_prefix(path, time.monotonic() + 2, 11)

    def test_owned_output_is_visible_before_native_exit(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            docker = root / "docker"
            docker.write_text("#!/usr/bin/env python3\nimport sys,time\n"
                              "if sys.argv[1]=='run':\n print('ready',file=sys.stderr,flush=True)\n time.sleep(30)\n")
            docker.chmod(0o700)
            request = root / "request.json"
            request.write_text(json.dumps({"argv": [str(docker), "run"], "docker": str(docker), "container": "owned-test",
                                           "deadline": time.monotonic() + 10, "output": str(root / "command")}))
            read_fd, write_fd = os.pipe()
            child = subprocess.Popen([sys.executable, "-I", str(Path(__file__).with_name("owned.py")), str(request), str(read_fd)],
                                     pass_fds=(read_fd,), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            os.close(read_fd)
            try:
                until = time.monotonic() + 2
                visible = b""
                while time.monotonic() < until:
                    if (root / "command.stderr").exists():
                        visible = (root / "command.stderr").read_bytes()
                    if visible:
                        break
                    time.sleep(0.01)
                self.assertIsNone(child.poll())
                self.assertEqual(visible, b"ready\n")
            finally:
                os.close(write_fd)
                child.wait(timeout=12)
                result = json.loads((root / "command.result.json").read_text())
                self.assertTrue(result["cleanup_complete"])
                self.assertTrue(result["process_group_absent"])

    def proof(self, root, name):
        def artifact(filename, value):
            path = root / filename
            path.write_text(value if isinstance(value, str) else json.dumps(value))
            return reference(path)
        methods = ["eth_blockNumber", "debug_getRawBlock", "admin_nodeInfo", "eth_call", "admin_importChain"]
        if name == "incomplete-chain":
            methods.append("eth_blockNumber")
        requests = [{"jsonrpc": "2.0", "id": n, "method": method} for n, method in enumerate(methods, 1)]
        requests[4]["params"] = ["/inputs/blocks.rlp"]
        responses = [{"jsonrpc": "2.0", "id": n, "result": value} for n, value in enumerate(["0x0", "0xc0", {}, "0x00", True, "0x1"][:len(methods)], 1)]
        if name == "native-corruption":
            responses[-1] = {"jsonrpc": "2.0", "id": 5, "error": {"code": -32000, "message": "unexpected EOF"}}
        argv = ["docker", "run", "image", "/inputs/helper", "observe", "/inputs/genesis.json", "/inputs/blocks.rlp", "/work/runtime"]
        return {"result": "FAIL", "commands": [{
            "request": artifact("request.json", {"argv": argv}),
            "result": artifact("result.json", {"argv": argv, "exit_code": 2, "reason": "NATIVE_EXIT", "cleanup_complete": True, "process_group_absent": True}),
            "stderr": artifact("stderr", "native IPC import: unexpected EOF" if name == "native-corruption" else "native import terminal head is not two"),
        }], "observations": {
            "rpc.requests": artifact("requests", "".join(json.dumps(row) + "\n" for row in requests)),
            "rpc.responses": artifact("responses", "".join(json.dumps(row) + "\n" for row in responses)),
            "readiness.json": artifact("ready.json", {"head": 0, "genesis_hash": "0x" + "1" * 64}),
            "server-cleanup.json": artifact("cleanup.json", {"wait_completed": True, "unexpected_exit": False, "forced_kill": False, "exit_code": 0, "exit": ""}),
        }}

    def test_only_exact_native_import_or_prefix_failure_is_accepted(self):
        for name in ("native-corruption", "incomplete-chain"):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                proof = self.proof(root, name)
                bor_exercise.validate_native_rejection(name, proof)
                for target, change in (("requests", lambda rows: rows[4].update(method="eth_call")),
                                       ("responses", lambda rows: rows[-1].update(id=99)),
                                       ("responses", lambda rows: rows[0].update(result="0x1")),
                                       ("responses", lambda rows: rows[-1].update(error={"code": -1, "message": "server crashed"})),
                                       ("requests", lambda rows: rows.append(copy.deepcopy(rows[-1])))):
                    original = (root / target).read_text()
                    rows = [json.loads(line) for line in original.splitlines()]
                    change(rows)
                    (root / target).write_text("".join(json.dumps(row) + "\n" for row in rows))
                    changed = copy.deepcopy(proof)
                    changed["observations"]["rpc." + target] = reference(root / target)
                    with self.assertRaises(Rejected):
                        bor_exercise.validate_native_rejection(name, changed)
                    (root / target).write_text(original)
                changed = copy.deepcopy(proof)
                changed["commands"] = []
                with self.assertRaises(Rejected):
                    bor_exercise.validate_native_rejection(name, changed)

    def test_resource_dispatch_rechecks_shared_budget_for_every_control(self):
        clock = [0.0]
        calls = []
        def one(*args):
            calls.append(clock[0])
            clock[0] += 60.0
        with tempfile.TemporaryDirectory() as temporary, patch.object(bor_exercise.time, "monotonic", side_effect=lambda: clock[0]), patch.object(bor_exercise, "server_stop_control", side_effect=one):
            with self.assertRaisesRegex(Rejected, "budget"):
                bor_exercise.resource_controls({}, {}, Path(temporary), {"controls": []}, 150.0)
        self.assertEqual(calls, [0.0])


if __name__ == "__main__":
    unittest.main()
