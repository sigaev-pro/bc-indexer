"""Test durable pre-merge captures with isolated storage and fake GitHub reads."""

import copy
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import evidence
from test_acceptance import BASE, COMMENT_ID, HEAD, PR_NUMBER, REPOSITORY, proof


class CaptureTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name)
        self.expected = {"repository": REPOSITORY, "task_id": "BOOT-05",
                         "pull_request_number": PR_NUMBER, "review_comment_id": COMMENT_ID,
                         "coordinator_login": "sigaev-pro", "implementer_session": "worker-1",
                         "head_sha": HEAD, "base_sha": BASE}

    def collect(self, *arguments):
        self.assertEqual(arguments, (REPOSITORY, PR_NUMBER, "BOOT-05", COMMENT_ID,
                                    "sigaev-pro", "worker-1"))
        return proof()

    def test_retains_complete_proof_and_readback_hash(self):
        result = evidence.capture(self.expected, self.path, self.collect)
        bundle = json.loads(Path(result["path"]).read_text())
        self.assertEqual(bundle["snapshot"], proof())
        self.assertEqual(bundle["snapshot_sha256"], evidence.digest(proof()))
        self.assertEqual(evidence.validate_capture(bundle)["verdict"], "PASS")
        self.assertFalse(result["merge_authorized"])
        self.assertEqual(Path(result["path"]).stat().st_mode & 0o777, 0o600)

    def test_requested_identity_is_checked_not_adopted_from_the_collector(self):
        for key, value in (("head_sha", "f" * 40), ("base_sha", "f" * 40),
                           ("repository", "different/repository"), ("task_id", "BOOT-03"),
                           ("pull_request_number", 99), ("review_comment_id", 99),
                           ("implementer_session", "another-session"), ("coordinator_login", "other")):
            with self.subTest(key=key):
                expected = dict(self.expected, **{key: value})
                with self.assertRaises(ValueError):
                    evidence.capture(expected, self.path, lambda *args: proof())
                self.assertEqual(list(self.path.iterdir()), [])

    def test_failed_ci_or_review_never_creates_a_capture(self):
        for target in ("ci", "review"):
            candidate = proof()
            if target == "ci":
                candidate["ci_run"]["conclusion"] = "failure"
            else:
                candidate["review_comment"]["body"] += " edited"
            with self.assertRaises(ValueError):
                evidence.capture(self.expected, self.path, lambda *args: candidate)
            self.assertEqual(list(self.path.iterdir()), [])

    def test_invalid_request_never_calls_github(self):
        for key, value in (("head_sha", "HEAD"), ("base_sha", "main"),
                           ("pull_request_number", True), ("review_comment_id", 0),
                           ("task_id", "../../escape"), ("repository", "../../escape"),
                           ("coordinator_login", ""), ("implementer_session", "")):
            with self.subTest(key=key), patch.object(evidence.acceptance, "collect") as read:
                with self.assertRaises(ValueError):
                    evidence.capture(dict(self.expected, **{key: value}), self.path)
                read.assert_not_called()

    def test_retry_preserves_the_existing_capture_bytes(self):
        first = evidence.capture(self.expected, self.path, self.collect)
        original = Path(first["path"]).read_bytes()
        again = evidence.capture(self.expected, self.path, self.collect)
        self.assertEqual(first, again)
        self.assertEqual(Path(again["path"]).read_bytes(), original)

    def test_tampered_existing_proof_is_not_overwritten(self):
        result = evidence.capture(self.expected, self.path, self.collect)
        path = Path(result["path"])
        bundle = json.loads(path.read_text())
        bundle["snapshot"]["pull_request"]["head"]["sha"] = "e" * 40
        path.write_text(json.dumps(bundle))
        original = path.read_bytes()
        with self.assertRaises(ValueError):
            evidence.capture(self.expected, self.path, self.collect)
        self.assertEqual(path.read_bytes(), original)

    def test_crash_before_replace_preserves_the_previous_record(self):
        path = self.path / "record.json"
        evidence.atomic_json(path, {"previous": True})
        with patch.object(evidence.os, "replace", side_effect=OSError("injected crash")):
            with self.assertRaises(OSError):
                evidence.atomic_json(path, {"new": True})
        self.assertEqual(json.loads(path.read_text()), {"previous": True})
        self.assertEqual(list(self.path.iterdir()), [path])

    def test_file_and_directory_are_synced_before_success(self):
        sync = os.fsync
        with patch.object(evidence.os, "fsync", wraps=sync) as call:
            evidence.capture(self.expected, self.path, self.collect)
        self.assertGreaterEqual(call.call_count, 2)

    def test_sync_failure_is_not_reported_as_success(self):
        with patch.object(evidence.os, "fsync", side_effect=OSError("storage failure")):
            with self.assertRaises(OSError):
                evidence.capture(self.expected, self.path, self.collect)
        self.assertEqual(list(self.path.iterdir()), [])

    def test_new_directory_entries_are_synced_before_descending(self):
        first, second = self.path / "runs", self.path / "runs" / "acceptance"
        calls = []
        real_sync = evidence.sync_directory

        def observe(path):
            path = Path(path)
            if path == first and first not in calls:
                self.assertFalse(second.exists())
            calls.append(path)
            real_sync(path)

        with patch.object(evidence, "sync_directory", side_effect=observe):
            evidence.durable_directory(second)
        self.assertEqual(calls, [self.path, self.path.parent, first, self.path,
                                 second, first])

    def test_directory_sync_failure_preserves_work_and_retry_syncs_its_entry(self):
        first, second = self.path / "runs", self.path / "runs" / "acceptance"
        real_sync = evidence.sync_directory

        def fail_parent_after_creation(path):
            if Path(path) == self.path and first.exists():
                raise OSError("injected parent sync failure")
            real_sync(path)

        with patch.object(evidence, "sync_directory", side_effect=fail_parent_after_creation):
            with self.assertRaises(OSError):
                evidence.atomic_json(second / "proof.json", {"proof": True})
        self.assertTrue(first.is_dir())
        self.assertFalse(second.exists())
        with patch.object(evidence, "sync_directory", wraps=real_sync) as sync:
            evidence.atomic_json(second / "proof.json", {"proof": True})
        self.assertEqual([Path(call.args[0]) for call in sync.call_args_list[:4]],
                         [first, self.path, second, first])
        self.assertEqual(json.loads((second / "proof.json").read_text()), {"proof": True})

    def test_capture_verdict_and_identity_are_bound_to_the_snapshot(self):
        result = evidence.capture(self.expected, self.path, self.collect)
        original = json.loads(Path(result["path"]).read_text())
        for field in ("verdict", "expected"):
            candidate = copy.deepcopy(original)
            candidate[field]["head_sha"] = "e" * 40
            with self.assertRaises(ValueError):
                evidence.validate_capture(candidate)

    def test_output_must_be_inside_repository_and_ignored(self):
        with self.assertRaises(ValueError):
            evidence.ignored_directory(self.path.parent, self.path)
        with patch.object(evidence.subprocess, "run") as run:
            run.return_value.returncode = 1
            with self.assertRaises(ValueError):
                evidence.ignored_directory(self.path / "tracked", self.path)
            run.return_value.returncode = 0
            self.assertEqual(evidence.ignored_directory(self.path / "ignored", self.path),
                             (self.path / "ignored").resolve())


if __name__ == "__main__":
    unittest.main()
