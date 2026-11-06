"""Reject forged, stale, or reconstructed acceptance receipts."""

import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import acceptance
import acceptance_receipts
from acceptance_receipts import Ledger
from controller import Blocked
import evidence
from test_acceptance import BASE, COMMENT_ID, HEAD, MERGE, PR_NUMBER, REPOSITORY, proof


class ReceiptTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name)
        self.snapshot = proof()
        expected = {"repository": REPOSITORY, "task_id": "BOOT-05", "pull_request_number": PR_NUMBER,
                    "review_comment_id": COMMENT_ID, "coordinator_login": "sigaev-pro", "implementer_session": "worker-1",
                    "head_sha": HEAD, "base_sha": BASE}
        captured = evidence.capture(expected, self.path / "capture", lambda *args: copy.deepcopy(self.snapshot))
        self.bundle = json.loads(Path(captured["path"]).read_text())
        pull = copy.deepcopy(self.snapshot["pull_request"])
        pull.update(merged=True, state="closed")
        check = self.snapshot["check_runs"]["check_runs"][0]
        self.rows = {f"issues/comments/{COMMENT_ID}": self.snapshot["review_comment"],
                     f"actions/runs/{self.snapshot['ci_run']['id']}": self.snapshot["ci_run"],
                     f"check-runs/{check['id']}": check,
                     f"commits/{HEAD}/statuses?per_page=100": self.snapshot["commit_statuses"],
                     f"pulls/{PR_NUMBER}": pull,
                     f"git/commits/{HEAD}": self.snapshot["head_commit"],
                     f"git/commits/{MERGE}": self.snapshot["merge_commit"],
                     "git/ref/heads/main": {"object": {"sha": MERGE}},
                     f"compare/{MERGE}...{MERGE}": {"status": "identical", "merge_base_commit": {"sha": MERGE}}}
        self.ledger = Ledger(self.path / "accepted", lambda path: copy.deepcopy(self.rows[path]))

    def test_import_authenticates_public_records_and_preserves_full_snapshot(self):
        receipt = self.ledger.from_capture(self.bundle, {"source_commit": "d" * 40})
        saved = self.ledger.save(receipt)
        self.assertEqual(saved["capture"]["snapshot"], self.snapshot)
        self.assertEqual(self.ledger.load("BOOT-05")["merge_sha"], MERGE)

    def test_changed_public_review_cannot_authenticate_a_local_snapshot(self):
        self.rows[f"issues/comments/{COMMENT_ID}"] = dict(self.snapshot["review_comment"], body="forged")
        with self.assertRaises(Blocked):
            self.ledger.from_capture(self.bundle, {})

    def test_remote_failed_ci_cannot_authenticate_a_saved_success(self):
        self.rows[f"actions/runs/{self.snapshot['ci_run']['id']}"] = dict(self.snapshot["ci_run"], conclusion="failure")
        with self.assertRaises(Blocked):
            self.ledger.from_capture(self.bundle, {})

    def test_actual_merge_must_use_captured_parents_and_tree(self):
        self.rows[f"git/commits/{MERGE}"] = dict(self.snapshot["merge_commit"], parents=[{"sha": HEAD}, {"sha": "f" * 40}])
        with self.assertRaises(Blocked):
            self.ledger.from_capture(self.bundle, {})

    def test_unmerged_capture_is_not_accepted(self):
        self.rows[f"pulls/{PR_NUMBER}"]["merged"] = False
        with self.assertRaises(Blocked) as caught:
            self.ledger.from_capture(self.bundle, {})
        self.assertEqual(caught.exception.code, "BLOCKED_NOT_MERGED")

    def test_receipt_tampering_and_loss_of_main_ancestry_are_rejected(self):
        self.ledger.save(self.ledger.from_capture(self.bundle, {}))
        path = self.path / "accepted/BOOT-05.json"
        record = json.loads(path.read_text())
        record["receipt"]["merge_sha"] = "f" * 40
        path.write_text(json.dumps(record))
        with self.assertRaises(Blocked):
            self.ledger.load("BOOT-05")
        self.rows[f"compare/{MERGE}...{MERGE}"]["status"] = "diverged"
        with self.assertRaises(Blocked):
            self.ledger.from_capture(self.bundle, {})

    def test_historical_exception_is_explicit_and_does_not_reconstruct_snapshot(self):
        body = "Pinned coordinator acceptance for a historical bootstrap epoch."
        comment = {"id": 9, "user": {"login": "sigaev-pro"}, "body": body,
                   "issue_url": f"https://api.github.com/repos/{REPOSITORY}/issues/1"}
        pin = {"issue": 1, "head": HEAD, "merge": MERGE, "pr": PR_NUMBER,
               "comments": [(9, 1, hashlib.sha256(body.encode()).hexdigest())]}
        self.rows["issues/comments/9"] = comment
        self.rows["issues/1"] = {"number": 1, "state": "closed", "body": "<!-- extractor-task:BOOT-01 -->"}
        with patch.dict(acceptance_receipts.HISTORICAL, {"BOOT-01": pin}):
            result = self.ledger.historical("BOOT-01")
            self.assertEqual(result["kind"], "historical-bootstrap-import")
            self.assertNotIn("capture", result)
            self.assertIn("No original pre-merge snapshot", result["limitations"])
            self.rows["issues/comments/9"]["body"] = "edited evidence"
            with self.assertRaises(Blocked):
                self.ledger.historical("BOOT-01")
        with self.assertRaises(Blocked):
            self.ledger.historical("BOOT-06")

    def test_retained_acceptance_does_not_depend_on_expiring_ci_logs(self):
        receipt = self.ledger.save(self.ledger.from_capture(self.bundle, {}))
        del self.rows[f"actions/runs/{self.snapshot['ci_run']['id']}"]
        self.assertEqual(self.ledger.load("BOOT-05"), receipt)


if __name__ == "__main__":
    unittest.main()
