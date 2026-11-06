"""Exercise durable orchestration against an isolated, fault-injected backend."""

import copy
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import controller as controller_module
from controller import AUTHORIZATION, Blocked, Controller, Store, catalog
import evidence


class Crash(BaseException):
    pass


def graph():
    return {
        "BOOT-03": {"task_id": "BOOT-03", "stage": "BOOT", "issue": 5, "dependencies": [],
                    "acceptance": "Prove recovery.", "snapshot": "Controller task."},
        "BOOT-09": {"task_id": "BOOT-09", "stage": "BOOT", "issue": 9, "dependencies": ["BOOT-03"],
                    "acceptance": "Verify accepted evidence.", "snapshot": "Bootstrap gate."},
        "E01-01.evidence": {"task_id": "E01-01.evidence", "stage": "E01", "issue": 10,
                            "dependencies": ["BOOT-09"], "acceptance": "Product evidence.", "snapshot": "Product task."},
    }


class FakeBackend:
    """External state survives controller crashes; every mutation has an identity."""
    def __init__(self):
        self.results, self.accepted, self.effects = {}, {}, {}
        self.crash = None
        self.fail = None
        self.review_changes = 0
        self.worker_verified = True
        self.closed_without_proof = set()
        self.sessions = []
        self.gate_passed = True

    def start_gate(self, tasks):
        return {"passed": self.gate_passed,
                "bootstrap_gate_receipt": {"task_id": "BOOT-09", "proof": "verified-merge"}}

    def reconcile_catalog(self, tasks):
        if self.fail == "catalog":
            raise Blocked("BLOCKED_CATALOG", "remote dependency changed")

    def receipts(self, run):
        return copy.deepcopy(self.accepted)

    def verify_receipt(self, task, receipt, run):
        if receipt.get("proof") != "verified-merge" or receipt.get("task_id") != task:
            raise Blocked("BLOCKED_EVIDENCE", "no actual accepted commit and proof")

    def effect(self, name, operation):
        key = operation["id"] + ":" + name
        if self.crash == "before:" + name:
            self.crash = None
            raise Crash(name)
        if key not in self.effects:
            self.effects[key] = 1
        if self.crash == "after:" + name:
            self.crash = None
            raise Crash(name)

    def perform(self, action, run, task, operation):
        if self.fail == action:
            raise Blocked("BLOCKED_" + action.upper(), "injected external failure")
        if self.fail in {"head", "base", "ci"} and action in {"review", "merge"}:
            raise Blocked("BLOCKED_STALE_PROOF", "head, base, or CI differs")
        if operation["id"] in self.results:
            return copy.deepcopy(self.results[operation["id"]])
        result = {"operation_id": operation["id"]}
        if action == "prepare":
            self.effect("worktree", operation)
            result.update(path="/isolated/worktree", branch="owned-branch", base_sha="b" * 40)
        elif action == "worker":
            self.effect("worker", operation)
            self.sessions.append(operation["id"])
            result.update(verified=self.worker_verified, commit_sha="a" * 40, session_id=operation["id"])
        elif action == "review":
            self.effect("review", operation)
            self.sessions.append(operation["id"])
            verdict = "REQUEST_CHANGES" if self.review_changes else "PASS"
            self.review_changes = max(0, self.review_changes - 1)
            result.update(verified=True, head_sha="a" * 40, session_id=operation["id"], verdict=verdict,
                          findings=["Required correction."] if verdict == "REQUEST_CHANGES" else [])
        elif action == "verify":
            self.effect("verification", operation)
            result["passed"] = True
        elif action == "publish":
            for name in ("push", "pr", "comment", "status"):
                self.effect(name, operation)
            result.update(number=100, comment_id=101)
        elif action == "merge":
            self.effect("capture", operation)
            self.effect("merge", operation)
            result.update(task_id=task["task_id"], proof="verified-merge", merge_sha="c" * 40)
            self.accepted[task["task_id"]] = dict(result)
        elif action == "close":
            self.verify_receipt(task["task_id"], task["receipt"], run)
            self.effect("close", operation)
            result["state"] = "closed"
        self.results[operation["id"]] = copy.deepcopy(result)
        return result


class ControllerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.store = Store(Path(self.temp.name) / "state")
        self.backend = FakeBackend()
        self.now = 100.0
        self.controller = Controller(self.store, self.backend, lambda: self.now)
        self.tasks = graph()
        self.recipes = {"BOOT-03": {"verification": "explicit"}, "BOOT-09": {"verification": "explicit"}}

    def create(self, limits=None):
        return self.controller.create(self.tasks, self.recipes, limits=limits)

    def test_complete_boot_never_dispatches_e01(self):
        with self.store.lock():
            run = self.controller.run(self.create())
        self.assertEqual(run["status"], "COMPLETE")
        self.assertEqual(run["tasks"]["E01-01.evidence"]["state"], "PLANNED")
        self.assertEqual(run["authorization"], AUTHORIZATION)
        self.assertEqual(run["invocations"], 4)

    def test_real_catalog_has_84_nodes_and_known_dependencies(self):
        tasks = catalog()
        self.assertEqual(len(tasks), 84)
        self.assertEqual(tasks["BOOT-03"]["dependencies"], ["BOOT-02", "BOOT-04", "BOOT-05"])

    def test_public_run_requires_accepted_bootstrap_gate_and_preflight(self):
        self.backend.gate_passed = False
        with self.store.lock(), self.assertRaises(Blocked):
            self.create()
        self.assertFalse((self.store.directory / "runs").exists())

    def test_scheduler_uses_a_reviewed_stage_bound_without_a_boot_specific_loop(self):
        authorization = {"reference": "TEST_OWNER_LAUNCH", "stage_limit": "E01"}
        with patch.dict(controller_module.AUTHORIZATIONS, {"TEST_OWNER_LAUNCH": authorization}), self.store.lock():
            recipes = dict(self.recipes, **{"E01-01.evidence": {"verification": "explicit fake recipe"}})
            run = self.controller.create(self.tasks, recipes, authorization_ref="TEST_OWNER_LAUNCH")
            self.controller.run(run)
        self.assertEqual(run["tasks"]["E01-01.evidence"]["state"], "DONE")
        self.assertEqual(run["invocations"], 6)

    def test_exit_zero_and_plausible_output_do_not_complete_task(self):
        self.backend.worker_verified = False
        with self.store.lock():
            run = self.controller.run(self.create())
        self.assertEqual(run["tasks"]["BOOT-03"]["state"], "BLOCKED")
        self.assertEqual(run["tasks"]["BOOT-09"]["state"], "PLANNED")
        self.assertFalse(any(key.endswith(":merge") for key in self.backend.effects))

    def test_closed_issue_is_not_dependency_evidence(self):
        self.backend.closed_without_proof.add("BOOT-03")
        self.recipes.pop("BOOT-03")
        with self.store.lock():
            run = self.controller.run(self.create())
        self.assertEqual(run["tasks"]["BOOT-09"]["state"], "PLANNED")
        self.assertEqual(run["tasks"]["BOOT-03"]["blocker"]["code"], "BLOCKED_UNCONFIGURED_VERIFICATION")

    def test_each_mutation_recovers_without_duplicate_changes(self):
        for when in ("before", "after"):
            for name in ("worktree", "worker", "review", "verification", "push", "pr", "comment", "status", "capture", "merge", "close"):
                with self.subTest(when=when, mutation=name), tempfile.TemporaryDirectory() as temporary:
                    store = Store(Path(temporary) / "state")
                    backend = FakeBackend()
                    controller = Controller(store, backend, lambda: self.now)
                    with store.lock():
                        run = controller.create(self.tasks, self.recipes)
                        backend.crash = when + ":" + name
                        with self.assertRaises(Crash):
                            controller.run(run)
                    with store.lock():
                        recovered = controller.run(store.load(run["run_id"]), resume=True)
                    self.assertEqual(recovered["status"], "COMPLETE")
                    self.assertTrue(all(count == 1 for count in backend.effects.values()))
                    self.assertEqual(recovered["invocations"], 4)

    def test_stale_head_base_and_ci_block_acceptance(self):
        for failure in ("head", "base", "ci"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                store = Store(Path(temporary) / "state")
                backend = FakeBackend()
                backend.fail = failure
                controller = Controller(store, backend, lambda: self.now)
                with store.lock():
                    run = controller.run(controller.create(self.tasks, self.recipes))
                self.assertEqual(run["status"], "BLOCKED")
                self.assertFalse(any(key.endswith(":merge") for key in backend.effects))

    def test_review_repair_budget_stops_after_three_implementations(self):
        self.backend.review_changes = 100
        with self.store.lock():
            run = self.controller.run(self.create())
        self.assertEqual(run["tasks"]["BOOT-03"]["attempts"], 3)
        self.assertEqual(run["tasks"]["BOOT-03"]["blocker"]["code"], "BLOCKED_ATTEMPTS")
        self.assertEqual(run["invocations"], 6)

    def test_saved_run_cannot_widen_the_authorized_stage(self):
        with self.store.lock():
            run = self.create()
            run["authorization"]["stage_limit"] = "E01"
            self.store.save(run)
        with self.assertRaises(Blocked):
            self.store.load(run["run_id"])

    def test_timeout_budget_survives_resume_and_requires_explicit_renewal(self):
        with self.store.lock():
            run = self.create({"seconds": 1})
            self.now += 2
            self.assertEqual(self.controller.run(run)["status"], "BUDGET_STOP")
            self.assertEqual(self.controller.run(self.store.load(run["run_id"]), resume=True)["status"], "BUDGET_STOP")
            renewed = self.controller.run(self.store.load(run["run_id"]), resume=True, renew_budget=True)
        self.assertEqual(renewed["status"], "COMPLETE")
        self.assertEqual(len(renewed["budget_history"]), 1)

    def test_invocation_count_is_reserved_before_launch_and_persists(self):
        with self.store.lock():
            run = self.controller.run(self.create({"invocations": 2}))
            self.assertEqual(run["status"], "BUDGET_STOP")
            self.assertEqual(run["invocations"], 2)
            saved = self.store.load(run["run_id"])
            self.controller.run(saved, resume=True)
            self.assertEqual(saved["invocations"], 2)
            self.controller.run(saved, resume=True, renew_budget=True)
            self.assertEqual(saved["invocations"], 4)

    def test_stop_request_preserves_pending_work_and_resume_reconciles(self):
        with self.store.lock():
            run = self.create()
            self.backend.crash = "after:pr"
            with self.assertRaises(Crash):
                self.controller.run(run)
            pending = self.store.load(run["run_id"])["tasks"]["BOOT-03"]["pending"]
            self.store.stop(run["run_id"])
            stopped = self.controller.run(self.store.load(run["run_id"]))
            self.assertEqual(stopped["status"], "STOPPED")
            self.assertEqual(stopped["tasks"]["BOOT-03"]["pending"], pending)
            resumed = self.controller.run(self.store.load(run["run_id"]), resume=True)
        self.assertEqual(resumed["status"], "COMPLETE")

    def test_old_lease_is_not_stolen_by_a_new_run(self):
        with self.store.lock():
            first = self.create()
            self.store.lease(first, first["tasks"]["BOOT-03"])
            path = self.store.directory / "leases/BOOT-03.json"
            lease = evidence.acceptance.parse_json(path.read_text())
            lease["heartbeat_at"] = 0
            evidence.atomic_json(path, lease)
            second = self.create()
            with self.assertRaises(Blocked):
                self.store.lease(second, second["tasks"]["BOOT-03"])

    def test_changed_lease_generation_rejects_result(self):
        with self.store.lock():
            run = self.create()
            task = run["tasks"]["BOOT-03"]
            self.store.lease(run, task)
            path = self.store.directory / "leases/BOOT-03.json"
            lease = dict(task["lease"], generation=task["lease"]["generation"] + 1)
            evidence.atomic_json(path, lease)
            with self.assertRaises(Blocked):
                self.controller.action(run, task, "prepare")
        self.assertFalse(self.backend.effects)

    def test_terminal_save_and_lease_release_crashes_recover_without_reclosing(self):
        for boundary in ("before-save", "after-save", "before-release", "after-release"):
            with self.subTest(boundary=boundary), tempfile.TemporaryDirectory() as temporary:
                store = Store(Path(temporary) / "state")
                backend = FakeBackend()
                controller = Controller(store, backend, lambda: self.now)
                with store.lock():
                    run = controller.create(self.tasks, self.recipes)
                    task = run["tasks"]["BOOT-03"]
                    task["state"] = "READY"
                    for _ in range(4):
                        controller.advance(run, task)
                    self.assertEqual(task["state"], "MERGING")
                    save, release = store.save, store.release

                    def crash_save(current):
                        if task["state"] == "DONE" and boundary == "before-save":
                            raise Crash(boundary)
                        save(current)
                        if task["state"] == "DONE" and boundary == "after-save":
                            raise Crash(boundary)

                    def crash_release(current):
                        if boundary == "before-release":
                            raise Crash(boundary)
                        release(current)
                        if boundary == "after-release":
                            raise Crash(boundary)

                    with patch.object(store, "save", side_effect=crash_save), patch.object(store, "release", side_effect=crash_release):
                        with self.assertRaises(Crash):
                            controller.advance(run, task)
                    # Load the durable state directly: a hard crash does not run a
                    # controller finally block that could hide the missing write.
                    saved = store.load(run["run_id"])
                    recovered = controller.run(saved, resume=True)
                    lease = evidence.acceptance.parse_json((store.directory / "leases/BOOT-03.json").read_text())
                self.assertEqual(recovered["status"], "COMPLETE")
                self.assertEqual(recovered["tasks"]["BOOT-03"]["state"], "DONE")
                self.assertTrue(lease["released"])
                self.assertEqual(len([key for key in backend.effects if key.endswith(":close")]), 2)

    def test_remote_dependency_drift_blocks_before_mutation(self):
        self.backend.fail = "catalog"
        with self.store.lock():
            run = self.controller.run(self.create())
        self.assertEqual(run["status"], "BLOCKED")
        self.assertFalse(self.backend.effects)

    def test_forged_dependency_receipt_is_rejected(self):
        self.backend.accepted["BOOT-03"] = {"task_id": "BOOT-03", "proof": "worker said done"}
        with self.store.lock():
            run = self.controller.run(self.create())
        self.assertEqual(run["status"], "BLOCKED")
        self.assertFalse(self.backend.effects)


if __name__ == "__main__":
    unittest.main()
