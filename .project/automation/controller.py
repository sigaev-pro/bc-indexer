"""Run the authorized task graph with durable state and independent acceptance."""

import argparse
from contextlib import ExitStack, contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time
import uuid

import acceptance
import check_docs
import evidence

ROOT = Path(__file__).resolve().parents[2]
if __name__ == "__main__":
    sys.modules["controller"] = sys.modules[__name__]
AUTHORIZATION = {"reference": "D86", "stage_limit": "BOOT"}
AUTHORIZATIONS = {"D86": dict(AUTHORIZATION)}
AUTHORIZATIONS["D92"] = {"reference": "D92", "stage_limit": "E07", "started_at": 1794007047,
                         "hard_deadline": 1794032247, "invocation_limit": 200}
AUTHORIZATIONS["D93"] = {"reference": "D93", "stage_limit": "E07", "mode": "continuous",
                         "started_at": 1794032764, "invocation_limit": None}
STAGES = ("BOOT", "E01", "E02", "E03", "E04", "E05", "E06", "E07")
DEFAULT_LIMITS = {"seconds": 86400, "invocations": 200, "session_seconds": 3600,
                  "implementation_attempts": 3, "model_sessions": 3}


def validate_authorization(authorization):
    require(authorization == AUTHORIZATIONS.get(authorization.get("reference")), "BLOCKED_AUTHORIZATION",
            "authorization differs from the reviewed owner launch record")


def in_scope(run, stage):
    validate_authorization(run["authorization"])
    return STAGES.index(stage) <= STAGES.index(run["authorization"]["stage_limit"])


class Blocked(Exception):
    def __init__(self, code, message):
        self.code = code
        super().__init__(message)


def require(condition, code, message):
    if not condition:
        raise Blocked(code, message)


def read_json(path):
    return acceptance.parse_json(Path(path).read_text())


def catalog(root=ROOT):
    """Pin complete task snapshots and reject ambiguous or inconsistent mappings."""
    text = (Path(root) / ".project/docs/ISSUE_CATALOG.md").read_text()
    mapping_text = (Path(root) / ".project/docs/ISSUE_MAP.md").read_text()
    mapping = {}
    for task, number, repository, url_number in re.findall(
            r"\[([^ ]+) — #(\d+)\]\(https://github\.com/([^/]+/[^/]+)/issues/(\d+)\)", mapping_text):
        require(task not in mapping and number == url_number and repository == "sigaev-pro/bc-indexer",
                "BLOCKED_CATALOG", "duplicate or inconsistent issue mapping")
        mapping[task] = int(number)
    tasks = {}
    for block in re.split(r"(?=^## )", text, flags=re.M):
        marker = re.search(r"<!-- extractor-task:([^ ]+) -->", block)
        if not marker:
            continue
        task = marker.group(1)
        stage = re.search(r"^Stage: ([A-Z0-9]+)\.", block, re.M)
        dependency = re.search(r"^Dependencies: (.+)\.$", block, re.M)
        criteria = re.search(r"^Acceptance: (.+)$", block, re.M)
        require(task not in tasks and task in mapping and stage and dependency and criteria,
                "BLOCKED_CATALOG", "task snapshot is incomplete or duplicated")
        require(stage[1] in STAGES and (task.startswith(stage[1] + "-") or task == "G." + stage[1]),
                "BLOCKED_CATALOG", "task stage does not match its ID")
        tasks[task] = {"task_id": task, "issue": mapping[task], "stage": stage[1],
                       "dependencies": [] if dependency[1] == "none" else dependency[1].split(", "),
                       "acceptance": criteria[1], "snapshot": block.strip()}
    require(len(tasks) == len(mapping) == len(set(mapping.values())) == 84,
            "BLOCKED_CATALOG", "the initial task graph must have 84 unique mapped issues")
    check_docs.check_dag({task: row["dependencies"] for task, row in tasks.items()})
    return tasks


