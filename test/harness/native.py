"""The fixed offline Docker boundary used by preparation and measured import."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time
import uuid

from artifacts import Rejected, load, reference, require, copy_artifact


class Native:
    def __init__(self, output, deadline):
        self.output = output
        self.deadline = deadline
        self.docker = shutil.which("docker")
        require(self.docker, "Docker executable is unavailable")
        self.commands = []
        self.failed = False
        self.cleanup_complete = True
        self.nonce = uuid.uuid4().hex
        (output / "logs").mkdir()

    def command(self, image, argv, mounts, cap=120, memory="768m", environment=None, cwd=None):
        require(not self.failed, "No dispatch after a failed command")
        # Reserve bounded owned cleanup inside the one overall deadline.
        deadline = min(self.deadline, time.monotonic() + cap) - 35
        require(time.monotonic() < deadline, "Insufficient time for execution and cleanup")
        index = len(self.commands)
        name = f"extractor-harness-{self.nonce}-{index}"
        args = [self.docker, "run", "--rm", "--pull", "never", "--name", name,
                "--label", f"extractor.harness={self.nonce}", "--network", "none",
                "--platform", "linux/amd64", "--cpus", "2", "--memory", memory,
                "--pids-limit", "128", "--read-only", "--cap-drop", "ALL",
                "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,size=256m"]
        for source, target, readonly in mounts:
            require("," not in str(source), "Mount path cannot contain a comma")
            args += ["--mount", f"type=bind,source={source},target={target}" + (",readonly" if readonly else "")]
        for key, value in sorted((environment or {}).items()):
            args += ["--env", f"{key}={value}"]
        if cwd:
            args += ["--workdir", cwd]
        args += [image, *argv]
        prefix = self.output / "logs" / f"{index:03d}"
        request_path = prefix.with_suffix(".request.json")
        request_path.write_text(json.dumps({"argv": args, "docker": self.docker,
                                          "container": name, "deadline": deadline,
                                          "output": str(prefix)}, sort_keys=True) + "\n")
        with request_path.open("rb") as stream:
            os.fsync(stream.fileno())
        read_fd, write_fd = os.pipe()
        process = None
        self.cleanup_complete = False
        self.failed = True
        try:
            process = subprocess.Popen([sys.executable, "-I", str(Path(__file__).with_name("owned.py")),
                                        str(request_path), str(read_fd)], pass_fds=(read_fd,),
                                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                       start_new_session=True)
            os.close(read_fd)
            read_fd = None
            process.wait(timeout=max(1, self.deadline - time.monotonic()))
        finally:
            os.close(write_fd)
            if read_fd is not None:
                os.close(read_fd)
            if process is not None and process.poll() is None:
                process.wait(timeout=35)
            result_path = prefix.with_suffix(".result.json")
            if result_path.exists():
                result = load(result_path, 1024 * 1024, self.deadline)
                self.commands.append({"request": reference(request_path, 1024 * 1024, self.deadline),
                                      "result": reference(result_path, 1024 * 1024, self.deadline),
                                      "stdout": reference(prefix.with_suffix(".stdout"), 8 * 1024 * 1024, self.deadline),
                                      "stderr": reference(prefix.with_suffix(".stderr"), 8 * 1024 * 1024, self.deadline),
                                      "cleanup": reference(prefix.with_suffix(".cleanup.json"), 1024 * 1024, self.deadline)})
                self.failed = result["reason"] is not None or result["exit_code"] != 0 or not result["cleanup_complete"]
                self.cleanup_complete = result["cleanup_complete"]
            else:
                self.failed = True
        require(not self.failed, f"Native command failed; inspect {result_path.name}")
        return prefix.with_suffix(".stdout")


def copy_verified(source, destination, expected, deadline=None):
    return copy_artifact(source, destination, expected, deadline=deadline)


def new_output(path):
    require(path.is_absolute() and ".." not in path.parts, "Output must be an absolute path")
    for parent in path.parents:
        require(not parent.is_symlink(), "Output ancestor is a symlink")
    path.mkdir(mode=0o700)
    return path
