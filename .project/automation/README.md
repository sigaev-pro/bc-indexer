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

GitHub's combined-status endpoint does not include the status creator. The collector preserves that response and reads the status-list endpoint for publisher identity. The latest matching row must have the same ID, commit/repository URL, context, state, target URL, description digest, and timestamps as the current combined status. A publisher from another status cannot be substituted. Before returning, the collector reads both endpoints again and rejects a changed identity or a newer status. A full 100-row status page blocks collection because this bounded reader cannot prove that pagination is complete.

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

An offline `--snapshot PATH` mode supports isolated tests and analysis of trusted collected snapshots. It does not authenticate arbitrary input and does not authorize a merge. Snapshots retain separate `combined_status` and `commit_statuses` API responses. The collector rejects oversized check sets or full status pages instead of silently skipping pagination.

The coordinator must merge with GitHub's expected-head condition, respect strict branch protection, then verify the resulting commit and tree. A PASS is evidence for the captured state, not a reservation against later changes. A changed head requires new review and CI; a changed base requires updating the candidate and repeating the gate. The future controller owns serialization and records the verified merge result.

## Durable capture before merge

Use `evidence.py` to retain the full passing collector snapshot before a manual or controller-managed merge. Every identity below is required. The output directory must be Git-ignored and inside the canonical repository; an isolated worktree may use the canonical repository's ignored run directory.

```bash
python3 .project/automation/evidence.py \
  --repo sigaev-pro/bc-indexer --pr PR_NUMBER --task TASK_ID \
  --review-comment-id COMMENT_ID --coordinator-login sigaev-pro \
  --implementer-session ACTUAL_IMPLEMENTER_SESSION_ID \
  --expected-head FULL_HEAD_SHA --expected-base FULL_BASE_SHA \
  --output-dir /Users/vadim/projects/bonpay/extractor/.project/automation/runs/acceptance
```

The command only reads GitHub. It rejects a passing proof for a different requested head, base, PR, or task. Before reporting success, it atomically retains one private JSON bundle and syncs both file data and the containing directory. When output directories are new, it creates each level separately and syncs that directory and its parent entry before descending. A retry also syncs the nearest existing ancestor and its parent, covering an earlier interrupted directory-creation attempt. The bundle contains the complete snapshot, expected identity, evaluator verdict, capture time, and snapshot hash. Its content-addressed filename preserves earlier evidence. Output includes the bundle path and hash, but no environment or credentials.

The snapshot hash is SHA-256 of `json.dumps(snapshot, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False) + "\n"`, encoded as UTF-8. Retain the full bundle with the actual merge receipt; a digest or CI log URL alone cannot reconstruct it. Capturing a PASS does not reserve GitHub state or authorize a merge. Repeat the live gate if the head or base changes, and always use the expected-head merge condition and verify the resulting merge/tree.

For the configured `E01-01.evidence` artifact task, both the live acceptance CLI and capture CLI additionally require `--task-proof PATH`. The file is the retained controller verification object, including exact task/head/base, frozen recipe and verifier hashes, required output hashes, process requests/results, and logs. The collector reads the actual coordinator report and verifier-source ancestry; the evaluator requires a matching `controller_verification` attestation before it can return artifact-conformance PASS. A generic green CI/review snapshot cannot pass or create an accepted product capture. BOOT capture format and historical verdicts remain unchanged. Other product tasks have no configured acceptance adapter.

## Persistent controller

`controller.py` owns dependency scheduling and state transitions. `controller_backend.py` owns reconciled Git/GitHub operations and explicit role execution. `acceptance_receipts.py` verifies accepted dependencies. `session_runner.py` supervises one bounded process group. Tests exercise the same controller interface with a fake backend and run real disposable subprocesses to check ownership, stop, output bounds, and timeouts. They do not launch models or mutate GitHub.

The canonical repository directory comes from Git's common directory. Every worktree therefore uses the same `.project/automation/state/` lock, run records, task leases, and accepted receipt ledger. Run/session artifacts live under `.project/automation/runs/`. These paths are ignored by Git. Back up both directories together. A per-operation `evidence/accepted-receipt.json` is a portable manifest containing the full collector snapshot, actual merge facts, verification records, source provenance, and a receipt digest. Preserve it with the named command/session logs; a remote CI log URL alone is insufficient.

```bash
python3 .project/automation/controller.py verify-bootstrap --repo sigaev-pro/bc-indexer
python3 .project/automation/controller.py run --repo sigaev-pro/bc-indexer --authorization-ref D86 --stage-limit BOOT
python3 .project/automation/controller.py status
python3 .project/automation/controller.py stop --run-id RUN_ID
python3 .project/automation/controller.py resume --run-id RUN_ID
```

