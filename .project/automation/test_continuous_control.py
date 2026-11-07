"""Synthetic D93 policy tests; these are not native build or runtime evidence."""

import copy
from pathlib import Path
import sys
import time
import unittest
from unittest.mock import patch

import controller
from controller import AUTHORIZATIONS, Blocked
from controller_backend import RealBackend
import continuous_control
import evidence
from launch_control import LaunchLedger, remaining
from test_controller import Crash, graph
import test_launch_control as fixtures

TASK = fixtures.TASK


class ContinuousTests(unittest.TestCase):
    setUp = fixtures.LaunchTests.setUp
    create = fixtures.LaunchTests.create
    stopped = fixtures.LaunchTests.stopped

    def bootstrap(self):
        return {"schema_version": 1, "authorization_ref": "D93", "coordinator_agent_id": "/root",
                "observations": [{"invocation_id": "aut04-" + axis, "canonical_agent_id": "/root/" + axis,
                    "role": "reviewer", "task_id": "AUT-04", "status": "completed", "started_at": self.now - 1,
                    "observed_at": self.now, "completed_at": self.now, "cleanup_confirmed": True,
                    "evidence": "Synthetic completed bootstrap review; no actual provider execution asserted."}
                    for axis in ("standards", "spec")]}

    def prepare(self):
        run = self.stopped()
        self.now = AUTHORIZATIONS["D93"]["started_at"] + 100
        self.backend.source["head_sha"] = "c" * 40
        return run

    def adopt(self, run):
        return continuous_control.adopt(self.driver, run, self.recipes, self.bootstrap(),
                                        run["revision"], run["configuration_epoch"])

    def test_expired_launch_is_adopted_without_resetting_history_or_stop(self):
        with self.store.lock():
            run = self.prepare()
            old = copy.deepcopy(run)
            ledger_bytes = self.ledger.path.read_bytes()
            self.adopt(run)
            self.assertEqual(run["run_id"], old["run_id"])
            self.assertEqual(run["tasks"], old["tasks"])
            self.assertEqual(run["catalog"], old["catalog"])
            self.assertEqual(run["configuration_epoch"], old["configuration_epoch"] + 1)
            self.assertEqual(run["authorization_history"][0]["authorization"], old["authorization"])
            self.assertEqual(run["configuration_history"][0]["source"], old["configuration_source"])
            self.assertEqual(self.ledger.path.read_bytes(), ledger_bytes)
            new_ledger = self.driver.launch(run).load()
            for key, value in self.ledger.load()["sessions"].items():
                self.assertEqual(new_ledger["sessions"][key], value)
            self.assertEqual(run["invocations"], old["invocations"] + 2)
            self.assertIsNone(run["deadline"])
            self.assertEqual(run["limits"]["session_seconds"], old["limits"]["session_seconds"])
            self.assertTrue(self.store.stopped(run["run_id"]))
            report = self.driver.run(run, resume=True, automatic=True)
            self.assertEqual(report["status"], "STOPPED")
            with self.assertRaises(Blocked):
                self.driver.run(run, resume=True, renew_budget=True)

    def test_adoption_crash_before_and_after_atomic_run_save_is_replayable(self):
        for after in (False, True):
            with self.subTest(after=after):
                self.setUp()
                with self.store.lock():
                    run = self.prepare()
                    before = self.store.run_path(run["run_id"]).read_bytes()
                    revision, epoch = run["revision"], run["configuration_epoch"]
                    save = self.store.save
                    def crash(candidate):
                        if after:
                            save(candidate)
                        raise Crash("adoption")
                    with patch.object(self.store, "save", side_effect=crash), self.assertRaises(Crash):
                        self.adopt(run)
                    if not after:
                        self.assertEqual(self.store.run_path(run["run_id"]).read_bytes(), before)
                    run = self.store.load(run["run_id"])
                    continuous_control.adopt(self.driver, run, self.recipes, self.bootstrap(), revision, epoch)
                    self.assertEqual(run["revision"], revision + 1)
                    self.assertEqual(len(run["authorization_history"]), 1)
                    self.assertEqual(run["invocations"], 5)

    def test_adoption_rejects_pending_active_leased_unfinished_or_stale_state(self):
        for defect in ("pending", "active", "lease", "workspace", "revision", "catalog", "bootstrap", "gate"):
            with self.subTest(defect=defect):
                self.setUp()
                with self.store.lock():
                    run = self.prepare()
                    revision, epoch = run["revision"], run["configuration_epoch"]
                    doc = self.bootstrap()
                    if defect == "pending": run["tasks"][TASK]["pending"] = {"id": "unresolved"}
                    if defect == "active":
                        record = self.ledger.load()
                        record["sessions"]["initial-evidence"].update(status="active", cleanup_confirmed=False)
                        self.ledger.save(record)
                    if defect == "lease": self.store.lease(run, run["tasks"][TASK])
                    if defect == "workspace": run["tasks"][TASK]["workspace"] = {"path": "unfinished"}
                    if defect == "revision": revision -= 1
                    if defect == "catalog": run["catalog"][TASK]["snapshot"] = "changed"
                    if defect == "bootstrap": doc["observations"][0]["cleanup_confirmed"] = False
                    if defect == "gate": self.backend.gate_passed = False
                    before = self.store.run_path(run["run_id"]).read_bytes()
                    with self.assertRaises(Blocked):
                        continuous_control.adopt(self.driver, run, self.recipes, doc, revision, epoch)
                    self.assertEqual(self.store.run_path(run["run_id"]).read_bytes(), before)
                    self.assertFalse((self.store.directory / "launches/D93.json").exists())

    def test_prepared_ledger_cannot_be_overwritten_after_a_crash(self):
        with self.store.lock():
            run = self.prepare()
            with patch.object(self.store, "save", side_effect=Crash("before save")), self.assertRaises(Crash):
                self.adopt(run)
            ledger = LaunchLedger(self.store, AUTHORIZATIONS["D93"], lambda: self.now)
            record = ledger.load()
            with self.assertRaises(Blocked):
                ledger.reserve("premature-review", "reviewer", "AUT-04")
            with self.assertRaises(Blocked):
                self.ledger.save(self.ledger.load())
            record["sessions"]["aut04-spec"]["cleanup_confirmed"] = False
            ledger.save(record)
            with self.assertRaises(Blocked): self.adopt(run)
            self.assertEqual(ledger.load(), record)
            self.assertEqual(self.store.load(run["run_id"])["authorization"]["reference"], "D92")

    def test_accepted_recipe_unlocks_only_its_configuration_blocker(self):
        for code, state in (("BLOCKED_UNCONFIGURED_VERIFICATION", "PLANNED"), ("BLOCKED_ACCESS", "BLOCKED")):
            with self.subTest(code=code):
                self.setUp()
                with self.store.lock():
                    run = self.prepare()
                    task = run["tasks"][TASK]
                    task.update(state="BLOCKED", blocker={"code": code, "reason": "Synthetic retained failure"})
                    self.store.save(run)
                    self.adopt(run)
                    task = run["tasks"][TASK]
                    self.assertEqual(task["state"], state)
                    if state == "PLANNED": self.assertEqual(task["blocker_history"][0]["blocker"]["code"], code)
                    else: self.assertEqual(task["blocker"]["code"], code)

    def test_time_invocations_and_attempts_are_records_but_capacity_is_enforced(self):
        with self.store.lock():
            run = self.prepare()
            self.adopt(run)
            self.store.clear_stop(run["run_id"])
            self.now += 86400 * 365
            launch = self.driver.launch(run)
            for index in range(205):
                row = launch.reserve("repair-" + str(index), "implementer", TASK, run["run_id"], limits=run["limits"])
                launch.complete_controller(row["invocation_id"], {"cleanup_complete": True, "outcome": "completed"})
            self.driver.budget(run)
            self.assertEqual(run["invocations"], 210)
            self.assertEqual(run["tasks"][TASK]["attempts"], 206)
            self.assertEqual(remaining(run, self.now), float("inf"))
            launch.reserve("review-one", "reviewer", TASK)
            launch.reserve("review-two", "reviewer", TASK)
            with self.assertRaises(Blocked) as caught: launch.reserve("review-three", "reviewer", TASK)
            self.assertEqual(caught.exception.code, "BLOCKED_SESSION_CAPACITY")
            self.store.stop(run["run_id"])
            with self.assertRaises(Blocked) as caught: self.driver.budget(run)
            self.assertEqual(caught.exception.code, "STOPPED")

    def test_d93_cannot_start_a_fresh_run_or_fake_a_continuous_authorization(self):
        with self.store.lock(), self.assertRaises(Blocked):
            self.driver.create(graph(), self.recipes, authorization_ref="D93")
        with self.assertRaises(Blocked):
            remaining({"authorization": dict(AUTHORIZATIONS["D93"], stage_limit="BOOT"), "deadline": None}, self.now)
        with self.assertRaises(Blocked):
            LaunchLedger(self.store, AUTHORIZATIONS["D93"]).load()

    def test_changed_candidates_can_complete_after_more_than_three_attempts(self):
        with self.store.lock():
            run = self.prepare()
            self.adopt(run)
            self.backend.review_changes = 4
            perform = self.backend.perform
            def changed(action, current, task, operation):
                result = perform(action, current, task, operation)
                if action == "worker": result["commit_sha"] = format(task["attempts"], "040x")
                if action == "review": result["head_sha"] = task["worker"]["commit_sha"]
                return result
            with patch.object(self.backend, "perform", side_effect=changed):
                report = self.driver.run(run, resume=True)
            self.assertEqual(report["status"], "COMPLETE")
            self.assertGreater(run["tasks"][TASK]["attempts"], 3)
            self.assertEqual(len(run["tasks"][TASK]["review_history"]), 4)
            self.assertEqual(run["tasks"][TASK]["state"], "DONE")

    def test_repeated_unchanged_failure_stops_only_that_task_for_diagnosis(self):
        with self.store.lock():
            run = self.prepare()
            self.adopt(run)
            self.backend.review_changes = 4
            report = self.driver.run(run, resume=True)
            task = report["tasks"][TASK]
            self.assertEqual(task["blocker"]["code"], "BLOCKED_NO_PROGRESS")
            self.assertEqual(len(task["review_history"]), 2)
            self.assertNotIn("receipt", task)

    def test_finite_command_timeout_still_kills_real_descendants(self):
        backend = RealBackend.__new__(RealBackend)
        backend.root, backend.store, backend.launch_bound = self.root, self.store, None
        backend.bind_launch(AUTHORIZATIONS["D93"])
        marker = self.root / "escaped-child"
        child = "import time; from pathlib import Path; time.sleep(.7); Path(" + repr(str(marker)) + ").write_text('escaped')"
        parent = "import subprocess,sys,time; subprocess.Popen([sys.executable,'-c'," + repr(child) + "]); time.sleep(2)"
        with self.store.lock(), self.assertRaises(Blocked):
            backend.command([sys.executable, "-c", parent], timeout=.25)
        time.sleep(.9)
        self.assertFalse(marker.exists())
        request = controller.read_json(next((self.store.directory / "commands").glob("*/request.json")))
        self.assertGreater(request["timeout_seconds"], 0)
        self.assertLessEqual(request["timeout_seconds"], .25)
        self.assertIsInstance(request["deadline"], float)

    def test_invalid_technical_timeouts_are_rejected_before_dispatch(self):
        backend = RealBackend.__new__(RealBackend)
        for value in (None, -1, 0, True, float("inf"), float("nan")):
            with self.subTest(timeout=value), patch("controller_backend.subprocess.Popen") as spawn:
                with self.assertRaises(Blocked): backend.command(["unused"], timeout=value)
                with self.assertRaises(Blocked): backend.supervised({}, "unused", ["unused"], self.root, timeout=value)
                spawn.assert_not_called()

    def test_model_and_verifier_requests_keep_finite_deadlines(self):
        with self.store.lock():
            run = self.prepare()
            self.adopt(run)
            self.store.clear_stop(run["run_id"])
            backend = RealBackend.__new__(RealBackend)
            backend.root, backend.store, backend.launch_bound = self.root, self.store, None
            backend.source = Path(controller.__file__).resolve().parents[2]
            for name, timeout in (("model", 3600), ("verifier", 7200)):
                with patch("controller_backend.subprocess.Popen", side_effect=Crash("before dispatch")), self.assertRaises(Crash):
                    backend.supervised(run, name, [sys.executable, "-c", "pass"], self.root, timeout=timeout)
                path = self.root / ".project/automation/runs" / run["run_id"] / name / "request.json"
                request = controller.read_json(path)
                self.assertEqual(request["timeout_seconds"], timeout)
                self.assertLessEqual(request["deadline"], time.time() + timeout)
                self.assertGreater(request["deadline"], time.time())


if __name__ == "__main__":
    unittest.main()
