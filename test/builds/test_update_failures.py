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
                    update.exclusive_json(target, {"candidate": "synthetic"})
                self.assertFalse(target.exists())
                self.assertEqual(list(root.iterdir()), [])

    def test_existing_output_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            target = root / "candidate.json"
            target.write_bytes(b"another owner")
            with self.assertRaises(FileExistsError):
                update.exclusive_json(target, {"candidate": "synthetic"})
            self.assertEqual(target.read_bytes(), b"another owner")
            self.assertEqual(list(root.iterdir()), [target])

    def test_concurrent_publication_collision_preserves_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            target = root / "candidate.json"
            original_link = os.link
            def collide(source, destination, *args, **kwargs):
                Path(destination).write_bytes(b"concurrent owner")
                return original_link(source, destination, *args, **kwargs)
            with mock.patch.object(os, "link", collide), self.assertRaises(FileExistsError):
                update.exclusive_json(target, {"candidate": "synthetic"})
            self.assertEqual(target.read_bytes(), b"concurrent owner")
            self.assertEqual(list(root.iterdir()), [target])

    def test_candidate_and_report_io_failures_leave_no_candidate(self):
        for output in ("candidate", "report"):
            for phase in ("write", "flush", "close"):
                with self.subTest(output=output, phase=phase), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory).resolve()
                    fixture = OfficialFixture(root)
                    report, candidate = root / "report.json", root / "candidate.json"
                    original = update.exclusive_json
                    before, modified = LOCK.read_bytes(), LOCK.stat().st_mtime_ns
                    def publish(path, value):
                        if Path(path) == root / (output + ".json"):
                            with stream_failure(phase):
                                return original(path, value)
                        return original(path, value)
                    args = ["update", "--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(before),
                            "--report", str(report), "--output", str(candidate), "--fixture-transport", str(fixture.save())]
                    with mock.patch.object(update, "exclusive_json", publish), contextlib.redirect_stderr(io.StringIO()):
                        self.assertEqual(update.main(args), 2)
                    self.assertFalse(candidate.exists())
                    self.assertFalse(report.exists())
                    self.assertEqual(list(root.glob(".*.tmp")), [])
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
                self.assertFalse((report if output == "candidate" else candidate).exists())
                self.assertEqual(list(root.glob(".*.tmp")), [])

    def test_success_closes_retained_handles_and_removes_staging(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            fixture = OfficialFixture(root)
            report, candidate = root / "report.json", root / "candidate.json"
            owners = []
            original = update.exclusive_json
            def publish(path, value):
                owner = original(path, value)
                owners.append(owner)
                return owner
            args = ["update", "--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(LOCK.read_bytes()),
                    "--report", str(report), "--output", str(candidate), "--fixture-transport", str(fixture.save())]
            with mock.patch.object(update, "exclusive_json", publish):
                self.assertEqual(update.main(args), 0)
            self.assertEqual(len(owners), 2)
            self.assertTrue(all(owner.fd is None and not owner.staging.exists() for owner in owners))
            self.assertTrue(report.is_file() and candidate.is_file())

    def test_report_failure_preserves_replaced_candidate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            fixture = OfficialFixture(root)
            report, candidate = root / "report.json", root / "candidate.json"
            original = update.exclusive_json
            def publish(path, value):
                if Path(path) == report:
                    candidate.unlink()
                    candidate.write_bytes(b"concurrent owner")
                    raise OSError(errno.ENOSPC, "Synthetic report failure")
                return original(path, value)
            args = ["update", "--registry", str(REGISTRY), "--active-lock", str(LOCK), "--expected-lock-sha256", hash_bytes(LOCK.read_bytes()),
                    "--report", str(report), "--output", str(candidate), "--fixture-transport", str(fixture.save())]
            with mock.patch.object(update, "exclusive_json", publish), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(update.main(args), 2)
            self.assertEqual(candidate.read_bytes(), b"concurrent owner")
            self.assertFalse(report.exists())
