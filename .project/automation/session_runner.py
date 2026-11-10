"""Supervise one bounded process group while retaining the controller lock."""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import signal
import selectors
import subprocess
import time

import acceptance
import evidence
import build_candidate_guard

LOG_LIMIT = 8 * 1024 * 1024


def file_hash(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def exited_unreaped(process):
    """Inspect our own child without releasing its PID for possible reuse."""
    if process.returncode is not None:
        raise ChildProcessError("child was already reaped")
    event = os.waitid(os.P_PID, process.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT)
    # Darwin can return CLD_STOPPED even when WSTOPPED was not requested.
    return event is not None and event.si_pid == process.pid and event.si_code in {os.CLD_EXITED, os.CLD_KILLED, os.CLD_DUMPED}


def live_group_members(group):
    rows = subprocess.run(["ps", "-axo", "pgid=,stat="], text=True, capture_output=True, timeout=2)
    acceptance.require(rows.returncode == 0, "cannot inspect owned process group")
    return any(len(values := line.split()) == 2 and values[0] == str(group) and not values[1].startswith("Z")
               for line in rows.stdout.splitlines())


def group_gone(process):
    return exited_unreaped(process) and not live_group_members(process.pid)


def terminate_group(process, grace=3):
    """Signal only a direct child whose unreaped leader still reserves the group ID.

    Darwin returns EPERM from killpg while the group leader is exiting but
    before waitid reports the exit. EPERM is therefore a denial only when it
    persists until the grace deadline with live group members.
    """
    try:
        exited_unreaped(process)
    except ChildProcessError:
        return "ownership-lost"
    deadline = time.monotonic() + grace
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return "already-exited"
    except PermissionError:
        while True:
            if group_gone(process):
                return "already-exited"
            if time.monotonic() >= deadline:
                return "cleanup-denied"
            time.sleep(0.02)
    while time.monotonic() < deadline:
        try:
            os.killpg(process.pid, 0)
        except ProcessLookupError:
            return "graceful"
        except PermissionError:
            pass
        if group_gone(process):
            return "graceful"
        time.sleep(0.05)
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        return "graceful"
    except PermissionError:
        return "graceful" if group_gone(process) else "cleanup-denied"
    return "forced"


def confirm_group_exit(process, timeout=2):
    """Prove the group is empty while its direct-child leader is still reserved."""
    deadline = time.monotonic() + timeout
    while True:
        if exited_unreaped(process) and not live_group_members(process.pid):
            return True
        if time.monotonic() >= deadline:
            return False
        time.sleep(0.02)


def watchdog_cleanup(process, grace=2):
    """Bound cleanup even if a supervisor is stopped or cannot execute its handler."""
    try:
        exited_unreaped(process)
    except ChildProcessError:
        return {"supervisor": "ownership-lost", "child_groups": []}
    children = []
    denied = []
    # Keep this direct child unreaped. Freeze it before inspecting its children so
    # it cannot reap their leaders and allow their numeric process IDs to be reused.
    os.kill(process.pid, signal.SIGSTOP)
    frozen = False
    deadline = time.monotonic() + grace
    while time.monotonic() < deadline:
        row = subprocess.run(["ps", "-p", str(process.pid), "-o", "stat="],
                             text=True, capture_output=True, timeout=1)
        if row.stdout.strip().startswith("T"):
            frozen = True
            break
        if exited_unreaped(process):
            break
        time.sleep(0.02)
    if frozen:
        rows = subprocess.run(["ps", "-axo", "pid=,ppid=,pgid="], text=True,
                              capture_output=True, timeout=2)
        acceptance.require(rows.returncode == 0, "cannot inspect supervised child ownership")
        for line in rows.stdout.splitlines():
            values = line.split()
            if len(values) == 3 and all(value.isdigit() for value in values):
                pid, parent, group = map(int, values)
                if parent == process.pid and group == pid:
                    children.append(pid)
        for group in children:
            try:
                os.killpg(group, signal.SIGTERM)
            except ProcessLookupError:
                pass
            except PermissionError:
                if live_group_members(group):
                    denied.append(group)
        if children:
            time.sleep(grace)
        for group in children:
            try:
                os.killpg(group, signal.SIGKILL)
            except ProcessLookupError:
                pass
            except PermissionError:
                if live_group_members(group) and group not in denied:
                    denied.append(group)
    # Never resume this supervisor after the child census. It may be stopped
    # immediately before Popen, and resuming it could admit an unseen worker.
    # Its unreaped leader still reserves this group ID through final cleanup.
    supervisor = "forced-while-stopped" if frozen else "forced-with-uncertain-child-ownership"
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        supervisor = "already-exited"
    except PermissionError:
        supervisor = "cleanup-denied"
    return {"supervisor": supervisor, "child_groups": children,
            "cleanup_denied_groups": denied,
            "cleanup_uncertain": not frozen or bool(denied) or supervisor == "cleanup-denied",
            "ownership": "unreaped direct supervisor; no resume between child census and final group termination"}


def supervise(request_path, lock_fd, on_interrupt=None):
    request_path = Path(request_path)
    request = acceptance.parse_json(request_path.read_text())
    directory = request_path.parent
    result_path = directory / "result.json"
    request_hash = evidence.digest(request)
    docker = None
    docker_inventory = directory / "docker-resources.json"
    if "owned_docker" in request:
        import build_resources as docker
        docker.validate(request["owned_docker"])
    os.fstat(lock_fd)
    if result_path.exists():
        saved = acceptance.parse_json(result_path.read_text())
        acceptance.require(saved["request_sha256"] == request_hash, "saved result belongs to another request")
        if docker is not None:
            docker.validate_cleanup(request["owned_docker"], saved["docker_cleanup"])
            acceptance.require(not docker.members(request["owned_docker"]), "owned Docker resources reappeared after cleanup")
        if "candidate_cleanup" in saved:
            acceptance.require(saved["candidate_cleanup"]["complete"] and build_candidate_guard.confirm_all(directory / "candidate-groups")["complete"],
                               "saved session has uncertain candidate cleanup")
        return saved
    claim_fd = os.open(directory / "session.lock", os.O_RDWR | os.O_CREAT, 0o600)
    try:
        fcntl.flock(claim_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BaseException:
        os.close(claim_fd)
        raise
    process = None
    interrupted = False
    interruption_error = None
    old_handlers = {}

    def interrupt(signum, frame):
        nonlocal interrupted, interruption_error
        interrupted = True
        if on_interrupt is not None:
            try:
                on_interrupt(signum, frame)
            except BaseException as error:
                # A caller's interrupt exception must not bypass owned cleanup.
                interruption_error = error

    try:
        for signum in (signal.SIGTERM, signal.SIGINT):
            old_handlers[signum] = signal.signal(signum, interrupt)
        stdout_path, stderr_path = directory / "stdout.jsonl", directory / "stderr.log"
        started = time.time()
        deadline = time.monotonic() + min(request.get("timeout_seconds", 3600), max(0, request["deadline"] - started))
        reason, termination = "completed", "not-needed"
        evidence.atomic_json(directory / "started.json", {"request_sha256": request_hash,
                             "operation_id": request["operation_id"], "supervisor_pid": os.getpid(),
                             "started_at": started})
        for output in (stdout_path, stderr_path):
            acceptance.require(not output.exists(), "session output already exists without a completed result")
        with stdout_path.open("xb") as stdout, stderr_path.open("xb") as stderr:
            os.chmod(stdout_path, 0o600)
            os.chmod(stderr_path, 0o600)
            stopped = interrupted or Path(request["stop_file"]).exists()
            if stopped or time.time() >= request["deadline"] or time.monotonic() >= deadline:
                for stream in (stdout, stderr):
                    stream.flush()
                    os.fsync(stream.fileno())
                result = {"schema_version": 1, "operation_id": request["operation_id"],
                          "request_sha256": request_hash, "started_at": started, "finished_at": time.time(),
                          "exit_code": None, "reason": "stopped" if stopped else "timeout",
                          "termination": "not-started", "cleanup_complete": True,
                          "stdout_sha256": file_hash(stdout_path), "stderr_sha256": file_hash(stderr_path)}
                if docker is not None:
                    result["docker_cleanup"] = {"schema_version": 1, "nonce": request["owned_docker"]["nonce"],
                        "complete": True, "resources": [], "remaining": [], "not_started": True,
                        "creation_inventory": {"schema_version": 1, "nonce": request["owned_docker"]["nonce"], "intents": []}}
                evidence.atomic_json(result_path, result)
                if interruption_error is not None:
                    raise interruption_error
                return result
            with (directory / "stdin.txt").open("rb") as stdin:
                acceptance.require(time.time() < request["deadline"] and time.monotonic() < deadline
                                   and not interrupted and not Path(request["stop_file"]).exists(),
                                   "session deadline or stop forbids worker dispatch")
                if docker is not None:
                    docker.begin(request["owned_docker"], docker_inventory)
                candidate_groups = directory / "candidate-groups"
                candidate_groups.mkdir(exist_ok=True)
                environment = dict(os.environ, **request.get("env", {}))
                environment["BOOTSTRAP_CANDIDATE_GROUPS"] = str(candidate_groups)
                process = subprocess.Popen(request["command"], cwd=request["cwd"], stdin=stdin,
                                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True,
                                           pass_fds=(lock_fd,), env=environment)
            selector = selectors.DefaultSelector()
            selector.register(process.stdout, selectors.EVENT_READ, stdout)
            selector.register(process.stderr, selectors.EVENT_READ, stderr)
            written = 0
            last_heartbeat = 0.0
            while True:
                now = time.time()
                if now - last_heartbeat >= 1:
                    evidence.atomic_json(directory / "heartbeat.json", {
                        "operation_id": request["operation_id"], "supervisor_pid": os.getpid(),
                        "process_group": process.pid, "started_at": started, "heartbeat_at": now})
                    last_heartbeat = now
                if interrupted or Path(request["stop_file"]).exists():
                    reason = "stopped"
                elif time.monotonic() >= deadline:
                    reason = "timeout"
                elif written >= request["log_limit"]:
                    reason = "output-limit"
                elif exited_unreaped(process) and not selector.get_map():
                    break
                else:
                    for key, _ in selector.select(timeout=0.1):
                        if written >= request["log_limit"]:
                            break
                        chunk = os.read(key.fd, min(65536, max(1, request["log_limit"] - written)))
                        if not chunk:
                            selector.unregister(key.fileobj)
                            key.fileobj.close()
                        else:
                            key.data.write(chunk)
                            written += len(chunk)
                    continue
                termination = terminate_group(process)
                break
            selector.close()
            for pipe in (process.stdout, process.stderr):
                if not pipe.closed:
                    pipe.close()
            # A process can exit while descendants keep its process group alive.
            if termination == "not-needed":
                termination = terminate_group(process, grace=0.1)
            cleanup_complete = termination in {"already-exited", "graceful", "forced"} and confirm_group_exit(process)
            if not cleanup_complete:
                reason = "cleanup-uncertain"
                evidence.atomic_json(directory / "cleanup.json", {"operation_id": request["operation_id"],
                                     "termination": termination, "reason": reason})
            process.wait(timeout=10)
            candidate_cleanup = build_candidate_guard.confirm_all(candidate_groups)
            cleanup_complete = cleanup_complete and candidate_cleanup["complete"]
            if not cleanup_complete:
                reason = "cleanup-uncertain"
            docker_cleanup = None
            if docker is not None:
                docker_cleanup = docker.cleanup(request["owned_docker"], docker_inventory)
                evidence.atomic_json(directory / "docker-cleanup.json", docker_cleanup)
                cleanup_complete = cleanup_complete and docker_cleanup["complete"]
                if not cleanup_complete:
                    reason = "cleanup-uncertain"
            stdout.flush()
            stderr.flush()
            os.fsync(stdout.fileno())
            os.fsync(stderr.fileno())
        result = {"schema_version": 1, "operation_id": request["operation_id"],
                  "request_sha256": request_hash, "started_at": started, "finished_at": time.time(),
                  "exit_code": process.returncode, "reason": reason, "termination": termination,
                  "cleanup_complete": cleanup_complete, "candidate_cleanup": candidate_cleanup,
                  "stdout_sha256": file_hash(stdout_path), "stderr_sha256": file_hash(stderr_path)}
        if docker is not None:
            result["docker_cleanup"] = docker_cleanup
        evidence.atomic_json(result_path, result)
        if interruption_error is not None:
            raise interruption_error
        return result
    finally:
        try:
            if process is not None and process.returncode is None:
                terminate_group(process)
                process.wait(timeout=10)
            if docker is not None and docker_inventory.exists() and not result_path.exists():
                evidence.atomic_json(directory / "docker-cleanup.json",
                                     docker.cleanup(request["owned_docker"], docker_inventory))
        finally:
            try:
                for signum, handler in old_handlers.items():
                    signal.signal(signum, handler)
            finally:
                os.close(claim_fd)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--request", required=True, type=Path)
    parser.add_argument("--lock-fd", required=True, type=int)
    args = parser.parse_args()
    try:
        result = supervise(args.request, args.lock_fd)
        return 0 if result["reason"] == "completed" else 1
    except (OSError, ValueError, KeyError, subprocess.SubprocessError):
        # Full commands and arbitrary environment are deliberately absent from errors.
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
