"""Exercise real interrupt delivery through inline preflight command supervision."""

from contextlib import redirect_stdout
import io
import json
import os
from pathlib import Path
import signal
import sys
import tempfile
import unittest
from unittest.mock import patch

import controller
from controller import Blocked, Controller, Store
from controller_backend import RealBackend
import evidence
import session_runner
from test_controller import FakeBackend, graph


class SignalBackend(FakeBackend):
    def __init__(self, root, store):
        super().__init__()
        self.real = RealBackend.__new__(RealBackend)
        self.real.root, self.real.store = root, store
        self.signum = None

    def start_gate(self, tasks):
        if self.signum is not None:
            command = [sys.executable, "-c", "import os,signal,time; os.kill(os.getppid()," + str(self.signum) + "); time.sleep(0.5)"]
            self.real.command(command, timeout=3)
        return super().start_gate(tasks)


class ControllerSignalTests(unittest.TestCase):
    def call_cli(self, root, backend, run_id, automatic=False):
        arguments = ["controller.py", "resume", "--run-id", run_id]
        if automatic:
            arguments.append("--automatic")
        def factory(source, canonical, store, *args, **kwargs):
            backend.real.store = store
            return backend
        with patch.object(sys, "argv", arguments), patch.object(evidence, "repository_root", return_value=root), \
                patch("controller_backend.RealBackend", side_effect=factory), redirect_stdout(io.StringIO()) as output:
            code = controller.main()
        return code, json.loads(output.getvalue())

    def test_real_preflight_signals_persist_stop_and_repeated_automatic_recovery_does_no_work(self):
        for signum in (signal.SIGTERM, signal.SIGINT):
            with self.subTest(signal=signum), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve(); store = Store(root / ".project/automation/state")
                backend = SignalBackend(root, store)
                recipes = {key: {"verification": "isolated"} for key in ("BOOT-03", "BOOT-09")}
                previous = {number: signal.getsignal(number) for number in (signal.SIGTERM, signal.SIGINT)}
                try:
                    with store.lock():
                        run = Controller(store, backend).create(graph(), recipes)
                    before = store.load(run["run_id"])
                    backend.signum = signum
                    code, report = self.call_cli(root, backend, run["run_id"])
                    self.assertEqual(code, 1)
                    results = list((store.directory / "commands").glob("*/result.json"))
                    self.assertEqual(len(results), 1, "the regression must execute a real preflight command")
                    self.assertEqual(controller.read_json(results[0])["reason"], "stopped")
                    self.assertTrue(store.stopped(run["run_id"]), "inline preflight swallowed the durable operator stop")
                    self.assertEqual(store.load(run["run_id"])["status"], "STOPPED")
                    self.assertEqual(report["status"], "STOPPED")
                    marker = (store.directory / "stop" / (run["run_id"] + ".json")).read_bytes()
                    for _ in range(2):
                        with patch.object(backend, "start_gate") as preflight:
                            _, recovered = self.call_cli(root, backend, run["run_id"], automatic=True)
                        preflight.assert_not_called()
                        self.assertEqual(recovered["status"], "STOPPED")
                        saved = store.load(run["run_id"])
                        for key in ("deadline", "invocations", "budget_history", "budget_baseline", "tasks"):
                            self.assertEqual(saved[key], before[key])
                        self.assertEqual((store.directory / "stop" / (run["run_id"] + ".json")).read_bytes(), marker)
                    self.assertFalse(backend.effects)
                    self.assertFalse(backend.sessions)
                    for number, handler in previous.items():
                        self.assertIs(signal.getsignal(number), handler)
                    self.assertTrue(controller.read_json(results[0])["cleanup_complete"])
                    heartbeat = controller.read_json(results[0].parent / "heartbeat.json")
                    self.assertFalse(session_runner.live_group_members(heartbeat["process_group"]))
                finally:
                    for number, handler in previous.items():
                        signal.signal(number, handler)

    def test_signal_during_backend_setup_is_durable_before_run_preflight(self):
        for signum in (signal.SIGTERM, signal.SIGINT):
            with self.subTest(signal=signum), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve(); store = Store(root / ".project/automation/state")
                backend = SignalBackend(root, store)
                with store.lock():
                    run = Controller(store, backend).create(graph(), {"BOOT-03": {}, "BOOT-09": {}})
                def factory(source, canonical, active_store, *args, **kwargs):
                    real = RealBackend.__new__(RealBackend)
                    real.root, real.store = root, active_store
                    real.command_stop_file = kwargs["stop_file"]
                    real.command([sys.executable, "-c", "import os,time; os.kill(os.getppid()," + str(signum) + "); time.sleep(0.5)"])
                    self.fail("interrupted setup must not continue")
                arguments = ["controller.py", "resume", "--run-id", run["run_id"]]
                previous = {number: signal.getsignal(number) for number in (signal.SIGTERM, signal.SIGINT)}
                with patch.object(sys, "argv", arguments), patch.object(evidence, "repository_root", return_value=root), \
                        patch("controller_backend.RealBackend", side_effect=factory), redirect_stdout(io.StringIO()):
                    self.assertEqual(controller.main(), 1)
                self.assertTrue(store.stopped(run["run_id"]))
                arguments.append("--automatic")
                with patch.object(sys, "argv", arguments), patch.object(evidence, "repository_root", return_value=root), \
                        patch("controller_backend.RealBackend") as forbidden_setup, redirect_stdout(io.StringIO()) as output:
                    self.assertEqual(controller.main(), 1)
                forbidden_setup.assert_not_called()
                self.assertEqual(json.loads(output.getvalue())["status"], "STOPPED")
                for number, handler in previous.items():
                    self.assertIs(signal.getsignal(number), handler)
                for heartbeat in (store.directory / "commands").glob("*/heartbeat.json"):
                    self.assertFalse(session_runner.live_group_members(controller.read_json(heartbeat)["process_group"]))

    def test_new_signal_during_prior_stop_acknowledgement_is_not_cleared(self):
        for number in (signal.SIGTERM, signal.SIGINT):
            for boundary in ("stop_generation", "clear_stop"):
                with self.subTest(number=number, boundary=boundary), tempfile.TemporaryDirectory() as temporary:
                    store = Store(Path(temporary) / "state")
                    with store.lock():
                        run = Controller(store, FakeBackend()).create(graph(), {})
                        store.stop(run["run_id"])
                        original = getattr(store, boundary)
                        def interrupt(*args):
                            mask = signal.pthread_sigmask(signal.SIG_BLOCK, set())
                            self.assertIn(number, mask, "acknowledgement must defer signals before observing a generation")
                            os.kill(os.getpid(), number)
                            return original(*args)
                        with patch.object(store, boundary, side_effect=interrupt):
                            with controller.stop_signals(store, run["run_id"], acknowledge=True):
                                self.assertTrue(store.stopped(run["run_id"]), "new pending signal was erased by prior-stop acknowledgement")

    def test_ordinary_command_restores_handlers_on_normal_timeout_and_install_failure(self):
        for outcome in ("normal", "timeout", "install-failure"):
            with self.subTest(outcome=outcome), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve(); store = Store(root / "state")
                backend = RealBackend.__new__(RealBackend); backend.root, backend.store = root, store
                previous = {number: signal.getsignal(number) for number in (signal.SIGTERM, signal.SIGINT)}
                if outcome == "install-failure":
                    original = signal.signal
                    def install(number, handler):
                        if number == signal.SIGINT and getattr(handler, "__name__", None) == "interrupt":
                            raise ValueError("injected second-handler installation failure")
                        return original(number, handler)
                    with store.lock(), patch.object(signal, "signal", side_effect=install), self.assertRaises(Blocked):
                        backend.command([sys.executable, "-c", "print('must not start')"])
                    self.assertFalse(list((store.directory / "commands").glob("*/started.json")))
                elif outcome == "timeout":
                    with store.lock(), self.assertRaises(Blocked):
                        backend.command([sys.executable, "-c", "import time; time.sleep(0.5)"], timeout=0.05)
                else:
                    with store.lock():
                        self.assertEqual(backend.command([sys.executable, "-c", "print('bounded normal completion')"]).returncode, 0)
                for number, handler in previous.items():
                    self.assertIs(signal.getsignal(number), handler)

    def test_caller_interrupt_exception_is_deferred_until_cleanup_and_handlers_restore(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve(); store = Store(root / "state")
            backend = RealBackend.__new__(RealBackend); backend.root, backend.store = root, store
            previous = signal.getsignal(signal.SIGTERM)
            received = []
            def caller(number, frame):
                received.append(number)
                raise RuntimeError("caller interruption exception")
            signal.signal(signal.SIGTERM, caller)
            try:
                with store.lock(), self.assertRaisesRegex(RuntimeError, "caller interruption"):
                    backend.command([sys.executable, "-c", "import os,signal,time; os.kill(os.getppid(),signal.SIGTERM); time.sleep(0.5)"])
                self.assertEqual(received, [signal.SIGTERM])
                self.assertIs(signal.getsignal(signal.SIGTERM), caller)
                result = next((store.directory / "commands").glob("*/result.json"))
                self.assertTrue(controller.read_json(result)["cleanup_complete"])
                heartbeat = controller.read_json(result.parent / "heartbeat.json")
                self.assertFalse(session_runner.live_group_members(heartbeat["process_group"]))
            finally:
                signal.signal(signal.SIGTERM, previous)


if __name__ == "__main__":
    unittest.main()
