# BOOT-08 evidence

Task: BOOT-08 / TEST-OPEN-02. Decision: D90. Worker session: /root/boot07_completion. Owned worktree: boot08. Branch: boot/08-test-corpus. Accepted base: 41d9ed0627fe4822bb3ffc2e1c043a84f18af406.

Status: READY_FOR_REVIEW after the checks below. Independent review, protected integration and remote CI are coordinator work. This file belongs to the candidate commit; the final exact commit SHA is returned in the worker handoff. Product runtime is NOT_RUN. No nodes, public transactions, product runtime, active source-pin changes, remote mutations or E01 work were performed.

## Change

[The corpus](../testing/corpus/README.md) defines all 217 existing ACs with 677 named baseline variants and 17 additional concrete scenario recipes. It adds a source-pinned projection of seven profiles, 270 execution-fork declarations including explicit omitted/null fields, paired consensus schedules, native transaction types, precompile implementations, five CAS20 routes/159 routed methods, and current-candidate deltas. The projection is not a runtime ProfileManifest.

The deterministic validator expands 36,083 definition obligations: 20,584 native-seam, 11,310 local-e2e, 2,804 harness, 1,239 synthetic, 95 public, and 51 document cells. These are separate assertions, not separate process launches or passed tests. Source-inapplicable and unresolved cells stay visible until E01/E06 resolve them; no fabricated genesis/fork/fixture hashes are supplied.

The definition preserves the 63 REQs, 217 ACs, 68 product tasks and 84 issue mappings. Existing AC owners and early gates remain unchanged. D90 fixes initial seed/soak/backlog floors and configurable allocation/deadline rules. The static allocation checker never grants READY or runtime PASS.

Normative summaries distinguish public inventory/declared-field proof from full trace semantics; private test identities from public profiles; native-seam from full E2E; compiler-target incompatibility from product failures; and synthetic transport fixtures from native execution. All existing normative mirrors and both reader-facing specifications are synchronized. D87's reviewed reader algorithm is preserved.

## Source findings

Exact candidate sources and per-file Git blob references are in [candidate-sources.json](../testing/corpus/candidate-sources.json). BOOT-06's candidate lock remains byte-for-byte unchanged.

- Geth/Prysm require separate Sepolia Amsterdam/Gloas and canceled Hoodi activation checks. Configuration/version-byte presence is not a runtime activation proof.
- BSC requires distinct Jenner schedules, CAS20 route/method/feature/privilege variants, parent-poststate payment-lane decisions, millisecond timestamps and native precompile gas rules.
- Bor requires Uburu StateSync final-body/hash/receipt/event validation and targeted replay while retaining sequential first-profile restrictions.
- Current source declarations include native types that still need profile/fork admission proof. A declared blob type cannot be silently treated as either always supported or always rejected.
- Historical full-consensus and controlled-private identity admission remain explicit E01/E06 proof obligations. A native seam cannot silently close required E2E.

## Verification

The original candidate passed 65 unit tests, including 34 corpus tests, but independent review requested two structural repairs. Repair 1 passed 80 portable unit tests, including 49 corpus tests. BOOT-06 component validation passed all 54 negative cases and retained its 41 components, seven profiles, 11 constraints and eight pending outputs. Document checks passed 16 mirrors, 90 documents/1040 local links, all stable ID counts and the unchanged v2 digest. Final exact-head results are reported in the repair handoff; the earlier candidate's successful checks do not accept this repair.

The new tests reject missing AC/variant/profile/recipe assignments, unreviewed source drift, fake runtime artifacts, synthetic-to-native relabeling, stale AC bindings, duplicate JSON fields, missing pairwise coverage, unsupported exclusions, missing campaign cells, standard deadlines above 1800 seconds, comparison outside the run, preparation counted as execution, mainnet signing, missing testnet signers, lower seed/soak floors, and insufficient real backlog. Positive controls verify complete definitions/allocations without claiming readiness or runtime success.

Commands:

~~~sh
python3 .project/testing/corpus/verify.py
python3 -m unittest discover -s .project/testing/corpus -p 'test_*.py'
python3 .project/automation/ci.py
git diff --check
git diff --cached --check
~~~

After the candidate commit, repeat the portable CI with BOOTSTRAP_HEAD_SHA set to that exact full SHA and BOOTSTRAP_BASE_SHA set to the accepted base above. The worker handoff reports that exact-head result. This definition has no runtime benchmark; the 30-minute limit and quotas remain requirements to validate during implementation.

## Remaining work

Independent review of 452b40a408c0915f3952705357631c9b68e8a97b found that empty/malformed oracle contracts and missing resolution/owner mappings could survive definition validation. Repair 1 adds typed field/provenance checks, complete explicit gate/profile/artifact ownership against the real task registry, and direct rejection from campaign validation even with a fresh digest. It preserves the full catalog digest and leaves human review responsible for meaning and independence. A related parser correction preserves digits in E07-02.e2e instead of truncating its AC owner. Definition COMMON/document cells now expose the existing D82 DOC identity in a partial projection; runtime hashes and unresolved fixture kinds remain null. The projection is not a resolved registry or a new schema.

Independent review must accept D90/BOOT-08 before TEST-OPEN-02 design closure. E01/E06 must implement resolved schemas, actual fixture/compiler outputs, native admission/equivalence, independent oracle artifacts, resource feasibility, prepared public profiles, and testnet funding/signing readiness. E02–E07 execute their assigned cells and preserve all required NOT_RUN blockers until actual evidence exists.

Stop before E01 under D86. The coordinator handles review and serialized merge; this worker does not push or merge.
