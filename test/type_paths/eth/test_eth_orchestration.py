"""Portable failure injection for the ETH driver's ownership and evidence boundary."""

import contextlib
import fcntl
import json
import io
from pathlib import Path
import signal
import subprocess
import tempfile
import tarfile
import unittest
from unittest.mock import patch

import eth_type_paths as driver


class OrchestrationTests(unittest.TestCase):
    def setUp(self):
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.root = Path(self.stack.enter_context(tempfile.TemporaryDirectory())).resolve()
        self.output = self.root / "output"
        self.lock = self.root / "lock"
        self.prep_path = self.root / "prepared" / "preparation.json"
        self.prep_path.parent.mkdir()
        self.eth = self.root / "eth"
        self.eth.mkdir()
        retained = self.root / "retained"
        retained.mkdir()
        self.code = []
        for name in driver.FILES:
            (self.eth / name).write_text(name)
            (retained / name).write_text(name)
            self.code.append({"copy": {"path": str(retained / name)}})
        self.accepted = {"kind": "admission-geth-preparation", "result": "PASS", "cleanup_complete": True,
                         "decision_native": True, "source_commit": driver.NATIVE,
                         "build_manifest": {"path": str(self.root / "manifest" / "build.json")}}
        self.prep = {"kind": "native-type-paths-eth-preparation", "result": "PASS", "cleanup_complete": True,
                     "accepted_preparation": {"path": "accepted"}, "binary": {"path": str(self.root / "bin" / "probe")},
                     "code": self.code}
        self.manifest = {"image_inputs": {"debian-runtime-image": {"reference": "docker.io/library/debian@sha256:" + "a" * 64}}}
        self.command_error = None
        self.cleanup = True
        self.commands = []
        self.created = []
        self.judge_exit = 0
        self.judge_result = "PASS"
        self.state = {"clean": True, "commit": "a" * 40}
        self.signal_handler = signal.getsignal(signal.SIGTERM)
        self.signal_mask = signal.pthread_sigmask(signal.SIG_BLOCK, ())
        test = self

        class FakeNative:
            def __init__(self, output, deadline):
                self.nonce = "owned-test-nonce"
                self.commands = test.commands
                self.cleanup_complete = True
                test.created.append(output)

            def command(self, *args, **kwargs):
                self.commands.append({"argv": list(args[1])})
                self.cleanup_complete = test.cleanup
                if test.command_error:
                    raise test.command_error

        def dispatch(argv, **kwargs):
            if argv[:2] == ["docker", "ps"]:
                return subprocess.CompletedProcess(argv, 0, "", "")
            return subprocess.CompletedProcess(argv, self.judge_exit, json.dumps({"result": self.judge_result}), "")

        for target, value in (("LOCK", self.lock),):
            self.stack.enter_context(patch.object(driver.capture_probe, target, value))
        self.stack.enter_context(patch.object(driver, "ETH", self.eth))
        self.stack.enter_context(patch.object(driver, "Native", FakeNative))
        self.stack.enter_context(patch.object(driver.capture_probe, "source_state", lambda: self.state))
        self.stack.enter_context(patch.object(driver, "pinned_json", lambda *a: ({"path": "pinned"}, self.prep)))
        self.stack.enter_context(patch.object(driver, "json_artifact", lambda row, *a, **kw:
                                              (None, self.accepted if row["path"] == "accepted" else self.manifest)))
        self.stack.enter_context(patch.object(driver, "artifact", lambda row, *a, **kw: Path(row["path"])))
        self.stack.enter_context(patch.object(driver, "reference", lambda p, *a, **kw: {"path": str(p)}))
        self.stack.enter_context(patch.object(driver.subprocess, "run", dispatch))

    def assert_released(self):
        self.assertEqual(signal.getsignal(signal.SIGTERM), self.signal_handler)
        self.assertEqual(signal.pthread_sigmask(signal.SIG_BLOCK, ()), self.signal_mask)
        with self.lock.open("a+") as handle:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
            fcntl.flock(handle, fcntl.LOCK_UN)

    def run_case(self):
        return driver.run(self.prep_path, "unused-test-pin", self.output)

    def failed_record(self, record, message):
        self.assertEqual(record["result"], "FAIL", record)
        self.assertIn(message, record["error"])
        saved = json.loads((self.output / "run-record.json").read_text())
        self.assertEqual(saved, record)
        self.assert_released()

    def test_success_retains_owned_command_and_cleanup(self):
        record = self.run_case()
        self.assertEqual(record["result"], "PASS")
        self.assertTrue(record["cleanup_complete"])
        self.assertEqual(len(record["commands"]), 1)
        self.assertEqual(self.created, [self.output])
        self.assert_released()

    def test_native_failure_retains_error_cleanup_and_releases_lock(self):
        self.command_error = RuntimeError("injected native failure")
        self.failed_record(self.run_case(), "injected native failure")

    def test_interruption_retains_record_and_releases_lock(self):
        with patch.object(driver, "artifact", side_effect=driver.Interrupted("injected signal")):
            self.failed_record(self.run_case(), "injected signal")

    def test_signal_handler_interrupts_owned_run(self):
        def interrupted(*args, **kwargs):
            driver.capture_probe.stop(signal.SIGTERM, None)
        with patch.object(driver, "artifact", side_effect=interrupted):
            record = self.run_case()
        self.failed_record(record, "signal")
        self.assertIn(signal.SIGTERM, record["deferred_signals"])

    def test_constructor_failure_still_writes_failure(self):
        with patch.object(driver, "Native", side_effect=RuntimeError("constructor")):
            record = self.run_case()
        self.failed_record(record, "constructor")
        self.assertEqual(record["commands"], [])

    def test_cleanup_failure_cannot_pass(self):
        self.cleanup = False
        record = self.run_case()
        self.assertEqual(record["result"], "FAIL")
        self.assertFalse(record["cleanup_complete"])
        self.assert_released()

    def test_prepared_source_mismatch_never_executes(self):
        (self.eth / "probe.go").write_text("changed")
        self.failed_record(self.run_case(), "Prepared sources differ")
        self.assertEqual(self.commands, [])

    def test_incomplete_prepared_source_set_never_executes(self):
        self.code.pop()
        self.failed_record(self.run_case(), "Incomplete preparation code set")
        self.assertEqual(self.commands, [])

    def test_unpinned_runtime_never_executes(self):
        self.manifest["image_inputs"]["debian-runtime-image"]["reference"] = "debian:latest"
        self.failed_record(self.run_case(), "Unpinned runtime")
        self.assertEqual(self.commands, [])

    def test_judge_failure_and_nonzero_exit_cannot_pass(self):
        for result, code in (("FAIL", 0), ("PASS", 1)):
            self.output = self.root / f"judge-{result}-{code}"
            self.judge_result, self.judge_exit = result, code
            self.failed_record(self.run_case(), "judge failed")

    def test_dirty_source_and_failed_preparation_rejected_before_ownership(self):
        self.state["clean"] = False
        with self.assertRaisesRegex(Exception, "clean committed"):
            self.run_case()
        self.state["clean"] = True
        for field, value in (("result", "FAIL"), ("cleanup_complete", False), ("kind", "other")):
            original = self.prep[field]
            self.prep[field] = value
            with self.assertRaisesRegex(Exception, "passing type-path preparation"):
                self.run_case()
            self.prep[field] = original
        self.assertFalse(self.output.exists())
        self.assertEqual(self.created, [])

    def test_foreign_output_is_never_overwritten(self):
        self.output.mkdir()
        sentinel = self.output / "foreign"
        sentinel.write_text("untouched")
        with self.assertRaises(Exception):
            self.run_case()
        self.assertEqual(sentinel.read_text(), "untouched")
        self.assertEqual(list(self.output.iterdir()), [sentinel])
        self.assert_released()

    def test_output_inside_preparation_is_rejected(self):
        self.output = self.prep_path.parent / "output"
        with self.assertRaisesRegex(Exception, "inside"):
            self.run_case()
        self.assertFalse(self.output.exists())

    def test_prepare_failure_retains_record_and_lock_release(self):
        self.prep = self.accepted
        with patch.object(driver.capture_probe, "copy_sources", side_effect=RuntimeError("copy failure")):
            record = driver.prepare(self.prep_path, "unused-test-pin", self.output)
        self.assertEqual(record["result"], "FAIL")
        self.assertIn("copy failure", record["error"])
        self.assertEqual(json.loads((self.output / "preparation.json").read_text()), record)
        self.assertTrue(record["cleanup_complete"])
        self.assert_released()

    def prepare_fixture(self):
        self.prep = self.accepted
        compiler = "docker.io/library/golang@sha256:" + "a" * 64
        self.accepted.update(compiler_image=compiler, source_archive={"path": "source", "size_bytes": 10240}, install={})
        self.manifest["image_inputs"]["golang-build-image"] = {"reference": compiler}
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w"):
            pass
        for owner, name, result in ((driver.capture_probe, "copy_sources", []),
                                    (driver, "copy_artifact", {}), (driver, "read_bytes", buffer.getvalue()),
                                    (driver.corpus_eth, "verified_copies", ({}, [])),
                                    (driver.corpus_eth, "repository_equal", []),
                                    (driver.corpus_eth, "code_equal", True),
                                    (driver.corpus_eth, "install", {}),
                                    (driver.corpus_eth, "install_differences", [])):
            self.stack.enter_context(patch.object(owner, name, return_value=result))

    def test_prepare_success_and_native_failure_are_retained(self):
        self.prepare_fixture()
        record = driver.prepare(self.prep_path, "unused-test-pin", self.output)
        self.assertEqual(record["result"], "PASS", record)
        self.assertEqual(len(record["commands"]), 3)
        self.assertTrue(record["install_equals_accepted"])
        self.output = self.root / "prepare-failure"
        self.command_error = RuntimeError("compiler failure")
        record = driver.prepare(self.prep_path, "unused-test-pin", self.output)
        self.assertEqual(record["result"], "FAIL")
        self.assertIn("compiler failure", record["error"])
        self.assertEqual(json.loads((self.output / "preparation.json").read_text()), record)
        self.assert_released()

    def test_prepare_rejects_unrelated_native_and_dirty_source(self):
        self.prep = self.accepted
        for field, value, reason in (("source_commit", "bad", "Wrong native source"),
                                    ("kind", "bad", "decision-bridge"), ("result", "FAIL", "decision-bridge"),
                                    ("decision_native", False, "decision-bridge"),
                                    ("cleanup_complete", False, "decision-bridge")):
            old = self.accepted[field]
            self.accepted[field] = value
            with self.assertRaisesRegex(Exception, reason):
                driver.prepare(self.prep_path, "unused-test-pin", self.output)
            self.accepted[field] = old
        self.state["clean"] = False
        with self.assertRaisesRegex(Exception, "clean committed"):
            driver.prepare(self.prep_path, "unused-test-pin", self.output)
        self.assertEqual(self.created, [])

    def test_prepare_adapter_compiler_and_install_guards(self):
        self.prepare_fixture()
        cases = ((driver.corpus_eth, "code_equal", False, "adapter files differ"),
                 (driver.corpus_eth, "install_differences", ["changed"], "install differs"))
        for index, (owner, name, value, reason) in enumerate(cases):
            self.output = self.root / f"prepare-guard-{index}"
            with patch.object(owner, name, return_value=value):
                record = driver.prepare(self.prep_path, "unused-test-pin", self.output)
            self.assertEqual(record["result"], "FAIL")
            self.assertIn(reason, record["error"])
            self.assertEqual(self.commands, [])
            self.assert_released()
        self.output = self.root / "prepare-compiler"
        self.accepted["compiler_image"] = "golang:latest"
        record = driver.prepare(self.prep_path, "unused-test-pin", self.output)
        self.assertEqual(record["result"], "FAIL")
        self.assertIn("Unpinned compiler", record["error"])
        self.assertEqual(self.commands, [])
        self.output = self.root / "prepare-mutually-unpinned"
        self.manifest["image_inputs"]["golang-build-image"]["reference"] = "golang:latest"
        record = driver.prepare(self.prep_path, "unused-test-pin", self.output)
        self.assertEqual(record["result"], "FAIL")
        self.assertIn("Unpinned compiler", record["error"])


if __name__ == "__main__":
    unittest.main()
