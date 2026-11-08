"""Independent malformed-source and publication failure regressions."""

import contextlib
import errno
import gzip
import io
import json
import lzma
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path[:0] = [str(Path(__file__).resolve().parent),
               str(Path(__file__).resolve().parents[2] / "scripts/versioning"),
               str(Path(__file__).resolve().parents[2] / "build/native")]
from fixtures import OfficialFixture, REGISTRY, LOCK, hash_bytes, packed
import update


class BrokenStream:
    def __init__(self, stream, phase):
        self.stream, self.phase = stream, phase

    def __getattr__(self, name):
        return getattr(self.stream, name)

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()

    def write(self, value):
        if self.phase == "write":
            self.stream.write(value[:5])
            raise OSError(errno.ENOSPC, "Synthetic disk full during write")
        return self.stream.write(value)

    def flush(self):
        if self.phase == "flush":
            raise OSError(errno.ENOSPC, "Synthetic disk full during flush")
        return self.stream.flush()

    def close(self):
        try:
            if self.phase == "flush":
                self.flush()
        finally:
            self.stream.close()
        if self.phase == "close":
            raise OSError(errno.EIO, "Synthetic close failure")


@contextlib.contextmanager
def stream_failure(phase):
    original_open, original_fdopen = Path.open, os.fdopen
    def path_open(path, mode="r", *args, **kwargs):
        stream = original_open(path, mode, *args, **kwargs)
        return BrokenStream(stream, phase) if mode in ("xb", "wb") else stream
    def fdopen(fd, mode="r", *args, **kwargs):
        stream = original_fdopen(fd, mode, *args, **kwargs)
        return BrokenStream(stream, phase) if mode == "wb" else stream
    with mock.patch.object(Path, "open", path_open), mock.patch.object(os, "fdopen", fdopen):
        yield


class SourceFailures(unittest.TestCase):
    def test_truncated_indexes_produce_incomplete_reports(self):
        for compression in ("gz", "xz"):
            for mode in ("check", "update"):
                with self.subTest(compression=compression, mode=mode), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory).resolve()
                    fixture = OfficialFixture(root)
                    active = fixture.active
                    original_url = active["package_indexes"][0]["url"]
                    url = original_url.rsplit(".", 1)[0] + "." + compression
                    active["package_indexes"][0]["url"] = url
                    payload = (gzip.compress(b"Package: synthetic\n", mtime=0) if compression == "gz"
                               else lzma.compress(b"Package: synthetic\n"))[:-4]
                    fixture.add(url, payload)
                    lock = root / "active.json"
                    lock.write_bytes(packed(active))
                    before = lock.stat()
                    report, candidate = root / "report.json", root / "candidate.json"
                    args = [mode, "--registry", str(REGISTRY), "--active-lock", str(lock), "--expected-lock-sha256", hash_bytes(lock.read_bytes()),
                            "--report", str(report), "--fixture-transport", str(fixture.save())]
                    if mode == "update":
                        args += ["--output", str(candidate)]
                    with contextlib.redirect_stderr(io.StringIO()):
                        status = update.main(args)
                    self.assertEqual(status, 1)
                    value = json.loads(report.read_bytes())
                    self.assertFalse(value["complete"])
                    self.assertIsNone(value["candidate"])
                    self.assertEqual(len(value["components"]), 41)
                    packages = [row for row in value["components"] if row["id"].startswith("debian-") and row["id"] != "debian-runtime-image"]
                    self.assertTrue(all(row["blocked_reasons"] for row in packages))
                    self.assertFalse(candidate.exists())
                    self.assertEqual(lock.read_bytes(), packed(active))
                    self.assertEqual(lock.stat().st_mtime_ns, before.st_mtime_ns)


