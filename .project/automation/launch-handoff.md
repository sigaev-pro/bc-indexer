# D92 launch, accounting, and configuration handoff

D92 starts at Unix time `1794007047` and ends at `1794032247` (2026-11-07 06:17:27 UTC). Preparation, preflight, implementation, review, recovery, and configuration changes consume that same window. Only process cleanup and honest completion accounting may finish after the deadline. This procedure preserves the existing D86 run; it never widens or renews that run.

The canonical state directory contains `launches/D92.json` and the single D92 run, `run_D92_1794007047`. Both use the repository writer lock and fsynced atomic JSON. A configuration change appends an epoch to this run. It does not create a fresh budget or replace task history. Keep the canonical state directory and retained acceptance evidence when moving execution to another host; a separate empty directory is not a continuation of this launch.

## Invocation accounting

One invocation is one explicitly dispatched coordinator, implementer, or reviewer task turn. This counts the current coordinator and the two delegated turns as three used invocations at launch. It does not claim to measure inaccessible internal provider inference calls. A diagnostic retry or a newly dispatched follow-up turn consumes another invocation. Cancelled reservations remain charged conservatively. At most three active or uncertain reservations can exist, including the coordinator, and at most one can own a particular implementation/review task. An implementation reservation consumes one of that task's three implementation attempts. D83 runtime failure/diagnostic rules remain separate.

Import the coordinator's actual initial observations before dispatch. The initial import is explicitly after-start accounting because these three sessions predate installation of this feature. Unknown start timestamps stay `null`. No provider session identifier, CLI log, process exit code, or prior reservation is invented. Re-importing the identical document is idempotent. Changing an invocation's task, role, canonical agent, or known timestamps is rejected.

```sh
python3 .project/automation/controller.py account-sessions --authorization-ref D92 --stage-limit E07 --input /absolute/private/launch-observations.json
```

The input is a JSON object with `schema_version: 1`, `authorization_ref: "D92"`, `coordinator_agent_id: "/root"`, and an `observations` array. Each row contains:

| Field | Meaning |
| --- | --- |
| `invocation_id` | Stable unique dispatch/reservation key. |
| `canonical_agent_id` | Actual collaboration identity, such as `/root/e01_evidence`; never an invented provider ID. |
| `role` | `coordinator`, `implementer`, or `reviewer`. |
| `task_id` | Exact assigned task; `null` for the coordinator. |
| `status` | `active`, `completed`, or `cancelled`. |
| `observed_at` | Actual Unix observation time. |
| `started_at` | Known dispatch time, or `null` when unavailable. |
| `completed_at` | Actual completion observation time, or `null` while active. |
| `cleanup_confirmed` | `true` only when the turn is finished and no owned writer/subprocess remains. |
| `evidence` | Actual observation or retained handoff reference. |

After initial import, reserve every new collaboration turn **before** starting it:

```sh
python3 .project/automation/controller.py reserve-session --authorization-ref D92 --stage-limit E07 --input /absolute/private/reservation.json
```

A reservation document contains `invocation_id`, `role`, and `task_id`. AUT-01 maintenance and coordinator reservations can precede the run. Product reservations additionally require `--run-id run_D92_1794007047`, an eligible recipe, and independently verified accepted dependencies. Update that reserved identity through `account-sessions` after observing the actual canonical agent and completion. A reserved or active turn without complete cleanup remains occupied. Coordinator observations cannot overwrite a supervised CLI process's cleanup evidence.

All subsequent controller model operations also reserve this ledger before dispatch. Their stable operation identities prevent double charging after a crash between intent, reservation, and execution. The 200th reservation may finish its bounded work; a 201st reservation is rejected. Lower per-run limits may narrow this ceiling. D92 rejects `--renew-budget`, and another `run` command cannot create a second D92 run. `status` displays the current shared ledger count even if the last run snapshot predates an observation update.

## Initialize and adopt an external candidate

Initialize a stopped run after AUT-01 is accepted on main. This performs the normal BOOT-09/preflight guard, freezes the clean accepted controller source and recipes, imports actual acceptance receipts, and requests no model work:

```sh
python3 .project/automation/controller.py initialize --authorization-ref D92 --stage-limit E07
python3 .project/automation/controller.py status --run-id run_D92_1794007047
```

