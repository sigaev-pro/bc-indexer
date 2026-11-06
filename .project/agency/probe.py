#!/usr/bin/env python3
"""Check that a real Codex session receives the assigned project role."""

import argparse
import json
import os
import signal
import subprocess
import uuid

import roles


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--role", choices=list(roles.registry()["roles"]), required=True)
    parser.add_argument("--timeout", type=int, default=45)
    args = parser.parse_args()
    if not 1 <= args.timeout <= 300:
        parser.error("timeout must be between 1 and 300 seconds")
    lock = json.loads((roles.AGENCY / "toolchain.lock.json").read_text())["codex"]
    executable = roles.ROOT / lock["executable"]
    if not executable.is_file():
        parser.exit(2, "The pinned project Codex CLI is missing; follow agency/README.md.\n")
    try:
        version = subprocess.run([str(executable), "--version"], capture_output=True, text=True,
                                 timeout=30, check=True).stdout.strip()
    except (subprocess.SubprocessError, OSError):
        parser.exit(2, "The project Codex CLI version check failed.\n")
    if version != "codex-cli " + lock["version"]:
        parser.exit(2, "The project Codex CLI does not match the pinned version.\n")
    package_lock = json.loads((executable.parents[2] / "package-lock.json").read_text())
    package = package_lock["packages"]["node_modules/@openai/codex"]
    if package["version"] != lock["version"] or package["integrity"] != lock["integrity"]:
        parser.exit(2, "The installed package lock does not match the pinned package.\n")
    head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=roles.ROOT, text=True).strip()
    handoff = {
        "task_id": "BOOT-02", "stage": "BOOT", "stage_limit": "BOOT",
        "authorization_ref": "D86", "run_id": "bootstrap-role-probe",
        "session_id": str(uuid.uuid4()), "base_commit": head, "spec_commit": head,
        "worktree": str(roles.ROOT), "read_only": True, "allowed_paths": [],
        "acceptance_criteria": ["Return the assigned role and BOOT-only restrictions accurately."],
        "task": "Installation probe only. The complete role and rules are supplied. Do not execute the implementation task or use tools. Return the schema fields from these instructions.",
    }
    prompt, revision = roles.render(args.role, handoff)
    command = [str(executable), "exec", "--sandbox", "read-only", "-c", 'approval_policy="never"',
               "--ephemeral", "--json", "--output-schema", str(roles.AGENCY / "probe.schema.json"), "-"]
    process = subprocess.Popen(command, cwd=roles.ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, text=True, start_new_session=True)
    timed_out = False
    try:
        stdout, _stderr = process.communicate(prompt, timeout=args.timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        os.killpg(process.pid, signal.SIGTERM)
        try:
            stdout, _stderr = process.communicate(timeout=3)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            stdout, _stderr = process.communicate()
    events = []
    for line in stdout.splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    messages = [event["item"].get("text", "") for event in events
                if event.get("type") == "item.completed" and event.get("item", {}).get("type") == "agent_message"]
    expected = {"role": args.role, "stage_limit": "BOOT", "read_only": True,
                "project_language": "American English", "can_accept_own_work": False, "can_start_e01": False}
    try:
        actual = json.loads(messages[-1]) if messages else None
    except json.JSONDecodeError:
        actual = None
    completed = any(event.get("type") == "turn.completed" for event in events)
    matches = json.dumps(actual, sort_keys=True) == json.dumps(expected, sort_keys=True)
    passed = process.returncode == 0 and not timed_out and completed and matches
    print(json.dumps({"role": args.role, "role_revision": revision, "base_commit": head,
                      "session_id": handoff["session_id"], "cli_version": lock["version"],
                      "exit_code": process.returncode, "timed_out": timed_out,
                      "turn_completed": completed, "response_matches": matches,
                      "status": "PASS" if passed else "FAIL"}, indent=2))
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
