"""Disposable Git/evidence recovery controls; no product or live verification claim."""

import copy
from contextlib import redirect_stdout
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

from controller import AUTHORIZATIONS, Blocked, Controller, Store, catalog
from controller_backend import RealBackend
import controller
import controller_backend
import controller_handoff
import evidence
import verification_recovery as recovery
from test_controller import Crash


TASK, RUN, OP = "E01-01.builds", "run_fixture_recovery", "a" * 32
SOURCE = Path(__file__).resolve().parents[2]


class Backend(RealBackend):
    """Real local Git; the accepted main read and Docker/process observations are isolated."""
    def command(self, arguments, cwd=None, payload=None, timeout=30):
        if arguments[0] == "ps":
            return subprocess.CompletedProcess(arguments, 0, self.processes, "")
        return subprocess.run(arguments, cwd=cwd or self.root, input=payload, capture_output=True, text=True, timeout=timeout)

    def read(self, path):
        if path == "git/ref/heads/main":
            return {"object": {"sha": self.accepted}}
        if path.startswith("issues/"):
            return {"state": self.issue_state}
        if path.startswith("pulls?"):
            return self.pulls
        raise AssertionError("unexpected external read: " + path)

    def reconcile_catalog(self, _catalog):
        pass

    def start_gate(self, _catalog):
        return {"passed": True, "bootstrap_gate_receipt": {"synthetic": True}}

    def verify_receipt(self, *_args):
        pass

    def snapshot_recipes(self, recipes):
        return copy.deepcopy(recipes)

    def eligible_recipe(self, task_id, recipes):
        return task_id == TASK and task_id in recipes


class VerificationRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve() / "repository"
        self.root.mkdir()
        self.store = Store(self.root / ".project/automation/state")
        self.backend = Backend.__new__(Backend)
        self.backend.root = self.backend.source = self.root
        self.backend.store = self.store
        self.backend.launch_bound = None
        self.backend.processes = ""
        self.backend.issue_state, self.backend.pulls = "open", []
        self.git("init", "-q")
        self.write(".gitignore", ".project/automation/state/\n.project/automation/runs\n")
        self.write(".project/AGENTS.md", "Synthetic source only.\n")
        self.write(".project/automation/frozen.py", "# Old reviewed implementation.\r\n")
        self.write(".project/automation/build_task_verifier.py", 'PACK_MODULES = ("frozen",)\n'
                   'PACK_FILES = tuple(".project/automation/" + name + ".py" for name in PACK_MODULES) + '
                   '(".project/bootstrap/components/candidate-lock.json",)\n')
        for name in ("ISSUE_CATALOG.md", "ISSUE_MAP.md"):
            self.write(".project/docs/" + name, (SOURCE / ".project/docs" / name).read_text())
        self.declared = {TASK: {"commands": [["python3", "check.py"]], "required_files": ["product.txt"]}}
        self.write(".project/automation/task-recipes.json", evidence.canonical_bytes(self.declared).decode())
        images = {name: {"pull_reference": "docker.io/library/" + name + "@sha256:" + "1" * 64}
                  for name in ("golang-build-image", "debian-runtime-image", "nats-runtime-image")}
        self.write(".project/bootstrap/components/candidate-lock.json", evidence.canonical_bytes({"components": images}).decode())
        self.commit("old source")
        self.old = self.git("rev-parse", "HEAD")
        self.backend.accepted = self.old
        self.frozen = self.backend.configuration()
        self.workspace = self.root.parent / "failed-candidate"
        self.git("worktree", "add", "-q", "-b", "candidate", str(self.workspace), self.old)
        (self.workspace / "product.txt").write_text("Synthetic failed product candidate.\n")
        self.commit("candidate", self.workspace)
        self.head = self.git("rev-parse", "HEAD", cwd=self.workspace)
        self.write(".project/automation/frozen.py", "# Newly reviewed recovery implementation.\n")
        self.write(".project/automation/verification_recovery.py", "# Synthetic source coverage marker.\n")
        self.write(".project/automation/build_task_verifier.py", 'PACK_MODULES = ("frozen", "verification_recovery")\n'
                   'PACK_FILES = tuple(".project/automation/" + name + ".py" for name in PACK_MODULES) + '
                   '(".project/bootstrap/components/candidate-lock.json",)\n')
        self.commit("accepted recovery")
        self.backend.accepted = self.git("rev-parse", "HEAD")
        self.driver = Controller(self.store, self.backend)
        self.recipes = copy.deepcopy(self.declared)
        lock_name = ".project/bootstrap/components/candidate-lock.json"
        self.recipes[TASK]["trusted_pack"] = {lock_name: hashlib.sha256((self.root / lock_name).read_bytes()).hexdigest(),
                                               ".project/automation/frozen.py": self.frozen["files"][".project/automation/frozen.py"]}
        graph = catalog(self.root)
        self.run = {"schema_version": 1, "run_id": RUN, "repository": "sigaev-pro/bc-indexer", "status": "BLOCKED",
                    "revision": 9, "configuration_epoch": 5, "configuration_source": self.frozen,
                    "authorization": copy.deepcopy(AUTHORIZATIONS["D93"]), "catalog": graph, "catalog_sha256": evidence.digest(graph),
                    "recipes": self.recipes, "recipes_sha256": evidence.digest(self.recipes), "invocations": 3,
                    "budget_baseline": 0, "deadline": None, "limits": {"seconds": None, "invocations": None,
                    "implementation_attempts": None, "model_sessions": 3, "session_seconds": 3600},
                    "budget_history": [{"historical": True}], "tasks": {TASK: {"task_id": TASK, "attempts": 6,
                    "state": "FAILED", "resume_state": "VERIFYING", "blocker": {"code": "FAILED_SESSION", "reason": "retained failure"},
                    "workspace": {"path": str(self.workspace), "base_sha": self.old, "branch": "candidate"},
                    "worker": {"commit_sha": self.head, "session_id": "synthetic-author", "invocation_id": "old-author"},
                    "review": {"session_id": "synthetic-reviewer", "verdict": "PASS", "head_sha": self.head},
                    "pending": {"id": OP, "action": "verify", "lease_generation": 1, "failure": {"code": "FAILED_SESSION"}},
                    "review_history": [{"verdict": "REQUEST_CHANGES"}]},
                    "BOOT-09": {"task_id": "BOOT-09", "state": "DONE", "attempts": 0, "receipt": {"retained": "unchanged"}}}}
        for dependency in graph[TASK]["dependencies"]:
            self.run["tasks"].setdefault(dependency, {"task_id": dependency, "state": "DONE", "attempts": 0, "receipt": {"synthetic": True}})
        with self.store.lock():
            self.store.lease(self.run, self.run["tasks"][TASK])
            self.store.save(self.run)
            evidence.atomic_json(self.driver.launch(self.run).path, {"schema_version": 1, "authorization": AUTHORIZATIONS["D93"],
                "run_id": RUN, "initial_observations_imported": True, "initial_held_invocations": 3,
                "sessions": {"author": {"task_id": TASK, "role": "implementer", "status": "completed", "cleanup_confirmed": True}}})
        self.revision = self.run["revision"]
        self.document = {"schema_version": 1, "run_id": RUN, "task_id": TASK, "operation_id": OP,
                         "head_sha": self.head, "base_sha": self.old, "source_sha": self.old,
                         "recovery_source_sha": self.backend.accepted, "diagnosis": "Synthetic reviewed maintenance diagnosis."}
        self.execution = self.root / ".project/automation/runs" / RUN
        (self.execution / OP).mkdir(parents=True)
        self.sessions = []
        self.session(0, ["python3", "check.py"], 0)
        directory = self.execution / (OP + "-check-1")
        self.session(1, [sys.executable, "-I", str(self.root / ".project/automation/build_task_verifier.py"),
                        "--candidate", str(self.workspace), "--request", str(directory / "request.json")], 1, images)
        self.members = patch.object(recovery.build_resources, "members", return_value=[])
        self.members.start()
        self.addCleanup(self.members.stop)

    def git(self, *arguments, cwd=None):
        result = subprocess.run(["git", *arguments], cwd=cwd or self.root, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip()

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def commit(self, message, cwd=None):
        self.git("add", ".", cwd=cwd)
        self.git("-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", message, cwd=cwd)

    def session(self, number, command, exit_code, images=None):
        identifier = OP + "-check-" + str(number)
        directory = self.execution / identifier
        directory.mkdir()
        (directory / "candidate-groups").mkdir()
        for name, content in (("stdin.txt", b""), ("stdout.jsonl", b"Synthetic failed-verifier output.\n"), ("stderr.log", b""), ("session.lock", b"")):
            (directory / name).write_bytes(content)
        request = {"operation_id": identifier, "command": command, "cwd": str(self.workspace),
                   "env": {"BOOTSTRAP_HEAD_SHA": self.head, "BOOTSTRAP_BASE_SHA": self.old},
                   "stop_file": str(self.store.directory / "stop" / (RUN + ".json")),
                   "stdin_sha256": hashlib.sha256(b"").hexdigest()}
        result = {"operation_id": identifier, "reason": "completed", "exit_code": exit_code, "started_at": 100,
                  "cleanup_complete": True, "termination": "already-exited", "candidate_cleanup": {"complete": True, "groups": []},
                  "stdout_sha256": hashlib.sha256((directory / "stdout.jsonl").read_bytes()).hexdigest(),
                  "stderr_sha256": hashlib.sha256(b"").hexdigest()}
        if images:
            spec = {"schema_version": 1, "run_id": RUN, "task_id": TASK, "operation_id": identifier, "nonce": "2" * 64,
                    "images": [row["pull_reference"] for row in images.values()]}
            request["owned_docker"] = spec
            inventory = {"schema_version": 1, "nonce": spec["nonce"], "intents": []}
            cleanup = {"schema_version": 1, "nonce": spec["nonce"], "complete": True, "remaining": [],
                       "resources": [], "creation_inventory": inventory}
            result["docker_cleanup"] = cleanup
            evidence.atomic_json(directory / "docker-resources.json", inventory)
            evidence.atomic_json(directory / "docker-cleanup.json", cleanup)
        result["request_sha256"] = evidence.digest(request)
        evidence.atomic_json(directory / "request.json", request)
        evidence.atomic_json(directory / "result.json", result)
        evidence.atomic_json(directory / "started.json", {"operation_id": identifier, "request_sha256": evidence.digest(request),
                                                          "supervisor_pid": 90000001 + number, "started_at": 100})
        evidence.atomic_json(directory / "heartbeat.json", {"operation_id": identifier, "supervisor_pid": 90000001 + number,
                                                            "process_group": 90000011 + number, "started_at": 100})
        self.sessions.append(directory)

    def inspect(self):
        return recovery.archive(self.driver, self.run, self.document, self.revision, 5, inspect=True)["input"]

    def apply(self, document):
        return recovery.archive(self.driver, self.run, document, self.revision, 5)

    def store_bytes(self):
        return {str(path.relative_to(self.store.directory)): path.read_bytes()
                for path in self.store.directory.rglob("*") if path.is_file()}

    def test_inspect_is_read_only_and_apply_retains_failure_counters_receipts_and_bytes(self):
        with self.store.lock():
            before = self.store_bytes()
            document = self.inspect()
            self.assertEqual(self.store_bytes(), before)
            old_task = copy.deepcopy(self.run["tasks"][TASK])
            result = self.apply(document)
            self.assertEqual(result["status"], "ARCHIVED_FAILED")
            task = self.run["tasks"][TASK]
            self.assertEqual(task["state"], "BLOCKED")
            self.assertEqual(task["blocker"]["code"], "BLOCKED_REPAIR_REQUIRED")
            self.assertEqual(task["attempts"], 6)
            self.assertEqual(self.run["invocations"], 3)
            self.assertEqual(self.run["budget_history"], [{"historical": True}])
            self.assertEqual(self.run["tasks"]["BOOT-09"]["receipt"], {"retained": "unchanged"})
            archive = recovery.raw_json(self.store.directory / "failed-verifications" / RUN / OP, "archive.json")
            self.assertEqual(archive["run"]["tasks"][TASK], old_task)
            self.assertTrue(read_lease(self.store)["released"])
            after = self.store_bytes()
            self.assertEqual(self.apply(document)["status"], "ALREADY_ARCHIVED")
            self.assertEqual(self.store_bytes(), after)
            controller_handoff.configuration(self.driver, self.run, self.recipes, self.run["revision"], 5)
            self.assertEqual(self.run["configuration_epoch"], 6)
            self.assertEqual(self.run["tasks"][TASK]["failed_verifications"][0]["outcome"], "FAILED")

    def test_stale_identity_state_and_stop_are_rejected_without_state_changes(self):
        mutations = [("revision", lambda: setattr(self, "revision", self.revision - 1)),
                     ("head", lambda: self.document.update(head_sha="f" * 40)),
                     ("base", lambda: self.document.update(base_sha="f" * 40)),
                     ("operation", lambda: self.document.update(operation_id="f" * 32)),
                     ("epoch", lambda: self.run.update(configuration_epoch=4)),
                     ("published", lambda: self.run["tasks"][TASK].update(pull={"number": 1})),
                     ("accepted", lambda: self.run["tasks"][TASK].update(receipt={"accepted": True})),
                     ("running", lambda: self.run["tasks"][TASK].update(state="VERIFYING"))]
        original_run, original_doc, revision = copy.deepcopy(self.run), copy.deepcopy(self.document), self.revision
        with self.store.lock():
            for label, mutation in mutations:
                with self.subTest(label=label):
                    self.run, self.document, self.revision = copy.deepcopy(original_run), copy.deepcopy(original_doc), revision
                    mutation()
                    before = self.store_bytes()
                    with self.assertRaises(Blocked):
                        self.inspect()
                    self.assertEqual(before, self.store_bytes())
            self.run, self.document, self.revision = original_run, original_doc, revision
            self.store.stop(RUN)
            with self.assertRaises(Blocked) as caught:
                self.inspect()
            self.assertEqual(caught.exception.code, "STOPPED")
            self.assertTrue(self.store.stopped(RUN))

    def test_old_source_recipe_and_new_source_must_match_reviewed_git_objects(self):
        with self.store.lock():
            with self.assertRaises(Blocked):
                self.backend.check_configuration(self.run)
            self.inspect()
            for name in ("old-source", "old-recipe", "unaccepted", "dirty", "wrong-descendant"):
                with self.subTest(name=name):
                    run, accepted = copy.deepcopy(self.run), self.backend.accepted
                    if name == "old-source":
                        self.run["configuration_source"]["files"][".project/automation/frozen.py"] = "0" * 64
                    elif name == "old-recipe":
                        self.run["recipes"][TASK]["commands"] = [["forged"]]
                        self.run["recipes_sha256"] = evidence.digest(self.run["recipes"])
                    elif name == "unaccepted":
                        self.backend.accepted = self.old
                    elif name == "dirty":
                        self.write(".project/automation/frozen.py", "changed\n")
                    else:
                        self.document["source_sha"] = self.head
                    with self.assertRaises(Blocked):
                        self.inspect()
                    self.run, self.backend.accepted = run, accepted
                    self.document["source_sha"] = self.old
                    self.git("restore", ".project/automation/frozen.py")

    def test_old_pack_membership_comes_from_old_git_not_the_new_verifier(self):
        old_names = set(self.run["recipes"][TASK]["trusted_pack"])
        new_names = recovery.old_pack_paths((self.root / ".project/automation/build_task_verifier.py").read_bytes())
        self.assertNotEqual(old_names, new_names)
        self.assertIn(".project/automation/verification_recovery.py", new_names - old_names)
        with self.store.lock():
            self.inspect()
            self.run["recipes"][TASK]["trusted_pack"].pop(".project/automation/frozen.py")
            self.run["recipes_sha256"] = evidence.digest(self.run["recipes"])
            with self.assertRaisesRegex(Blocked, "membership"):
                self.inspect()
            self.run["recipes"][TASK].pop("trusted_pack")
            self.run["recipes_sha256"] = evidence.digest(self.run["recipes"])
            with self.assertRaisesRegex(Blocked, "missing its required pack"):
                self.inspect()

    def test_clean_accepted_but_unrelated_source_is_rejected(self):
        self.git("checkout", "--orphan", "unrelated")
        self.commit("unrelated accepted test source")
        self.backend.accepted = self.git("rev-parse", "HEAD")
        self.document["recovery_source_sha"] = self.backend.accepted
        with self.store.lock(), self.assertRaises(Blocked):
            self.inspect()

    def test_closed_issue_or_published_branch_is_rejected(self):
        with self.store.lock():
            self.backend.issue_state = "closed"
            with self.assertRaises(Blocked):
                self.inspect()
            self.backend.issue_state = "open"
            self.backend.pulls = [{"number": 123, "state": "open"}]
            with self.assertRaises(Blocked):
                self.inspect()

    def test_changed_source_requires_handoff_and_same_source_allows_new_candidate_in_same_epoch(self):
        with self.store.lock():
            document = self.inspect()
            self.apply(document)
            attestation = {"schema_version": 1, "run_id": RUN, "authorization_ref": "D93", "coordinator_agent_id": "/root",
                           "configuration_epoch": 5, "task_id": TASK, "head_sha": "b" * 40, "base_sha": self.backend.accepted,
                           "worktree": str(self.root.parent / "repaired"), "branch": "repaired", "implementer_invocation": "new-author"}
            with self.assertRaises(Blocked) as caught:
                controller_handoff.adopt(self.driver, self.run, attestation, self.run["revision"], 5)
            self.assertEqual(caught.exception.code, "BLOCKED_CONFIGURATION")
            # Model a later failure already using the accepted recovery source.
            archive = recovery.raw_json(self.store.directory / "failed-verifications" / RUN / OP, "archive.json")
            self.run = copy.deepcopy(archive["run"])
            self.run["configuration_source"] = self.backend.configuration()
            pack = recovery.old_pack_paths((self.root / ".project/automation/build_task_verifier.py").read_bytes())
            self.run["recipes"][TASK]["trusted_pack"] = {name: hashlib.sha256((self.root / name).read_bytes()).hexdigest() for name in pack}
            self.run["recipes_sha256"] = evidence.digest(self.run["recipes"])
            self.document["source_sha"] = self.backend.accepted
            import shutil
            shutil.rmtree(self.store.directory / "failed-verifications")
            evidence.atomic_json(self.store.directory / "leases" / (TASK + ".json"), self.run["tasks"][TASK]["lease"])
            self.store.save(self.run)
            self.revision = self.run["revision"]
            self.apply(self.inspect())
            launch = self.driver.launch(self.run)
            record = launch.load()
            record["sessions"]["new-author"] = {"invocation_id": "new-author", "task_id": TASK, "role": "implementer",
                "status": "completed", "cleanup_confirmed": True, "canonical_agent_id": "/synthetic/new-author",
                "origin": "coordinator-managed", "observation": {"synthetic": True}}
            launch.save(record)
            same_head = dict(attestation, head_sha=self.head)
            with self.assertRaisesRegex(Blocked, "different reviewed candidate"):
                controller_handoff.adopt(self.driver, self.run, same_head, self.run["revision"], 5)
            with patch.object(self.backend, "external_candidate", return_value={"commit_sha": attestation["head_sha"], "evidence": ["synthetic"]}):
                controller_handoff.adopt(self.driver, self.run, attestation, self.run["revision"], 5)
            self.assertEqual(self.run["configuration_epoch"], 5)
            self.assertEqual(self.run["tasks"][TASK]["state"], "REVIEW")
            self.assertEqual(self.run["tasks"][TASK]["failed_verifications"][0]["outcome"], "FAILED")

    def test_cli_inspection_leaves_state_unchanged_and_returns_only_bindings(self):
        path = self.root.parent / "diagnosis.json"
        path.write_bytes(evidence.canonical_bytes(self.document))
        arguments = ["controller.py", "archive-failed-verification", "--run-id", RUN, "--authorization-ref", "D93",
                     "--stage-limit", "E07", "--expected-revision", str(self.revision), "--expected-epoch", "5",
                     "--input", str(path), "--inspect"]
        before = self.store_bytes()
        output = io.StringIO()
        with patch.object(controller, "ROOT", self.root), patch.object(evidence, "repository_root", return_value=self.root), \
                patch.object(controller_backend, "RealBackend", return_value=self.backend), patch.object(sys, "argv", arguments), redirect_stdout(output):
            self.assertEqual(controller.main(), 0)
        result = json.loads(output.getvalue())
        self.assertEqual(result["status"], "INSPECTED")
        self.assertEqual(set(result["input"]["bindings"]), {"sessions", "evidence_sha256"})
        self.assertNotIn("manifest", result)
        self.assertEqual(before, self.store_bytes())

    def cli_resume(self, automatic=False):
        arguments = ["controller.py", "resume", "--run-id", RUN, "--authorization-ref", "D93", "--stage-limit", "E07"]
        if automatic:
            arguments.append("--automatic")
        output = io.StringIO()
        with patch.object(controller, "ROOT", self.root), patch.object(evidence, "repository_root", return_value=self.root), \
                patch.object(controller_backend, "RealBackend", side_effect=AssertionError("resume must not construct a backend")), \
                patch.object(sys, "argv", arguments), redirect_stdout(output):
            self.assertEqual(controller.main(), 1)
        return json.loads(output.getvalue())

    def test_operator_stop_before_and_after_state_swap_requires_separate_explicit_acknowledgement(self):
        with self.store.lock():
            document = self.inspect()
        original_atomic = evidence.atomic_json
        for phase in ("before", "after"):
            with self.subTest(phase=phase):
                if phase == "after":
                    def stop_after_swap(path, value, **kwargs):
                        original_atomic(path, value, **kwargs)
                        if "failure_recovery" in value:
                            self.store.stop(RUN)
                    with self.store.lock(), patch.object(evidence, "atomic_json", side_effect=stop_after_swap), self.assertRaises(Blocked):
                        self.apply(document)
                    self.run = self.store.load(RUN)
                    self.assertIn("failure_recovery", self.run)
                else:
                    self.store.stop(RUN)
                before = self.store_bytes()
                self.assertEqual(self.cli_resume(automatic=True)["code"], "BLOCKED_RECOVERY")
                self.assertEqual(before, self.store_bytes())
                with self.store.lock():
                    for inspect in (False, True):
                        with self.assertRaises(Blocked) as caught:
                            recovery.archive(self.driver, self.run, document, self.revision, 5, inspect=inspect)
                        self.assertEqual(caught.exception.code, "STOPPED")
                self.assertEqual(before, self.store_bytes())
                self.assertFalse(read_lease(self.store)["released"])
                self.assertEqual(self.cli_resume()["code"], "BLOCKED_RECOVERY")
                self.assertFalse(self.store.stopped(RUN))
                self.assertEqual({k: v for k, v in before.items() if not k.startswith("stop/")}, self.store_bytes())
                after_ack = self.store_bytes()
                self.assertEqual(self.cli_resume()["code"], "BLOCKED_RECOVERY")
                self.assertEqual(after_ack, self.store_bytes())
        with self.store.lock():
            self.assertEqual(self.apply(document)["status"], "ARCHIVED_FAILED")
        self.assertTrue(read_lease(self.store)["released"])

    def test_new_stop_during_explicit_acknowledgement_is_preserved(self):
        self.store.stop(RUN)
        original = Store.clear_stop
        def new_stop(store, run_id, observed):
            original(store, run_id, observed)
            store.stop(run_id)
        before = self.store.run_path(RUN).read_bytes()
        with patch.object(Store, "clear_stop", new_stop):
            self.assertEqual(self.cli_resume()["code"], "STOPPED")
        self.assertTrue(self.store.stopped(RUN))
        self.assertEqual(before, self.store.run_path(RUN).read_bytes())

    def test_new_stop_before_deletion_is_not_acknowledged_as_the_observed_stop(self):
        self.store.stop(RUN)
        original = Store.clear_stop
        def newer_stop(store, run_id, *args):
            store.stop(run_id)
            return original(store, run_id, *args)
        before = self.store.run_path(RUN).read_bytes()
        with patch.object(Store, "clear_stop", newer_stop):
            self.assertEqual(self.cli_resume()["code"], "STOPPED")
        self.assertTrue(self.store.stopped(RUN))
        self.assertEqual(before, self.store.run_path(RUN).read_bytes())

    def test_stop_writer_and_acknowledgement_coordinate_only_the_short_delete_boundary(self):
        self.store.stop(RUN)
        path = self.store.directory / "stop" / (RUN + ".json")
        original = Path.unlink
        started, finished, errors = threading.Event(), threading.Event(), []
        def stop_writer():
            started.set()
            try:
                self.store.stop(RUN)
            except BaseException as error:
                errors.append(error)
            finally:
                finished.set()
        writer = threading.Thread(target=stop_writer)
        def concurrent_stop(target, *args, **kwargs):
            if target == path:
                writer.start()
                self.assertTrue(started.wait(2))
                self.assertFalse(finished.wait(0.05), "writer must share the short acknowledgement lock")
            return original(target, *args, **kwargs)
        try:
            with patch.object(Path, "unlink", concurrent_stop):
                self.assertIn(self.cli_resume()["code"], {"STOPPED", "BLOCKED_RECOVERY"})
        finally:
            if writer.ident is not None:
                writer.join(3)
        self.assertFalse(writer.is_alive())
        self.assertEqual(errors, [])
        self.assertTrue(self.store.stopped(RUN))

    def test_no_stop_resume_never_acknowledges_a_marker(self):
        before = self.store_bytes()
        with patch.object(Store, "clear_stop", side_effect=AssertionError("no stop was observed")):
            self.assertEqual(self.cli_resume()["code"], "BLOCKED_RECOVERY")
            self.assertEqual(self.cli_resume(automatic=True)["code"], "BLOCKED_RECOVERY")
        self.assertEqual(before, self.store_bytes())

    def set_recorded_driver(self, path):
        directory = self.sessions[-1]
        request = recovery.raw_json(directory, "request.json")
        request["command"][2] = str(path)
        evidence.atomic_json(directory / "request.json", request)
        for name in ("started.json", "result.json"):
            value = recovery.raw_json(directory, name)
            value["request_sha256"] = evidence.digest(request)
            evidence.atomic_json(directory / name, value)

    def test_historical_driver_uses_its_recorded_accepted_worktree(self):
        producer = self.root.parent / "old-accepted-source"
        self.git("worktree", "add", "-q", "--detach", str(producer), self.old)
        self.set_recorded_driver(producer / ".project/automation/build_task_verifier.py")
        with self.store.lock():
            document = self.inspect()
            binding = document["bindings"]["sessions"][-1]["producer"]
            self.assertEqual(binding["path"], str(producer))
            self.assertEqual(binding["source_sha"], self.old)
            self.assertEqual(binding["driver_sha256"], self.frozen["files"][".project/automation/build_task_verifier.py"])
            self.assertEqual(self.apply(document)["status"], "ARCHIVED_FAILED")

    def test_advanced_canonical_producer_binds_the_old_driver_not_current_bytes(self):
        with self.store.lock():
            document = self.inspect()
            producer = document["bindings"]["sessions"][-1]["producer"]
            self.assertEqual(producer["path"], str(self.root))
            old_digest = self.frozen["files"][".project/automation/build_task_verifier.py"]
            self.assertEqual(producer["driver_sha256"], old_digest)
            self.assertNotEqual(old_digest, hashlib.sha256((self.root / ".project/automation/build_task_verifier.py").read_bytes()).hexdigest())

    def test_foreign_dirty_unaccepted_wrong_driver_and_linked_producers_fail(self):
        producer = self.root.parent / "old-accepted-source"
        self.git("worktree", "add", "-q", "--detach", str(producer), self.old)
        driver = producer / ".project/automation/build_task_verifier.py"
        with self.store.lock():
            self.set_recorded_driver(driver)
            self.inspect()
            dirty = producer / "dirty.txt"
            dirty.write_text("Unreviewed source.\n")
            with self.assertRaises(Blocked):
                self.inspect()
            self.commit("unaccepted producer", producer)
            with self.assertRaises(Blocked):
                self.inspect()
            self.set_recorded_driver(producer / ".project/automation/other_verifier.py")
            with self.assertRaises(Blocked):
                self.inspect()
            link = self.root.parent / "linked-source"
            link.symlink_to(self.root, target_is_directory=True)
            self.set_recorded_driver(link / ".project/automation/build_task_verifier.py")
            with self.assertRaises((Blocked, OSError)):
                self.inspect()
            foreign = self.root.parent / "foreign-repository"
            self.git("clone", "-q", "--no-hardlinks", str(self.root), str(foreign))
            self.set_recorded_driver(foreign / ".project/automation/build_task_verifier.py")
            with self.assertRaisesRegex(Blocked, "another repository"):
                self.inspect()

    def test_symlinked_input_ancestor_cannot_archive_outside_objects(self):
        with self.store.lock():
            document = self.inspect()
        source = self.execution.parent
        outside = self.root.parent / "outside-runs"
        source.rename(outside)
        source.symlink_to(outside, target_is_directory=True)
        try:
            with self.store.lock():
                self.backend.configuration()
                before = self.store_bytes()
                with self.assertRaises((Blocked, OSError)):
                    self.inspect()
                with patch.object(self.backend, "configuration", side_effect=AssertionError("unsafe input ancestry reached source checks")):
                    with self.assertRaises((Blocked, OSError)):
                        self.apply(document)
                self.assertEqual(before, self.store_bytes())
                self.assertFalse(read_lease(self.store)["released"])
        finally:
            source.unlink()
            outside.rename(source)

    def test_active_or_uncertain_external_owner_blocks_inspection(self):
        with self.store.lock():
            launch = self.driver.launch(self.run)
            record = launch.load()
            for status in ("active", "uncertain"):
                record["sessions"]["other-owner"] = {"invocation_id": "other-owner", "task_id": TASK,
                    "role": "reviewer", "status": status, "cleanup_confirmed": False}
                launch.save(record)
                with self.assertRaises(Blocked) as caught:
                    self.inspect()
                self.assertEqual(caught.exception.code, "BLOCKED_EXTERNAL_SESSION")

    def test_archive_object_tampering_and_later_revision_block_replay(self):
        with self.store.lock():
            document = self.inspect()
            self.apply(document)
            directory = self.store.directory / "failed-verifications" / RUN / OP
            saved = recovery.raw_json(directory, "archive.json")
            row = next(row for row in saved["manifest"].values() if row["kind"] == "file")
            path = directory / "objects" / row["sha256"]
            original = path.read_bytes()
            path.write_bytes(b"changed archive bytes")
            with self.assertRaises(Blocked) as caught:
                self.apply(document)
            self.assertEqual(caught.exception.code, "BLOCKED_RECOVERY_EVIDENCE")
            path.write_bytes(original)
            self.store.save(self.run)
            with self.assertRaises(Blocked) as caught:
                self.apply(document)
            self.assertEqual(caught.exception.code, "BLOCKED_REVISION")

    def test_pending_cleanup_cannot_release_a_foreign_lease_or_adopt_a_candidate(self):
        with self.store.lock():
            document = self.inspect()
            with patch.object(self.store, "release", side_effect=Crash("before release")), self.assertRaises(Crash):
                self.apply(document)
            self.run = self.store.load(RUN)
            with self.assertRaises(Blocked) as caught:
                controller_handoff.adopt(self.driver, self.run, {}, self.run["revision"], 5)
            self.assertEqual(caught.exception.code, "BLOCKED_HANDOFF")
            path = self.store.directory / "leases" / (TASK + ".json")
            lease = read_lease(self.store)
            evidence.atomic_json(path, dict(lease, generation=lease["generation"] + 1))
            before = self.store_bytes()
            with self.assertRaises(Blocked) as caught:
                self.apply(document)
            self.assertEqual(caught.exception.code, "BLOCKED_LEASE")
            self.assertEqual(before, self.store_bytes())

    def test_changed_request_result_logs_cleanup_or_live_ownership_blocks_archival(self):
        mutations = [("request.json", lambda r: r.update(cwd="/another/worktree")),
                     ("result.json", lambda r: r.update(cleanup_complete=False)),
                     ("result.json", lambda r: r.update(reason="stopped")),
                     ("result.json", lambda r: r.update(exit_code=0)),
                     ("result.json", lambda r: r.update(candidate_cleanup={"complete": False, "groups": []})),
                     ("docker-cleanup.json", lambda r: r.update(complete=False))]
        with self.store.lock():
            for name, mutate in mutations:
                path = self.sessions[-1] / name
                original = path.read_bytes()
                value = recovery.raw_json(path.parent, name)
                mutate(value)
                evidence.atomic_json(path, value)
                with self.subTest(name=name), self.assertRaises(Blocked):
                    self.inspect()
                path.write_bytes(original)
            self.backend.processes = "90000001 90000011 S\n"
            with self.assertRaises(Blocked):
                self.inspect()
            self.backend.processes = ""
            with patch.object(recovery.build_resources, "members", return_value=["1" * 64]), self.assertRaises(Blocked):
                self.inspect()
            fd = os.open(self.sessions[-1] / "session.lock", os.O_RDONLY)
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                with self.assertRaises(Blocked):
                    self.inspect()
            finally:
                os.close(fd)

    def test_ancillary_bytes_and_symlink_target_changes_after_inspect_are_rejected(self):
        path = self.sessions[-1] / "ancillary.txt"
        path.write_text("original\n")
        link = self.sessions[-1] / "synthetic-link"
        link.symlink_to("/not/read/by/recovery")
        with self.store.lock():
            document = self.inspect()
            path.write_text("changed\n")
            with self.assertRaises(Blocked):
                self.apply(document)
            path.write_text("original\n")
            link.unlink()
            link.symlink_to("/different/target")
            with self.assertRaises(Blocked):
                self.apply(document)
            link.unlink()
            link.symlink_to("/not/read/by/recovery")
            self.apply(document)
            archive = recovery.raw_json(self.store.directory / "failed-verifications" / RUN / OP, "archive.json")
            self.assertEqual(archive["manifest"][str(link.relative_to(self.execution))],
                             {"kind": "symlink", "target": "/not/read/by/recovery"})
            self.assertTrue(link.is_symlink())

    def test_special_files_are_rejected_without_reading_them(self):
        os.mkfifo(self.sessions[-1] / "blocked-pipe")
        with self.store.lock(), self.assertRaises(Blocked):
            self.inspect()

    def test_crashes_before_and_after_archive_save_state_swap_release_and_final_save_replay(self):
        with self.store.lock():
            document = self.inspect()
            original_run = copy.deepcopy(self.run)
            original_atomic = evidence.atomic_json
            stages = [(stage, when) for stage in ("archive", "state", "release", "final") for when in ("before", "after")]
            for stage, when in stages:
                with self.subTest(stage=stage, when=when):
                    self.run = copy.deepcopy(original_run)
                    evidence.atomic_json(self.store.run_path(RUN), self.run)
                    evidence.atomic_json(self.store.directory / "leases" / (TASK + ".json"), self.run["tasks"][TASK]["lease"])
                    fired = False

                    def fault(path, value, **kwargs):
                        nonlocal fired
                        matches = ((stage == "archive" and Path(path).name == "archive.json")
                                   or (stage == "state" and "failure_recovery" in value)
                                   or (stage == "release" and value.get("released") is True)
                                   or (stage == "final" and value.get("run_id") == RUN and "tasks" in value
                                       and value["tasks"][TASK]["state"] == "BLOCKED" and "failure_recovery" not in value))
                        if matches and not fired and when == "before":
                            fired = True
                            raise Crash(stage)
                        original_atomic(path, value, **kwargs)
                        if matches and not fired and when == "after":
                            fired = True
                            raise Crash(stage)

                    # Remove the prior completed archive only in this disposable fixture.
                    import shutil
                    shutil.rmtree(self.store.directory / "failed-verifications", ignore_errors=True)
                    with patch.object(evidence, "atomic_json", side_effect=fault), self.assertRaises(Crash):
                        self.apply(document)
                    self.run = self.store.load(RUN)
                    if self.run.get("failure_recovery"):
                        with self.assertRaises(Blocked):
                            controller_handoff.boundary(self.driver, self.run, self.run["revision"], 5)
                        with self.assertRaises(Blocked):
                            self.driver.run(self.run, resume=True)
                    self.assertIn(self.apply(document)["status"], {"ARCHIVED_FAILED", "ALREADY_ARCHIVED"})
                    self.assertTrue(read_lease(self.store)["released"])
                    self.assertEqual(len(self.run["tasks"][TASK]["failed_verifications"]), 1)


def read_lease(store):
    return recovery.raw_json(store.directory / "leases", TASK + ".json")


if __name__ == "__main__":
    unittest.main()
