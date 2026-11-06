# Concrete test corpus

D90 specifies TEST-OPEN-02 within D83/D84. BOOT-08 accepts a definition after independent review. It does not execute the product or accept a client version. Every product result remains NOT_RUN.

The normative inputs are [TESTING_CONTRACT](../../docs/TESTING_CONTRACT.md), [ACCEPTANCE_CASES](../../docs/ACCEPTANCE_CASES.md), [TASK_DECOMPOSITION](../../docs/TASK_DECOMPOSITION.md), and the accepted [candidate registry](../../bootstrap/components/README.md). The original REQ, AC, task, and gate IDs remain authoritative.

## Files and commands

- [catalog.json](catalog.json) assigns 677 named baseline variants to all 217 ACs, adds concrete scenario recipes, oracle contracts, size boundaries, quotas, and E01–E07 artifact owners.
- [candidate-sources.json](candidate-sources.json) records the seven candidate profile identities, 270 explicit execution-fork declarations including omitted/null native fields, consensus schedules, native type declarations, precompile tables/selection rules, CAS20 routes, and candidate deltas. Every source reference has its repository, exact commit, path, and Git blob SHA. It is a source projection, not a ProfileManifest.
- [verify.py](verify.py) checks source/AC bindings, preserves original task owners, and deterministically expands obligations. [test_corpus.py](test_corpus.py) tests omissions, invalid claims, boundaries, allocations, and quota failures.

Run from the repository root:

~~~sh
python3 .project/testing/corpus/verify.py
python3 .project/testing/corpus/verify.py --expand
python3 .project/testing/corpus/verify.py --check-plan path/to/campaign.json
python3 -m unittest discover -s .project/testing/corpus -p 'test_*.py'
~~~

The default command reports definition validity, never runtime PASS. Expansion prints separate NOT_RUN obligations. A checked campaign reports COMPLETE or PARTIAL allocation and always reports ready=false. This validator does not implement E01 fixture readiness, E07 result aggregation, or the runtime runner. The normal CI discovers its unit tests automatically.

## Assignment and expansion

A cell ID is the slash-separated tuple AC/recipe/variant/profile/suite/mode/dimension. It is a test identity, not a product field. A baseline variant inherits the complete existing AC Given/When/Then predicate; its name identifies one required subcase. Scenario recipes add explicit stimuli and assertions. They do not replace the AC. Family-only and optional variants have explicit profile/condition selectors. DOC cells use COMMON; they cannot close a per-profile runtime cell.

Expansion creates separate obligations for current local E2E, historical/future native seams, native transaction declarations, precompile map/address/implementation entries, all five CAS20 routes and 159 routed methods, source fork boundaries, size boundaries, topologies, and deterministic pairwise rows. There are 105 distinct CAS20 selector declarations because Asset and Stablecoin share methods. Every route/method still gets its own obligation.

The source groups are finite symbolic inputs to resolution, not invented runtime interval hashes. Equal activation values are grouped only within the same clock domain. Block height, timestamp, total difficulty, and consensus epoch remain distinct. E01/E06 must derive ordered reachable native feature intervals and link every source group/current/genesis obligation to its exact predicates and interval ID. Equivalent obligations may share a fixture or run, but each cell retains its own assertion and evidence reference. They cannot disappear from the matrix.

For a nonzero activation, test the last valid native point before activation, activation itself, the next valid point, and restart across the transition. For timestamps/epochs, use valid native headers/slots on each side, not an invalid timestamp invented by subtracting one. Genesis has at/after plus a separate invalid-negative boundary check; it has no native block -1. Disabled forks require absence and stale-activation rejection checks. A source declaration alone does not activate a native type or public feature.

Native type cells resolve body admission, pool admission, execution behavior, and receipt encoding separately. A mandatory system body transaction is exercised through its native system entrypoint, never forged through an ordinary signer. For unsupported/preactivation types, the source-defined rejection or inapplicability proof is explicit. A public profile, helper precompile table, or a generic Ethereum type declaration cannot imply support by analogy.

Precompile cells bind the ordered native map selector, map implementation/settings, and applicable input/gas rules. Helper-only, aliased, stripped non-Parlia, disabled, or unreachable maps require a reviewed source applicability proof. Their obligations remain visible; they do not force impossible public transactions. CAS20 cells also resolve feature activation, read-only behavior, permissions, and state initialization independently of Jenner routing. A STATICCALL to a read method may succeed; the generic variant never mandates an incorrect uniform rejection.

