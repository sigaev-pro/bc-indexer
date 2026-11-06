# Initial issue catalog

D85 language revision, 2026-11-06. Repository: [sigaev-pro/bc-indexer](https://github.com/sigaev-pro/bc-indexer). The backlog contains 84 issues: 9 bootstrap tasks, 68 implementation tasks, and 7 stage gates. Runtime results remain NOT_RUN until verified runs provide evidence.

Source requirements: [TASK_DECOMPOSITION](TASK_DECOMPOSITION.md) and [ACCEPTANCE_CASES](ACCEPTANCE_CASES.md). Roles and execution: [DEVELOPMENT_AUTOPILOT](../../DEVELOPMENT_AUTOPILOT.md). Published numbers: [ISSUE_MAP](ISSUE_MAP.md).

Use each stable task ID and its marker to reconcile GitHub issues. Do not create duplicates or change requirements when translating text. Expand ETH, BSC, and POLY into all applicable mainnet and testnet profiles. A COMMON implementation does not grant every profile a shared PASS. Tasks without a dedicated AC owner still require artifact acceptance in the source task catalog.

All project-authored text must use plain American English under [.project/AGENTS.md](../AGENTS.md). Technical IDs, field names, APIs, and source data retain their exact spelling.

## BOOT-01 — Connect the repository and publish the baseline

<!-- extractor-task:BOOT-01 -->

Stage: BOOT. Role: DevOps. Module and applicability: COMMON.

Dependencies: none.

Output: Verify the remote, branch, and history. Pin the specification commit and publish the baseline to an empty main branch without a force push. Add ignore rules for secrets, run output, state, and worktrees.

Acceptance: Verify the remote identity, check tracked files for secrets, preserve Git history and the original v2 specification, and confirm that rerunning the setup does not overwrite existing work.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## BOOT-02 — Adapt and pin the agency-agents roles

<!-- extractor-task:BOOT-02 -->

Stage: BOOT. Role: Coordinator / Technical Writer. Module and applicability: COMMON.

Dependencies: BOOT-01.

Output: Create project-local roles based on agency-agents commit b859c515d813efcfc4755d3471dd9e7686c7706d. Record source hashes and define scope, handoffs, allowed paths, and review duties.

Acceptance: Allow one writer per worktree and require an independent reviewer. Remove UI-only instructions and conflicting retry defaults. Verify role discovery or the explicit-prompt fallback. Do not install the full roster globally.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## BOOT-04 — Verify unattended access and runner readiness

<!-- extractor-task:BOOT-04 -->

Stage: BOOT. Role: DevOps. Module and applicability: COMMON.

Dependencies: BOOT-01.

Output: Check Codex, GitHub, Git, Docker, network access, resources, and secret handling. Report available capabilities and restrictions. Prepare the testnet signer separately.

Acceptance: Verify the required sandbox operations. Stop affected work when a managed restriction blocks it. Keep credentials out of Git and logs, and do not bypass security controls.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## BOOT-05 — Set up CI and independent acceptance checks

<!-- extractor-task:BOOT-05 -->

Stage: BOOT. Role: DevOps / Reviewer. Module and applicability: COMMON.

Dependencies: BOOT-01, BOOT-04.

Output: Define required checks for documentation, code, and test evidence on the exact commit. Set up a PR workflow that does not present one GitHub actor as two independent human reviewers.

Acceptance: Block merging after an unreviewed commit or a failed or missing check. Verify that the workflow is available in this repository. Do not bypass branch rules with admin access.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## BOOT-03 — Build and verify the persistent task controller

<!-- extractor-task:BOOT-03 -->

Stage: BOOT. Role: DevOps / Protocol Engineer. Module and applicability: COMMON.

Dependencies: BOOT-02, BOOT-04, BOOT-05.

Output: Implement .project/automation/controller.py with verify-bootstrap, run, resume, status, and stop commands; dependency scheduling; leases; persistent state; worker and reviewer sessions; and bounded attempts and budgets.

Acceptance: Use isolated failure tests to prove that exit code 0 without evidence is not DONE, crashes do not duplicate changes, stale leases and review or CI results are rejected, and stop or recovery preserves work. Use a fake GitHub backend so these tests do not create extra real issues.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## BOOT-06 — Check stable versions and build the component inventory

<!-- extractor-task:BOOT-06 -->

Stage: BOOT. Role: Architect / DevOps. Module and applicability: COMMON.

Dependencies: BOOT-01.

Output: Create a ComponentRegistry of stable release sources and an initial candidate lock for all native clients, supporting clients, toolchains, libraries, and images. Record compatibility risks for all seven profiles.

Acceptance: Use exact versions and digests, with no floating latest references. Check current releases against official sources. Unverified compatibility is not PASS. Full updater and build validation remains in E01 and E07.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## BOOT-07 — Resolve TEST-OPEN-01: define when NATS validation ends

<!-- extractor-task:BOOT-07 -->

Stage: BOOT. Role: Architect / QA. Module and applicability: COMMON.

Dependencies: BOOT-01, BOOT-02.

Output: Define exact boundaries and completion rules for the local and public-stream readers, including reorgs, incomplete parts, extra or missing records, and timeouts.

Acceptance: Review counterexamples independently. Record the decision within the accepted guarantees and update all specification copies. If the decision requires a scope change, report BLOCKED_DECISION instead of closing the issue.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## BOOT-08 — Resolve TEST-OPEN-02: define the test corpus and expected results

<!-- extractor-task:BOOT-08 -->

Stage: BOOT. Role: Native Engineer / QA. Module and applicability: COMMON.

Dependencies: BOOT-06, BOOT-07.

Output: Define named cases and variants for all seven profiles and their supported forks, contract scenarios, independent sources of expected results, minimum seed and soak quotas, and configurable RunPlans.

Acceptance: Assign every required variant. Do not derive expected results from the normalizer under test. Distinguish public and local runs. Limit a standard run to 1800 seconds, excluding preparation. Documentation alone is not runtime PASS.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## BOOT-09 — Accept bootstrap results and open E01

<!-- extractor-task:BOOT-09 -->

Stage: BOOT. Role: Reviewer. Module and applicability: COMMON.

Dependencies: BOOT-01, BOOT-02, BOOT-03, BOOT-04, BOOT-05, BOOT-06, BOOT-07, BOOT-08.

Output: Reconcile all 84 issues, their mappings, and their acyclic dependency graph. Verify accepted bootstrap results with verify-bootstrap.

Acceptance: Require accepted commits and evidence for every bootstrap dependency. Reject false DONE results. Open E01 only when the controller is ready and the open criteria are resolved.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## E01-01.evidence — Define the versioned test evidence schemas

<!-- extractor-task:E01-01.evidence -->

Stage: E01. Role: QA / Engineer. Module and applicability: `test/evidence`; COMMON.

Dependencies: BOOT-09.

Output: Define CaseRegistry, CaseResult, and RunManifest with the full D82 profile, fork, fixture, and suite identities. Keep applicability separate from verdicts. Version the evidence and represent NOT_RUN without fabricated actual results.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DEV-01, REQ-DEV-04. AC: AC-DEV-01-01, AC-DEV-04-02, AC-DEV-04-03.

Contracts: EVID. Inputs: SPEC provides the accepted requirements.

## E01-01.builds — Create isolated native and supporting client builds

<!-- extractor-task:E01-01.builds -->

Stage: E01. Role: DevOps. Module and applicability: `build/native`; ETH/BSC/POLY.

Dependencies: E01-01.evidence, BOOT-09.

Output: Create a separate build and artifact manifest for each client and supporting dependency. Pin sources, patches, compilers, and dependency locks. Do not combine internal types from different native clients in one dependency graph.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DEV-03, REQ-OPS-10. AC: AC-OPS-10-01, AC-OPS-10-03, AC-DEV-03-01.

Contracts: STACK, BOOT. Inputs: E01-01.evidence provides the manifest format.

## E01-01.harness — Build the reproducible native test runner

<!-- extractor-task:E01-01.harness -->

Stage: E01. Role: QA / Engineer. Module and applicability: `test/harness`; ETH/BSC/POLY.

Dependencies: E01-01.builds, E01-01.evidence, BOOT-09.

Output: Create a minimal reproducible runner with explicit inputs, state, test credentials, supporting services, and independent expected results. Do not depend on an undocumented node prepared by the author.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

Contracts: EVID, BOOT. Inputs: E01-01.builds provides pinned clients; E01-01.evidence provides the evidence schema.

## E01-02.pins — Verify native state retention and release

<!-- extractor-task:E01-02.pins -->

Stage: E01. Role: Native Engineer. Module and applicability: `adapters/<client>/state_provider`; ETH/BSC/POLY.

Dependencies: E01-01.harness, BOOT-09.

Output: Implement the hash-provider and pin interface. Keep the source root and required inputs available until release is allowed. Reject unsupported state backends.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-03, REQ-OPS-01, REQ-OPS-03. AC: AC-CAP-03-01, AC-CAP-03-02, AC-OPS-01-02, AC-OPS-01-03, AC-OPS-03-02.

Contracts: NATIVE, CONFIG. Inputs: E01-01.harness provides the runner and parent state.

## E01-02.inputs — Persist native inputs and consumed dependency responses

<!-- extractor-task:E01-02.inputs -->

Stage: E01. Role: Native Engineer. Module and applicability: `adapters/<client>/source_inputs`; ETH/BSC/POLY.

Dependencies: E01-02.pins, BOOT-09.

Output: Implement the static input capsule and append-only log of consumed dependencies. Persist each returned native execution response before allowing it to be consumed. Replay must use the saved response rather than a fresh replacement.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-04. AC: AC-CAP-04-01, AC-CAP-04-02, AC-CAP-04-03.

Contracts: NATIVE. Inputs: E01-02.pins defines state and input ownership.

## E01-02.owned_capture — Capture immutable data with bounded memory

<!-- extractor-task:E01-02.owned_capture -->

Stage: E01. Role: Protocol Engineer. Module and applicability: `core/capture`; ETH/BSC/POLY.

Dependencies: E01-01.harness, BOOT-09.

Output: Create immutable chunks and snapshots with clear ownership and bounded additional RAM. Wait at safe points outside callbacks. Do not pass mutable aliases of native state to the worker.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-05, REQ-CAP-06. AC: AC-CAP-05-01, AC-CAP-06-02.

Contracts: NATIVE. Inputs: E01-01.harness provides execution fixtures.

## E01-02.admission — Persist capture admission before publishing results

<!-- extractor-task:E01-02.admission -->

Stage: E01. Role: Native Engineer. Module and applicability: `adapters/<client>/admission`; ETH/BSC/POLY.

Dependencies: E01-02.inputs, E01-02.owned_capture, E01-02.pins, BOOT-09.

Output: Implement the guard, pin, and ADMITTED transition. Make the obligation recoverable after synchronized admission. Do not expose a public result before admission.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-02. AC: AC-CAP-02-01, AC-CAP-02-02.

Contracts: NATIVE. Inputs: E01-02.pins provides the pin contract; E01-02.inputs provides the capsule and dependency log; E01-02.owned_capture provides the ownership boundary.

## E01-02.decision_bridge — Recover native decisions and delivery obligations

<!-- extractor-task:E01-02.decision_bridge -->

Stage: E01. Role: Native Engineer. Module and applicability: `adapters/<client>/decision`; ETH/BSC/POLY.

Dependencies: E01-02.admission, BOOT-09.

Output: Implement the child-root, native-marker, and mirror interface. Recover the authoritative decision and its single delivery obligation. Rejected decisions and decisions without public events must not create a public batch.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-07. AC: AC-CAP-07-01, AC-CAP-07-02, AC-CAP-07-03, AC-CAP-07-06.

Contracts: NATIVE, DELIVERY. Inputs: E01-02.admission provides the admitted observation.

## E01-02.lifecycle_seams — Map all required native lifecycle callers

<!-- extractor-task:E01-02.lifecycle_seams -->

Stage: E01. Role: Native Engineer. Module and applicability: `adapters/<client>/lifecycle`; ETH/BSC/POLY.

Dependencies: E01-02.decision_bridge, BOOT-09.

Output: Map actual execution, system, canonical-chain, and finality callers, including events without a new block and known-branch paths. Every required caller must pass through the guard.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-01, REQ-LIF-05, REQ-LIF-06. AC: AC-CAP-01-01, AC-LIF-05-02, AC-LIF-05-03, AC-LIF-06-04, AC-LIF-06-05.

Contracts: NATIVE, SEM. Inputs: E01-02.decision_bridge provides the decision-marker interface.

## E01-03.smoke_corpus — Create the minimal independent native test corpus

<!-- extractor-task:E01-03.smoke_corpus -->

Stage: E01. Role: QA / Engineer. Module and applicability: `test/native_corpus`; ETH/BSC/POLY.

Dependencies: E01-01.harness, E01-02.decision_bridge, BOOT-09.

Output: Create transfer, nested-revert, fee, system-action, and empty-block cases with independent expected results. Do not treat this small corpus as full production semantic coverage.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

Contracts: NATIVE, SEM, TYPES. Inputs: E01-02.decision_bridge provides native execution output; E01-01.harness provides independent expected results.

## E01-04.early_faults — Test early capture and decision boundaries under power loss

<!-- extractor-task:E01-04.early_faults -->

Stage: E01. Role: QA / Engineer. Module and applicability: `test/native_faults`; ETH/BSC/POLY.

Dependencies: E01-02.lifecycle_seams, E01-03.smoke_corpus, BOOT-09.

Output: Collect evidence for admission, pins, EXEC_READY, native decisions, and the H boundary under power loss. Keep the full native-plus-Gateway failure matrix in E05.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-SCP-03. AC: AC-SCP-03-01.

Contracts: NATIVE, EVID. Inputs: E01-02.lifecycle_seams provides caller coverage; E01-03.smoke_corpus provides inputs and expected results.

## E01-05.type_paths — Verify every required native transaction path

<!-- extractor-task:E01-05.type_paths -->

Stage: E01. Role: Native Engineer. Module and applicability: `adapters/<client>/type_mapping`; ETH/BSC/POLY.

Dependencies: E01-03.smoke_corpus, BOOT-09.

Output: Build the matrix of active native transaction paths. Reject unknown types rather than skipping them. Include required BSC system transactions and Bor StateSync paths.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

Contracts: TYPES, SEM. Inputs: E01-03.smoke_corpus provides the native corpus.

## E02-01.semantic_descriptors — Define stable event and batch identities

<!-- extractor-task:E02-01.semantic_descriptors -->

Stage: E02. Role: Protocol Engineer. Module and applicability: `protocol/semantic`; ETH/BSC/POLY.

Dependencies: E01-02.decision_bridge, G.E01.

Output: Define semantic identities in the manifest. Keep activation, revision, and source stable across delivery attempts. Allocate the decision-to-batch mapping exactly once.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

Contracts: MSG, CONTROL, NATIVE. Inputs: E01-02.decision_bridge provides the decision interface.

## E02-05.actions_model — Define monetary actions, execution order, and rollback scopes

<!-- extractor-task:E02-05.actions_model -->

Stage: E02. Role: Protocol Engineer. Module and applicability: `protocol/balance`; ETH/BSC/POLY.

Dependencies: E01-03.smoke_corpus, G.E01.

Output: Define typed native monetary actions, shared execution_order, equality of causes, and RollbackScope references, trees, and validation. Do not count the receipt summary as a second monetary movement.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DAT-05. AC: AC-DAT-05-03.

Contracts: MSG, SEM. Inputs: E01-03.smoke_corpus provides reference effects.

## E02-08.block_metadata — Define block metadata and lifecycle messages

<!-- extractor-task:E02-08.block_metadata -->

Stage: E02. Role: Protocol Engineer. Module and applicability: `protocol/block`; ETH/BSC/POLY.

Dependencies: E01-05.type_paths, G.E01.

Output: Define block metadata and lifecycle schemas, including empty blocks. Do not treat native source counts as proof of complete capture.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

Contracts: MSG, SEM. Inputs: E01-05.type_paths provides profile-specific native fields.

## E02-10.closed_profiles — Define the closed payload and extension registry

<!-- extractor-task:E02-10.closed_profiles -->

Stage: E02. Role: Protocol Engineer. Module and applicability: `protocol/profiles`; ETH/BSC/POLY.

Dependencies: E01-05.type_paths, G.E01.

Output: Define payload variants, extensions, field presence, exact widths, and applicability. Include D75 receipt kinds, native_type, origins, and network details. Require the D80 sources_binding_digest:Hash32 in SourceObservationRef and L1Checkpoint. Do not use an ANY fallback for unsupported versions.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DAT-02, REQ-DAT-09, REQ-LIF-07. AC: AC-DAT-02-03, AC-DAT-09-03, AC-LIF-07-07.

Contracts: TYPES, SEM, MSG. Inputs: E01-05.type_paths provides source variants.

## E02-02.schemas — Generate reproducible schemas, bindings, and validators

<!-- extractor-task:E02-02.schemas -->

Stage: E02. Role: Protocol Engineer. Module and applicability: `protocol/proto`; COMMON.

Dependencies: E02-01.semantic_descriptors, E02-05.actions_model, E02-08.block_metadata, E02-10.closed_profiles, G.E01.

Output: Generate reproducible schemas and bindings. Define structural validation for required field presence, oneof fields, lengths, and unknown values.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DAT-01. AC: AC-DAT-01-01, AC-DAT-01-02.

Contracts: MSG, CONTROL. Inputs: E02-01.semantic_descriptors provides identities; E02-05.actions_model provides action schemas; E02-08.block_metadata provides block schemas; E02-10.closed_profiles provides variants.

## E02-02.codec — Implement bounded fragment encoding and exact transport bytes

<!-- extractor-task:E02-02.codec -->

Stage: E02. Role: Protocol Engineer. Module and applicability: `protocol/codec`; COMMON.

Dependencies: E02-02.schemas, G.E01.

Output: Implement the typed RecordFragment, tree, and blob codec with bounded memory, streaming manifests, and exact transport layout, headers, and digests. Do not serialize a complete Envelope as one message. Split large individual fields as well.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-04. AC: AC-DLV-04-01, AC-DLV-04-02, AC-DLV-04-04, AC-DLV-04-05, AC-DLV-04-06, AC-DLV-04-07, AC-DLV-04-08, AC-DLV-04-09, AC-DLV-04-12.

Contracts: CONTROL, DELIVERY. Inputs: E02-02.schemas provides typed messages and validators.

## E02-03.lifecycle_validator — Validate event order, revisions, and block reinclusion

<!-- extractor-task:E02-03.lifecycle_validator -->

Stage: E02. Role: QA / Engineer. Module and applicability: `test/lifecycle_validator`; ETH/BSC/POLY.

Dependencies: E02-01.semantic_descriptors, E02-02.schemas, G.E01.

Output: Build the test lifecycle state machine. Define unambiguous results for reordered Applied, Removed, and Status events, revision conflicts, and reinclusion. Do not create a production consumer.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-LIF-03. AC: AC-LIF-03-01, AC-LIF-03-02, AC-LIF-03-03.

Contracts: MSG, SEM. Inputs: E02-02.schemas provides lifecycle messages; E02-01.semantic_descriptors provides identity invariants.

## E02-04.compatibility — Define read, replay, and write compatibility checks

<!-- extractor-task:E02-04.compatibility -->

Stage: E02. Role: Protocol Engineer. Module and applicability: `protocol/compatibility`; COMMON.

Dependencies: E02-02.codec, G.E01.

Output: Define the read, replay, and new-write capability matrix. Support immutable pending data or reject the transition. A completed drain does not make an incompatible format of retained metadata acceptable.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

Contracts: CONTROL, DELIVERY, CONFIG. Inputs: E02-02.codec provides encoding versions.

## E03-01.producer_store — Implement the durable producer state machine

<!-- extractor-task:E03-01.producer_store -->

Stage: E03. Role: Protocol Engineer. Module and applicability: `core/journal/producer`; COMMON.

Dependencies: E02-01.semantic_descriptors, E02-02.codec, G.E02.

Output: Implement INTENT, STAGING, READY, SEALED, ACKED_CLEANUP, and outbox states. Make closing commits atomic. A delivery attempt must not change semantic identity.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-08, REQ-DLV-02. AC: AC-CAP-08-01, AC-DLV-02-01, AC-DLV-02-02, AC-DLV-02-03, AC-DLV-02-04, AC-DLV-02-05.

Contracts: DELIVERY, CONTROL. Inputs: E02-02.codec provides immutable bytes; E02-01.semantic_descriptors provides descriptors.

## E03-01.gateway_store — Implement the durable Gateway state machine

<!-- extractor-task:E03-01.gateway_store -->

Stage: E03. Role: Protocol Engineer. Module and applicability: `gateway/journal`; COMMON.

Dependencies: E03-01.producer_store, G.E02.

Output: Implement PREPARING, reservation, RECEIVING, CLOSED, ACKED, and RELEASING states. Retrying a reservation must not allocate a new sequence.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-03. AC: AC-DLV-03-04.

Contracts: DELIVERY, CONTROL. Inputs: E03-01.producer_store provides the batch and plan interface.

## E03-02.rpc_streaming — Stream bounded RPC chunks and validate sealed imports

<!-- extractor-task:E03-02.rpc_streaming -->

Stage: E03. Role: Protocol Engineer. Module and applicability: `gateway/ingest`; COMMON.

Dependencies: E03-01.gateway_store, E03-01.producer_store, G.E02.

Output: Implement bounded chunk RPC and IMPORT_SEALED. Validate offsets, conflicts, and manifests before entering CLOSED.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-03. AC: AC-DLV-03-01, AC-DLV-03-02.

Contracts: CONTROL, DELIVERY. Inputs: E03-01.gateway_store provides Gateway transitions; E03-01.producer_store provides producer transitions.

## E03-02.fencing — Reject stale sessions, RPCs, and callbacks

<!-- extractor-task:E03-02.fencing -->

Stage: E03. Role: Protocol Engineer. Module and applicability: `gateway/session`; COMMON.

Dependencies: E03-02.rpc_streaming, G.E02.

Output: Implement session, admission, and publisher fencing. Stale RPCs and callbacks must not create state after a barrier or Release.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-08, REQ-OPS-05. AC: AC-DLV-08-01, AC-DLV-08-02, AC-OPS-05-01.

Contracts: CONTROL, DELIVERY. Inputs: E03-02.rpc_streaming provides the mutating RPC boundary.

## E03-03.destination — Validate the bound NATS destination before publishing

<!-- extractor-task:E03-03.destination -->

Stage: E03. Role: Protocol Engineer. Module and applicability: `gateway/destination`; COMMON.

Dependencies: E02-02.codec, E03-02.fencing, G.E02.

Output: Bind the destination before creating the publication plan. Validate the live account, stream, routing, and exact D77 metadata. Missing or mismatched settings must not redirect pending data.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-09, REQ-OPS-06. AC: AC-DLV-09-01, AC-DLV-09-02, AC-DLV-09-03, AC-DLV-09-04, AC-DLV-09-05, AC-OPS-06-01, AC-OPS-06-03.

Contracts: CONTROL, CONFIG, BOOT. Inputs: E03-02.fencing provides the authenticated owner; E02-02.codec provides the binding digest.

## E03-03.publisher — Persist full-batch NATS acknowledgments

<!-- extractor-task:E03-03.publisher -->

Stage: E03. Role: Protocol Engineer. Module and applicability: `gateway/publisher`; COMMON.

Dependencies: E03-02.rpc_streaming, E03-03.destination, G.E02.

Output: Implement the sequence from NATS PubAck to durable NATS_ACKED and then IngestAck for one manifest, plan, and destination. A partial acknowledgment must not complete a batch.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-04, REQ-DLV-05. AC: AC-DLV-04-03, AC-DLV-04-10, AC-DLV-05-01, AC-DLV-05-02, AC-DLV-05-03.

Contracts: DELIVERY, CONTROL, BOOT. Inputs: E03-03.destination provides the verified destination; E03-02.rpc_streaming provides CLOSED frames.

## E03-01.cleanup — Release acknowledged data in a recoverable order

<!-- extractor-task:E03-01.cleanup -->

Stage: E03. Role: Protocol Engineer. Module and applicability: `core/delivery/release`; COMMON.

Dependencies: E03-02.fencing, E03-03.publisher, G.E02.

Output: Implement ordered Release and RecoveryRelease, retired baselines on both sides, and bounded RELEASING cleanup. Recovery after Gateway loss must not require payloads that were already acknowledged.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-06. AC: AC-DLV-06-01, AC-DLV-06-02, AC-DLV-06-03, AC-DLV-06-04.

Contracts: DELIVERY, CONTROL. Inputs: E03-03.publisher provides IngestAck; E03-02.fencing provides the barrier.

## E03-03.maintenance — Implement destination changes and maintenance recovery

<!-- extractor-task:E03-03.maintenance -->

Stage: E03. Role: Protocol Engineer. Module and applicability: `core/delivery/maintenance`; COMMON.

Dependencies: E03-01.cleanup, E03-02.fencing, E03-03.destination, G.E02.

Output: Implement Seal, Switch, and Resume, D77 destination metadata checks, and an idempotent committed receipt during a NATS outage. Test MAINTENANCE recovery from a producer already given as quiesced. Proving native quiescence is outside this task.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-09, REQ-DLV-11. AC: AC-DLV-09-06, AC-DLV-09-07, AC-DLV-09-08, AC-DLV-11-02, AC-DLV-11-04, AC-DLV-11-05, AC-DLV-11-06, AC-DLV-11-07.

Contracts: DELIVERY, CONTROL. Inputs: E03-01.cleanup provides an empty delivery frontier; E03-03.destination provides the binding; E03-02.fencing provides the session barrier.

## E03-04.pressure_errors — Handle backpressure and delivery failures

<!-- extractor-task:E03-04.pressure_errors -->

Stage: E03. Role: Protocol Engineer. Module and applicability: `core/delivery/errors`; COMMON.

Dependencies: E03-01.cleanup, E03-03.publisher, G.E02.

Output: Define transient, configuration, poison-record, and ENOSPC behavior with bounded queues. Pending data has no TTL or quota. Distinguish fatal local failures from remote outages.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-13. AC: AC-DLV-13-01, AC-DLV-13-02, AC-DLV-13-04, AC-DLV-13-05.

Contracts: DELIVERY, CONFIG. Inputs: E03-03.publisher provides retry points; E03-01.cleanup provides cleanup points.

## E03-05.recovery_matrix — Verify delivery recovery under failures

<!-- extractor-task:E03-05.recovery_matrix -->

Stage: E03. Role: QA / Engineer. Module and applicability: `test/delivery_faults`; COMMON.

Dependencies: E02-04.compatibility, E03-03.maintenance, E03-04.pressure_errors, G.E02.

Output: Collect delivery-only failure evidence from a given durable INTENT or READY state. Test Gateway loss, late RPCs, and partial cleanup. Do not claim full REC-T01 coverage.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-01, REQ-DLV-03, REQ-DLV-04, REQ-DLV-07, REQ-DLV-14. AC: AC-DLV-01-01, AC-DLV-01-02, AC-DLV-03-03, AC-DLV-04-11, AC-DLV-07-01, AC-DLV-07-02, AC-DLV-07-04, AC-DLV-14-01.

Contracts: DELIVERY, CONTROL, EVID. Inputs: E03-03.maintenance provides the transition state machine; E03-04.pressure_errors provides error states; E02-04.compatibility provides the capability matrix.

## E03-11.block_lifecycle — Verify block and lifecycle delivery through NATS

<!-- extractor-task:E03-11.block_lifecycle -->

Stage: E03. Role: QA / Engineer. Module and applicability: `test/nats_smoke`; ETH/BSC/POLY.

Dependencies: E01-03.smoke_corpus, E02-03.lifecycle_validator, E03-03.publisher, G.E02.

Output: Run a NATS smoke corpus for blocks, transactions, lifecycle events, and empty blocks, including parts and PubAck. This is not a verifier for the entire blockchain.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

Contracts: MSG, DELIVERY, EVID. Inputs: E03-03.publisher provides the publication path; E01-03.smoke_corpus provides native inputs; E02-03.lifecycle_validator provides expected lifecycle results.

## E04-01.calls — Capture complete calls, execution order, and rollback results

<!-- extractor-task:E04-01.calls -->

Stage: E04. Role: Protocol Engineer. Module and applicability: `core/normalization/calls`; ETH/BSC/POLY.

Dependencies: E01-05.type_paths, E02-02.schemas, E02-05.actions_model, G.E03.

Output: Capture the complete call tree, shared order with actions, native scope mapping, and outcome and committed fields. Apply D81: distinguish an attempted address and a locally successful created_address from final account existence. A successful child call does not survive an ancestor rollback.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DAT-04, REQ-DAT-05. AC: AC-DAT-04-01, AC-DAT-04-02, AC-DAT-04-03, AC-DAT-05-01, AC-DAT-05-02, AC-DAT-05-04, AC-DAT-05-05.

Contracts: MSG, SEM. Inputs: E02-02.schemas provides call variants; E02-05.actions_model provides order and scopes; E01-05.type_paths provides native paths.

## E04-01.receipts — Preserve exact native receipt bytes and projections

<!-- extractor-task:E04-01.receipts -->

Stage: E04. Role: Native Engineer. Module and applicability: `adapters/<client>/receipts`; ETH/BSC/POLY.

Dependencies: E01-05.type_paths, E02-02.codec, E02-10.closed_profiles, G.E03.

Output: Implement the native consensus and storage receipt registry, the D76 bounded writer with byte-identical output, and log, contract-creation, and network-detail projections. Do not substitute derived values for source values or the reverse.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-09, REQ-DAT-02, REQ-DAT-09. AC: AC-CAP-09-01, AC-DAT-02-01, AC-DAT-02-02, AC-DAT-02-04, AC-DAT-02-05, AC-DAT-09-01, AC-DAT-09-02, AC-DAT-09-04.

Contracts: MSG, SEM, TYPES. Inputs: E01-05.type_paths provides receipt origins; E02-10.closed_profiles provides the receipt registry; E02-02.codec provides D73 fragments and disk spooling.

## E04-01.authorization — Preserve every source authorization tuple

<!-- extractor-task:E04-01.authorization -->

Stage: E04. Role: Protocol Engineer. Module and applicability: `core/normalization/authorization`; ETH/BSC/POLY according to native applicability.

Dependencies: E02-10.closed_profiles, E04-01.receipts, G.E03.

Output: Preserve source authorization tuples, including invalid and duplicate tuples. Validation of the outer transaction must not filter them.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DAT-03. AC: AC-DAT-03-01.

Contracts: TYPES, SEM. Inputs: E04-01.receipts provides native outcomes; E02-10.closed_profiles provides source field widths.

## E04-01.block_record — Project exact native block metadata

<!-- extractor-task:E04-01.block_record -->

Stage: E04. Role: Protocol Engineer. Module and applicability: `core/normalization/block`; ETH/BSC/POLY.

Dependencies: E01-05.type_paths, E02-08.block_metadata, G.E03.

Output: Implement BlockApplied from native data with exact headers, RLP, counts, optional field presence, and source timestamp precision for both empty and nonempty blocks.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DAT-10. AC: AC-DAT-10-01.

Contracts: MSG, SEM. Inputs: E02-08.block_metadata provides the typed block schema; E01-05.type_paths provides source header variants.

## E04-03.state_diff — Capture exact transaction-boundary state changes

<!-- extractor-task:E04-03.state_diff -->

Stage: E04. Role: Protocol Engineer. Module and applicability: `core/normalization/state`; ETH/BSC/POLY.

Dependencies: E01-02.owned_capture, E04-01.calls, G.E03.

Output: Capture account existence, balance, nonce, code, and storage before and after the transaction. Handle rollbacks, empty accounts, and resets correctly.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-05, REQ-DAT-07. AC: AC-CAP-05-02, AC-DAT-07-01, AC-DAT-07-02.

Contracts: MSG, SEM. Inputs: E04-01.calls provides scopes; E01-02.owned_capture provides immutable snapshots.

## E04-01.selfdestruct — Separate SELFDESTRUCT actions, transfers, and deletion

<!-- extractor-task:E04-01.selfdestruct -->

Stage: E04. Role: Protocol Engineer. Module and applicability: `core/normalization/selfdestruct`; ETH/BSC/POLY.

Dependencies: E04-01.calls, E04-03.state_diff, G.E03.

Output: Keep the SELFDESTRUCT action, monetary movement, and account deletion separate. Cover zero value, self-beneficiary, revert, and fork-specific cases.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DAT-06. AC: AC-DAT-06-01, AC-DAT-06-02, AC-DAT-06-03, AC-DAT-06-04.

Contracts: SEM, MSG. Inputs: E04-03.state_diff provides boundary state changes; E04-01.calls provides call scopes.

## E04-03.balance_actions — Capture all monetary actions with stable causes and IDs

<!-- extractor-task:E04-03.balance_actions -->

Stage: E04. Role: Protocol Engineer. Module and applicability: `core/normalization/balance`; ETH/BSC/POLY.

Dependencies: E02-05.actions_model, E04-01.receipts, E04-03.state_diff, G.E03.

Output: Capture monetary movements with their causes, including fee purchases, refunds, and recipient credits. Preserve native scopes and stable order and IDs during capture recovery. Keep actions whose net effect is zero, and do not count summary amounts twice.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DAT-05, REQ-DAT-08. AC: AC-DAT-05-06, AC-DAT-05-07, AC-DAT-08-01, AC-DAT-08-02, AC-DAT-08-03.

Contracts: SEM, MSG. Inputs: E04-03.state_diff provides account deltas; E04-01.receipts provides fee summaries; E02-05.actions_model provides the action schema.

## E04-04.import_pressure — Verify capture under large inputs and slow workers

<!-- extractor-task:E04-04.import_pressure -->

Stage: E04. Role: QA / Engineer. Module and applicability: `test/native_import`; ETH/BSC/POLY.

Dependencies: E01-02.owned_capture, E04-01.block_record, E04-01.selfdestruct, E04-03.balance_actions, G.E03.

Output: Test the full import path with large fields and slow workers. Native execution results must stay unchanged, collector memory must stay bounded, and system scopes must remain distinct.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-06, REQ-SCP-04. AC: AC-SCP-04-01, AC-SCP-04-02, AC-CAP-06-01.

Contracts: NATIVE, SEM, EVID. Inputs: E04-03.balance_actions provides complete effects; E04-01.selfdestruct provides action semantics; E04-01.block_record provides full block projections; E01-02.owned_capture provides the safe-point interface.

## E04-02.optional_scope — Declare and test optional capture scopes

<!-- extractor-task:E04-02.optional_scope -->

Stage: E04. Role: Protocol Engineer. Module and applicability: `protocol/optional_scope`; ETH/BSC/POLY.

Dependencies: E04-03.balance_actions, G.E03.

Output: Verify the default OFF setting without losing required native transaction effects. Enable outside-transaction or legacy records only with explicitly declared typed fixtures.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-SCP-05. AC: AC-SCP-05-01, AC-SCP-05-02.

Contracts: MSG, TYPES, SEM. Inputs: E04-03.balance_actions defines the required transaction scope.

## E05-01.registry — Persist activation, revision, and native decision state

<!-- extractor-task:E05-01.registry -->

Stage: E05. Role: Native Engineer. Module and applicability: `adapters/<client>/lifecycle_registry`; ETH/BSC/POLY.

Dependencies: E01-02.lifecycle_seams, E02-03.lifecycle_validator, G.E04.

Output: Implement the durable activation and revision registry and the native decision mirror. Speculative capture data must not become Applied. A READY Applied event must remain reversible.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-07, REQ-LIF-01. AC: AC-CAP-07-04, AC-CAP-07-05, AC-LIF-01-01, AC-LIF-01-02.

Contracts: NATIVE, MSG. Inputs: E02-03.lifecycle_validator provides the semantic test oracle; E01-02.lifecycle_seams provides native callers.

## E05-01.eth_finality — Capture authoritative Ethereum finality observations

<!-- extractor-task:E05-01.eth_finality -->

Stage: E05. Role: Native Engineer. Module and applicability: `adapters/geth/finality`; ETH.

Dependencies: E01-02.lifecycle_seams, E05-01.registry, G.E04.

Output: Capture typed native safe and finalized observations. Cover setters, restarts, and changes without a new block. Use only authoritative finalized ancestry as the GC anchor.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-LIF-05. AC: AC-LIF-05-01, AC-LIF-05-04.

Contracts: SEM, NATIVE. Inputs: E05-01.registry provides activation references; E01-02.lifecycle_seams provides native setter paths.

## E05-01.bsc_finality — Separate early and header-attested BSC finality

<!-- extractor-task:E05-01.bsc_finality -->

Stage: E05. Role: Native Engineer. Module and applicability: `adapters/bsc/finality`; BSC.

Dependencies: E01-02.lifecycle_seams, E05-01.registry, G.E04.

Output: Keep early VotePool evidence separate from finality attested in block headers. Allow GC only from the header Source defined by the contract.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-LIF-06. AC: AC-LIF-06-01, AC-LIF-06-02, AC-LIF-06-03.

Contracts: SEM, NATIVE. Inputs: E05-01.registry provides activation references; E01-02.lifecycle_seams provides native finality paths.

## E05-01.polygon_observer — Observe Polygon status and finalized L1 checkpoints

<!-- extractor-task:E05-01.polygon_observer -->

Stage: E05. Role: Native Engineer. Module and applicability: `adapters/bor/finality_observer`; POLY.

Dependencies: E01-01.harness, E05-01.registry, G.E04.

Output: Capture native Polygon statuses and checkpoint evidence confirmed by finalized L1 state. Bind the D80 sources_binding_digest to each observation and preserve it during replay and configuration replacement. Transaction and status publication must not wait for the GC source. A source outage must not invent an anchor.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-LIF-07. AC: AC-LIF-07-01, AC-LIF-07-02, AC-LIF-07-03, AC-LIF-07-04, AC-LIF-07-05, AC-LIF-07-06, AC-LIF-07-08, AC-LIF-07-09.

Contracts: SEM, NATIVE, BOOT. Inputs: E05-01.registry provides activation references; E01-01.harness provides dedicated Polygon L1 fixture endpoints.

## E05-01.activation_gc — Release recovery state only after a valid GC decision

<!-- extractor-task:E05-01.activation_gc -->

Stage: E05. Role: Native Engineer. Module and applicability: `adapters/<client>/recovery_gc`; ETH/BSC/POLY.

Dependencies: E03-01.cleanup, E05-01.bsc_finality, E05-01.eth_finality, E05-01.polygon_observer, G.E04.

Output: Base GC decisions on durable terminal evidence, an accepted anchor, ancestry, and the absence of pending work. Keep pins when no anchor is available. A proven regression below an accepted anchor is fatal.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-LIF-08. AC: AC-LIF-08-01, AC-LIF-08-02, AC-LIF-08-03, AC-LIF-08-04, AC-LIF-08-05.

Contracts: SEM, NATIVE. Inputs: E05-01.eth_finality, E05-01.bsc_finality, and E05-01.polygon_observer provide network-specific evidence; E03-01.cleanup provides delivery status.

## E05-02.reorg — Verify reorgs, reapplication, and reordered lifecycle events

<!-- extractor-task:E05-02.reorg -->

Stage: E05. Role: QA / Engineer. Module and applicability: `test/native_lifecycle`; ETH/BSC/POLY.

Dependencies: E05-01.activation_gc, E05-01.registry, G.E04.

Output: Collect evidence for reorgs, reapplication, known branches, and reordered events. Create Removed for a READY Applied event regardless of whether PubAck has arrived.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-08, REQ-LIF-02. AC: AC-CAP-08-02, AC-LIF-02-01.

Contracts: NATIVE, MSG, SEM. Inputs: E05-01.registry provides the registry; E05-01.activation_gc provides retained ancestry.

## E05-03.drain — Drain native obligations without admitting new work

<!-- extractor-task:E05-03.drain -->

Stage: E05. Role: Native Engineer. Module and applicability: `adapters/<client>/maintenance`; ETH/BSC/POLY.

Dependencies: E03-03.maintenance, E05-02.reorg, G.E04.

Output: Implement native RECOVERY_ONLY DRAIN. Complete all obligations before the seal and high-water mark. Pins retained for later GC are allowed. Do not admit new work or start new polling.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-10, REQ-DLV-11, REQ-DLV-12. AC: AC-DLV-10-01, AC-DLV-10-02, AC-DLV-10-03, AC-DLV-10-04, AC-DLV-11-03, AC-DLV-12-01.

Contracts: NATIVE, DELIVERY, CONTROL, CONFIG. Inputs: E05-02.reorg provides pending native transitions; E03-03.maintenance provides delivery drain and destination switching.

## E05-03.upgrade — Verify drain, replay, and upgrade compatibility

<!-- extractor-task:E05-03.upgrade -->

Stage: E05. Role: QA / Engineer. Module and applicability: `test/compatibility_recovery`; ETH/BSC/POLY.

Dependencies: E02-04.compatibility, E05-03.drain, G.E04.

Output: Test old-build drain, compatible replay, and rollback behavior with retained lifecycle metadata. Reject incompatible persistent formats before any write.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-14. AC: AC-DLV-14-02.

Contracts: DELIVERY, CONFIG, NATIVE. Inputs: E05-03.drain provides a completed drain; E02-04.compatibility provides read and write rules.

## E05-04.concurrent_fencing — Prevent duplicate actions and revived state during races

<!-- extractor-task:E05-04.concurrent_fencing -->

Stage: E05. Role: QA / Engineer. Module and applicability: `test/native_concurrency`; ETH/BSC/POLY.

Dependencies: E03-02.fencing, E05-02.reorg, E05-03.drain, G.E04.

Output: Test races involving native decisions, old workers, sessions, and late PubAck responses. These races must not create a second action or revive released state.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-11. AC: AC-DLV-11-08.

Contracts: NATIVE, DELIVERY, CONTROL. Inputs: E05-03.drain provides the mode barrier; E05-02.reorg provides lifecycle races; E03-02.fencing provides transport fencing.

## E05-03.full_crash_matrix — Verify the complete native-to-NATS recovery path

<!-- extractor-task:E05-03.full_crash_matrix -->

Stage: E05. Role: QA / Engineer. Module and applicability: `test/composed_faults`; ETH/BSC/POLY.

Dependencies: E03-05.recovery_matrix, E05-03.upgrade, E05-04.concurrent_fencing, G.E04.

Output: Prove recovery across native capture, WAL, Gateway, and NATS for crashes, reorgs, finality changes, and restarts, including cleanup and GC. Only this combined test provides full REC-T01 evidence.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-CAP-10, REQ-DLV-07, REQ-DLV-13, REQ-LIF-04, REQ-SCP-03. AC: AC-SCP-03-02, AC-CAP-10-01, AC-LIF-04-01, AC-LIF-04-02, AC-DLV-07-03, AC-DLV-13-03.

Contracts: NATIVE, DELIVERY, SEM, EVID. Inputs: E05-04.concurrent_fencing provides race evidence; E05-03.upgrade provides version recovery; E03-05.recovery_matrix provides delivery-only evidence.

## E06-07.yaml — Validate strict role and native configuration

<!-- extractor-task:E06-07.yaml -->

Stage: E06. Role: DevOps. Module and applicability: `config`; ETH/BSC/POLY.

Dependencies: E02-02.codec, E05-03.drain, G.E05.

Output: Validate strict role configuration and effective native settings before startup. Keep config check free of side effects. Resolve storage paths relative to the YAML file; require an absolute Polygon sources_file path. Accept only declared keys, profiles, and modes. Treat binding_file as the desired destination descriptor.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-OPS-01, REQ-SCP-01. AC: AC-SCP-01-01, AC-SCP-01-02, AC-OPS-01-01, AC-OPS-01-04, AC-OPS-01-05.

Contracts: CONFIG, CONTROL. Inputs: E02-02.codec provides the descriptor parser; E05-03.drain provides the mode contract.

## E06-13.artifact_lock — Pin and validate deployment artifacts

<!-- extractor-task:E06-13.artifact_lock -->

Stage: E06. Role: DevOps. Module and applicability: `deploy/artifacts`; ETH/BSC/POLY.

Dependencies: E01-01.builds, E06-07.yaml, G.E05.

Output: Create the user-facing artifact manifest with source, patch, toolchain, image, and genesis identities. Reject incorrect artifacts before initialization.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

Contracts: BOOT, STACK. Inputs: E01-01.builds provides build manifests; E06-07.yaml provides profile identity.

## E06-13.init — Initialize native clients safely and repeatably

<!-- extractor-task:E06-13.init -->

Stage: E06. Role: DevOps. Module and applicability: `deploy/init`; ETH/BSC/POLY.

Dependencies: E06-07.yaml, E06-13.artifact_lock, G.E05.

Output: Implement idempotent native initialization with explicit phases. Repeated or partial initialization must preserve keys and state and verify genesis and ENV_READY.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-OPS-03. AC: AC-OPS-03-01, AC-OPS-03-03.

Contracts: BOOT. Inputs: E06-13.artifact_lock provides verified artifacts; E06-07.yaml provides paths and profiles.

## E06-13.topology — Verify service topology and storage ownership

<!-- extractor-task:E06-13.topology -->

Stage: E06. Role: DevOps. Module and applicability: `deploy/compose+kubernetes`; ETH/BSC/POLY.

Dependencies: E06-07.yaml, E06-13.init, G.E05.

Output: Verify the 11-service, 14-volume mainnet baseline and each backing-store owner. Isolate adapters, Gateways, and the dedicated Polygon L1 components. Apply the accepted profile expansion rules when adding testnets.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-OPS-02. AC: AC-OPS-02-01, AC-OPS-02-02.

Contracts: TOPO, BOOT. Inputs: E06-13.init provides initialized native homes; E06-07.yaml defines role-specific stores.

## E06-13.secrets — Configure trust, credentials, and secret rotation

<!-- extractor-task:E06-13.secrets -->

Stage: E06. Role: DevOps. Module and applicability: `deploy/trust`; ETH/BSC/POLY.

Dependencies: E06-07.yaml, G.E05.

Output: Implement CA, SAN, EKU, role, and network admission checks; private keys, JWT secrets, credentials, and rotation. Keep bootstrap admin credentials out of the Gateway.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-OPS-05. AC: AC-OPS-05-02, AC-OPS-05-03.

Contracts: BOOT, CONFIG. Inputs: E06-07.yaml defines connection roles.

## E06-13.nats_bootstrap — Create and verify NATS accounts, streams, and permissions

<!-- extractor-task:E06-13.nats_bootstrap -->

Stage: E06. Role: DevOps. Module and applicability: `deploy/nats`; COMMON.

Dependencies: E03-03.destination, E06-13.secrets, G.E05.

Output: Create or verify accounts, streams, ACLs, and D77 identity metadata. Read back created settings, reject adoption when required identity keys are missing, and preserve unrelated keys during compatible updates. Runtime components must not receive admin or reset permissions.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-OPS-06. AC: AC-OPS-06-02, AC-OPS-06-04, AC-OPS-06-05, AC-OPS-06-06.

Contracts: BOOT, CONTROL. Inputs: E06-13.secrets provides trust material; E03-03.destination provides the expected binding.

## E06-13.network — Verify ports, advertised addresses, and internal connections

<!-- extractor-task:E06-13.network -->

Stage: E06. Role: DevOps. Module and applicability: `deploy/network`; ETH/BSC/POLY.

Dependencies: E06-13.secrets, E06-13.topology, G.E05.

Output: Verify port mappings, advertised addresses, and internal links, including the Bor observer connection to its dedicated supporting L1. Expose only P2P by default. Reject localhost as a cross-container destination and reject the wrong L1 identity.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-OPS-07. AC: AC-OPS-07-01, AC-OPS-07-02, AC-OPS-07-03.

Contracts: PORTS, BOOT, TOPO. Inputs: E06-13.topology defines services; E06-13.secrets provides authenticated connections.

## E06-08.config_replace — Replace configuration without overwriting persistent state

<!-- extractor-task:E06-08.config_replace -->

Stage: E06. Role: DevOps. Module and applicability: `deploy/config_generation`; ETH/BSC/POLY.

Dependencies: E06-07.yaml, E06-13.init, G.E05.

Output: Allow configuration replacement to change only allowlisted files and apply after restart. Do not overwrite immutable state, keys, or genesis data.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-OPS-04. AC: AC-OPS-04-01, AC-OPS-04-02.

Contracts: BOOT, CONFIG. Inputs: E06-13.init provides initialization state; E06-07.yaml provides validated desired configuration.

## E06-08.maintenance — Verify the complete maintenance procedure

<!-- extractor-task:E06-08.maintenance -->

Stage: E06. Role: QA / Engineer. Module and applicability: `test/user_maintenance`; ETH/BSC/POLY.

Dependencies: E05-03.upgrade, E06-08.config_replace, E06-13.nats_bootstrap, E06-13.network, G.E05.

Output: Provide an executable user procedure for stop-confirm-start, drain, destination switching, Resume, and upgrades. Cover shared-volume and delayed-writer failures.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DLV-11, REQ-DLV-14, REQ-OPS-08. AC: AC-DLV-11-01, AC-DLV-14-03, AC-OPS-08-02.

Contracts: BOOT, CONFIG, DELIVERY, CONTROL. Inputs: E06-08.config_replace provides configuration generations; E06-13.network provides reachable services; E06-13.nats_bootstrap provides the destination; E05-03.upgrade provides version rules.

## E06-09.live_start — Start live capture and recover pending delivery independently

<!-- extractor-task:E06-09.live_start -->

Stage: E06. Role: QA / Engineer. Module and applicability: `test/user_startup`; ETH/BSC/POLY.

Dependencies: E06-08.config_replace, E06-13.nats_bootstrap, E06-13.network, G.E05.

Output: Verify clean startup and synchronization followed by capture at H+1. Recover existing pending work independently. A NATS outage must not prevent safe capture, and restarting one network must not require restarting the others.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-OPS-08. AC: AC-OPS-08-01.

Contracts: CONFIG, NATIVE, BOOT. Inputs: E06-13.network provides connections; E06-13.nats_bootstrap provides NATS; E06-08.config_replace provides effective configuration.

## E06-12.logs — Report control states and failures without exposing secrets

<!-- extractor-task:E06-12.logs -->

Stage: E06. Role: DevOps. Module and applicability: `config/control_logs`; ETH/BSC/POLY.

Dependencies: E06-08.maintenance, E06-09.live_start, G.E05.

Output: Emit required structured control states and errors with debug logging OFF. Keep secrets out of logs. A SIGKILL termination must not be reported as a graceful STOPPED state.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-OPS-09. AC: AC-OPS-09-01, AC-OPS-09-02.

Contracts: BOOT, CONFIG, DELIVERY. Inputs: E06-09.live_start provides startup states; E06-08.maintenance provides maintenance and error states.

## E06-13.clean_bootstrap — Reproduce the full deployment from a clean environment

<!-- extractor-task:E06-13.clean_bootstrap -->

Stage: E06. Role: QA / Engineer. Module and applicability: `test/deployment`; ETH/BSC/POLY.

Dependencies: E06-12.logs, E06-13.artifact_lock, E06-13.topology, G.E05.

Output: Collect evidence that the complete user deployment can be reproduced from a clean environment. The early test harness does not replace this deployment.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-OPS-10. AC: AC-OPS-10-02.

Contracts: BOOT, TOPO, EVID. Inputs: E06-12.logs provides diagnostic states; E06-13.artifact_lock provides artifacts; E06-13.topology provides the ownership map.

## E07-02.e2e — Verify end-to-end correctness and failure recovery

<!-- extractor-task:E07-02.e2e -->

Stage: E07. Role: QA / Engineer. Module and applicability: `test/acceptance`; ETH/BSC/POLY.

Dependencies: E05-03.full_crash_matrix, E06-13.clean_bootstrap, G.E06.

Output: Produce end-to-end native correctness and failure verdicts for every network profile using independent expected results. Source inspection alone is not runtime PASS.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DEV-02. AC: AC-DEV-02-01, AC-DEV-02-02, AC-DEV-02-03, AC-DEV-02-04, AC-DEV-02-05, AC-DEV-02-06, AC-DEV-02-07, AC-DEV-02-08, AC-DEV-02-09, AC-DEV-02-10.

Contracts: EVID, MSG, SEM. Inputs: E05-03.full_crash_matrix provides combined inputs and evidence; E06-13.clean_bootstrap provides the user environment.

## E07-02.scope — Verify the product boundary from native capture to NATS

<!-- extractor-task:E07-02.scope -->

Stage: E07. Role: QA / Engineer. Module and applicability: `test/scope_audit`; ETH/BSC/POLY.

Dependencies: E07-02.e2e, G.E06.

Output: Trace the product boundary from native capture to NATS. Confirm that no additional downstream, high-availability, history, or performance obligations have been introduced. Keep optional scope explicit.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-SCP-02, REQ-SCP-06. AC: AC-SCP-02-01, AC-SCP-06-01.

Contracts: EVID, MSG, CONFIG. Inputs: E07-02.e2e provides the observed product path.

## E07-03.runbook — Verify operating and recovery instructions

<!-- extractor-task:E07-03.runbook -->

Stage: E07. Role: Protocol Engineer. Module and applicability: `docs/runbook`; ETH/BSC/POLY.

Dependencies: E06-08.maintenance, E06-12.logs, E07-02.e2e, G.E06.

Output: Verify startup, shutdown, diagnostics, and recovery drills against actual system states. Commands must not depend on an undocumented environment.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

Contracts: BOOT, CONFIG, DELIVERY. Inputs: E07-02.e2e provides the accepted path; E06-12.logs provides observable states; E06-08.maintenance provides operating procedures.

## E07-08.isolation — Verify independent network configuration and operation

<!-- extractor-task:E07-08.isolation -->

Stage: E07. Role: QA / Engineer. Module and applicability: `test/network_isolation`; ETH/BSC/POLY.

Dependencies: E07-02.e2e, G.E06.

Output: Test adding, configuring, stopping, and restarting one network without changing another network's writers or stores. Sharing NATS must not imply sharing a journal.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

Contracts: TOPO, DELIVERY, EVID. Inputs: E07-02.e2e provides the running acceptance topology.

## E07-09.nats_acceptance — Validate the final records received from NATS

<!-- extractor-task:E07-09.nats_acceptance -->

Stage: E07. Role: QA / Engineer. Module and applicability: `test/nats_reader`; ETH/BSC/POLY.

Dependencies: E07-02.e2e, E07-02.scope, G.E06.

Output: Validate final NATS fields, parts, lifecycle events, empty blocks, allowed duplicates, and PubAck evidence against native expected results. Do not create a production consumer.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

Contracts: MSG, CONTROL, EVID. Inputs: E07-02.e2e provides native expected results; E07-02.scope defines the product boundary.

## E07-09.requirement_verdict — Check complete acceptance coverage for every required variant

<!-- extractor-task:E07-09.requirement_verdict -->

Stage: E07. Role: QA / Engineer. Module and applicability: `test/evidence`; COMMON.

Dependencies: E07-03.runbook, E07-08.isolation, E07-09.nats_acceptance, G.E06.

Output: Reconcile CaseRegistry and CaseResult using full D82 identities and individual run outcomes. A missing required result, FAIL, or NOT_RUN must block the gate. Every exception requires an accepted rule.

Acceptance: Verify the output and invariant against the source task, its assigned ACs, and the artifact acceptance criteria in TASK_DECOMPOSITION. Require independent review of the exact commit and PASS for every required check. Mainnet and testnet profiles do not replace one another.

REQ: REQ-DEV-04, REQ-DEV-05. AC: AC-DEV-04-01, AC-DEV-04-04, AC-DEV-04-05, AC-DEV-04-06, AC-DEV-04-07, AC-DEV-04-08, AC-DEV-05-01, AC-DEV-05-02, AC-DEV-05-03, AC-DEV-05-04, AC-DEV-05-05, AC-DEV-05-06.

Contracts: EVID. Inputs: E07-09.nats_acceptance provides output evidence; E07-08.isolation provides isolation evidence; E07-03.runbook provides operating evidence.

## G.E01 — Accept E01: native source and early evidence

<!-- extractor-task:G.E01 -->

Stage: E01. Role: Reviewer / QA. Module and applicability: ALL: all seven profiles.

Dependencies: E01-01.evidence, E01-01.builds, E01-01.harness, E01-02.pins, E01-02.inputs, E01-02.owned_capture, E01-02.admission, E01-02.decision_bridge, E01-02.lifecycle_seams, E01-03.smoke_corpus, E01-04.early_faults, E01-05.type_paths.

Output: Accept all required outputs separately for ETH, BSC, and POLY profiles. This gate covers early native integration boundaries; it does not accept transport, full semantics, or the combined failure matrix.

Acceptance: Every child task must be accepted, and the current stage's profile and variant matrix must be complete. Evidence must match the actual pinned versions and commits. Do not relabel partial evidence as full proof. Keep the next stage blocked until this gate passes.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## G.E02 — Accept E02: schemas, identities, and encoding

<!-- extractor-task:G.E02 -->

Stage: E02. Role: Reviewer / QA. Module and applicability: ALL: all seven profiles.

Dependencies: E02-01.semantic_descriptors, E02-05.actions_model, E02-08.block_metadata, E02-10.closed_profiles, E02-02.schemas, E02-02.codec, E02-03.lifecycle_validator, E02-04.compatibility.

Output: Accept all required schemas and local contract cases as defined and verifiable. E04 extends native semantic correctness; E03 verifies delivery through real components.

Acceptance: Every child task must be accepted, and the current stage's profile and variant matrix must be complete. Evidence must match the actual pinned versions and commits. Do not relabel partial evidence as full proof. Keep the next stage blocked until this gate passes.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## G.E03 — Accept E03: durable delivery through NATS

<!-- extractor-task:G.E03 -->

Stage: E03. Role: Reviewer / QA. Module and applicability: ALL: all seven profiles.

Dependencies: E03-01.producer_store, E03-01.gateway_store, E03-02.rpc_streaming, E03-02.fencing, E03-03.destination, E03-03.publisher, E03-01.cleanup, E03-03.maintenance, E03-04.pressure_errors, E03-05.recovery_matrix, E03-11.block_lifecycle.

Output: Accept producer, Gateway, and NATS delivery guarantees from a given durable input. Native admission was checked separately in E01. Combined reorg, finality, and capture-crash evidence remains in E05.

Acceptance: Every child task must be accepted, and the current stage's profile and variant matrix must be complete. Evidence must match the actual pinned versions and commits. Do not relabel partial evidence as full proof. Keep the next stage blocked until this gate passes.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## G.E04 — Accept E04: required native semantics

<!-- extractor-task:G.E04 -->

Stage: E04. Role: Reviewer / QA. Module and applicability: ALL: all seven profiles.

Dependencies: E04-01.calls, E04-01.receipts, E04-01.authorization, E04-01.block_record, E04-03.state_diff, E04-01.selfdestruct, E04-03.balance_actions, E04-04.import_pressure, E04-02.optional_scope.

Output: Accept all required semantic cases for the current network profiles. The default OFF case is required; enabled optional cases are conditional. This gate does not establish historical coverage or performance guarantees.

Acceptance: Every child task must be accepted, and the current stage's profile and variant matrix must be complete. Evidence must match the actual pinned versions and commits. Do not relabel partial evidence as full proof. Keep the next stage blocked until this gate passes.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## G.E05 — Accept E05: lifecycle and recovery integration

<!-- extractor-task:G.E05 -->

Stage: E05. Role: Reviewer / QA. Module and applicability: ALL: all seven profiles.

Dependencies: E05-01.registry, E05-01.eth_finality, E05-01.bsc_finality, E05-01.polygon_observer, E05-01.activation_gc, E05-02.reorg, E05-03.drain, E05-03.upgrade, E05-04.concurrent_fencing, E05-03.full_crash_matrix.

Output: Accept the combined guarantees for each network profile. Keep the early caller evidence from E01 and the delivery-only evidence from E03 separate; do not relabel either as full integration evidence.

Acceptance: Every child task must be accepted, and the current stage's profile and variant matrix must be complete. Evidence must match the actual pinned versions and commits. Do not relabel partial evidence as full proof. Keep the next stage blocked until this gate passes.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## G.E06 — Accept E06: deployment and operating procedures

<!-- extractor-task:G.E06 -->

Stage: E06. Role: Reviewer / QA. Module and applicability: ALL: all seven profiles.

Dependencies: E06-07.yaml, E06-13.artifact_lock, E06-13.init, E06-13.topology, E06-13.secrets, E06-13.nats_bootstrap, E06-13.network, E06-08.config_replace, E06-08.maintenance, E06-09.live_start, E06-12.logs, E06-13.clean_bootstrap.

Output: Accept the complete deployment against required BOOT, CONFIG, MOUNT, TRUST, NATS, START, REPLACE, LOG, and ARTIFACT cases. This gate does not cover public release packaging or a production performance methodology.

Acceptance: Every child task must be accepted, and the current stage's profile and variant matrix must be complete. Evidence must match the actual pinned versions and commits. Do not relabel partial evidence as full proof. Keep the next stage blocked until this gate passes.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## G.E07 — Accept E07: complete product acceptance

<!-- extractor-task:G.E07 -->

Stage: E07. Role: Reviewer / QA. Module and applicability: ALL: all seven profiles.

Dependencies: E07-02.e2e, E07-02.scope, E07-03.runbook, E07-08.isolation, E07-09.nats_acceptance, E07-09.requirement_verdict.

Output: Accept every required AC according to its applicability and verify that the complete result matrix is consistent. Execute gates in order under D84. Runtime results remain NOT_RUN until actual runs provide evidence.

Acceptance: Every child task must be accepted, and the current stage's profile and variant matrix must be complete. Evidence must match the actual pinned versions and commits. Do not relabel partial evidence as full proof. Keep the next stage blocked until this gate passes.

REQ: bootstrap, artifact, or gate criteria. AC: artifact or gate criteria; no dedicated AC owner does not mean no verification.

## Publication status

All 84 issues are published as #1 through #84. [ISSUE_MAP](ISSUE_MAP.md) lists their actual URLs. All were open when the catalog was created. DONE is determined by the workflow and evidence, not by this document. Before publishing again, scan every page of open and closed issues for stable markers.
