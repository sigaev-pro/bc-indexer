"""Portable input-boundary checks for explicit native controls."""

from pathlib import Path
import sys
import subprocess
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import Rejected
from bsc_exercise import first_rlp_item
import bsc_exercise
from test_harness import ref, write_json
from test_bsc_inputs import fixture


class BscControlInputTests(unittest.TestCase):
    def test_final_authority_check_uses_campaign_budget_and_rejects_replacement(self):
        for name in ("request.json", "manifest.json", "fixture.json", "tool.json"):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                fixture(root)
                admitted = bsc_exercise.admit(root / "request.json", root / "tool.json",
                                              ref(root / "tool.json")["sha256"])
                admitted["deadline"] = time.monotonic() - 1
                campaign = {**admitted, "deadline": time.monotonic() + 30}
                bsc_exercise.revalidate_authority(campaign)
                path = root / name
                path.write_bytes(path.read_bytes() + b" ")
                with self.assertRaises(Rejected):
                    bsc_exercise.revalidate_authority(campaign)
                self.assertLess(admitted["deadline"], time.monotonic())

    def test_first_complete_native_rlp_list_keeps_exact_bytes(self):
        first = b"\xf8\x38" + b"x" * 56
        self.assertEqual(first_rlp_item(first + b"\xc0"), first)
        self.assertEqual(first_rlp_item(b"\xc1\x00\xc0"), b"\xc1\x00")

    def test_truncated_noncanonical_or_nonlist_input_rejects(self):
        for value in (b"", b"\x80", b"\xf8", b"\xf8\x38x", b"\xf8\x01x", b"\xf9\x00\x38" + b"x" * 56):
            with self.subTest(value=value[:4]), self.assertRaises(Rejected):
                first_rlp_item(value)

    def test_native_rejection_requires_exact_stage_and_diagnostic(self):
        for name, count, tail, message in (
            ("native-corruption", 2, ["import", "/inputs/blocks"], "Import error: at block 1: unexpected EOF"),
            ("incomplete-chain", 5, ["dump", "--iterative=false", "--limit", "0", "2"], "header for block 2 not found"),
        ):
            for mutation in (None, "wrong-stage", "wrong-count", "docker-exit", "unrelated-error", "changed-result"):
                with self.subTest(name=name, mutation=mutation), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory).resolve()
                    argv = ["docker", "run", "pinned-image", "/inputs/bsc", *tail]
                    if mutation == "wrong-stage":
                        argv = argv[:-len(tail)] + ["init", "/inputs/genesis"]
                    result = {"argv": argv, "exit_code": 125 if mutation == "docker-exit" else 1,
                              "reason": "NATIVE_EXIT", "cleanup_complete": True, "process_group_absent": True}
                    write_json(root / "request.json", {"argv": argv})
                    write_json(root / "result.json", result)
                    (root / "stderr").write_text("daemon unavailable" if mutation == "unrelated-error" else message)
                    row = {key: ref(root / filename) for key, filename in (("request", "request.json"), ("result", "result.json"), ("stderr", "stderr"))}
                    proof = {"result": "FAIL", "commands": [row] * (count - 1 if mutation == "wrong-count" else count)}
                    if mutation == "changed-result":
                        (root / "result.json").write_text("{}")
                    if mutation is None:
                        bsc_exercise.validate_native_rejection(name, proof)
                    else:
                        with self.assertRaises(Rejected):
                            bsc_exercise.validate_native_rejection(name, proof)

    def test_shared_resource_budget_rejects_new_dispatch_after_reserve_is_spent(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            clock, starts, child_budgets, pending = [100.0], [], [], []
            class FakeNative:
                def __init__(self, output, deadline):
                    self.output, self.deadline, self.nonce = output, deadline, "synthetic"
                    self.commands = []
                def command(self, *args, **kwargs):
                    clock[0] += 30  # Command plus cleanup remains below its local bound.
                    path = self.output / "result.json"
                    write_json(path, {"reason": "DEADLINE", "cleanup_complete": True})
                    self.commands.append({"result": ref(path)})
                    raise Rejected("Expected synthetic timeout")
            class FakeChild:
                def __init__(self, args, **kwargs):
                    starts.append(clock[0]); child_budgets.append(float(args[6]) if len(args) == 7 else None)
                    target = Path(args[4]); (target / "logs").mkdir()
                    write_json(target / "logs/000.request.json", {"container": "synthetic-container"})
                    pending.append((clock[0] + 38, target / "logs/000.result.json"))
                    self.pid, self.returncode = 123456789, None
                def send_signal(self, number):
                    pass
                def wait(self, timeout):
                    clock[0] += 4  # Under the existing five-second parent wait.
                    self.returncode = -15
                def poll(self):
                    return self.returncode
                def terminate(self):
                    self.returncode = -15
                def kill(self):
                    self.returncode = -9
            def sleep(duration):
                clock[0] += duration
                for ready, path in pending:
                    if clock[0] >= ready and not path.exists():
                        write_json(path, {"reason": "PARENT_EXIT", "cleanup_complete": True, "process_group_absent": True})
            with patch.object(bsc_exercise, "Native", FakeNative), patch.object(bsc_exercise.subprocess, "Popen", FakeChild), \
                 patch.object(bsc_exercise.subprocess, "run", return_value=SimpleNamespace(returncode=0, stdout=b"true")), \
                 patch.object(bsc_exercise.os, "killpg"), patch.object(bsc_exercise.time, "monotonic", side_effect=lambda: clock[0]), \
                 patch.object(bsc_exercise.time, "sleep", side_effect=sleep):
                summary = {"controls": []}
                with self.assertRaisesRegex(Rejected, "budget"):
                    bsc_exercise.resource_controls("synthetic-image", root, summary, 201.0)
            self.assertEqual(starts, [130.0])
            self.assertTrue(all(value is not None and value <= 201 for value in child_budgets))
            self.assertEqual(len(summary["controls"]), 2)
            self.assertTrue(all(path.exists() for _, path in pending))

    def test_owned_resource_child_rejects_expired_or_nonfinite_inherited_budget(self):
        for value in ("0", "nan"):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                result = subprocess.run([sys.executable, "-I", str(Path(__file__).with_name("exercise.py")),
                                         "--owned-sleep", str(root), "synthetic-image", value],
                                        capture_output=True, text=True, timeout=5)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((root / "logs").exists())


if __name__ == "__main__":
    unittest.main()