The catalog is a coverage definition, not tens of thousands of separate process launches. Pack compatible obligations into finite prepared runs, share immutable vectors, and compare the exact distinct assertions. A campaign may use multiple standard runs. It may not remove cells to fit one 30-minute run.

## Resolution and fixture admission

E01-01.evidence implements the resolved CaseRegistry/RunPlan/manifest schemas. E01-01.harness and the assigned native task implement fixture builders, driver actions, oracle adapters, and source applicability. E06 provides the exact seven ProfileManifests and prepared profile environments. E07 executes the complete applicable matrix. The baseline AC owner and original early gate remain unchanged; adding E07 coverage does not defer an E01 obligation.

Before READY, each expanded cell needs:

1. Its exact source-profile/fork predicates and expected positive, negative, or justified inapplicable outcome. A source or accepted optional-scope proof explains every inapplicability; no unknown required path becomes N/A.
2. An immutable fixture identity with actual genesis hash, full native config digest, state format, client/supporting pins, validators and consensus parameters, external response fixtures, retained inputs, and reproduction command.
3. An independently prepared oracle with source provenance, expected identities/values or a precise public observation predicate, and a review reference. The normalizer under test cannot generate its own expected records.
4. Exact compiler/source/build artifacts when contracts are used, plus the resource, signer, and deadline proof needed to run the case.

All hashes in those runtime artifacts must be computed from real outputs. The source catalog deliberately keeps ProfileManifest, genesis, and compiled-fixture hashes PENDING_E01_E06. Missing inputs block preparation with NOT_RUN. After scenario execution starts, a missing required observation or incomplete phase fails at its finite deadline.

A private consensus chain with owned validators, custom genesis, or shifted activation times has a test-only identity. Its fixture records the actual genesis/config/validator/supporting-service identities and the evidence connecting its native semantics to the public profile/fork obligation. Never reuse a public manifest digest or disable production identity checks. Local E2E is valid only through legitimate supported admission and real required processes. If that admission cannot be established, use an honestly labeled native-seam fixture and retain the required full E2E/public cells as unresolved. A seam or custom chain never claims to be the public network.

Historical intervals may need direct native execution/import seams because the current client cannot run an old consensus topology. The harness must demonstrate that the seam executes the pinned native path and preserves its semantics. It cannot claim historical full-process E2E without the real path and required components. This adds no historical-export feature to the product.

## Contract and oracle recipes

Controlled contracts cover ERC-20 transfer/approve/transferFrom with finite/infinite allowance, zero/boundary/self transfers and failures; ERC-721 receiver success/failure; ERC-1155 single/batch receiver success/failure; calls, CALLCODE, DELEGATECALL, STATICCALL, recursion/depth; caught/uncaught revert and out-of-gas; CREATE/CREATE2 and creation failures; repeated storage, code/nonce changes, SELFDESTRUCT, transient storage and authorizations where applicable. The catalog names these variants and their expected state/call/receipt/rollback assertions. It introduces no business token decoder.

Pin compiler version, commit and binary digest, language, complete settings, EVM target, source and linked-library digests, initcode/runtime bytecode digests, and the reproduction command. Match the target and emitted opcodes/initcode rules to each interval. Use a compatible older compiler or independently reviewed hand bytecode when needed. A modern compiler emitting an unsupported opcode must not cause a false historical extractor failure.

Use original native block/transaction bytes and consensus receipt codec bytes, independent native state commitments/proofs, and a separately authored action ledger or reference executor. The independent model derives action order, rollback scopes, effects and final state from the contract source and input, not the extractor's records. Cross-check independent views; native receipt/log counts alone do not prove trace completeness. Expected model/code is reviewed separately from the normalizer.

Preparation can deploy/fund contracts and build expected fixtures. It cannot execute scenario actions on behalf of the SUT or count preparation receipts as runtime results. CREATE, fees, rollback and fault actions execute inside the measured run.

Public smoke has a narrower semantic oracle and a complete inventory. Independently preserve every admitted native block/body transaction and lifecycle decision in the closed D87 window; derive all required logical event identities, including reorg and finality revisions, from that ledger. Assert the declared profile/source identities, order/references, original headers/transactions/receipts, and complete framing/delivery. Record internal trace/state fields that smoke does not independently assert. A complete public inventory is not full trace-semantic proof. The separate local/native trace matrix remains mandatory.

A controlled public-contract variant that claims deeper trace/state semantics requires a reviewed independent ledger or replay using the actual block prestate and environment. Preserve that evidence and complete its comparison inside the measured budget. Missing deep evidence is not replaced by receipt success.

