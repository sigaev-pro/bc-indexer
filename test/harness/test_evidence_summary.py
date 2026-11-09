"""Check that evidence_summary reports retained figures and fails closed."""

import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest

import evidence_summary

GO_LOG = """=== RUN   TestOne
--- PASS: TestOne (0.01s)
=== RUN   TestTwo
=== RUN   TestTwo/a
=== RUN   TestTwo/b
--- PASS: TestTwo (0.02s)
    --- PASS: TestTwo/a (0.00s)
    --- FAIL: TestTwo/b (0.00s)
--- SKIP: TestThree (0.00s)
PASS
ok  \t_/source\t0.213s
"""

PY_LOG = """test_a (module.Case.test_a) ... ok

----------------------------------------------------------------------
Ran 12 tests in 1.250s

OK (skipped=2)
"""


def reference(path):
    data = Path(path).read_bytes()
    return {"path": str(path), "sha256": hashlib.sha256(data).hexdigest(), "size_bytes": len(data)}


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    return path


def run_tool(*arguments):
    stdout, stderr = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
        code = evidence_summary.main([str(argument) for argument in arguments])
    return code, stdout.getvalue(), stderr.getvalue()


def snapshot(root):
    return {str(path): (path.stat().st_size, path.stat().st_mtime_ns, path.read_bytes())
            for path in sorted(Path(root).rglob("*")) if path.is_file()}


class Fixture:
    """Build small retained-run directories with real hashes."""

    def __init__(self, root):
        self.root = Path(root)

    def command(self, run, index, stdout="", stderr="", exit_code=0, elapsed=1.25, cleanup=True, group=True):
        logs = run / "logs"
        logs.mkdir(parents=True, exist_ok=True)
        stem = logs / f"{index:03d}"
        Path(f"{stem}.stdout").write_text(stdout)
        Path(f"{stem}.stderr").write_text(stderr)
        write_json(Path(f"{stem}.request.json"), {"argv": ["tool"]})
        write_json(Path(f"{stem}.cleanup.json"), {"remove_exit": 0})
        write_json(Path(f"{stem}.result.json"), {"exit_code": exit_code, "elapsed_seconds": elapsed,
                                                 "cleanup_complete": cleanup, "process_group_absent": group,
                                                 "reason": None})
        return {name: reference(f"{stem}.{name}" + (".json" if name in ("request", "result", "cleanup") else ""))
                for name in ("request", "result", "cleanup", "stdout", "stderr")}

    def nats_run(self, name="nats-image-fence-01-abc", previous=None, attempt=1):
        run = self.root / name
        run.mkdir(parents=True)
        tool = write_json(self.root / "tool.json", {"kind": "tool"})
        (run / "R3").mkdir()
        report = run / "R3" / "native.json"
        report.write_text('{"native": true}\n')
        shared = run / "R3" / "input.json"
        shared.write_text("input\n")
        proof = {"kind": "nats-image-run", "result": "PASS", "run_id": "r1", "attempt_index": attempt,
                 "previous_attempt": previous, "correction": None, "elapsed_seconds": 9.0641715,
                 "cleanup_complete": True, "runtime_admitted": False, "public_profiles": "NOT_RUN",
                 "native_owner_nonce": "nonce", "tool": reference(tool),
                 "cases": {"R3": {"result": "PASS", "report": reference(report), "input": reference(shared),
                                  "artifacts": [reference(shared), reference(report)],
                                  "comparison": {"stored_messages": 5, "lower_sequence": 0, "upper_sequence": 5,
                                                 "groups": ["APP/S", "SYS/_meta_"]}}},
                 "commands": [self.command(run, 0, stdout=GO_LOG, elapsed=1.3501),
                              self.command(run, 1, elapsed=6.9039)]}
        write_json(run / "image-fence.json", proof)
        (self.root / (name + ".exit")).write_text("0\n")
        return run

    def runner(self):
        run = self.root / "local-runner-01-abc"
        child = write_json(run / "run" / "children" / "eth" / "proof.json",
                           {"kind": "child", "input": reference(write_json(self.root / "child-input.json", {}))})
        plan = write_json(run / "plan" / "plan.json", {"plan": 1})
        admission = write_json(run / "run" / "admission.json", {"kind": "admission", "plan": reference(plan)})
        record = {"kind": "local-native-seam-run", "result": "PASS", "run_id": "r2", "error": None,
                  "attempt": {"attempt_index": 1}, "admission": reference(admission),
                  "resolution_sha256": "a" * 64,
                  "phases": {"preparation": {"deadline_seconds": 900, "started_at": "2026-11-09T18:58:57.744721Z",
                                             "finished_at": "2026-11-09T18:58:58.277276Z"},
                             "execution": {"deadline_seconds": 1800, "started_at": "2026-11-09T18:58:58.277288Z",
                                           "finished_at": "2026-11-09T18:59:31.288171Z"}},
                  "children": [{"profile": "ethereum-mainnet", "slice": "geth-import", "result": "PASS",
                                "elapsed_seconds": 6.160863, "exit_code": 0, "process_group_absent": True,
                                "native_owner_nonce": "n2", "proof": reference(child)}],
                  "teardown": {"cleanup_complete": True, "foreign_harness_containers": []}}
        write_json(run / "run" / "run-record.json", record)
        (run / "run.exit").write_text("0\n")
        return run


class EvidenceSummaryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.fixture = Fixture(self.temp.name)

    def test_nats_run_reports_every_quoted_figure(self):
        run = self.fixture.nats_run()
        code, output, errors = run_tool(run)
        self.assertEqual((code, errors), (0, ""))
        proof_sha = hashlib.sha256((run / "image-fence.json").read_bytes()).hexdigest()
        report_sha = hashlib.sha256((run / "R3" / "native.json").read_bytes()).hexdigest()
        for expected in (f"SHA256 `{proof_sha}`", f"`{report_sha}`", "Elapsed 9.064 seconds",
                         "new lineage (no previous attempt)", "| 000 | 0 | 1.350 | true | true | none |",
                         "| 001 | 0 | 6.904 |", "Go methods 2 PASS, 1 SKIP; Go subtests 1 FAIL, 1 PASS; Go packages 1 ok",
                         "exit 0", "| R3 | PASS |", "| 5 | 1–5 | `APP/S`, `SYS/_meta_` |",
                         "Public profiles: NOT_RUN", "Runtime admitted: false", "| `tool` |"):
            self.assertIn(expected, output)

    def test_unique_absolute_references_count_paths_once_and_sum_bytes(self):
        run = self.fixture.nats_run()
        proof = json.loads((run / "image-fence.json").read_text())
        found = evidence_summary.references(proof)
        paths = {item["path"] for item in found}
        total = sum(Path(path).stat().st_size for path in paths)
        _, output, _ = run_tool(run)
        self.assertIn(f"{len(found)} in the proof, {len(paths)} unique absolute ({total:,} bytes), 0 mismatches",
                      output)
        self.assertGreater(len(found), len(paths))

    def test_relative_references_are_listed_and_not_counted(self):
        run = self.fixture.nats_run()
        path = run / "image-fence.json"
        proof = json.loads(path.read_text())
        proof["source_archive"] = {"path": "sources/source.tar", "sha256": "0" * 64, "size_bytes": 3}
        write_json(path, proof)
        code, output, _ = run_tool(run)
        self.assertEqual(code, 0)
        self.assertIn("Relative references (not verified): `sources/source.tar`", output)

    def test_every_reference_problem_fails_closed_without_a_summary(self):
        def size(run):
            (run / "R3" / "native.json").write_text('{"native": false, "changed": 1}\n')

        def content(run):
            (run / "R3" / "native.json").write_text('{"native": 0000}\n')

        def missing(run):
            (run / "R3" / "native.json").unlink()

        def directory(run):
            (run / "R3" / "native.json").unlink()
            (run / "R3" / "native.json").mkdir()

        def command_log(run):
            (run / "logs" / "000.stdout").write_text(GO_LOG.replace("PASS", "FAIL"))

        def conflict(run):
            path = run / "image-fence.json"
            proof = json.loads(path.read_text())
            proof["cases"]["R3"]["artifacts"][0]["sha256"] = "f" * 64
            write_json(path, proof)

        def external(run):
            (self.fixture.root / "tool.json").write_text('{"kind": "changed"}\n')

        cases = {"size": (size, "size mismatch"), "content": (content, "SHA256 mismatch"),
                 "missing": (missing, "missing or unreadable"), "directory": (directory, "missing or unreadable"),
                 "command log": (command_log, "000.stdout"), "conflict": (conflict, "conflicting references"),
                 "external": (external, "tool.json")}
        for name, (damage, message) in cases.items():
            with self.subTest(name), tempfile.TemporaryDirectory() as temporary:
                fixture = Fixture(temporary)
                self.fixture = fixture
                run = fixture.nats_run()
                damage(run)
                code, output, errors = run_tool(run)
                self.assertEqual(code, 1)
                self.assertEqual(output, "")
                self.assertIn(message, errors)
                self.assertIn("no summary printed", errors)

    def test_non_regular_reference_fails_closed(self):
        run = self.fixture.nats_run()
        path = run / "image-fence.json"
        proof = json.loads(path.read_text())
        proof["device"] = {"path": os.devnull, "sha256": hashlib.sha256(b"").hexdigest(), "size_bytes": 0}
        write_json(path, proof)
        code, output, errors = run_tool(run)
        self.assertEqual((code, output), (1, ""))
        self.assertIn("not a regular file", errors)

    def test_lineage_follows_previous_attempts_and_verifies_them(self):
        first = self.fixture.nats_run("nats-image-fence-01-abc")
        first_proof = first / "image-fence.json"
        document = json.loads(first_proof.read_text())
        document.update(result="FAIL", correction=None)
        write_json(first_proof, document)
        second = self.fixture.nats_run("nats-image-fence-02-abc", previous=reference(first_proof), attempt=2)
        code, output, _ = run_tool(second)
        self.assertEqual(code, 0)
        self.assertIn("Attempt index 2; previous attempts, newest first: `nats-image-fence-01-abc` FAIL attempt 1 "
                      f"(`{reference(first_proof)['sha256']}`)", output)
        document["result"] = "PASS"
        write_json(first_proof, document)
        code, output, errors = run_tool(second)
        self.assertEqual((code, output), (1, ""))
        self.assertIn("image-fence.json", errors)

    def test_lineage_verifies_attempts_beyond_the_directly_cited_one(self):
        previous = None
        for attempt in range(1, 5):
            run = self.fixture.nats_run(f"nats-image-fence-0{attempt}-abc", previous=previous, attempt=attempt)
            previous = reference(run / "image-fence.json")
        code, output, _ = run_tool(run)
        self.assertEqual(code, 0)
        self.assertIn("Attempt index 4; previous attempts, newest first: `nats-image-fence-03-abc`", output)
        self.assertIn("`nats-image-fence-01-abc` PASS attempt 1", output)
        oldest = self.fixture.root / "nats-image-fence-01-abc" / "image-fence.json"
        oldest.write_text(oldest.read_text().replace('"run_id": "r1"', '"run_id": "r9"'))
        code, output, errors = run_tool(run)
        self.assertEqual((code, output), (1, ""))
        self.assertIn("nats-image-fence-01-abc/image-fence.json", errors)

    def test_lineage_loops_and_non_references_are_malformed(self):
        run = self.fixture.nats_run()
        path = run / "image-fence.json"
        proof = json.loads(path.read_text())
        proof["previous_attempt"] = "nats-image-fence-00"
        write_json(path, proof)
        self.assertEqual(run_tool(run)[0], 2)
        other = self.fixture.root / "loop.json"
        write_json(other, {"kind": "nats-image-run", "previous_attempt": None})
        loop = reference(other)
        write_json(other, {"kind": "nats-image-run", "previous_attempt": loop})
        loop = reference(other)
        proof["previous_attempt"] = loop
        write_json(path, proof)
        code, _, errors = run_tool(run)
        self.assertEqual(code, 2)
        self.assertIn("lineage loops", errors)

    def test_runner_reports_phases_children_and_nested_references(self):
        run = self.fixture.runner()
        code, output, errors = run_tool(run)
        self.assertEqual((code, errors), (0, ""))
        for expected in ("| preparation | 0.533 | 900 |", "| execution | 33.011 | 1800 |",
                         "| ethereum-mainnet | geth-import | PASS | 6.161 | 0 | true | `n2` |",
                         "1 unique (3 bytes)", "| `admission.plan` |", "Launcher record `run.exit`: exit 0",
                         "Teardown: cleanup_complete true, foreign_harness_containers 0.",
                         f"Resolution SHA256: `{'a' * 64}`"):
            self.assertIn(expected, output)

    def test_runner_child_and_nested_references_fail_closed(self):
        for target in ("child-input.json", "plan/plan.json"):
            with self.subTest(target), tempfile.TemporaryDirectory() as temporary:
                fixture = Fixture(temporary)
                run = fixture.runner()
                path = fixture.root / target if target == "child-input.json" else run / target
                path.write_text('{"changed": true}\n')
                code, output, errors = run_tool(run)
                self.assertEqual((code, output), (1, ""))
                self.assertIn(target.split("/")[-1], errors)

    def test_tool_never_writes_into_the_run(self):
        run = self.fixture.nats_run()
        before = snapshot(self.fixture.root)
        self.assertEqual(run_tool(run)[0], 0)
        (run / "R3" / "native.json").write_text("changed\n")
        changed = snapshot(self.fixture.root)
        self.assertEqual(run_tool(run)[0], 1)
        self.assertEqual(snapshot(self.fixture.root), changed)
        self.assertNotEqual(before, changed)

    def test_unrecognized_or_ambiguous_runs_are_errors(self):
        empty = self.fixture.root / "empty"
        empty.mkdir()
        self.assertEqual(run_tool(empty)[0], 2)
        run = self.fixture.nats_run()
        write_json(run / "preparation.json", {"kind": "nats-image-preparation"})
        code, _, errors = run_tool(run)
        self.assertEqual(code, 2)
        self.assertIn("found 2", errors)
        self.assertEqual(run_tool(self.fixture.root / "absent")[0], 2)
        broken = self.fixture.root / "broken"
        broken.mkdir()
        (broken / "preparation.json").write_text("{")
        self.assertEqual(run_tool(broken)[0], 2)
        write_json(broken / "preparation.json", {"result": "PASS"})
        self.assertEqual(run_tool(broken)[0], 2)

    def test_malformed_proof_structures_are_errors(self):
        def edit(proof, run):
            return proof

        def commands_not_list(proof, run):
            proof["commands"] = {}

        def command_without_result(proof, run):
            del proof["commands"][0]["result"]

        def result_not_object(proof, run):
            path = run / "logs" / "000.result.json"
            write_json(path, [1])
            proof["commands"][0]["result"] = reference(path)

        def phases_not_object(proof, run):
            proof["phases"] = []

        def phase_without_finish(proof, run):
            proof["phases"] = {"execution": {"started_at": "2026-11-09T18:58:58Z"}}

        def child_not_object(proof, run):
            proof["children"] = ["child"]

        def previous_not_object(proof, run):
            path = write_json(self.fixture.root / "previous.json", ["not", "an", "object"])
            proof["previous_attempt"] = reference(path)

        for damage in (commands_not_list, command_without_result, result_not_object, phases_not_object,
                       phase_without_finish, child_not_object, previous_not_object):
            with self.subTest(damage.__name__), tempfile.TemporaryDirectory() as temporary:
                self.fixture = Fixture(temporary)
                run = self.fixture.nats_run()
                path = run / "image-fence.json"
                proof = json.loads(path.read_text())
                damage(proof, run)
                write_json(path, proof)
                code, output, errors = run_tool(run)
                self.assertEqual((code, output), (2, ""), errors)
                self.assertTrue(errors.startswith("ERROR: "), errors)
        self.assertEqual(run_tool(self.fixture.root / "image-fence.json")[0], 2)

    def test_large_nested_documents_are_not_listed(self):
        run = self.fixture.nats_run()
        large = write_json(self.fixture.root / "large.json", {"inner": reference(self.fixture.root / "tool.json"),
                                                              "padding": "x" * (1 << 20)})
        path = run / "image-fence.json"
        proof = json.loads(path.read_text())
        proof["large"] = reference(large)
        write_json(path, proof)
        code, output, _ = run_tool(run)
        self.assertEqual(code, 0)
        self.assertIn("| `large` |", output)
        self.assertNotIn("`large.inner`", output)
        self.assertIn("| `tool` |", output)

    def test_unreadable_exit_record_is_an_error(self):
        run = self.fixture.nats_run()
        record = self.fixture.root / (run.name + ".exit")
        record.chmod(0)
        self.addCleanup(record.chmod, 0o600)
        if os.access(record, os.R_OK):
            self.skipTest("this user can read files without read permission")
        code, output, errors = run_tool(run)
        self.assertEqual((code, output), (2, ""))
        self.assertIn("ERROR:", errors)

    def test_exit_records_accept_plain_and_prefixed_integers_only(self):
        for text, expected in (("0\n", 0), ("EXIT=2\n", 2), ("-15", -15)):
            with self.subTest(text=text):
                run = self.fixture.nats_run(name=f"run-{expected}")
                (self.fixture.root / (run.name + ".exit")).write_text(text)
                code, output, _ = run_tool(run)
                self.assertEqual(code, 0)
                self.assertIn(f"Launcher record `{run.name}.exit`: exit {expected}", output)
        (self.fixture.root / "run-0.exit").write_text("passed\n")
        self.assertEqual(run_tool(self.fixture.root / "run-0")[0], 2)

    def test_completion_record_is_reported(self):
        run = self.fixture.nats_run()
        write_json(self.fixture.root / (run.name + ".completion.json"),
                   {"exit_code": 0, "elapsed_seconds": 235.668, "source_commit": "6a5fc49"})
        _, output, _ = run_tool(run)
        self.assertIn("exit 0, 235.668 seconds, source `6a5fc49`", output)

    def test_external_test_logs_are_hashed_and_parsed(self):
        run = self.fixture.nats_run()
        log = self.fixture.root / "unittest.log"
        log.write_text(PY_LOG)
        code, output, _ = run_tool(run, "--test-log", log)
        self.assertEqual(code, 0)
        digest = hashlib.sha256(PY_LOG.encode()).hexdigest()
        self.assertIn(f"Test log `unittest.log` SHA256 `{digest}` ({len(PY_LOG):,} bytes): "
                      "Python 12 tests OK (skipped=2) in 1.250 s.", output)
        self.assertEqual(run_tool(run, "--test-log", self.fixture.root / "absent.log")[0], 2)

    def test_sequence_range_uses_the_exclusive_lower_frontier(self):
        for lower, upper, expected in ((0, 5, "1–5"), (3, 5, "4–5"), (5, 5, "none"), (None, 5, "n/a")):
            with self.subTest(lower=lower, upper=upper):
                self.assertEqual(evidence_summary.sequence_range({"lower_sequence": lower, "upper_sequence": upper}),
                                 expected)

    def test_test_count_parser(self):
        counts = evidence_summary.test_counts(GO_LOG + "Ran 3 tests in 0.5s\n\nFAILED (failures=1)\n")
        self.assertEqual(counts["go"]["methods"], {"PASS": 2, "SKIP": 1})
        self.assertEqual(counts["go"]["subtests"], {"PASS": 1, "FAIL": 1})
        self.assertEqual(counts["go"]["packages"], {"ok": 1})
        self.assertEqual(counts["python"], [{"tests": 3, "seconds": 0.5, "status": "FAILED", "detail": "failures=1"}])
        later = evidence_summary.test_counts("Ran 2 tests in 0.1s\n\nOK\nFAILED (errors=1)\n")
        self.assertEqual((later["python"][0]["status"], later["python"][0]["detail"]), ("OK", None))
        colored = evidence_summary.test_counts("\x1b[1mRan 4 tests in 0.2s\x1b[0m\n\n\x1b[32mOK\x1b[0m\n")
        self.assertEqual(colored["python"], [{"tests": 4, "seconds": 0.2, "status": "OK", "detail": None}])
        self.assertIsNone(evidence_summary.test_counts("no tests here\n"))
        self.assertEqual(evidence_summary.describe_tests(evidence_summary.test_counts("Ran 1 test in 0.1s\n")),
                         "Python 1 tests NO_STATUS in 0.100 s")


