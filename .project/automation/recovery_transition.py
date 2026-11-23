"""The smallest safe controller transition after a failed verification.

Pure standard-library module. A failed verification run (run, task,
configuration epoch, revision, operation and candidate identified exactly) is
archived with all of its evidence; only a proven-clean lease is released; an
explicitly accepted review permits one source handoff; and a fresh candidate
starts under the accepted source without fabricating success or rewriting the
failed operation. Every transition is idempotent, preserves failure history,
and rejects running, uncertain, tampered, stopped, published or accepted work.
A review or generic CI PASS never becomes task acceptance: this module records
no acceptance and the fresh candidate starts as pending verification.
"""

from contextlib import contextmanager
import copy
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
import time

SCHEMA_VERSION = 1
FAILED_OUTCOME = "FAILED_VERIFICATION"
FRESH_STATUS = "PENDING_VERIFICATION"
RUN = re.compile(r"[a-zA-Z0-9_-]{8,80}")
OPERATION = re.compile(r"[0-9a-f]{32}")
COMMIT = re.compile(r"[0-9a-f]{40}")
REVIEW = re.compile(r"[0-9a-f]{64}")
MAX_EVIDENCE_BYTES = 64 * 1024 * 1024
# The retained evidence must identify the exact failed attempt it belongs to.
EVIDENCE_IDENTITY = ("schema_version", "run_id", "epoch", "revision", "operation", "candidate", "source", "outcome")


class BlockedRecovery(Exception):
    """A recovery transition refused unsafe, stale, or unproven work."""

    def __init__(self, code, message):
        self.code = code
        super().__init__(code + ": " + message)


def require(condition, code, message):
    if not condition:
        raise BlockedRecovery(code, message)


def canonical_bytes(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()


def parse_json(data):
    try:
        value = json.loads(data.decode(), object_pairs_hook=unique_object)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise BlockedRecovery("BLOCKED_EVIDENCE", "retained evidence is not a readable document: " + str(error)) from error
    require(type(value) is dict, "BLOCKED_EVIDENCE", "retained evidence is not an identity document")
    return value


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "BLOCKED_EVIDENCE", "retained evidence repeats a JSON field")
        result[key] = value
    return result