class Store:
    """One repository writer, atomic run records, and durable operator requests."""
    def __init__(self, directory):
        self.directory = Path(directory).resolve()
        self.lock_fd = None

    def run_path(self, run_id):
        require(isinstance(run_id, str) and re.fullmatch(r"[a-zA-Z0-9_-]{8,80}", run_id),
                "BLOCKED_STATE", "invalid run ID")
        return self.directory / "runs" / (run_id + ".json")

    @contextmanager
    def lock(self):
        evidence.durable_directory(self.directory)
        fd = os.open(self.directory / "controller.lock", os.O_RDWR | os.O_CREAT, 0o600)
        try:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as error:
                raise Blocked("BLOCKED_LOCK", "another controller or supervised subprocess owns the repository lock") from error
            self.lock_fd = fd
            yield
        finally:
            self.lock_fd = None
            os.close(fd)

    def save(self, run):
        require(self.lock_fd is not None, "BLOCKED_LOCK", "state writes require the repository lock")
        run["revision"] += 1
        evidence.atomic_json(self.run_path(run["run_id"]), run)

    def load(self, run_id):
        run = read_json(self.run_path(run_id))
        require(run["schema_version"] == 1 and run["run_id"] == run_id,
                "BLOCKED_STATE", "run identity or schema mismatch")
        validate_authorization(run["authorization"])
        require(run["catalog_sha256"] == evidence.digest(run["catalog"]),
                "BLOCKED_STATE", "saved task catalog changed")
        return run

    def stopped(self, run_id):
        return (self.directory / "stop" / (run_id + ".json")).exists()

    def stop(self, run_id):
        self.load(run_id)
        evidence.atomic_json(self.directory / "stop" / (run_id + ".json"),
                             {"run_id": run_id, "requested_at": time.time(), "reason": "operator stop"})

    def clear_stop(self, run_id):
        path = self.directory / "stop" / (run_id + ".json")
        if path.exists():
            path.unlink()
            evidence.sync_directory(path.parent)

    def lease(self, run, task):
        path = self.directory / "leases" / (task["task_id"] + ".json")
        old = read_json(path) if path.exists() else None
        require(old is None or old["run_id"] == run["run_id"] or old["released"] is True,
                "BLOCKED_LEASE", "another run owns this task; resume that run instead of stealing its lease")
        generation = old["generation"] if old and old["run_id"] == run["run_id"] else (old["generation"] + 1 if old else 1)
        lease = {"task_id": task["task_id"], "run_id": run["run_id"], "generation": generation,
                 "released": False, "heartbeat_at": time.time()}
        evidence.atomic_json(path, lease)
        task["lease"] = lease

    def check_lease(self, run, task):
        actual = read_json(self.directory / "leases" / (task["task_id"] + ".json"))
        require(actual["run_id"] == run["run_id"] and actual["generation"] == task["lease"]["generation"]
                and actual["released"] is False, "BLOCKED_LEASE", "task lease is stale or belongs to another writer")

    def release(self, task):
        path = self.directory / "leases" / (task["task_id"] + ".json")
        actual = read_json(path)
        require(all(actual[key] == task["lease"][key] for key in ("task_id", "run_id", "generation")),
                "BLOCKED_LEASE", "terminal cleanup cannot release a different task lease")
        if not actual["released"]:
            evidence.atomic_json(path, dict(actual, released=True))


@contextmanager
def stop_signals(store, run_id, acknowledge=False):
    """Scope durable stop handlers; defer new signals until prior stop acknowledgement ends."""
    numbers = {signal.SIGTERM, signal.SIGINT}
    previous_mask = signal.pthread_sigmask(signal.SIG_BLOCK, numbers)
    previous = {}
    try:
        for number in numbers:
            previous[number] = signal.signal(number, lambda *_: store.stop(run_id))
        if acknowledge:
            store.clear_stop(run_id)
        signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask)
        yield
    finally:
        signal.pthread_sigmask(signal.SIG_BLOCK, numbers)
        try:
            for number, handler in previous.items():
                signal.signal(number, handler)
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous_mask)


