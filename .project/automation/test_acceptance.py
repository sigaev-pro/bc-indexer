"""Exercise rejected acceptance proofs without GitHub writes or network access."""

import copy
import hashlib
import json
import unittest
from unittest.mock import patch

import acceptance

HEAD, BASE, MERGE, TREE = (character * 40 for character in "abcd")
REPOSITORY = "sigaev-pro/bc-indexer"
PR_NUMBER = 87
COMMENT_ID = 6693432825
COMMENT_URL = f"https://github.com/{REPOSITORY}/pull/{PR_NUMBER}#issuecomment-{COMMENT_ID}"


def proof():
    report = {
        "schema_version": 1, "repository": REPOSITORY, "task_id": "BOOT-05",
        "head_sha": HEAD, "base_sha": BASE, "implementer_session": "worker-1",
        "reviewer_session": "reviewer-2", "read_only": True, "verdict": "PASS",
        "findings": [], "checks": ["Ran the required offline checks."],
        "unverified": ["Native product runtime remains NOT_RUN."],
    }
    snapshot = {
        "schema_version": 1, "repository": REPOSITORY, "task_id": "BOOT-05",
        "coordinator_login": "sigaev-pro", "implementer_session": "worker-1",
        "pull_request_number": PR_NUMBER, "review_comment_id": COMMENT_ID,
        "pull_request": {
            "number": PR_NUMBER,
            "state": "open", "draft": False, "mergeable": True, "merge_commit_sha": MERGE,
            "body": "<!-- extractor-task:BOOT-05 -->",
            "head": {"sha": HEAD, "repo": {"full_name": REPOSITORY}},
            "base": {"sha": BASE, "ref": "main", "repo": {"full_name": REPOSITORY}},
        },
        "base_ref": {"object": {"sha": BASE}},
        "comparison": {"status": "ahead", "merge_base_commit": {"sha": BASE}},
        "head_commit": {"sha": HEAD, "tree": {"sha": TREE}},
        "merge_commit": {"sha": MERGE, "tree": {"sha": TREE}, "parents": [{"sha": BASE}, {"sha": HEAD}]},
        "protection": {
            "required_status_checks": {"strict": True, "checks": [
                {"context": acceptance.CI_CONTEXT, "app_id": 15368},
                {"context": acceptance.REVIEW_CONTEXT, "app_id": -1},
            ]},
            "enforce_admins": {"enabled": True}, "allow_force_pushes": {"enabled": False},
            "allow_deletions": {"enabled": False}, "required_conversation_resolution": {"enabled": True},
            "required_pull_request_reviews": {"dismiss_stale_reviews": True, "required_approving_review_count": 0},
        },
        "check_runs": {"total_count": 1, "check_runs": [{
            "id": 11, "name": acceptance.CI_CONTEXT, "head_sha": HEAD,
            "status": "completed", "conclusion": "success",
            "app": {"id": 15368, "slug": "github-actions"},
            "details_url": f"https://github.com/{REPOSITORY}/actions/runs/10/job/11",
        }]},
        "ci_run": {
            "id": 10, "head_sha": HEAD, "repository": {"full_name": REPOSITORY},
            "path": acceptance.WORKFLOW_PATH, "event": "pull_request",
            "status": "completed", "conclusion": "success",
        },
        "review_comment": {"id": COMMENT_ID, "user": {"login": "sigaev-pro"}, "html_url": COMMENT_URL,
                           "issue_url": f"https://api.github.com/repos/{REPOSITORY}/issues/{PR_NUMBER}",
                           "body": acceptance.REVIEW_MARKER + "\n```json\n" + json.dumps(report) + "\n```"},
        "combined_status": {"sha": HEAD, "statuses": [{
            "context": acceptance.REVIEW_CONTEXT, "state": "success",
            "creator": {"login": "sigaev-pro"}, "target_url": COMMENT_URL,
        }]},
    }
    body = snapshot["review_comment"]["body"]
    snapshot["combined_status"]["statuses"][0]["description"] = "Review SHA256: " + hashlib.sha256(body.encode()).hexdigest()
    return snapshot


def change_review(snapshot, **changes):
    comment = snapshot["review_comment"]
    report = json.loads(comment["body"].split("```json\n")[1].split("\n```")[0])
    report.update(changes)
    comment["body"] = acceptance.REVIEW_MARKER + "\n```json\n" + json.dumps(report) + "\n```"


