"""Test real process ownership, bounded output, stop, and interrupted recovery."""

import json
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from controller import AUTHORIZATION, Blocked, Store
from controller_backend import RealBackend
import controller_backend
import evidence
import session_runner


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.store = Store(self.root / "state")

    def request(self, command, seconds=10, log_limit=8192):
        directory = self.root / "session"
        evidence.durable_directory(directory)
        (directory / "stdin.txt").write_text("")
        request = {"operation_id": "test-session", "command": command, "cwd": str(self.root),
                   "deadline": time.time() + seconds, "stop_file": str(self.root / "stop.json"),
                   "log_limit": log_limit, "env": {"BOOTSTRAP_HEAD_SHA": "a" * 40}}
        evidence.atomic_json(directory / "request.json", request)
        return directory

    def launch(self, directory):
        return subprocess.Popen([sys.executable, str(Path(session_runner.__file__)), "--request",
                                 str(directory / "request.json"), "--lock-fd", str(self.store.lock_fd)],
                                pass_fds=(self.store.lock_fd,), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def wait_started(self, directory):
        deadline = time.monotonic() + 5
        while not (directory / "heartbeat.json").exists():
            self.assertLess(time.monotonic(), deadline)
            time.sleep(0.02)

    def test_inherited_lock_blocks_replacement_controller_until_child_exits(self):
        with self.store.lock():
            process = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(0.5)"],
                                       pass_fds=(self.store.lock_fd,))
        try:
            with self.assertRaises(Blocked):
                with Store(self.store.directory).lock():
                    self.fail("a live subprocess lost the controller lock")
        finally:
            process.wait(timeout=5)
        with Store(self.store.directory).lock():
            pass

    def test_operator_stop_terminates_owned_group_and_preserves_work(self):
        directory = self.request([sys.executable, "-c",
                                  "from pathlib import Path; import time; Path('work.txt').write_text('preserve'); time.sleep(30)"])
        with self.store.lock():
            process = self.launch(directory)
            try:
                self.wait_started(directory)
                deadline = time.monotonic() + 5
                while not (self.root / "work.txt").exists():
                    self.assertLess(time.monotonic(), deadline)
                    time.sleep(0.02)
                evidence.atomic_json(self.root / "stop.json", {"stop": True})
                process.wait(timeout=10)
            finally:
                if process.poll() is None:
                    process.terminate()
                    process.wait(timeout=10)
        result = json.loads((directory / "result.json").read_text())
        self.assertEqual(result["reason"], "stopped")
        self.assertIn(result["termination"], {"graceful", "forced"})
        self.assertEqual((self.root / "work.txt").read_text(), "preserve")

    def test_process_timeout_is_bounded_and_retained(self):
        directory = self.request([sys.executable, "-c", "import time; time.sleep(30)"], seconds=0.3)
        with self.store.lock():
            process = self.launch(directory)
            process.wait(timeout=10)
        result = json.loads((directory / "result.json").read_text())
        self.assertEqual(result["reason"], "timeout")
        self.assertNotEqual(result["exit_code"], 0)

    def test_output_is_bounded_without_restricting_worker_file_sizes(self):
        directory = self.request([sys.executable, "-c", "import sys; sys.stdout.write('x' * 1000000)"], log_limit=1024)
        with self.store.lock():
            self.launch(directory).wait(timeout=10)
        result = json.loads((directory / "result.json").read_text())
        self.assertEqual(result["reason"], "output-limit")
        self.assertLessEqual((directory / "stdout.jsonl").stat().st_size + (directory / "stderr.log").stat().st_size, 1024)

    def test_completed_process_keeps_actual_exit_and_environment_contract(self):
        directory = self.request([sys.executable, "-c", "import os; print(os.environ['BOOTSTRAP_HEAD_SHA'])"])
        with self.store.lock():
            self.launch(directory).wait(timeout=10)
        result = json.loads((directory / "result.json").read_text())
        self.assertEqual(result["reason"], "completed")
        self.assertEqual(result["exit_code"], 0)
        self.assertEqual((directory / "stdout.jsonl").read_text().strip(), "a" * 40)

    def test_stale_pid_record_cannot_signal_or_adopt_an_unrelated_process(self):
        backend = RealBackend.__new__(RealBackend)
        backend.root, backend.source, backend.store = self.root, self.root, self.store
        run = {"run_id": "test_run_123", "deadline": time.time() + 10, "authorization": AUTHORIZATION}
        directory = self.root / ".project/automation/runs/test_run_123/op"
        evidence.durable_directory(directory)
        command = [sys.executable, "-c", "raise SystemExit('must not launch')"]
        request = {"operation_id": "op", "command": command, "cwd": str(self.root),
                   "deadline": run["deadline"], "stop_file": str(self.store.directory / "stop/test_run_123.json"),
                   "log_limit": session_runner.LOG_LIMIT, "env": {}, "stdin_sha256": hashlib.sha256(b"").hexdigest()}
        (directory / "stdin.txt").write_text("")
        evidence.atomic_json(directory / "request.json", request)
        evidence.atomic_json(directory / "started.json", {"supervisor_pid": os.getpid(), "started_at": 0})
        with self.store.lock(), patch.object(subprocess, "Popen") as launch, patch.object(os, "killpg") as kill:
            with self.assertRaises(Blocked) as caught:
                backend.supervised(run, "op", command, self.root)
        self.assertEqual(caught.exception.code, "BLOCKED_SESSION_INTERRUPTED")
        launch.assert_not_called()
        kill.assert_not_called()

    def test_reaped_or_reused_group_leader_is_never_signaled(self):
        process = SimpleNamespace(pid=123456, returncode=None)
        with patch.object(os, "waitid", side_effect=ChildProcessError), patch.object(os, "killpg") as kill:
            self.assertEqual(session_runner.terminate_group(process), "ownership-lost")
        kill.assert_not_called()
        process.returncode = 0
        with patch.object(os, "killpg") as kill:
            self.assertEqual(session_runner.terminate_group(process), "ownership-lost")
        kill.assert_not_called()

    def test_uncertain_cleanup_cannot_return_successful_session(self):
        directory = self.request([sys.executable, "-c", "pass"])
        with self.store.lock(), patch.object(session_runner, "terminate_group", return_value="cleanup-denied"):
            result = session_runner.supervise(directory / "request.json", self.store.lock_fd)
        self.assertEqual(result["reason"], "cleanup-uncertain")
        self.assertTrue((directory / "cleanup.json").is_file())

    def test_sending_a_signal_is_not_proof_of_complete_group_cleanup(self):
        directory = self.request([sys.executable, "-c", "pass"])
        with self.store.lock(), patch.object(session_runner, "confirm_group_exit", return_value=False):
            result = session_runner.supervise(directory / "request.json", self.store.lock_fd)
        self.assertFalse(result["cleanup_complete"])
        self.assertEqual(result["reason"], "cleanup-uncertain")

    def test_backward_wall_clock_cannot_extend_session_timeout(self):
        directory = self.request([sys.executable, "-c", "import time; time.sleep(30)"], seconds=30)
        path = directory / "request.json"
        request = json.loads(path.read_text())
        request["timeout_seconds"] = 0.2
        evidence.atomic_json(path, request)
        old_time = time.time()
        started = time.monotonic()
        with self.store.lock(), patch.object(session_runner.time, "time", return_value=old_time - 10000):
            result = session_runner.supervise(path, self.store.lock_fd)
        self.assertEqual(result["reason"], "timeout")
        self.assertLess(time.monotonic() - started, 6)

    def test_stopped_supervisor_and_its_child_have_a_bounded_parent_watchdog(self):
        backend = RealBackend.__new__(RealBackend)
        backend.root, backend.source, backend.store = self.root, self.root, self.store
        source = self.root / ".project/automation/session_runner.py"
        source.parent.mkdir(parents=True)
        source.write_text("import os, signal, subprocess, sys, time\n"
                          "child = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)'], start_new_session=True)\n"
                          "os.kill(os.getpid(), signal.SIGSTOP)\n"
                          "time.sleep(30)\n")
        run = {"run_id": "watchdog_run", "deadline": time.time() + 10, "authorization": AUTHORIZATION}
        started = time.monotonic()
        with self.store.lock(), patch.object(controller_backend, "WATCHDOG_GRACE_SECONDS", 0.1):
            with self.assertRaises(Blocked) as caught:
                backend.supervised(run, "stopped-supervisor", [sys.executable, "-c", "pass"], self.root, timeout=0.2)
        self.assertEqual(caught.exception.code, "BLOCKED_SESSION_INTERRUPTED")
        path = self.root / ".project/automation/runs/watchdog_run/stopped-supervisor/watchdog.json"
        report = json.loads(path.read_text())
        self.assertEqual(report["reason"], "monotonic watchdog timeout")
        self.assertTrue(report["cleanup"]["child_groups"])
        self.assertLess(time.monotonic() - started, 10)
        with Store(self.store.directory).lock():
            pass

    def test_watchdog_cannot_resume_a_supervisor_into_a_new_worker_spawn(self):
        backend = RealBackend.__new__(RealBackend)
        backend.root, backend.source, backend.store = self.root, self.root, self.store
        source = self.root / ".project/automation/session_runner.py"
        source.parent.mkdir(parents=True)
        # The 0.3-second session timeout is also the supervisor's own dispatch
        # deadline. A supervisor that starts late (as under load) correctly
        # refuses to dispatch and never reaches the spawn boundary, so its
        # clock stays at the request time until it reaches Popen. The start
        # delay makes every run take that late-start path; the parent watchdog
        # still uses the real clock.
        source.write_text(
            "import json, os, signal, sys, time\n"
            "from pathlib import Path\n"
            "time.sleep(0.5)\n"
            f"sys.path.insert(0, {str(Path(session_runner.__file__).parent)!r})\n"
            "import session_runner\n"
            "request = json.loads(Path(sys.argv[sys.argv.index('--request') + 1]).read_text())\n"
            "class RequestClock:\n"
            "    frozen = True\n"
            "    wall = request['deadline'] - request['timeout_seconds']\n"
            "    steady = time.monotonic()\n"
            "    def time(self):\n"
            "        return self.wall if self.frozen else time.time()\n"
            "    def monotonic(self):\n"
            "        return self.steady if self.frozen else time.monotonic()\n"
            "    def __getattr__(self, name):\n"
            "        return getattr(time, name)\n"
            "clock = RequestClock()\n"
            "session_runner.time = clock\n"
            "original = session_runner.subprocess.Popen\n"
            "paused = False\n"
            "def before_spawn(*args, **kwargs):\n"
            "    global paused\n"
            "    first = not paused\n"
            "    clock.frozen = False\n"
            "    if not paused:\n"
            "        paused = True\n"
            f"        open({str(self.root / 'before-spawn')!r}, 'w').write('paused')\n"
            "        os.kill(os.getpid(), signal.SIGSTOP)\n"
            "    process = original(*args, **kwargs)\n"
            "    if first:\n"
            f"        Path({str(self.root / 'spawn-returned')!r}).write_text('spawned')\n"
            "        deadline = time.monotonic() + 1\n"
            f"        while not Path({str(self.root / 'worker-started')!r}).exists() and time.monotonic() < deadline:\n"
            "            time.sleep(0.005)\n"
            "    return process\n"
            "session_runner.subprocess.Popen = before_spawn\n"
            "raise SystemExit(session_runner.main())\n")
        marker = self.root / "worker-started"
        worker = ("import signal,time; from pathlib import Path; "
                  "signal.signal(signal.SIGTERM, signal.SIG_IGN); "
                  f"Path({str(marker)!r}).write_text('started'); time.sleep(6)")
        run = {"run_id": "prespawn_run", "deadline": time.time() + 15, "authorization": AUTHORIZATION}
        paused_at = []
        original_launch = subprocess.Popen

        def launch_and_wait_for_pause(*args, **kwargs):
            process = original_launch(*args, **kwargs)
            if isinstance(args[0], list) and len(args[0]) > 1 and args[0][1] == str(source):
                # A hang guard only: the frozen supervisor clock means a slow
                # start can delay, but never prevent, the spawn boundary.
                deadline = time.monotonic() + 60
                while not (self.root / "before-spawn").exists():
                    self.assertIsNone(process.poll(), "supervisor exited before the spawn boundary")
                    self.assertLess(time.monotonic(), deadline, "supervisor never reached the spawn boundary")
                    time.sleep(0.01)
                paused_at.append(time.monotonic())
            return process

        try:
            with self.store.lock(), patch.object(controller_backend, "WATCHDOG_GRACE_SECONDS", 0.1), \
                    patch.object(subprocess, "Popen", side_effect=launch_and_wait_for_pause):
                with self.assertRaises(Blocked):
                    backend.supervised(run, "pre-spawn", [sys.executable, "-c", worker], self.root, timeout=0.3)
            self.assertTrue((self.root / "before-spawn").exists())
            self.assertFalse((self.root / "spawn-returned").exists(), "watchdog admitted a spawn after its child census")
            self.assertFalse(marker.exists(), "watchdog resumed the supervisor into a new worker")
            with Store(self.store.directory).lock():
                pass
            # The watchdog is bounded from the pause, not from a worker that
            # sleeps six seconds; supervisor start time is excluded.
            self.assertEqual(len(paused_at), 1)
            self.assertLess(time.monotonic() - paused_at[0], 5)
        finally:
            # The regression worker is self-bounded even when run against the old
            # implementation, so a failing red test cannot leave a live helper.
            if marker.exists():
                remaining = marker.stat().st_mtime + 6.5 - time.time()
                if remaining > 0:
                    time.sleep(remaining)