Mainnet smoke is passive. Public testnet contracts require verified owned deployment/code, exact chain identity, funded accounts, nonce coordination, a spending limit, and testnet-only signing before READY. They cannot force public reorg/fork/governance paths. Privileged CAS20 activation/policy and system paths use controlled native fixtures; public source observations do not imply signer authority. Optional known-token fixtures, including USDT, are additive and bind exact network/address/block/transaction inputs and amounts.

Synthetic wire/size mutations and streamed records over 2 GiB remain synthetic evidence. Compare streaming lengths/digests, independent golden vectors, resource use, and exact reassembly. They never prove that a real block admitted a huge receipt or that the native capture path handled it. Native size-pressure cases are separate and use valid native limits.

## Faults, combinations, and resources

Every baseline durable-boundary fault remains assigned to its existing AC/task. Distinguish process crash from OS-cache/power loss. The reader mutations include all D87 counterexamples: whole-batch omission, equal-count identity replacement, fragment gaps, retries/conflicts, stop-race and later reorgs, finality-only/L1 evidence, retention/recreation/filtering, pending publication/commit/apply after seal, unknown timeout, surviving process/leader change, append after U, exact fence-rejected retry versus first part, and late comparison. Positive controls prevent an always-failing verifier from passing.

The five required compound sequences remain explicit: reorg+restart; partial publish+lost ACK+Gateway restart; backlog+drain; fork transition+restart; profile isolation+one supporting service unavailable. Resolve exact injection points before READY. If a profile has no separate service for a selected supporting-service fault, retain a source/topology applicability proof and run the applicable own-component/isolation cells; do not pretend a shared NATS outage is an independent-profile fault.

The pairwise factors are restart {none, adapter, Gateway}, delivery {online, outage, lost ACK}, branch {stable, reorg}, payload {small, fragmented}, and support {available, unavailable}. The deterministic greedy expansion covers every pair of factor values per profile/current interval. A forbidden native combination needs a recorded source constraint and independent review. Removing a pair without that proof fails allocation. Pairwise rows supplement the five mandatory combinations and seeded exploration; they do not promise exhaustive unbounded state space.

Initial engineering floors per profile/current interval:

- Eight fixed seed indices, each with at least 256 scenario actions. Resolve each actual seed from the plan digest plus index, freeze the sequence, and record every action/injection. Increased seeds or actions are allowed. Minimize and retain every counterexample.
- A standard soak observes at least 600 seconds and 1,000 actions. Both floors apply. Extended soak observes at least 3,600 seconds and 10,000 actions and is explicitly selected as extended.
- A backlog recovery case reaches at least 32 durable, unacknowledged delivery batches and at least max(64 MiB, 2 × configured collector queue capacity in bytes) of exact pending publication bytes. Declare the batch/part identities and byte count from the immutable plan. Stop NATS or Gateways long enough to prove this durable backlog was reached; elapsed outage time alone is insufficient. Verify complete drain and comparison afterward.

These are starting engineering quotas, not measured throughput or production SLOs. Volumes, payload sizes, timeouts, storage and parallelism are configurable. Increased settings retain every mandatory variant. Reduced diagnostic plans report PARTIAL and preserve omitted cells as NOT_RUN; they cannot close acceptance.

Resources must reserve native state, both owner journals, NATS retention, oracle/reader spools and raw evidence, with filesystem headroom; pending bytes are not the total disk requirement. Separate native RAM from collector, Gateway, reader and queue budgets. E01 preparation computes exact plan demands and rejects an infeasible resource/deadline combination before READY. The static validator only checks declared limits; it does not measure feasibility.

A standard parent run is at most 1,800 wall-clock seconds across all selected profiles, including scenario restarts/faults, shutdown/drain, terminal fence, reconstruction and comparison. Images, state/fixture construction, public sync, contract deployment and funding are preparation. Their timestamps/deadlines are separate. No scenario action moves into preparation to satisfy the limit. Extended soak is a separate explicit run; the campaign may need several standard runs.

D83 permits one clean diagnostic retry after FAIL, with the same plan/versions/seed and separate run identity. It cannot erase FAIL, merge output windows, lower quotas, or become acceptance merely by passing. Corrected runs retain the earlier history.

## Campaign allocation format

The optional static allocation checker accepts schema_version=1, catalog_sha256 from the default validator, purpose=acceptance or diagnostic, profiles, suites, runs, and runtime=NOT_RUN. Each run has run_id, suite, profiles, cells (expanded cell IDs), duration_mode, total_seconds, phase_seconds, resources, preparation_in_measured_run=false, and runtime=NOT_RUN.

