"""Account for the fixed D92 launch without manufacturing provider executions."""

import copy
import re
import time

from controller import Blocked, read_json, require
import evidence

ROLES = {"coordinator", "implementer", "reviewer"}


def fixed(authorization):
    return authorization.get("reference") == "D92"


def check_window(authorization, now):
    if fixed(authorization):
        require(authorization["started_at"] <= now < authorization["hard_deadline"],
                "BUDGET_STOP", "the fixed D92 launch window is not active; setup and all continuations share its deadline")


class LaunchLedger:
    """One canonical reservation ledger, shared before and after configuration epochs."""
    def __init__(self, store, authorization, clock=time.time):
        self.store, self.authorization, self.clock = store, authorization, clock
        require(fixed(authorization), "BLOCKED_AUTHORIZATION", "shared launch accounting requires D92")
        self.path = store.directory / "launches/D92.json"

    def load(self):
        if self.path.exists():
            record = read_json(self.path)
            require(record["schema_version"] == 1 and record["authorization"] == self.authorization,
                    "BLOCKED_LAUNCH", "retained launch differs from the owner authorization")
            return record
        return {"schema_version": 1, "authorization": copy.deepcopy(self.authorization), "sessions": {},
                "initial_observations_imported": False, "observation_history": [], "run_id": None,
                "initial_held_invocations": 3}

    def save(self, record):
        require(self.store.lock_fd is not None, "BLOCKED_LOCK", "launch accounting requires the repository lock")
        evidence.atomic_json(self.path, record)

    @staticmethod
    def used(record):
        return max(record["initial_held_invocations"], len(record["sessions"]))

    @staticmethod
    def active(record, include_coordinator=True):
        return [row for row in record["sessions"].values()
                if (include_coordinator or row["role"] != "coordinator")
                and not (row["status"] in {"completed", "cancelled"} and row.get("cleanup_confirmed") is True)]

    @staticmethod
    def attempts(record, task_id):
        return sum(row["task_id"] == task_id and row["role"] == "implementer" for row in record["sessions"].values())

    def sync(self, run):
        record = self.load()
        run["invocations"] = self.used(record)
        run["budget_baseline"] = 0
        for task_id, task in run["tasks"].items():
            task["attempts"] = max(task["attempts"], self.attempts(record, task_id))
        run["launch_accounting"] = {"used": self.used(record), "limit": self.authorization["invocation_limit"],
            "initial_observations_imported": record["initial_observations_imported"],
            "active_invocation_ids": [row["invocation_id"] for row in self.active(record)],
            "ledger_sha256": evidence.digest(record)}
        return record

    def claim_run(self, run_id):
        check_window(self.authorization, self.clock())
        record = self.load()
        require(record["run_id"] in {None, run_id}, "BLOCKED_LAUNCH", "D92 already owns another run; preserve and resume it")
        record["run_id"] = run_id
        self.save(record)

    def import_observations(self, document):
        """The trusted coordinator records observed facts, including late initial accounting."""
        record = self.load()
        require(document["schema_version"] == 1 and document["authorization_ref"] == "D92"
                and document["coordinator_agent_id"] == "/root", "BLOCKED_SESSION_ACCOUNTING", "unexpected observation authority")
        if any(item["sha256"] == evidence.digest(document) for item in record["observation_history"]):
            return record
        rows = document["observations"]
        require(isinstance(rows, list) and rows and len({row["invocation_id"] for row in rows}) == len(rows),
                "BLOCKED_SESSION_ACCOUNTING", "observations must have unique invocation identities")
        for row in rows:
            key = row["invocation_id"]
            require(isinstance(key, str) and re.fullmatch(r"[A-Za-z0-9_.:-]{3,160}", key)
                    and row["role"] in ROLES and row["status"] in {"active", "completed", "cancelled"},
                    "BLOCKED_SESSION_ACCOUNTING", "invalid observation identity, role, or state")
            require(row["task_id"] is None if row["role"] == "coordinator" else isinstance(row["task_id"], str),
                    "BLOCKED_SESSION_ACCOUNTING", "observation task does not match its role")
            agent = row["canonical_agent_id"]
            require((isinstance(agent, str) and re.fullmatch(r"/root(?:/[A-Za-z0-9_-]+)*", agent)) or (agent is None and row["status"] == "cancelled"),
                    "BLOCKED_SESSION_ACCOUNTING", "use the real canonical agent identity; do not invent provider session IDs")
            observed = row["observed_at"]
            require(type(observed) in {int, float} and self.authorization["started_at"] <= observed <= self.clock(),
                    "BLOCKED_SESSION_ACCOUNTING", "observation time is outside the known launch history")
            started, completed = row.get("started_at"), row.get("completed_at")
            require(started is None or (type(started) in {int, float}
                    and self.authorization["started_at"] <= started < self.authorization["hard_deadline"] and started <= observed),
                    "BLOCKED_SESSION_ACCOUNTING", "observed start is outside the authorized window")
            require(row.get("evidence") and isinstance(row["evidence"], str), "BLOCKED_SESSION_ACCOUNTING", "observation needs an actual evidence reference")
            if row["status"] in {"completed", "cancelled"}:
                require(type(completed) in {int, float} and (started or self.authorization["started_at"]) <= completed <= observed,
                        "BLOCKED_SESSION_ACCOUNTING", "completion needs an observed completion time")
            old = record["sessions"].get(key)
            if old is None:
                require(not record["initial_observations_imported"], "BLOCKED_SESSION_RESERVATION",
                        "future collaboration invocations must reserve their budget before starting")
                old = {"invocation_id": key, "role": row["role"], "task_id": row["task_id"],
                       "origin": "coordinator-observed", "accounting": "recorded-after-start", "history": []}
            else:
                require(old["origin"] in {"coordinator-observed", "coordinator-managed"},
                        "BLOCKED_SESSION_ACCOUNTING", "coordinator observations cannot replace supervised process cleanup evidence")
                require(old["role"] == row["role"] and old["task_id"] == row["task_id"]
                        and old.get("canonical_agent_id") in {None, agent},
                        "BLOCKED_SESSION_ACCOUNTING", "an invocation cannot change its task, role, or canonical agent")
                require(not (old["status"] in {"completed", "cancelled"} and row["status"] == "active"),
                        "BLOCKED_SESSION_ACCOUNTING", "a completed invocation cannot be reopened")
                require(old.get("started_at") in {None, started} and old.get("completed_at") in {None, completed}
                        and not (old.get("cleanup_confirmed") and not row.get("cleanup_confirmed")),
                        "BLOCKED_SESSION_ACCOUNTING", "retained observed times or complete cleanup cannot be rewritten")
            if old.get("observation") != row:
                old["history"].append(copy.deepcopy(row))
            old.update(canonical_agent_id=agent, status=row["status"], started_at=started,
                       completed_at=completed, cleanup_confirmed=row.get("cleanup_confirmed") is True,
                       observation=copy.deepcopy(row))
            record["sessions"][key] = old
        if not record["initial_observations_imported"]:
            require(len({row["canonical_agent_id"] for row in record["sessions"].values()}) >= 3
                    and any(row["role"] == "coordinator" and row["canonical_agent_id"] == "/root" for row in record["sessions"].values()),
                    "BLOCKED_SESSION_ACCOUNTING", "the initial coordinator and two delegated invocations must be accounted explicitly")
            record["initial_observations_imported"] = True
        require(self.used(record) <= self.authorization["invocation_limit"], "BUDGET_STOP", "observed invocations exceed the shared launch budget")
        digest = evidence.digest(document)
        if not any(item["sha256"] == digest for item in record["observation_history"]):
            record["observation_history"].append({"sha256": digest, "document": copy.deepcopy(document)})
        self.save(record)
        return record

    def reserve(self, key, role, task_id, run_id=None, origin="controller", limits=None):
        check_window(self.authorization, self.clock())
        record = self.load()
        require(record["initial_observations_imported"], "BLOCKED_SESSION_ACCOUNTING", "import actual initial collaboration observations before dispatch")
        if key in record["sessions"]:
            old = record["sessions"][key]
            require((old["role"], old["task_id"], old.get("run_id"), old["origin"]) == (role, task_id, run_id, origin),
                    "BLOCKED_SESSION_RESERVATION", "reservation identity changed")
            return old
        require(role in ROLES and isinstance(key, str) and re.fullmatch(r"[A-Za-z0-9_.:-]{3,160}", key)
                and origin in {"controller", "coordinator-managed"}
                and (task_id is None if role == "coordinator" else isinstance(task_id, str)),
                "BLOCKED_SESSION_RESERVATION", "invalid reservation identity or role")
        limits = limits or {"invocations": 200, "implementation_attempts": 3}
        require(self.used(record) < min(self.authorization["invocation_limit"], limits["invocations"]), "BUDGET_STOP", "shared D92 invocation budget is exhausted")
        require(len(self.active(record)) < 3, "BLOCKED_SESSION_CAPACITY", "three coordinator/worker/reviewer slots are already occupied")
        require(not any(row["task_id"] == task_id and row["role"] != "coordinator"
                        and (role == "implementer" or row["role"] == "implementer") for row in self.active(record))
                if task_id is not None else True,
                "BLOCKED_SESSION_OWNERSHIP", "an implementation cannot overlap another active or uncertain task session")
        if role == "implementer":
            require(self.attempts(record, task_id) < min(3, limits["implementation_attempts"]), "BLOCKED_ATTEMPTS", "the launch's implementation attempts are exhausted")
        row = {"invocation_id": key, "role": role, "task_id": task_id, "run_id": run_id, "origin": origin,
               "accounting": "reserved-before-start", "status": "reserved", "canonical_agent_id": None,
               "reserved_at": self.clock(), "cleanup_confirmed": False, "history": []}
        record["sessions"][key] = row
        self.save(record)
        return row

    def complete_controller(self, key, proof, canonical_agent_id=None):
        record = self.load()
        row = record["sessions"][key]
        require(row["origin"] == "controller" and proof.get("cleanup_complete") is True,
                "BLOCKED_SESSION_CLEANUP", "controller completion needs retained complete-cleanup evidence")
        if row["status"] == "completed":
            require(row["completion"] == proof, "BLOCKED_SESSION_ACCOUNTING", "retained completion changed")
            return
        row.update(status="completed", cleanup_confirmed=True, completed_at=self.clock(),
                   completion=copy.deepcopy(proof), canonical_agent_id=canonical_agent_id)
        self.save(record)

    def completed_external(self, key, task_id, role):
        row = self.load()["sessions"].get(key)
        require(row and row["origin"] in {"coordinator-observed", "coordinator-managed"}
                and row["task_id"] == task_id and row["role"] == role
                and row["status"] == "completed" and row.get("cleanup_confirmed") is True
                and row.get("canonical_agent_id"), "BLOCKED_EXTERNAL_SESSION", "external candidate/review needs a completed, cleanup-confirmed real invocation")
        return row

    def bind_external(self, key, binding):
        record = self.load()
        row = record["sessions"][key]
        require(row.get("candidate_binding") in (None, binding), "BLOCKED_EXTERNAL_SESSION",
                "a completed external invocation cannot attest a different candidate or review")
        row["candidate_binding"] = copy.deepcopy(binding)
        self.save(record)
