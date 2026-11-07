"""Check service CLI recovery and real retained process results without models."""

from contextlib import redirect_stdout
import copy
import io
import json
from pathlib import Path
import plistlib
import shlex
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

import controller
from controller import Blocked, Controller, Store
from controller_backend import RealBackend
import evidence
import session_runner
from test_controller import Crash, FakeBackend, graph


class SessionBackend(FakeBackend):
    """Use the real session adapter, with fake task checks and remote effects."""
    def __init__(self, root, store, outcome):
        super().__init__()
        self.real = RealBackend.__new__(RealBackend)
        self.real.root, self.real.store = root, store
        self.real.source = Path(controller.__file__).resolve().parents[2]
        self.outcome = outcome
        self.first = True

    def session_recovery(self, run, task, operation):
        return self.real.session_recovery(run, task, operation)

    def perform(self, action, run, task, operation):
        if action == "worker":
            command = [sys.executable, "-c", "print('bounded local process')"]
            if self.first:
                self.first = False
                if self.outcome == "stopped":
                    # Run the actual supervisor directly so it records the stop
                    # before the parent watchdog can race its durable result.
                    directory = self.real.directory(run, operation)
                    evidence.durable_directory(directory)
                    (directory / "stdin.txt").write_text("")
                    request = {"operation_id": operation["id"], "command": command,
                               "cwd": str(self.real.root), "deadline": run["deadline"],
                               "timeout_seconds": 10, "log_limit": session_runner.LOG_LIMIT,
                               "stop_file": str(self.real.store.directory / "stop" / (run["run_id"] + ".json")),
                               "env": {}, "stdin_sha256": session_runner.file_hash(directory / "stdin.txt")}
                    evidence.atomic_json(directory / "request.json", request)
                    self.real.store.stop(run["run_id"])
                    session_runner.supervise(directory / "request.json", self.real.store.lock_fd)
                else:
                    command = [sys.executable, "-c", "raise SystemExit(7)"]
            self.real.supervised(run, operation["id"], command, self.real.root, timeout=10)
        return super().perform(action, run, task, operation)


class RecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.store = Store(self.root / ".project/automation/state")
        self.tasks = graph()
        self.recipes = {"BOOT-03": {"verification": "explicit"}, "BOOT-09": {"verification": "explicit"}}

    def service_arguments(self):
        directory = Path(controller.__file__).parent
        linux = next(line.split("=", 1)[1] for line in (directory / "controller.service").read_text().splitlines()
                     if line.startswith("ExecStart="))
        mac = plistlib.loads((directory / "controller.launchd.plist").read_bytes())["ProgramArguments"]
        return [shlex.split(linux)[2:], mac[5:]]

    def call_cli(self, arguments, backend):
        with patch.object(sys, "argv", ["controller.py", *arguments]), \
                patch.object(evidence, "repository_root", return_value=self.root), \
                patch("controller_backend.RealBackend", return_value=backend), redirect_stdout(io.StringIO()) as output:
            code = controller.main()
        return code, json.loads(output.getvalue())

    @staticmethod
    def configure_service(arguments, run):
        values = {"RECORDED_RUN_ID": run["run_id"], "RECORDED_AUTHORIZATION_REF": run["authorization"]["reference"],
                  "RECORDED_STAGE_LIMIT": run["authorization"]["stage_limit"]}
        return [values.get(value, value) for value in arguments]

    def test_service_templates_preserve_stop_and_budget_through_actual_cli(self):
        for arguments in self.service_arguments():
            for status, marker in (("STOPPED", True), ("STOPPED", False), ("RUNNING", True),
                                   ("BUDGET_STOP", False), ("BLOCKED", False)):
                with self.subTest(arguments=arguments, status=status, marker=marker):
                    backend = FakeBackend()
                    with self.store.lock():
                        run = Controller(self.store, backend).create(self.tasks, self.recipes)
                        run["status"] = status
                        self.store.save(run)
                    if marker:
                        self.store.stop(run["run_id"])
                    before = self.store.load(run["run_id"])
                    actual = self.configure_service(arguments, run)
                    with patch.object(backend, "start_gate") as gate:
                        code, report = self.call_cli(actual, backend)
                    gate.assert_not_called()
                    after = self.store.load(run["run_id"])
                    self.assertEqual(code, 1)
                    self.assertEqual(report["status"], "STOPPED" if marker else status)
                    self.assertEqual(self.store.stopped(run["run_id"]), marker)
                    for key in ("invocations", "budget_baseline", "budget_history", "deadline", "tasks"):
                        self.assertEqual(after[key], before[key])
                    self.assertFalse(backend.effects)
                    self.assertFalse(backend.sessions)

    def test_automatic_recovery_cannot_grant_budget_or_session_retry(self):
        backend = FakeBackend()
        with self.store.lock():
            run = Controller(self.store, backend).create(self.tasks, self.recipes)
        for option in ("--renew-budget", "--retry-session"):
            with self.subTest(option=option):
                _, report = self.call_cli(["resume", "--run-id", run["run_id"], "--automatic", option], backend)
                self.assertEqual(report["code"], "BLOCKED_RECOVERY_MODE")
        self.assertFalse(backend.effects)

    def test_service_cli_can_recover_a_running_crash_without_changing_authorization(self):
        backend = FakeBackend()
        with self.store.lock():
            run = Controller(self.store, backend).create(self.tasks, self.recipes)
            backend.crash = "after:pr"
            with self.assertRaises(Crash):
                Controller(self.store, backend).run(run)
        arguments = self.configure_service(self.service_arguments()[0], run)
        code, report = self.call_cli(arguments, backend)
        self.assertEqual(code, 0)
        self.assertEqual(report["status"], "COMPLETE")
        self.assertEqual(report["authorization"], controller.AUTHORIZATION)
        self.assertEqual(report["invocations"], 4)
        self.assertTrue(all(count == 1 for count in backend.effects.values()))

    def test_explicit_resume_replaces_clean_stopped_result_and_preserves_history(self):
        backend = SessionBackend(self.root, self.store, "stopped")
        driver = Controller(self.store, backend)
        with self.store.lock():
            run = driver.run(driver.create(self.tasks, self.recipes))
            self.assertEqual(run["status"], "STOPPED")
            old = copy.deepcopy(run["tasks"]["BOOT-03"]["pending"])
            result_path = backend.real.directory(run, old) / "result.json"
            original = result_path.read_bytes()
            resumed = driver.run(self.store.load(run["run_id"]), resume=True)
        self.assertEqual(resumed["status"], "COMPLETE")
        task = resumed["tasks"]["BOOT-03"]
        self.assertEqual(task["attempts"], 2)
        self.assertEqual(resumed["invocations"], 5)
        history = task["session_history"][0]
        self.assertEqual(history["operation"]["id"], old["id"])
        self.assertNotEqual(history["replacement_id"], old["id"])
        self.assertEqual(history["evidence"]["outcome"], "stopped")
        self.assertEqual(result_path.read_bytes(), original)

    def test_failed_process_requires_explicit_diagnostic_retry_and_keeps_failure(self):
        backend = SessionBackend(self.root, self.store, "failed")
        driver = Controller(self.store, backend)
        with self.store.lock():
            run = driver.run(driver.create(self.tasks, self.recipes))
            task = run["tasks"]["BOOT-03"]
            self.assertEqual(task["blocker"]["code"], "FAILED_SESSION")
            old = copy.deepcopy(task["pending"])
            result_path = backend.real.directory(run, old) / "result.json"
            original = result_path.read_bytes()
            request = json.loads((result_path.parent / "request.json").read_text())
            with self.assertRaises(Blocked) as replay:
                backend.real.supervised(run, old["id"], request["command"], self.root)
            self.assertEqual(replay.exception.code, "FAILED_SESSION")
            run = driver.run(self.store.load(run["run_id"]), resume=True)
            self.assertEqual(run["blocker"]["code"], "BLOCKED_SESSION_RETRY_REQUIRED")
            self.assertEqual(run["invocations"], 1)
            resumed = driver.run(self.store.load(run["run_id"]), resume=True, retry_session=True)
        self.assertEqual(resumed["status"], "COMPLETE")
        self.assertEqual(resumed["invocations"], 5)
        task = resumed["tasks"]["BOOT-03"]
        self.assertEqual(task["diagnostic_retries"], 1)
        self.assertEqual(task["session_history"][0]["evidence"]["outcome"], "failed")
        self.assertEqual(result_path.read_bytes(), original)

    def test_failed_model_turn_with_successful_process_still_requires_diagnostic_retry(self):
        backend = SessionBackend(self.root, self.store, "failed")
        driver = Controller(self.store, backend)
        with self.store.lock():
            run = driver.create(self.tasks, self.recipes)
            task = run["tasks"]["BOOT-03"]
            task["state"] = "IMPLEMENTING"
            self.store.lease(run, task)
            operation = driver.intent(run, task, "worker")
            backend.real.supervised(run, operation["id"], [sys.executable, "-c", "print('turn.failed')"], self.root)
            operation["failure"] = {"code": "FAILED_SESSION", "reason": "model did not return a completed turn"}
            self.store.save(run)
            resumed = driver.run(self.store.load(run["run_id"]), resume=True)
        self.assertEqual(resumed["blocker"]["code"], "BLOCKED_SESSION_RETRY_REQUIRED")
        self.assertEqual(resumed["invocations"], 1)

    def test_unknown_or_changed_cleanup_proof_cannot_launch_replacement(self):
        for fault in ("missing-cleanup", "uncertain", "missing-result", "watchdog", "log-changed"):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                store = Store(root / "state")
                backend = SessionBackend(root, store, "stopped")
                driver = Controller(store, backend)
                with store.lock():
                    run = driver.run(driver.create(self.tasks, self.recipes))
                    operation = copy.deepcopy(run["tasks"]["BOOT-03"]["pending"])
                    directory = backend.real.directory(run, operation)
                    path = directory / "result.json"
                    result = json.loads(path.read_text())
                    if fault == "missing-cleanup":
                        result.pop("cleanup_complete")
                        evidence.atomic_json(path, result)
                    elif fault == "uncertain":
                        result.update(cleanup_complete=False, termination="cleanup-denied")
                        evidence.atomic_json(path, result)
                    elif fault == "missing-result":
                        path.unlink()
                    elif fault == "watchdog":
                        evidence.atomic_json(directory / "watchdog.json", {"cleanup_uncertain": True})
                    else:
                        (directory / "stdout.jsonl").write_text("changed")
                    with patch.object(backend.real, "supervised") as launch:
                        resumed = driver.run(store.load(run["run_id"]), resume=True, retry_session=True)
                    launch.assert_not_called()
                self.assertEqual(resumed["status"], "BLOCKED")
                self.assertEqual(resumed["tasks"]["BOOT-03"]["pending"], operation)
                self.assertEqual(resumed["invocations"], 1)
                self.assertNotIn("session_history", resumed["tasks"]["BOOT-03"])

    def test_retry_intent_crash_preserves_single_reservation_and_link(self):
        for boundary in ("before-save", "after-save"):
            with self.subTest(boundary=boundary), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                store = Store(root / "state")
                backend = SessionBackend(root, store, "stopped")
                driver = Controller(store, backend)
                with store.lock():
                    run = driver.run(driver.create(self.tasks, self.recipes))
                    store.clear_stop(run["run_id"])
                    save = store.save

                    def crash_save(current):
                        if boundary == "after-save":
                            save(current)
                        raise Crash(boundary)

                    with patch.object(store, "save", side_effect=crash_save), self.assertRaises(Crash):
                        driver.recover_sessions(run, False)
                    resumed = driver.run(store.load(run["run_id"]), resume=True)
                self.assertEqual(resumed["status"], "COMPLETE")
                self.assertEqual(resumed["invocations"], 5)
                self.assertEqual(resumed["tasks"]["BOOT-03"]["attempts"], 2)
                self.assertEqual(len(resumed["tasks"]["BOOT-03"]["session_history"]), 1)

    def test_session_replacement_preserves_attempt_invocation_and_diagnostic_limits(self):
        for limit in ("attempts", "invocations", "diagnostic"):
            with self.subTest(limit=limit), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                store = Store(root / "state")
                backend = SessionBackend(root, store, "failed" if limit == "diagnostic" else "stopped")
                driver = Controller(store, backend)
                with store.lock():
                    run = driver.run(driver.create(self.tasks, self.recipes))
                    task = run["tasks"]["BOOT-03"]
                    if limit == "attempts":
                        task["attempts"] = run["limits"]["implementation_attempts"]
                    elif limit == "invocations":
                        run["invocations"] = run["limits"]["invocations"]
                    else:
                        task["diagnostic_retries"] = 1
                    store.save(run)
                    before = copy.deepcopy(run)
                    with patch.object(backend.real, "supervised") as launch:
                        resumed = driver.run(store.load(run["run_id"]), resume=True, retry_session=True)
                    launch.assert_not_called()
                self.assertIn(resumed["status"], {"BLOCKED", "BUDGET_STOP"})
                self.assertEqual(resumed["invocations"], before["invocations"])
                self.assertEqual(resumed["tasks"]["BOOT-03"]["attempts"], before["tasks"]["BOOT-03"]["attempts"])
                self.assertNotIn("session_history", resumed["tasks"]["BOOT-03"])

    def test_failed_verification_cannot_be_cleared_by_model_diagnostic_retry(self):
        backend = SessionBackend(self.root, self.store, "failed")
        driver = Controller(self.store, backend)
        self.recipes["BOOT-03"]["commands"] = [[sys.executable, "-c", "raise SystemExit(7)"]]
        with self.store.lock():
            run = driver.create(self.tasks, self.recipes)
            task = run["tasks"]["BOOT-03"]
            task["state"] = "VERIFYING"
            self.store.lease(run, task)
            operation = driver.intent(run, task, "verify")
            with self.assertRaises(Blocked):
                backend.real.supervised(run, operation["id"] + "-check-0", self.recipes["BOOT-03"]["commands"][0], self.root)
            with patch.object(backend.real, "supervised") as launch:
                resumed = driver.run(self.store.load(run["run_id"]), resume=True, retry_session=True)
            launch.assert_not_called()
        self.assertEqual(resumed["blocker"]["code"], "BLOCKED_VERIFICATION_FAILURE")
        self.assertEqual(resumed["tasks"]["BOOT-03"]["pending"], operation)
        self.assertFalse(backend.effects)


if __name__ == "__main__":
    unittest.main()