RETAINED = os.environ.get("EVIDENCE_SUMMARY_RUNS")


@unittest.skipUnless(RETAINED, "set EVIDENCE_SUMMARY_RUNS to the retained d95-20261107 run directory")
class RetainedRunTests(unittest.TestCase):
    """Opt-in comparison with figures quoted in .project/evidence/E01-01.harness.md."""

    def summary(self, name):
        code, output, errors = run_tool(Path(RETAINED) / name)
        self.assertEqual((code, errors), (0, ""))
        return output

    def test_fence_08_matches_the_evidence_section(self):
        output = self.summary("nats-image-fence-08-ed723a4")
        for expected in ("bbd32af4297d75b4417a6707452ad4b026613d62ce3e7d3c8ab1db9b8dd447a4",
                         "2d7ca0b855a5b084ee819fc4f5be3c9e1ac4a6996b692cd8e7ad6e4f43d72bd7",
                         "416e12b02318e402f5e8c9822565c63d85e24093c60f9d57d84b5bf83bdcf980",
                         "81d80d3e961f212149e5eaa6b936cc14eb54f92577bae08fa5ef36aec11af40c",
                         "815980258e923139185066e98ab64d239b3661fb3fe81f4ad7d6ec2b5e0ab9b1",
                         "Elapsed 9.064 seconds", "Attempt index 1; new lineage",
                         "409 unique absolute (14,201,441 bytes), 0 mismatches",
                         "| 000 | 0 | 1.350 | true | true |", "| 001 | 0 | 6.904 | true | true |",
                         "| 5 | 1–5 | `APP/S-R3F-TSDwT5SB`, `SYS/_meta_` |", "| standalone | PASS |"):
            self.assertIn(expected, output)

    def test_preparation_08_matches_the_evidence_section(self):
        output = self.summary("nats-image-preparation-08-ed723a4")
        for expected in ("e35a84313f31236495246a61df4465d2e5400a46b79139c4d66946e393c2e91b",
                         "Elapsed 56.826 seconds", "| 000 | 0 | 28.602 | true | true | none | "
                         "Go methods 15 PASS; Go subtests 8 PASS", "| 001 | 0 | 21.900 | true | true |"):
            self.assertIn(expected, output)

    def test_fence_06_lineage_and_references(self):
        output = self.summary("nats-image-fence-06-599521b")
        self.assertIn("Attempt index 2; previous attempts, newest first: `nats-image-fence-05-1d933c3` FAIL", output)
        self.assertIn("545 unique absolute (14,558,731 bytes)", output)

    def test_runner_04_matches_the_evidence_section(self):
        output = self.summary("local-runner-04-f2b351e")
        for expected in ("d60abc8f86dea93b675730d7c00808f2d51894de4fc51094a1170e65e6ddd919",
                         "eb1d2d1a0863ee454bfb5267bb8b80c3e2bf70770db13525cc4a4062917daba1",
                         "ad3d648cabcf89943d10881755e6e83b6d84d742996d87074f476eeb0f2b3faa",
                         "3f12a65d7e3dc5d1e221bbe0716ba6bf8a8c288ee0cbee8eb6eb5d47d9cac001",
                         "| preparation | 0.533 | 900 |", "| execution | 33.011 | 1800 |", "| teardown | 0.178 | 120 |",
                         "| bsc-mainnet | bsc-parlia-import | PASS | 22.091 | 0 | true |",
                         "| polygon-pos-mainnet | bor-import | PASS | 4.756 | 0 | true |"):
            self.assertIn(expected, output)


if __name__ == "__main__":
    unittest.main()
