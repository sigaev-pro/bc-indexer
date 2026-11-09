"""Portable controls for the E01-02.pins driver; fakes replace Docker and native tests."""

import copy
import hashlib
import json
from pathlib import Path
import signal
import tempfile
import unittest
from unittest.mock import patch

import pins

ROOT1 = "0x" + "11" * 32
ROOT2 = "0x" + "22" * 32
BLOCKS = [{"number": 1, "state_root": ROOT1}, {"number": 2, "state_root": ROOT2}]


def go_output(names, extra=()):
    lines = ["=== RUN   " + name for name in names]
    lines += ["--- PASS: %s (0.01s)" % name for name in names]
    return "\n".join([*lines, *extra, "PASS"]) + "\n"


def report(family="geth", same_root=False):
    root2 = ROOT1 if same_root else ROOT2
    walk = {"accounts": 3, "slots": 1, "codes": 1}
    preflight = {name: prefix + ": detail" for name, prefix in pins.PREFLIGHT_REJECTIONS.items()}
    preflight.update(unbound_fresh_store="<nil>", bound_store="<nil>",
                     native_hash_override_on_path_store="incompatible state scheme, stored: path, provided: hash",
                     native_default_scheme_for_empty_store="path")
    cases = sorted(pins.PROFILE_CASES[family])
    rows = [
        ("TestNativeVolatileTrieLost", "pinned", {
            "pinned": True, "child": {"head": 2, "block1_root_on_disk_before": False, "block1_root_on_disk_after": True},
            "preflight_pins": ["obs-volatile"], "block1_root_on_disk": True, "walk": walk, "walk_error": "<nil>",
            "recovered_head": 2 if same_root else 1, "recovered_root": ROOT1}),
        ("TestNativeVolatileTrieLost", "control", {
            "pinned": False, "child": {"head": 2, "block1_root_on_disk_before": False, "block1_root_on_disk_after": False},
            "preflight_pins": [], "block1_root_on_disk": False, "walk": {"accounts": 0, "slots": 0, "codes": 0},
            "walk_error": "missing trie node", "recovered_head": 0, "recovered_root": "0x" + "33" * 32}),
        ("TestNativePinnedStatePruning", "pruning", {
            "head_root": root2, "side_root": "0x" + "44" * 32, "block1_root": ROOT1,
            "refused": "live pins forbid this native deletion: 2 live, first obs-block1", "pinned_side_walk": walk,
            "unguarded_side_deleted": True, "unguarded_preflight": "pinned native data is unavailable: pin obs-side",
            "released_side_deleted": True, "head_walk": walk}),
        ("TestNativeRewindDeletesPinnedHeader", "rewind", {
            "refused": "live pins forbid this native deletion: rewind to 0 deletes header 1 of pin obs-rewind",
            "unguarded_verify": "pinned native data is unavailable: pin obs-rewind header 1"}),
        ("TestNativeStorePreflight", "preflight", preflight),
        ("TestNativeEffectiveProfile", "profile", {"cases": cases, "results": {
            name: {"accepted": name in pins.ACCEPTED_PROFILE_CASES, "detail": {}} for name in cases}}),
    ]
    return [{"family": family, "test": test, "name": name, "value": value} for test, name, value in rows]


def blocks(same_root=False):
    return [BLOCKS[0], {"number": 2, "state_root": ROOT1 if same_root else ROOT2}]


def find(rows, test, name):
    return next(row["value"] for row in rows if row["test"] == test and row["name"] == name)


class FakeLock:
    def __init__(self):
        self.closed = False

    def close(self):
        self.closed = True


class FakeNative:
    created = []
    fail = False

    def __init__(self, output, deadline):
        if FakeNative.fail:
            raise ValueError("Docker executable is unavailable")
        self.commands, self.cleanup_complete, self.nonce = [{"command": 1}], True, "nonce"
        FakeNative.created.append(self)


