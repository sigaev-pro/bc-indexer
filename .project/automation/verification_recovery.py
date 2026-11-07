"""Archive one diagnosed build-verification failure without accepting or retrying it."""

import copy
import base64
import ast
from contextlib import ExitStack, contextmanager
import fcntl
import hashlib
import os
from pathlib import Path
import re
import stat
import sys
import tempfile
import time
import urllib.parse

import acceptance
import build_resources
from controller import require
import evidence


FIELDS = {"schema_version", "run_id", "task_id", "operation_id", "head_sha", "base_sha",
          "source_sha", "recovery_source_sha", "diagnosis"}
MAX_BYTES, MAX_FILES = 1024 ** 3, 20000


def old_pack_paths(content):
    """Read the accepted declarative pack shape without importing historical code."""
    values = {node.targets[0].id: node.value for node in ast.parse(content).body
              if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name)}
    value = values["PACK_FILES"]
    if isinstance(value, ast.BinOp):
        expected = ast.parse('tuple(".project/automation/" + name + ".py" for name in PACK_MODULES)', mode="eval").body
        require(isinstance(value.op, ast.Add) and ast.dump(value.left) == ast.dump(expected),
                "BLOCKED_CONFIGURATION", "historical pack uses an unsupported dynamic definition")
        modules, extra = ast.literal_eval(values["PACK_MODULES"]), ast.literal_eval(value.right)
        require(type(modules) is tuple and all(isinstance(name, str) for name in modules),
                "BLOCKED_CONFIGURATION", "historical pack module inventory is invalid")
        names = tuple(".project/automation/" + name + ".py" for name in modules) + extra
    else:
        names = ast.literal_eval(value)
    require(type(names) is tuple and all(isinstance(name, str) for name in names) and len(set(names)) == len(names),
            "BLOCKED_CONFIGURATION", "historical pack paths are invalid or duplicated")
    return set(names)


@contextmanager
def regular(root, relative):
    """Open through directory descriptors; links and special files cannot be read."""
    parts = Path(relative).parts
    require(parts and not Path(relative).is_absolute() and all(part not in {".", ".."} for part in parts),
            "BLOCKED_RECOVERY_EVIDENCE", "unsafe evidence path")
    with ExitStack() as stack:
        fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        stack.callback(os.close, fd)
        for part in parts[:-1]:
            fd = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            stack.callback(os.close, fd)
        leaf = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
        stack.callback(os.close, leaf)
        require(stat.S_ISREG(os.fstat(leaf).st_mode), "BLOCKED_RECOVERY_EVIDENCE", "evidence is not a regular file")
        yield leaf


def raw_json(root, relative):
    with regular(root, relative) as fd:
        require(os.fstat(fd).st_size <= 16 * 1024 * 1024, "BLOCKED_RECOVERY_EVIDENCE", "JSON evidence is too large")
        with os.fdopen(os.dup(fd), "rb") as stream:
            return acceptance.parse_json(stream.read().decode("utf-8"))


def hash_file(root, relative, guard=lambda: None):
    with regular(root, relative) as fd, os.fdopen(os.dup(fd), "rb") as stream:
        digest, size = hashlib.sha256(), 0
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            guard()
            digest.update(block)
            size += len(block)
            require(size <= MAX_BYTES, "BLOCKED_RECOVERY_EVIDENCE", "evidence exceeds its byte bound")
        return {"kind": "file", "sha256": digest.hexdigest(), "size_bytes": size}


