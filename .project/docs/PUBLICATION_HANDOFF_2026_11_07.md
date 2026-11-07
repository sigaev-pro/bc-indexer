# Publication handoff — November 7, 2026

Task: `HANDOFF-PUBLISH-01`, linked to existing issues [#11](https://github.com/sigaev-pro/bc-indexer/issues/11) and [#105](https://github.com/sigaev-pro/bc-indexer/issues/105). Scope: owner-approved step 1 under D94. Publish existing work and report its actual state. No new implementation, independent review rounds, canonical recovery, native runs, or merges were performed in this block.

## Published work

- [PR #106](https://github.com/sigaev-pro/bc-indexer/pull/106): AUT06 recovery and trusted pagination, updated to `db07c163d3d5679308d04bb9b9c3b967973333c0` on `aut/06-verification-recovery`. The PR remains a draft. The four previous review findings have repairs and regression tests; fresh independent review remains pending.
- [PR #107](https://github.com/sigaev-pro/bc-indexer/pull/107): build contracts, manifest schema, planner/verifier, and version-refresh tools at `7022c0dd172e68e35eb6d73eef5487223583c674` on `feat/e01-native-builds-repair`. The PR remains a draft. Real native build acceptance is incomplete.
- `archive/e01-builds-failed-f17` retains `f17bb195d1baf62e93c596ed0fc067f2c6b60574`, the candidate from the failed real-source verification. The original local worktree and branch remain unchanged.
- `archive/e01-builds-before-aut05-7723` retains `7723a1704b7f0f43987611c6d218ecdd73eda256`, the earlier product history before accepted-source synchronization.

Remote refs and both draft PR heads were checked directly after publication. `main` remains `f917e73a7e5ec3de28110faddb6a924d2e04cf9e`; no changes were merged. The old local `master` tip is already an ancestor of `origin/main`, so it did not require a separate publication. Other named worktree branches matched their existing remote refs.

## Verified results

AUT06's retained ordinary-command supervisor run completed successfully on clean `db07c16`: 323 tests, two opt-in Docker skips, and 54 rejected negative controls. Tests took 172.312 seconds; the complete supervised command took about 179 seconds. The earlier context-wrapper timeout remains in the history with an unproven cause. This later retained result was discovered during handoff; no new local test run was started.

The earlier candidate `2e01564` passed CI but received REQUEST_CHANGES from both independent review axes. Those findings concerned a newer stop being erased, an incorrect historical producer path, a logical pagination alias loop, and symlinked evidence ancestors. They are not relabeled as a passing review of `db07c16`.

The product head `7022c0d` has recorded passing results for 51 isolated tests, independent Spec and Standards reviews, and 292 portable CI tests with two opt-in Docker skips and 54 negative controls. The independent reviews included 20 and 27 additional control groups. These are code-level results. The earlier real verification of `f17bb19` failed on GitHub pagination before compilation; the repaired head has not completed a replacement real verification.

GitHub automatically started fresh CI after publication: run `42398930314` for AUT06 and run `42398999006` for the product branch. Both were in progress at the remote publication check. Their eventual results do not replace the remaining review and product acceptance requirements.

## Background work and retained evidence

All worktrees were clean at intake. The AUT06 author model session was interrupted; its launch-ledger entry is now cancelled with confirmed cleanup. No agent was restarted. Supervisor PID 46060, process group 46078, and all 28 recorded child groups were absent. No matching project processes or running Docker containers were found. No process needed to be killed during this handoff.

The retained diagnostic is under `.project/worktrees/aut06-verification-recovery/.project/automation/runs/aut06-repair-ci-diagnostic/commands/command-f7bc89c533f04f6486ac0db804f7094d/`. Its result reports exit zero, complete command cleanup, and complete candidate cleanup. Recorded output hashes match the files:

- `request.json`: `f0cfef5213fe44851fd15d4392ce6084c42f3a7c31dde3b3dd575b37ad07f29f`
- `result.json`: `322265441af117cb8a605e2c4538b8f7c9f37d41df7af2263e9116e96d4ecea4`
- `stdout.jsonl`: `9909bf04de9c21948c6e990a8c57ebebc2cca8adcc7f1fdedfc982c152926e06`
- `stderr.log`: `bd5edb86b2c4a9492abd08c525fa593a5445e6d3a631e8fdf408359ccf0dcf83`

Canonical run `run_D92_1794007047` remains BLOCKED at revision 55, configuration epoch 5, with its original failed build operation retained. Its file SHA-256 is `32330f83bc498f37acdd67da10a10e50e41fb1a538ee1b09a3b44c5d1158d882`. The historical D92 launch ledger remains unchanged at `d5321b9d59ad3b8fc84c4dcceb450deb02bcfbf2f7c31c7398c9d632c936d741`. No counters, failed attempts, or accepted receipts were reset.

## Remaining product work and stopping point

Actual native builds and native/NATS runtime remain NOT_RUN. There is no accepted end-to-end flow from running nodes through the collector to NATS and a checking receiver.

The next product task is still issue #11: obtain actual reproducible client builds and their verification evidence. Under the existing workflow, AUT06 review/acceptance and explicit recovery/source synchronization remain outstanding. The proposed simpler workflow has not been approved; this publication does not silently adopt it or change the specification.

Stop after the publication report. Do not resume controller repairs, new review sessions, or product execution automatically while D94's publication-only scope is active. No deployment or merge occurred.