`verify-bootstrap` reconciles all 84 task markers, issue mappings, and dependencies, then requires accepted receipts for BOOT-01 through BOOT-08. It reports missing work and returns nonzero until those results exist. It does not require BOOT-09, start workers, or grant E01 authorization. `run` additionally requires accepted BOOT-09 and current CLI, GitHub, branch-policy, and runner preflight. Existing D86/BOOT runs end without product dispatch. D92 uses its own recorded run and fixed deadline; `initialize` performs the same start gate and retains a stopped run for explicit external-candidate adoption. There is no ungated bootstrap execution mode.

The reviewed authorization registry in `controller.py` is independent of CLI requests and mutable run state. D93 authorizes continuous ordered work through E07 in meaningful blocks; follow the [continuous-work procedure](continuous-work.md) to adopt it without resetting history. D86 retains BOOT, and historical D92 retains its original deadline and 200-invocation ceiling. Missing recipes produce `BLOCKED_UNCONFIGURED_VERIFICATION`. Accepted adapters cover `E01-01.evidence` and `E01-01.builds`; see their [evidence contract](evidence-task-contract.md) and [build contract](build-task-contract.md). The remaining E01 tasks and `G.E01` need separately reviewed adapters. Adapter readiness does not accept a product task, and `bootstrap/ci` alone cannot accept one. Product runtime remains NOT_RUN.

`verify-bootstrap` reports the initial artifact contract and trusted pack hashes separately from `product_verification_configured: false` and lists the unconfigured E01 tasks. That result establishes a bounded starting path, not unattended completion of all E01. The future worker supplies the real schemas and validator; BOOT-09 supplies only trusted oracle inputs, a bounded driver, and proof authentication. Its disposable protocol stubs are bootstrap tests, never completed product schema validation.

The driver keeps expected outcomes and vector IDs private to its own invocation and supplies ordinary validation bundles to the candidate CLI. Each candidate call has a five-second timeout and 64 KiB file-output limit; the complete finite pack runs under the existing 600-second supervisor and bounded logs. Successful artifact proof retains full driver logs. The coordinator publishes its proof digest in `controller_verification` after verification, separately from the earlier review. Receipt import/replay rereads that public binding, so changing local proof, capture, report, and hashes together cannot replace the authentic coordinator publication. Required task proof is checked before merge, not only afterward.

Coordinator plus independent QA review owns later recipes and adapters. Task owners may propose commands and evidence contracts. The run freezes its recipes and trusted verifier bytes, and resume rejects changed pins. A reviewed addition uses the explicit revision-checked `handoff`, preserving attempts, receipts, leases, and work. D93 adoption is a separate one-time authorization transition; ordinary resume or configuration handoff cannot change an authorization. Do not create a fresh run to discard history.

The importer has exactly four historical exceptions: BOOT-01 initial publication, BOOT-02 PR #85, BOOT-04 PR #86, and BOOT-05 PR #87. Each pins the candidate, accepted merge, coordinator comment identity, and exact comment body digest. It checks the public commit/tree and current main ancestry. BOOT-05 imports post-merge proof and the coordinator's live-gate attestation; it does not claim the original snapshot bytes exist. All later manual acceptances use the complete `evidence.py` capture. Place those captures in the canonical `runs/acceptance/` directory or select another directory with `--capture-dir`. The importer authenticates public review, status, CI, and actual merge facts before retaining a receipt. Once accepted, the full retained receipt does not depend on expiring CI logs.

The controller reserves an operation ID and saves its intent before execution. PRs, comments, statuses, merges, and issue closure reconcile the existing remote result before a retry. An uncertain API response remains pending. A new head or base invalidates the candidate's acceptance. Dirty files, conflicting branches, ambiguous PRs, incomplete evidence, failed checks, unavailable access, and unsupported policy are explicit blockers. The controller never force-pushes, resets, cleans, discards stores, relaxes branch protection, or treats a closed issue as evidence.

Worker and reviewer requests retain the exact task snapshot, dependency receipts, base/specification commits, role digest, session ID, path limits, schema, and process results. Codex resolves from the canonical repository's pinned 0.167.0 cache, including in isolated worktrees. The owner's configured model is preserved. Writers use the existing workspace-write sandbox, reviewers a separate read-only session, and both retain managed restrictions. The controller validates actual commits, changed paths, required tracked artifacts, independently executed verification commands, and exact-head review before publication. BOOT-09 uses a technical writer for its committed gate record and a separate reviewer for acceptance.

## Stop, recovery, and budgets

`status` reads atomic run records and prints the exact run ID, state, task blockers, attempt counts, invocation count, and deadline. `stop` writes a durable operator request; it does not mark product behavior as failed. The supervisor and parent watchdog stop owned groups with bounded graceful/forced cleanup and retain the outcome. Output is streamed through bounded pipes into private logs. The parent and supervisor use monotonic session deadlines so backward wall-clock changes cannot extend a session indefinitely.