The coordinator may adopt the real candidate only after its author turn has completed and relinquished its worktree. Currently only `E01-01.evidence` has this adapter. The input identifies the exact worktree, branch, base, head, configuration epoch, and completed implementer invocation. Git independently checks repository/worktree ownership, clean state, ancestry, allowed paths, required tracked outputs, and current main. This establishes candidate provenance; it does not establish task acceptance.

```json
{
  "schema_version": 1,
  "authorization_ref": "D92",
  "coordinator_agent_id": "/root",
  "run_id": "run_D92_1794007047",
  "configuration_epoch": 1,
  "task_id": "E01-01.evidence",
  "implementer_invocation": "ACTUAL_RECORDED_INVOCATION",
  "head_sha": "EXACT_40_HEX_CANDIDATE",
  "base_sha": "EXACT_40_HEX_ACCEPTED_BASE",
  "worktree": "/absolute/canonical/repository/.project/worktrees/e01-evidence",
  "branch": "ACTUAL_BRANCH"
}
```

Optionally include a `review` object containing `invocation_id`, the same exact `head_sha` and `base_sha`, `verdict` (`PASS` or `REQUEST_CHANGES`), and string arrays `checks`, `findings`, and `unverified`. The reviewer must be a completed, cleanup-confirmed observed invocation assigned to this exact task. Its canonical agent must differ from every agent that implemented the task; a different invocation key for the same agent is insufficient. A PASS has actual checks and no findings. An invocation cannot later be rebound to a different candidate or review.

```sh
python3 .project/automation/controller.py adopt-candidate --authorization-ref D92 --stage-limit E07 --run-id run_D92_1794007047 --expected-revision CURRENT_REVISION --expected-epoch CURRENT_EPOCH --input /absolute/private/candidate-attestation.json
```

Adoption retains the whole coordinator attestation and its digest. Without a supplied independent review it enters REVIEW. A supplied PASS enters VERIFYING; a review requesting changes enters IMPLEMENTING. Existing attempts and prior candidates remain recorded. A missing, active, foreign, or uncertain invocation blocks adoption. Pending operations, published work, and accepted receipts must first be reconciled through their existing operation identities.

Explicit `resume` may then continue the existing controller path. It clears the operator stop only by that explicit command. It executes the trusted verifier and the explicit evidence unit-test command, retains full task proof, publishes the separate authenticated `controller_verification` binding, captures the exact-head GitHub gate, and verifies actual merge/closure receipts. External attestations never substitute for this verification. Generic green bootstrap CI and a review alone remain insufficient. Automatic service recovery preserves a durable stop or budget stop.

## Reviewed configuration changes

The coordinator and QA own later recipes and independent adapters. New product tasks remain blocked until their real verifier and acceptance path have been implemented, independently reviewed, and accepted. AUT-01 does not admit E01-01.builds or native tasks. Accepted source and image prefetching are preparation; they do not accept a dependency or establish a native PASS.

Use `handoff` from a clean accepted source checkout at a stopped or blocked boundary. It requires the current revision and epoch, no pending operations, no active or uncertain delegated turns, no unreleased/foreign task leases, and no unfinished candidate workspace. The coordinator's own active accounting row does not prevent it from performing the handoff. The immutable task graph must match. A mismatch or uncertain ownership fails closed; handoff never kills workers, clears leases, discards work, or resets attempts.

```sh
python3 .project/automation/controller.py handoff --authorization-ref D92 --stage-limit E07 --run-id run_D92_1794007047 --expected-revision CURRENT_REVISION --expected-epoch CURRENT_EPOCH
```

One atomic state replacement appends the full old source/recipe snapshot to `configuration_history` and freezes the newly accepted source/recipes. It preserves authorization, deadline, consumed invocations, limits, attempts, receipts, candidate/session history, and the operator stop marker. Only a task blocked specifically by `BLOCKED_UNCONFIGURED_VERIFICATION` returns to PLANNED when the new adapter declares its recipe eligible. Other blockers remain. Resume cannot implicitly adopt changed source bytes. Inspect `status`, then use explicit `resume` when continuation is intended.

The deadline is checked before and after setup, before each operation/reservation, during subprocess execution, and before handoff/adoption commits. Subprocess bounds also use monotonic elapsed time so a backward wall-clock change cannot extend a running process. At expiry the controller begins owned-process cleanup and retains uncertainty rather than claiming cleanup succeeded. Completed observation import after expiry records history only and cannot authorize further work.