class Controller:
    """Drive the same durable protocol through a real or isolated fake adapter."""
    def __init__(self, store, backend, clock=time.time):
        self.store, self.backend, self.clock = store, backend, clock

    def launch(self, run):
        from launch_control import LaunchLedger, accounted
        return LaunchLedger(self.store, run["authorization"], self.clock) if accounted(run["authorization"]) else None

    def window(self, authorization):
        from launch_control import check_window
        validate_authorization(authorization)
        check_window(authorization, self.clock())
        if hasattr(self.backend, "bind_launch"):
            self.backend.bind_launch(authorization)

    def create(self, tasks, recipes, repository="sigaev-pro/bc-indexer", limits=None, authorization_ref="D86"):
        require(repository == "sigaev-pro/bc-indexer", "BLOCKED_REPOSITORY", "this launch authorizes only sigaev-pro/bc-indexer")
        require(authorization_ref != "D93", "BLOCKED_AUTHORIZATION",
                "D93 continues the existing D92 run; use adopt-authorization to preserve its history")
        limit = dict(DEFAULT_LIMITS, **(limits or {}))
        require(set(limit) == set(DEFAULT_LIMITS) and all(type(v) is int and 1 <= v <= DEFAULT_LIMITS[k]
                for k, v in limit.items()), "BLOCKED_BUDGET", "limits may only narrow the accepted defaults")
        authorization = AUTHORIZATIONS.get(authorization_ref)
        require(authorization is not None, "BLOCKED_AUTHORIZATION", "owner launch is not recorded and reviewed")
        self.window(authorization)
        run_id = "run_D92_1794007047" if authorization_ref == "D92" else "run_" + uuid.uuid4().hex
        require(not self.store.run_path(run_id).exists(), "BLOCKED_LAUNCH", "the launch already has a run; resume or hand off its existing state")
        gate = self.backend.start_gate(tasks)
        require(gate.get("passed") is True and gate.get("bootstrap_gate_receipt"),
                "BLOCKED_BOOTSTRAP", "run requires accepted BOOT-09 and current preflight")
        self.backend.verify_receipt("BOOT-09", gate["bootstrap_gate_receipt"], {"catalog": tasks})
        recipes = self.backend.snapshot_recipes(recipes)
        self.window(authorization)
        configuration = self.backend.configuration() if authorization_ref == "D92" else gate.get("source")
        run = {"schema_version": 1, "run_id": run_id, "repository": repository,
               "authorization": dict(authorization), "preflight": gate, "catalog": tasks, "catalog_sha256": evidence.digest(tasks),
               "recipes": recipes, "recipes_sha256": evidence.digest(recipes), "revision": 0,
               "limits": limit, "created_at": self.clock(), "deadline": self.clock() + limit["seconds"],
               "invocations": 0, "budget_baseline": 0, "budget_history": [], "status": "RUNNING",
               "tasks": {key: {"task_id": key, "state": "PLANNED", "attempts": 0} for key in tasks}}
        run["configuration_epoch"] = 1
        run["configuration_source"] = configuration
        run["configuration_history"] = []
        launch = self.launch(run)
        if launch:
            run["deadline"] = min(authorization["hard_deadline"], authorization["started_at"] + limit["seconds"])
            require(self.clock() < run["deadline"], "BUDGET_STOP", "setup exhausted the narrowed launch window")
            launch.claim_run(run_id)
            launch.sync(run)
        self.store.save(run)
        return run

    def reconcile(self, run):
        validate_authorization(run["authorization"])
        require(run["recipes_sha256"] == evidence.digest(run["recipes"]), "BLOCKED_STATE", "saved verification recipes changed")
        self.backend.reconcile_catalog(run["catalog"])
        receipts = self.backend.receipts(run)
        for task_id, receipt in receipts.items():
            require(task_id in run["tasks"], "BLOCKED_EVIDENCE", "receipt names an unknown task")
            self.backend.verify_receipt(task_id, receipt, run)
            task = run["tasks"][task_id]
            if task.get("pending"):
                continue
            if task.get("workspace") and task["state"] != "DONE":
                task.update(state="MERGING", receipt=receipt)
                continue
            task.update(state="DONE", receipt=receipt)
        for task in run["tasks"].values():
            if task["state"] == "DONE":
                self.backend.verify_receipt(task["task_id"], task["receipt"], run)
        self.store.save(run)
        # DONE is durable before lease cleanup. A crash between them is repaired
        # here without replaying issue closure or borrowing another generation.
        for task in run["tasks"].values():
            if task["state"] == "DONE" and task.get("lease"):
                self.store.release(task)

    def intent(self, run, task, action):
        self.store.check_lease(run, task)
        launch = self.launch(run)
        if task.get("pending"):
            require(task["pending"]["action"] == action, "BLOCKED_STATE", "unfinished operation does not match the task state")
            if launch and action in {"worker", "review"}:
                launch.reserve(task["pending"]["id"], "implementer" if action == "worker" else "reviewer", task["task_id"], run["run_id"], limits=run["limits"])
                launch.sync(run)
            return task["pending"]
        if action in {"worker", "review"} and not launch:
            self.budget(run)
            if action == "worker":
                require(task["attempts"] < run["limits"]["implementation_attempts"],
                        "BLOCKED_ATTEMPTS", "the three implementation attempts are exhausted")
                task["attempts"] += 1
            run["invocations"] += 1
        operation = {"id": uuid.uuid4().hex, "action": action, "lease_generation": task["lease"]["generation"]}
        task["pending"] = operation
        self.store.save(run)
        if launch and action in {"worker", "review"}:
            # Persist intent first: replay can reserve this identity exactly once
            # after a crash, and no process starts before durable reservation.
            launch.reserve(operation["id"], "implementer" if action == "worker" else "reviewer", task["task_id"], run["run_id"], limits=run["limits"])
            launch.sync(run)
            self.store.save(run)
        return operation

    def budget(self, run):
        if self.store.stopped(run["run_id"]):
            raise Blocked("STOPPED", "operator requested a stop")
        self.window(run["authorization"])
        launch = self.launch(run)
        if launch:
            launch.sync(run)
        from launch_control import continuous
        if continuous(run["authorization"]):
            require(run["deadline"] is None and all(run["limits"][key] is None
                    for key in ("seconds", "invocations", "implementation_attempts")),
                    "BLOCKED_CONFIGURATION", "D93 must retain its explicit continuous policy")
        elif self.clock() >= run["deadline"] or (not launch and run["invocations"] - run["budget_baseline"] >= run["limits"]["invocations"]):
            raise Blocked("BUDGET_STOP", "the bounded run budget is exhausted")

    def action(self, run, task, action):
        self.budget(run)
        operation = self.intent(run, task, action)
        self.store.check_lease(run, task)
        # The adapter must reconcile this stable operation ID before any mutation.
        try:
            result = self.backend.perform(action, run, task, operation)
        except Blocked as error:
            operation["failure"] = {"code": error.code, "reason": str(error)}
            self.store.save(run)
            raise
        self.store.check_lease(run, task)
        require(result.get("operation_id") == operation["id"], "BLOCKED_EVIDENCE", "result belongs to another operation")
        launch = self.launch(run)
        if launch and action in {"worker", "review"}:
            proof = self.backend.session_recovery(run, task, operation, include_completed=True)
            require(proof and proof.get("cleanup_complete") is True,
                    "BLOCKED_SESSION_CLEANUP", "the shared launch needs retained session cleanup proof")
            launch.complete_controller(operation["id"], proof, result.get("canonical_agent_id"))
            launch.sync(run)
        task.pop("pending")
        return result

    def advance(self, run, task):
        state = task["state"]
        if state == "READY":
            launch = self.launch(run)
            if launch:
                require(not any(row["task_id"] == task["task_id"] for row in launch.active(launch.load())),
                        "BLOCKED_EXTERNAL_SESSION", "an observed or uncertain invocation already owns this task")
            self.store.lease(run, task)
            task["workspace"] = self.action(run, task, "prepare")
            task["state"] = "IMPLEMENTING"
        elif state == "IMPLEMENTING":
            result = self.action(run, task, "worker")
            require(result.get("verified") is True and result.get("commit_sha"),
                    "BLOCKED_EVIDENCE", "worker exit or JSON is not verified implementation evidence")
            task["worker"] = result
            task["state"] = "REVIEW"
        elif state == "REVIEW":
            result = self.action(run, task, "review")
            require(result.get("verified") is True and result.get("head_sha") == task["worker"]["commit_sha"]
                    and result.get("session_id") != task["worker"]["session_id"],
                    "BLOCKED_REVIEW", "review session or exact head is not independently verified")
            task["review"] = result
            if result["verdict"] == "REQUEST_CHANGES":
                from launch_control import continuous
                history = task.setdefault("review_history", [])
                repeated_head = any(item["head_sha"] == result["head_sha"] for item in history)
                history.append(dict(result))
                require(not continuous(run["authorization"]) or not repeated_head,
                        "BLOCKED_NO_PROGRESS", "the same candidate failed review again; diagnose it before another attempt")
                require(continuous(run["authorization"]) or task["attempts"] < run["limits"]["implementation_attempts"],
                        "BLOCKED_ATTEMPTS", "two review repair cycles are exhausted")
                task["state"] = "IMPLEMENTING"
                task.pop("verification", None)
                task.pop("pull", None)
            else:
                require(result["verdict"] == "PASS", "BLOCKED_REVIEW", "independent review did not pass")
                task["state"] = "VERIFYING"
        elif state == "VERIFYING":
            if "verification" not in task:
                task["verification"] = self.action(run, task, "verify")
            require(task["verification"].get("passed") is True,
                    "BLOCKED_VERIFICATION", "declared independent verification did not pass")
            task["pull"] = self.action(run, task, "publish")
            task["state"] = "MERGING"
        elif state == "MERGING":
            if "receipt" not in task:
                task["receipt"] = self.action(run, task, "merge")
                self.backend.verify_receipt(task["task_id"], task["receipt"], run)
                self.store.save(run)
            self.action(run, task, "close")
            task["state"] = "DONE"
            self.store.save(run)
            self.store.release(task)
            return
        else:
            raise Blocked("BLOCKED_STATE", "unsupported task transition")
        self.store.save(run)

    def recover_sessions(self, run, retry_session):
        """An operator may authorize a new operation, never rewrite old results."""
        for task in run["tasks"].values():
            operation = task.get("pending")
            if not operation or operation["action"] not in {"worker", "review", "verify"}:
                continue
            proof = self.backend.session_recovery(run, task, operation)
            if proof is None:
                continue
            launch = self.launch(run)
            if launch and operation["action"] in {"worker", "review"}:
                launch.complete_controller(operation["id"], proof)
                launch.sync(run)
            failed = proof["outcome"] == "failed"
            if failed:
                require(operation["action"] != "verify", "BLOCKED_VERIFICATION_FAILURE",
                        "failed verification requires reviewed diagnosis; a session retry cannot clear a task or runtime FAIL")
                require(retry_session, "BLOCKED_SESSION_RETRY_REQUIRED",
                        "a failed model session requires an explicit diagnostic --retry-session")
                require(task.get("diagnostic_retries", 0) < 1, "BLOCKED_SESSION_RETRIES",
                        "the single diagnostic model-session retry is exhausted")
            self.budget(run)
            self.store.check_lease(run, task)
            action = operation["action"]
            if action == "worker" and not launch:
                require(task["attempts"] < run["limits"]["implementation_attempts"],
                        "BLOCKED_ATTEMPTS", "the three implementation attempts are exhausted")
                task["attempts"] += 1
            if action in {"worker", "review"} and not launch:
                run["invocations"] += 1
            replacement = {"id": uuid.uuid4().hex, "action": action,
                           "lease_generation": operation["lease_generation"], "retry_of": operation["id"],
                           "authorization": "operator-diagnostic-retry" if failed else "operator-resume"}
            task.setdefault("session_history", []).append({"operation": operation, "evidence": proof,
                                                          "replacement_id": replacement["id"]})
            if failed:
                task["diagnostic_retries"] = task.get("diagnostic_retries", 0) + 1
            task["pending"] = replacement
            self.store.save(run)

    def run(self, run, resume=False, renew_budget=False, automatic=False, retry_session=False, acknowledge_stop=True):
        require(not automatic or (resume and not renew_budget and not retry_session),
                "BLOCKED_RECOVERY_MODE", "automatic recovery cannot authorize budget renewal or session retries")
        require(resume or not (renew_budget or retry_session), "BLOCKED_RECOVERY_MODE",
                "budget renewal and diagnostic retries require explicit resume")
        require(not (renew_budget and self.launch(run)), "BLOCKED_BUDGET", "accounted launches cannot reset their policy or history through budget renewal")
        require(not renew_budget or run["status"] == "BUDGET_STOP", "BLOCKED_BUDGET", "only an explicit budget stop can receive a new budget window")
        with stop_signals(self.store, run["run_id"], acknowledge=resume and not automatic and acknowledge_stop):
            return self._run(run, resume, renew_budget, automatic, retry_session)

    def _run(self, run, resume, renew_budget, automatic, retry_session):
        if automatic:
            # A service start is not an operator's decision to continue. Check
            # durable stop state before preflight, reconciliation, or dispatch.
            if self.store.stopped(run["run_id"]):
                run["status"] = "STOPPED"
                self.store.save(run)
                return run
            if run["status"] != "RUNNING":
                return run
        try:
            if resume:
                self.window(run["authorization"])
                if self.launch(run):
                    self.backend.check_configuration(run)
                gate = self.backend.start_gate(run["catalog"])
                require(gate.get("passed") is True, "BLOCKED_BOOTSTRAP", "resume requires current preflight and accepted BOOT-09")
                self.backend.verify_receipt("BOOT-09", gate["bootstrap_gate_receipt"], run)
                if renew_budget:
                    run["budget_history"].append({"deadline": run["deadline"], "invocations": run["invocations"]})
                    run["deadline"] = self.clock() + run["limits"]["seconds"]
                    run["budget_baseline"] = run["invocations"]
                if not automatic:
                    for task in run["tasks"].values():
                        if task["state"] in {"BLOCKED", "FAILED"} and task.get("resume_state") and task.get("blocker", {}).get("code") != "BLOCKED_ATTEMPTS":
                            task["state"] = task.pop("resume_state")
                            task.pop("blocker", None)
            self.budget(run)
            self.reconcile(run)
            if resume and not automatic:
                self.recover_sessions(run, retry_session)
            run.pop("blocker", None)
            run["status"] = "RUNNING"
            while True:
                self.budget(run)
                for key, task in run["tasks"].items():
                    spec = run["catalog"][key]
                    if task["state"] != "PLANNED" or not in_scope(run, spec["stage"]):
                        continue
                    if all(run["tasks"][dep]["state"] == "DONE" for dep in spec["dependencies"]):
                        if key not in run["recipes"]:
                            task.update(state="BLOCKED", blocker={"code": "BLOCKED_UNCONFIGURED_VERIFICATION",
                                        "reason": "no accepted task-specific verification recipe exists"})
                        else:
                            task["state"] = "READY"
                active = next((task for key, task in run["tasks"].items()
                               if in_scope(run, run["catalog"][key]["stage"])
                               and task["state"] in {"READY", "IMPLEMENTING", "REVIEW", "VERIFYING", "MERGING"}), None)
                if active is None:
                    scoped = [t for k, t in run["tasks"].items() if in_scope(run, run["catalog"][k]["stage"])]
                    run["status"] = "COMPLETE" if all(t["state"] == "DONE" for t in scoped) else "BLOCKED"
                    break
                try:
                    self.advance(run, active)
                except Blocked as error:
                    if error.code in {"STOPPED", "BUDGET_STOP"}:
                        raise
                    active["resume_state"] = active["state"]
                    active["state"] = "FAILED" if error.code == "FAILED_SESSION" else "BLOCKED"
                    active["blocker"] = {"code": error.code, "reason": str(error)}
                    self.store.save(run)
        except Blocked as error:
            run["status"] = error.code if error.code in {"STOPPED", "BUDGET_STOP"} else "BLOCKED"
            run["blocker"] = {"code": error.code, "reason": str(error)}
        finally:
            self.store.save(run)
        return run


