# G.E02 (#79) gate review brief — prepared 2026-11-22

Coordinator preparation for the independent gate reviewer. The gate may run
only after issues #22-#29 close; this brief fixes the scope so the review
cannot drift into new gates.

## What the gate requires (TASK_DECOMPOSITION)

All mandatory outputs of E02-01/05/08/10 (phase 1) and E02-02 schemas/codec,
E02-03 lifecycle validator, E02-04 compatibility (phase 2), for COMMON +
per-family applicability: typed schemas with reproducible bindings,
structural validators rejecting malformed records with specific reasons,
the lifecycle automaton with unambiguous reorder/conflict/reinclusion
results, and the compatibility capability matrix.

## Verified state at preparation time

- **Phase 1 merged**: #22 semantic (#197, `b8c41fed`), #23 actions (#198,
  `ad49a4a1`), #24 block (#199, `fd2c3e73`), #25 profiles (#200,
  `72958bf9`); issues closed with acceptance records.
- **Phase 2 running** (three streams, D97 limit): #26 schemas (stream E,
  canonical v2 encodings / TransportFrame / layout plans against
  CONTROL_WIRE_CONTRACT); #28 lifecycle automaton schema-independent half
  (stream F, deterministic reorder/conflict/reinclusion over protocol/
  semantic); #29 compatibility policy skeleton (stream G, capability
  matrix / immutable-pending / drain guard against DELIVERY contracts).
- The schema-dependent halves of #28/#29 and the whole of #27 codec follow
  the #26 merge per the dependency graph.

## Non-blockers the reviewer must NOT convert into gates

- Native runs are NOT required for E02 (the plan assigned none; the corpus
  fixtures derive from retained E01 records offline).
- Full transaction semantics (E04), delivery integration (E03), reorg/
  finality integration (E05) — none may be demanded here.
- Public profile cells remain NOT_RUN per D96.
- Mutation-table coverage: E02 reviews used SPEC/Standards with test
  coverage verification; Go mutation tables were not a phase-1 gate and
  must not be retroactively invented.

## Reviewer tasks

1. Confirm every merged PR from primary records (state, mergeCommit, both
   required check contexts on the merged heads).
2. Confirm issues #22-#29 all CLOSED with acceptance records.
3. Confirm the output inventory: the four protocol packages + schemas +
   codec + lifecycle validator + compatibility exist on main with their
   tests passing from a clean checkout.
4. Confirm the applicability matrix is consistent (phase-1 rows covered;
   phase-2 rows point at merged work).
5. Return PASS or REQUEST_CHANGES with numbered findings; PASS closes
   G.E02 (#79) and unblocks E03.
