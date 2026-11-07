# AUT-06: GitHub pagination and failed-verification recovery

Date: 2026-11-07. Maintenance [issue #105](https://github.com/sigaev-pro/bc-indexer/issues/105), outside the fixed 84-task catalog. Author invocation `d93-aut06-author-051` was reserved before dispatch. Accepted base: `f917e73a7e5ec3de28110faddb6a924d2e04cf9e`.

## Retained failure and scope

The first canonical `E01-01.builds` verification retained a normal exit 1 during its live-update control. Operation `78d89ce5487b416a9ab476da9a856e02` used candidate `f17bb195d1baf62e93c596ed0fc067f2c6b60574` and the accepted base above. Its two preceding commands exited 0. All three results report completed/already-exited with complete cleanup; this report does not replace their raw evidence. The task remains failed and unpublished. Its candidate worktree, original evidence, canonical run, and ledger are unchanged by this maintenance author.

The trusted updater rejected real GitHub pagination that changes a named repository path to `/repositories/{id}/...`. The initial isolated positive controls reproduced two errors before the fix. The trusted reader now authenticates the numeric alias against retained metadata at the exact configured official repository endpoint: a positive non-boolean integer ID, exact `full_name`, and exact canonical named API `url`. Origin, endpoint tail, loop, ambiguous-link, and 64-page bounds remain required. Non-GitHub behavior is unchanged. Synthetic transports include retained metadata and numeric second pages; they are not live acquisition or product evidence.

## Recovery boundary

The [runbook](../automation/README.md) documents the inspection/apply API. Recovery is limited to D93 and unpublished failed `E01-01.builds` verification. It authenticates the old frozen source and derived recipe pack against old Git objects, allowing an accepted equal or descendant executing source without weakening ordinary frozen-source checks. Old and new pack membership may differ. The recovery module itself is included in future frozen verifier packs.

Read-only inspection returns binding metadata, not raw evidence. Apply rechecks exact revision, epoch, task, operation, candidate, source, lease, command, request/result/log hashes, process ownership, Docker cleanup, and publication state. It retains regular bytes and symlink target descriptions without following links; special files are rejected. Ancillary evidence is included in the manifest. The complete prior run and ledger observation remain in the immutable archive.

The task transitions to `BLOCKED` with `BLOCKED_REPAIR_REQUIRED` before its exact lease is released. A durable cleanup marker blocks dispatch, handoff, and adoption until replay finishes. Replay rejects changed evidence, a different lease, or subsequent run progress. Explicit stop acknowledgement is separate, starts no work, and does not revise run history. Neither inspection, archive, nor automatic recovery clears operator stop. Counters, attempts, receipts, identities, and original failures remain retained.

After cleanup, changed accepted source requires the normal explicit configuration handoff. A repair using unchanged accepted source may retain its epoch. Either path requires a different candidate SHA, independent review, and actual trusted verification before acceptance. Same-head retries and generic proof overrides are unsupported.

## Checks and limits

Focused pagination controls cover numeric aliases, exact official metadata, forged/unavailable observations, wrong origin/repository/tail, loops, ambiguous links, limits, optional metadata, and unchanged non-GitHub behavior. Recovery tests use disposable Git repositories and synthetic session/Docker observations. They cover old/new pack membership, raw Git bytes, accepted source ancestry and equality, configuration drift, stopped/active/uncertain work, tampering, symlinks, special files, same-epoch repaired adoption, and eight crash boundaries around archive/state/release/completion.

The first expanded test run exposed a test expectation mismatch: pending adoption correctly returned existing `BLOCKED_HANDOFF`, while the new test expected `BLOCKED_RECOVERY`. Only that assertion was corrected. The combined focused run passed all 40 methods in 41.53 seconds. Portable CI then passed in 163.39 seconds: 315 tests with two opt-in Docker skips, all 54 component negatives, 105 documents/1,090 links, 84 task entries, 16 normative mirrors, and unchanged original v2.

One final source-authentication refinement requires the complete derived `trusted_pack` field, in addition to authenticating every old member. Its expanded existing regression and all 17 recovery methods passed in 33.60 seconds. The earlier CI result is retained as a check of the preceding working source; final exact-commit CI is run after committing and reported in the ownership handoff. These controls test recovery mechanics, not successful product verification or native execution.

Checked implementation SHA-256 values:

| File under `.project/automation/` | SHA-256 |
| --- | --- |
| `verification_recovery.py` | `bd4334da11c6ce91db5e1d21f7cabf0d6148bf5cdbf4ba48d389a658b1604eb3` |
| `build_updates.py` | `f579881e686fe93351f969025c5a4cbe530be075f35bb730b0f5d133e484160f` |
| `build_update_vectors.py` | `c18a7c4946332337079216a766a1b9cf5921c1dea9793a9179be5f3bed1e5208` |
| `build_task_verifier.py` | `4d0383b6c79402565cfeae03f9b322c4457568c8521ae8a3bf3a1779b979a1e4` |
| `controller.py` | `8e775b382d957846182bf0a43d560b0d6d2a7c8ab1b6b7b0ef815eac3a634ade` |
| `controller_backend.py` | `e48b6f164d2776d24c23c223a47ac05452a65c7ae898af07d9021e503eff26d8` |
| `controller_handoff.py` | `fb35d7e368c4e6ba53e0cb52bb8df14d11a400a13ecaccfe04b75f9fc807e640` |

Native builds and native/NATS runtime remain NOT_RUN. No product files, locks, catalog, credentials, canonical runtime state, remote publication, or services were changed. Next: commit checked maintenance, obtain both independent exact-head reviews and protected acceptance, then let the coordinator inspect/apply recovery and perform the normal source handoff and repaired-candidate adoption.