class TerminateGroupPermissionTests(unittest.TestCase):
    """Darwin reports EPERM from killpg while a group leader is still exiting."""

    def run_group(self, denied, exits_after, live=False, grace=0.3):
        calls = []
        polls = []

        def killpg(group, signum):
            calls.append(signum)
            if signum in denied:
                raise PermissionError(1, "Operation not permitted")

        def exited(process):
            polls.append(True)
            return exits_after is not None and len(polls) > exits_after

        process = SimpleNamespace(pid=424242, returncode=None)
        with patch.object(session_runner.os, "killpg", side_effect=killpg), \
                patch.object(session_runner, "exited_unreaped", side_effect=exited), \
                patch.object(session_runner, "live_group_members", return_value=live), \
                patch.object(session_runner.time, "sleep"):
            return session_runner.terminate_group(process, grace=grace), calls

    def test_transient_eperm_while_polling_is_not_a_denial(self):
        result, calls = self.run_group({0}, exits_after=3)
        self.assertEqual(result, "graceful")
        self.assertNotIn(9, calls)

    def test_transient_eperm_on_sigterm_waits_for_the_exit(self):
        result, calls = self.run_group({15}, exits_after=3)
        self.assertEqual(result, "already-exited")
        self.assertEqual(calls, [15])

    def test_persistent_eperm_with_live_members_is_a_denial(self):
        for denied in ({15}, {0, 9}):
            with self.subTest(denied=sorted(denied)):
                result, _ = self.run_group(denied, exits_after=None, live=True, grace=0.05)
                self.assertEqual(result, "cleanup-denied")

    def test_exited_leader_with_live_members_is_not_gone(self):
        result, calls = self.run_group({0, 9}, exits_after=0, live=True, grace=0.05)
        self.assertEqual(result, "cleanup-denied")
        self.assertEqual(calls[-1], 9)

    def test_eperm_on_sigkill_after_exit_is_graceful(self):
        exited = []

        def killpg(group, signum):
            if signum == 9:
                exited.append(True)
            if signum in (0, 9):
                raise PermissionError(1, "Operation not permitted")

        process = SimpleNamespace(pid=424242, returncode=None)
        with patch.object(session_runner.os, "killpg", side_effect=killpg), \
                patch.object(session_runner, "exited_unreaped", side_effect=lambda process: bool(exited)), \
                patch.object(session_runner, "live_group_members", return_value=False), \
                patch.object(session_runner.time, "sleep"):
            self.assertEqual(session_runner.terminate_group(process, grace=0.05), "graceful")


if __name__ == "__main__":
    unittest.main()
