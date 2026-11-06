"""Isolated D92 accounting, crash boundaries, and real-process deadline checks."""

import copy
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

import controller
from controller import AUTHORIZATIONS, Blocked, Controller, Store
from controller_backend import RealBackend
import controller_handoff
import evidence
from launch_control import LaunchLedger
import session_runner
import task_proofs
from test_controller import Crash, FakeBackend, graph

AUTH = AUTHORIZATIONS["D92"]
TASK = "E01-01.evidence"


def observations(now, active=False):
    rows = []
    for key, agent, role, task in (("initial-root", "/root", "coordinator", None),
            ("initial-maintenance", "/root/maintenance", "implementer", "AUT-01"),
            ("initial-evidence", "/root/evidence", "implementer", TASK)):
        running = role == "coordinator" or active
        rows.append({"invocation_id": key, "canonical_agent_id": agent, "role": role, "task_id": task,
                     "status": "active" if running else "completed", "started_at": None,
                     "observed_at": now, "completed_at": None if running else now,
                     "cleanup_confirmed": not running, "evidence": "Synthetic observed collaboration turn; no provider execution asserted."})
    return {"schema_version": 1, "authorization_ref": "D92", "coordinator_agent_id": "/root", "observations": rows}


class LaunchBackend(FakeBackend):
    def __init__(self):
        super().__init__()
        self.source = {"head_sha": "a" * 40, "files": {"synthetic.py": "first"}}
        self.accepted = {task: {"task_id": task, "proof": "verified-merge"} for task in ("BOOT-03", "BOOT-09")}

    def configuration(self):
        return copy.deepcopy(self.source)

    def check_configuration(self, run):
        if run["configuration_source"] != self.source:
            raise Blocked("BLOCKED_CONFIGURATION", "synthetic source changed")

    def eligible_recipe(self, task_id, recipes):
        return task_id == TASK and recipes.get(task_id) == {"verification": "synthetic independent check"}

    def session_recovery(self, run, task, operation, include_completed=False):
        if include_completed and operation["id"] in self.results:
            return {"operation_id": operation["id"], "outcome": "completed", "cleanup_complete": True,
                    "evidence": "Isolated fake process ownership; not product evidence."}
        return None

    def external_candidate(self, run, task, document):
        if document["head_sha"] != "a" * 40 or document["base_sha"] != "b" * 40:
            raise Blocked("BLOCKED_HEAD_CHANGED", "synthetic Git comparison rejected candidate")
        return {"commit_sha": document["head_sha"], "changed_files": ["test/evidence/synthetic"], "evidence": ["synthetic"]}


class LaunchTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.store = Store(self.root / "state")
        self.now = AUTH["started_at"] + 100
        self.backend = LaunchBackend()
        self.driver = Controller(self.store, self.backend, lambda: self.now)
        self.ledger = LaunchLedger(self.store, AUTH, lambda: self.now)
        self.recipes = {TASK: {"verification": "synthetic independent check"}}

    def create(self, active=False, recipes=None):
        self.ledger.import_observations(observations(self.now, active))
        return self.driver.create(graph(), self.recipes if recipes is None else recipes, authorization_ref="D92")

    def stopped(self):
        run = self.create()
        self.driver.reconcile(run)
        run["status"] = "STOPPED"
        self.store.save(run)
        self.store.stop(run["run_id"])
        return run

    def test_initial_accounting_is_late_honest_idempotent_and_conflict_checked(self):
        with self.store.lock():
            doc = observations(self.now, active=True)
            first = self.ledger.import_observations(doc)
            self.assertEqual(first, self.ledger.import_observations(doc))
            self.assertEqual(self.ledger.used(first), 3)
            self.assertTrue(all(row["accounting"] == "recorded-after-start" and row["started_at"] is None for row in first["sessions"].values()))
            changed = copy.deepcopy(doc)
            changed["observations"][1]["canonical_agent_id"] = "/root/impostor"
            with self.assertRaises(Blocked):
                self.ledger.import_observations(changed)
            self.assertEqual(first, self.ledger.load())

    def test_future_dispatch_requires_reservation_and_capacity(self):
        with self.store.lock():
            with self.assertRaises(Blocked):
                self.ledger.reserve("future-one", "reviewer", "AUT-01")
            self.ledger.import_observations(observations(self.now, active=True))
            with self.assertRaises(Blocked) as caught:
                self.ledger.reserve("future-one", "reviewer", "AUT-01")
            self.assertEqual(caught.exception.code, "BLOCKED_SESSION_CAPACITY")
            self.ledger.import_observations(observations(self.now))
            row = self.ledger.reserve("future-one", "reviewer", "AUT-01", origin="coordinator-managed")
            self.assertEqual(row, self.ledger.reserve("future-one", "reviewer", "AUT-01", origin="coordinator-managed"))
            self.assertEqual(self.ledger.used(self.ledger.load()), 4)
            with self.assertRaises(Blocked):
                self.ledger.reserve("future-one", "implementer", TASK, origin="coordinator-managed")

    def test_first_import_includes_extra_preinstallation_review_exactly_once(self):
        with self.store.lock():
            doc = observations(self.now)
            row = dict(doc["observations"][-1], invocation_id="manual-review-four", canonical_agent_id="/root/independent_review",
                       role="reviewer", task_id="AUT-01", evidence="Synthetic manually reserved pre-installation review observation.")
            doc["observations"].append(row)
            imported = self.ledger.import_observations(doc)
            self.assertEqual(self.ledger.used(imported), 4)
            self.assertEqual(imported, self.ledger.import_observations(doc))
            self.assertEqual(imported["sessions"]["manual-review-four"]["accounting"], "recorded-after-start")
            later = copy.deepcopy(doc)
            later["observations"][-1]["invocation_id"] = "unreserved-later-review"
            with self.assertRaises(Blocked):
                self.ledger.import_observations(later)
            self.assertEqual(self.ledger.used(self.ledger.load()), 4)

    def test_fixed_deadline_includes_setup_and_prevents_new_run_or_renewal(self):
        with self.store.lock():
            run = self.create()
            self.assertEqual(run["deadline"], AUTH["hard_deadline"])
            with self.assertRaises(Blocked):
                self.driver.create(graph(), self.recipes, authorization_ref="D92")
            run["status"] = "BUDGET_STOP"
            with self.assertRaises(Blocked):
                self.driver.run(run, resume=True, renew_budget=True)
            before = copy.deepcopy(run)
            self.now = AUTH["hard_deadline"]
            run["deadline"] += 10000
            with self.assertRaises(Blocked) as caught:
                self.driver.run(run, resume=True)
            self.assertEqual(caught.exception.code, "BUDGET_STOP")
            self.assertEqual(run["budget_history"], before["budget_history"])
            self.assertFalse(self.backend.effects)

    def test_setup_crossing_deadline_creates_no_run(self):
        original = self.backend.start_gate
        def expire(tasks):
            result = original(tasks)
            self.now = AUTH["hard_deadline"]
            return result
        self.backend.start_gate = expire
        with self.store.lock(), self.assertRaises(Blocked):
            self.create()
        self.assertFalse((self.store.directory / "runs").exists())

    def test_shared_200_ceiling_and_global_attempts_survive_configurations(self):
        with self.store.lock():
            self.ledger.import_observations(observations(self.now))
            record = self.ledger.load()
            for number in range(197):
                record["sessions"]["completed-" + str(number)] = {"invocation_id": "completed-" + str(number),
                    "role": "reviewer", "task_id": "AUT-01", "status": "completed", "cleanup_confirmed": True}
            self.ledger.save(record)
            with self.assertRaises(Blocked) as caught:
                self.ledger.reserve("too-many", "reviewer", "AUT-01")
            self.assertEqual(caught.exception.code, "BUDGET_STOP")
            self.assertEqual(self.ledger.used(self.ledger.load()), 200)

    def test_attempt_ceiling_and_final_reserved_invocation_can_finish(self):
        with self.store.lock():
            self.ledger.import_observations(observations(self.now))
            for number in range(2):
                key = "implementation-" + str(number)
                self.ledger.reserve(key, "implementer", TASK)
                self.ledger.complete_controller(key, {"cleanup_complete": True, "synthetic": key})
            with self.assertRaises(Blocked) as caught:
                self.ledger.reserve("fourth-implementation", "implementer", TASK)
            self.assertEqual(caught.exception.code, "BLOCKED_ATTEMPTS")
        with tempfile.TemporaryDirectory() as temporary:
            store = Store(Path(temporary) / "state"); backend = LaunchBackend()
            driver = Controller(store, backend, lambda: self.now)
            with store.lock():
                LaunchLedger(store, AUTH, lambda: self.now).import_observations(observations(self.now))
                run = driver.create(graph(), self.recipes, limits={"invocations": 5}, authorization_ref="D92")
                driver.run(run)
            self.assertEqual(run["status"], "COMPLETE")
            self.assertEqual(run["invocations"], 5)

    def test_external_active_owner_blocks_prepare_without_creating_work_or_intent(self):
        with self.store.lock():
            run = self.create(active=True)
            self.driver.run(run)
            task = run["tasks"][TASK]
            self.assertEqual(task["blocker"]["code"], "BLOCKED_EXTERNAL_SESSION")
            self.assertNotIn("pending", task)
            self.assertNotIn("lease", task)
            self.assertFalse(self.backend.effects)

    def test_observations_cannot_forge_cleanup_for_a_supervised_reservation(self):
        with self.store.lock():
            self.ledger.import_observations(observations(self.now))
            self.ledger.reserve("actual-controller", "reviewer", TASK)
            doc = observations(self.now)
            row = dict(doc["observations"][-1], invocation_id="actual-controller", role="reviewer")
            doc["observations"] = [row]
            with self.assertRaises(Blocked):
                self.ledger.import_observations(doc)
            self.assertEqual(self.ledger.load()["sessions"]["actual-controller"]["status"], "reserved")

    def test_reservation_and_intent_crashes_do_not_double_charge(self):
        for boundary in ("before-reserve", "after-reserve", "after-worker"):
            with self.subTest(boundary=boundary), tempfile.TemporaryDirectory() as temporary:
                store = Store(Path(temporary) / "state")
                backend = LaunchBackend()
                driver = Controller(store, backend, lambda: self.now)
                ledger = LaunchLedger(store, AUTH, lambda: self.now)
                with store.lock():
                    ledger.import_observations(observations(self.now))
                    run = driver.create(graph(), self.recipes, authorization_ref="D92")
                    original = LaunchLedger.reserve
                    once = [False]
                    def reserve(owner, *args, **kwargs):
                        if once[0]:
                            return original(owner, *args, **kwargs)
                        once[0] = True
                        if boundary == "before-reserve":
                            raise Crash("before reservation")
                        result = original(owner, *args, **kwargs)
                        raise Crash("after reservation")
                    if boundary == "after-worker":
                        backend.crash = "after:worker"
                        with self.assertRaises(Crash):
                            driver.run(run)
                    else:
                        with patch.object(LaunchLedger, "reserve", reserve), self.assertRaises(Crash):
                            driver.run(run)
                    recovered = driver.run(store.load(run["run_id"]), resume=True)
                    self.assertEqual(recovered["status"], "COMPLETE")
                    self.assertEqual(recovered["invocations"], 5)
                    self.assertEqual(recovered["tasks"][TASK]["attempts"], 2)
                    self.assertTrue(all(value == 1 for value in backend.effects.values()))

    def test_handoff_retains_budget_history_receipts_and_stop_and_resets_only_eligible(self):
        with self.store.lock():
            run = self.create(recipes={})
            self.driver.run(run)
            self.store.stop(run["run_id"])
            before = copy.deepcopy(run)
            self.backend.source["files"]["synthetic.py"] = "second"
            controller_handoff.configuration(self.driver, run, self.recipes, run["revision"], 1)
            self.assertEqual(run["configuration_epoch"], 2)
            self.assertEqual(run["configuration_history"][0]["source"], before["configuration_source"])
            self.assertEqual(run["configuration_history"][0]["recipes"], {})
            for field in ("authorization", "deadline", "invocations", "budget_history", "limits"):
                self.assertEqual(run[field], before[field])
            self.assertEqual(run["tasks"]["BOOT-09"], before["tasks"]["BOOT-09"])
            self.assertEqual(run["tasks"][TASK]["attempts"], 1)
            self.assertEqual(run["tasks"][TASK]["state"], "PLANNED")
            self.assertTrue(self.store.stopped(run["run_id"]))

    def test_handoff_rejects_active_pending_lease_wrong_revision_and_d86(self):
        with self.store.lock():
            run = self.stopped()
            for defect in ("active", "pending", "lease", "revision", "d86"):
                with self.subTest(defect=defect):
                    candidate = copy.deepcopy(run)
                    revision = candidate["revision"]
                    if defect == "active":
                        self.ledger.reserve("uncertain-session", "reviewer", "AUT-01", origin="coordinator-managed")
                    elif defect == "pending":
                        candidate["tasks"][TASK]["pending"] = {"id": "uncertain", "action": "worker"}
                    elif defect == "lease":
                        self.store.lease(candidate, candidate["tasks"][TASK])
                    elif defect == "revision":
                        revision -= 1
                    else:
                        candidate["authorization"] = dict(controller.AUTHORIZATION)
                    with self.assertRaises(Blocked):
                        controller_handoff.configuration(self.driver, candidate, self.recipes, revision, 1)
                    if defect == "active":
                        record = self.ledger.load(); record["sessions"].pop("uncertain-session"); self.ledger.save(record)
                    if defect == "lease":
                        self.store.release(candidate["tasks"][TASK])

    def test_handoff_crash_retains_one_complete_configuration_epoch(self):
        with self.store.lock():
            run = self.stopped()
            self.backend.source["files"]["synthetic.py"] = "second"
            original = self.store.save
            def crash(record):
                original(record)
                raise Crash("after atomic configuration replacement")
            with patch.object(self.store, "save", crash), self.assertRaises(Crash):
                controller_handoff.configuration(self.driver, run, self.recipes, run["revision"], 1)
            saved = self.store.load(run["run_id"])
            self.assertEqual(saved["configuration_epoch"], 2)
            self.assertEqual(len(saved["configuration_history"]), 1)
            self.assertEqual(saved["invocations"], 3)

    def test_handoff_keeps_unrelated_blockers_and_rejects_unknown_recipe(self):
        with self.store.lock():
            run = self.stopped()
            run["tasks"][TASK].update(state="BLOCKED", blocker={"code": "BLOCKED_VERIFICATION_FAILURE", "reason": "Retained actual FAIL."})
            self.store.save(run)
            self.backend.source["files"]["synthetic.py"] = "second"
            controller_handoff.configuration(self.driver, run, self.recipes, run["revision"], 1)
            self.assertEqual(run["tasks"][TASK]["blocker"]["code"], "BLOCKED_VERIFICATION_FAILURE")
            run["tasks"][TASK]["blocker"]["code"] = "BLOCKED_UNCONFIGURED_VERIFICATION"
            self.store.save(run)
            self.backend.source["files"]["synthetic.py"] = "third"
            controller_handoff.configuration(self.driver, run, {TASK: {"verification": "unreviewed"}}, run["revision"], 2)
            self.assertEqual(run["tasks"][TASK]["state"], "BLOCKED")

    def candidate(self, run, review=True):
        document = {"schema_version": 1, "authorization_ref": "D92", "coordinator_agent_id": "/root",
            "run_id": run["run_id"], "configuration_epoch": run["configuration_epoch"], "task_id": TASK,
            "implementer_invocation": "initial-evidence", "head_sha": "a" * 40, "base_sha": "b" * 40,
            "worktree": str(self.root / ".project/worktrees/evidence"), "branch": "test/evidence"}
        if review:
            self.ledger.reserve("external-review", "reviewer", TASK, run["run_id"], "coordinator-managed")
            doc = observations(self.now)
            doc["observations"] = [{"invocation_id": "external-review", "canonical_agent_id": "/root/reviewer", "role": "reviewer",
                "task_id": TASK, "status": "completed", "started_at": None, "completed_at": self.now, "observed_at": self.now,
                "cleanup_confirmed": True, "evidence": "Synthetic exact-candidate review handoff."}]
            self.ledger.import_observations(doc)
            document["review"] = {"invocation_id": "external-review", "head_sha": "a" * 40, "base_sha": "b" * 40,
                "verdict": "PASS", "checks": ["Synthetic review check."], "findings": [], "unverified": ["No real product execution."]}
        return document

    def test_external_candidate_enters_real_verification_state_without_fake_execution(self):
        with self.store.lock():
            run = self.stopped()
            document = self.candidate(run)
            controller_handoff.adopt(self.driver, run, document, run["revision"], 1)
            task = run["tasks"][TASK]
            self.assertEqual(task["state"], "VERIFYING")
            self.assertEqual(task["worker"]["canonical_agent_id"], "/root/evidence")
            self.assertEqual(task["review"]["canonical_agent_id"], "/root/reviewer")
            self.assertNotIn("execution", task["worker"])
            self.assertNotIn("exit_code", task["worker"])
            self.assertNotIn("verification", task)
            self.assertEqual(run["invocations"], 4)
            self.assertFalse(self.backend.effects)
            self.assertTrue(self.store.stopped(run["run_id"]))

    def test_adoption_rejects_same_agent_foreign_task_stale_head_and_uncertain_cleanup(self):
        with self.store.lock():
            run = self.stopped()
            document = self.candidate(run)
            for defect in ("same-agent", "foreign-task", "head", "cleanup", "source"):
                with self.subTest(defect=defect):
                    record = self.ledger.load(); altered = copy.deepcopy(document)
                    old_source = copy.deepcopy(self.backend.source)
                    if defect == "same-agent":
                        record["sessions"]["external-review"]["canonical_agent_id"] = "/root/evidence"
                    elif defect == "foreign-task":
                        record["sessions"]["external-review"]["task_id"] = "AUT-01"
                    elif defect == "head":
                        altered["head_sha"] = "c" * 40
                    elif defect == "cleanup":
                        record["sessions"]["initial-evidence"]["cleanup_confirmed"] = False
                    else:
                        self.backend.source["files"]["synthetic.py"] = "drift"
                    prior = self.ledger.load(); self.ledger.save(record)
                    with self.assertRaises(Blocked):
                        controller_handoff.adopt(self.driver, run, altered, run["revision"], 1)
                    self.ledger.save(prior); self.backend.source = old_source
            self.assertFalse(self.backend.effects)

    def test_adoption_after_lease_before_save_recovers_without_reset_or_duplicate_charge(self):
        with self.store.lock():
            run = self.stopped(); document = self.candidate(run)
            with patch.object(self.store, "save", side_effect=Crash("before state commit")), self.assertRaises(Crash):
                controller_handoff.adopt(self.driver, run, document, run["revision"], 1)
            saved = self.store.load(run["run_id"])
            controller_handoff.adopt(self.driver, saved, document, saved["revision"], 1)
            self.assertEqual(saved["invocations"], 4)
            self.assertEqual(saved["tasks"][TASK]["attempts"], 1)
            self.assertEqual(len(saved["tasks"][TASK]["external_attestations"]), 1)

    def test_cli_initial_observations_import_uses_no_backend_and_can_be_replayed(self):
        path = self.root / "observations.json"
        evidence.atomic_json(path, observations(time.time()))
        arguments = ["controller.py", "account-sessions", "--authorization-ref", "D92", "--stage-limit", "E07", "--input", str(path)]
        with patch.object(sys, "argv", arguments), patch.object(evidence, "repository_root", return_value=self.root), \
                patch("controller_backend.RealBackend") as backend, redirect_stdout(io.StringIO()) as output:
            self.assertEqual(controller.main(), 0)
            self.assertEqual(controller.main(), 0)
        backend.assert_not_called()
        self.assertTrue(all(json.loads(line)["used"] == 3 for line in output.getvalue().splitlines()))

    def test_actual_cli_initialize_never_dispatches_and_automatic_resume_preserves_stop(self):
        canonical = Store(self.root / ".project/automation/state")
        with canonical.lock():
            LaunchLedger(canonical, AUTH, lambda: self.now).import_observations(observations(self.now))
        arguments = ["controller.py", "initialize", "--authorization-ref", "D92", "--stage-limit", "E07"]
        with patch.object(evidence, "repository_root", return_value=self.root), \
                patch("controller_backend.RealBackend", return_value=self.backend), \
                patch.object(controller, "catalog", return_value=graph()), \
                patch.object(controller, "Controller", side_effect=lambda store, backend: Controller(store, backend, lambda: self.now)):
            with patch.object(sys, "argv", arguments), redirect_stdout(io.StringIO()) as output:
                self.assertEqual(controller.main(), 0)
            report = json.loads(output.getvalue())
            self.assertEqual(report["status"], "STOPPED")
            self.assertEqual(report["invocations"], 3)
            arguments[1] = "resume"
            arguments.extend(["--run-id", report["run_id"], "--automatic"])
            with patch.object(sys, "argv", arguments), redirect_stdout(io.StringIO()) as output:
                self.assertEqual(controller.main(), 1)
            self.assertEqual(json.loads(output.getvalue())["status"], "STOPPED")
            self.assertTrue(canonical.stopped(report["run_id"]))
            self.assertFalse(self.backend.effects)


