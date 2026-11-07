"""Bootstrap protocol stubs and fake publications; these never validate product schemas."""

import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

import acceptance
from acceptance_receipts import Ledger
import controller
from controller import Blocked, Controller, Store
from controller_backend import RealBackend
import evidence
import evidence_task_verifier as verifier
import task_proofs
from test_acceptance import BASE, COMMENT_ID, HEAD, MERGE, PR_NUMBER, REPOSITORY, proof as github_proof
from test_controller import Crash, FakeBackend, graph
from test_controller_backend import PublicationBackend

SOURCE = Path(__file__).resolve().parents[2]
AUTH = {"reference": "TEST_ONLY_LAUNCH", "stage_limit": "E01"}


def recipe_fixture():
    declared = json.loads((SOURCE / ".project/automation/task-recipes.json").read_text())
    return task_proofs.snapshot(declared, SOURCE)[verifier.TASK]


def protocol_stub(root, behavior="oracle"):
    """Disposable driver fixture, intentionally not an evidence-schema implementation."""
    directory = root / "test/evidence"
    directory.mkdir(parents=True)
    for name, required in (("case-registry", ["schema_version", "version", "cases"]),
                           ("case-result", [*verifier.IDENTITY, "schema_version", "task_id", "fixture_id", "fixture_version", "applicability"]),
                           ("run-manifest", ["schema_version", "run_id", "case_identities", "case_outcomes", "profile", "phases"])):
        (directory / (name + ".schema.json")).write_text(json.dumps({"type": "object", "required": required,
            "properties": {"schema_version": {"const": 3}}, "description": "BOOTSTRAP PROTOCOL STUB ONLY"}))
    (directory / "profile-registry.json").write_text(json.dumps({"schema_version": 3, "profiles": verifier.PROFILES}))
    (directory / "reader-boundary-manifest.schema.json").write_text(json.dumps({"type": "object",
        "required": sorted(verifier.reader_vectors.REQUIRED), "properties": {"schema_version": {"const": 1}},
        "description": "BOOTSTRAP PROTOCOL STUB ONLY; NOT A D87 IMPLEMENTATION"}))
    (directory / "README.md").write_text("BOOTSTRAP PROTOCOL STUB ONLY. No product schemas or runtime proof.\n")
    table = {verifier.digest(vector["request"]): 0 if vector["valid"] else 2 for vector in verifier.vectors()}
    script = "import json,sys,hashlib,time\nrequest=json.load(sys.stdin)\n"
    script += "assert not ({'valid','expected','id'} & set(request))\n"
    if behavior == "oracle":
        script += f"profiles={verifier.PROFILES!r}\ntable={table!r}\n"
        script += "if request['kind']=='ExpandProfiles':\n print(json.dumps(sorted(p for p,f in profiles.items() if request['scope'] in {'ALL',f})))\n sys.exit(0)\n"
        script += "key=hashlib.sha256((json.dumps(request,sort_keys=True,separators=(',',':'),ensure_ascii=True)+'\\n').encode()).hexdigest()\nsys.exit(table[key])\n"
    elif behavior == "always-valid":
        script += "sys.exit(0)\n"
    elif behavior == "always-invalid":
        script += "sys.exit(2)\n"
    elif behavior == "noisy":
        script += "sys.stdout.write('x'*10000000)\n"
    else:
        script += "time.sleep(20)\n"
    (directory / "validate.py").write_text(script)
    (directory / "test_protocol_stub.py").write_text("import unittest\nfrom pathlib import Path\nclass StubSmoke(unittest.TestCase):\n def test_protocol_stub_exists(self):\n  self.assertTrue((Path(__file__).parent / 'validate.py').is_file())\n")
    (root / ".project/evidence").mkdir(parents=True)
    (root / ".project/evidence/E01-01.evidence.md").write_text("Synthetic bootstrap adapter fixture only.\n")
    (root / ".project/automation").mkdir(parents=True)
    (root / ".project/automation/ci.py").write_text("print('synthetic bootstrap CI stub')\n")


