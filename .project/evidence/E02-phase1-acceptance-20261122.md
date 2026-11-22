# E02 phase 1 acceptance records — 2026-11-22

Formal evidence for the four merged phase-1 tasks. Each section cites the
PR, merge commit, review verdicts and the quality numbers the reviews
verified from primary records.

## #22 E02-01.semantic_descriptors — PR #197, merged `b8c41fed`

Package `protocol/semantic` (module stdlib-only, go 1.25). Invariants:
manifest identity stable across retries (digest excludes attempt/event/
delivery fields — proven by flipping all 12 identity fields); decision→
batch allocation exactly once (EnsureBatch idempotent per decision,
counter monotonic, overflow leaves state unchanged; AdoptBatch restores
on marker repeat, ErrReallocation on conflicting batch, ErrIdentity on
foreign identity); Removed events carry revision > 0 from the same
activation counter (CURRENT_SEMANTICS.md:79, fixed per review P2 at
`55ef443`). Review: SPEC PASS / Standards PASS; 90.1% statements,
race clean; two golden digest vectors from an independent Python
implementation. Open questions correctly deferred: production Id16
minting (assigned to E03-01 store creation per CONTROL_WIRE_CONTRACT).

## #23 E02-05.actions_model — PR #198, merged `ad49a4a1`

Package `protocol/balance`. Invariants: single tx-local execution_order
across frames/actions/effects with the equal-order rule (only an
ExecutionAction and its exactly-named projection share an order);
RollbackScope tree with ancestor propagation, cycle/orphan detection,
fees/nonce never in cancelled scopes; net-zero actions preserved;
FeeSummary a pure aggregate (net = purchase − return, credits 1:1) —
never a source of double-counted movements. Corpus fixtures: all 11 user
transactions of the E01-03 smoke corpus transcribed with SHA-256
provenance; fee arithmetic independently verified per tx before coding;
each fixture reconciles against corpus state_before/state_after. Review:
SPEC PASS / Standards PASS; 90.2% statements, race clean; reviewer
reconciled every fixture literal against the corpus (0 mismatches).
Pre-merge P2 fixed at `6d747da`: the gas-return effect on root revert
now starts from the restored post-purchase point (journal restore
precedes the refund credit). Open questions correctly deferred with
corpus evidence: zero-amount gas return publish-vs-omit (E04-03);
Polygon fee-credit relative order (needs source binding).

## #24 E02-08.block_metadata — PR #199, merged `fd2c3e73`

Package `protocol/block`. Invariants: native counts recorded as
observations, never lifecycle inputs (mutating any count to 0 or 2^40
changes nothing — tested); empty blocks first-class (real empty-block
fixtures from retained records); reorg creates a NEW activation with the
old record immutable (enforced by value-receiver transitions; DeepEqual
snapshot proof); DeliveryBatchRef carries batch/frontier only — no
BlockComplete (the D25 exclusion enforced in three guards); presence-
explicit optionals distinguish null from present-zero (real present-zero
values pinned: BSC pre-London base fee 0, present-zero beacon root, ETH
post-merge difficulty 0). Review: SPEC PASS / Standards PASS; reviewer
independently decoded all fixture headers byte-exact. Open questions
(6) all judged sound deferrals with named owners.

## #25 E02-10.closed_profiles — PR #200, merged `72958bf9`

Package `protocol/profiles`. Invariants: closed v1 registry of payload
variants (12, matching MESSAGE_CONTRACT section 4 exactly; BlockComplete
and CoverageUpdated absent per D25), receipt encoding kinds (D75: kind 1
native_type present-and-0; kind 2 native_type mandatory 1..127 with
missing rejected per AC-DAT-02-03; kind 3 auxiliary-only, forbidden in
body receipts while kinds 1/2 are forbidden in the auxiliary slot; the
closed Origin enum mirrors applicability), network-details closed oneof
per family; unsupported registry version rejected without ANY fallback;
enum zero values INVALID. SourcesBindingDigest (D80/IR-02) = SHA-256 of
the exact sources_file bytes, mandatory on SourceObservationRef and
L1Checkpoint (typed [32]byte; missing bytes and mismatches rejected;
whitespace sensitivity tested). Review history: initial SPEC REQUEST_
CHANGES (P1: non-normative digest construction; P2×2: kind-2 native_type
and auxiliary-slot rules) — all fixed at `327a46f`, delta code-PASS,
doc-P2 fixed at `6a6053e`; final 93.0% statements, race clean. Open
question correctly deferred: per-network/fork native_type allowlist
(assigned to the ProfileManifest consumer).

## Phase summary

Four packages, four merged PRs, four closed issues; coverage
90.1/90.2/93.0/64.6 percent respectively (block is lifecycle-message
helper residue, per the #199 review), race clean throughout, all review
findings resolved before merge, all open questions recorded with named
downstream owners.
No native runs required or performed. The phase-1 outputs are the
inputs of phase 2 (#26-#29), running at the time of this record.
