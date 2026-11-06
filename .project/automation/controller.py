"""Run the authorized task graph with durable state and independent acceptance."""

import argparse
from contextlib import contextmanager
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


class Controller:
    """Drive the same durable protocol through a real or isolated fake adapter."""
    def __init__(self, store, backend, clock=time.time):
        self.store, self.backend, self.clock = store, backend, clock

    def create(self, tasks, recipes, repository="sigaev-pro/bc-indexer", limits=None, authorization_ref="D86"):
        require(repository == "sigaev-pro/bc-indexer", "BLOCKED_REPOSITORY", "this launch authorizes only sigaev-pro/bc-indexer")
        limit = dict(DEFAULT_LIMITS, **(limits or {}))
        require(set(limit) == set(DEFAULT_LIMITS) and all(type(v) is int and 1 <= v <= DEFAULT_LIMITS[k]
                for k, v in limit.items()), "BLOCKED_BUDGET", "limits may only narrow the accepted defaults")
        authorization = AUTHORIZATIONS.get(authorization_ref)
        require(authorization is not None, "BLOCKED_AUTHORIZATION", "owner launch is not recorded and reviewed")
        gate = self.backend.start_gate(tasks)
        require(gate.get("passed") is True and gate.get("bootstrap_gate_receipt"),
                "BLOCKED_BOOTSTRAP", "run requires accepted BOOT-09 and current preflight")
        self.backend.verify_receipt("BOOT-09", gate["bootstrap_gate_receipt"], {"catalog": tasks})
        recipes = self.backend.snapshot_recipes(recipes)
        run_id = "run_" + uuid.uuid4().hex
        run = {"schema_version": 1, "run_id": run_id, "repository": repository,
               "authorization": dict(authorization), "preflight": gate, "catalog": tasks, "catalog_sha256": evidence.digest(tasks),
               "recipes": recipes, "recipes_sha256": evidence.digest(recipes), "revision": 0,
               "limits": limit, "created_at": self.clock(), "deadline": self.clock() + limit["seconds"],
               "invocations": 0, "budget_baseline": 0, "budget_history": [], "status": "RUNNING",
               "tasks": {key: {"task_id": key, "state": "PLANNED", "attempts": 0} for key in tasks}}
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
        if task.get("pending"):
            require(task["pending"]["action"] == action, "BLOCKED_STATE", "unfinished operation does not match the task state")
            return task["pending"]
        if action in {"worker", "review"}:
            self.budget(run)
            if action == "worker":
                require(task["attempts"] < run["limits"]["implementation_attempts"],
                        "BLOCKED_ATTEMPTS", "the three implementation attempts are exhausted")
                task["attempts"] += 1
            run["invocations"] += 1
        operation = {"id": uuid.uuid4().hex, "action": action, "lease_generation": task["lease"]["generation"]}
        task["pending"] = operation
        self.store.save(run)
        return operation

    def budget(self, run):
        if self.store.stopped(run["run_id"]):
            raise Blocked("STOPPED", "operator requested a stop")
        if self.clock() >= run["deadline"] or run["invocations"] - run["budget_baseline"] >= run["limits"]["invocations"]:
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
        task.pop("pending")
        return result

    def advance(self, run, task):
        state = task["state"]
        if state == "READY":
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
                require(task["attempts"] < run["limits"]["implementation_attempts"],
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
            if action == "worker":
                require(task["attempts"] < run["limits"]["implementation_attempts"],
                        "BLOCKED_ATTEMPTS", "the three implementation attempts are exhausted")
                task["attempts"] += 1
            if action in {"worker", "review"}:
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

    def run(self, run, resume=False, renew_budget=False, automatic=False, retry_session=False):
        require(not automatic or (resume and not renew_budget and not retry_session),
                "BLOCKED_RECOVERY_MODE", "automatic recovery cannot authorize budget renewal or session retries")
        require(resume or not (renew_budget or retry_session), "BLOCKED_RECOVERY_MODE",
                "budget renewal and diagnostic retries require explicit resume")
        if automatic:
            # A service start is not an operator's decision to continue. Check
            # durable stop state before preflight, reconciliation, or dispatch.
            if self.store.stopped(run["run_id"]):
                run["status"] = "STOPPED"
                self.store.save(run)
                return run
            if run["status"] != "RUNNING":
                return run
        if resume:
            gate = self.backend.start_gate(run["catalog"])
            require(gate.get("passed") is True, "BLOCKED_BOOTSTRAP", "resume requires current preflight and accepted BOOT-09")
            self.backend.verify_receipt("BOOT-09", gate["bootstrap_gate_receipt"], run)
            if renew_budget:
                require(run["status"] == "BUDGET_STOP", "BLOCKED_BUDGET", "only an explicit budget stop can receive a new budget window")
                run["budget_history"].append({"deadline": run["deadline"], "invocations": run["invocations"]})
                run["deadline"] = self.clock() + run["limits"]["seconds"]
                run["budget_baseline"] = run["invocations"]
            if not automatic:
                self.store.clear_stop(run["run_id"])
                for task in run["tasks"].values():
                    if task["state"] in {"BLOCKED", "FAILED"} and task.get("resume_state") and task.get("blocker", {}).get("code") != "BLOCKED_ATTEMPTS":
                        task["state"] = task.pop("resume_state")
                        task.pop("blocker", None)
        try:
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
            "tasks": {k: {key: value for key, value in task.items() if key in {"state", "attempts", "blocker"}}
                      for k, task in run["tasks"].items()}, "blocker": run.get("blocker"),
            "runtime": "NOT_RUN", "product_verification_configured": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["verify-bootstrap", "run", "resume", "status", "stop"])
    parser.add_argument("--repo", default="sigaev-pro/bc-indexer")
    parser.add_argument("--run-id")
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
            print(json.dumps([summary(store.load(path.stem)) for path in paths], indent=2))
            return 0
        if args.command == "stop":
            require(args.run_id, "BLOCKED_STATE", "stop requires an explicit run ID")
            store.stop(args.run_id)
            print(json.dumps({"run_id": args.run_id, "status": "STOP_REQUESTED"}))
            return 0
        with store.lock():
            backend = RealBackend(ROOT, root, store, args.repo, args.capture_dir)
            controller = Controller(store, backend)
            if args.command == "verify-bootstrap":
                report = backend.verify_bootstrap(catalog())
                print(json.dumps(report, indent=2))
                return 0 if report["verdict"] == "PASS" else 1
            if args.command == "run":
                require(not args.run_id and not args.renew_budget, "BLOCKED_STATE", "a new run cannot reuse an ID or renew a budget")
                recipes = read_json(ROOT / ".project/automation/task-recipes.json")
                run = controller.create(catalog(), recipes, args.repo, {"session_seconds": args.session_timeout}, args.authorization_ref)
            else:
                require(args.run_id, "BLOCKED_STATE", "resume requires an explicit run ID")
                run = store.load(args.run_id)
                require(run["repository"] == args.repo, "BLOCKED_REPOSITORY", "resume repository changed")
                require(run["authorization"] == AUTHORIZATIONS[args.authorization_ref], "BLOCKED_AUTHORIZATION", "resume cannot change the run authorization")
            for signum in (signal.SIGTERM, signal.SIGINT):
                signal.signal(signum, lambda *_: store.stop(run["run_id"]))
            report = controller.run(run, resume=args.command == "resume", renew_budget=args.renew_budget,
                                    automatic=args.automatic, retry_session=args.retry_session)
            print(json.dumps(summary(report), indent=2))
            return 0 if report["status"] == "COMPLETE" else 1
    except (Blocked, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print(json.dumps({"status": "BLOCKED", "code": getattr(error, "code", "BLOCKED_STATE"), "reason": str(error)}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
