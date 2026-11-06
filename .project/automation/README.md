# Bootstrap checks and merge acceptance

Run `python3 .project/automation/ci.py` from the repository root. It checks only Git-tracked paths, so local caches, logs, and other worktrees do not change the result. It validates the specification mirrors, immutable original, Markdown links/tables, REQ/AC mapping, task graph, Python syntax, JSON, agency roles, and all tracked `test_*.py` unittest modules under `.project`. It also runs the component validator and its negative cases when BOOT-06 files are present. A tracked test module with no unittest cases is an error; adding a different test framework requires an explicit CI update.

These are bootstrap checks. Native execution, delivery, public-network tests, and the full product matrix remain NOT_RUN. Product stages must add their own required checks and evidence; `bootstrap/ci` alone cannot accept those stages.

## GitHub Actions

`.github/workflows/bootstrap.yml` runs on pull requests targeting `main` and pushes to `main`. The job name is the required check context `bootstrap/ci`. Pull requests check out the exact head SHA, not an implicit merge ref. The job checks that SHA again before running checks. The job has read-only contents permission, does not persist Git credentials, and uses exact action commit pins and Python 3.15.2. The hosted runner label is `ubuntu-24.04`; it is a managed runner image, not a reproducible native build image.

Action sources: [checkout v7.0.1](https://github.com/actions/checkout/tree/3d3c42e5aac5ba805825da76410c181273ba90b1), [setup-python v7.1.0](https://github.com/actions/setup-python/tree/6d4fd22b20465ccc0f7354476588e8df7c9ca0e0). Remote setup and Python availability must pass in a real Actions run before BOOT-05 is accepted. Local tests cannot establish that result.

## Branch protection

The coordinator applies the reviewed `branch-policy.json` to `main` with the GitHub branch-protection API. This file is the PUT request body:

```bash
gh api --method PUT repos/sigaev-pro/bc-indexer/branches/main/protection \
  --input .project/automation/branch-policy.json
gh api repos/sigaev-pro/bc-indexer/branches/main/protection
```

Both `bootstrap/ci` and `bootstrap/independent-review` are required. CI is bound to GitHub Actions app ID 15368. The independent-review commit status is published by the trusted coordinator account; it is not a second human approval. Protection requires the branch to be current, applies to administrators, forbids force pushes and deletion, and requires resolved review conversations. Required human approvals are zero because this repository uses separate agent sessions under one GitHub actor. Never impersonate another user or bypass a required human approval on a differently configured repository.

Before acceptance, verify the returned protection object and the real CI context names and publisher. An API error, an unavailable check, or incompatible existing policy is a blocker. The bootstrap evaluator rejects additional required contexts or human approval rules it cannot verify; extend the evaluator when product stages add their checks. Do not relax controls to make the merge pass.

## Independent review evidence

The implementer cannot publish their own approval. A separate read-only reviewer checks the exact head and base. The coordinator records that session's real report as a new comment on the intended pull request. Use this complete body shape, replacing every placeholder with actual evidence:

````text
<!-- extractor-review:v1 -->
```json
{
  "schema_version": 1,
  "repository": "sigaev-pro/bc-indexer",
  "task_id": "BOOT-05",
  "head_sha": "FULL_REVIEWED_HEAD_SHA",
  "base_sha": "FULL_CURRENT_MAIN_SHA",
  "implementer_session": "ACTUAL_IMPLEMENTER_SESSION_ID",
  "reviewer_session": "ACTUAL_SEPARATE_REVIEWER_SESSION_ID",
  "read_only": true,
  "verdict": "PASS",
  "findings": [],
  "checks": ["ACTUAL_CHECK_COMMAND_AND_RESULT"],
  "unverified": ["Native product runtime remains NOT_RUN."]
}
```
````

Publish the commit status on that exact head only after the independent report passes. Set `context` to `bootstrap/independent-review`, `state` to `success`, and `target_url` to the new PR comment URL returned by GitHub. Set `description` to `Review SHA256: ` followed by the SHA-256 of the exact UTF-8 comment body returned by GitHub. The evaluator checks this digest, so editing the comment invalidates the published status. Preserve previous reports; publish a new comment and status for a new review.

The collector binds the requested PR number and comment ID to GitHub's response. Both `/pull/NUMBER#issuecomment-ID` and `/issues/NUMBER#issuecomment-ID` URL forms must identify that exact PR and comment in the selected repository. The comment's API `issue_url` must also identify the intended PR. A report on another PR or task issue cannot provide this PR's review evidence.

The collector trusts the configured coordinator to identify the real sessions and publish the returned report. A worker-generated JSON object, a different session name chosen by the author, or an arbitrary success status is not independent evidence. The controller must retain its own session handoff and result records; text alone cannot authenticate a model session.

## Exact-commit gate

Add `<!-- extractor-task:BOOT-05 -->` to the PR body, using the assigned task ID. Immediately before merging, the coordinator runs:

```bash
python3 .project/automation/acceptance.py \
  --repo sigaev-pro/bc-indexer --pr PR_NUMBER --task BOOT-05 \
  --review-comment-id COMMENT_ID --coordinator-login sigaev-pro \
  --implementer-session ACTUAL_IMPLEMENTER_SESSION_ID
```

The command only reads GitHub. It fails closed unless all of these checks pass:

- The PR is open, ready, mergeable, in the selected repository, and assigned to the task.
- The current `main` commit is an ancestor of the exact reviewed head, established by GitHub compare data. The PR head and base still match after collection.
- The synthetic merge commit has exactly the current base and reviewed head as parents, and its tree equals the reviewed head tree. This makes head-only CI valid for the actual candidate tree. Any different merge tree needs fresh review and CI.
- The latest matching GitHub Actions check and its run both passed for this head, repository, and workflow. A pending rerun cannot borrow an earlier success.
- The trusted coordinator's separate-agent report covers this head and base. Its successful required status refers to that exact, unchanged report.
- The actual branch-protection response enforces the required controls.

An offline `--snapshot PATH` mode supports isolated tests and analysis of trusted collected snapshots. It does not authenticate arbitrary input and does not authorize a merge. The collector rejects oversized check sets instead of silently skipping pagination.

The coordinator must merge with GitHub's expected-head condition, respect strict branch protection, then verify the resulting commit and tree. A PASS is evidence for the captured state, not a reservation against later changes. A changed head requires new review and CI; a changed base requires updating the candidate and repeating the gate. The future controller owns serialization and records the verified merge result.
