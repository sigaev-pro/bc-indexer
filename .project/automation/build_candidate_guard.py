"""Keep a candidate group tied to its parent without signaling reused IDs."""

import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import time

if __name__ == "__main__":
    sys.path.insert(0, str(Path(__file__).resolve().parent))

import evidence


def confirm_all(directory, timeout=5):
    """Observe registered groups; never signal a PID obtained from an old file."""
    directory = Path(directory)
    if not directory.is_dir() or directory.is_symlink():
        return {"complete": False, "groups": [], "error": "candidate group inventory is missing or unsafe"}
    deadline = time.monotonic() + timeout
    while True:
        groups, complete = [], True
        try:
            for path in sorted(directory.glob("*.json")):
                if path.is_symlink():
                    raise ValueError("candidate group ownership record is a symlink")
                row = json.loads(path.read_text())
                if (type(row) is not dict or set(row) != {"token", "state", "group"}
                        or row.get("token") != path.stem or row.get("state") not in {"pending", "running", "complete"}):
                    raise ValueError("invalid candidate group ownership record")
                group = row.get("group")
                if row["state"] != "pending" and (type(group) is not int or group <= 0):
                    raise ValueError("candidate group identity is invalid")
                if row["state"] == "complete":
                    groups.append({"token": path.stem, "group": group, "complete": True})
                    continue
                if type(group) is not int or group <= 0:
                    groups.append({"token": path.stem, "group": None, "complete": False})
                    complete = False
                    continue
                observed = subprocess.run(["ps", "-axo", "pgid=,stat="], capture_output=True, text=True, timeout=2, check=True)
                live = any(len(parts := line.split()) == 2 and parts[0] == str(group) and not parts[1].startswith("Z")
                           for line in observed.stdout.splitlines())
                groups.append({"token": path.stem, "group": group, "complete": not live})
                complete = complete and not live
        except (OSError, ValueError, TypeError, subprocess.SubprocessError) as error:
            return {"complete": False, "groups": groups, "error": str(error)}
        if complete or time.monotonic() >= deadline:
            return {"complete": complete, "groups": groups}
        time.sleep(0.02)


def parent_gone(descriptor):
    ready, _, _ = select.select([descriptor], [], [], 0.02)
    return bool(ready) and os.read(descriptor, 1) == b""


def main():
    request_path, intent_path, descriptor = Path(sys.argv[1]), Path(sys.argv[2]), int(sys.argv[3])
    request = json.loads(request_path.read_text())
    group = os.getpgrp()
    if group != os.getpid():
        raise ValueError("candidate guard is not its owned group leader")
    evidence.atomic_json(intent_path, {"token": intent_path.stem, "state": "running", "group": group})
    deadline = time.monotonic() + max(0, request["deadline"] - time.time())
    result = Path(request["result"])
    try:
        if parent_gone(descriptor):
            return
        if time.monotonic() >= deadline:
            evidence.atomic_json(result, {"group": group, "exit_code": None, "error": "timeout"})
            return
        with Path(request["stdin"]).open("rb") as stdin:
            environment = dict(os.environ)
            environment.pop("BOOTSTRAP_CANDIDATE_GROUPS", None)
            # The candidate and all ordinary descendants stay in this guard's
            # group. They never inherit the parent's write end of the lifeline.
            child = subprocess.Popen(request["command"], stdin=stdin, close_fds=True, env=environment)
        reported = False
        while True:
            if parent_gone(descriptor):
                return
            if time.monotonic() >= deadline:
                if not reported:
                    evidence.atomic_json(result, {"group": group, "exit_code": None, "error": "timeout"})
                return
            if not reported and child.poll() is not None:
                evidence.atomic_json(result, {"group": group, "exit_code": child.returncode, "error": None})
                reported = True
            # Stay alive as the reserved group leader until the parent finishes
            # trusted cleanup or its lifeline closes, even after the child exits.
    finally:
        # This process is still the live leader, so its own group ID cannot have
        # been reused. This also covers forced death of the parent driver.
        os.killpg(group, signal.SIGKILL)


if __name__ == "__main__":
    main()
