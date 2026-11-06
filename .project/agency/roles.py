#!/usr/bin/env python3
"""Load pinned project roles for explicit Codex prompts."""

import argparse
import hashlib
import json
import re
from pathlib import Path, PurePosixPath


ROOT = Path(__file__).resolve().parents[2]
AGENCY = ROOT / ".project" / "agency"
STAGES = ("BOOT", "E01", "E02", "E03", "E04", "E05", "E06", "E07")


def registry():
    data = json.loads((AGENCY / "sources.lock.json").read_text())
    if data["schema_version"] != 1:
        raise ValueError("Unsupported role registry version")
    if not re.fullmatch(r"[0-9a-f]{40}", data["upstream_commit"]):
        raise ValueError("The upstream commit must be an exact SHA")
    for name, source in data["sources"].items():
        if not re.fullmatch(r"[0-9a-f]{64}", source["sha256"]):
            raise ValueError(f"Invalid source digest: {name}")
    license_bytes = (AGENCY / data["license_file"]).read_bytes()
    if hashlib.sha256(license_bytes).hexdigest() != data["sources"]["LICENSE"]["sha256"]:
        raise ValueError("The upstream license differs from the pinned source")
    for role, entry in data["roles"].items():
        if entry["prompt"] != role + ".md" or not (AGENCY / entry["prompt"]).is_file():
            raise ValueError(f"Missing or invalid role prompt: {role}")
        if not entry["sources"] or any(s not in data["sources"] for s in entry["sources"]):
            raise ValueError(f"Missing source attribution: {role}")
    return data


def validate_handoff(role, handoff, data):
    if role not in data["roles"]:
        raise ValueError(f"Unknown role: {role}")
    required = ("task_id", "stage", "stage_limit", "authorization_ref", "run_id",
                "session_id", "base_commit", "spec_commit", "worktree", "read_only",
                "allowed_paths", "acceptance_criteria", "task")
    if any(key not in handoff for key in required):
        raise ValueError("The handoff is missing required fields")
    stage, limit = handoff["stage"], handoff["stage_limit"]
    if stage not in STAGES or limit not in STAGES or STAGES.index(stage) > STAGES.index(limit):
        raise ValueError("The task exceeds the authorized stage limit")
    if not handoff["task_id"].startswith(stage + "-") and handoff["task_id"] != "G." + stage:
        raise ValueError("The task ID does not match its stage")
    if handoff["authorization_ref"] == "D86" and limit != "BOOT":
        raise ValueError("D86 authorizes BOOT only")
    for key in ("base_commit", "spec_commit"):
        if not isinstance(handoff[key], str) or not re.fullmatch(r"[0-9a-f]{40}", handoff[key]):
            raise ValueError(f"{key} must be an exact commit SHA")
    for key in ("authorization_ref", "run_id", "session_id", "task"):
        if not isinstance(handoff[key], str) or not handoff[key].strip():
            raise ValueError(f"{key} must be a nonempty string")
    if type(handoff["read_only"]) is not bool:
        raise ValueError("read_only must be a boolean")
    if not isinstance(handoff["worktree"], str) or not Path(handoff["worktree"]).is_absolute():
        raise ValueError("worktree must be an absolute path")
    paths = handoff["allowed_paths"]
    if not isinstance(paths, list) or any(not isinstance(p, str) for p in paths):
        raise ValueError("allowed_paths must be a list of paths")
    if handoff["read_only"] and paths:
        raise ValueError("A read-only session cannot have writable paths")
    if not handoff["read_only"] and not paths:
        raise ValueError("A writer needs explicit allowed paths")
    for value in paths:
        path = PurePosixPath(value)
        if not value or value == "." or path.is_absolute() or ".." in path.parts or "\\" in value:
            raise ValueError(f"Unsafe writable path: {value}")
        if any(part in (".git", ".aws", "secrets") or part.startswith(".env") for part in path.parts):
            raise ValueError(f"Sensitive writable path: {value}")
    if role == "reviewer" and not handoff["read_only"]:
        raise ValueError("The reviewer must be read-only")
    criteria = handoff["acceptance_criteria"]
    if not isinstance(criteria, list) or not criteria or any(not isinstance(c, str) or not c.strip() for c in criteria):
        raise ValueError("Provide concrete acceptance criteria")


def render(role, handoff):
    data = registry()
    validate_handoff(role, handoff, data)
    parts = [
        ("project_rules", (ROOT / ".project" / "AGENTS.md").read_text()),
        ("shared_contract", (AGENCY / "common.md").read_text()),
        ("role", (AGENCY / data["roles"][role]["prompt"]).read_text()),
    ]
    revision_input = json.dumps({"registry": data, "instructions": parts}, sort_keys=True).encode()
    revision = hashlib.sha256(revision_input).hexdigest()
    text = f"Assigned role: {role}\nRole revision SHA-256: {revision}\n"
    text += "\n".join(f"\n<{name}>\n{body}\n</{name}>" for name, body in parts)
    text += "\n\n<task_handoff>\n" + json.dumps(handoff, indent=2) + "\n</task_handoff>\n"
    return text, revision


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("list")
    sub.add_parser("verify")
    prompt = sub.add_parser("render")
    prompt.add_argument("--role", required=True)
    prompt.add_argument("--handoff", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "list":
            print("\n".join(registry()["roles"]))
        elif args.command == "verify":
            print(json.dumps({"roles": len(registry()["roles"]), "status": "PASS"}))
        else:
            print(render(args.role, json.loads(args.handoff.read_text()))[0], end="")
    except (ValueError, KeyError, OSError, TypeError) as error:
        parser.exit(2, f"Role configuration error: {error}\n")


if __name__ == "__main__":
    main()
