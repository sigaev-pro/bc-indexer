# E02 parallel implementation plan — 2026-11-21

Coordinator preparation for opening E02 the moment G.E01 (#78) passes.
Scope source: TASK_DECOMPOSITION (E02 cards, dependency column) and the
GitHub backlog (#22-#29, gate #79). No E02 work starts before the gate;
this document only fixes the split, the sequence and the review rules.

## Dependency graph (from TASK_DECOMPOSITION)

Phase 1 (independent, parallel): E02-01.semantic_descriptors (#22),
E02-05.actions_model (#23), E02-08.block_metadata (#24),
E02-10.closed_profiles (#25) — each applies to ETH/BSC/POLY from the
retained E01 fixtures; all four depend only on accepted E01 outputs.

Phase 2 (convergent, serialized): E02-02.schemas (#26, COMMON) needs all
four; E02-02.codec (#27, COMMON) needs schemas; then E02-03
.lifecycle_validator (#28) and E02-04.compatibility (#29) run in parallel
again (validator needs schemas+descriptors; compatibility needs codec).

Phase 3: G.E02 review (#79).

## Streams and concurrency

D97 allows at most three implementer sessions besides the coordinator, so
the four Phase-1 streams map to three background implementer agents plus
one stream taken by the coordinator directly:

- **Stream A (agent):** #22 semantic_descriptors — manifest identity:
  activation/revision/source stable until delivery; decision→batch
  allocation exactly once; per-family sources from the retained E01
  decision interfaces.
- **Stream B (agent):** #23 actions_model — typed native monetary actions,
  common execution_order, cause equality, RollbackScope refs/tree/
  validator; receipt summary never double-counts (MSG, SEM).
- **Stream C (agent):** #24 block_metadata — block/lifecycle schema incl.
  empty blocks; native counts are not completeness assertions.
- **Stream D (coordinator):** #25 closed_profiles — closed registry of
  payload/extensions/presence; D75 receipt kinds/native_type/origins;
  D80 sources_binding_digest Hash32 mandatory in SourceObservationRef and
  L1Checkpoint; unsupported version gets no ANY fallback.

Each stream: its own worktree and branch (e02-<task>-<family>), offline
fixtures from the retained E01 run records (no native runs in Phase 1 —
no grant needed), portable tests, a mutation table, then two parallel
read-only review agents (Spec + Standards) per draft PR.

## Review and merge discipline

- Draft PRs open early; CI + both reviews gate merging.
- Merges serialized under D95; each needs the owner's approval.
- Review-status contexts are posted by the coordinator per the standing
  procedure (both `bootstrap/ci` and `bootstrap/independent-review`).
- Suggested merge order: A, B, C, D (D last: closed_profiles feeds
  schemas' variant table and benefits from the other three merged first).

## Phase 2 details

- #26 schemas (COMMON, single implementer): reproducible proto bindings
  and structural validators over the four merged models; starts the day
  the last Phase-1 PR merges.
- #27 codec after #26; #28 and #29 in parallel after their inputs merge.
- G.E02 (#79) review when all mandatory schemas and local contract cases
  exist and pass.

## Estimates

Phase 1: ~3-4 calendar days at full parallelism (any stream that stalls
does not block the others; the coordinator stream absorbs slack).
Phase 2: ~3-5 days. Total E02: ~1.5-2.5 weeks versus ~3-4 serial.

## Start checklist (on gate PASS)

1. Close #78 with the review verdict; open E02 in PROJECT_STATUS.
2. Scaffold `protocol/{semantic,balance,block,profiles,proto,codec,compatibility}`
   and `test/lifecycle_validator` on a coordination branch; merge first.
3. Spawn streams A-C (worktrees+branches+agent prompts pinned to the
   TASK_DECOMPOSITION cards and the E01 evidence files); take stream D.
4. Telegram/system-monitoring automation for the owner runs alongside
   (outside the task graph; see PROJECT_STATUS ops note if enabled).
