"""One owned Docker command, with a parent lifeline and bounded cleanup."""

import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import sys
import time


OUTPUT_LIMIT = 8 * 1024 * 1024


def exited(process):
    # WNOWAIT keeps the leader's PID reserved until group termination is done.
    return os.waitid(os.P_PID, process.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT)


def group_absent(group):
    deadline = time.monotonic() + 4
    while time.monotonic() < deadline:
        try:
            snapshot = subprocess.run(["ps", "-axo", "pid=,pgid="], capture_output=True, text=True, timeout=1)
        except (OSError, subprocess.SubprocessError):
            return False
        if snapshot.returncode != 0:
            return False
        rows = [line.split() for line in snapshot.stdout.splitlines() if line.strip()]
        if not all(len(row) == 2 and all(value.isdecimal() for value in row) for row in rows):
            return False
        if not any(int(row[1]) == group for row in rows):
            return True
        time.sleep(0.02)
    return False


def execute(request, lifeline):
    stopped = []
    for number in (signal.SIGTERM, signal.SIGINT):
        signal.signal(number, lambda *_: stopped.append("INTERRUPTED"))
    output = Path(request["output"])
    process = None
    selector = selectors.DefaultSelector()
    selector.register(lifeline, selectors.EVENT_READ, "parent")
    reason = None
    cleanup = False
    code = None
    counts = {"stdout": 0, "stderr": 0}
    start = time.monotonic()
    streams = {}
    process_group_absent = True
    try:
        for label in counts:
            streams[label] = output.with_suffix("." + label).open("xb")
        if selector.select(0):
            reason = "PARENT_EXIT"
        elif start >= request["deadline"]:
            reason = "DEADLINE"
        else:
            process = subprocess.Popen(request["argv"], stdout=subprocess.PIPE,
                                       stderr=subprocess.PIPE, start_new_session=True)
            for label in counts:
                selector.register(getattr(process, label), selectors.EVENT_READ, label)
            while exited(process) is None or len(selector.get_map()) > 1:
                if stopped:
                    reason = stopped[0]
                    break
                if time.monotonic() >= request["deadline"]:
                    reason = "DEADLINE"
                    break
                for key, _ in selector.select(min(0.05, max(0, request["deadline"] - time.monotonic()))):
                    if key.data == "parent":
                        if not os.read(lifeline, 1):
                            reason = "PARENT_EXIT"
                            break
                    else:
                        data = os.read(key.fd, 65536)
                        if not data:
                            selector.unregister(key.fileobj)
                            continue
                        available = max(0, OUTPUT_LIMIT - counts[key.data])
                        streams[key.data].write(data[:available])
                        counts[key.data] += len(data)
                        if counts[key.data] > OUTPUT_LIMIT:
                            reason = "OUTPUT_LIMIT"
                            break
                if reason:
                    break
            if not reason:
                event = exited(process)
                code = event.si_status if event.si_code == os.CLD_EXITED else -event.si_status
                if code:
                    reason = "NATIVE_EXIT"
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        reason = type(error).__name__
    finally:
        # The child remains owned until its process group has been terminated and
        # reaped. Container cleanup is independent of Docker CLI termination.
        if process is not None:
            # No poll()/wait() has reaped the leader, even if it exited while a
            # descendant retained a pipe. Never signal this group after reaping.
            process_group_absent = False
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass
        try:
            removed = subprocess.run([request["docker"], "rm", "-f", request["container"]],
                                     capture_output=True, timeout=10)
            listed = subprocess.run([request["docker"], "container", "ls", "-aq", "--filter",
                                     "name=^/" + request["container"] + "$"],
                                    capture_output=True, timeout=10)
            cleanup = listed.returncode == 0 and not listed.stdout.strip()
            # Preserve bounded cleanup responses; absence, not rm's exit alone,
            # establishes cleanup after a normal --rm container exit.
            output.with_suffix(".cleanup.json").write_text(json.dumps({
                "remove_exit": removed.returncode, "list_exit": listed.returncode,
                "remove_stdout": removed.stdout[:4096].decode(errors="replace"),
                "remove_stderr": removed.stderr[:4096].decode(errors="replace"),
                "list_stdout": listed.stdout[:4096].decode(errors="replace"),
                "list_stderr": listed.stderr[:4096].decode(errors="replace"),
            }, sort_keys=True) + "\n")
        except (OSError, subprocess.SubprocessError):
            cleanup = False
        if process is not None:
            try:
                code = process.wait(timeout=5)
                process_group_absent = group_absent(process.pid)
            except subprocess.SubprocessError:
                process_group_absent = False
            process.stdout.close()
            process.stderr.close()
        cleanup = cleanup and process_group_absent
        for stream in streams.values():
            stream.flush()
            os.fsync(stream.fileno())
            stream.close()
        selector.close()
        result = {"argv": request["argv"], "container": request["container"],
                  "exit_code": code, "reason": reason, "cleanup_complete": cleanup,
                  "process_group_absent": process_group_absent,
                  "observed_bytes": counts, "retained_limit": OUTPUT_LIMIT,
                  "elapsed_seconds": time.monotonic() - start}
        output.with_suffix(".result.json").write_text(json.dumps(result, sort_keys=True) + "\n")
    return 0 if code == 0 and reason is None and cleanup else 2


if __name__ == "__main__":
    raise SystemExit(execute(json.loads(Path(sys.argv[1]).read_text()), int(sys.argv[2])))