def proof_fixture():
    """Fabricated unit-test transport input; authority exists only in fake GitHub."""
    recipe = recipe_fixture()
    files = {name: "a" * 64 for name in recipe["required_files"]}
    report = {"contract": verifier.CONTRACT, "task_id": verifier.TASK, "scope": "artifact-conformance",
              "runtime": "NOT_RUN", "synthetic_inputs": True, "vector_sha256": verifier.digest(verifier.vectors()),
              "outputs": {name: files[name] for name in verifier.OUTPUTS}, "outcomes": []}
    expected = [(v["id"], 0 if v["valid"] else 2) for v in verifier.vectors()] + [("expand-" + s, 0) for s in ("ALL", "ETH", "BSC", "POLY")]
    report["outcomes"] = [{"id": name, "expected_exit": code, "actual_exit": code,
                           "stdout_sha256": "a" * 64, "stderr_sha256": "b" * 64} for name, code in expected]
    checks = []
    commands = recipe["commands"] + [[sys.executable, "-I", str(SOURCE / ".project/automation/evidence_task_verifier.py"), "--candidate", "/synthetic-candidate"]]
    for number, command in enumerate(commands):
        stdout = json.dumps(report) if number == len(commands) - 1 else "synthetic CI log\n"
        request = {"operation_id": "verify-check-" + str(number), "command": command, "cwd": "/synthetic-candidate",
                   "env": {"BOOTSTRAP_HEAD_SHA": HEAD, "BOOTSTRAP_BASE_SHA": BASE},
                   "stdin_sha256": hashlib.sha256(b"").hexdigest()}
        execution = {"operation_id": request["operation_id"], "request_sha256": evidence.digest(request),
                     "reason": "completed", "exit_code": 0, "cleanup_complete": True,
                     "stdout_sha256": hashlib.sha256(stdout.encode()).hexdigest(), "stderr_sha256": hashlib.sha256(b"").hexdigest()}
        checks.append({"request": request, "execution": execution, "stdout": stdout, "stderr": ""})
    return {"passed": True, "task_id": verifier.TASK, "head_sha": HEAD, "base_sha": BASE,
            "runtime": "NOT_RUN", "recipe": recipe, "recipe_sha256": evidence.digest(recipe),
            "verifier_source_commit": BASE, "authorization": AUTH, "verification_operation_id": "verify", "files": files, "checks": checks}


class ArtifactFlowBackend(FakeBackend):
    """Fake workers/remotes with the real bounded verification subprocess path."""
    def __init__(self, root, store):
        super().__init__()
        self.real = RealBackend.__new__(RealBackend)
        self.real.root, self.real.source, self.real.store = root, SOURCE, store
        self.real.provenance = {"checkout_head_sha": BASE, "test_only": True}
        self.real.candidate = lambda run, task: HEAD
        self.real.git = lambda *args: ""
        self.accepted = {task: {"task_id": task, "proof": "verified-merge"} for task in ("BOOT-03", "BOOT-09")}

    def snapshot_recipes(self, recipes):
        return self.real.snapshot_recipes(recipes)

    def session_recovery(self, run, task, operation):
        return self.real.session_recovery(run, task, operation)

    def perform(self, action, run, task, operation):
        if task["task_id"] == verifier.TASK:
            self.real.artifact_recipe(run, task)
        if action == "verify":
            result = self.real.do_verify(run, task, operation, self.real.directory(run, operation))
            self.effect("verification", operation)
            return dict(result, operation_id=operation["id"])
        result = super().perform(action, run, task, operation)
        if action == "prepare":
            result.update(path=str(self.real.root), base_sha=BASE)
        return result


