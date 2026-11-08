"""Regressions for the rejected first harness candidate."""

import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock
import builtins

from test_harness import HERE, admitted_fixture, ref, write_json


class ReadProbe:
    def __init__(self, stream, counts, grow=None):
        self.stream, self.counts, self.grow = stream, counts, grow

    def read(self, count=-1):
        if self.grow:
            grow, self.grow = self.grow, None
            grow()
        data = self.stream.read(count)
        self.counts.append(len(data))
        return data

    def __enter__(self):
        return self

    def fileno(self):
        if self.grow:
            grow, self.grow = self.grow, None
            grow()
        return self.stream.fileno()

    def __exit__(self, *args):
        self.stream.close()

    def __getattr__(self, name):
        return getattr(self.stream, name)


class BoundRepairs(unittest.TestCase):
    def setUp(self):
        sys.path.insert(0, str(HERE))

    def tearDown(self):
        sys.path.pop(0)

    def test_declared_one_byte_cannot_read_or_copy_two_megabytes(self):
        from artifacts import artifact
        from native import copy_verified
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = root / "source"
            source.write_bytes(b"x" * (2 * 1024 * 1024))
            expected = {"path": str(source), "size_bytes": 1, "sha256": hashlib.sha256(b"x").hexdigest()}
            counts = []
            original = Path.open

            def opening(path, *args, **kwargs):
                stream = original(path, *args, **kwargs)
                return ReadProbe(stream, counts) if path == source else stream

            with mock.patch.object(Path, "open", opening):
                with self.assertRaises(ValueError):
                    artifact(expected)
            self.assertEqual(sum(counts), 0, "Oversized opened files must reject before a body read")
            destination = root / "copy"
            with self.assertRaises(ValueError):
                copy_verified(source, destination, expected)
            self.assertFalse(destination.exists(), "Static oversize must reject before copying")

    def test_declared_one_byte_cannot_create_a_two_megabyte_copy(self):
        from native import copy_verified
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = root / "source"
            source.write_bytes(b"x" * (2 * 1024 * 1024))
            expected = {"path": str(source), "size_bytes": 1, "sha256": hashlib.sha256(b"x").hexdigest()}
            with self.assertRaises(ValueError):
                copy_verified(source, root / "copy", expected)
            self.assertFalse((root / "copy").exists(), "Oversize was copied before rejection")

    def test_growth_never_reads_or_copies_more_than_the_admitted_size(self):
        from artifacts import artifact
        from native import copy_verified
        for operation in ("read", "copy"):
            with self.subTest(operation=operation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                source = root / "source"
                source.write_bytes(b"x")
                expected = ref(source)
                inode = source.stat().st_ino
                counts = []
                grown = []
                original_open, original_read, original_builtin = Path.open, os.read, builtins.open

                def grow():
                    if not grown:
                        grown.append(True)
                        with original_open(source, "ab") as stream:
                            stream.write(b"y" * (2 * 1024 * 1024))

                def opening(path, *args, **kwargs):
                    stream = original_open(path, *args, **kwargs)
                    return ReadProbe(stream, counts, grow) if path == source else stream

                def reading(fd, count):
                    if os.fstat(fd).st_ino == inode:
                        grow()
                        data = original_read(fd, count)
                        counts.append(len(data))
                        return data
                    return original_read(fd, count)

                def builtin_open(path, *args, **kwargs):
                    stream = original_builtin(path, *args, **kwargs)
                    return ReadProbe(stream, counts, grow) if Path(path) == source else stream

                with mock.patch.object(Path, "open", opening), mock.patch("os.read", reading), mock.patch("builtins.open", builtin_open):
                    with self.assertRaises(ValueError):
                        if operation == "read":
                            artifact(expected)
                        else:
                            copy_verified(source, root / "copy", expected)
                self.assertTrue(grown)
                self.assertLessEqual(sum(counts), 1)
                if (root / "copy").exists():
                    self.assertLessEqual((root / "copy").stat().st_size, 1)

    def test_expired_aggregate_budget_rejects_before_build_artifact_io(self):
        from run import measured
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            inputs = root / "inputs"
            inputs.mkdir()
            request, _ = admitted_fixture(inputs)
            request["deadline_seconds"] = 1
            write_json(inputs / "request.json", request)
            counts = []
            original = Path.open

            def opening(path, *args, **kwargs):
                stream = original(path, *args, **kwargs)
                return ReadProbe(stream, counts) if path == inputs / "manifest.json" else stream

            with mock.patch.object(Path, "open", opening):
                with self.assertRaises(ValueError):
                    measured(inputs / "request.json", root / "output", time.monotonic() - 2)
            self.assertEqual(sum(counts), 0)
            self.assertFalse((root / "output").exists())

    def test_many_small_reads_share_one_run_budget(self):
        from run import measured
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            inputs = root / "inputs"
            inputs.mkdir()
            request, _ = admitted_fixture(inputs)
            request["deadline_seconds"] = 1
            write_json(inputs / "request.json", request)
            clock = [10.0]
            original = os.read
            counts = []

            def reading(descriptor, count):
                data = original(descriptor, count)
                clock[0] += 0.25
                counts.append(len(data))
                return data

            with mock.patch("artifacts.time.monotonic", lambda: clock[0]), mock.patch("os.read", reading):
                with self.assertRaisesRegex(ValueError, "Aggregate deadline expired"):
                    measured(inputs / "request.json", root / "output", 10.0)
            self.assertEqual(len(counts), 4)
            self.assertFalse((root / "output").exists())

    def test_source_snapshot_cannot_reset_or_outlive_the_shared_budget(self):
        from artifacts import source_snapshot
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            clock = [10.0]
            original = os.read

            def reading(descriptor, count):
                data = original(descriptor, count)
                clock[0] = 11.0
                return data

            with mock.patch("artifacts.time.monotonic", lambda: clock[0]), mock.patch("os.read", reading):
                with self.assertRaisesRegex(ValueError, "Aggregate deadline expired"):
                    source_snapshot(root, ("artifacts.py", "native.py"), 10.5)
            self.assertEqual(list((root / "harness-code").iterdir()), [])

    def test_failed_bounded_proof_read_fences_later_dispatch(self):
        from native import Native
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            docker = root / "docker"
            docker.write_text("#!/usr/bin/env python3\n")
            docker.chmod(0o700)
            output = root / "output"
            output.mkdir()
            with mock.patch.dict(os.environ, {"PATH": str(root) + os.pathsep + os.environ["PATH"]}):
                runner = Native(output, time.monotonic() + 60)
                original = os.open

                def opening(path, *args, **kwargs):
                    if str(path).endswith("000.result.json"):
                        raise OSError("Proof input is unavailable")
                    return original(path, *args, **kwargs)

                with mock.patch("os.open", opening):
                    with self.assertRaisesRegex(OSError, "Proof input"):
                        runner.command("unused-pinned-test-image", [], [])
                with self.assertRaisesRegex(ValueError, "No dispatch after"):
                    runner.command("unused-pinned-test-image", [], [])


class GroupRepair(unittest.TestCase):
    def test_parent_group_death_does_not_kill_the_cleanup_guardian(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            inputs = root / "inputs"
            inputs.mkdir()
            admitted_fixture(inputs)
            docker = root / "docker"
            docker.write_text("#!/usr/bin/env python3\nimport os,sys,time\nfrom pathlib import Path\n"
                              "if sys.argv[1]=='run':\n"
                              " Path(__file__).with_name('native.pid').write_text(str(os.getpid()))\n time.sleep(30)\n")
            docker.chmod(0o700)
            process = subprocess.Popen([sys.executable, "-I", str(HERE / "run.py"), "run",
                                        str(inputs / "request.json"), "--output", str(root / "output")],
                                       env=dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"]),
                                       start_new_session=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            native_pid = None
            try:
                deadline = time.monotonic() + 5
                while not (root / "native.pid").exists() and time.monotonic() < deadline:
                    time.sleep(0.02)
                native_pid = int((root / "native.pid").read_text())
                os.killpg(process.pid, signal.SIGKILL)
                process.communicate(timeout=5)
                result = root / "output/logs/000.result.json"
                deadline = time.monotonic() + 4
                while not result.exists() and time.monotonic() < deadline:
                    time.sleep(0.02)
                self.assertTrue(result.exists(), "Guardian died with its parent's process group")
                proof = json.loads(result.read_text())
                self.assertEqual(proof["reason"], "PARENT_EXIT")
                self.assertTrue(proof["cleanup_complete"] and proof["process_group_absent"])
                with self.assertRaises(ProcessLookupError):
                    os.kill(native_pid, 0)
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.communicate(timeout=5)
                if native_pid is not None:
                    state = subprocess.run(["ps", "-p", str(native_pid), "-o", "command="], capture_output=True, text=True)
                    if str(docker) in state.stdout:
                        os.killpg(native_pid, signal.SIGKILL)


class IdentityRepair(unittest.TestCase):
    def test_native_header_logs_fail_closed_on_missing_ambiguous_or_malformed_identity(self):
        sys.path.insert(0, str(HERE))
        try:
            from compare import compare_headers
            for invalid in ("missing", "duplicate", "malformed", "duplicate-key", "swapped", 1, 1.0, True, "01", "+1", " 1"):
                with self.subTest(invalid=invalid), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory).resolve()
                    logs = []
                    values = []
                    for number in (0, 1, 2):
                        value = {"msg": "State dump configured", "block": str(number), "hash": "0x" + str(number + 1) * 64}
                        path = root / str(number)
                        write_json(path, value)
                        logs.append(path)
                        values.append(value)
                    admitted = {"header_observations": logs, "fixture": {
                        "identity": {"genesis_hash": values[0]["hash"]},
                        "prepared_blocks": [{"hash": values[1]["hash"]}, {"hash": values[2]["hash"]}]}}
                    compare_headers(admitted)
                    if invalid == "missing": logs[1].write_text("")
                    elif invalid == "duplicate": logs[1].write_text(json.dumps(values[1]) + "\n" + json.dumps(values[1]) + "\n")
                    elif invalid == "malformed": logs[1].write_text("not JSON\n")
                    elif invalid == "duplicate-key": logs[1].write_text('{"msg":"State dump configured","block":"1","block":"1","hash":"' + values[1]["hash"] + '"}\n')
                    elif invalid == "swapped": admitted["header_observations"] = [logs[1], logs[0], logs[2]]
                    else:
                        values[1]["block"] = invalid
                        write_json(logs[1], values[1])
                    with self.assertRaisesRegex(ValueError, "Native header identity"):
                        compare_headers(admitted)
        finally:
            sys.path.pop(0)

    def test_changed_genesis_and_block_hashes_cannot_enter_a_pass(self):
        sys.path.insert(0, str(HERE))
        try:
            from artifacts import admit
            from compare import compare
            for target in ("genesis", "block"):
                with self.subTest(target=target), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory).resolve()
                    request, fixture = admitted_fixture(root)
                    fixture["identity"]["genesis_hash"] = "0x" + "3" * 64
                    write_json(root / "fixture.json", fixture)
                    request["fixture"] = ref(root / "fixture.json")
                    write_json(root / "request.json", request)
                    dumps, headers = [], []
                    for number, coinbase in ((1, "2000023625000000000"), (2, "4000023625000000000")):
                        path = root / f"state-{number}.json"
                        write_json(path, {"root": "2" * 64, "accounts": {
                            "7e5f4552091a69125d5dfcb7b8c2659029395bdf": {"balance": "8999958000000000000", "nonce": 1},
                            "1000000000000000000000000000000000000000": {"balance": "1000000000000000000", "nonce": 0},
                            "2000000000000000000000000000000000000000": {"balance": coinbase, "nonce": 0}}})
                        dumps.append(path)
                    for number, digest in ((0, "3" * 64), (1, "1" * 64), (2, "1" * 64)):
                        path = root / f"header-{number}.jsonl"
                        write_json(path, {"msg": "State dump configured", "block": str(number), "hash": "0x" + digest})
                        headers.append(path)
                    admitted = admit(root / "request.json")
                    admitted["header_observations"] = headers
                    compare(admitted, dumps, root / "full_chain")
                    if target == "genesis": fixture["identity"]["genesis_hash"] = "0x" + "0" * 64
                    else: fixture["prepared_blocks"][0]["hash"] = "0x" + "0" * 64
                    write_json(root / "fixture.json", fixture)
                    request["fixture"] = ref(root / "fixture.json")
                    write_json(root / "request.json", request)
                    admitted = admit(root / "request.json")
                    admitted["header_observations"] = headers
                    with self.assertRaisesRegex(ValueError, "Native header identity"):
                        compare(admitted, dumps, root / "full_chain")
        finally:
            sys.path.pop(0)


if __name__ == "__main__":
    unittest.main()
