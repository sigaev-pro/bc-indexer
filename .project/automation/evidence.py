"""Retain exact-commit acceptance proof before a coordinator attempts a merge."""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

import acceptance

ROOT = Path(__file__).resolve().parents[2]


def canonical_bytes(value):
    """Use one documented encoding for portable, reproducible evidence hashes."""
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True,
                       allow_nan=False) + "\n").encode("utf-8")


def digest(value):
    return hashlib.sha256(canonical_bytes(value)).hexdigest()


def sync_directory(path):
    descriptor = os.open(path, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def atomic_json(path, value, exclusive=False):
    """Replace a complete JSON record, then sync its directory before returning."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = None
    try:
        descriptor, temporary = tempfile.mkstemp(prefix=".pending-", dir=path.parent)
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(canonical_bytes(value))
            stream.flush()
            os.fsync(stream.fileno())
        if exclusive:
            os.link(temporary, path)
            Path(temporary).unlink()
        else:
            os.replace(temporary, path)
        temporary = None
        sync_directory(path.parent)
    finally:
        if temporary is not None:
            Path(temporary).unlink(missing_ok=True)


def validate_capture(bundle):
    """Check retained bytes and evaluator results; this does not authorize a merge."""
    acceptance.require(bundle["schema_version"] == 1 and bundle["kind"] == "premerge-capture",
                       "unsupported acceptance capture")
    snapshot = bundle["snapshot"]
    acceptance.require(digest(snapshot) == bundle["snapshot_sha256"], "snapshot digest mismatch")
    result = acceptance.evaluate(snapshot)
    acceptance.require(result == bundle["verdict"] and result["verdict"] == "PASS",
                       "retained acceptance proof does not pass")
    expected = bundle["expected"]
    actual = {"repository": snapshot["repository"], "task_id": snapshot["task_id"],
              "pull_request_number": snapshot["pull_request_number"],
              "review_comment_id": snapshot["review_comment_id"],
              "coordinator_login": snapshot["coordinator_login"],
              "implementer_session": snapshot["implementer_session"],
              "head_sha": result["head_sha"], "base_sha": result["base_sha"]}
    acceptance.require(actual == expected, "capture identity differs from its requested identity")
    return result


def capture(expected, output_directory, collector=None):
    """Collect and durably retain a PASS before returning its path to the caller."""
    acceptance.require(set(expected) == {"repository", "task_id", "pull_request_number",
                       "review_comment_id", "coordinator_login", "implementer_session",
                       "head_sha", "base_sha"}, "capture identity fields are incomplete")
    for key in ("head_sha", "base_sha"):
        acceptance.require(isinstance(expected[key], str)
                           and re.fullmatch(acceptance.SHA_PATTERN, expected[key]),
                           f"{key} must be an exact commit SHA")
    for key in ("pull_request_number", "review_comment_id"):
        acceptance.require(type(expected[key]) is int and expected[key] > 0,
                           f"{key} must be a positive integer")
    acceptance.require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", expected["repository"]),
                       "invalid repository")
    acceptance.require(re.fullmatch(acceptance.TASK_PATTERN, expected["task_id"]), "invalid task ID")
    acceptance.require(re.fullmatch(r"[A-Za-z0-9-]+", expected["coordinator_login"]),
                       "invalid coordinator login")
    acceptance.require(isinstance(expected["implementer_session"], str)
                       and expected["implementer_session"].strip(), "missing implementer session")
    read = collector or acceptance.collect
    snapshot = read(expected["repository"], expected["pull_request_number"], expected["task_id"],
                    expected["review_comment_id"], expected["coordinator_login"],
                    expected["implementer_session"])
    bundle = {"schema_version": 1, "kind": "premerge-capture", "expected": dict(expected),
              "captured_at": datetime.now(timezone.utc).isoformat(), "snapshot": snapshot,
              "snapshot_sha256": digest(snapshot), "verdict": acceptance.evaluate(snapshot)}
    validate_capture(bundle)
    # Content-addressed names preserve prior captures, including after a later rerun.
    path = Path(output_directory) / (expected["task_id"] + "-" + bundle["snapshot_sha256"] + ".json")
    acceptance.require(not path.is_symlink(), "capture path must not be a symbolic link")
    if path.exists():
        old = acceptance.parse_json(path.read_text())
        validate_capture(old)
        acceptance.require(old["snapshot"] == snapshot and old["expected"] == expected,
                           "existing capture conflicts with the requested proof")
    else:
        try:
            atomic_json(path, bundle, exclusive=True)
        except FileExistsError:
            # Another read-only capture may have retained the same snapshot first.
            saved = acceptance.parse_json(path.read_text())
            validate_capture(saved)
            acceptance.require(saved["snapshot"] == snapshot and saved["expected"] == expected,
                               "concurrent capture conflicts with the requested proof")
    # A readback catches serialization or storage failures before the caller can merge.
    saved = acceptance.parse_json(path.read_text())
    validate_capture(saved)
    sync_directory(path.parent)
    return {"path": str(path), "snapshot_sha256": saved["snapshot_sha256"],
            "bundle_sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
            "verdict": saved["verdict"], "merge_authorized": False}


def repository_root():
    result = subprocess.run(["git", "rev-parse", "--path-format=absolute", "--git-common-dir"],
                            cwd=ROOT, text=True, capture_output=True, check=True, timeout=30)
    common = Path(result.stdout.strip()).resolve()
    acceptance.require(common.name == ".git", "expected a non-bare repository with a common Git directory")
    return common.parent


def ignored_directory(path, root=None):
    """Reject tracked or unrelated output locations for this local capture CLI."""
    path, root = Path(path).resolve(), Path(root or repository_root()).resolve()
    acceptance.require(path.is_relative_to(root) and path != root,
                       "capture directory must be inside this repository")
    result = subprocess.run(["git", "check-ignore", "-q", "--no-index", "--",
                             str(path / "capture.json")], cwd=root, timeout=30)
    acceptance.require(result.returncode == 0, "capture directory must be ignored by Git")
    return path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--pr", type=int, required=True)
    parser.add_argument("--task", required=True)
    parser.add_argument("--review-comment-id", type=int, required=True)
    parser.add_argument("--coordinator-login", required=True)
    parser.add_argument("--implementer-session", required=True)
    parser.add_argument("--expected-head", required=True)
    parser.add_argument("--expected-base", required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    try:
        directory = ignored_directory(args.output_dir)
        expected = {"repository": args.repo, "task_id": args.task, "pull_request_number": args.pr,
                    "review_comment_id": args.review_comment_id, "coordinator_login": args.coordinator_login,
                    "implementer_session": args.implementer_session,
                    "head_sha": args.expected_head, "base_sha": args.expected_base}
        print(json.dumps(capture(expected, directory), indent=2))
        return 0
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print(json.dumps({"verdict": "BLOCKED", "error": str(error), "merge_authorized": False}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
