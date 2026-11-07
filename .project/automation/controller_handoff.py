"""Explicit, revision-checked configuration and coordinator-candidate handoffs."""

import copy
from pathlib import Path

from controller import in_scope, read_json, require
import evidence


def boundary(controller, run, revision, epoch):
    require(run["revision"] == revision and run.get("configuration_epoch", 1) == epoch,
            "BLOCKED_REVISION", "run revision or configuration epoch changed; inspect current state")
    require(controller.launch(run) is not None, "BLOCKED_AUTHORIZATION", "handoff cannot widen or reset a D86 run")
    controller.window(run["authorization"])
    require(run["recipes_sha256"] == evidence.digest(run["recipes"]), "BLOCKED_CONFIGURATION", "saved recipe snapshot changed")
    require(run["status"] in {"STOPPED", "BLOCKED"}, "BLOCKED_HANDOFF", "handoff requires an explicitly stopped or blocked run")
    require(all(not task.get("pending") for task in run["tasks"].values()),
            "BLOCKED_HANDOFF", "pending operations must be reconciled before handoff")


def configuration(controller, run, recipes, revision, epoch):
    """Append a configuration epoch without changing the launch or task history."""
    boundary(controller, run, revision, epoch)
    launch = controller.launch(run)
    record = launch.sync(run)
    require(record["initial_observations_imported"], "BLOCKED_SESSION_ACCOUNTING", "handoff requires explicit initial observations")
    require(not launch.active(record, include_coordinator=False), "BLOCKED_HANDOFF", "active or uncertain delegated sessions forbid handoff")
    for task in run["tasks"].values():
        if task.get("lease"):
            actual = read_json(controller.store.directory / "leases" / (task["task_id"] + ".json"))
            require(actual["released"] is True and all(actual[key] == task["lease"][key] for key in ("task_id", "run_id", "generation")),
                    "BLOCKED_HANDOFF", "an unreleased or foreign task lease forbids configuration handoff")
        require(task["state"] in {"PLANNED", "DONE", "BLOCKED"} and not (task.get("workspace") and task["state"] != "DONE"),
                "BLOCKED_HANDOFF", "unfinished candidate work must be preserved under its existing configuration")
    controller.backend.reconcile_catalog(run["catalog"])
    gate = controller.backend.start_gate(run["catalog"])
    require(gate.get("passed") is True, "BLOCKED_BOOTSTRAP", "handoff requires current accepted bootstrap and preflight")
    controller.backend.verify_receipt("BOOT-09", gate["bootstrap_gate_receipt"], run)
    source = controller.backend.configuration()
    require(source.get("catalog_sha256", run["catalog_sha256"]) == run["catalog_sha256"],
            "BLOCKED_CONFIGURATION", "configuration handoff cannot replace the run's task graph")
    new_recipes = controller.backend.snapshot_recipes(recipes)
    controller.window(run["authorization"])
    require(source != run["configuration_source"] or new_recipes != run["recipes"],
            "BLOCKED_HANDOFF", "configuration is unchanged")
    next_run = copy.deepcopy(run)
    next_run.setdefault("configuration_history", []).append({
        "epoch": run.get("configuration_epoch", 1), "source": run["configuration_source"],
        "recipes": run["recipes"], "recipes_sha256": run["recipes_sha256"],
        "last_revision": run["revision"], "replaced_at": controller.clock()})
    next_run.update(configuration_epoch=epoch + 1, configuration_source=source, recipes=new_recipes,
                    recipes_sha256=evidence.digest(new_recipes), preflight=gate)
    for task_id, task in next_run["tasks"].items():
        if (task["state"] == "BLOCKED" and task.get("blocker", {}).get("code") == "BLOCKED_UNCONFIGURED_VERIFICATION"
                and in_scope(run, run["catalog"][task_id]["stage"])
                and controller.backend.eligible_recipe(task_id, new_recipes)):
            task.setdefault("blocker_history", []).append({"blocker": task.pop("blocker"), "configuration_epoch": epoch})
            task.pop("resume_state", None)
            task["state"] = "PLANNED"
    # One atomic run replacement retains the old epoch on either side of a crash.
    controller.store.save(next_run)
    run.clear()
    run.update(next_run)
    return run


