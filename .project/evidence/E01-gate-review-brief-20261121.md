# G.E01 (#78) gate review brief — prepared 2026-11-21

Coordinator preparation for the independent gate reviewer. The gate may run
only after issue #21 closes (PR #194 merged); this brief fixes the scope so
the review cannot drift into new gates.

## What the gate requires (TASK_DECOMPOSITION, D82/D96/D108)

All mandatory outputs of E01-01..E01-05 separately for ETH, BSC and POLY:
evidence schemas, builds, harness, pins, inputs, owned capture, admission,
decision bridge, lifecycle seams, smoke corpus, early faults, type paths.

## Verified state at preparation time

- **E01-01/02/03**: accepted in PRs #169, #173 and the four E01-02 blocks
  (D106 acceptance audit found no failed check and no bypass without an
  owner).
- **#20 early_faults**: closed 2026-11-21. Block 0 (PR #174), Geth A/B
  (#176/#182 merged `ae8b428`; run09 PASS 30/30 cases, 4/4 handoffs,
  review 002 SPEC/Standards PASS), BSC A/B (#177/#179), Bor A/B
  (#178/#181).
- **#21 type_paths**: BSC leg merged (`ccd786a` — coherent private
  current-fork import with both importer refusal controls; judged run09 +
  run15 PASS; three review rounds closed with a delta-only PASS). ETH leg
  accepted (#183, #186). POLY: current-fork proof (#189), preactivation
  (#193) accepted; crossings: paired native run PASS + paired judge PASS
  (judge `ba18c40`), mutation audit 06 PASS at `694a22c` with 9/9
  equivalence claims independently UPHELD; Madhugiri body-StateSync cases
  (producer `e7634d1`, native run01 PASS on the first attempt) with the
  judge extension in flight. Remaining at preparation: judge the Madhugiri
  observation, audit-08, independent review of PR #194, merge.

## Non-blockers the reviewer must NOT convert into gates (D96/D98/D100/D106/D108)

- Every public-profile cell NOT_RUN (assigned to E06/E07).
- Historical/public upgrades beyond the applicable crossings; no new
  blanket crossing gates.
- Polygon normal-profile type-3 (refusal is the accepted outcome).
- Full normalization semantics (E04); reorg/finality/combined faults (E05);
  native Synced GoLive (E06-09); performance (D40).

## Reviewer tasks

1. Confirm every closed/merged item above from the primary records
   (evidence files, PR merge commits, run records with SHA-256 chains).
2. Confirm the applicability matrix reconciliation
   (E01-applicability-matrix-20261120.md) is consistent with the accepted
   state at review time.
3. Confirm the E01 output inventory: CaseRegistry/CaseResult/RunManifest
   schemas exist and no verdict claims credit without an actual run
   (NOT_RUN discipline).
4. Return PASS or REQUEST_CHANGES with numbered findings; PASS closes
   G.E01 (#78) and unblocks E02.
