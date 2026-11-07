# Continuous development under D93

**Legacy controller reference.** D95's [issue-driven workflow](../docs/DEVELOPMENT_WORKFLOW.md) governs current product work. This file describes retained D93 controller mechanics only. Its reservations, adoption, handoff, and recovery steps are not prerequisites for direct sessions, drafts, or evidence-producing product checks. Use them only for an explicitly scoped controller task; preserve historical state.

[D93](../docs/DECISIONS.md) is the current owner authorization. Complete the accepted backlog in dependency order, with one concrete, reviewable outcome per block. Record the issue/subtask, scope, checks, failures, acceptance, commit, and next action. Continue to the next ready block without asking for routine permission. A real blocker on one task does not prevent independent ready work.

Campaign time, invocation totals, and ordinary implementation/review repair counts are history, not stopping limits. Keep all attempts. A repeated review failure on the same commit produces `BLOCKED_NO_PROGRESS`: diagnose the cause and prepare a changed candidate, rather than repeatedly submitting it. Failed processes still require cleanup and a diagnostic recovery; failed product verification is not a retryable model-session error. D83's test failure rules remain unchanged.

Technical limits remain finite: at most three active model sessions including the coordinator, one writer per worktree, the saved model-session timeout, per-command timeouts, a 7,200-second native build verification ceiling, bounded API retries, and graceful/forced cleanup. The standard runtime test run remains at most 1,800 seconds, excluding preparation. D93 does not authorize production deployment, purchases, relaxed acceptance, or invented evidence.

## Adopt the accepted policy once

Use the accepted, clean controller source after AUT-04 review, CI, and protected merge. D93 continues `run_D92_1794007047`; its ID is a historical identity, not its current policy. `initialize --authorization-ref D93` is deliberately rejected. Do not create a new run to lose earlier results.

1. Inspect current run revision, configuration epoch, task operations, leases, and session cleanup. Resolve unfinished work first. The run must be STOPPED, BLOCKED, or BUDGET_STOP, with no pending operations, active delegated sessions, unreleased task leases, or unfinished candidate worktrees.
2. Retain actual AUT-04 bootstrap review observations in a private JSON file. Use schema version 1, `authorization_ref: D93`, `coordinator_agent_id: /root`, and an `observations` list with all actual bootstrap reviewer invocations. Each row includes its unique invocation ID, real canonical agent ID, role `reviewer`, task `AUT-04`, status `completed`, observed times, `cleanup_confirmed: true`, and an evidence reference. Include both independent review axes and every failed or superseded review round. These bootstrap invocations are honestly labeled `recorded-after-start`; do not manufacture reservations or provider IDs. Reserve future invocations before dispatch.
3. Run `controller.py adopt-authorization --run-id run_D92_1794007047 --authorization-ref D93 --stage-limit E07 --expected-revision CURRENT_REVISION --expected-epoch CURRENT_EPOCH --input OBSERVATIONS_FILE`. Replace the three placeholders with the inspected values and absolute file path. This verifies the accepted source, bootstrap gate, current preflight, and unchanged task catalog before adoption.
4. Inspect `status`. Adoption preserves the old D92 ledger and appends authorization and configuration history. The D93 ledger copies every earlier session unchanged, adds observed bootstrap reviews, and pins its predecessor digest. A crash between ledger preparation and atomic run replacement is replayable with the same inputs. A conflicting prepared ledger blocks adoption. A repeated successful command does not create another epoch or reset counters.
5. Use `resume --run-id run_D92_1794007047 --authorization-ref D93 --stage-limit E07` to continue. The coordinator may do this under D93 without another owner confirmation. Adoption itself does not dispatch work or clear an operator stop. An explicit later owner stop must still be honored.

`account-sessions` and `reserve-session` use `--authorization-ref D93 --stage-limit E07` after adoption. Maintenance reservations use their `AUT-NN` task identity; product reservations require the existing run, an eligible recipe, and accepted dependencies. Counts remain cumulative. `--renew-budget` is rejected because no campaign window needs renewal.

## Continue and recover

Finish the current issue block, obtain independent exact-head review, run its required checks, commit, and use protected acceptance. Save the next concrete action, then continue automatically. Add missing trusted adapters as separately reviewed maintenance blocks before dispatching their product tasks; missing adapters never mean product success.

For reviewed configuration changes, retain the explicit revision-checked `handoff` procedure. For real external candidate adoption, use an attestation naming D93 and the exact run/epoch. Neither operation resets attempts or accepts unverified output. Keep finite deadlines in worker, verifier, and command requests even though the run-level `deadline` is null.

Service crash recovery uses `resume --automatic` with D93 and E07. It resumes only a previously RUNNING run. It does not clear stop markers, authorize diagnostic process retries, or erase blockers. The coordinator diagnoses blockers and continues independent work; a new permission question is needed only for a genuine missing authorization, resource, access, or product decision.

The historical [D92 handoff procedure](launch-handoff.md) describes its original fixed window. Existing D86/D92 runs retain that policy until the explicit adoption above. Never relabel their old requests, results, or receipts as D93 execution.