class ReportTests(unittest.TestCase):
    def test_complete_reports_pass_for_every_family(self):
        for family in pins.FAMILIES:
            for same in (False, True):
                summary = pins.evaluate_report(family, report(family, same), blocks(same))
                self.assertEqual(summary["recovered_head"], 2 if same else 1)
                self.assertEqual(summary["profile_cases"], len(pins.PROFILE_CASES[family]))

    def test_each_broken_observation_rejects(self):
        mutations = {
            "control kept state": ("TestNativeVolatileTrieLost", "control", lambda v: v.update(block1_root_on_disk=True)),
            "control head": ("TestNativeVolatileTrieLost", "control", lambda v: v.update(recovered_head=1)),
            "control walk": ("TestNativeVolatileTrieLost", "control", lambda v: v.update(walk_error="<nil>")),
            "volatile before": ("TestNativeVolatileTrieLost", "pinned", lambda v: v["child"].update(block1_root_on_disk_before=True)),
            "pin not flushed": ("TestNativeVolatileTrieLost", "pinned", lambda v: v["child"].update(block1_root_on_disk_after=False)),
            "wrong root": ("TestNativeVolatileTrieLost", "pinned", lambda v: v.update(recovered_root=ROOT2)),
            "wrong head": ("TestNativeVolatileTrieLost", "pinned", lambda v: v.update(recovered_head=2)),
            "walk error": ("TestNativeVolatileTrieLost", "pinned", lambda v: v.update(walk_error="missing")),
            "pin lost": ("TestNativeVolatileTrieLost", "pinned", lambda v: v.update(preflight_pins=[])),
            "no refusal": ("TestNativePinnedStatePruning", "pruning", lambda v: v.update(refused="")),
            "control kept": ("TestNativePinnedStatePruning", "pruning", lambda v: v.update(unguarded_side_deleted=False)),
            "no missing provider": ("TestNativePinnedStatePruning", "pruning", lambda v: v.update(unguarded_preflight="<nil>")),
            "release kept": ("TestNativePinnedStatePruning", "pruning", lambda v: v.update(released_side_deleted=False)),
            "no code": ("TestNativePinnedStatePruning", "pruning", lambda v: v["pinned_side_walk"].update(codes=0)),
            "head mismatch": ("TestNativePinnedStatePruning", "pruning", lambda v: v.update(head_root=ROOT1)),
            "rewind allowed": ("TestNativeRewindDeletesPinnedHeader", "rewind", lambda v: v.update(refused="<nil>")),
            "rewind kept": ("TestNativeRewindDeletesPinnedHeader", "rewind", lambda v: v.update(unguarded_verify="<nil>")),
            "foreign accepted": ("TestNativeStorePreflight", "preflight", lambda v: v.update(foreign_genesis="<nil>")),
            "wrong reason": ("TestNativeStorePreflight", "preflight", lambda v: v.update(path_initialized_store="forbidden pruning profile")),
            "fresh rejected": ("TestNativeStorePreflight", "preflight", lambda v: v.update(bound_store="mismatch")),
            "converted": ("TestNativeStorePreflight", "preflight", lambda v: v.update(native_hash_override_on_path_store="<nil>")),
            "profile flipped": ("TestNativeEffectiveProfile", "profile", lambda v: v["results"]["allowed_config"].update(accepted=False)),
            "forbidden accepted": ("TestNativeEffectiveProfile", "profile",
                                   lambda v: v["results"]["forbidden_pruning_archive_flag"].update(accepted=True)),
            "case missing": ("TestNativeEffectiveProfile", "profile", lambda v: v["cases"].pop()),
        }
        for label, (test, name, change) in mutations.items():
            rows = copy.deepcopy(report())
            change(find(rows, test, name))
            with self.subTest(label), self.assertRaises((ValueError, KeyError)):
                pins.evaluate_report("geth", rows, blocks())

    def test_duplicate_foreign_and_missing_rows_reject(self):
        rows = report()
        with self.assertRaises(ValueError):
            pins.evaluate_report("geth", rows + [rows[0]], blocks())
        with self.assertRaises(ValueError):
            pins.evaluate_report("bsc", rows, blocks())
        with self.assertRaises(KeyError):
            pins.evaluate_report("geth", rows[1:], blocks())

    def test_bsc_needs_its_extra_profile_cases(self):
        with self.assertRaises(ValueError):
            pins.evaluate_report("bsc", [dict(row, family="bsc") for row in report("geth")], blocks())

    def test_read_report_rejects_malformed_rows(self):
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw).resolve()
            path = Path(directory) / "report.jsonl"
            path.write_text("\n".join(json.dumps(row) for row in report()) + "\n")
            self.assertEqual(len(pins.read_report(path, None)), 6)
            for bad in ('{"family":"geth"}', "[]", "not json"):
                path.write_text(bad + "\n")
                with self.subTest(bad), self.assertRaises(ValueError):
                    pins.read_report(path, None)


