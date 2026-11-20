# Polygon crossing readiness plan — 2026-11-20

Read-only assessment by the coordinator (background investigator) for the
owner's reassignment decision on the Polygon transition stream (PR #194).
No Polygon tree was modified.

## Current state (verified)

- Judge worktree `.worktrees/type-paths-poly-crossing-judge` at `694a22c`
  (clean, pushed). `crossing_judge.py`/`crossing_helpers.py` are
  byte-identical to the audit-05 versions; the two newer commits only add
  `equivalent` reason labels to exactly the nine surviving mutants of
  `crossing.mutants.json`. The mutation tool honors the labels: a labeled
  survivor counts as EQUIVALENT, a labeled killed mutant fails the audit.
- Offline suite: 49 tests OK (matches the audit-05 baseline).
- Producer worktree `.worktrees/type-paths-poly-crossing` clean at
  `37e59cb`; the paired London/Jaipur + Prague/Bhilai preparation02
  (`d107-poly-crossing-preparation-02-37e59cb/tool.json`, sha256
  `54f6e408d248dce9ca44b6362244dc25c3e9c6eb2570b12cc03937f884d4`) is fully
  retained; only `bor/bin`, `bor/inputs`, `bor/bodies` are needed and all
  survived the cache reclamation.

## Why the nine survivors are annotated equivalent

Each is a guard-deletion mutant subsumed by a mandatory downstream check:
strict tuple unpacks that raise for every other arity
(`positive["blocks"]`, `auth[0]`), the recursive `configuration_numbers`
type rejection, the strict uint64 serialization inside `intent_digest`
(sequence and head numbers, rejecting bool/float aliases), and three
hash-collision assumptions (state-root advance, header-hash and tx-root
distinctness under committed roots). Killing them would require deleting
the subsuming check or breaking a collision assumption.

## Next commands after reassignment

1. Offline gate: `python3 -m unittest discover -s test/type_paths/poly/crossing`
   (expect 49 OK).
2. Mutation audit 06 (the unblocking step):
   `python3 test/mutate.py test/type_paths/poly/crossing/crossing.mutants.json
   --output .project/automation/runs/e01-completion-20261119/poly-crossing-mutants-06-694a22c`
   — expect baseline PASS, 131 KILLED + 9 EQUIVALENT, result PASS.
3. Independent review of the nine equivalence claims (ORACLE.md requires
   it); a disputed claim costs one strengthen-and-rerun iteration.
4. Paired native run (serialized slot, ~4 GiB free):
   `crossing_native.py run <prep-02 tool.json> --sha256 54f6e408... --output
   d107-poly-crossing-run-02-37e59cb`.
5. Judge caveat: the current judge expects the unpaired 3-case vector; the
   paired observation (5 cases, Bor gates at block 2) needs a judge
   extension first — 5-case matrix, per-case Bor gate, `bor_activation`
   verification, Jaipur SealHash and Bhilai-denominator-16 expectations —
   plus new offline tests, mutant rows and another audit (2-3 iterations).

## Madhugiri/MadhugiriPro body-StateSync fixture

Separate follow-up beyond PR #194: a case scheduling `madhugiriBlock` at
block 2 whose crossing block's final body transaction is the type-127
StateSync built from Heimdall events; native array-encoding checks, two
StateSync items at a sprint boundary including a reverting recipient,
per-item before/after state, receiver progress, sequential event IDs,
receipt and final root; rejection controls for missing/non-final/mismatched
body StateSync (unknown-type decode refusal reported separately). Only then
may the judge flip `reserved127` off NOT_EVALUATED.

## Estimate

One offline iteration to unblock PR #194 (audit + equivalence review);
3-4 total including the paired-judge extension; Madhugiri is a further
follow-up.