class FixedDeadlineProcessTests(unittest.TestCase):
    def test_command_remaining_time_is_bounded_by_fixed_and_monotonic_deadline(self):
        backend = RealBackend.__new__(RealBackend)
        backend.root = Path("/tmp")
        backend.launch_bound = {"authorization": AUTH, "monotonic_deadline": 105}
        with patch("controller_backend.time.time", return_value=AUTH["hard_deadline"] - 2), \
                patch("controller_backend.time.monotonic", return_value=100), \
                patch("controller_backend.subprocess.run", return_value=subprocess.CompletedProcess([], 0, "", "")) as command:
            backend.command(["synthetic"], timeout=30)
            self.assertEqual(command.call_args.kwargs["timeout"], 2)
        with patch("controller_backend.time.time", return_value=AUTH["started_at"]), \
                patch("controller_backend.time.monotonic", return_value=106), self.assertRaises(Blocked):
            backend.command(["synthetic"])

    def test_expired_request_never_spawns_a_worker_and_records_no_exit_code(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); store = Store(root / "state")
            (root / "stdin.txt").write_text("")
            request = {"operation_id": "expired-before-spawn", "deadline": time.time() - 1,
                       "command": [sys.executable, "-c", "raise SystemExit('must not execute')"], "cwd": str(root),
                       "stop_file": str(root / "stop.json"), "log_limit": 8192, "env": {}}
            evidence.atomic_json(root / "request.json", request)
            with store.lock(), patch("session_runner.subprocess.Popen") as spawn:
                result = session_runner.supervise(root / "request.json", store.lock_fd)
            spawn.assert_not_called()
            self.assertEqual(result["termination"], "not-started")
            self.assertIsNone(result["exit_code"])
            self.assertTrue(result["cleanup_complete"])