def adopt(controller, run, document, revision, epoch):
    """Adopt real Git outputs and honest coordinator attestations, never fake CLI logs."""
    boundary(controller, run, revision, epoch)
    controller.backend.check_configuration(run)
    require(document.get("schema_version") == 1 and document.get("authorization_ref") == run["authorization"]["reference"]
            and document.get("coordinator_agent_id") == "/root" and document.get("run_id") == run["run_id"]
            and document.get("configuration_epoch") == epoch,
            "BLOCKED_EXTERNAL_CANDIDATE", "candidate attestation does not identify this launch configuration")
    task_id = document["task_id"]
    require(task_id in run["tasks"] and in_scope(run, run["catalog"][task_id]["stage"])
            and controller.backend.eligible_recipe(task_id, run["recipes"]),
            "BLOCKED_UNCONFIGURED_VERIFICATION", "candidate has no eligible task-specific adapter")
    task = run["tasks"][task_id]
    require(task["state"] in {"PLANNED", "READY", "REVIEW", "IMPLEMENTING", "BLOCKED"}
            and not task.get("verification") and not task.get("pull") and not task.get("receipt"),
            "BLOCKED_EXTERNAL_CANDIDATE", "task already has verification, publication, or acceptance to reconcile")
    for dependency in run["catalog"][task_id]["dependencies"]:
        saved = run["tasks"][dependency]
        require(saved["state"] == "DONE", "BLOCKED_DEPENDENCY", "candidate dependency is not accepted")
        controller.backend.verify_receipt(dependency, saved["receipt"], run)
    launch = controller.launch(run)
    author = launch.completed_external(document["implementer_invocation"], task_id, "implementer")
    record = launch.sync(run)
    require(not any(row["task_id"] == task_id for row in launch.active(record)),
            "BLOCKED_EXTERNAL_SESSION", "an active or uncertain session still owns this task")
    checked = controller.backend.external_candidate(run, task, document)
    worker = dict(checked, verified=True, origin="coordinator-attested", session_id=author["canonical_agent_id"],
                  canonical_agent_id=author["canonical_agent_id"], invocation_id=author["invocation_id"],
                  observation_sha256=evidence.digest(author["observation"]), attestation_sha256=evidence.digest(document))
    review = document.get("review")
    if review is not None:
        reviewer = launch.completed_external(review["invocation_id"], task_id, "reviewer")
        authors = {row["canonical_agent_id"] for row in record["sessions"].values()
                   if row["task_id"] == task_id and row["role"] == "implementer" and row.get("canonical_agent_id")}
        require(reviewer["canonical_agent_id"] not in authors, "BLOCKED_REVIEW", "the same canonical agent cannot review its implementation")
        require(review["head_sha"] == document["head_sha"] and review["base_sha"] == document["base_sha"]
                and review["verdict"] in {"PASS", "REQUEST_CHANGES"}
                and isinstance(review["checks"], list) and review["checks"]
                and isinstance(review["findings"], list) and isinstance(review["unverified"], list)
                and all(isinstance(value, str) and value.strip() for key in ("checks", "findings", "unverified") for value in review[key])
                and (review["verdict"] != "PASS" or not review["findings"]),
                "BLOCKED_REVIEW", "external review is stale or lacks an actual consistent report")
        review = dict(review, verified=True, origin="coordinator-attested", session_id=reviewer["canonical_agent_id"],
                      canonical_agent_id=reviewer["canonical_agent_id"], observation_sha256=evidence.digest(reviewer["observation"]))
    controller.window(run["authorization"])
    launch.bind_external(author["invocation_id"], {key: document[key] for key in ("task_id", "head_sha", "base_sha", "worktree", "branch")})
    if review:
        launch.bind_external(reviewer["invocation_id"], document["review"])
    next_run = copy.deepcopy(run)
    next_task = next_run["tasks"][task_id]
    if task.get("lease"):
        controller.store.check_lease(run, task)
    else:
        controller.store.lease(next_run, next_task)
    if task.get("worker") and task["worker"] != worker:
        require(task["worker"].get("invocation_id") != author["invocation_id"]
                or task["worker"]["commit_sha"] == worker["commit_sha"],
                "BLOCKED_EXTERNAL_CANDIDATE", "one completed invocation cannot claim a changed candidate")
        next_task.setdefault("candidate_history", []).append({key: task[key] for key in ("workspace", "worker", "review") if key in task})
    next_task.update(workspace={"path": str(Path(document["worktree"]).resolve()), "branch": document["branch"],
                                "base_sha": document["base_sha"]}, worker=worker, state="REVIEW")
    next_task.setdefault("external_attestations", {})[evidence.digest(document)] = copy.deepcopy(document)
    next_task.pop("blocker", None)
    next_task.pop("resume_state", None)
    next_task.pop("review", None)
    if review:
        next_task.update(review=review, state="VERIFYING" if review["verdict"] == "PASS" else "IMPLEMENTING")
    controller.store.save(next_run)
    run.clear()
    run.update(next_run)
    return run