class PublicationFailures(unittest.TestCase):
    def test_partial_write_flush_and_close_leave_no_output(self):
        for phase in ("write", "flush", "close"):
            with self.subTest(phase=phase), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                target = root / "candidate.json"
                with stream_failure(phase), self.assertRaises(OSError):
                    update.stage_json(root, "candidate", {"candidate": "synthetic"})
                self.assertFalse(target.exists())
                self.assertEqual(list(root.iterdir()), [])

    def test_existing_output_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            target = root / "candidate.json"
            target.write_bytes(b"another owner")
            stage = root / "evidence"
            stage.mkdir()
            with self.assertRaises(FileExistsError):
                update.publish_outputs(stage, root / "report.json", {}, target, {"candidate": "synthetic"})
            self.assertEqual(target.read_bytes(), b"another owner")
            self.assertTrue((root / "report.json").is_file())
            self.assertEqual(len(list(stage.glob("unpromoted-*.json"))), 2)

    def test_concurrent_publication_collision_preserves_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            target = root / "candidate.json"
            stage = root / "evidence"
            stage.mkdir()
            original_link = os.link
            def collide(source, destination, *args, **kwargs):
                if Path(destination) == target:
                    Path(destination).write_bytes(b"concurrent owner")
                return original_link(source, destination, *args, **kwargs)
            with mock.patch.object(os, "link", collide), self.assertRaises(FileExistsError):
                update.publish_outputs(stage, root / "report.json", {}, target, {"candidate": "synthetic"})
            self.assertEqual(target.read_bytes(), b"concurrent owner")
            self.assertTrue((root / "report.json").is_file())

    def test_candidate_and_report_io_failures_leave_no_candidate(self):
        for output in ("candidate", "report"):
            for phase in ("write", "flush", "close"):
                with self.subTest(output=output, phase=phase), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory).resolve()
                    fixture = OfficialFixture(root)
                    report, candidate = root / "report.json", root / "candidate.json"
                    original = update.stage_json
                    before, modified = LOCK.read_bytes(), LOCK.stat().st_mtime_ns
                    def prepare(directory, label, value):
                        if label == output:
                            with stream_failure(phase):
                                return original(directory, label, value)
                        return original(directory, label, value)
                    args = ["update", "--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(before),
                            "--report", str(report), "--output", str(candidate), "--fixture-transport", str(fixture.save())]
                    with mock.patch.object(update, "stage_json", prepare), contextlib.redirect_stderr(io.StringIO()):
                        self.assertEqual(update.main(args), 2)
                    self.assertFalse(candidate.exists())
                    self.assertFalse(report.exists())
                    self.assertEqual(list(root.glob(".*.tmp")), [])
                    for artifact in root.rglob("unpromoted-*.json"):
                        json.loads(artifact.read_bytes())
                    self.assertEqual(LOCK.read_bytes(), before)
                    self.assertEqual(LOCK.stat().st_mtime_ns, modified)

    def test_candidate_and_report_collisions_preserve_other_owner(self):
        for output in ("candidate", "report"):
            with self.subTest(output=output), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                fixture = OfficialFixture(root)
                report, candidate = root / "report.json", root / "candidate.json"
                target = root / (output + ".json")
                original_link = os.link
                def collide(source, destination, *args, **kwargs):
                    if Path(destination) == target:
                        target.write_bytes(b"concurrent owner")
                    return original_link(source, destination, *args, **kwargs)
                args = ["update", "--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(LOCK.read_bytes()),
                        "--report", str(report), "--output", str(candidate), "--fixture-transport", str(fixture.save())]
                with mock.patch.object(os, "link", collide), contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(update.main(args), 2)
                self.assertEqual(target.read_bytes(), b"concurrent owner")
                if output == "report":
                    self.assertFalse(candidate.exists())
                else:
                    observed = json.loads(report.read_bytes())
                    self.assertTrue(observed["complete"])
                    self.assertNotEqual(observed["candidate"]["sha256"], hash_bytes(candidate.read_bytes()))
                self.assertEqual(list(root.glob(".*.tmp")), [])

    def test_success_closes_writers_before_final_candidate_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            fixture = OfficialFixture(root)
            report, candidate = root / "report.json", root / "candidate.json"
            streams, order = [], []
            original_prepare, original_link, original_fdopen, original_unlink = update.stage_json, os.link, os.fdopen, Path.unlink
            def prepare(directory, label, value):
                artifact = original_prepare(directory, label, value)
                order.append("prepare:" + label)
                return artifact
            def opened(*args, **kwargs):
                stream = original_fdopen(*args, **kwargs)
                streams.append(stream)
                return stream
            def link(source, destination, *args, **kwargs):
                self.assertTrue(all(stream.closed for stream in streams))
                result = original_link(source, destination, *args, **kwargs)
                order.append("publish:" + Path(destination).stem)
                return result
            def unlink(path, *args, **kwargs):
                self.assertNotIn("publish:candidate", order, "Cleanup followed the final commit point")
                self.assertNotIn(path, (candidate, report), "A final output was deleted")
                return original_unlink(path, *args, **kwargs)
            args = ["update", "--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(LOCK.read_bytes()),
                    "--report", str(report), "--output", str(candidate), "--fixture-transport", str(fixture.save())]
            with mock.patch.object(update, "stage_json", prepare), mock.patch.object(os, "fdopen", opened), \
                 mock.patch.object(os, "link", link), mock.patch.object(Path, "unlink", unlink):
                self.assertEqual(update.main(args), 0)
            self.assertEqual(order, ["prepare:candidate", "prepare:report", "publish:report", "publish:candidate"])
            self.assertTrue(all(stream.closed for stream in streams))
            self.assertEqual(len(list(root.rglob("unpromoted-*.json"))), 2)
            self.assertTrue(report.is_file() and candidate.is_file())

    def test_report_failure_preserves_replaced_candidate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            fixture = OfficialFixture(root)
            report, candidate = root / "report.json", root / "candidate.json"
            original = update.stage_json
            def prepare(directory, label, value):
                if label == "report":
                    candidate.write_bytes(b"concurrent owner")
                    raise OSError(errno.ENOSPC, "Synthetic report failure")
                return original(directory, label, value)
            args = ["update", "--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(LOCK.read_bytes()),
                    "--report", str(report), "--output", str(candidate), "--fixture-transport", str(fixture.save())]
            with mock.patch.object(update, "stage_json", prepare), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(update.main(args), 2)
            self.assertEqual(candidate.read_bytes(), b"concurrent owner")
            self.assertFalse(report.exists())

    def test_final_commit_failure_retains_only_diagnostic_report(self):
        for replace_report in (False, True):
            with self.subTest(replace_report=replace_report), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                fixture = OfficialFixture(root)
                report, candidate = root / "report.json", root / "candidate.json"
                original_link = os.link
                def link(source, destination, *args, **kwargs):
                    if Path(destination) == candidate:
                        if replace_report:
                            replacement = root / "foreign.json"
                            replacement.write_bytes(b"foreign report")
                            os.replace(replacement, report)
                        raise OSError(errno.ENOSPC, "Synthetic final commit failure")
                    return original_link(source, destination, *args, **kwargs)
                args = ["update", "--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(LOCK.read_bytes()),
                        "--report", str(report), "--output", str(candidate), "--fixture-transport", str(fixture.save())]
                with mock.patch.object(os, "link", link), contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(update.main(args), 2)
                self.assertFalse(candidate.exists())
                if replace_report:
                    self.assertEqual(report.read_bytes(), b"foreign report")
                else:
                    value = json.loads(report.read_bytes())
                    self.assertTrue(value["complete"])
                    self.assertEqual(value["candidate"]["path"], "candidate.json")
                self.assertEqual(len(list(root.rglob("unpromoted-*.json"))), 2)
