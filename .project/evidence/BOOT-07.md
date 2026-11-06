# BOOT-07 — Finite reader completion

Status: READY_FOR_REVIEW. Date: 2026-11-06. Task: BOOT-07 (issue #7). Base: 1ab566cf402a84661729502685be284afb38ba73. Branch: boot/07-reader-completion. Decision: D87, delegated under D84. Independent acceptance is pending. Product runtime remains NOT_RUN.

## Result and boundary

TEST-OPEN-01 now has an explicit completion contract in [TESTING_CONTRACT](../docs/TESTING_CONTRACT.md), the canonical v3 specification, and TZ_EXECUTOR. The accepted native shutdown/drain and delivery seal protocols are unchanged. The test harness preserves independent source evidence, delivery evidence, and raw NATS evidence; closes the admitted source window; completes drain; closes only its isolated test streams; verifies the complete stream interval; reconstructs every record/batch; and compares the full independent expected set.

A public chain continues beyond the test window. Its later changes are outside that run, while stop races and admitted recovery work remain inside. Cutover preserves separate old/new destination evidence without copying acknowledged history. Reader completion, producer delivery, semantic correctness, and timeout are separate results. Declared negative tests prove their expected rejection without falsely claiming complete delivery.

The test-only ReaderBoundaryManifest records exact boundaries, evidence references, phase results, and deadlines. Existing CaseResult verdicts, product schemas/APIs, profile coverage, and REQ/AC/task IDs are unchanged. TEST-OPEN-02 remains BOOT-08 work. D86 still stops this launch before E01.

## Counterexample analysis

These are document-level checks of the proposed rules, not executed product tests.

- A source decision produces no batch because of a bug: the independent source-derived set disagrees with delivery manifests even if the producer reports a clean drain.
- One expected event is missing and one unrelated event replaces it: equal counts fail exact identity/value comparison.
- The last fragment or RecordSeal arrives while an earlier part is missing: reconstruction remains incomplete and fails by the deadline.
- A publish retry survives the NATS deduplication window: equal bytes with the same immutable part identity are allowed; a changed identity or payload fails.
- A consumer restarts and redelivers older messages after newer ones: stream sequence coverage and exact byte checks avoid double-counting without hiding holes.
- An admitted observation is rejected, or a native decision creates no public event: preserve its source checkpoint; do not invent a transport batch. A genuinely empty interval requires independent proof.
- A reorg spans the requested stop height: the actual terminal source frontier includes the whole committed decision and batch. A later reorg cannot rewrite this closed run.
- A finality-only decision has no next block, or a case still needs Polygon L1 evidence: include the status event and keep the scenario-specific evidence requirement.
- A process crashes before or after product SealAck: existing durable recovery finishes the obligation. A queued NATS write is included before the test stream's terminal seal or is recorded as rejected.
- An abandoned staging attempt leaves an event counter gap: the gap is legal. An unresolved allocated batch or missing JetStream sequence is not silently skipped.
- Retention, purge, recreation, or a narrow subject filter hides a message: the stream/configuration and complete interval checks prevent a false positive. Planned temporary rejection may pass only after required recovery.
- A Removed event follows cutover and references an Applied in the old destination: explicit old-destination/baseline evidence resolves the reference; no new history-copy guarantee is introduced.
- Transport finishes before the deadline but comparison does not: the started case fails at the comparison deadline. No late success is moved outside the measured run.
- A mutation intentionally causes incomplete delivery: only the declared negative behavior and forbidden-output checks can pass; reader completion remains unsatisfied.
- A retry succeeds after a first failed run: evidence stays separate and D83 preserves the first FAIL with UNSTABLE diagnostics.

## Verification

The following checks completed with exit code 0 in the isolated worktree:

- git diff --check.
- Canonical/executor comparison from the agreed-contract marker, with relative link targets normalized: full body matches.
- The five changed normative mirrors match their canonical sections: TESTING_CONTRACT, ACCEPTANCE_REQUIREMENTS, ACCEPTANCE_CASES, IMPLEMENTATION_GATES, and TASK_DECOMPOSITION.
- Exact acceptance-ID comparison against the base: 217 AC and 63 REQ, unchanged order, no duplicates. The unchanged issue catalog still contains 68 unique product child task IDs.
- All 84 Markdown files, including this new evidence file, pass 530 local-path and 491 heading-anchor checks with no missing targets.
- New prose contains no Cyrillic text or the checked British spelling variants; preserved protocol names and existing anchor targets are unchanged.
- Original v2 SHA256 remains ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634.
- The diff is limited to specification, glossary, status, and this evidence file. Agency, automation, CI, component inventory, source pins, and the original v2 have no changes.

The checks inspect documents; no node, reader, producer, Gateway, runtime test, performance run, or version-update process was executed by BOOT-07. No GitHub mutation, push, or merge was performed by this worker.

## Source check and remaining proof

Official [NATS stream documentation](https://github.com/nats-io/nats.docs/blob/master/nats-concepts/jetstream/streams.md) and the [server configuration source](https://github.com/nats-io/nats-server/blob/main/server/stream.go) support using a sealed stream and rejection limits for test closure. Official [consumer documentation](https://github.com/nats-io/nats.docs/blob/master/nats-concepts/jetstream/consumers.md) defines explicit acknowledgments and start-sequence delivery. These sources were consulted on 2026-11-06. They establish the design mechanism, not compatibility or successful execution with the selected NATS pin; E01 must verify that exact lock.

Independent review must challenge the source-ledger independence, complete admitted-window accounting, server-seal race, baseline/cutover references, negative-case semantics, and timing budget. BOOT-08 must provide the concrete independent corpus and quotas. The coordinator owns acceptance and serialized integration; a successful document review does not authorize E01 in this launch.
