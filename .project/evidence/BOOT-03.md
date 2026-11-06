# BOOT-03 persistent controller

Task: [BOOT-03](https://github.com/sigaev-pro/bc-indexer/issues/5). Authorization: D86, BOOT only.
Accepted implementation base: `504b711dca4eb1c621f14ef54102102c3739248d`.

## Durable capture substep

`automation/evidence.py` calls the accepted BOOT-05 collector and evaluator using explicit repository, task, PR, review comment, coordinator, implementer, expected head, and expected base identities. It retains the complete passing snapshot, verdict, identity, capture time, and canonical snapshot hash in one atomic JSON bundle before returning success. File data and the containing directory are synced. Content-addressed names preserve prior captures; a failed or stale proof cannot create a successful capture.

The capture command only reads GitHub. It neither merges nor authorizes a merge. The coordinator must still use the expected-head merge condition, preserve branch protection, and verify and retain the actual merge result. CLI output goes to an explicitly selected Git-ignored directory in the canonical repository, including when the command runs from an isolated worktree.

Focused tests use temporary directories and a fake collector. The original 11 capture tests passed with 42 total bootstrap tests. Independent review then identified that new nested directory entries also need parent-directory sync. The correction creates and syncs each directory level before descending, including the nearest existing ancestor on retry. Two additional tests cover exact sync ordering and recovery after parent sync fails. The corrected suite has 13 capture tests and 44 total bootstrap tests. No real model session, issue, or GitHub mutation is used.

The complete scheduler, state recovery, leases, subprocess handling, and historical receipt importer remain in progress. This substep does not establish controller readiness. Product runtime remains NOT_RUN, and E01 is not authorized.