def summary(run):
    return {"run_id": run["run_id"], "status": run["status"], "authorization": run["authorization"],
            "invocations": run["invocations"], "deadline": run["deadline"],
            "configuration_epoch": run.get("configuration_epoch", 1), "revision": run["revision"],
            "launch_accounting": run.get("launch_accounting"),
            "tasks": {k: {key: value for key, value in task.items() if key in {"state", "attempts", "blocker"}}
                      for k, task in run["tasks"].items()}, "blocker": run.get("blocker"),
            "runtime": "NOT_RUN", "product_verification_configured": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["verify-bootstrap", "initialize", "run", "resume", "status", "stop",
                                             "account-sessions", "reserve-session", "handoff", "adopt-candidate", "adopt-authorization"])
    parser.add_argument("--repo", default="sigaev-pro/bc-indexer")
    parser.add_argument("--run-id")
    parser.add_argument("--input", type=Path, help="Explicit coordinator observations, reservation, or candidate attestation JSON.")
    parser.add_argument("--expected-revision", type=int)
    parser.add_argument("--expected-epoch", type=int)
    parser.add_argument("--capture-dir", type=Path, help="Directory containing durable pre-merge captures for manually accepted tasks.")
    parser.add_argument("--stage-limit", choices=STAGES, default="BOOT")
    parser.add_argument("--authorization-ref", choices=sorted(AUTHORIZATIONS), default="D86")
    parser.add_argument("--session-timeout", type=int, default=3600)
    parser.add_argument("--renew-budget", action="store_true", help="Explicitly grant a stopped run one new bounded budget window.")
    parser.add_argument("--automatic", action="store_true", help="Recover a running process after a crash; preserve durable stops and blockers.")
    parser.add_argument("--retry-session", action="store_true", help="Explicitly allow one diagnostic retry of a failed model session with proven cleanup.")
    args = parser.parse_args()
    try:
        require(not (args.automatic or args.retry_session or args.renew_budget) or args.command == "resume",
                "BLOCKED_RECOVERY_MODE", "recovery options require resume")
        require(not args.automatic or not (args.retry_session or args.renew_budget),
                "BLOCKED_RECOVERY_MODE", "automatic recovery cannot authorize budget renewal or session retries")
        require(AUTHORIZATIONS[args.authorization_ref]["stage_limit"] == args.stage_limit,
                "BLOCKED_AUTHORIZATION", "CLI stage limit differs from the recorded owner launch")
        from controller_backend import RealBackend
        root = evidence.repository_root()
        store = Store(root / ".project/automation/state")
        if args.command == "status":
            paths = [store.run_path(args.run_id)] if args.run_id else sorted((store.directory / "runs").glob("*.json"))
            reports = []
            from launch_control import LaunchLedger, accounted
            for path in paths:
                run = store.load(path.stem)
                if accounted(run["authorization"]):
                    LaunchLedger(store, run["authorization"]).sync(run)
                reports.append(summary(run))
            print(json.dumps(reports, indent=2))
            return 0
        if args.command == "stop":
            require(args.run_id, "BLOCKED_STATE", "stop requires an explicit run ID")
            store.stop(args.run_id)
            print(json.dumps({"run_id": args.run_id, "status": "STOP_REQUESTED"}))
            return 0
        with store.lock(), ExitStack() as signal_contexts:
            if args.command in {"account-sessions", "reserve-session"}:
                from launch_control import LaunchLedger
                require(args.authorization_ref in {"D92", "D93"} and args.input, "BLOCKED_SESSION_ACCOUNTING", "explicit accounted launch input is required")
                launch = LaunchLedger(store, AUTHORIZATIONS[args.authorization_ref])
                document = read_json(args.input)
                if args.command == "account-sessions":
                    record = launch.import_observations(document)
                    print(json.dumps({"used": launch.used(record), "active": [row["invocation_id"] for row in launch.active(record)],
                                      "ledger_sha256": evidence.digest(record)}))
                    return 0
                if document["task_id"] is None or document["task_id"] == "AUT-01" or (
                        args.authorization_ref == "D93" and re.fullmatch(r"AUT-[0-9]{2,}", document["task_id"])):
                    result = launch.reserve(document["invocation_id"], document["role"], document["task_id"],
                                            origin="coordinator-managed")
                    print(json.dumps(result))
                    return 0
                require(args.run_id, "BLOCKED_SESSION_RESERVATION", "product session reservation requires its existing run")
            if args.command not in {"verify-bootstrap", "run", "initialize"}:
                require(args.run_id, "BLOCKED_STATE", "continuation requires an explicit run ID")
                run = store.load(args.run_id)
                require(run["repository"] == args.repo, "BLOCKED_REPOSITORY", "resume repository changed")
                require(args.command == "adopt-authorization" or run["authorization"] == AUTHORIZATIONS[args.authorization_ref],
                        "BLOCKED_AUTHORIZATION", "resume cannot change the run authorization")
                require(not args.renew_budget or (args.authorization_ref == "D86" and run["status"] == "BUDGET_STOP"),
                        "BLOCKED_BUDGET", "this run cannot renew its budget")
                if args.command == "resume" and args.automatic and (store.stopped(run["run_id"]) or run["status"] != "RUNNING"):
                    if store.stopped(run["run_id"]):
                        run["status"] = "STOPPED"
                        store.save(run)
                    print(json.dumps(summary(run), indent=2))
                    return 0 if run["status"] == "COMPLETE" else 1
                signal_contexts.enter_context(stop_signals(store, run["run_id"], acknowledge=args.command == "resume" and not args.automatic))
            backend = RealBackend(ROOT, root, store, args.repo, args.capture_dir,
                                  AUTHORIZATIONS[args.authorization_ref] if args.command != "verify-bootstrap" else None,
                                  stop_file=store.directory / "stop" / (args.run_id + ".json") if args.command == "resume" else None)
            controller = Controller(store, backend)
            if args.command == "adopt-authorization":
                require(args.authorization_ref == "D93" and args.input
                        and args.expected_revision is not None and args.expected_epoch is not None,
                        "BLOCKED_AUTHORIZATION", "D93 adoption requires bootstrap observations and the expected revision and epoch")
                import continuous_control
                continuous_control.adopt(controller, run, read_json(ROOT / ".project/automation/task-recipes.json"),
                                         read_json(args.input), args.expected_revision, args.expected_epoch)
                print(json.dumps(summary(run), indent=2))
                return 0
            if args.command == "verify-bootstrap":
                report = backend.verify_bootstrap(catalog())
                print(json.dumps(report, indent=2))
                return 0 if report["verdict"] == "PASS" else 1
            if args.command in {"run", "initialize"}:
                require(not args.run_id and not args.renew_budget, "BLOCKED_STATE", "a new run cannot reuse an ID or renew a budget")
                recipes = read_json(ROOT / ".project/automation/task-recipes.json")
                run = controller.create(catalog(), recipes, args.repo, {"session_seconds": args.session_timeout}, args.authorization_ref)
                signal_contexts.enter_context(stop_signals(store, run["run_id"]))
            if args.command == "initialize":
                controller.reconcile(run)
                run["status"] = "STOPPED"
                store.save(run)
                store.stop(run["run_id"])
                print(json.dumps(summary(run), indent=2))
                return 0
            if args.command == "reserve-session":
                controller.window(run["authorization"])
                backend.check_configuration(run)
                task_id = document["task_id"]
                require(task_id in run["tasks"] and in_scope(run, run["catalog"][task_id]["stage"])
                        and backend.eligible_recipe(task_id, run["recipes"]), "BLOCKED_UNCONFIGURED_VERIFICATION", "product session has no accepted adapter")
                for dependency in run["catalog"][task_id]["dependencies"]:
                    require(run["tasks"][dependency]["state"] == "DONE", "BLOCKED_DEPENDENCY", "session dependency is not accepted")
                    backend.verify_receipt(dependency, run["tasks"][dependency]["receipt"], run)
                result = launch.reserve(document["invocation_id"], document["role"], task_id, run["run_id"],
                                        "coordinator-managed", run["limits"])
                launch.sync(run)
                store.save(run)
                print(json.dumps(result))
                return 0
            if args.command in {"handoff", "adopt-candidate"}:
                require(args.expected_revision is not None and args.expected_epoch is not None,
                        "BLOCKED_REVISION", "handoff requires both expected revision and configuration epoch")
                import controller_handoff
                if args.command == "handoff":
                    controller_handoff.configuration(controller, run, read_json(ROOT / ".project/automation/task-recipes.json"),
                                                     args.expected_revision, args.expected_epoch)
                else:
                    require(args.input, "BLOCKED_EXTERNAL_CANDIDATE", "candidate adoption needs an explicit attestation")
                    controller_handoff.adopt(controller, run, read_json(args.input), args.expected_revision, args.expected_epoch)
                print(json.dumps(summary(run), indent=2))
                return 0
            report = controller.run(run, resume=args.command == "resume", renew_budget=args.renew_budget,
                                    automatic=args.automatic, retry_session=args.retry_session, acknowledge_stop=False)
            print(json.dumps(summary(report), indent=2))
            return 0 if report["status"] == "COMPLETE" else 1
    except (Blocked, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print(json.dumps({"status": "BLOCKED", "code": getattr(error, "code", "BLOCKED_STATE"), "reason": str(error)}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
