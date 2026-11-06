#!/usr/bin/env python3
"""Report bootstrap capabilities without reading credential contents."""

import argparse
import json
import platform
import shutil
import subprocess
import uuid
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
PROBE_IMAGE = "alpine@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce"


def run(args, timeout=20):
    try:
        result = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, timeout=timeout)
        return {"exit_code": result.returncode, "stdout": result.stdout.strip(),
                "stderr": result.stderr.strip()[-1000:]}
    except (OSError, subprocess.TimeoutExpired) as error:
        return {"exit_code": None, "error": type(error).__name__}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", action="store_true", help="Run a disposable, offline container probe")
    args = parser.parse_args()
    result = {"platform": platform.system(), "machine": platform.machine(),
              "disk_free_bytes": shutil.disk_usage(ROOT).free,
              "git": run(["git", "--version"]), "github": run(["gh", "api", "user", "--jq", ".login"]),
              "remote": run(["git", "ls-remote", "origin", "refs/heads/main"]),
              "docker_context": run(["docker", "context", "show"]),
              "docker_version": run(["docker", "version", "--format", "{{json .Server.Version}}"])}
    info = run(["docker", "info", "--format", "{{json .}}"])
    if info["exit_code"] == 0:
        data = json.loads(info["stdout"])
        result["docker"] = {key: data.get(key) for key in
                            ("NCPU", "MemTotal", "OSType", "Architecture", "Driver", "Warnings")}
    else:
        result["docker"] = info
    if platform.system() == "Darwin":
        result["host_memory_bytes"] = run(["sysctl", "-n", "hw.memsize"])
        result["power_policy"] = run(["pmset", "-g", "custom"])
    if args.container:
        run_id = uuid.uuid4().hex
        name = "extractor-boot04-" + run_id
        command = ["docker", "run", "--name", name, "--rm", "--pull", "never", "--network", "none", "--read-only",
                   "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "64m",
                   "--cpus", "0.25", "--pids-limit", "32", "--user", "65534:65534",
                   "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=8m", "--label", "extractor.task=BOOT-04",
                   "--label", "extractor.probe=" + run_id,
                   PROBE_IMAGE, "/bin/sh", "-c",
                   'test "$(id -u)" = 65534 && test ! -w / && test -z "$(ip route show default)" && '
                   'printf "BOOT-04 container probe passed\\n" > /tmp/probe && cat /tmp/probe']
        result["container_probe"] = {"image": PROBE_IMAGE, **run(command, timeout=30)}
        if result["container_probe"]["exit_code"] is None:
            owned = run(["docker", "inspect", "--format", '{{index .Config.Labels "extractor.probe"}}', name])
            if owned["exit_code"] == 0 and owned["stdout"] == run_id:
                result["container_cleanup"] = run(["docker", "rm", "--force", name])
    print(json.dumps(result, indent=2))
    required = ("git", "github", "remote", "docker_version")
    failed = any(result[name]["exit_code"] != 0 for name in required)
    if args.container:
        failed |= result["container_probe"]["exit_code"] != 0
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
