# AUT-04: Continuous work in meaningful blocks

Issue: [#101](https://github.com/sigaev-pro/bc-indexer/issues/101). Accepted base: `a7ea9a2db265f5cf9879650509e351fa3aae819f`. Owner decision: [D93](../docs/DECISIONS.md). Status: local checks passed; independent review, protected acceptance, and canonical adoption are pending.

## Delivered behavior

- D93 removes campaign windows, invocation ceilings, and ordinary review-repair caps. The existing task graph continues by dependency order; each block retains its result, checks, review, commit, and next action.
- Explicit adoption preserves the D92 run ID, receipts, tasks, attempts, failures, and source snapshots. It appends authorization/configuration history and forks the accounting ledger with a predecessor digest. The D92 ledger becomes read-only. Adoption requires a safe boundary, current preflight, accepted source, and actual completed bootstrap review observations.
- Ledger preparation and atomic run replacement recover across either crash boundary. A prepared ledger cannot dispatch before adoption. Conflicting state, pending operations, unfinished candidates, active delegated sessions, foreign/unreleased leases, and stale revisions are rejected. Adoption preserves stop markers and does not start work.
- Model sessions, commands, build verification, process cleanup, and resource limits remain finite. All build-proof executions require finite authorized deadlines; exact D92/D93 policies are checked. D83's 1,800-second standard runtime test limit and failure rules remain unchanged.
- Repeated review failure on an unchanged commit blocks that task for diagnosis. Changed candidates may continue beyond the old repair count. Counts and failed reviews remain recorded. Existing exact-head review, independent verification, protected merge, and receipt checks remain required.

## Verification

`python3 .project/automation/ci.py` passed locally with 278 tests and 54 rejected component mutations. After the final documentation updates, `check_docs.py` passed with 103 Markdown documents, 1,084 local links, the unchanged 84-issue catalog, 16 normative mirrors, and unchanged original v2 bytes; all six agency-role tests passed. Diff checks passed. This is portable bootstrap verification, not product acceptance. GitHub CI must verify the final candidate.

New regression coverage includes expired-D92 adoption, receipt/history/stop preservation, crashes before and after run replacement, a conflicting prepared ledger, blocked premature dispatch, session capacity, counts beyond 200 invocations and three attempts, changed-candidate progress, unchanged-candidate diagnosis, accepted-recipe unblocking, invalid technical timeouts, real descendant cleanup, finite worker/verifier requests, and finite/exact-authority build proof parsing.

Earlier local runs failed on legacy synthetic fixtures missing explicit authorization, the old two-policy registry expectation, and a synthetic build policy no longer accepted by the stricter parser. Those fixtures now name reviewed policies explicitly. One new negative test initially attempted to serialize infinity before reaching the verifier; the test now checks the verifier's rejection directly. These failures were test-development results, not product runtime evidence.

## Remaining work

Obtain independent Standards and Spec reviews of the exact candidate, complete GitHub CI and protected acceptance, then adopt D93 using actual bootstrap observations. Preserve the canonical old ledger and accepted receipt bytes. Continue with E01-01.builds after adoption. Native builds and native/NATS runtime remain `NOT_RUN`.