def inventory(root, operation, guard):
    """Describe owned paths without following symlinks or opening special files."""
    require(re.fullmatch(r"[a-f0-9]{32}", operation), "BLOCKED_RECOVERY_EVIDENCE", "invalid operation ID")
    root = Path(root)
    names = sorted(path.name for path in root.iterdir() if path.name.startswith(operation))
    require(operation in names and all(name == operation or re.fullmatch(operation + r"-check-[0-9]+", name) for name in names),
            "BLOCKED_RECOVERY_EVIDENCE", "operation directories are incomplete or ambiguous")
    result, total = {}, 0

    def walk(fd, prefix):
        nonlocal total
        guard()
        require(len(Path(prefix).parts) <= 64, "BLOCKED_RECOVERY_EVIDENCE", "owned evidence nesting exceeds its bound")
        with os.scandir(fd) as entries:
            for entry in sorted(entries, key=lambda item: item.name):
                guard()
                name = prefix + "/" + entry.name
                mode = entry.stat(follow_symlinks=False).st_mode
                require(len(result) < MAX_FILES, "BLOCKED_RECOVERY_EVIDENCE", "too many owned evidence paths")
                if stat.S_ISDIR(mode):
                    result[name] = {"kind": "directory"}
                    child = os.open(entry.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                    try:
                        walk(child, name)
                    finally:
                        os.close(child)
                elif stat.S_ISLNK(mode):
                    result[name] = {"kind": "symlink", "target": os.readlink(entry.name, dir_fd=fd)}
                else:
                    require(stat.S_ISREG(mode), "BLOCKED_RECOVERY_EVIDENCE", "special evidence files cannot be archived")
                    result[name] = hash_file(root, name, guard)
                    total += result[name]["size_bytes"]
                    require(total <= MAX_BYTES, "BLOCKED_RECOVERY_EVIDENCE", "owned evidence exceeds its total byte bound")

    for name in names:
        fd = os.open(root / name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            result[name] = {"kind": "directory"}
            walk(fd, name)
        finally:
            os.close(fd)
    return result


def retain(root, manifest, destination, guard):
    """Durably copy regular bytes; link descriptions stay only in the manifest."""
    objects = Path(destination) / "objects"
    for parent in (objects, *objects.parents):
        if parent.exists() or parent.is_symlink():
            require(stat.S_ISDIR(parent.lstat().st_mode), "BLOCKED_RECOVERY_EVIDENCE", "archive destination traverses a non-directory or symlink")
    evidence.durable_directory(objects)
    for name, row in manifest.items():
        guard()
        if row["kind"] != "file":
            continue
        target = objects / row["sha256"]
        if target.exists():
            require(hash_file(objects, target.name, guard) == row, "BLOCKED_RECOVERY_EVIDENCE", "retained archive object changed")
            continue
        fd, temporary = tempfile.mkstemp(prefix=".pending-", dir=objects)
        try:
            digest, size = hashlib.sha256(), 0
            with os.fdopen(fd, "wb") as output, regular(root, name) as source_fd, os.fdopen(os.dup(source_fd), "rb") as source:
                for block in iter(lambda: source.read(1024 * 1024), b""):
                    guard()
                    digest.update(block)
                    size += len(block)
                    require(size <= row["size_bytes"], "BLOCKED_RECOVERY_EVIDENCE", "evidence grew during archival")
                    output.write(block)
                output.flush()
                os.fsync(output.fileno())
            require(size == row["size_bytes"] and digest.hexdigest() == row["sha256"],
                    "BLOCKED_RECOVERY_EVIDENCE", "evidence changed during archival")
            os.link(temporary, target)
            evidence.sync_directory(objects)
        finally:
            Path(temporary).unlink(missing_ok=True)


def source_identity(backend, run, document, guard):
    """Validate an accepted descendant without relaxing ordinary frozen-source checks."""
    current = backend.configuration()
    saved = run["configuration_source"]
    require(saved["head_sha"] == document["source_sha"] and current["head_sha"] == document["recovery_source_sha"]
            and saved["catalog_sha256"] == current["catalog_sha256"] == run["catalog_sha256"],
            "BLOCKED_CONFIGURATION", "recovery source or frozen task graph differs")
    backend.git(backend.source, "merge-base", "--is-ancestor", saved["head_sha"], current["head_sha"])
    names = backend.git(backend.source, "ls-tree", "-r", "--name-only", saved["head_sha"], "--",
                        ".project/automation", ".project/agency", ".project/AGENTS.md").splitlines()
    require(set(names) == set(saved["files"]), "BLOCKED_CONFIGURATION", "saved source inventory differs from its Git commit")
    contents = {}

    def original(name):
        guard()
        require(isinstance(name, str) and not Path(name).is_absolute() and ".." not in Path(name).parts,
                "BLOCKED_CONFIGURATION", "unsafe frozen source path")
        if name not in contents:
            # Preserve raw Git bytes through the bounded command runner's text interface.
            script = ("import base64,subprocess,sys; "
                      "r=subprocess.run(['git','cat-file','blob',sys.argv[1]],capture_output=True,timeout=20); "
                      "sys.stdout.buffer.write(base64.b64encode(r.stdout)); sys.exit(r.returncode)")
            result = backend.command([sys.executable, "-I", "-c", script, saved["head_sha"] + ":" + name], cwd=backend.source)
            require(result.returncode == 0, "BLOCKED_CONFIGURATION", "frozen source object is unavailable")
            contents[name] = base64.b64decode(result.stdout, validate=True)
        return contents[name]

    for name, digest in saved["files"].items():
        require(hashlib.sha256(original(name)).hexdigest() == digest, "BLOCKED_CONFIGURATION", "saved source differs from retained Git objects")
    declared = acceptance.parse_json(original(".project/automation/task-recipes.json").decode())
    recipes = run["recipes"]
    require(evidence.digest(recipes) == run["recipes_sha256"] and set(recipes) == set(declared),
            "BLOCKED_CONFIGURATION", "frozen recipe inventory changed")
    for task_id, recipe in recipes.items():
        require({key: value for key, value in recipe.items() if key != "trusted_pack"} == declared[task_id],
                "BLOCKED_CONFIGURATION", "frozen recipe differs from its Git object")
        module = {"E01-01.evidence": "evidence_task_verifier.py", "E01-01.builds": "build_task_verifier.py"}.get(task_id)
        require(("trusted_pack" in recipe) == (module is not None),
                "BLOCKED_CONFIGURATION", "historical task recipe is missing its required pack or adds an unsupported pack")
        if "trusted_pack" in recipe:
            require(set(recipe["trusted_pack"]) == old_pack_paths(original(".project/automation/" + module)),
                    "BLOCKED_CONFIGURATION", "frozen verifier pack membership differs from its Git object")
        for name, digest in recipe.get("trusted_pack", {}).items():
            require(hashlib.sha256(original(name)).hexdigest() == digest,
                    "BLOCKED_CONFIGURATION", "frozen verifier pack differs from retained Git objects")
    return current, contents


def terminal_sessions(controller, run, task, document, manifest, stack, guard, contents):
    """Require a contiguous, normally exited verification prefix with an actual failure."""
    backend, operation = controller.backend, document["operation_id"]
    root = backend.root / ".project/automation/runs" / run["run_id"]
    expected = list(run["recipes"][task["task_id"]]["commands"])
    driver = backend.root / ".project/automation/build_task_verifier.py"
    expected.append([sys.executable, "-I", str(driver), "--candidate", task["workspace"]["path"]])
    identifiers = [operation + "-check-" + str(index) for index in range(len(expected))]
    present = [identifier for identifier in identifiers if identifier in manifest]
    require(present and present == identifiers[:len(present)]
            and {name for name in manifest if "/" not in name} == {operation, *present}
            and not any(name.startswith(operation + "/") for name in manifest),
            "BLOCKED_RECOVERY_EVIDENCE", "verification sessions are incomplete, unrecognized, or already have an operation result")
    records, process_ids, group_ids = [], set(), set()
    for index, identifier in enumerate(present):
        guard()
        directory = root / identifier
        require(identifier + "/watchdog.json" not in manifest, "BLOCKED_SESSION_CLEANUP", "watchdog cleanup requires separate diagnosis")
        lock = stack.enter_context(regular(root, identifier + "/session.lock"))
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            require(False, "BLOCKED_SESSION_CLEANUP", "a session still owns its execution lock")
        request, result = raw_json(root, identifier + "/request.json"), raw_json(root, identifier + "/result.json")
        started, heartbeat = raw_json(root, identifier + "/started.json"), raw_json(root, identifier + "/heartbeat.json")
        command = list(expected[index])
        native = index == len(expected) - 1
        if native:
            command += ["--request", str(directory / "request.json")]
        require(request["operation_id"] == result["operation_id"] == started["operation_id"] == heartbeat["operation_id"] == identifier
                and request["command"] == command and request["cwd"] == task["workspace"]["path"]
                and request["env"] == {"BOOTSTRAP_HEAD_SHA": document["head_sha"], "BOOTSTRAP_BASE_SHA": document["base_sha"]}
                and request["stop_file"] == str(controller.store.directory / "stop" / (run["run_id"] + ".json"))
                and result["request_sha256"] == started["request_sha256"] == evidence.digest(request),
                "BLOCKED_RECOVERY_EVIDENCE", "session request/result does not bind this candidate, command, or run")
        require(result["reason"] == "completed" and type(result["exit_code"]) is int and result["exit_code"] >= 0
                and result["cleanup_complete"] is True and result["termination"] == "already-exited"
                and (result["exit_code"] != 0) == (index == len(present) - 1),
                "BLOCKED_RECOVERY_EVIDENCE", "verification is running, stopped, uncertain, successful, or not a failed prefix")
        for name, digest in (("stdin.txt", request["stdin_sha256"]), ("stdout.jsonl", result["stdout_sha256"]), ("stderr.log", result["stderr_sha256"])):
            require(manifest[identifier + "/" + name]["sha256"] == digest,
                    "BLOCKED_RECOVERY_EVIDENCE", "session input or output bytes changed")
        require(request["stdin_sha256"] == hashlib.sha256(b"").hexdigest()
                and started["supervisor_pid"] == heartbeat["supervisor_pid"]
                and started["started_at"] == heartbeat["started_at"] == result["started_at"],
                "BLOCKED_RECOVERY_EVIDENCE", "session ownership or input differs")
        process_ids.add(started["supervisor_pid"])
        group_ids.add(heartbeat["process_group"])
        cleanup = result["candidate_cleanup"]
        require(cleanup.get("complete") is True, "BLOCKED_SESSION_CLEANUP", "candidate cleanup is incomplete")
        groups = {row["token"]: row for row in cleanup["groups"]}
        prefix = identifier + "/candidate-groups/"
        require(manifest.get(prefix[:-1]) == {"kind": "directory"}, "BLOCKED_SESSION_CLEANUP", "candidate group inventory is missing")
        paths = [name for name in manifest if name.startswith(prefix) and name.endswith(".json")]
        require(len(groups) == len(cleanup["groups"]) == len(paths), "BLOCKED_SESSION_CLEANUP", "candidate group inventory differs")
        for name in paths:
            row = raw_json(root, name)
            require(row["token"] == Path(name).stem and row["state"] in {"running", "complete"}
                    and groups.get(row["token"]) == {"token": row["token"], "group": row["group"], "complete": True},
                    "BLOCKED_SESSION_CLEANUP", "candidate group ownership is unresolved or changed")
            group_ids.add(row["group"])
        if native:
            spec = request["owned_docker"]
            lock_bytes = contents.get(".project/bootstrap/components/candidate-lock.json")
            require(lock_bytes is not None, "BLOCKED_CONFIGURATION", "frozen build lock is unavailable")
            lock_value = acceptance.parse_json(lock_bytes.decode())
            import build_contract
            require(spec["run_id"] == run["run_id"] and spec["task_id"] == task["task_id"] and spec["operation_id"] == identifier
                    and spec["images"] == [lock_value["components"][name]["pull_reference"] for name in build_contract.IMAGES],
                    "BLOCKED_SESSION_CLEANUP", "Docker ownership differs from the failed build")
            build_resources.validate_cleanup(spec, result["docker_cleanup"])
            require(raw_json(root, identifier + "/docker-cleanup.json") == result["docker_cleanup"]
                    and raw_json(root, identifier + "/docker-resources.json") == result["docker_cleanup"]["creation_inventory"]
                    and not build_resources.members(spec), "BLOCKED_SESSION_CLEANUP", "owned Docker resources or cleanup evidence differ")
        else:
            require("owned_docker" not in request, "BLOCKED_SESSION_CLEANUP", "unexpected Docker ownership in a recipe command")
        records.append({"operation_id": identifier, "request_sha256": manifest[identifier + "/request.json"]["sha256"],
                        "result_sha256": manifest[identifier + "/result.json"]["sha256"]})
    require(all(type(value) is int and value > 0 for value in process_ids | group_ids),
            "BLOCKED_SESSION_CLEANUP", "invalid retained process identity")
    processes = backend.command(["ps", "-axo", "pid=,pgid=,stat="], timeout=10)
    require(processes.returncode == 0, "BLOCKED_SESSION_CLEANUP", "process ownership cannot be inspected")
    for line in processes.stdout.splitlines():
        parts = line.split()
        require(len(parts) == 3 and parts[0].isdigit() and parts[1].isdigit(), "BLOCKED_SESSION_CLEANUP", "process listing is incomplete")
        require(parts[2].startswith("Z") or (int(parts[0]) not in process_ids and int(parts[1]) not in group_ids),
                "BLOCKED_SESSION_CLEANUP", "a retained process identity is live or has been reused; no signal was sent")
    return records


def archive(controller, run, document, revision, epoch, inspect=False):
    """Inspect or archive a failed build, retaining a replayable exact lease release."""
    store, backend = controller.store, controller.backend
    deadline = time.monotonic() + 300

    def guard():
        require(not store.stopped(run["run_id"]) and run["status"] != "STOPPED", "STOPPED", "operator stop forbids failed-verification recovery")
        require(time.monotonic() < deadline, "BLOCKED_RECOVERY_TIMEOUT", "failed-verification recovery exceeded five minutes")
        controller.window(run["authorization"])

    guard()
    require(store.lock_fd is not None and run["status"] == "BLOCKED" and run["authorization"]["reference"] == "D93",
            "BLOCKED_RECOVERY", "recovery requires the D93 repository lock and a blocked run")
    require(type(document) is dict and set(document) in (FIELDS, FIELDS | {"bindings"})
            and type(document["schema_version"]) is int and document["schema_version"] == 1
            and document["run_id"] == run["run_id"] and document["task_id"] == "E01-01.builds"
            and isinstance(document["diagnosis"], str) and 1 <= len(document["diagnosis"]) <= 4000
            and re.fullmatch(r"[a-f0-9]{32}", document["operation_id"])
            and all(re.fullmatch(r"[a-f0-9]{40}", document[key]) for key in ("head_sha", "base_sha", "source_sha", "recovery_source_sha")),
            "BLOCKED_RECOVERY", "recovery input is incomplete or outside the configured build task")
    task = run["tasks"][document["task_id"]]
    marker = run.get("failure_recovery")
    previous = next((row for row in task.get("failed_verifications", []) if row["operation_id"] == document["operation_id"]), None)
    reference = marker or previous
    archive_root = store.directory / "failed-verifications" / run["run_id"] / document["operation_id"]
    saved = None
    if reference:
        require(not inspect and reference["input_sha256"] == evidence.digest(document)
                and reference["revision"] == revision and reference["epoch"] == epoch
                and run["configuration_epoch"] == epoch
                and run["revision"] == (revision + 1 if marker else reference.get("completed_revision")),
                "BLOCKED_REVISION", "recovery replay input or run revision changed")
        saved = raw_json(archive_root, "archive.json")
        require(evidence.digest(saved) == reference["archive_sha256"], "BLOCKED_RECOVERY_EVIDENCE", "failed-attempt archive changed")
        original_run = saved["run"]
        original_task = original_run["tasks"][document["task_id"]]
        if marker:
            require(task["state"] == "BLOCKED" and task.get("blocker", {}).get("code") == "BLOCKED_REPAIR_REQUIRED"
                    and not any(task.get(key) for key in ("workspace", "worker", "review", "pending", "verification", "pull", "receipt"))
                    and task.get("lease") == original_task["lease"] and previous == marker,
                    "BLOCKED_RECOVERY", "pending release state differs from the archived failure")
    else:
        require(run["revision"] == revision and run["configuration_epoch"] == epoch,
                "BLOCKED_REVISION", "run revision or configuration epoch changed")
        require(not marker and task["state"] == "FAILED" and task.get("resume_state") == "VERIFYING"
                and task.get("pending", {}).get("id") == document["operation_id"]
                and task["pending"]["action"] == "verify" and task["pending"].get("failure", {}).get("code") == "FAILED_SESSION"
                and not any(task.get(key) for key in ("verification", "pull", "receipt")),
                "BLOCKED_RECOVERY", "task is not an unpublished failed verification")
        original_run, original_task = run, task
        store.check_lease(run, task)
    require(original_task["worker"]["commit_sha"] == document["head_sha"]
            and original_task["workspace"]["base_sha"] == document["base_sha"]
            and original_task["pending"]["lease_generation"] == original_task["lease"]["generation"],
            "BLOCKED_RECOVERY", "failed candidate or lease generation differs")
    launch = controller.launch(run)
    record = launch.load()
    require(not any(row["task_id"] == document["task_id"] for row in launch.active(record)),
            "BLOCKED_EXTERNAL_SESSION", "an active or uncertain session still owns the failed task")
    current_source, contents = source_identity(backend, original_run, document, guard)
    issue = backend.read("issues/" + str(run["catalog"][document["task_id"]]["issue"]))
    branch = original_task["workspace"]["branch"]
    head_query = urllib.parse.quote(run["repository"].split("/", 1)[0] + ":" + branch, safe="")
    pulls = backend.read("pulls?state=all&head=" + head_query + "&per_page=100")
    require(issue.get("state") == "open" and pulls == [], "BLOCKED_RECOVERY", "task is closed or its candidate branch was published")
    workspace = Path(original_task["workspace"]["path"])
    require(backend.git(workspace, "rev-parse", "HEAD") == document["head_sha"]
            and not backend.git(workspace, "status", "--porcelain=v1"),
            "BLOCKED_HEAD_CHANGED", "the failed candidate changed or has uncommitted files")
    backend.git(workspace, "merge-base", "--is-ancestor", document["base_sha"], document["head_sha"])
    root = backend.root / ".project/automation/runs" / run["run_id"]
    with ExitStack() as stack:
        manifest = inventory(root, document["operation_id"], guard)
        records = terminal_sessions(controller, original_run, original_task, document, manifest, stack, guard, contents)
        bindings = {"sessions": records, "evidence_sha256": evidence.digest(manifest)}
        if inspect:
            return {"status": "INSPECTED", "input": dict(document, bindings=bindings), "revision": revision, "epoch": epoch,
                    "paths": len(manifest), "bytes": sum(row.get("size_bytes", 0) for row in manifest.values()), "runtime": "NOT_RUN"}
        require(document.get("bindings") == bindings, "BLOCKED_RECOVERY_EVIDENCE", "evidence changed since the reviewed inspection")
        if saved is None:
            retain(root, manifest, archive_root, guard)
            require(inventory(root, document["operation_id"], guard) == manifest,
                    "BLOCKED_RECOVERY_EVIDENCE", "owned evidence changed while retaining the archive")
            saved = {"schema_version": 1, "kind": "failed-verification", "input": document, "revision": revision, "epoch": epoch,
                     "run": copy.deepcopy(run), "launch": record, "recovery_source": current_source, "manifest": manifest}
            path = archive_root / "archive.json"
            if path.exists():
                retained = raw_json(archive_root, "archive.json")
                require(retained["input"] == document and retained["run"] == run and retained["manifest"] == manifest,
                        "BLOCKED_RECOVERY_EVIDENCE", "another archive already owns this operation")
                saved = retained
            else:
                guard()
                evidence.atomic_json(path, saved, exclusive=True)
        require(saved["input"] == document and saved["manifest"] == manifest,
                "BLOCKED_RECOVERY_EVIDENCE", "archive no longer matches the retained failure")
        for row in manifest.values():
            if row["kind"] == "file":
                require(hash_file(archive_root / "objects", row["sha256"], guard) == row,
                        "BLOCKED_RECOVERY_EVIDENCE", "archive bytes changed")
        if previous and not marker:
            return {"status": "ALREADY_ARCHIVED", "archive_sha256": previous["archive_sha256"], "runtime": "NOT_RUN"}
        if marker is None:
            guard()
            reference = {"operation_id": document["operation_id"], "archive_sha256": evidence.digest(saved),
                         "input_sha256": evidence.digest(document), "revision": revision, "epoch": epoch,
                         "head_sha": document["head_sha"], "source_sha": document["source_sha"], "outcome": "FAILED"}
            next_run = copy.deepcopy(run)
            next_task = next_run["tasks"][document["task_id"]]
            for key in ("workspace", "worker", "review", "pending", "resume_state"):
                next_task.pop(key, None)
            next_task.update(state="BLOCKED", blocker={"code": "BLOCKED_REPAIR_REQUIRED",
                             "reason": "Failed verification retained; release cleanup, accepted-source handoff, and a fresh reviewed candidate are required."})
            next_task.setdefault("failed_verifications", []).append(copy.deepcopy(reference))
            next_run["failure_recovery"] = copy.deepcopy(reference)
            store.save(next_run)
            run.clear()
            run.update(next_run)
        guard()
        store.release(original_task)
        guard()
        next_run = copy.deepcopy(run)
        next_task = next_run["tasks"][document["task_id"]]
        next_task.pop("lease", None)
        next_task["failed_verifications"][-1]["completed_revision"] = next_run["revision"] + 1
        next_run.pop("failure_recovery")
        store.save(next_run)
        run.clear()
        run.update(next_run)
        return {"status": "ARCHIVED_FAILED", "archive_sha256": reference["archive_sha256"], "runtime": "NOT_RUN"}