class GoOutputTests(unittest.TestCase):
    def test_complete_output_passes(self):
        self.assertEqual(pins.parse_go_test(go_output(pins.COMMON_TESTS), pins.COMMON_TESTS), sorted(pins.COMMON_TESTS))

    def test_incomplete_or_failed_output_rejects(self):
        names = list(pins.COMMON_TESTS)
        cases = {
            "missing": go_output(names[1:]),
            "duplicate": go_output(names + names[:1]),
            "extra": go_output(names + ["TestOther"]),
            "failed": go_output(names, ["--- FAIL: TestOther (0.00s)"]),
            "skipped": go_output(names, ["    --- SKIP: TestNativeX/sub (0.00s)"]),
            "no final pass": go_output(names).replace("PASS\n", "FAIL\n"),
            "empty": "",
        }
        for label, text in cases.items():
            with self.subTest(label), self.assertRaises(ValueError):
                pins.parse_go_test(text, pins.COMMON_TESTS)


class SourceTests(unittest.TestCase):
    def test_shared_sources_are_identical_in_the_repository(self):
        pins.check_shared_sources()
        for family in pins.FAMILIES:
            for origin, relative in pins.adapter_files(family):
                self.assertTrue(origin.is_file(), origin)
                self.assertFalse(relative.startswith("/"))

    def test_shared_source_drift_rejects(self):
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw).resolve()
            root = Path(directory)
            for family in pins.FAMILIES:
                base = root / "adapters" / family / "state_provider"
                base.mkdir(parents=True)
                for name in pins.SHARED:
                    (base / name).write_text("package stateprovider\n")
            (root / "adapters/bsc/state_provider/provider.go").write_text("package stateprovider // drift\n")
            with patch.object(pins, "ROOT", root), self.assertRaises(ValueError):
                pins.check_shared_sources()

    def test_install_copies_and_never_replaces_a_native_file(self):
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw).resolve()
            source, retained = Path(directory) / "source", Path(directory) / "adapter"
            (source / "internal/cli/server").mkdir(parents=True)
            rows = pins.install_adapter("bor", source, retained, None)
            self.assertEqual([row["relative"] for row in rows],
                             [relative for _, relative in pins.adapter_files("bor")])
            for row in rows:
                self.assertEqual((source / row["relative"]).read_bytes(), Path(row["source"]["path"]).read_bytes())
                self.assertEqual(row["retained"]["sha256"], row["source"]["sha256"])
            clash = Path(directory) / "clash"
            (clash / "internal/cli/server").mkdir(parents=True)
            (clash / "internal/cli/server/zz_extractor_effective.go").write_text("package server\n")
            with self.assertRaises(ValueError):
                pins.install_adapter("bor", clash, Path(directory) / "adapter2", None)


