# BOOT-07 — Finite reader completion

Status: READY_FOR_REVIEW. Date: 2026-11-06. Task: BOOT-07 (issue #7). Base: 1ab566cf402a84661729502685be284afb38ba73. Branch: boot/07-reader-completion. Decision: D87, delegated under D84. Independent acceptance is pending. Product runtime remains NOT_RUN.

## Result and boundary

TEST-OPEN-01 now has an explicit completion contract in [TESTING_CONTRACT](../docs/TESTING_CONTRACT.md), the canonical v3 specification, and TZ_EXECUTOR. The accepted native shutdown/drain and delivery seal protocols are unchanged. The test harness preserves independent source evidence, delivery evidence, an exhaustive publication-attempt census, and raw NATS evidence; closes the admitted source window; completes drain; establishes a verified terminal fence for its isolated NATS topology; verifies the complete stream interval; reconstructs every record/batch; and compares the full independent expected set. A stream seal acknowledgment alone does not establish that fence.

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
- A process crashes before or after product SealAck: existing durable recovery finishes the obligation. A queued NATS write may outlive the test stream's seal response; the terminal fence must include any surviving committed append before U is sampled.
- A clustered request pauses after reading the unsealed flag but before proposal: a seal response cannot authorize U. The cold restart ends old handlers and requires surviving log work to finish applying.
- A client times out or another retry receives PubAck: the earlier attempt stays unknown. The attempt census and terminal fence resolve its effect without inferring server completion from a client outcome.
- An exact retry is rejected solely by the fence: it is diagnostic only if the same immutable part already has durable delivery and stored-byte proof. A first part, altered bytes, or extra identity fails.
- An old process survives, a leader changes during collection, or a message appears after U: invalidate the fence. No quiet delay or moving upper bound may hide the race.
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

Official [NATS stream documentation](https://github.com/nats-io/nats.docs/blob/master/nats-concepts/jetstream/streams.md) describes sealing and retention; it does not establish a barrier for all pending clustered writes. Official [consumer documentation](https://github.com/nats-io/nats.docs/blob/master/nats-concepts/jetstream/consumers.md) defines explicit acknowledgments and start-sequence delivery. These sources were consulted on 2026-11-06. They establish available mechanisms, not successful execution with the selected NATS pin.

Independent review of candidate 3c691d75517a291ed1ba2fc4d1e5a9cc42aa4f9f identified a source-supported barrier proof gap. At NATS commit 5a815f11a911efa2c88fd1dbfb18ed89a449bfc1, [jetstream_cluster.go](https://github.com/nats-io/nats-server/blob/5a815f11a911efa2c88fd1dbfb18ed89a449bfc1/server/jetstream_cluster.go#L13080) reads the sealed flag under a lock, releases it, and later proposes the message. [stream.go](https://github.com/nats-io/nats-server/blob/5a815f11a911efa2c88fd1dbfb18ed89a449bfc1/server/stream.go#L6559) skips the standalone consistency check during clustered apply. Therefore the former seal-only explanation was not a proved terminal barrier. This is a code-path finding, not a reproduced NATS bug.

The revised default fence stops all old NATS processes, preserves the same stores and topology, restarts with every stream-producing input disabled, and waits for source-verified native recovery. Clustered closure additionally requires recovered stream Raft frontiers and queues from every configured voter. Candidate fields are exposed by [monitor.go](https://github.com/nats-io/nats-server/blob/5a815f11a911efa2c88fd1dbfb18ed89a449bfc1/server/monitor.go#L4233). The harness must verify the complete apply path and reproduce the delayed-proposal/apply/ack/leadership counterexamples for each enabled exact lock and topology before using this fence. Missing observations reject preparation; a deadline during a started fence is FAIL. Runtime conformance remains NOT_RUN. No active NATS source pin was changed.

Independent review must challenge the source-ledger independence, complete admitted-window accounting, terminal-fence proof and attempt census, baseline/cutover references, negative-case semantics, and timing budget. BOOT-08 must provide the concrete independent corpus and quotas. The coordinator owns acceptance and serialized integration; a successful document review does not authorize E01 in this launch.