class ArtifactDriverTests(unittest.TestCase):
    def test_oracle_controls_are_consistent_and_mutations_preserve_valid_context(self):
        rows = {row["id"]: row for row in verifier.vectors()}
        control = rows["composite"]
        self.assertTrue(control["valid"])
        context = control["request"]["context"]
        for child in control["request"]["document"]["children"]:
            manifest = context["child_manifests"][child["path"]]
            self.assertEqual(child["sha256"], verifier.digest(manifest))
            self.assertEqual(child["profile"], manifest["profile"])
            self.assertEqual(manifest["parent_integration_run"], control["request"]["document"]["run_id"])
            self.assertEqual(manifest["case_outcomes"][0]["verdict"], child["verdict"])
        for name in ("composite-missing-child", "composite-duplicate-child", "composite-child-outcome-mismatch",
                     "composite-missing-integration", "composite-wrong-child-hash"):
            self.assertEqual(rows[name]["request"]["context"], context)
            self.assertFalse(rows[name]["valid"])
        failure = rows["composite-recorded-failure"]
        hidden = rows["composite-parent-cannot-hide-fail"]
        self.assertTrue(failure["valid"], "recording a real FAIL must remain valid evidence")
        self.assertFalse(hidden["valid"])
        self.assertEqual(failure["request"]["context"], hidden["request"]["context"])
        self.assertEqual(dict(failure["request"]["document"], verdict="PASS"), hidden["request"]["document"])
        for name in ("NOT_APPLICABLE", "NOT_ENABLED", "DEFERRED"):
            row = rows[name]
            self.assertTrue(row["valid"])
            document = row["request"]["document"]
            self.assertEqual(row["request"]["context"]["registry"]["cases"], [document])
            self.assertEqual(document["ac_id"], "AC-SCP-05-02")
            self.assertEqual(document["task_id"], "E04-02.optional_scope")
        for row in rows["registry-seven-profiles-two-forks-two-suites"]["request"]["document"]["cases"]:
            self.assertEqual((row["ac_id"], row["task_id"]), ("AC-DEV-04-01", "E07-09.requirement_verdict"))
            self.assertEqual(row["network"], verifier.PROFILES[row["profile"]])
        manifest = rows["manifest"]["request"]["document"]
        phases = [manifest["phases"][name] for name in ("preparation", "execution", "teardown")]
        self.assertEqual(manifest["started_at"], phases[0]["started_at"])
        self.assertEqual(manifest["finished_at"], phases[-1]["finished_at"])
        self.assertTrue(all(a["finished_at"] <= b["started_at"] for a, b in zip(phases, phases[1:])))
        partial = rows["acyclic-stage-control"]
        forward = rows["acyclic-forward-prerequisite"]
        self.assertTrue(partial["valid"])
        self.assertFalse(forward["valid"])
        self.assertEqual(forward["request"]["document"]["stages"][1]["dependencies"], [])
        self.assertEqual(forward["request"]["document"]["stages"][0]["dependencies"], ["E05"])
        restored = copy.deepcopy(forward["request"])
        restored["document"]["stages"][0]["dependencies"] = []
        self.assertEqual(restored, partial["request"])

    def test_driver_executes_protocol_stub_without_disclosing_expected_outcomes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            protocol_stub(root)
            result = verifier.execute(root)
            verifier.validate_report(result)
            self.assertEqual(result["runtime"], "NOT_RUN")
            self.assertEqual(len(result["outcomes"]), len(verifier.vectors()) + 4)

    def test_unconditional_success_or_failure_is_not_conformance(self):
        for behavior in ("always-valid", "always-invalid"):
            with self.subTest(behavior=behavior), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                protocol_stub(root, behavior)
                with self.assertRaises(ValueError):
                    verifier.execute(root)

    def test_noisy_and_hung_candidates_are_bounded(self):
        for behavior in ("noisy", "hung"):
            with self.subTest(behavior=behavior), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                protocol_stub(root, behavior)
                started = time.monotonic()
                with self.assertRaises((ValueError, subprocess.TimeoutExpired)):
                    verifier.execute(root)
                self.assertLess(time.monotonic() - started, 8)

    def test_caught_output_overflow_cannot_report_valid_or_invalid(self):
        for stream in (1, 2):
            for exit_code in (0, 2):
                with self.subTest(stream=stream, exit_code=exit_code):
                    script = ("import errno,json,os,signal,sys\njson.load(sys.stdin)\n"
                              "signal.signal(signal.SIGXFSZ, signal.SIG_IGN)\n"
                              "try:\n"
                              f" while True: os.write({stream}, b'x' * 8192)\n"
                              "except OSError as error:\n"
                              " if error.errno != errno.EFBIG: raise\n"
                              f"os._exit({exit_code})\n")
                    with self.assertRaisesRegex(ValueError, "output limit"):
                        verifier.candidate_call([sys.executable, "-I", "-c", script], {"synthetic": True})

    def test_output_boundary_is_rejected_and_empty_validation_stdout_is_allowed(self):
        for stream in (1, 2):
            for exit_code in (0, 2):
                for size in (0, 65535, 65536):
                    with self.subTest(stream=stream, exit_code=exit_code, size=size):
                        script = ("import json,os,sys\njson.load(sys.stdin)\n"
                                  f"os.write({stream}, b'x' * {size})\nos._exit({exit_code})\n")
                        if size == 65536:
                            with self.assertRaisesRegex(ValueError, "output limit"):
                                verifier.candidate_call([sys.executable, "-I", "-c", script], {})
                        else:
                            actual, stdout, stderr = verifier.candidate_call([sys.executable, "-I", "-c", script], {})
                            self.assertEqual(actual, exit_code)
                            self.assertEqual(len(stdout if stream == 1 else stderr), size)

    def test_empty_expansion_stdout_is_rejected_after_valid_ordinary_calls(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            protocol_stub(root)
            path = root / "test/evidence/validate.py"
            lines = path.read_text().splitlines()
            path.write_text("\n".join(" pass" if line.startswith(" print(json.dumps(") else line for line in lines) + "\n")
            with self.assertRaises(ValueError):
                verifier.execute(root)

    def test_missing_output_and_profile_alias_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            protocol_stub(root)
            path = root / "test/evidence/profile-registry.json"
            path.write_text(json.dumps({"schema_version": 3, "profiles": {"ETH": "ETH"}}))
            with self.assertRaises(ValueError):
                verifier.validate_outputs(root)
            path.unlink()
            with self.assertRaises(ValueError):
                verifier.validate_outputs(root)

    def test_trusted_pack_and_recipe_cannot_change_on_resume(self):
        recipe = recipe_fixture()
        task_proofs.check_recipe(recipe, SOURCE)
        for field in ("allowed_paths", "commands", "trusted_pack"):
            changed = copy.deepcopy(recipe)
            changed[field] = {} if field == "trusted_pack" else []
            with self.subTest(field=field), self.assertRaises(ValueError):
                task_proofs.check_recipe(changed, SOURCE)

    def test_real_dispatch_guard_rejects_d86_and_unconfigured_next_task(self):
        backend = RealBackend.__new__(RealBackend)
        backend.source, backend.root = SOURCE, SOURCE
        backend.check_stop = lambda run: None
        backend.git = lambda *args: "git@github.com:sigaev-pro/bc-indexer.git"
        backend.repository = REPOSITORY
        run = {"authorization": controller.AUTHORIZATION, "catalog": {verifier.TASK: {"stage": "E01"}}, "recipes": {verifier.TASK: recipe_fixture()}}
        with patch.object(backend, "do_prepare") as mutation, self.assertRaises(Blocked) as caught:
            backend.perform("prepare", run, {"task_id": verifier.TASK}, {"id": "guard"})
        self.assertEqual(caught.exception.code, "BLOCKED_AUTHORIZATION")
        mutation.assert_not_called()
        run["authorization"] = AUTH
        run["catalog"]["E01-01.harness"] = {"stage": "E01"}
        with patch.dict(controller.AUTHORIZATIONS, {AUTH["reference"]: AUTH}), self.assertRaises(Blocked) as caught:
            backend.perform("prepare", run, {"task_id": "E01-01.harness"}, {"id": "guard"})
        self.assertEqual(caught.exception.code, "BLOCKED_UNCONFIGURED_VERIFICATION")

    def test_fake_authorized_flow_executes_trusted_driver_and_recovers_verify_crash(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            protocol_stub(root)
            store = Store(root / "state")
            backend = ArtifactFlowBackend(root, store)
            tasks = graph()
            tasks["E01-01.builds"] = {"task_id": "E01-01.builds", "stage": "E01", "issue": 11,
                "dependencies": [verifier.TASK], "acceptance": "No verifier configured.", "snapshot": "Future build task."}
            recipes = {verifier.TASK: recipe_fixture()}
            driver = Controller(store, backend)
            with patch.dict(controller.AUTHORIZATIONS, {AUTH["reference"]: AUTH}), store.lock():
                run = driver.create(tasks, recipes, authorization_ref=AUTH["reference"])
                backend.crash = "after:verification"
                with self.assertRaises(Crash):
                    driver.run(run)
                prior = store.load(run["run_id"])
                operation = prior["tasks"][verifier.TASK]["pending"]
                artifact_log = backend.real.directory(run, {"id": operation["id"] + "-check-" + str(len(recipes[verifier.TASK]["commands"]))}) / "result.json"
                original = artifact_log.read_bytes()
                recovered = driver.run(prior, resume=True)
            self.assertEqual(recovered["tasks"][verifier.TASK]["state"], "DONE")
            self.assertEqual(recovered["tasks"]["E01-01.builds"]["blocker"]["code"], "BLOCKED_UNCONFIGURED_VERIFICATION")
            self.assertEqual(recovered["invocations"], 2)
            self.assertEqual(artifact_log.read_bytes(), original)
            proof = recovered["tasks"][verifier.TASK]["verification"]
            task_proofs.check_verification(proof, HEAD, BASE)
            self.assertEqual(proof["runtime"], "NOT_RUN")
            self.assertEqual(set(controller.AUTHORIZATIONS), {"D86", "D92", "D93"})


class ArtifactReceiptTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name)
        self.task_proof = proof_fixture()
        self.snapshot = github_proof()
        self.snapshot["task_id"] = verifier.TASK
        self.snapshot["task_verification"] = copy.deepcopy(self.task_proof)
        self.snapshot["verifier_source_ancestry"] = {"status": "identical", "merge_base_commit": {"sha": BASE}}
        pull = self.snapshot["pull_request"]
        pull["body"] = pull["body"].replace("BOOT-05", verifier.TASK)
        comment = self.snapshot["review_comment"]
        report = json.loads(comment["body"].split("```json\n")[1].rsplit("\n```", 1)[0])
        report["task_id"] = verifier.TASK
        report["controller_verification"] = task_proofs.publication(self.task_proof)
        comment["body"] = acceptance.REVIEW_MARKER + "\n```json\n" + json.dumps(report, indent=2) + "\n```"
        description = "Review SHA256: " + hashlib.sha256(comment["body"].encode()).hexdigest()
        for row in self.snapshot["combined_status"]["statuses"] + self.snapshot["commit_statuses"]:
            if row["context"] == acceptance.REVIEW_CONTEXT:
                row["description"] = description
        expected = {"repository": REPOSITORY, "task_id": verifier.TASK, "pull_request_number": PR_NUMBER,
                    "review_comment_id": COMMENT_ID, "coordinator_login": "sigaev-pro", "implementer_session": "worker-1", "head_sha": HEAD, "base_sha": BASE}
        captured = evidence.capture(expected, self.path / "capture", lambda *args, **kwargs: copy.deepcopy(self.snapshot), self.task_proof)
        self.bundle = json.loads(Path(captured["path"]).read_text())
        check = self.snapshot["check_runs"]["check_runs"][0]
        self.rows = {f"issues/comments/{COMMENT_ID}": comment,
                     f"actions/runs/{self.snapshot['ci_run']['id']}": self.snapshot["ci_run"],
                     f"check-runs/{check['id']}": check, f"commits/{HEAD}/statuses?per_page=100": self.snapshot["commit_statuses"],
                     f"pulls/{PR_NUMBER}": dict(pull, merged=True, state="closed"),
                     f"git/commits/{HEAD}": self.snapshot["head_commit"], f"git/commits/{MERGE}": self.snapshot["merge_commit"],
                     "git/ref/heads/main": {"object": {"sha": MERGE}},
                     f"compare/{MERGE}...{MERGE}": {"status": "identical", "merge_base_commit": {"sha": MERGE}},
                     f"compare/{BASE}...{BASE}": {"status": "identical", "merge_base_commit": {"sha": BASE}}}
        self.ledger = Ledger(self.path / "ledger", lambda path: copy.deepcopy(self.rows[path]))

    def receipt(self):
        return self.ledger.from_capture(copy.deepcopy(self.bundle), {"test_only": True}, copy.deepcopy(self.task_proof))

    def test_bound_artifact_receipt_roundtrips_without_ci_log_dependency(self):
        receipt = self.ledger.save(self.receipt())
        del self.rows[f"actions/runs/{self.snapshot['ci_run']['id']}"]
        self.assertEqual(self.ledger.load(verifier.TASK), receipt)

    def test_generic_green_capture_is_not_product_acceptance(self):
        generic = copy.deepcopy(self.bundle)
        generic["snapshot"].pop("task_verification")
        self.assertEqual(acceptance.evaluate(generic["snapshot"])["verdict"], "BLOCKED")
        with self.assertRaises((Blocked, ValueError)):
            self.ledger.from_capture(generic, {})
        with patch.object(acceptance, "collect") as collect, self.assertRaises(ValueError):
            evidence.capture(self.bundle["expected"], self.path / "must-not-exist")
        collect.assert_not_called()

    def test_rehashed_local_proof_cannot_replace_publicly_bound_recipe_outputs_or_logs(self):
        for field in ("recipe", "pack", "output", "log", "head", "base", "task"):
            with self.subTest(field=field):
                receipt = self.receipt()
                changed = receipt["task_verification"]
                if field == "recipe":
                    changed["recipe"]["role"] = "architect"
                    changed["recipe_sha256"] = evidence.digest(changed["recipe"])
                elif field == "pack":
                    changed["recipe"]["trusted_pack"][verifier.PACK_FILES[0]] = "f" * 64
                    changed["recipe_sha256"] = evidence.digest(changed["recipe"])
                elif field == "output":
                    changed["files"][".project/evidence/E01-01.evidence.md"] = "f" * 64
                elif field == "log":
                    changed["checks"][0]["stdout"] = "forged log\n"
                    changed["checks"][0]["execution"]["stdout_sha256"] = hashlib.sha256(b"forged log\n").hexdigest()
                else:
                    changed[{"head": "head_sha", "base": "base_sha", "task": "task_id"}[field]] = "f" * 40
                if field in {"recipe", "pack", "output", "log"}:
                    # Forge every local layer consistently. The actual public
                    # coordinator comment remains the independent authority.
                    snapshot = receipt["capture"]["snapshot"] = copy.deepcopy(receipt["capture"]["snapshot"])
                    snapshot["task_verification"] = copy.deepcopy(changed)
                    comment = snapshot["review_comment"]
                    report = json.loads(comment["body"].split("```json\n")[1].rsplit("\n```", 1)[0])
                    report["controller_verification"] = task_proofs.publication(changed)
                    comment["body"] = acceptance.REVIEW_MARKER + "\n```json\n" + json.dumps(report, indent=2) + "\n```"
                    description = "Review SHA256: " + hashlib.sha256(comment["body"].encode()).hexdigest()
                    for row in snapshot["combined_status"]["statuses"] + snapshot["commit_statuses"]:
                        if row["context"] == acceptance.REVIEW_CONTEXT:
                            row["description"] = description
                    receipt["capture"]["snapshot_sha256"] = evidence.digest(snapshot)
                    receipt["capture"]["verdict"] = acceptance.evaluate(snapshot)
                    self.assertEqual(receipt["capture"]["verdict"]["verdict"], "PASS")
                evidence.atomic_json(self.path / "ledger" / (verifier.TASK + ".json"), {"receipt": receipt, "sha256": evidence.digest(receipt)})
                with self.assertRaises(Blocked):
                    self.ledger.load(verifier.TASK)

    def test_real_premerge_path_rejects_missing_or_wrong_public_task_attestation(self):
        for mode in ("missing", "wrong"):
            with self.subTest(mode=mode):
                snapshot = copy.deepcopy(self.snapshot)
                comment = snapshot["review_comment"]
                report = json.loads(comment["body"].split("```json\n")[1].rsplit("\n```", 1)[0])
                if mode == "missing":
                    report.pop("controller_verification")
                else:
                    report["controller_verification"]["proof_sha256"] = "f" * 64
                comment["body"] = acceptance.REVIEW_MARKER + "\n```json\n" + json.dumps(report, indent=2) + "\n```"
                description = "Review SHA256: " + hashlib.sha256(comment["body"].encode()).hexdigest()
                for row in snapshot["combined_status"]["statuses"] + snapshot["commit_statuses"]:
                    if row["context"] == acceptance.REVIEW_CONTEXT:
                        row["description"] = description
                self.assertEqual(acceptance.evaluate(snapshot)["verdict"], "BLOCKED")
                backend = RealBackend.__new__(RealBackend)
                backend.source, backend.root, backend.repository = SOURCE, self.path, REPOSITORY
                backend.ledger = self.ledger
                backend.candidate = lambda run, task: HEAD
                backend.check_stop = lambda run: None
                backend.read = lambda path: dict(snapshot["pull_request"], merged=False) if path.startswith("pulls/") else snapshot["check_runs"]
                task = {"task_id": verifier.TASK, "workspace": {"base_sha": BASE},
                        "pull": {"number": PR_NUMBER, "comment_id": COMMENT_ID},
                        "worker": {"commit_sha": HEAD, "session_id": "worker-1"}, "verification": self.task_proof}
                run = {"deadline": time.time() + 20, "authorization": controller.AUTHORIZATION,
                       "recipes": {verifier.TASK: recipe_fixture()}}
                with patch.object(acceptance, "collect", return_value=snapshot), patch.object(backend, "mutate") as mutate:
                    with self.assertRaises(Blocked):
                        backend.do_merge(run, task, {"id": "merge"}, self.path / mode)
                mutate.assert_not_called()
                self.assertFalse((self.path / mode / "merge-intent.json").exists())

    def test_changed_public_binding_and_unaccepted_verifier_source_are_rejected(self):
        receipt = self.receipt()
        self.rows[f"issues/comments/{COMMENT_ID}"] = dict(self.rows[f"issues/comments/{COMMENT_ID}"], body="changed")
        with self.assertRaises(Blocked):
            self.ledger.verify(verifier.TASK, receipt)
        self.rows[f"issues/comments/{COMMENT_ID}"] = self.snapshot["review_comment"]
        self.rows[f"compare/{BASE}...{BASE}"]["status"] = "diverged"
        with self.assertRaises(Blocked):
            self.ledger.verify(verifier.TASK, receipt)

    def test_publication_retry_preserves_the_same_separate_controller_binding(self):
        for fault in ("push", "pr", "comment", "status"):
            with self.subTest(fault=fault):
                backend = PublicationBackend()
                backend.source = SOURCE
                backend.main = BASE
                backend.crash_after = fault
                task = {"task_id": verifier.TASK, "workspace": {"branch": "owned", "path": "/isolated", "base_sha": BASE},
                        "worker": {"commit_sha": HEAD, "session_id": "worker"},
                        "review": {"verdict": "PASS", "session_id": "reviewer", "checks": ["Exact source review."], "unverified": ["Later controller checks."]},
                        "verification": copy.deepcopy(self.task_proof)}
                run = {"catalog": {verifier.TASK: {"snapshot": "## Artifact task"}}, "recipes": {verifier.TASK: recipe_fixture()}}
                with self.assertRaises(Crash):
                    backend.do_publish(run, task, {"id": "publish"}, None)
                backend.do_publish(run, task, {"id": "publish"}, None)
                report = json.loads(backend.comments[0]["body"].split("```json\n")[1].rsplit("\n```", 1)[0])
                self.assertEqual(report["controller_verification"], task_proofs.publication(self.task_proof))
                self.assertFalse(report["controller_verification"]["reviewer_attestation"])
                self.assertEqual(backend.counts, {name: 1 for name in ("push", "pr", "comment", "status")})


if __name__ == "__main__":
    unittest.main()
