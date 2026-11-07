"""Adopt D93 at a quiescent boundary without resetting D92 history."""

import copy

from controller import AUTHORIZATIONS, in_scope, read_json, require
import evidence
from launch_control import LaunchLedger


def adopt(controller, run, recipes, observations, revision, epoch):
    """Prepare an immutable ledger fork, then atomically replace the run policy."""
    target = AUTHORIZATIONS["D93"]
    observation_digest = evidence.digest(observations)
    if run["authorization"] == target:
        history = run.get("authorization_history", [])
        require(history and history[-1]["last_revision"] == revision
                and history[-1]["configuration_epoch"] == epoch
                and history[-1]["bootstrap_observations_sha256"] == observation_digest,
                "BLOCKED_REVISION", "completed adoption does not match this request")
        controller.launch(run).load()
        return run
    require(run["authorization"] == AUTHORIZATIONS["D92"], "BLOCKED_AUTHORIZATION", "D93 can only continue the recorded D92 run")
    require(run["revision"] == revision and run["configuration_epoch"] == epoch,
            "BLOCKED_REVISION", "run revision or configuration epoch changed")
    require(run["status"] in {"STOPPED", "BLOCKED", "BUDGET_STOP"},
            "BLOCKED_HANDOFF", "authorization adoption requires a stopped or blocked run")
    require(run["recipes_sha256"] == evidence.digest(run["recipes"])
            and run["catalog_sha256"] == evidence.digest(run["catalog"]),
            "BLOCKED_CONFIGURATION", "retained catalog or recipe snapshot changed")
    previous = LaunchLedger(controller.store, run["authorization"], controller.clock)
    old_ledger = previous.load()
    require(old_ledger["run_id"] == run["run_id"] and old_ledger["initial_observations_imported"]
            and not previous.active(old_ledger, include_coordinator=False),
            "BLOCKED_HANDOFF", "adoption requires accounted sessions and proven delegated cleanup")
    for task in run["tasks"].values():
        require(not task.get("pending") and task["state"] in {"PLANNED", "DONE", "BLOCKED"}
                and not (task.get("workspace") and task["state"] != "DONE"),
                "BLOCKED_HANDOFF", "reconcile pending operations and unfinished candidates before adopting D93")
        if task.get("lease"):
            actual = read_json(controller.store.directory / "leases" / (task["task_id"] + ".json"))
            require(actual["released"] is True and all(actual[key] == task["lease"][key]
                    for key in ("task_id", "run_id", "generation")),
                    "BLOCKED_HANDOFF", "an unreleased or foreign lease forbids adoption")
    rows = observations.get("observations", [])
    require(rows and len({row.get("canonical_agent_id") for row in rows}) >= 2
            and all(row.get("task_id") == "AUT-04" and row.get("role") == "reviewer"
                    and row.get("canonical_agent_id") != "/root" and row.get("status") == "completed"
                    and row.get("cleanup_confirmed") is True
                    and row.get("invocation_id") not in old_ledger["sessions"] for row in rows),
            "BLOCKED_SESSION_ACCOUNTING", "account for the actual completed AUT-04 bootstrap reviews without rewriting old sessions")
    controller.window(target)
    controller.backend.reconcile_catalog(run["catalog"])
    gate = controller.backend.start_gate(run["catalog"])
    require(gate.get("passed") is True, "BLOCKED_BOOTSTRAP", "adoption requires accepted bootstrap and current preflight")
    controller.backend.verify_receipt("BOOT-09", gate["bootstrap_gate_receipt"], run)
    source = controller.backend.configuration()
    require(source.get("catalog_sha256", run["catalog_sha256"]) == run["catalog_sha256"],
            "BLOCKED_CONFIGURATION", "adoption cannot replace the task graph")
    new_recipes = controller.backend.snapshot_recipes(recipes)
    launch = LaunchLedger(controller.store, target, controller.clock)
    seed = copy.deepcopy(old_ledger)
    seed.update(authorization=copy.deepcopy(target), initial_observations_imported=False,
                predecessor={"authorization": run["authorization"], "ledger_sha256": evidence.digest(old_ledger),
                             "run_sha256": evidence.digest(run), "revision": revision, "configuration_epoch": epoch})
    prepared = launch.import_observations(observations, record=seed, persist=False)
    if launch.path.exists():
        require(launch.load() == prepared, "BLOCKED_AUTHORIZATION", "prepared D93 accounting changed; reconcile it instead of resetting it")
    else:
        launch.save(prepared)
    # A crash here leaves D92 intact and a deterministic, replayable prepared ledger.
    next_run = copy.deepcopy(run)
    next_run.setdefault("authorization_history", []).append({
        "authorization": run["authorization"], "deadline": run["deadline"], "limits": run["limits"],
        "invocations": run["invocations"], "last_revision": revision, "configuration_epoch": epoch,
        "ledger_sha256": evidence.digest(old_ledger), "bootstrap_observations_sha256": observation_digest,
        "adopted_at": controller.clock()})
    next_run.setdefault("configuration_history", []).append({
        "epoch": epoch, "source": run["configuration_source"], "recipes": run["recipes"],
        "recipes_sha256": run["recipes_sha256"], "last_revision": revision, "replaced_at": controller.clock()})
    next_run.update(authorization=copy.deepcopy(target), deadline=None, configuration_epoch=epoch + 1,
                    configuration_source=source, recipes=new_recipes, recipes_sha256=evidence.digest(new_recipes), preflight=gate)
    for key in ("seconds", "invocations", "implementation_attempts"):
        next_run["limits"][key] = None
    for task_id, task in next_run["tasks"].items():
        if (task["state"] == "BLOCKED" and task.get("blocker", {}).get("code") == "BLOCKED_UNCONFIGURED_VERIFICATION"
                and in_scope(next_run, run["catalog"][task_id]["stage"])
                and controller.backend.eligible_recipe(task_id, new_recipes)):
            task.setdefault("blocker_history", []).append({"blocker": task.pop("blocker"), "configuration_epoch": epoch})
            task.pop("resume_state", None)
            task["state"] = "PLANNED"
    launch.sync(next_run)
    controller.store.save(next_run)
    run.clear()
    run.update(next_run)
    return run