The repository lock is inherited by supervisors and workers. A controller crash cannot allow a second writer while its supervised child still owns that lock. Cleanup keeps direct-child leaders unreaped until their owned groups have been handled. Recovery never signals a numeric PID from an old heartbeat. A stopped or hung supervisor triggers a bounded parent watchdog; its child ownership is inspected while the supervisor is stopped and cannot reap those child leaders. The supervisor remains stopped through final group termination, so it cannot spawn an unseen worker after that inspection. Denied or unverifiable cleanup is a blocker, not successful cleanup.

Resume uses the explicit run ID and reconciles current GitHub/Git evidence before pending operations. It preserves the saved authorization, task/recipe snapshots, attempts, and budget counters. An interrupted model session without a durable result remains `BLOCKED_SESSION_INTERRUPTED`: inspect its retained request, logs, process evidence, and worktree before preparing a reviewed recovery. Do not delete a lock file or edit state to steal ownership. A changed base/head or unresolved dirty work also requires a reviewed candidate recovery and fresh review/CI. There is no destructive automatic reset.

Services use `resume --automatic --run-id RUN_ID`. Automatic recovery continues only a previously RUNNING run and never clears an operator stop, resets a blocker, renews a budget, or grants a new session attempt. A stop marker takes effect even if the controller crashed before saving STOPPED. STOPPED and BUDGET_STOP remain stopped even without a marker. To continue intentionally, an operator uses plain `resume --run-id RUN_ID`; budget renewal still requires its separate flag.

Durable SIGTERM/SIGINT handlers are scoped around existing-run setup and execution. Ordinary command supervision forwards interruptions to that caller through an explicit callback while retaining its own process-group cleanup intent. Both handler layers restore the prior handlers on exit. Explicit resume acknowledges the prior stop at entry while signals are deferred; a newly arriving signal is delivered afterward and retains a fresh stop. Resume preflight participates in durable status persistence. Automatic recovery checks stop/status before constructing the backend, so it performs no preflight or task work for a stopped run.

When a stopped session has an intact terminal result and proven complete process-group cleanup, explicit resume reserves a new operation linked to the old one. The prior request, result, logs, failure, and hashes remain unchanged in session history. Cleanup is confirmed while the child leader remains unreaped. Missing results, missing cleanup proof, changed logs, or watchdog uncertainty block replacement. A new worker attempt and model invocation are charged before launch; an interrupted reservation is recovered without charging twice.

A failed model process or failed model turn requires explicit `resume --run-id RUN_ID --retry-session`. Only one diagnostic model-session retry is allowed per task, within the existing implementation and invocation limits. This flag cannot retry failed task verification or erase a runtime FAIL. Failed verification remains blocked for reviewed diagnosis under D83. Neither a diagnostic process result nor worker output accepts a task; the independent review and verification gates still apply.

After confirmed issue closure, the controller durably saves DONE before releasing the task lease. If a crash separates those writes, reconciliation verifies the accepted receipt and completes only the same run's matching lease generation. It does not repeat issue closure or release a lease acquired by another run.

Under D93, campaign time, invocation totals, and ordinary repair counts do not stop work. At most three model sessions may be active, including the coordinator; model sessions default to a finite 60-minute limit. The adopted run retains its saved session limit, and command, verification, cleanup, and resource limits remain finite. D86 and D92 keep their historical limits; D86 alone supports its original explicit budget renewal. D93 rejects `--renew-budget`. Normal resume retains all history. D83 remains separate: one diagnostic clean retry cannot erase a runtime FAIL or make a gate pass.

## Runner service wrapper

Use a dedicated runner for unattended restart after reboot. This implementation does not install or enable a machine-wide service. On the current macOS host, the process-scoped launcher below prevents idle sleep while preserving the owner's global power settings:

```bash
nohup caffeinate -i -s python3 .project/automation/controller.py resume --automatic --run-id RUN_ID \
  > .project/automation/runs/controller-launcher.log 2>&1 < /dev/null &
```

For a previously selected dedicated macOS runner, copy [controller.launchd.plist](controller.launchd.plist) to that runner account's LaunchAgents directory after replacing every placeholder with absolute paths and the recorded run ID. Validate it with `plutil -lint`, load it with `launchctl bootstrap gui/USER_ID PATH`, and start it with `launchctl kickstart gui/USER_ID/com.extractor.controller`. `RunAtLoad` and automatic restart are disabled in the template. Enable reboot startup only for the explicitly selected dedicated environment. Lid close and loss of power remain outside `caffeinate` guarantees.

On a dedicated Linux runner, use [controller.service](controller.service) as a user service after replacing placeholders. Run `systemd-analyze verify PATH`, then `systemctl --user daemon-reload` and `systemctl --user start extractor-controller.service`. Enable reboot/login startup only under the same explicit runner authorization. Both service templates use automatic recovery, so reboot or service restart cannot revoke an operator stop. Automatic failure restart is disabled so an access or budget blocker cannot create an unbounded restart loop. Before any service restart, read `status`, inspect the retained blocker, and use the same run ID. Stop through the controller command first; service termination still triggers the controller's durable stop handler.