class ExternalGitTests(unittest.TestCase):
    def test_actual_git_adoption_checks_clean_candidate_paths_and_required_outputs(self):
        """These placeholder files exercise provenance only, never product conformance."""
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            def git(*args, cwd=root):
                return subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True, check=True).stdout.strip()
            git("init", "-q")
            (root / ".gitignore").write_text(".project/worktrees/\n")
            git("add", ".gitignore")
            git("-c", "user.name=Isolated test", "-c", "user.email=test@example.invalid", "commit", "-qm", "base")
            base = git("rev-parse", "HEAD")
            path = root / ".project/worktrees/candidate"
            git("worktree", "add", "-qb", "isolated-candidate", str(path), base)
            source = Path(controller.__file__).resolve().parents[2]
            recipes = task_proofs.snapshot(controller.read_json(source / ".project/automation/task-recipes.json"), source)
            for name in recipes[TASK]["required_files"]:
                target = path / name; target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text("Synthetic provenance fixture, not product evidence.\n")
            git("add", ".", cwd=path)
            git("-c", "user.name=Isolated test", "-c", "user.email=test@example.invalid", "commit", "-qm", "synthetic candidate", cwd=path)
            backend = RealBackend.__new__(RealBackend)
            backend.root, backend.source = root, source
            backend.read = lambda endpoint: {"object": {"sha": base}}
            run = {"recipes": recipes}
            document = {"task_id": TASK, "worktree": str(path), "branch": "isolated-candidate", "base_sha": base,
                        "head_sha": git("rev-parse", "HEAD", cwd=path)}
            result = backend.external_candidate(run, {"task_id": TASK}, document)
            self.assertEqual(result["commit_sha"], document["head_sha"])
            self.assertEqual(set(result["evidence"]), set(recipes[TASK]["required_files"]))
            (path / "uncommitted.txt").write_text("preserve")
            with self.assertRaises(Blocked) as caught:
                backend.external_candidate(run, {"task_id": TASK}, document)
            self.assertEqual(caught.exception.code, "BLOCKED_DIRTY_WORKTREE")
            git("add", "uncommitted.txt", cwd=path)
            git("-c", "user.name=Isolated test", "-c", "user.email=test@example.invalid", "commit", "-qm", "forbidden output", cwd=path)
            document["head_sha"] = git("rev-parse", "HEAD", cwd=path)
            with self.assertRaises(Blocked) as caught:
                backend.external_candidate(run, {"task_id": TASK}, document)
            self.assertEqual(caught.exception.code, "BLOCKED_PATHS")


if __name__ == "__main__":
    unittest.main()