class AttemptTests(unittest.TestCase):
    def setUp(self):
        FakeNative.created, FakeNative.fail = [], False
        self.lock = FakeLock()
        self.patches = [patch.object(pins, "Native", FakeNative),
                        patch.object(pins, "admit_docker", lambda: self.lock)]
        for item in self.patches:
            item.start()

    def tearDown(self):
        for item in self.patches:
            item.stop()

    def attempt(self, output, body):
        proof = {"result": "FAIL", "error": None}
        return pins.owned_attempt(output, 0, None, proof, "run.json", body)

    def test_pass_fail_and_dirty_cleanup_are_recorded(self):
        def dirty(output, runner):
            runner.cleanup_complete = False

        def broken(output, runner):
            raise ValueError("native command failed")

        def stopped(output, runner):
            raise InterruptedError("Interrupted by SIGTERM")
        for label, body, result in (("pass", lambda output, runner: None, "PASS"), ("fail", broken, "FAIL"),
                                    ("dirty", dirty, "FAIL"), ("signal", stopped, "FAIL")):
            with self.subTest(label), tempfile.TemporaryDirectory() as raw:
                directory = Path(raw).resolve()
                self.lock = FakeLock()
                output = Path(directory) / "new"
                proof = self.attempt(output, body)
                record = json.loads((output / "run.json").read_text())
                self.assertEqual((proof["result"], record["result"]), (result, result))
                self.assertEqual(record["native_owner_nonce"], "nonce")
                self.assertTrue(self.lock.closed)

    def test_native_setup_failure_still_records(self):
        FakeNative.fail = True
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw).resolve()
            output = Path(directory) / "new"
            proof = self.attempt(output, lambda output, runner: None)
            self.assertEqual(proof["result"], "FAIL")
            self.assertEqual(json.loads((output / "run.json").read_text())["commands"], [])
            self.assertTrue(self.lock.closed)

    def test_existing_output_is_refused_without_a_record(self):
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw).resolve()
            output = Path(directory) / "old"
            output.mkdir()
            with self.assertRaises(FileExistsError):
                self.attempt(output, lambda output, runner: None)
            self.assertEqual(list(output.iterdir()), [])
            self.assertTrue(self.lock.closed)

    def test_final_record_is_written_with_stop_signals_blocked(self):
        seen = {}

        def save(path, value):
            seen["mask"] = signal.pthread_sigmask(signal.SIG_BLOCK, [])
            raise OSError("disk full")
        lock = FakeLock()
        with tempfile.TemporaryDirectory() as raw, patch.object(pins, "save", save):
            directory = Path(raw).resolve()
            with self.assertRaises(OSError):
                pins.save_final(Path(directory) / "run.json", {}, lock)
        self.assertTrue(set(pins.STOP_SIGNALS) <= set(seen["mask"]))
        self.assertTrue(lock.closed)
        self.assertFalse(set(pins.STOP_SIGNALS) & set(signal.pthread_sigmask(signal.SIG_BLOCK, [])))


class AdmissionTests(unittest.TestCase):
    def test_foreign_containers_refuse_and_release_the_lock(self):
        lock = FakeLock()
        with patch.object(pins, "single_writer", lambda: lock), \
                patch.object(pins, "foreign_harness_containers", lambda: ["abc"]):
            with self.assertRaises(ValueError):
                pins.admit_docker()
        self.assertTrue(lock.closed)
        lock = FakeLock()
        with patch.object(pins, "single_writer", lambda: lock), \
                patch.object(pins, "foreign_harness_containers", lambda: []):
            self.assertIs(pins.admit_docker(), lock)
        self.assertFalse(lock.closed)

    def test_prepare_refuses_before_creating_output(self):
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw).resolve()
            manifest = Path(directory) / "manifest.json"
            manifest.write_text("{}")
            output = Path(directory) / "out"
            with self.assertRaises(ValueError):
                pins.prepare(manifest, "0" * 64, output)
            digest = hashlib.sha256(manifest.read_bytes()).hexdigest()
            with self.assertRaises(ValueError):
                pins.prepare(manifest, digest, output, correction="no earlier failure")
            with self.assertRaises(ValueError):
                pins.prepare(manifest, digest, Path(directory) / "inside")
            with self.assertRaises(ValueError):
                pins.prepare(Path("relative.json"), digest, output)
            self.assertFalse(output.exists())

    def test_run_refuses_relative_paths_and_wrong_pins(self):
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw).resolve()
            tool = Path(directory) / "tool.json"
            tool.write_text('{"kind": "pins-native-tool", "families": {}}')
            output = Path(directory) / "out"
            with self.assertRaises(ValueError):
                pins.run(Path("tool.json"), "0" * 64, [], output)
            with self.assertRaises(ValueError):
                pins.run(tool, "0" * 64, [], output)
            digest = hashlib.sha256(tool.read_bytes()).hexdigest()
            with self.assertRaises(ValueError):
                pins.run(tool, digest, [], output)
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