class AcceptanceProofTests(unittest.TestCase):
    def assert_blocked(self, snapshot):
        result = acceptance.evaluate(snapshot)
        self.assertEqual(result["verdict"], "BLOCKED", result)
        self.assertTrue(result["errors"])

    def test_complete_independent_proof_accepts_exact_commit(self):
        result = acceptance.evaluate(proof())
        self.assertEqual(result["verdict"], "PASS", result)
        self.assertEqual(result["head_sha"], HEAD)

    def test_real_pull_comment_url_and_issue_alias_bind_the_same_pr(self):
        for route in ["pull", "issues"]:
            snapshot = proof()
            url = f"https://github.com/{REPOSITORY}/{route}/{PR_NUMBER}#issuecomment-{COMMENT_ID}"
            snapshot["review_comment"]["html_url"] = url
            snapshot["combined_status"]["statuses"][0]["target_url"] = url
            with self.subTest(route=route):
                result = acceptance.evaluate(snapshot)
                self.assertEqual(result["verdict"], "PASS", result)
                self.assertEqual(result["review_url"], url)

    def test_review_comment_cannot_belong_to_a_different_repo_pr_or_comment(self):
        for condition in ["html_repo", "html_pr", "html_comment", "api_repo", "api_pr",
                          "response_comment_id", "requested_comment_id", "response_pr_number"]:
            snapshot = proof()
            comment = snapshot["review_comment"]
            comment["html_url"] = COMMENT_URL.replace("/pull/", "/issues/")
            if condition == "html_repo":
                comment["html_url"] = comment["html_url"].replace(REPOSITORY, "sigaev-pro/another-repo")
            elif condition == "html_pr":
                comment["html_url"] = comment["html_url"].replace(f"/issues/{PR_NUMBER}#", f"/issues/{PR_NUMBER + 1}#")
            elif condition == "html_comment":
                comment["html_url"] = comment["html_url"].replace(str(COMMENT_ID), str(COMMENT_ID + 1))
            elif condition == "api_repo":
                comment["issue_url"] = comment["issue_url"].replace(REPOSITORY, "sigaev-pro/another-repo")
            elif condition == "api_pr":
                comment["issue_url"] = comment["issue_url"].replace(str(PR_NUMBER), str(PR_NUMBER + 1))
            elif condition == "response_comment_id":
                comment["id"] += 1
            elif condition == "requested_comment_id":
                snapshot["review_comment_id"] += 1
            else:
                snapshot["pull_request"]["number"] += 1
            snapshot["combined_status"]["statuses"][0]["target_url"] = comment["html_url"]
            with self.subTest(condition=condition):
                self.assert_blocked(snapshot)

    def test_github_null_app_binding_keeps_review_status_required(self):
        snapshot = proof()
        snapshot["protection"]["required_status_checks"]["checks"][1]["app_id"] = None
        self.assertEqual(acceptance.evaluate(snapshot)["verdict"], "PASS")

    def test_missing_failed_running_cancelled_or_stale_ci_is_rejected(self):
        for condition in ["missing", "failure", "in_progress", "cancelled", "stale"]:
            snapshot = proof()
            check = snapshot["check_runs"]["check_runs"][0]
            if condition == "missing":
                snapshot["check_runs"]["check_runs"] = []
            elif condition == "in_progress":
                check.update(status="in_progress", conclusion=None)
            elif condition == "stale":
                check["head_sha"] = BASE
            else:
                check["conclusion"] = condition
            with self.subTest(condition=condition):
                self.assert_blocked(snapshot)

    def test_newer_pending_ci_attempt_invalidates_older_success(self):
        snapshot = proof()
        newer = dict(snapshot["check_runs"]["check_runs"][0], id=12, status="queued", conclusion=None)
        snapshot["check_runs"]["check_runs"].append(newer)
        self.assert_blocked(snapshot)

    def test_unrelated_publisher_workflow_or_run_cannot_supply_ci(self):
        for condition in ["publisher", "workflow", "run", "run_head"]:
            snapshot = proof()
            if condition == "publisher":
                snapshot["check_runs"]["check_runs"][0]["app"]["id"] = 99
            elif condition == "workflow":
                snapshot["ci_run"]["path"] = ".github/workflows/unrelated.yml"
            elif condition == "run":
                snapshot["ci_run"]["id"] = 9
            else:
                snapshot["ci_run"]["head_sha"] = BASE
            with self.subTest(condition=condition):
                self.assert_blocked(snapshot)

    def test_missing_failed_or_stale_independent_review_is_rejected(self):
        for changes in [{"head_sha": BASE}, {"base_sha": HEAD}, {"verdict": "REQUEST_CHANGES"},
                        {"checks": []}, {"findings": ["Unresolved data loss."]}]:
            snapshot = proof()
            change_review(snapshot, **changes)
            with self.subTest(changes=changes):
                self.assert_blocked(snapshot)
        snapshot = proof()
        snapshot["review_comment"]["body"] = "Looks good."
        self.assert_blocked(snapshot)

    def test_author_review_and_a_writing_reviewer_are_rejected(self):
        for changes in [{"reviewer_session": "worker-1"}, {"read_only": False},
                        {"implementer_session": "unrelated-worker"}]:
            snapshot = proof()
            change_review(snapshot, **changes)
            with self.subTest(changes=changes):
                self.assert_blocked(snapshot)

    def test_missing_failed_or_misdirected_review_status_is_rejected(self):
        for condition in ["missing", "failure", "other_report", "other_actor", "stale"]:
            snapshot = proof()
            status = snapshot["combined_status"]["statuses"][0]
            if condition == "missing":
                snapshot["combined_status"]["statuses"] = []
            elif condition == "failure":
                status["state"] = "failure"
            elif condition == "other_report":
                status["target_url"] = COMMENT_URL + "0"
            elif condition == "other_actor":
                status["creator"]["login"] = "worker"
            else:
                snapshot["combined_status"]["sha"] = BASE
            with self.subTest(condition=condition):
                self.assert_blocked(snapshot)

    def test_changed_base_or_unreviewed_merge_tree_is_rejected(self):
        for condition in ["moved_base", "behind", "merge_tree", "merge_parent", "merge_sha"]:
            snapshot = proof()
            if condition == "moved_base":
                snapshot["base_ref"]["object"]["sha"] = HEAD
            elif condition == "behind":
                snapshot["comparison"]["status"] = "diverged"
            elif condition == "merge_tree":
                snapshot["merge_commit"]["tree"]["sha"] = BASE
            elif condition == "merge_parent":
                snapshot["merge_commit"]["parents"][0]["sha"] = TREE
            else:
                snapshot["merge_commit"]["sha"] = TREE
            with self.subTest(condition=condition):
                self.assert_blocked(snapshot)

    def test_missing_branch_controls_are_rejected(self):
        for condition in ["strict", "admin", "force", "delete", "missing_context", "extra_context"]:
            snapshot = proof()
            protection = snapshot["protection"]
            if condition == "strict":
                protection["required_status_checks"]["strict"] = False
            elif condition == "admin":
                protection["enforce_admins"]["enabled"] = False
            elif condition in {"force", "delete"}:
                protection["allow_force_pushes" if condition == "force" else "allow_deletions"]["enabled"] = True
            elif condition == "missing_context":
                protection["required_status_checks"]["checks"].pop()
            else:
                protection["required_status_checks"]["checks"].append({"context": "unverified-product-check", "app_id": 15368})
            with self.subTest(condition=condition):
                self.assert_blocked(snapshot)

    def test_editing_an_accepted_review_comment_invalidates_its_status(self):
        snapshot = proof()
        change_review(snapshot, unverified=[])
        self.assert_blocked(snapshot)

    def test_a_closed_or_draft_pull_request_cannot_be_accepted(self):
        for changes in [{"state": "closed"}, {"draft": True}, {"mergeable": None}]:
            snapshot = proof()
            snapshot["pull_request"].update(changes)
            self.assert_blocked(snapshot)

    def test_empty_worker_json_and_duplicate_fields_are_not_evidence(self):
        for value in [{}, [], None, {"exit_code": 0, "status": "DONE"}]:
            self.assert_blocked(value)
        with self.assertRaisesRegex(ValueError, "duplicate JSON"):
            acceptance.parse_json('{"verdict":"FAIL","verdict":"PASS"}')

    def test_live_collection_rejects_a_head_change_during_reads(self):
        snapshot = proof()
        pulls = 0

        def fake_github(repository, path):
            nonlocal pulls
            self.assertEqual(repository, REPOSITORY)
            if path == f"pulls/{PR_NUMBER}":
                pulls += 1
                result = copy.deepcopy(snapshot["pull_request"])
                if pulls == 2:
                    result["head"]["sha"] = TREE
                return result
            mapping = {
                f"commits/{HEAD}/check-runs?filter=latest&per_page=100": "check_runs",
                "actions/runs/10": "ci_run", f"commits/{HEAD}/status?per_page=100": "combined_status",
                f"issues/comments/{COMMENT_ID}": "review_comment", f"compare/{BASE}...{HEAD}": "comparison",
                f"git/commits/{HEAD}": "head_commit", f"git/commits/{MERGE}": "merge_commit",
                "branches/main/protection": "protection", "git/ref/heads/main": "base_ref",
            }
            return copy.deepcopy(snapshot[mapping[path]])

        with patch.object(acceptance, "github", side_effect=fake_github):
            with self.assertRaisesRegex(ValueError, "changed during proof collection"):
                acceptance.collect(REPOSITORY, PR_NUMBER, "BOOT-05", COMMENT_ID, "sigaev-pro", "worker-1")


if __name__ == "__main__":
    unittest.main()
