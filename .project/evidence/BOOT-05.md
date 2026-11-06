# BOOT-05 CI and independent acceptance

Task: [BOOT-05](https://github.com/sigaev-pro/bc-indexer/issues/4).
Starting main commit: `1ab566cf402a84661729502685be284afb38ba73`.
Launch boundary: D86, BOOT only.

The candidate adds a pinned GitHub Actions workflow, portable offline checks, an exact-commit acceptance collector/evaluator, isolated rejection tests, and an explicit branch-protection request. The workflow and evaluator enforce separate CI and independent-review contexts. Independent agent review is represented honestly under the existing GitHub actor, not as approval by a second person.

Operational instructions and the review-comment schema are in [automation/README.md](../automation/README.md). Runtime results for native product execution remain NOT_RUN.

## Local verification

`python3 .project/automation/ci.py` passes on local Python 3.13.5. It runs 23 unittest cases across the agency and automation modules, including rejected missing/failed/stale CI, newer pending CI attempts, stale or self-authored reviews, changed review comments, changed base/merge trees, incomplete branch protection, and a PR head race during proof collection. Tracked-file regressions reject missing committed targets and ignore unrelated worktree documents.

Document checks pass for 85 tracked Markdown files, 1008 local links, all 16 normative mirrors, 63 requirements, 217 acceptance cases, 68 product tasks, and 84 issue IDs. Original v2 SHA-256 remains `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634`. Python compilation, JSON parsing, agency validation, and Git whitespace checks pass. BOOT-06 component files are not yet on this branch; CI invokes their validator when they are merged and rejects a partial component directory without it.

The pre-commit local report labels its source state `modified`. Rerun the same command on the committed candidate to obtain a clean exact-head result. Negative acceptance tests use local fixtures and fake GitHub reads; they do not create real issues, statuses, or PRs.

## Remote acceptance still required

Before closing this issue, an independent reviewer must accept the exact candidate commit. The coordinator must then publish it through a PR, apply and read back the reviewed branch policy, observe a real successful `bootstrap/ci` run on the exact head, publish the independent review report/status, and run the live acceptance collector. Verify the final merge tree and record the actual GitHub evidence. This document does not claim those remote steps have happened.
