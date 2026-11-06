# BOOT-03 persistent controller

Task: [BOOT-03](https://github.com/sigaev-pro/bc-indexer/issues/5). Authorization: D86, BOOT only.
Accepted implementation base: `504b711dca4eb1c621f14ef54102102c3739248d`.

## Durable capture substep

`automation/evidence.py` calls the accepted BOOT-05 collector and evaluator using explicit repository, task, PR, review comment, coordinator, implementer, expected head, and expected base identities. It retains the complete passing snapshot, verdict, identity, capture time, and canonical snapshot hash in one atomic JSON bundle before returning success. File data and the containing directory are synced. Content-addressed names preserve prior captures; a failed or stale proof cannot create a successful capture.

The capture command only reads GitHub. It neither merges nor authorizes a merge. The coordinator must still use the expected-head merge condition, preserve branch protection, and verify and retain the actual merge result. CLI output goes to an explicitly selected Git-ignored directory in the canonical repository, including when the command runs from an isolated worktree.

Focused tests use temporary directories and a fake collector. The original 11 capture tests passed with 42 total bootstrap tests. Independent review then identified that new nested directory entries also need parent-directory sync. The correction creates and syncs each directory level before descending, including the nearest existing ancestor on retry. Two additional tests cover exact sync ordering and recovery after parent sync fails. The corrected suite has 13 capture tests and 44 total bootstrap tests. No real model session, issue, or GitHub mutation is used.

The capture substep was committed as `d138a76cd44bc3718423f7e52470fe0e9f27a6fe`, then corrected after independent review in `ed513201e357da23414fd103227ecbadb15b8a84`. The coordinator independently reviewed the correction and used it to retain complete BOOT-06 and BOOT-07 proofs before their merges. Their original captures remain in the canonical ignored acceptance directory.

## Controller implementation

The implementation supplies `verify-bootstrap`, `run`, `resume`, `status`, and `stop`; validates the 84-node DAG and live issue mapping; retains exact task/recipe snapshots; and schedules only through the immutable reviewed owner stage bound. D86 remains BOOT only. Public `run` requires accepted BOOT-09 plus current preflight; `verify-bootstrap` checks the preceding eight tasks. Unconfigured product verification and acceptance remain explicit blockers.

Atomic run state, an inherited repository lock, and generation-bound task leases prevent multiple writers across worktrees. Before external changes, the controller retains operation identities and reconciles current worktrees, branches, PRs, reports, statuses, CI, and merge facts. It preserves dirty work and uncertain session outcomes. Full pre-merge captures precede merge requests; durable accepted receipts follow verified actual merges. Issue closure follows accepted evidence.

The supervisor uses bounded pipes, monotonic deadlines, process groups, and unreaped direct-child ownership. A parent watchdog bounds stopped or hung supervisors. Darwin tests exposed that `waitid` may report `CLD_STOPPED` despite `WEXITED`, and an empty zombie-owned group may return `EPERM`. The implementation filters exit event types and distinguishes empty groups from denied cleanup using live membership. PID records never authorize signaling another process. Denied or uncertain cleanup remains visible.

The owner's existing model remains unchanged. Codex resolves from the canonical repository's pinned 0.167.0 cache; isolated worktrees do not install their own copy. No new real model session was launched for BOOT-03. Existing BOOT-02/04 proofs establish the model and access environment, while the new process tests use temporary Python subprocesses.

## Failure evidence

- Fake-backend crash injection covers before and after worktree preparation, worker/reviewer execution, verification, push, PR creation, review comment, status, pre-merge capture, merge, and issue closure. Recovery retains one operation identity and does not duplicate changes.
- The real publication adapter is tested with a fake GitHub transport that loses responses after push, PR, comment, and status mutations. Retried operations discover and reuse the existing remote objects.
- Negative checks reject unverified worker output, manually closed dependencies, stale head/base/CI, forged receipts, missing recipes, changed lease generations, foreign stale leases, graph drift, and exhausted review attempts.
- Budget tests retain invocation reservations and elapsed window deadlines across normal resume. Only an explicit renewal adds a bounded window. A test-only future authorization proves that scheduling follows ordered stages; D86 still cannot dispatch E01.
- Disposable process tests cover inherited locks, stop with work preservation, process timeout, bounded output, environment fields, stale/reused PID records, backward wall-clock movement, and an unresponsive stopped supervisor with a child group.
- Receipt tests authenticate retained captures against actual public record shapes, verify merge parents/tree/main ancestry, reject altered evidence, and preserve accepted proof independently of expiring CI logs. Historical exceptions are limited to BOOT-01/02/04/05.

An early provisional live read-only `verify-bootstrap` run reconciled all 84 issues and authenticated BOOT-01/02/04/05/06/07. It correctly returned BLOCKED with BOOT-03 and BOOT-08 missing, `e01_authorized: false`, and product runtime NOT_RUN. This ran working source, not an accepted controller commit. The first bounded local attempt timed out during read-only collection; the normal sandbox lacked network access. The completed read-only check used approved escalation and retained local receipts. No GitHub mutation occurred. Executing source hashes and explicit clean/modified state accompany later revalidation records so checkout HEAD is not confused with uncommitted source bytes.

Implementation commit `af7c4b55edb922589f5bafb2665cecdd38c7867e` passed exact-head CI with 81 tests. The accepted BOOT-06/07 main was integrated with both status/decision histories preserved; component validation also passes. A final cleanup-outcome regression adds one test: a process that exits zero still cannot produce successful session evidence when owned-group cleanup is denied or uncertain. The corrected local suite has 82 tests. Document/mapping/mirror checks and the immutable v2 hash pass. Final committed-source live revalidation, independent review, fresh exact-head GitHub CI, and controller acceptance remain pending. Native product runtime remains NOT_RUN; E01 is not authorized.