def sync_directory(path):
    descriptor = os.open(path, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


class FailedRunRecovery:
    """Durable, replayable transitions over one recovery state directory."""

    def __init__(self, root, clock=time.time):
        self.root = Path(root)
        self.clock = clock

    # ---- durable state helpers -------------------------------------------------

    def _path(self, *parts):
        return self.root.joinpath(*parts)

    @contextmanager
    def _transition(self):
        """Serialize transitions so a replay observes exactly one durable state."""
        self._durable_directory(self.root)
        descriptor = os.open(self._path("transition.lock"), os.O_RDWR | os.O_CREAT, 0o600)
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX)
            yield
        finally:
            os.close(descriptor)

    def _durable_directory(self, path):
        path = Path(path)
        path.mkdir(parents=True, exist_ok=True)
        sync_directory(path)

    def _write_json(self, path, value):
        path = Path(path)
        self._durable_directory(path.parent)
        descriptor, temporary = tempfile.mkstemp(prefix=".pending-", dir=path.parent)
        try:
            with os.fdopen(descriptor, "wb") as stream:
                stream.write(canonical_bytes(value))
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, path)
        except BaseException:
            Path(temporary).unlink(missing_ok=True)
            raise
        sync_directory(path.parent)

    def _read_json(self, path):
        if not path.exists():
            return None
        return parse_json(path.read_bytes())

    def run_record(self, run_id):
        return self._read_json(self._path("runs", run_id + ".json"))

    def handoff_record(self):
        return self._read_json(self._path("handoff.json"))

    # ---- shared validation -----------------------------------------------------

    def _run_id(self, run_id):
        require(isinstance(run_id, str) and RUN.fullmatch(run_id), "BLOCKED_STATE", "invalid run ID")
        return run_id

    def _count(self, value, label):
        require(type(value) is int and value >= 1, "BLOCKED_STATE", "invalid " + label)
        return value

    def _hex(self, value, pattern, label):
        require(isinstance(value, str) and pattern.fullmatch(value), "BLOCKED_STATE", "invalid " + label)
        return value

    def stopped(self, run_id):
        self._run_id(run_id)
        return self._path("stops", run_id + ".json").exists()

    def stop(self, run_id, reason):
        """Record the operator's durable stop request; recovery never removes it."""
        self._run_id(run_id)
        require(isinstance(reason, str) and 0 < len(reason) <= 512, "BLOCKED_STATE", "invalid stop reason")
        with self._transition():
            path = self._path("stops", run_id + ".json")
            if not path.exists():
                self._write_json(path, {"schema_version": SCHEMA_VERSION, "run_id": run_id,
                                        "reason": reason, "requested_at": self.clock()})

    def _forbid_stop(self, run_id):
        require(not self.stopped(run_id), "BLOCKED_STOPPED",
                "an operator stop forbids recovering this run; its stop state is preserved")

    # ---- 1. archive the failed attempt ------------------------------------------

    def archive(self, run_id, epoch, revision, operation, candidate, evidence_path, cleanup_verified):
        """Record the exact failed attempt and preserve all of its evidence.

        The retained evidence document must identify this attempt exactly and
        must carry FAILED_VERIFICATION as its outcome, so a review or CI PASS
        can never be archived as a failed verification or become acceptance.
        """
        self._run_id(run_id)
        self._count(epoch, "configuration epoch")
        self._count(revision, "run revision")
        self._hex(operation, OPERATION, "operation identity")
        self._hex(candidate, COMMIT, "candidate identity")
        require(cleanup_verified is True, "BLOCKED_CLEANUP",
                "the failed attempt has no verified cleanup; recovery cannot touch uncertain work")
        require(isinstance(evidence_path, (str, Path)), "BLOCKED_EVIDENCE",
                "retained evidence needs a real path")
        with self._transition():
            self._forbid_stop(run_id)
            record = self.run_record(run_id)
            if record is not None:
                return self._replay_archive(record, epoch, revision, operation, candidate, evidence_path)
            evidence_file = Path(evidence_path)
            require(evidence_file.is_file(), "BLOCKED_EVIDENCE",
                    "the failed attempt has no retained evidence file to archive")
            raw = evidence_file.read_bytes()
            require(0 < len(raw) <= MAX_EVIDENCE_BYTES, "BLOCKED_EVIDENCE",
                    "retained evidence is empty or exceeds its bound")
            document = parse_json(raw)
            for key in EVIDENCE_IDENTITY:
                require(key in document, "BLOCKED_EVIDENCE", "retained evidence omits its " + key)
            require(document["schema_version"] == SCHEMA_VERSION and document["run_id"] == run_id
                    and document["epoch"] == epoch and document["revision"] == revision
                    and document["operation"] == operation and document["candidate"] == candidate,
                    "BLOCKED_MISMATCH",
                    "the retained evidence identifies another run, epoch, revision, operation, or candidate")
            source = self._hex(document["source"], COMMIT, "frozen source identity")
            require(document["outcome"] == FAILED_OUTCOME, "BLOCKED_OUTCOME",
                    "only a failed verification can be archived; a review or CI PASS is not task acceptance")
            digest = hashlib.sha256(raw).hexdigest()
            retained = self._retain_evidence(run_id, digest, raw)
            now = self.clock()
            record = {"schema_version": SCHEMA_VERSION, "run_id": run_id, "phase": "ARCHIVED",
                      "epoch": epoch, "revision": revision, "operation": operation, "candidate": candidate,
                      "source": source, "outcome": FAILED_OUTCOME, "cleanup_verified": True,
                      "evidence": {"path": str(evidence_file), "sha256": digest, "size_bytes": len(raw),
                                   "retained": retained.relative_to(self.root).as_posix()},
                      "archived_at": now, "lease": None, "fresh": None,
                      "history": [{"transition": "archive", "at": now}]}
            self._write_json(self._path("runs", run_id + ".json"), record)
            return copy.deepcopy(record)

    def _replay_archive(self, record, epoch, revision, operation, candidate, evidence_path):
        if (record["epoch"], record["revision"]) != (epoch, revision):
            raise BlockedRecovery("BLOCKED_STALE_CAS",
                                  "the archived attempt is bound to epoch " + str(record["epoch"]) + " revision "
                                  + str(record["revision"]) + ", not the caller's " + str(epoch) + "/" + str(revision))
        if (record["operation"], record["candidate"]) != (operation, candidate):
            raise BlockedRecovery("BLOCKED_MISMATCH",
                                  "the archived attempt records another operation or candidate")
        evidence_file = Path(evidence_path)
        if evidence_file.is_file():
            digest = hashlib.sha256(evidence_file.read_bytes()).hexdigest()
            require(digest == record["evidence"]["sha256"], "BLOCKED_TAMPERED",
                    "the retained evidence changed after it was archived")
        # else: normal post-archive cleanup may have removed the original copy.
        self._verify_retained_evidence(record)
        return copy.deepcopy(record)

    def _retain_evidence(self, run_id, digest, raw):
        """Copy the evidence bytes once; a crash before the record write replays onto them."""
        target = self._path("evidence", run_id, digest + ".json")
        self._durable_directory(target.parent)
        if target.exists():
            require(target.read_bytes() == raw, "BLOCKED_TAMPERED",
                    "a retained evidence copy differs from its identity")
            return target
        descriptor, temporary = tempfile.mkstemp(prefix=".pending-", dir=target.parent)
        try:
            with os.fdopen(descriptor, "wb") as stream:
                stream.write(raw)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, target)
        except BaseException:
            Path(temporary).unlink(missing_ok=True)
            raise
        sync_directory(target.parent)
        return target

    def _verify_retained_evidence(self, record):
        retained = self._path(record["evidence"]["retained"])
        require(retained.is_file(), "BLOCKED_TAMPERED", "the archived evidence copy is missing")
        digest = hashlib.sha256(retained.read_bytes()).hexdigest()
        require(digest == record["evidence"]["sha256"], "BLOCKED_TAMPERED",
                "the archived evidence copy differs from its archived identity")

    # ---- 2. release only a proven-clean lease ------------------------------------

    def lease_release(self, run_id, process_owner, docker_cleaned):
        """Release the archived attempt's lease once its process is proven dead
        and Docker cleanup is verified; replays return the released state."""
        self._run_id(run_id)
        require(type(process_owner) is dict and set(process_owner) == {"pid"} and type(process_owner["pid"]) is int
                and process_owner["pid"] > 0, "BLOCKED_STATE", "process ownership needs an exact live process identity")
        pid = process_owner["pid"]
        with self._transition():
            self._forbid_stop(run_id)
            record = self.run_record(run_id)
            require(record is not None, "BLOCKED_STATE",
                    "the failed attempt must be archived before its lease can be released")
            if record.get("lease") is not None:
                require(record["lease"]["pid"] == pid and docker_cleaned is True, "BLOCKED_MISMATCH",
                        "the lease was released with another ownership proof")
                return copy.deepcopy(record)  # durable release: a replay needs no re-verification
            require(docker_cleaned is True, "BLOCKED_CLEANUP",
                    "Docker cleanup is not verified; an uncertain lease is never released")
            self._require_dead_process(pid)
            now = self.clock()
            record["phase"] = "LEASE_RELEASED"
            record["lease"] = {"pid": pid, "docker_cleaned": True, "process_dead": True, "released_at": now}
            record["history"].append({"transition": "lease_release", "at": now, "pid": pid})
            self._write_json(self._path("runs", run_id + ".json"), record)
            return copy.deepcopy(record)

    @staticmethod
    def _require_dead_process(pid):
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return
        except PermissionError:
            raise BlockedRecovery("BLOCKED_LIVE_PROCESS", "the lease owner process is still alive") from None
        raise BlockedRecovery("BLOCKED_LIVE_PROCESS", "the lease owner process is still alive")

    # ---- 3. explicit accepted-source handoff --------------------------------------

    def source_handoff(self, old_source, new_source, accepted_review_sha):
        """Permit exactly one source change, authorized by an archived failed
        attempt that still records the old frozen source and an accepted review."""
        self._hex(old_source, COMMIT, "old source identity")
        self._hex(new_source, COMMIT, "new source identity")
        self._hex(accepted_review_sha, REVIEW, "accepted review digest")
        with self._transition():
            accepted = self.handoff_record()
            if accepted is not None:
                if (accepted["old_source"], accepted["new_source"], accepted["accepted_review_sha"]) \
                        == (old_source, new_source, accepted_review_sha):
                    return copy.deepcopy(accepted)
                raise BlockedRecovery("BLOCKED_MISMATCH",
                                      "the accepted source handoff is frozen; a different change needs a new reviewed acceptance")
            require(old_source != new_source, "BLOCKED_HANDOFF", "a source handoff must change the accepted source")
            authorizing = [record for record in self._archived_attempts() if record["source"] == old_source]
            require(authorizing, "BLOCKED_HANDOFF",
                    "no archived failed attempt still records the old frozen source")
            for record in authorizing:
                self._forbid_stop(record["run_id"])
            accepted = {"schema_version": SCHEMA_VERSION, "old_source": old_source, "new_source": new_source,
                        "accepted_review_sha": accepted_review_sha, "accepted_at": self.clock(),
                        "authorized_by": sorted(record["run_id"] for record in authorizing)}
            self._write_json(self._path("handoff.json"), accepted)
            return copy.deepcopy(accepted)

    def _archived_attempts(self):
        attempts = []
        if (self.root / "runs").exists():
            for path in sorted((self.root / "runs").glob("*.json")):
                record = self._read_json(path)
                require(record is not None and record.get("outcome") == FAILED_OUTCOME,
                        "BLOCKED_STATE", "a recovery run record is not an archived failed attempt")
                attempts.append(record)
        return attempts

    # ---- 4. fresh candidate under the accepted source ------------------------------

    def fresh_candidate(self, run_id, source, epoch):
        """Start a fresh candidate under the accepted source: the lease must be
        released, the source must be the accepted handoff target, and the
        configuration epoch must advance by exactly one. The failed operation
        and candidate stay recorded and nothing is marked accepted."""
        self._run_id(run_id)
        self._hex(source, COMMIT, "fresh source identity")
        self._count(epoch, "configuration epoch")
        with self._transition():
            self._forbid_stop(run_id)
            record = self.run_record(run_id)
            require(record is not None, "BLOCKED_STATE",
                    "a fresh candidate requires the archived failed attempt of the same run")
            if record["phase"] == "FRESH_CANDIDATE":
                fresh = record["fresh"]
                if fresh["source"] == source and fresh["epoch"] == epoch:
                    return copy.deepcopy(record)
                raise BlockedRecovery("BLOCKED_MISMATCH", "this run already started a different fresh candidate")
            require(record.get("lease") is not None, "BLOCKED_LEASE",
                    "the failed attempt's lease is not released; fresh work would race it")
            accepted = self.handoff_record()
            require(accepted is not None, "BLOCKED_HANDOFF", "no accepted source handoff exists")
            require(run_id in accepted["authorized_by"] and accepted["old_source"] == record["source"],
                    "BLOCKED_HANDOFF", "the accepted handoff does not authorize this run's frozen source")
            require(accepted["new_source"] == source, "BLOCKED_HANDOFF",
                    "the fresh candidate must use the accepted source")
            require(epoch == record["epoch"] + 1, "BLOCKED_STALE_CAS",
                    "a fresh candidate advances the configuration epoch by exactly one")
            now = self.clock()
            record["phase"] = "FRESH_CANDIDATE"
            record["fresh"] = {"source": source, "epoch": epoch, "status": FRESH_STATUS, "created_at": now,
                               "parent": {"operation": record["operation"], "candidate": record["candidate"],
                                          "source": record["source"], "revision": record["revision"]}}
            record["history"].append({"transition": "fresh_candidate", "at": now, "source": source, "epoch": epoch})
            self._write_json(self._path("runs", run_id + ".json"), record)
            return copy.deepcopy(record)
