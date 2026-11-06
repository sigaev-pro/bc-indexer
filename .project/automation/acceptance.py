"""Read GitHub proof and reject missing, failed, or stale bootstrap acceptance."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess

CI_CONTEXT = "bootstrap/ci"
REVIEW_CONTEXT = "bootstrap/independent-review"
ACTIONS_APP_ID = 15368
WORKFLOW_PATH = ".github/workflows/bootstrap.yml"
REVIEW_MARKER = "<!-- extractor-review:v1 -->"
SHA_PATTERN = r"[0-9a-f]{40}"
TASK_PATTERN = r"(?:BOOT-\d{2}|E0[1-7]-\d+\.[a-z0-9_]+|G\.E0[1-7])"
STATUS_FIELDS = ("id", "url", "context", "state", "target_url", "description", "created_at", "updated_at")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f"duplicate JSON field: {key}")
        result[key] = value
    return result


def parse_json(text):
    return json.loads(text, object_pairs_hook=unique_object)


def check_protection(protection):
    checks = protection["required_status_checks"]
    require(checks["strict"] is True, "branch checks must require an up-to-date head")
    required = {row["context"]: row["app_id"] for row in checks["checks"]}
    require(set(required) == {CI_CONTEXT, REVIEW_CONTEXT} and len(checks["checks"]) == 2,
            "branch has missing or unsupported additional required checks")
    require(required.get(CI_CONTEXT) == ACTIONS_APP_ID, "CI must be required from GitHub Actions")
    require(REVIEW_CONTEXT in required and required[REVIEW_CONTEXT] in {-1, None}, "independent review context is not required")
    require(protection["enforce_admins"]["enabled"] is True, "admin bypass is enabled")
    require(protection["allow_force_pushes"]["enabled"] is False, "force pushes are allowed")
    require(protection["allow_deletions"]["enabled"] is False, "branch deletion is allowed")
    reviews = protection["required_pull_request_reviews"]
    require(reviews["dismiss_stale_reviews"] is True, "stale human reviews are retained")
    require(reviews["required_approving_review_count"] == 0, "configured human review requirement is not supported by this workflow")
    require(not reviews.get("require_code_owner_reviews", False) and not reviews.get("require_last_push_approval", False),
            "additional human review controls need a supported approval workflow")
    require(protection["required_conversation_resolution"]["enabled"] is True, "unresolved review threads do not block merging")


def review_status(combined, listed, repository, head):
    """Join the current combined status to its publisher-bearing status-list row."""
    require(combined["sha"] == head, "status response belongs to another commit")
    require(isinstance(listed, list) and len(listed) < 100,
            "status-list response reached the pagination limit")
    require(combined["total_count"] < 100 and len(combined["statuses"]) < 100,
            "combined-status response reached the pagination limit")
    current = [row for row in combined["statuses"] if row["context"] == REVIEW_CONTEXT]
    require(len(current) == 1, "independent review status is missing or ambiguous")
    matching = [row for row in listed if row["context"] == REVIEW_CONTEXT]
    require(matching, "independent review publisher proof is missing")
    require(all(type(row["id"]) is int and row["id"] > 0 for row in matching), "invalid review status ID")
    require(len({row["id"] for row in matching}) == len(matching), "duplicate review status IDs")
    latest = max(matching, key=lambda row: row["id"])
    require(latest["url"] == f"https://api.github.com/repos/{repository}/statuses/{head}",
            "review publisher proof belongs to another repository or commit")
    require(all(current[0][field] == latest[field] for field in STATUS_FIELDS),
            "combined and latest status-list identities differ")
    creator = latest.get("creator")
    require(isinstance(creator, dict) and isinstance(creator.get("login"), str) and creator["login"].strip(),
            "status-list publisher identity is missing")
    return latest


def evaluate(snapshot):
    """Evaluate a trusted collector snapshot; worker JSON is not a trusted source."""
    head = None
    try:
        require(snapshot["schema_version"] == 1, "unsupported snapshot schema")
        repository = snapshot["repository"]
        require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository), "invalid repository")
        task = snapshot["task_id"]
        require(re.fullmatch(TASK_PATTERN, task), "invalid task ID")
        coordinator = snapshot["coordinator_login"]
        implementer = snapshot["implementer_session"]
        require(isinstance(implementer, str) and implementer.strip(), "missing implementer session")
        pull = snapshot["pull_request"]
        number = snapshot["pull_request_number"]
        require(type(number) is int and number > 0 and pull["number"] == number, "pull request number mismatch")
        head, base = pull["head"]["sha"], pull["base"]["sha"]
        require(re.fullmatch(SHA_PATTERN, head) and re.fullmatch(SHA_PATTERN, base), "invalid commit SHA")
        require(pull["state"] == "open" and pull["draft"] is False, "pull request is not open and ready")
        require(pull["base"]["ref"] == "main", "pull request targets a different branch")
        require(pull["head"]["repo"]["full_name"] == repository == pull["base"]["repo"]["full_name"], "pull request repository mismatch")
        require(f"<!-- extractor-task:{task} -->" in pull["body"], "pull request lacks the assigned task marker")
        require(pull["mergeable"] is True, "mergeability is unknown or conflicts remain")
        require(snapshot["base_ref"]["object"]["sha"] == base, "base branch changed during proof collection")
        comparison = snapshot["comparison"]
        require(comparison["status"] == "ahead" and comparison["merge_base_commit"]["sha"] == base,
                "candidate is not based on the current main commit")
        head_commit, merge_commit = snapshot["head_commit"], snapshot["merge_commit"]
        require(head_commit["sha"] == head and merge_commit["sha"] == pull["merge_commit_sha"], "merge candidate changed")
        require({parent["sha"] for parent in merge_commit["parents"]} == {base, head}, "merge candidate has stale parents")
        require(head_commit["tree"]["sha"] == merge_commit["tree"]["sha"], "merge candidate differs from the reviewed tree")
        check_protection(snapshot["protection"])

        candidates = [row for row in snapshot["check_runs"]["check_runs"]
                      if row["name"] == CI_CONTEXT and row["app"]["id"] == ACTIONS_APP_ID]
        require(candidates, "required CI check is missing")
        check = max(candidates, key=lambda row: row["id"])
        require(check["head_sha"] == head, "CI belongs to another commit")
        require(check["status"] == "completed" and check["conclusion"] == "success", "CI has not passed")
        require(check["app"]["slug"] == "github-actions", "unexpected CI publisher")
        match = re.fullmatch(r"https://github\.com/" + re.escape(repository) + r"/actions/runs/(\d+)(?:/job/\d+)?", check["details_url"])
        require(match is not None, "CI has no valid GitHub Actions run URL")
        run = snapshot["ci_run"]
        require(run["id"] == int(match.group(1)), "CI run does not match the check")
        require(run["head_sha"] == head and run["repository"]["full_name"] == repository, "CI run belongs to another commit or repository")
        require(run["path"].split("@", 1)[0] == WORKFLOW_PATH, "CI came from a different workflow")
        require(run["event"] in {"pull_request", "push"}, "unexpected CI trigger")
        require(run["status"] == "completed" and run["conclusion"] == "success", "CI run has not passed")

        comment = snapshot["review_comment"]
        comment_id = snapshot["review_comment_id"]
        require(type(comment_id) is int and comment_id > 0 and comment["id"] == comment_id, "review comment ID mismatch")
        require(comment["user"]["login"] == coordinator, "review evidence was not recorded by the trusted coordinator")
        require(comment["issue_url"] == f"https://api.github.com/repos/{repository}/issues/{number}",
                "review comment does not belong to the intended pull request")
        require(re.fullmatch(r"https://github\.com/" + re.escape(repository)
                             + rf"/(?:pull|issues)/{number}#issuecomment-{comment_id}", comment["html_url"]),
                "review comment URL does not match the repository, pull request, and comment ID")
        match = re.fullmatch(re.escape(REVIEW_MARKER) + r"\s*```json\n(.*)\n```", comment["body"].strip(), re.S)
        require(match is not None, "review comment has no structured independent report")
        review = parse_json(match.group(1))
        require(review["schema_version"] == 1 and review["repository"] == repository and review["task_id"] == task, "review task or repository mismatch")
        require(review["head_sha"] == head and review["base_sha"] == base, "review is stale")
        require(review["implementer_session"] == implementer, "review identifies a different implementer")
        require(isinstance(review["reviewer_session"], str) and review["reviewer_session"].strip()
                and review["reviewer_session"] != implementer, "implementer cannot review their own work")
        require(review["read_only"] is True, "reviewer was not read-only")
        require(review["verdict"] == "PASS" and review["findings"] == [], "independent review has not passed")
        require(isinstance(review["checks"], list) and review["checks"] and all(isinstance(item, str) and item.strip() for item in review["checks"]), "review lists no verified checks")
        require(isinstance(review["unverified"], list), "review must state unverified work")
        status = review_status(snapshot["combined_status"], snapshot["commit_statuses"], repository, head)
        require(status["state"] == "success", "independent review status has not passed")
        require(status["creator"]["login"] == coordinator, "unexpected independent review status publisher")
        require(status["target_url"] == comment["html_url"], "review status points to different evidence")
        digest = hashlib.sha256(comment["body"].encode()).hexdigest()
        require(status["description"] == f"Review SHA256: {digest}", "review evidence changed after its status was published")
        extra = {}
        if not task.startswith("BOOT-"):
            import evidence_task_verifier as artifact_verifier
            import task_proofs
            require(task == artifact_verifier.TASK, "this product task has no acceptance adapter")
            proof = task_proofs.check_verification(snapshot["task_verification"], head, base)
            require(review.get("controller_verification") == task_proofs.publication(proof),
                    "task proof differs from the coordinator's verification attestation")
            ancestry = snapshot["verifier_source_ancestry"]
            require(ancestry["status"] in {"identical", "ahead"}
                    and ancestry["merge_base_commit"]["sha"] == proof["verifier_source_commit"],
                    "trusted verifier source was not accepted before the candidate base")
            extra = {"scope": "artifact-conformance", "runtime": "NOT_RUN", "task_proof_sha256": task_proofs.publication(proof)["proof_sha256"]}
        return {"verdict": "PASS", "task_id": task, "head_sha": head, "base_sha": base, **extra,
                "ci_run_id": run["id"], "review_url": comment["html_url"],
                "review_status_id": status["id"], "errors": []}
    except (ValueError, KeyError, TypeError, AttributeError) as error:
        return {"verdict": "BLOCKED", "head_sha": head, "errors": [str(error)]}


def github(repository, path):
    result = subprocess.run(["gh", "api", f"repos/{repository}/{path}"],
                            capture_output=True, text=True, timeout=30)
    require(result.returncode == 0, f"GitHub read failed for {path} (exit {result.returncode})")
    return parse_json(result.stdout)


def collect(repository, number, task, comment_id, coordinator, implementer, task_verification=None):
    """Collect fresh remote facts without writing a status, review, or merge."""
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository), "invalid repository")
    require(re.fullmatch(TASK_PATTERN, task), "invalid task ID")
    require(number > 0 and comment_id > 0, "pull request and comment IDs must be positive")
    if not task.startswith("BOOT-"):
        import evidence_task_verifier as artifact_verifier
        require(task == artifact_verifier.TASK and task_verification is not None,
                "product acceptance requires the configured exact-task verification proof")
    pull = github(repository, f"pulls/{number}")
    head, base = pull["head"]["sha"], pull["base"]["sha"]
    checks = github(repository, f"commits/{head}/check-runs?filter=latest&per_page=100")
    require(checks["total_count"] <= 100, "too many CI checks for this bounded collector")
    candidates = [row for row in checks["check_runs"]
                  if row["name"] == CI_CONTEXT and row["app"]["id"] == ACTIONS_APP_ID]
    run = {}
    if candidates:
        check = max(candidates, key=lambda row: row["id"])
        match = re.fullmatch(r"https://github\.com/" + re.escape(repository) + r"/actions/runs/(\d+)(?:/job/\d+)?", check["details_url"])
        if match:
            run = github(repository, "actions/runs/" + match.group(1))
    snapshot = {
        "schema_version": 1, "repository": repository, "task_id": task,
        "coordinator_login": coordinator, "implementer_session": implementer,
        "pull_request_number": number, "review_comment_id": comment_id,
        "pull_request": pull, "check_runs": checks, "ci_run": run,
        "combined_status": github(repository, f"commits/{head}/status?per_page=100"),
        "commit_statuses": github(repository, f"commits/{head}/statuses?per_page=100"),
        "review_comment": github(repository, f"issues/comments/{comment_id}"),
        "comparison": github(repository, f"compare/{base}...{head}"),
        "head_commit": github(repository, f"git/commits/{head}"),
        "merge_commit": github(repository, f"git/commits/{pull['merge_commit_sha']}"),
        "protection": github(repository, "branches/main/protection"),
        "base_ref": github(repository, "git/ref/heads/main"),
    }
    if not task.startswith("BOOT-"):
        import task_proofs
        task_proofs.check_verification(task_verification, head, base)
        snapshot["task_verification"] = task_verification
        snapshot["verifier_source_ancestry"] = github(repository, f"compare/{task_verification['verifier_source_commit']}...{base}")
    initial_status = review_status(snapshot["combined_status"], snapshot["commit_statuses"], repository, head)
    fresh_combined = github(repository, f"commits/{head}/status?per_page=100")
    fresh_list = github(repository, f"commits/{head}/statuses?per_page=100")
    fresh_status = review_status(fresh_combined, fresh_list, repository, head)
    require(all(initial_status[field] == fresh_status[field] for field in STATUS_FIELDS)
            and initial_status["creator"]["login"] == fresh_status["creator"]["login"],
            "review status changed during proof collection")
    # A later head must never borrow the proof collected for the earlier head.
    fresh_pull = github(repository, f"pulls/{number}")
    require(fresh_pull["head"]["sha"] == head and fresh_pull["base"]["sha"] == base,
            "pull request changed during proof collection")
    require(fresh_pull["state"] == "open" and fresh_pull["draft"] is False, "pull request state changed during proof collection")
    return snapshot


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot", type=Path, help="Evaluate a saved trusted snapshot offline.")
    parser.add_argument("--repo", default="sigaev-pro/bc-indexer")
    parser.add_argument("--pr", type=int)
    parser.add_argument("--task")
    parser.add_argument("--review-comment-id", type=int)
    parser.add_argument("--coordinator-login", default="sigaev-pro")
    parser.add_argument("--implementer-session")
    parser.add_argument("--task-proof", type=Path, help="Retained controller verification proof required for the configured artifact task.")
    args = parser.parse_args()
    try:
        if args.snapshot:
            snapshot = parse_json(args.snapshot.read_text())
        else:
            require(all([args.pr, args.task, args.review_comment_id, args.implementer_session]),
                    "live checks require --pr, --task, --review-comment-id, and --implementer-session")
            snapshot = collect(args.repo, args.pr, args.task, args.review_comment_id,
                               args.coordinator_login, args.implementer_session,
                               parse_json(args.task_proof.read_text()) if args.task_proof else None)
        report = evaluate(snapshot)
    except (OSError, ValueError, KeyError, TypeError, subprocess.TimeoutExpired) as error:
        report = {"verdict": "BLOCKED", "errors": [str(error)]}
    print(json.dumps(report, indent=2))
    return 0 if report["verdict"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
