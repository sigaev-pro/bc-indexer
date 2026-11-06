# BOOT-09 readiness evidence

## Scope and accepted baseline

Work starts from accepted main `24ae66d72512427e0e1fd7dc281640035c37db51`, after BOOT-03 PR #90 and issue #5 acceptance comment `6695975227`. The controller's final review was `6695941742`, and its exact-head CI run was `42295448825`. BOOT-01 through BOOT-08 have coordinator acceptance records. BOOT-09 itself still requires independent review, exact-head checks, retained capture, verified merge, and coordinator acceptance.

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
