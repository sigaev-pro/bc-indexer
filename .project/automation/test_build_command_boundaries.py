"""Run disposable candidate processes against protected inputs and artifacts."""

import shutil
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
import threading
from unittest import mock

import build_contract as contract
from build_product_commands import Commands, finish_group
import build_product_commands
import build_candidate_guard
import build_task_verifier as verifier
import build_vectors
import evidence
import session_runner


SOURCE = Path(__file__).resolve().parents[2]


class CommandBoundaryTests(unittest.TestCase):
    def test_signal_denial_requires_actual_group_absence(self):
        terminate = session_runner.terminate_group
        for actually_stopped in (True, False):
            with self.subTest(actually_stopped=actually_stopped), tempfile.TemporaryDirectory() as root:
                commands = self.commands(root)
                owned = []
                def report_denial(process, grace=0.1):
                    owned.append(process)
                    if actually_stopped:
                        terminate(process, grace=grace)
                    return "cleanup-denied"
                with mock.patch.object(session_runner, "terminate_group", side_effect=report_denial):
                    if actually_stopped:
                        commands.call("denial", [sys.executable, "-I", "-c", "pass"])
                        self.assertTrue(commands.outcomes[-1]["cleanup_confirmed"])
                    else:
                        with self.assertRaisesRegex(ValueError, "ownership or cleanup"):
                            commands.call("denial", [sys.executable, "-I", "-c", "pass"])
                        self.assertFalse(commands.outcomes[-1]["cleanup_confirmed"])
                self.assertTrue(build_candidate_guard.confirm_all(commands.groups)["complete"])
                for process in owned:
                    if process.returncode is None:
                        finish_group(process)

    def test_normal_cleanup_keeps_lifeline_open_across_scheduling_delays(self):
        for delay in (0, 0.0001, 0.001, 0.005, 0.05):
            with self.subTest(delay=delay), tempfile.TemporaryDirectory() as root:
                commands = self.commands(root)
                def delayed_cleanup(process):
                    time.sleep(delay)
                    try:
                        self.assertFalse(session_runner.exited_unreaped(process), "lifeline closed before explicit cleanup")
                    finally:
                        finish_group(process)
                with mock.patch.object(build_product_commands, "finish_group", side_effect=delayed_cleanup):
                    commands.call("normal", [sys.executable, "-I", "-c", "pass"])
                self.assertEqual(commands.outcomes[-1]["actual_exit"], 0)
                self.assertTrue(commands.outcomes[-1]["cleanup_confirmed"])

    def test_cleanup_failure_still_closes_lifeline_and_remains_failure(self):
        with tempfile.TemporaryDirectory() as root:
            commands = self.commands(root)
            owned = []
            def denied(process):
                owned.append(process)
                raise ValueError("injected cleanup denial")
            try:
                with mock.patch.object(build_product_commands, "finish_group", side_effect=denied):
                    with self.assertRaisesRegex(ValueError, "injected cleanup denial"):
                        commands.call("denied", [sys.executable, "-I", "-c", "pass"])
                self.assertFalse(commands.outcomes[-1]["cleanup_confirmed"])
                self.assertTrue(build_candidate_guard.confirm_all(commands.groups)["complete"])
            finally:
                for process in owned:
                    finish_group(process)

    def commands(self, root):
        candidate = Path(root) / "candidate"
        candidate.mkdir()
        registry, _, _, _ = contract.inputs(SOURCE)
        for name in registry["external_lock_paths"]:
            path = candidate / name
            path.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(SOURCE / name, path)
        return Commands(candidate, SOURCE, Path(root) / "controls", time.time() + 30)

    def driver(self, root):
        commands = self.commands(root)
        root = Path(root)
        ready, marker = root / "ready", root / "late-write"
        child = "from pathlib import Path; import time; Path(" + repr(str(ready)) + ").write_text('ready'); time.sleep(1); Path(" + repr(str(marker)) + ").write_text('escaped'); time.sleep(5)"
        code = "import sys,time\nsys.path.insert(0," + repr(str(SOURCE / '.project/automation')) + ")\nfrom build_product_commands import Commands\n"
        code += "c=Commands(" + repr(str(commands.candidate)) + "," + repr(str(SOURCE)) + "," + repr(str(root / 'driver-controls')) + ",time.time()+10)\n"
        code += "c.call('worker',[sys.executable,'-I','-c'," + repr(child) + "],timeout=8)\n"
        return [sys.executable, "-I", "-c", code], ready, marker

    def test_reaped_group_leader_is_never_signaled(self):
        child = subprocess.Popen([sys.executable, "-I", "-c", "pass"], start_new_session=True)
        child.wait(timeout=5)
        with mock.patch.object(os, "killpg") as kill, self.assertRaisesRegex(ValueError, "ownership or cleanup"):
            finish_group(child)
        kill.assert_not_called()

    def test_parent_sigterm_and_sigkill_close_the_candidate_lifeline(self):
        for signum in (signal.SIGTERM, signal.SIGKILL):
            with self.subTest(signum=signum), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                argv, ready, marker = self.driver(root)
                groups = root / "owned-groups"
                process = subprocess.Popen(argv, start_new_session=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                           env=dict(os.environ, BOOTSTRAP_CANDIDATE_GROUPS=str(groups)))
                try:
                    deadline = time.monotonic() + 5
                    while not ready.exists() and time.monotonic() < deadline:
                        self.assertFalse(session_runner.exited_unreaped(process))
                        time.sleep(0.01)
                    self.assertTrue(ready.exists())
                    os.kill(process.pid, signum)
                    self.assertTrue(session_runner.confirm_group_exit(process))
                    cleanup = build_candidate_guard.confirm_all(groups)
                    self.assertTrue(cleanup["complete"], cleanup)
                    self.assertEqual(len(cleanup["groups"]), 1)
                    time.sleep(1.1)
                    self.assertFalse(marker.exists())
                finally:
                    if process.returncode is None:
                        session_runner.terminate_group(process, grace=0.1)
                        process.wait(timeout=5)
                    self.assertTrue(build_candidate_guard.confirm_all(groups)["complete"])

    def test_outer_supervisor_stop_and_deadline_include_candidate_cleanup(self):
        for mode in ("stop", "deadline"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                argv, ready, marker = self.driver(root)
                session = root / "session"
                session.mkdir()
                (session / "stdin.txt").write_text("")
                stop = root / "stop"
                request = {"operation_id": "candidate-" + mode, "command": argv, "cwd": str(root),
                           "deadline": time.time() + 10, "timeout_seconds": 0.7 if mode == "deadline" else 8,
                           "stop_file": str(stop), "log_limit": 8192, "env": {}, "stdin_sha256": hashlib.sha256(b"").hexdigest()}
                evidence.atomic_json(session / "request.json", request)
                done = threading.Event()
                def request_stop():
                    while not done.wait(0.01):
                        if ready.exists():
                            stop.write_text("stop")
                            return
                thread = threading.Thread(target=request_stop) if mode == "stop" else None
                descriptor = os.open(root / "lock", os.O_CREAT | os.O_RDWR, 0o600)
                try:
                    if thread:
                        thread.start()
                    result = session_runner.supervise(session / "request.json", descriptor)
                    self.assertEqual(result["reason"], "stopped" if mode == "stop" else "timeout")
                    self.assertTrue(result["cleanup_complete"], result)
                    self.assertTrue(result["candidate_cleanup"]["complete"])
                    self.assertEqual(len(result["candidate_cleanup"]["groups"]), 1)
                    time.sleep(1.1)
                    self.assertFalse(marker.exists())
                finally:
                    done.set()
                    if thread:
                        thread.join(timeout=2)
                        self.assertFalse(thread.is_alive())
                    os.close(descriptor)

    def test_unresolved_candidate_intent_cannot_claim_cleanup(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "pending.json"
            evidence.atomic_json(path, {"token": "pending", "state": "pending", "group": None})
            self.assertFalse(build_candidate_guard.confirm_all(root, timeout=0)["complete"])

    def test_external_locks_reject_identical_byte_rewrites(self):
        registry, _, _, _ = contract.inputs(SOURCE)
        for name in registry["external_lock_paths"]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as root:
                commands = self.commands(root)
                commands.call("unchanged", [sys.executable, "-I", "-c", "pass"])
                code = "from pathlib import Path; import sys; p=Path(sys.argv[1]); p.write_bytes(p.read_bytes())"
                with self.assertRaisesRegex(ValueError, "mutated an active input"):
                    commands.call("rewrite", [sys.executable, "-I", "-c", code, name])

    def test_candidate_descendants_are_stopped_on_exit_and_timeout(self):
        for timeout in (False, True):
            with self.subTest(timeout=timeout), tempfile.TemporaryDirectory() as root:
                commands = self.commands(root)
                marker = Path(root) / "late-write"
                child = "import time; from pathlib import Path; time.sleep(0.5); Path(" + repr(str(marker)) + ").write_text('changed')"
                parent = "import subprocess,sys,time; subprocess.Popen([sys.executable,'-I','-c',sys.argv[1]]); "
                parent += "time.sleep(5)" if timeout else "pass"
                argv = [sys.executable, "-I", "-c", parent, child]
                if timeout:
                    with self.assertRaises(subprocess.TimeoutExpired):
                        commands.call("descendant", argv, timeout=0.1)
                else:
                    commands.call("descendant", argv)
                self.assertTrue(commands.outcomes[-1]["cleanup_confirmed"])
                time.sleep(0.6)
                self.assertFalse(marker.exists())

    def test_final_preflight_rejects_artifact_changes_after_candidate_success(self):
        for mutate in (False, True):
            with self.subTest(mutate=mutate), tempfile.TemporaryDirectory() as root:
                commands = self.commands(root)
                artifacts = Path(root) / "artifacts"
                manifest = build_vectors.fixture(artifacts, SOURCE)
                contract.validate_manifest(manifest, artifacts, SOURCE, allow_synthetic=True)
                script = commands.candidate / "build/native/verify.py"
                script.parent.mkdir(parents=True)
                script.write_text("import json,sys\nfrom pathlib import Path\nr=json.load(sys.stdin)\n"
                                  "m=json.loads((Path(r['root'])/r['manifest_path']).read_text())\n"
                                  + ("(Path(r['root'])/m['builds']['geth']['binary']['path']).write_bytes(b'changed after validation')\n" if mutate else "")
                                  + "print(json.dumps({'valid':True,'reason':'Validated'}))\n")
                # Only native semantics are replaced for this synthetic fixture;
                # actual reference hashing and candidate process execution remain.
                def inspect(value, directory, source, now):
                    return contract.validate_manifest(value, directory, source, allow_synthetic=True)
                with mock.patch.object(verifier.build_materials, "validate_materialized", side_effect=inspect) as final_check:
                    if mutate:
                        with self.assertRaisesRegex(ValueError, "artifact bytes differ"):
                            verifier.final_preflights(commands, manifest, artifacts, SOURCE)
                    else:
                        verifier.final_preflights(commands, manifest, artifacts, SOURCE)
                    self.assertEqual(final_check.call_count, 1)
                    self.assertEqual(len(commands.outcomes), len(manifest["plan"]["profiles"]))

    def test_final_materialized_budget_is_selected_by_trusted_orchestration(self):
        with tempfile.TemporaryDirectory() as root:
            commands = self.commands(root)
            artifacts = Path(root) / "artifacts"
            manifest = build_vectors.fixture(artifacts, SOURCE)
            with mock.patch.object(commands, "call", return_value=b'{"valid":true,"reason":"Validated"}') as called, mock.patch.object(verifier.build_materials, "validate_materialized") as checked:
                verifier.final_preflights(commands, manifest, artifacts, SOURCE)
            self.assertEqual(len(called.call_args_list), len(manifest["plan"]["profiles"]))
            self.assertEqual([item.kwargs["timeout"] for item in called.call_args_list], [300] * len(manifest["plan"]["profiles"]))
            checked.assert_called_once()

    def test_small_control_budget_does_not_follow_candidate_declarations(self):
        with tempfile.TemporaryDirectory() as root:
            commands = self.commands(root)
            artifacts = Path(root) / "artifacts"
            manifest = build_vectors.fixture(artifacts, SOURCE)
            manifest.update(evidence_kind="ACTUAL", declared_bytes=1 << 60, timeout_seconds=999999)
            with mock.patch.object(commands, "call", return_value=b'{"valid":true,"reason":"Validated"}') as called:
                commands.preflight("candidate-claims-actual", manifest, artifacts,
                                   alter=lambda request: request.update(kind="ActualMaterialized", timeout_seconds=999999))
            self.assertEqual(called.call_args.kwargs["timeout"], 10)
            with mock.patch.object(commands, "call", side_effect=lambda *args, **kwargs: contract.canonical({"valid": kwargs.get("expected", 0) == 0, "reason": "Synthetic control"})) as called:
                commands.preflight_controls()
            self.assertTrue(called.call_args_list)
            self.assertTrue(all(item.kwargs["timeout"] == 10 for item in called.call_args_list))
            negative = next(item for item in called.call_args_list if item.args[0] == "synthetic-cannot-be-actual")
            self.assertFalse(negative.args[2]["synthetic"])
            self.assertEqual(negative.kwargs["expected"], 2)

    def test_materialized_budget_keeps_original_deadline_and_owned_cleanup(self):
        with tempfile.TemporaryDirectory() as root:
            commands = self.commands(root)
            artifacts = Path(root) / "artifacts"
            manifest = build_vectors.fixture(artifacts, SOURCE)
            script = commands.candidate / "build/native/verify.py"
            script.parent.mkdir(parents=True)
            script.write_text("import time\ntime.sleep(30)\n")
            commands.deadline = time.time() + 0.3
            started = time.monotonic()
            with self.assertRaises(subprocess.TimeoutExpired):
                commands.preflight("materialized-timeout", manifest, artifacts, materialized=True)
            self.assertLess(time.monotonic() - started, 5)
            request = json.loads((commands.directory / "materialized-timeout.request.json").read_text())
            self.assertLessEqual(request["deadline"], commands.deadline)
            self.assertTrue(commands.outcomes[-1]["cleanup_confirmed"])
            self.assertIsNone(commands.outcomes[-1]["actual_exit"])
            self.assertTrue(build_candidate_guard.confirm_all(commands.groups)["complete"])
            with mock.patch.object(build_product_commands.subprocess, "Popen") as launched:
                with self.assertRaisesRegex(ValueError, "original launch deadline"):
                    commands.preflight("materialized-already-expired", manifest, artifacts, materialized=True)
                launched.assert_not_called()

    def test_plan_controls_keep_ten_seconds_and_invalid_budget_choices_do_not_dispatch(self):
        with tempfile.TemporaryDirectory() as root:
            commands = self.commands(root)
            def response(*args, **kwargs):
                if kwargs.get("expected") == 2:
                    return b""
                request = args[2]
                return contract.canonical(contract.expected_plan(commands.registry, commands.lock, commands.registry_hash,
                                          commands.lock_hash, request["platform"], request["limits"]))
            with mock.patch.object(commands, "call", side_effect=response) as called:
                commands.plans()
            self.assertEqual(len(called.call_args_list), 7)
            self.assertTrue(all(item.kwargs["timeout"] == 10 for item in called.call_args_list))
            artifacts = Path(root) / "artifacts"
            manifest = build_vectors.fixture(artifacts, SOURCE)
            for choice in ({"materialized": 1}, {"materialized": "true"}, {"materialized": True, "synthetic": True}):
                with self.subTest(choice=choice), mock.patch.object(commands, "call") as called:
                    with self.assertRaisesRegex(ValueError, "explicit trusted actual-mode choice"):
                        commands.preflight("invalid-budget-choice", manifest, artifacts, **choice)
                    called.assert_not_called()

    def test_materialized_preflight_still_rejects_malformed_candidate_responses(self):
        responses = ['{}', '{"valid":true,"reason":""}', '{"valid":true,"reason":"ok","timeout":300}',
                     '{"valid":false,"reason":"wrong verdict"}', 'not-json']
        with tempfile.TemporaryDirectory() as root:
            commands = self.commands(root)
            artifacts = Path(root) / "artifacts"
            manifest = build_vectors.fixture(artifacts, SOURCE)
            script = commands.candidate / "build/native/verify.py"
            script.parent.mkdir(parents=True)
            for index, response in enumerate(responses):
                script.write_text("print(" + repr(response) + ")\n")
                with self.subTest(response=response), self.assertRaises(ValueError):
                    commands.preflight("materialized-invalid-" + str(index), manifest, artifacts, materialized=True)
                self.assertTrue(commands.outcomes[-1]["cleanup_confirmed"])
            self.assertTrue(build_candidate_guard.confirm_all(commands.groups)["complete"])


if __name__ == "__main__":
    unittest.main()
