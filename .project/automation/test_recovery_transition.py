"""Positive and negative controls for the failed-verification recovery transition."""

import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import recovery_transition
from recovery_transition import BlockedRecovery, FailedRunRecovery, MAX_EVIDENCE_BYTES

RUN = "run_D92_1794007047"
EPOCH, REVISION = 5, 52
OPERATION = "78d89ce5487b416a9ab476da9a856e02"
CANDIDATE = "f17bb195d1baf62e93c596ed0fc067f2c6b60574"
SOURCE = "f917e73a7e5ec3de28110faddb6a924d2e04cf9e"
NEXT_SOURCE = hashlib.sha1(b"accepted next source").hexdigest()
REVIEW = hashlib.sha256(b"review acceptance of the source change").hexdigest()
DEAD_SESSION = "session-check-2"


def recovery(root):
    return FailedRunRecovery(root, clock=lambda: 1794007047.0)


class RecoveryTransitionTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.recovery = recovery(self.root)
        self.evidence_path = self.root / "session" / (DEAD_SESSION + ".json")
        self.evidence_path.parent.mkdir(parents=True, exist_ok=True)
        self.evidence_path.write_bytes(self.evidence_bytes())
        self.dead = subprocess.Popen([sys.executable, "-c", "pass"])
        self.dead.wait()

    def evidence_bytes(self, **changes):
        document = {"schema_version": 1, "run_id": RUN, "epoch": EPOCH, "revision": REVISION,
                    "operation": OPERATION, "candidate": CANDIDATE, "source": SOURCE,
                    "outcome": "FAILED_VERIFICATION",
                    "session": {"id": DEAD_SESSION, "exit_code": 1, "cleanup_complete": True},
                    "outputs": {"candidate_sha256": hashlib.sha256(CANDIDATE.encode()).hexdigest()}}
        document.update(changes)
        return json.dumps(document, sort_keys=True, indent=1).encode()

    def rewrite_evidence(self, **changes):
        self.evidence_path.write_bytes(self.evidence_bytes(**changes))

    def archive(self, instance=None):
        return (instance or self.recovery).archive(RUN, EPOCH, REVISION, OPERATION, CANDIDATE,
                                                   self.evidence_path, cleanup_verified=True)

    def full_flow(self, instance=None):
        instance = instance or self.recovery
        self.archive(instance)
        instance.lease_release(RUN, {"pid": self.dead.pid}, docker_cleaned=True)
        instance.source_handoff(SOURCE, NEXT_SOURCE, REVIEW)
        return instance.fresh_candidate(RUN, NEXT_SOURCE, EPOCH + 1)

    def assertBlocked(self, action, code, *arguments):
        with self.assertRaisesRegex(BlockedRecovery, code):
            action(*arguments)

    def state_digest(self):
        return hashlib.sha256(json.dumps(self.recovery.run_record(RUN), sort_keys=True).encode()).hexdigest()

    # ---- the whole transition ----------------------------------------------------

    def test_failed_attempt_is_archived_released_handed_off_and_restarted(self):
        record = self.archive()
        self.assertEqual(record["phase"], "ARCHIVED")
        self.assertEqual((record["epoch"], record["revision"], record["operation"], record["candidate"]),
                         (EPOCH, REVISION, OPERATION, CANDIDATE))
        self.assertEqual(record["source"], SOURCE)
        self.assertEqual(record["evidence"]["sha256"], hashlib.sha256(self.evidence_bytes()).hexdigest())
        retained = self.root / record["evidence"]["retained"]
        self.assertEqual(retained.read_bytes(), self.evidence_bytes())
        record = self.recovery.lease_release(RUN, {"pid": self.dead.pid}, docker_cleaned=True)
        self.assertEqual(record["phase"], "LEASE_RELEASED")
        self.assertEqual(record["lease"], {"pid": self.dead.pid, "docker_cleaned": True,
                                           "process_dead": True, "released_at": 1794007047.0})
        accepted = self.recovery.source_handoff(SOURCE, NEXT_SOURCE, REVIEW)
        self.assertEqual(accepted["authorized_by"], [RUN])
        record = self.recovery.fresh_candidate(RUN, NEXT_SOURCE, EPOCH + 1)
        self.assertEqual(record["phase"], "FRESH_CANDIDATE")
        self.assertEqual(record["fresh"]["source"], NEXT_SOURCE)
        self.assertEqual(record["fresh"]["epoch"], EPOCH + 1)
        self.assertEqual(record["fresh"]["status"], "PENDING_VERIFICATION")
        self.assertEqual(record["fresh"]["parent"], {"operation": OPERATION, "candidate": CANDIDATE,
                                                     "source": SOURCE, "revision": REVISION})
        self.assertEqual([row["transition"] for row in record["history"]],
                         ["archive", "lease_release", "fresh_candidate"])
        # Nothing fabricated: no acceptance claim exists anywhere in the record.
        self.assertNotIn("accepted", record)
        self.assertNotIn("receipt", record)

    # ---- repeated transitions and failure history ---------------------------------

    def test_repeated_transitions_are_idempotent_and_preserve_history(self):
        self.archive()
        self.recovery.lease_release(RUN, {"pid": self.dead.pid}, docker_cleaned=True)
        self.recovery.source_handoff(SOURCE, NEXT_SOURCE, REVIEW)
        self.recovery.fresh_candidate(RUN, NEXT_SOURCE, EPOCH + 1)
        record = self.recovery.run_record(RUN)
        # Every step replays with identical arguments and returns the same state.
        self.assertEqual(self.archive(), record)
        self.assertEqual(self.recovery.lease_release(RUN, {"pid": self.dead.pid}, docker_cleaned=True), record)
        self.assertEqual(self.recovery.source_handoff(SOURCE, NEXT_SOURCE, REVIEW),
                         self.recovery.handoff_record())
        self.assertEqual(self.recovery.fresh_candidate(RUN, NEXT_SOURCE, EPOCH + 1), record)
        self.assertEqual([row["transition"] for row in record["history"]],
                         ["archive", "lease_release", "fresh_candidate"])
        self.assertEqual(self.recovery.handoff_record()["accepted_at"], 1794007047.0)

    def test_evidence_outlives_its_original_and_replays_without_it(self):
        self.archive()
        self.evidence_path.unlink()  # the retained copy is the surviving evidence
        record = self.archive()
        retained = self.root / record["evidence"]["retained"]
        self.assertTrue(retained.is_file())
        self.assertEqual(retained.read_bytes(), self.evidence_bytes())

    # ---- stale compare-and-swap values ---------------------------------------------

    def test_stale_cas_values_are_rejected_without_touching_state(self):
        self.archive()
        before = self.state_digest()
        for epoch, revision in ((EPOCH, REVISION - 1), (EPOCH, REVISION + 1), (EPOCH - 1, REVISION), (EPOCH + 1, REVISION)):
            with self.subTest(epoch=epoch, revision=revision):
                self.assertBlocked(self.recovery.archive, "BLOCKED_STALE_CAS", RUN, epoch, revision,
                                   OPERATION, CANDIDATE, self.evidence_path, True)
        self.assertEqual(self.state_digest(), before)

    def test_fresh_candidate_requires_exactly_one_epoch_advance(self):
        self.archive()
        self.recovery.lease_release(RUN, {"pid": self.dead.pid}, docker_cleaned=True)
        self.recovery.source_handoff(SOURCE, NEXT_SOURCE, REVIEW)
        for epoch in (EPOCH, EPOCH + 2, EPOCH - 1):
            with self.subTest(epoch=epoch):
                self.assertBlocked(self.recovery.fresh_candidate, "BLOCKED_STALE_CAS", RUN, NEXT_SOURCE, epoch)
        self.assertEqual(self.recovery.fresh_candidate(RUN, NEXT_SOURCE, EPOCH + 1)["phase"], "FRESH_CANDIDATE")
        for epoch in (EPOCH, EPOCH + 2):
            with self.subTest(epoch=epoch):
                self.assertBlocked(self.recovery.fresh_candidate, "BLOCKED_MISMATCH", RUN, NEXT_SOURCE, epoch)
        self.assertEqual(self.recovery.fresh_candidate(RUN, NEXT_SOURCE, EPOCH + 1)["phase"], "FRESH_CANDIDATE")

    # ---- mismatched candidates, operations, and evidence ----------------------------

    def test_evidence_must_identify_the_exact_attempt(self):
        for field, value in (("run_id", "run_D92_1794007048"), ("epoch", EPOCH + 1), ("revision", REVISION - 1),
                             ("operation", "0" * 32), ("candidate", "a" * 40), ("schema_version", 2)):
            with self.subTest(field=field):
                self.rewrite_evidence(**{field: value})
                self.assertBlocked(self.recovery.archive, "BLOCKED_MISMATCH|BLOCKED_STATE",
                                   RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
                self.assertIsNone(self.recovery.run_record(RUN))
        self.rewrite_evidence()

    def test_frozen_source_is_taken_from_the_retained_evidence(self):
        record = self.recovery.archive(RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
        self.assertEqual(record["source"], SOURCE)  # the run's old frozen source, from evidence
        rewritten = self.root / "session" / "other-frozen-source.json"
        rewritten.write_bytes(self.evidence_bytes(run_id="run_other_123456", source="b" * 40))
        other = recovery(self.root)
        record = other.archive("run_other_123456", EPOCH, REVISION, OPERATION, CANDIDATE, rewritten, True)
        self.assertEqual(record["source"], "b" * 40)

    def test_replay_with_another_operation_or_candidate_is_rejected(self):
        self.archive()
        for operation, candidate in (("0" * 32, CANDIDATE), (OPERATION, "a" * 40)):
            self.assertBlocked(self.recovery.archive, "BLOCKED_MISMATCH",
                               RUN, EPOCH, REVISION, operation, candidate, self.evidence_path, True)
        accepted = self.recovery.source_handoff(SOURCE, NEXT_SOURCE, REVIEW)
        with self.assertRaisesRegex(BlockedRecovery, "BLOCKED_MISMATCH"):
            self.recovery.source_handoff(SOURCE, NEXT_SOURCE, "c" * 64)
        self.assertEqual(self.recovery.handoff_record(), accepted)

    def test_fresh_candidate_must_use_the_accepted_source_of_its_own_failure(self):
        self.archive()
        self.recovery.lease_release(RUN, {"pid": self.dead.pid}, docker_cleaned=True)
        self.recovery.source_handoff(SOURCE, NEXT_SOURCE, REVIEW)
        self.assertBlocked(self.recovery.fresh_candidate, "BLOCKED_HANDOFF", RUN, SOURCE, EPOCH + 1)
        self.assertBlocked(self.recovery.fresh_candidate, "BLOCKED_HANDOFF", RUN,
                           hashlib.sha1(b"another accepted source").hexdigest(), EPOCH + 1)
        self.assertEqual(self.recovery.fresh_candidate(RUN, NEXT_SOURCE, EPOCH + 1)["fresh"]["source"], NEXT_SOURCE)

    # ---- missing cleanup and uncertain work ----------------------------------------

    def test_unverified_cleanup_is_never_archived_or_released(self):
        self.assertBlocked(self.recovery.archive, "BLOCKED_CLEANUP",
                           RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, False)
        self.assertIsNone(self.recovery.run_record(RUN))
        self.archive()
        self.assertBlocked(self.recovery.lease_release, "BLOCKED_CLEANUP", RUN, {"pid": self.dead.pid}, False)
        self.assertBlocked(self.recovery.lease_release, "BLOCKED_CLEANUP", RUN, {"pid": self.dead.pid}, None)
        self.assertEqual(self.recovery.run_record(RUN)["phase"], "ARCHIVED")

    def test_missing_or_unreadable_evidence_cannot_be_archived(self):
        self.evidence_path.unlink()
        self.assertBlocked(self.recovery.archive, "BLOCKED_EVIDENCE",
                           RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
        self.evidence_path.write_bytes(b"not json at all\n")
        self.assertBlocked(self.recovery.archive, "BLOCKED_EVIDENCE",
                           RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
        self.evidence_path.write_bytes(b"[1, 2, 3]")
        self.assertBlocked(self.recovery.archive, "BLOCKED_EVIDENCE",
                           RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
        self.assertIsNone(self.recovery.run_record(RUN))

    def test_evidence_bound_is_enforced(self):
        document = self.evidence_bytes()
        with patch.object(recovery_transition, "MAX_EVIDENCE_BYTES", len(document) - 1):
            self.assertBlocked(self.recovery.archive, "BLOCKED_EVIDENCE",
                               RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
        self.assertLess(len(document), MAX_EVIDENCE_BYTES)
        self.assertIsNone(self.recovery.run_record(RUN))

    def test_review_or_ci_pass_is_never_archived_as_acceptance(self):
        for outcome in ("CI_PASS", "REVIEW_PASS", "ACCEPTED", "PUBLISHED", "SUCCEEDED"):
            with self.subTest(outcome=outcome):
                self.rewrite_evidence(outcome=outcome)
                self.assertBlocked(self.recovery.archive, "BLOCKED_OUTCOME",
                                   RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
                self.assertIsNone(self.recovery.run_record(RUN))
        self.rewrite_evidence()

    # ---- process ownership and Docker cleanup --------------------------------------

    def test_live_owner_blocks_release_until_the_process_is_proven_dead(self):
        self.archive()
        live = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
        self.addCleanup(live.wait)   # cleanups run in reverse: kill first, then reap
        self.addCleanup(live.kill)
        before = self.state_digest()
        self.assertBlocked(self.recovery.lease_release, "BLOCKED_LIVE_PROCESS", RUN, {"pid": live.pid}, True)
        self.assertEqual(self.state_digest(), before)
        for owner in ({"pid": 0}, {"pid": -1}, {"pid": "7"}, {}, {"pid": self.dead.pid, "extra": 1}, self.dead.pid):
            with self.subTest(owner=owner):
                self.assertBlocked(self.recovery.lease_release, "BLOCKED_STATE", RUN, owner, True)
        live.kill()
        live.wait()  # ownership is now proven dead by a terminal observed result
        record = self.recovery.lease_release(RUN, {"pid": live.pid}, docker_cleaned=True)
        self.assertEqual(record["lease"]["pid"], live.pid)
        self.assertTrue(record["lease"]["process_dead"])

    def test_release_replay_with_another_ownership_proof_is_rejected(self):
        self.archive()
        self.recovery.lease_release(RUN, {"pid": self.dead.pid}, docker_cleaned=True)
        for pid, cleaned in ((self.dead.pid + 1, True), (self.dead.pid, False)):
            with self.subTest(pid=pid, cleaned=cleaned):
                self.assertBlocked(self.recovery.lease_release, "BLOCKED_MISMATCH", RUN, {"pid": pid}, cleaned)
        self.assertEqual(self.recovery.run_record(RUN)["lease"]["pid"], self.dead.pid)

    # ---- operator stops -------------------------------------------------------------

    def test_operator_stops_block_every_transition_and_are_preserved(self):
        self.recovery.stop(RUN, "operator requested a stop")
        marker = self.root / "stops" / (RUN + ".json")
        digest = hashlib.sha256(marker.read_bytes()).hexdigest()
        self.assertBlocked(self.recovery.archive, "BLOCKED_STOPPED",
                           RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
        self.assertBlocked(self.recovery.lease_release, "BLOCKED_STOPPED", RUN, {"pid": self.dead.pid}, True)
        self.assertBlocked(self.recovery.fresh_candidate, "BLOCKED_STOPPED", RUN, NEXT_SOURCE, EPOCH + 1)
        # With no archived attempt, the handoff is rejected before the stop check.
        self.assertBlocked(self.recovery.source_handoff, "BLOCKED_HANDOFF", SOURCE, NEXT_SOURCE, REVIEW)
        self.assertIsNone(self.recovery.run_record(RUN))
        self.assertIsNone(self.recovery.handoff_record())
        self.assertEqual(hashlib.sha256(marker.read_bytes()).hexdigest(), digest)
        self.recovery.stop(RUN, "operator requested a stop")  # replaying the stop request changes nothing
        self.assertEqual(hashlib.sha256(marker.read_bytes()).hexdigest(), digest)
        # A stop arriving between archive and release also halts the run and
        # blocks the handoff that the archived attempt would otherwise authorize.
        mid = recovery(self.root / "mid-recovery")
        mid.archive(RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
        mid.stop(RUN, "operator stopped mid-recovery")
        self.assertBlocked(mid.lease_release, "BLOCKED_STOPPED", RUN, {"pid": self.dead.pid}, True)
        self.assertBlocked(mid.source_handoff, "BLOCKED_STOPPED", SOURCE, NEXT_SOURCE, REVIEW)
        self.assertEqual(mid.run_record(RUN)["phase"], "ARCHIVED")
        marker2 = self.root / "mid-recovery" / "stops" / (RUN + ".json")
        self.assertEqual(json.loads(marker2.read_text())["reason"], "operator stopped mid-recovery")

    # ---- crash boundaries ------------------------------------------------------------

    def test_crash_between_evidence_copy_and_record_write_replays_onto_the_copy(self):
        digest = hashlib.sha256(self.evidence_bytes()).hexdigest()
        copy_path = self.root / "evidence" / RUN / (digest + ".json")
        copy_path.parent.mkdir(parents=True, exist_ok=True)
        copy_path.write_bytes(self.evidence_bytes())
        record = self.archive()
        self.assertEqual(Path(record["evidence"]["retained"]).name, digest + ".json")
        forged = self.root / "evidence" / "run_forged_0000001" / ("e" * 64 + ".json")
        forged.parent.mkdir(parents=True, exist_ok=True)
        forged.write_bytes(self.evidence_bytes(run_id="run_forged_0000001"))
        self.assertBlocked(self.recovery.archive, "BLOCKED_MISMATCH",
                           "run_forged_0000001", EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)

    def test_tampered_original_or_retained_evidence_is_rejected_on_replay(self):
        self.archive()
        record = self.recovery.run_record(RUN)
        self.rewrite_evidence(session={"id": DEAD_SESSION, "exit_code": 0, "cleanup_complete": True})
        self.assertBlocked(self.recovery.archive, "BLOCKED_TAMPERED",
                           RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
        self.rewrite_evidence()
        self.assertEqual(self.archive(), record)
        retained = self.root / record["evidence"]["retained"]
        retained.write_bytes(self.evidence_bytes(candidate="a" * 40))
        self.assertBlocked(self.recovery.archive, "BLOCKED_TAMPERED",
                           RUN, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
        self.assertEqual(self.recovery.run_record(RUN), record)

    def test_failed_transitions_replace_no_state(self):
        self.archive()
        live = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
        self.addCleanup(live.wait)   # cleanups run in reverse: kill first, then reap
        self.addCleanup(live.kill)
        before = self.state_digest()
        self.assertBlocked(self.recovery.lease_release, "BLOCKED_LIVE_PROCESS", RUN, {"pid": live.pid}, True)
        self.assertBlocked(self.recovery.fresh_candidate, "BLOCKED_LEASE", RUN, NEXT_SOURCE, EPOCH + 1)
        self.assertEqual(self.state_digest(), before)
        self.recovery.lease_release(RUN, {"pid": self.dead.pid}, docker_cleaned=True)
        before = self.state_digest()
        self.assertBlocked(self.recovery.fresh_candidate, "BLOCKED_HANDOFF", RUN, NEXT_SOURCE, EPOCH + 1)
        self.assertEqual(self.state_digest(), before)

    # ---- unarchived, foreign, or malformed work ---------------------------------------

    def test_transitions_reject_work_that_was_never_archived(self):
        self.assertBlocked(self.recovery.lease_release, "BLOCKED_STATE", RUN, {"pid": self.dead.pid}, True)
        self.assertBlocked(self.recovery.fresh_candidate, "BLOCKED_STATE", RUN, NEXT_SOURCE, EPOCH + 1)
        self.assertBlocked(self.recovery.source_handoff, "BLOCKED_HANDOFF", SOURCE, NEXT_SOURCE, REVIEW)
        self.assertIsNone(self.recovery.handoff_record())

    def test_handoff_requires_a_real_change_and_a_reviewed_acceptance(self):
        self.archive()
        for old, new, review in ((SOURCE, SOURCE, REVIEW), (SOURCE, "nothex", REVIEW), (SOURCE, NEXT_SOURCE, "short"),
                                 (SOURCE, NEXT_SOURCE, "g" * 64), (SOURCE, NEXT_SOURCE, "F" * 64),
                                 ("e" * 40, NEXT_SOURCE, REVIEW), (SOURCE, SOURCE[:39], REVIEW)):
            with self.subTest(old=old, new=new, review=review):
                self.assertBlocked(self.recovery.source_handoff, "BLOCKED_HANDOFF|BLOCKED_STATE", old, new, review)
        self.assertIsNone(self.recovery.handoff_record())
        self.recovery.source_handoff(SOURCE, NEXT_SOURCE, REVIEW)

    def test_identity_fields_are_validated_before_any_state_is_read(self):
        for run_id in ("short", "bad/id", "", None, 7):
            with self.subTest(run_id=run_id):
                self.assertBlocked(self.recovery.archive, "BLOCKED_STATE",
                                   run_id, EPOCH, REVISION, OPERATION, CANDIDATE, self.evidence_path, True)
        for epoch, revision in ((0, REVISION), (EPOCH, 0), ("5", REVISION), (EPOCH, None)):
            with self.subTest(epoch=epoch, revision=revision):
                self.assertBlocked(self.recovery.archive, "BLOCKED_STATE",
                                   RUN, epoch, revision, OPERATION, CANDIDATE, self.evidence_path, True)
        for operation in ("short", "g" * 31, None):
            with self.subTest(operation=operation):
                self.assertBlocked(self.recovery.archive, "BLOCKED_STATE",
                                   RUN, EPOCH, REVISION, operation, CANDIDATE, self.evidence_path, True)
        self.assertBlocked(self.recovery.archive, "BLOCKED_EVIDENCE",
                           RUN, EPOCH, REVISION, OPERATION, CANDIDATE, None, True)
        self.assertIsNone(self.recovery.run_record(RUN))


if __name__ == "__main__":
    unittest.main()
