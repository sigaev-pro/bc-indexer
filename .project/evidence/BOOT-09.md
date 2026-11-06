# BOOT-09 readiness evidence

## Accepted result and stopping point

All nine BOOT tasks are accepted. [PR #92](https://github.com/sigaev-pro/bc-indexer/pull/92) merged candidate `8387694e78a6a0ddc733b8c7f3b521dac2d967a2` as `17d421a0c6aec96c2af82dbc9080eda36849a692`. Both merge parents and the candidate-identical tree were verified. The [acceptance record](https://github.com/sigaev-pro/bc-indexer/issues/9#issuecomment-6697157355) closes issue #9. All 84 task issues were reconciled afterward: issues #1 through #9 are closed, and the other 75 remain open.

Independent reviewers `/root/boot01_review` and `/root/boot04_preflight` passed the exact candidate. [Their published report](https://github.com/sigaev-pro/bc-indexer/pull/92#issuecomment-6697118671) and [CI run 42302651951](https://github.com/sigaev-pro/bc-indexer/actions/runs/42302651951) cover 161 portable tests, 54 component negative checks, all normative mirrors and stable IDs, and the unchanged original v2. The trusted corpus retains 54 vectors and four profile-expansion calls. The full premerge capture is retained in the canonical `runs/acceptance/` directory with snapshot SHA-256 `c7d0df6dffc967796b57d498ce4e7602b14b501de5057c30d815c7505bc875ec` and bundle SHA-256 `b0373fbb35ba674ea7b2b57edd704a55027e9a1bdaf19cf9ff4cf1470402fa58`.

On accepted main, the coordinator ran:

```sh
python3 .project/automation/controller.py run --repo sigaev-pro/bc-indexer --authorization-ref D86 --stage-limit BOOT
python3 .project/automation/controller.py status --run-id run_466b9ee5481a45f99b682d94f00c0309
```

The run completed in 161.93 seconds with exit code 0. Its persisted state is COMPLETE: nine DONE tasks, 75 PLANNED tasks, zero model invocations in this run, and no blocker. All product tasks remain PLANNED. The authorization is D86/BOOT, and product runtime remains NOT_RUN. The completion-state file is `.project/automation/state/runs/run_466b9ee5481a45f99b682d94f00c0309.json`; its SHA-256 at completion is `abf2199f82c14dc773ec0358be6ab8b3ebd64155edcd68bbdf20745fd9fa6a6d`. Back up the canonical `state/` and `runs/` directories together as described in the automation guide. This duration measures controller verification, not a native network test.

D86 is complete. Stop before E01. The next action requires a new owner launch and its reviewed authorization record for the configured E01-01.evidence artifact task. The later eleven E01 tasks and G.E01 still need independently reviewed verification contracts. They cannot be enabled by a permissive adapter, a changed resume argument, or a new run that resets consumed budgets. No general cross-run budget-transfer command is implemented. No product schemas, native product runtime, installed service, or production deployment are claimed.

## Candidate history and accepted baseline

The sections below record candidate preparation and review. The accepted result above supersedes their earlier pending-review wording.

Work started from accepted main `24ae66d72512427e0e1fd7dc281640035c37db51`, after BOOT-03 PR #90 and issue #5 acceptance comment `6695975227`. The controller's final review was `6695941742`, and its exact-head CI run was `42295448825`. BOOT-01 through BOOT-08 had coordinator acceptance records. BOOT-09's subsequent review, capture, verified merge, and acceptance are recorded above.

D91 adds a concrete starting path for the future E01-01.evidence artifact task. It does not widen D86, create product schemas, execute native runtime, or configure the other eleven E01 tasks and G.E01. Initial-task readiness is distinct from permission to launch and from complete E01 readiness. The accepted corpus, source locks, specification, stable IDs, and original v2 remain unchanged.

## Trusted contract and proof

The [handoff](../automation/evidence-task-contract.md) fixes required schema artifacts, the seven-profile definition, the candidate validation CLI, and bounded input/output rules. The trusted pack contains 54 validation bundles (13 valid and 41 invalid) and four profile-expansion calls. They cover the three assigned ACs: prerequisite stages, applicability/verdict and reference rules, and complete profile/fork/fixture/suite identities with parent/child outcomes. The driver executes the future candidate's real validator; expected results and vector IDs are not sent to it. Always-success and always-failure candidates fail conformance.

Oracle controls use actual AC owners and profile families, consistent registry/rule/applicability contexts, complete child references, and sequential preparation/execution/teardown times. A recorded failed child remains valid evidence with aggregate FAIL; a claimed parent PASS cannot hide it. Negative composite vectors preserve valid controls. An acyclic forward-stage dependency fails independently of cycle detection. DOC network spelling is explicitly D91's test-evidence choice; COMMON is not promoted into a runtime identity. All fixture bytes are synthetic bootstrap inputs, not executed product cases.

Each validator call has a five-second timeout and a 64 KiB file-output limit. The full finite pack runs under the accepted bounded process supervisor. Full process requests/results and driver logs join exact task/head/base, output hashes, frozen recipe/verifier hashes, source commit, and authorization in a durable task proof. The coordinator publishes its digest in a separate controller_verification field after verification. This field is not attributed to the earlier reviewer.

The public acceptance evaluator and capture CLI reject generic product proofs. Initial-task artifact-conformance acceptance additionally requires the complete task proof, its matching public attestation, and accepted verifier-source ancestry. The controller validates this before merge. Receipt replay authenticates the public binding again; changing local proof bytes and recomputing every local capture/report/status/digest cannot forge that external publication. Existing BOOT capture verdicts and the four historical exceptions remain unchanged.

## Isolated checks and limits

Fourteen focused initial-task tests pass against working source. They use disposable protocol stubs and fake GitHub state, not product schema implementations or real model sessions. Tests execute the real bounded driver, reject noisy/hung validators, check oracle control isolation, reject D86 and unconfigured dispatch, preserve frozen recipes, recover a verification crash without new charges, reject forged receipt bindings, and reconcile lost publication responses. The actual pre-merge adapter is exercised with missing and wrong public attestations and performs zero merge calls.

Coordinator plus independent QA review owns later task verifiers. An accepted addition cannot silently mutate an existing run's recipe snapshot. A fresh-snapshot handoff must preserve prior attempts, budgets, receipts, leases, and work. The current controller has no general budget-transfer command; later handoff support must be implemented and reviewed before using a new run to continue consumed work. This limitation is explicit and does not prevent a first authorized initial-task run.

Full working-source CI passes 158 tests, 54 component negative cases, 16 normative mirrors, 93 documents/1051 links, all 84 task mappings, and the unchanged v2 check. No real E01 task, product runtime, model session, service, or remote mutation ran during this work.

## Exact committed-source readiness check

Implementation commit `41015c8043431829f4d72fb08ef713f113b4fb0f`, based on accepted main `24ae66d72512427e0e1fd7dc281640035c37db51`, passes the same exact-head CI checks with a clean worktree. One asynchronous live `verify-bootstrap` check ran on that exact clean source with a 300-second aggregate limit. It completed successfully and returned PASS with BOOT-01 through BOOT-08 accepted, no missing bootstrap dependency, and 84 issues reconciled. The real read-only importer authenticated BOOT-03's retained coordinator capture and merge receipt without a new historical exception.

The live report identifies `E01-01.evidence` / `evidence-artifacts-v1` as configured but still requiring authorization. It lists all eleven remaining E01 tasks and G.E01 as unconfigured. It explicitly reports `e01_authorized: false`, `product_verification_configured: false`, and `runtime: NOT_RUN`. Durable receipt revalidations record the clean executing commit and hashes of all eight controller/acceptance modules.

The trusted pack reported SHA-256 values `815416a15c73d795814ea93a7219914b2fdefae5251e823f88de03f4a46feff2` for the driver, `4d4f7684f6e93a46e85c3e424dd6dde478982125f797abf909befa7cda093a53` for the handoff, `9283e0938cf6d267711b7dfe3ec6cfd52ba38608991c513c4e320bc8bd76e030` for the proof adapter, and `a9fc51d3334e3f7d5616df0687b5161c82a0f61b20e8f4714823324428a0a95e` for task recipes. The final documentation commit changes only this evidence and PROJECT_STATUS; implementation and trusted-pack hashes remain identical. Independent review and coordinator acceptance of BOOT-09 remain pending. This readiness result does not authorize E01.

## First independent review repair

Independent review of `d6b11e0b6a12af6e3b13610a1973f9c6740f9231` requested two P2 corrections. The handoff's ordinary validation protocol now explicitly allows empty stdout: exit 0 means valid, and exit 2 means invalid. ExpandProfiles still requires the exact checked JSON array, and missing required artifact files remain failures.

The driver previously bounded file output but could accept a candidate that caught EFBIG or SIGXFSZ after saturating a stream, then exited with the expected code. It now rejects either stdout or stderr at 65,536 bytes. New regressions first reproduced eight failures across caught overflow and exact-cap writes, both streams, and both expected exit codes. After the fix, those cases fail correctly; empty and below-cap ordinary outputs remain allowed, while missing ExpandProfiles JSON is rejected.

Three added tests bring full local CI to 161 tests, with 54 component negative cases, document checks, and immutable v2 verification passing. The complete `vectors()` source body is byte-identical to the reviewed candidate; all 54 vector inputs and four expansion calls remain unchanged. Only the driver limit check, protocol wording, regressions, and status/evidence are part of this repair. Fresh exact committed-source verification and independent re-review remain required. No product schema, native runtime, model session, service, or remote mutation ran.

Clean repair commit `912bae9cd040de32489a234e81931d479811b9cf` passes exact-head CI with 161 tests and 54 component negatives. One live read-only check on that exact source, bounded to 300 seconds, returns PASS for BOOT-01 through BOOT-08 and all 84 issue mappings. E01 remains unauthorized, the other twelve E01 nodes remain unconfigured, and product runtime remains NOT_RUN. The updated driver SHA-256 is `7802d17c4a0bf6f2f59eae4a85b4a1878a4119482731a1b64cc72d4fece2a96c`; the updated handoff hash is `cc3a55067284c562a541f4e937c1e2336a9e43349eb16451c8390c4533f424dc`. Proof-adapter and recipe hashes are unchanged. The final status/evidence commit changes no implementation or trusted-pack bytes. Independent re-review and coordinator acceptance remain pending.
