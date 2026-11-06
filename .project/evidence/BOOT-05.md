# BOOT-05 CI and independent acceptance

Task: [BOOT-05](https://github.com/sigaev-pro/bc-indexer/issues/4).
Starting main commit: `1ab566cf402a84661729502685be284afb38ba73`.
Launch boundary: D86, BOOT only.

The candidate adds a pinned GitHub Actions workflow, portable offline checks, an exact-commit acceptance collector/evaluator, isolated rejection tests, and an explicit branch-protection request. The workflow and evaluator enforce separate CI and independent-review contexts. Independent agent review is represented honestly under the existing GitHub actor, not as approval by a second person.

Operational instructions and the review-comment schema are in [automation/README.md](../automation/README.md). Runtime results for native product execution remain NOT_RUN.

## Final allowed collector repair

Candidate `799a25f444257f64268c469a63b9cc340653eee6` passed independent review and real Actions run `42280000422`. Review comment `6693562933` and status `58284457193` were published. The next live collector call stopped with `BLOCKED` and error `'creator'`: the combined-status response omits that field, while the status-list response includes it. A collect-to-evaluate regression using captured minimal API fields reproduced the same failure before the correction.

The correction preserves both raw response shapes and obtains publisher identity only from the latest status-list row matching the current combined status. It compares ID, commit/repository URL, context, state, target URL, description, and timestamps. Missing publisher proof, wrong publishers, mismatched IDs or URLs, newer statuses, changes between reads, and full status pages are rejected. Tests exercise collection through the API adapter, not only the evaluator's in-memory input. The full local bootstrap suite passes all 31 tests, document checks, Python/JSON checks, and the unchanged original v2 hash.

The complete read-only live smoke with the corrected local collector returned PASS for the already reviewed remote head `799a25f444257f64268c469a63b9cc340653eee6`, base `1ab566cf402a84661729502685be284afb38ba73`, CI run `42280000422`, PR #87 review comment `6693562933`, and review status `58284457193`. The snapshot also confirmed current branch protection, exact CI provenance, review body digest, and synthetic merge parents/tree. This is API-path validation against an existing accepted review/CI pair; it does not accept the modified collector code or authorize merging its new commit.

This is the second repair cycle and third implementation attempt allowed by the bootstrap budget. The new commit still requires independent review, fresh real CI, a new exact-head report/status, and a successful live gate. A further failure must be recorded as a blocker rather than starting another uncontrolled repair cycle. No new remote mutation, push, or merge was performed during this repair.

## Live verification and collector correction

SSH publication succeeded for `9896982a7ef995bd76120cb0dc310c407d47719a`, and [PR #87](https://github.com/sigaev-pro/bc-indexer/pull/87) was created. [Actions run 42279051046](https://github.com/sigaev-pro/bc-indexer/actions/runs/42279051046) passed on that exact head. The coordinator applied and read back the reviewed branch policy, including `bootstrap/ci` from GitHub Actions app 15368, required independent review, strict checks, and administrator enforcement. The PR was blocked while its review status was missing. Independent review then passed, and comment 6693432825 plus its digest-bound status were published.

The live collector failed on a valid GitHub PR comment URL: `https://github.com/sigaev-pro/bc-indexer/pull/87#issuecomment-6693432825`. Its URL check accepted only `/issues/...`. A focused regression reproduced this rejection before the fix. The correction accepts the valid PR route and binds the URL, API `issue_url`, response ID, and requested comment ID to the selected repository and intended PR. Tests reject other repositories, PR numbers, and comment identities. This fixes collector parsing; it does not relax review or branch controls.

The first URL correction passed all 25 tests and the document checks. Its successful Actions run and review remain evidence for `799a25f444257f64268c469a63b9cc340653eee6` only; the final collector repair above supersedes that candidate. No merge or BOOT-05 completion is claimed.

## Local verification

`python3 .project/automation/ci.py` passes on local Python 3.13.5. It runs 23 unittest cases across the agency and automation modules, including rejected missing/failed/stale CI, newer pending CI attempts, stale or self-authored reviews, changed review comments, changed base/merge trees, incomplete branch protection, and a PR head race during proof collection. Tracked-file regressions reject missing committed targets and ignore unrelated worktree documents.

At implementation commit `3af36b6661cdb6855459950221ff85fd6985bd9c`, document checks passed for 85 tracked Markdown files, 1008 local links, all 16 normative mirrors, 63 requirements, 217 acceptance cases, 68 product tasks, and 84 issue IDs. Original v2 SHA-256 remains `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634`. Python compilation, JSON parsing, agency validation, and Git whitespace checks passed. BOOT-06 component files are not yet on this branch; CI invokes their validator when they are merged and rejects a partial component directory without it.

The exact-head local run on implementation commit `3af36b6661cdb6855459950221ff85fd6985bd9c` passed with `BOOTSTRAP_HEAD_SHA` set to that commit and `BOOTSTRAP_BASE_SHA` set to the starting main commit. Its source state was `clean`. Negative acceptance tests use local fixtures and fake GitHub reads; they do not create real issues, statuses, or PRs.

## HTTPS publication blocker and SSH recovery

Independent review of `3af36b6661cdb6855459950221ff85fd6985bd9c` returned PASS. The subsequent push was rejected because the current gh OAuth token lacks the `workflow` scope required to publish `.github/workflows/bootstrap.yml`. Its reported scopes are `repo`, `read:org`, and `gist`. This is an access failure, not a CI result.

At the time of the HTTPS rejection, no BOOT-05 PR had been created, and the verified remote `main` was `1ab566cf402a84661729502685be284afb38ba73`. The coordinator initially asked the owner to refresh gh with the `workflow` scope. The owner reported that the OAuth code did not arrive and offered SSH authentication instead. The later successful SSH publication and live results are recorded above.

D88 records the selected recovery. The coordinator checked the existing public-key configuration and ran SSH authentication with `BatchMode=yes` and `StrictHostKeyChecking=yes`. GitHub returned: `Hi sigaev-pro! You've successfully authenticated, but GitHub does not provide shell access.` The command exited 1 with that success message; this establishes GitHub identity, not shell access or permission to publish this candidate. Only this repository's origin changed to `git@github.com:sigaev-pro/bc-indexer.git`. No key material, global Git/SSH configuration, or gh authentication was changed, and no credentials are recorded here. gh remains available for its permitted issue, PR, and API operations.

The OAuth workflow-scope refresh is no longer the chosen prerequisite for Git publication. Successful SSH authentication does not establish push, CI, branch-policy, or API readiness. Keep the original HTTPS failure as historical evidence and verify the chosen transport through the normal reviewed branch push.

Recovery sequence:

1. Confirm that this repository's origin still identifies `sigaev-pro/bc-indexer` over SSH. Reuse the already verified key and keep strict host-key checking; no new key or global authentication change is needed for this recovery.
2. Independently review this status-only delta and record the full new candidate SHA. A PASS for the earlier implementation commit does not silently accept a later head.
3. Retry the normal, non-force push of the reviewed `boot/05-ci-acceptance` branch over SSH. Use gh to create a PR containing `<!-- extractor-task:BOOT-05 -->` only after the push succeeds. Treat any actual Git or API permission failure as a separate blocker.
4. Apply and read back the reviewed branch policy; verify the real required context names and publishers. Do not remove the workflow or weaken protection to bypass the access failure.
5. Observe successful Actions on the exact head, publish the real independent report and its digest-bound status for that head, and run the live acceptance collector.
6. Merge with the expected-head condition, verify the resulting tree, and record the actual issue/PR evidence. Stop before E01.

## Remote acceptance still required

Before closing this issue, verify the corrected candidate with fresh review and CI, publish its new review status, rerun the live collector, and verify the merge tree. The earlier publication and policy setup succeeded; their existence does not make the new head accepted. Native product runtime remains NOT_RUN.
