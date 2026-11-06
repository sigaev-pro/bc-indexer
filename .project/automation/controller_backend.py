"""Execute controller operations with Git, GitHub, and supervised Codex sessions."""

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

import acceptance
from acceptance_receipts import HISTORICAL, Ledger
from controller import Blocked, in_scope, read_json, require
import evidence
import session_runner

WATCHDOG_GRACE_SECONDS = 5

class RealBackend:
    def __init__(self, source, canonical_root, store, repository, capture_directory=None):
        self.source, self.root = Path(source).resolve(), Path(canonical_root).resolve()
        self.store, self.repository = store, repository
        require(repository == "sigaev-pro/bc-indexer", "BLOCKED_REPOSITORY", "unexpected repository")
        self.captures = Path(capture_directory or self.root / ".project/automation/runs/acceptance").resolve()
        self.ledger = Ledger(store.directory / "accepted", self.read, repository)
        self.provenance = {"checkout_head_sha": self.git(self.source, "rev-parse", "HEAD"),
                           "source_state": "modified" if self.git(self.source, "status", "--porcelain=v1") else "clean",
                           "commit_contains_executing_files": not bool(self.git(self.source, "status", "--porcelain=v1")),
                           "source_sha256": {name: hashlib.sha256((self.source / ".project/automation" / name).read_bytes()).hexdigest()
                                             for name in ("controller.py", "controller_backend.py", "session_runner.py", "acceptance_receipts.py", "evidence.py", "acceptance.py")}}
        self.roles = None
        self.cached_receipts = None

    def command(self, arguments, cwd=None, payload=None, timeout=30):
        try:
            result = subprocess.run(arguments, cwd=cwd or self.root, input=payload,
                                    capture_output=True, text=True, timeout=timeout)
        except (OSError, subprocess.SubprocessError) as error:
            raise Blocked("BLOCKED_ACCESS", f"command could not complete: {arguments[0]}") from error
        require(len(result.stdout.encode()) <= session_runner.LOG_LIMIT, "BLOCKED_OUTPUT", "command output exceeded its bound")
        return result

    def git(self, directory, *arguments):
        result = self.command(["git", *arguments], cwd=directory)
        require(result.returncode == 0, "BLOCKED_GIT", "Git operation failed: " + arguments[0])
        return result.stdout.strip()

    def read(self, path):
        for attempt in range(3):
            result = self.command(["gh", "api", f"repos/{self.repository}/{path}"])
            if result.returncode == 0:
                return acceptance.parse_json(result.stdout)
            if attempt < 2:
                time.sleep(2 ** attempt)
        raise Blocked("BLOCKED_ACCESS", "GitHub read failed after bounded retries: " + path)

    def mutate(self, method, path, body):
        result = self.command(["gh", "api", "--method", method, f"repos/{self.repository}/{path}", "--input", "-"],
                              payload=json.dumps(body))
        require(result.returncode == 0, "BLOCKED_REMOTE_UNCERTAIN",
                "GitHub mutation did not confirm success; reconcile before retry: " + path)
        return acceptance.parse_json(result.stdout)

    def pages(self, path):
        rows = []
        for page in range(1, 11):
            batch = self.read(path + ("&" if "?" in path else "?") + f"per_page=100&page={page}")
            require(isinstance(batch, list), "BLOCKED_REMOTE", "expected a paginated list")
            rows.extend(batch)
            if len(batch) < 100:
                return rows
        raise Blocked("BLOCKED_REMOTE", "GitHub list exceeded the bounded page limit")

    def reconcile_catalog(self, tasks):
        remote = self.git(self.root, "remote", "get-url", "origin")
        require(remote in {f"git@github.com:{self.repository}.git", f"https://github.com/{self.repository}.git"},
                "BLOCKED_REPOSITORY", "origin does not match the authorized repository")
        found = {}
        for issue in self.pages("issues?state=all"):
            if "pull_request" in issue:
                continue
            markers = re.findall(r"<!-- extractor-task:([^ ]+) -->", issue.get("body") or "")
            if not markers:
                continue
            require(len(markers) == 1 and markers[0] in tasks and markers[0] not in found,
                    "BLOCKED_CATALOG", "remote task markers are unknown or duplicated")
            task = markers[0]
            deps = re.search(r"^### Dependencies\s*\n(.*?)(?=^### |\Z)", issue["body"], re.M | re.S)
            require(deps is not None, "BLOCKED_CATALOG", "remote issue has no dependency list")
            actual = re.findall(r"^- ([A-Za-z0-9_.-]+)\s*$", deps[1], re.M)
            require(actual or deps[1].strip().startswith("None."), "BLOCKED_CATALOG", "remote dependency list is malformed")
            require(issue["number"] == tasks[task]["issue"] and actual == tasks[task]["dependencies"],
                    "BLOCKED_CATALOG", "remote issue identity or dependencies changed")
            found[task] = issue["number"]
        require(set(found) == set(tasks), "BLOCKED_CATALOG", "remote task set differs from the pinned 84-node graph")

    def receipts(self, run):
        if self.cached_receipts is not None:
            return self.cached_receipts
        result = {}
        for task in HISTORICAL:
            if (self.ledger.directory / (task + ".json")).exists():
                result[task] = self.ledger.load(task)
            else:
                result[task] = self.ledger.save(self.ledger.historical(task))
        if self.ledger.directory.exists():
            for path in sorted(self.ledger.directory.glob("BOOT-*.json")):
                result[path.stem] = self.ledger.load(path.stem)
        if self.captures.exists():
            for path in sorted(self.captures.glob("*.json")):
                bundle = read_json(path)
                if bundle.get("kind") != "premerge-capture":
                    continue
                task = bundle["expected"]["task_id"]
                require(task in run["catalog"], "BLOCKED_EVIDENCE", "capture names an unknown task")
                if task in result:
                    continue
                try:
                    receipt = self.ledger.from_capture(bundle, {"capture_origin": "retained coordinator capture",
                                                               "importer": self.provenance,
                                                               "bundle_sha256": hashlib.sha256(path.read_bytes()).hexdigest()})
                except Blocked as error:
                    if error.code == "BLOCKED_NOT_MERGED":
                        continue
                    raise
                result[task] = self.ledger.save(receipt)
        self.cached_receipts = result
        for task, receipt in result.items():
            evidence.atomic_json(self.ledger.directory / "revalidations" / (task + "-" + evidence.digest(self.provenance) + ".json"), {
                "task_id": task, "receipt_sha256": evidence.digest(receipt), "executing_source": self.provenance,
                "note": "This revalidation identifies the executing source. Earlier working-source checkout references do not assert that uncommitted files existed in that commit."})
        return result

    def verify_receipt(self, task, receipt, run):
        require(task in run["catalog"], "BLOCKED_EVIDENCE", "unknown dependency receipt")
        return self.ledger.verify(task, receipt)

    def verify_bootstrap(self, tasks):
        self.reconcile_catalog(tasks)
        receipts = self.receipts({"catalog": tasks})
        required = {f"BOOT-{number:02}" for number in range(1, 9)}
        missing = sorted(required - set(receipts))
        return {"verdict": "BLOCKED" if missing else "PASS", "missing_accepted_tasks": missing,
                "accepted_tasks": sorted(receipts), "issue_count": len(tasks),
                "authorization": {"reference": "D86", "stage_limit": "BOOT"},
                "e01_authorized": False, "product_verification_configured": False, "runtime": "NOT_RUN",
                "executing_source": self.provenance}

    def start_gate(self, tasks):
        report = self.verify_bootstrap(tasks)
        receipts = self.receipts({"catalog": tasks})
        require(report["verdict"] == "PASS" and "BOOT-09" in receipts,
                "BLOCKED_BOOTSTRAP", "run requires an accepted BOOT-09 receipt; verification readiness alone is insufficient")
        self.cli()
        actor = self.command(["gh", "api", "user"])
        require(actor.returncode == 0 and json.loads(actor.stdout)["login"] == "sigaev-pro",
                "BLOCKED_ACCESS", "trusted GitHub coordinator authentication is unavailable")
        acceptance.check_protection(self.read("branches/main/protection"))
        environment = self.command([sys.executable, str(self.source / ".project/automation/check_environment.py")])
        require(environment.returncode == 0, "BLOCKED_ACCESS", "current runner preflight failed")
        return {"passed": True, "bootstrap_gate_receipt": receipts["BOOT-09"],
                "environment": acceptance.parse_json(environment.stdout), "source": self.provenance}

    def directory(self, run, operation):
        return self.root / ".project/automation/runs" / run["run_id"] / operation["id"]

    def check_stop(self, run):
        if self.store.stopped(run["run_id"]):
            raise Blocked("STOPPED", "operator requested a stop")
        if time.time() >= run["deadline"]:
            raise Blocked("BUDGET_STOP", "run wall-time budget expired")

    def head(self, task):
        workspace = task["workspace"]
        path = Path(workspace["path"])
        require(path.resolve() == path and path.is_relative_to(self.root / ".project/worktrees"),
                "BLOCKED_WORKTREE", "worktree path escaped the controller-owned directory")
        require(Path(self.git(path, "rev-parse", "--show-toplevel")).resolve() == path
                and Path(self.git(path, "rev-parse", "--path-format=absolute", "--git-common-dir")).resolve() == self.root / ".git"
                and self.git(path, "branch", "--show-current") == workspace["branch"],
                "BLOCKED_WORKTREE", "worktree or branch ownership changed")
        return self.git(path, "rev-parse", "HEAD")

    def candidate(self, run, task):
        head = self.head(task)
        require(head == task["worker"]["commit_sha"], "BLOCKED_HEAD_CHANGED", "candidate changed after its worker result")
        require(not self.git(task["workspace"]["path"], "status", "--porcelain=v1"),
                "BLOCKED_DIRTY_WORKTREE", "uncommitted work must be preserved and reconciled")
        main = self.read("git/ref/heads/main")["object"]["sha"]
        require(main == task["workspace"]["base_sha"], "BLOCKED_BASE_CHANGED", "main changed; update the candidate and obtain fresh review before resuming")
        return head

    def perform(self, action, run, task, operation):
        self.check_stop(run)
        require(self.git(self.root, "remote", "get-url", "origin") in {
            f"git@github.com:{self.repository}.git", f"https://github.com/{self.repository}.git"},
            "BLOCKED_REPOSITORY", "repository transport identity changed")
        stage = run["catalog"][task["task_id"]]["stage"]
        require(in_scope(run, stage), "BLOCKED_AUTHORIZATION", "task exceeds the recorded owner stage limit")
        require(stage == "BOOT", "BLOCKED_UNCONFIGURED_VERIFICATION", "product acceptance adapters and recipes are not configured")
        directory = self.directory(run, operation)
        evidence.durable_directory(directory)
        saved = directory / "operation-result.json"
        if saved.exists():
            record = read_json(saved)
            require(record["sha256"] == evidence.digest(record["result"]), "BLOCKED_EVIDENCE", "operation result changed")
            result = record["result"]
            if action in {"worker", "review", "verify", "publish"}:
                # Read-only comparison on replay; no remote mutation can hide a new head.
                require(self.head(task) == result.get("commit_sha", task.get("worker", {}).get("commit_sha")),
                        "BLOCKED_HEAD_CHANGED", "worktree changed after the retained operation result")
            return result
        result = getattr(self, "do_" + action)(run, task, operation, directory)
        result["operation_id"] = operation["id"]
        evidence.atomic_json(saved, {"result": result, "sha256": evidence.digest(result)})
        return result

    def do_prepare(self, run, task, operation, directory):
        require(not self.git(self.source, "status", "--porcelain=v1", "--untracked-files=no"),
                "BLOCKED_SOURCE", "controller source checkout has uncommitted tracked changes")
        who = self.command(["gh", "api", "user"])
        require(who.returncode == 0 and json.loads(who.stdout)["login"] == "sigaev-pro",
                "BLOCKED_ACCESS", "configured GitHub actor differs from the trusted coordinator")
        plan = directory / "workspace.json"
        if plan.exists():
            workspace = read_json(plan)
        else:
            base = self.read("git/ref/heads/main")["object"]["sha"]
            workspace = {"path": str(self.root / ".project/worktrees" / (run["run_id"] + "-" + task["task_id"].lower())),
                         "branch": "automation/" + run["run_id"] + "/" + task["task_id"].lower(), "base_sha": base}
            evidence.atomic_json(plan, workspace)
        path = Path(workspace["path"])
        if path.exists():
            require((path / ".git").is_file(), "BLOCKED_WORKTREE", "existing worktree directory is incomplete; preserve and inspect it")
            temporary = dict(task, workspace=workspace)
            require(self.head(temporary) == workspace["base_sha"], "BLOCKED_WORKTREE", "existing worktree has unexpected work before assignment")
        else:
            self.git(self.root, "fetch", "origin", "main")
            existing = self.command(["git", "show-ref", "--verify", "--quiet", "refs/heads/" + workspace["branch"]])
            if existing.returncode == 0:
                require(self.git(self.root, "rev-parse", workspace["branch"]) == workspace["base_sha"],
                        "BLOCKED_WORKTREE", "existing task branch contains unexpected work")
                self.git(self.root, "worktree", "add", str(path), workspace["branch"])
            else:
                self.git(self.root, "worktree", "add", "-b", workspace["branch"], str(path), workspace["base_sha"])
        return workspace

    def cli(self):
        lock = read_json(self.source / ".project/agency/toolchain.lock.json")["codex"]
        executable = self.root / lock["executable"]
        require(executable.is_file(), "BLOCKED_ACCESS", "pinned project Codex is missing; do not reinstall or replace the model silently")
        require(self.command([str(executable), "--version"]).stdout.strip() == "codex-cli " + lock["version"],
                "BLOCKED_ACCESS", "project Codex version differs from the pin")
        packages = read_json(executable.parents[2] / "package-lock.json")["packages"]
        package = packages["node_modules/@openai/codex"]
        require(package["version"] == lock["version"] and package["integrity"] == lock["integrity"],
                "BLOCKED_ACCESS", "project CLI package integrity differs from the pin")
        require(any(row.get("integrity") == lock["platform_integrity"] for row in packages.values()),
                "BLOCKED_ACCESS", "runner platform package was not verified")
        return executable

    def role_prompt(self, role, handoff):
        if self.roles is None:
            spec = importlib.util.spec_from_file_location("controller_roles", self.source / ".project/agency/roles.py")
            self.roles = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(self.roles)
        return self.roles.render(role, handoff)

    def supervised(self, run, operation_id, command, cwd, stdin="", timeout=3600, environment=None):
        directory = self.root / ".project/automation/runs" / run["run_id"] / operation_id
        evidence.durable_directory(directory)
        request_path = directory / "request.json"
        request = {"operation_id": operation_id, "command": command, "cwd": str(cwd),
                   "deadline": min(run["deadline"], time.time() + timeout),
                   "timeout_seconds": timeout,
                   "stop_file": str(self.store.directory / "stop" / (run["run_id"] + ".json")),
                   "log_limit": session_runner.LOG_LIMIT, "env": environment or {},
                   "stdin_sha256": hashlib.sha256(stdin.encode()).hexdigest()}
        if request_path.exists():
            saved = read_json(request_path)
            require(all(saved[key] == request[key] for key in ("operation_id", "command", "cwd", "stop_file", "env", "stdin_sha256")),
                    "BLOCKED_SESSION", "retained session request differs from the resumed command")
            request = saved
        else:
            # The stdin file contains instructions, never a saved process environment.
            input_path = directory / "stdin.txt"
            with input_path.open("x", encoding="utf-8") as stream:
                os.chmod(input_path, 0o600)
                stream.write(stdin)
                stream.flush()
                os.fsync(stream.fileno())
            evidence.sync_directory(directory)
            evidence.atomic_json(request_path, request)
        require(session_runner.file_hash(directory / "stdin.txt") == request["stdin_sha256"],
                "BLOCKED_EVIDENCE", "retained session instructions changed")
        result_path = directory / "result.json"
        if not result_path.exists():
            require(not (directory / "started.json").exists(), "BLOCKED_SESSION_INTERRUPTED",
                    "session started without a durable result; preserve work and inspect its process/log evidence")
            process = subprocess.Popen([sys.executable, str(self.source / ".project/automation/session_runner.py"),
                                        "--request", str(request_path), "--lock-fd", str(self.store.lock_fd)],
                                       cwd=self.root, start_new_session=True, pass_fds=(self.store.lock_fd,),
                                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            watchdog_deadline = time.monotonic() + min(timeout, max(0, request["deadline"] - time.time())) + WATCHDOG_GRACE_SECONDS
            while not session_runner.exited_unreaped(process):
                stop = self.store.stopped(run["run_id"])
                expired = time.monotonic() >= watchdog_deadline
                if stop or expired:
                    cleanup = session_runner.watchdog_cleanup(process)
                    evidence.atomic_json(directory / "watchdog.json", {
                        "operation_id": operation_id, "supervisor_pid": process.pid,
                        "reason": "operator stop" if stop else "monotonic watchdog timeout", "cleanup": cleanup})
                    break
                time.sleep(0.1)
            process.wait(timeout=10)
            require(result_path.exists(), "BLOCKED_SESSION_INTERRUPTED", "supervisor exited without a durable result")
        result = read_json(result_path)
        require(result["operation_id"] == operation_id and result["request_sha256"] == evidence.digest(request),
                "BLOCKED_SESSION", "session result belongs to another request")
        for name, key in (("stdout.jsonl", "stdout_sha256"), ("stderr.log", "stderr_sha256")):
            require(session_runner.file_hash(directory / name) == result[key], "BLOCKED_EVIDENCE", "session log changed")
        watchdog = directory / "watchdog.json"
        if watchdog.exists():
            stopped = read_json(watchdog)["reason"] == "operator stop"
            raise Blocked("STOPPED" if stopped else "FAILED_SESSION", "parent watchdog stopped an unresponsive supervisor; inspect retained cleanup evidence")
        if result["reason"] == "stopped":
            raise Blocked("STOPPED", "supervised process stopped; work and logs are retained")
        require(result["reason"] == "completed" and result["exit_code"] == 0,
                "FAILED_SESSION", "bounded process did not complete successfully: " + result["reason"])
        return directory, result

    def model(self, run, task, operation, review=False):
        recipe = run["recipes"][task["task_id"]]
        if review:
            self.candidate(run, task)
        spec = run["catalog"][task["task_id"]]
        handoff = {"task_id": task["task_id"], "stage": spec["stage"], "stage_limit": run["authorization"]["stage_limit"], "authorization_ref": run["authorization"]["reference"],
                   "run_id": run["run_id"], "session_id": operation["id"], "base_commit": task["workspace"]["base_sha"],
                   "spec_commit": task["workspace"]["base_sha"], "worktree": task["workspace"]["path"],
                   "read_only": review, "allowed_paths": [] if review else recipe["allowed_paths"],
                   "acceptance_criteria": [spec["acceptance"]],
                   "dependencies": {dep: {"merge_sha": run["tasks"][dep]["receipt"]["merge_sha"],
                                          "receipt_sha256": evidence.digest(run["tasks"][dep]["receipt"])} for dep in spec["dependencies"]},
                   "task": spec["snapshot"] + "\nController owns all GitHub writes, pushes, merges, and issue closure. Do not perform them. "
                           "Do not start other sessions. Preserve existing work. Return the required schema; do not claim DONE."}
        if review:
            handoff["task"] += "\nReview exact head " + task["worker"]["commit_sha"] + ". Check every acceptance criterion independently."
        elif task.get("review"):
            handoff["task"] += "\nPrior review findings: " + json.dumps(task["review"]["findings"])
        prompt, revision = self.role_prompt("reviewer" if review else recipe["role"], handoff)
        directory = self.directory(run, operation)
        handoff_path = directory / "handoff.json"
        if handoff_path.exists():
            retained = read_json(handoff_path)
            require(retained["handoff"] == handoff and retained["role_revision"] == revision,
                    "BLOCKED_ROLE_CHANGED", "session role or handoff changed on resume")
        else:
            evidence.atomic_json(handoff_path, {"handoff": handoff, "role_revision": revision})
        schema = "review.schema.json" if review else "worker.schema.json"
        command = [str(self.cli()), "exec", "--sandbox", "read-only" if review else "workspace-write",
                   "-c", 'approval_policy="never"', "-c", "sandbox_workspace_write.network_access=true",
                   "--ephemeral", "--json", "--output-schema", str(self.source / ".project/automation" / schema), "-"]
        directory, execution = self.supervised(run, operation["id"], command, task["workspace"]["path"], prompt,
                                               run["limits"]["session_seconds"])
        events = [acceptance.parse_json(line) for line in (directory / "stdout.jsonl").read_text().splitlines() if line.strip()]
        require(any(event.get("type") == "turn.completed" for event in events)
                and not any(event.get("type") in {"turn.failed", "error"} for event in events),
                "FAILED_SESSION", "model did not return a completed turn")
        messages = [event["item"].get("text", "") for event in events if event.get("type") == "item.completed"
                    and event.get("item", {}).get("type") == "agent_message"]
        require(messages, "BLOCKED_EVIDENCE", "model returned no structured task result")
        result = acceptance.parse_json(messages[-1])
        fields = read_json(self.source / ".project/automation" / schema)["properties"]
        require(set(result) == set(fields) and result["task_id"] == task["task_id"] and result["session_id"] == operation["id"],
                "BLOCKED_EVIDENCE", "model result has a different schema, task, or session identity")
        for key, field in fields.items():
            require((field["type"] == "string" and isinstance(result[key], str)) or
                    (field["type"] == "array" and isinstance(result[key], list)
                     and all(isinstance(value, str) and value.strip() for value in result[key])),
                    "BLOCKED_EVIDENCE", "model result contains an invalid field: " + key)
        result.update(role_revision=revision, execution=execution, artifact_directory=str(directory))
        return result

    def do_worker(self, run, task, operation, directory):
        result = self.model(run, task, operation)
        require(result["proposed_status"] == "REVIEW" and not result["blockers"],
                "BLOCKED_WORKER", "worker reported an unresolved blocker")
        head = self.head(task)
        require(re.fullmatch(acceptance.SHA_PATTERN, result["commit_sha"]) and result["commit_sha"] == head
                and head != task["workspace"]["base_sha"], "BLOCKED_EVIDENCE", "worker did not produce the claimed candidate commit")
        path = Path(task["workspace"]["path"])
        require(not self.git(path, "status", "--porcelain=v1"), "BLOCKED_DIRTY_WORKTREE", "worker left uncommitted work")
        self.git(path, "merge-base", "--is-ancestor", task["workspace"]["base_sha"], head)
        actual = self.git(path, "diff", "--no-renames", "--name-only", task["workspace"]["base_sha"], head).splitlines()
        require(actual and set(actual) == set(result["changed_files"]), "BLOCKED_EVIDENCE", "worker changed-file claim differs from Git")
        allowed = run["recipes"][task["task_id"]]["allowed_paths"]
        for name in actual:
            require(name != ".project/evm-transaction-extractor-tz-v2.md"
                    and any(name == prefix or (prefix.endswith("/") and name.startswith(prefix)) for prefix in allowed)
                    and (path / name).resolve().is_relative_to(path),
                    "BLOCKED_PATHS", "candidate changes a forbidden path: " + name)
        require(result["checks"] and result["evidence"], "BLOCKED_EVIDENCE", "worker supplied no checks or artifact references")
        for name in result["evidence"]:
            require(not Path(name).is_absolute() and (path / name).resolve().is_relative_to(path)
                    and (path / name).is_file(), "BLOCKED_EVIDENCE", "worker evidence path is missing or unsafe")
            self.git(path, "cat-file", "-e", head + ":" + name)
        result["verified"] = True
        return result

    def do_review(self, run, task, operation, directory):
        result = self.model(run, task, operation, review=True)
        require(result["head_sha"] == self.candidate(run, task) and result["base_sha"] == task["workspace"]["base_sha"]
                and result["session_id"] != task["worker"]["session_id"] and result["checks"],
                "BLOCKED_REVIEW", "review is stale, self-authored, or lacks checks")
        require(result["verdict"] in {"PASS", "REQUEST_CHANGES"}
                and (result["verdict"] != "PASS" or not result["findings"]),
                "BLOCKED_REVIEW", "review verdict conflicts with its findings")
        result["verified"] = True
        return result

    def do_verify(self, run, task, operation, directory):
        head = self.candidate(run, task)
        recipe = run["recipes"].get(task["task_id"])
        require(recipe and recipe.get("required_files") and recipe.get("commands"),
                "BLOCKED_UNCONFIGURED_VERIFICATION", "no concrete independent verification recipe exists")
        workspace = Path(task["workspace"]["path"])
        files = {}
        for name in recipe["required_files"]:
            require((workspace / name).is_file() and (workspace / name).resolve().is_relative_to(workspace),
                    "BLOCKED_VERIFICATION", "required task output is missing: " + name)
            self.git(workspace, "cat-file", "-e", head + ":" + name)
            files[name] = hashlib.sha256((workspace / name).read_bytes()).hexdigest()
        checks = []
        for number, command in enumerate(recipe["commands"]):
            require(isinstance(command, list) and command and all(isinstance(arg, str) for arg in command),
                    "BLOCKED_VERIFICATION", "verification command must be an argument array")
            _, execution = self.supervised(run, operation["id"] + "-check-" + str(number), command, workspace,
                                           timeout=600, environment={"BOOTSTRAP_HEAD_SHA": head,
                                                                   "BOOTSTRAP_BASE_SHA": task["workspace"]["base_sha"]})
            checks.append({"command": command, "execution": execution})
        if recipe.get("bootstrap_gate"):
            report = self.verify_bootstrap(run["catalog"])
            require(report["verdict"] == "PASS", "BLOCKED_BOOTSTRAP", "bootstrap dependencies remain unaccepted")
        require(self.candidate(run, task) == head, "BLOCKED_HEAD_CHANGED", "verification changed the candidate")
        return {"passed": True, "head_sha": head, "files": files, "checks": checks,
                "recipe_sha256": evidence.digest(recipe), "runtime": "NOT_RUN"}

    def do_publish(self, run, task, operation, directory):
        head = self.candidate(run, task)
        require(task["verification"]["passed"] is True and task["review"]["verdict"] == "PASS",
                "BLOCKED_EVIDENCE", "publication requires independent verification and review")
        branch = task["workspace"]["branch"]
        remote = self.git(self.root, "ls-remote", "origin", "refs/heads/" + branch)
        if not remote or remote.split()[0] != head:
            self.git(task["workspace"]["path"], "push", "origin", head + ":refs/heads/" + branch)
        require(self.git(self.root, "ls-remote", "origin", "refs/heads/" + branch).split()[0] == head,
                "BLOCKED_REMOTE", "pushed branch does not match the candidate")
        matches = self.pages("pulls?state=all&head=sigaev-pro:" + branch)
        require(len(matches) <= 1, "BLOCKED_REMOTE", "task branch has ambiguous pull requests")
        if matches:
            pull = matches[0]
        else:
            self.candidate(run, task)
            pull = self.mutate("POST", "pulls", {"head": branch, "base": "main", "title": task["task_id"] + ": " + run["catalog"][task["task_id"]]["snapshot"].splitlines()[0].split(" — ")[-1],
                "body": f"<!-- extractor-task:{task['task_id']} -->\n<!-- extractor-operation:{operation['id']} -->\n\nImplements the assigned bootstrap acceptance criteria. The controller closes the task only after verified acceptance."})
        require(pull["state"] == "open" and pull["head"]["sha"] == head
                and f"<!-- extractor-task:{task['task_id']} -->" in pull["body"],
                "BLOCKED_REMOTE", "existing pull request does not match the task candidate")
        report = {"schema_version": 1, "repository": self.repository, "task_id": task["task_id"],
                  "head_sha": head, "base_sha": task["workspace"]["base_sha"],
                  "implementer_session": task["worker"]["session_id"], "reviewer_session": task["review"]["session_id"],
                  "read_only": True, "verdict": "PASS", "findings": [], "checks": task["review"]["checks"],
                  "unverified": task["review"]["unverified"], "operation_id": operation["id"]}
        body = acceptance.REVIEW_MARKER + "\n```json\n" + json.dumps(report, indent=2) + "\n```"
        comments = [c for c in self.pages(f"issues/{pull['number']}/comments") if c["body"] == body]
        require(len(comments) <= 1, "BLOCKED_REMOTE", "independent report is duplicated")
        self.candidate(run, task)
        comment = comments[0] if comments else self.mutate("POST", f"issues/{pull['number']}/comments", {"body": body})
        require(comment["body"] == body and comment["user"]["login"] == "sigaev-pro",
                "BLOCKED_REMOTE", "published review differs from the recorded reviewer result")
        description = "Review SHA256: " + hashlib.sha256(comment["body"].encode()).hexdigest()
        statuses = [s for s in self.read(f"commits/{head}/statuses?per_page=100") if s["context"] == acceptance.REVIEW_CONTEXT]
        latest = max(statuses, key=lambda row: row["id"]) if statuses else None
        if not latest or not (latest["state"] == "success" and latest["target_url"] == comment["html_url"] and latest["description"] == description
                              and latest["creator"]["login"] == "sigaev-pro"):
            self.candidate(run, task)
            self.mutate("POST", f"statuses/{head}", {"state": "success", "context": acceptance.REVIEW_CONTEXT,
                                                    "target_url": comment["html_url"], "description": description})
        return {"number": pull["number"], "comment_id": comment["id"], "head_sha": head,
                "base_sha": task["workspace"]["base_sha"]}

    def do_merge(self, run, task, operation, directory):
        expected = {"repository": self.repository, "task_id": task["task_id"], "pull_request_number": task["pull"]["number"],
                    "review_comment_id": task["pull"]["comment_id"], "coordinator_login": "sigaev-pro",
                    "implementer_session": task["worker"]["session_id"], "head_sha": task["worker"]["commit_sha"],
                    "base_sha": task["workspace"]["base_sha"]}
        pull = self.read(f"pulls/{expected['pull_request_number']}")
        intent_path = directory / "merge-intent.json"
        if not pull["merged"]:
            self.candidate(run, task)
            deadline = min(run["deadline"], time.time() + 900)
            while True:
                self.check_stop(run)
                checks = self.read(f"commits/{expected['head_sha']}/check-runs?filter=latest&per_page=100")
                matches = [c for c in checks["check_runs"] if c["name"] == acceptance.CI_CONTEXT and c["app"]["id"] == acceptance.ACTIONS_APP_ID]
                latest = max(matches, key=lambda row: row["id"]) if matches else None
                if latest and latest["status"] == "completed":
                    break
                require(time.time() < deadline, "BLOCKED_CI_TIMEOUT", "CI did not finish within the bounded wait")
                time.sleep(2)
            self.candidate(run, task)
            try:
                captured = evidence.capture(expected, directory / "evidence")
            except (ValueError, KeyError, TypeError) as error:
                raise Blocked("BLOCKED_ACCEPTANCE", str(error)) from error
            bundle = read_json(captured["path"])
            evidence.atomic_json(intent_path, {"capture": bundle, "provenance": self.provenance})
            self.check_stop(run)
            self.mutate("PUT", f"pulls/{expected['pull_request_number']}/merge", {"sha": expected["head_sha"], "merge_method": "merge"})
        require(intent_path.exists(), "BLOCKED_EVIDENCE", "merged PR has no retained pre-merge intent; do not invent acceptance")
        intent = read_json(intent_path)
        receipt = self.ledger.from_capture(intent["capture"], intent["provenance"])
        receipt["task_verification"] = task["verification"]
        receipt["worker_session"] = task["worker"]["session_id"]
        receipt["reviewer_session"] = task["review"]["session_id"]
        saved = self.ledger.save(receipt)
        self.cached_receipts[task["task_id"]] = saved
        # This portable evidence file contains the full capture and actual merge facts.
        evidence.atomic_json(directory / "evidence" / "accepted-receipt.json", {"receipt": saved, "sha256": evidence.digest(saved)})
        return saved

    def do_close(self, run, task, operation, directory):
        self.verify_receipt(task["task_id"], task["receipt"], run)
        number = run["catalog"][task["task_id"]]["issue"]
        issue = self.read(f"issues/{number}")
        require(issue["number"] == number and f"<!-- extractor-task:{task['task_id']} -->" in issue["body"],
                "BLOCKED_CATALOG", "issue identity changed before closure")
        if issue["state"] != "closed":
            self.check_stop(run)
            issue = self.mutate("PATCH", f"issues/{number}", {"state": "closed", "state_reason": "completed"})
        require(issue["state"] == "closed", "BLOCKED_REMOTE", "issue closure was not confirmed")
        return {"issue": number, "state": "closed", "receipt_sha256": evidence.digest(task["receipt"])}