phase_seconds has positive startup, scenario, shutdown_drain, stream_fence and reader_compare budgets whose sum fits total_seconds. resources has positive cpu_count, ram_bytes, disk_bytes, queue_bytes and parallelism. Public runs add a signing map for each profile: none or testnet_only. Soak runs add seed_count/actions_per_seed/soak_seconds/soak_actions quotas. Backlog runs add pending_batches, pending_bytes, and plan_exact_bytes_ref. A soak campaign supplies per-profile pairwise rows and excluded_pairs; each exclusion carries its exact pair, source_proof_ref and review_ref.

All selected profile/suite cells must be allocated for an acceptance campaign; a diagnostic campaign retains missing_cells. COMPLETE allocation means every definition obligation has a run assignment. It does not mean the run is prepared, the applicability is resolved, or the product passed. E01 owns the stricter resolved runtime RunPlan and real artifact validation.

## Source findings and invalidation

The candidate lock binds Geth 1df30b18d9ed35ee9d28ffd4648442c2204554ad, Prysm 96d418cd964b083e37519a9df274030a1beac7db, BSC 54afaff4da31ac9e49ba646a4f65104edac5a2f3 and Bor 3a018e38467594508ca31ebeaf804837fb92834a. These are candidates, not new active pins.

- [Geth Sepolia](https://github.com/ethereum/go-ethereum/blob/1df30b18d9ed35ee9d28ffd4648442c2204554ad/params/chainspecs/sepolia.json) configures Amsterdam; [Hoodi](https://github.com/ethereum/go-ethereum/blob/1df30b18d9ed35ee9d28ffd4648442c2204554ad/params/chainspecs/hoodi.json) does not. [Prysm Sepolia](https://github.com/OffchainLabs/prysm/blob/96d418cd964b083e37519a9df274030a1beac7db/config/params/testnet_sepolia_config.go) sets Gloas epoch 353024; [Hoodi](https://github.com/OffchainLabs/prysm/blob/96d418cd964b083e37519a9df274030a1beac7db/config/params/testnet_hoodi_config.go) retains disabled activation from the mainnet default. Do not copy canceled Hoodi timing or infer activation from version bytes.
- [BSC config](https://github.com/bnb-chain/bsc/blob/54afaff4da31ac9e49ba646a4f65104edac5a2f3/params/config.go) schedules Jenner at 1796697000 on mainnet and 1793241000 on testnet. [CAS20 routing](https://github.com/bnb-chain/bsc/blob/54afaff4da31ac9e49ba646a4f65104edac5a2f3/core/vm/cas20.go) and [payment-lane classification](https://github.com/bnb-chain/bsc/blob/54afaff4da31ac9e49ba646a4f65104edac5a2f3/core/paymentlane/classify.go) add distinct feature/privilege and parent-poststate obligations. Test millisecond timestamps, native precompile gas changes, and lane budgets without inventing new transaction types.
- [Bor config](https://github.com/0xPolygon/bor/blob/3a018e38467594508ca31ebeaf804837fb92834a/params/config.go) sets Uburu at 96787900 on mainnet and 51978200 on Amoy. [Native finalization](https://github.com/0xPolygon/bor/blob/3a018e38467594508ca31ebeaf804837fb92834a/consensus/bor/bor.go) validates final-body StateSync presence and metadata; [processing](https://github.com/0xPolygon/bor/blob/3a018e38467594508ca31ebeaf804837fb92834a/core/state_processor.go) supplies the dedicated hooks/receipts. Cover zero/multiple events, bad metadata, missing final transaction, receipt-count mismatch, event scope/order/gas/revert and targeted replay. Keep the accepted sequential first-profile restrictions.
- The type declarations include 0x00–0x04 for all three execution clients and Bor 0x7f. Public admission still needs exact source/fork proof; a declaration is insufficient. Bor blob validation must be inspected, not assumed absent.
- Polygon mainnet and Amoy require separate own Ethereum L1 instances on mainnet and Sepolia respectively. A primary ETH extractor or neighboring profile is not a substitute.

Changing the candidate lock, source projection, AC rows, oracle, compiler/build settings, fixture identity, fork rules, or allocation invalidates the applicable definition/results under D82/D83. Update the projection and reviewed definition together; never silently reuse an old cell PASS for a new commit. No full acceptance is possible with unresolved mandatory cells, missing testnets, or missing public smoke.
