"""Check that mutate.py compiles every mutant first and names each kill."""

import contextlib
import hashlib
import io
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import mutate  # noqa: E402

CALC = '''def clamp(value, low, high):
    if value < low:
        return low
    if value > high:
        return high
    return value
'''

TEST_CALC = '''import unittest

import calc


class CalcTests(unittest.TestCase):
    def test_low(self):
        self.assertEqual(calc.clamp(-1, 0, 10), 0)

    def test_high(self):
        self.assertEqual(calc.clamp(11, 0, 10), 10)

    def test_inside(self):
        self.assertEqual(calc.clamp(5, 0, 10), 5)


if __name__ == "__main__":
    unittest.main()
'''

GO_CODE = '''package calc

func Clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
'''

GO_TEST = '''package calc

import "testing"

func TestClamp(t *testing.T) {
	if got := Clamp(-1, 0, 10); got != 0 {
		t.Errorf("low clamp = %d", got)
	}
	if got := Clamp(11, 0, 10); got != 10 {
		t.Errorf("high clamp = %d", got)
	}
}
'''

UNITTEST = ["{python}", "-m", "unittest", "-v", "test_calc"]
LOW = {"name": "low bound", "file": "calc.py", "original": "if value < low:", "replacement": "if value < low and False:"}
EQUIVALENT = {"name": "high bound inclusive", "file": "calc.py", "original": "if value > high:",
              "replacement": "if value >= high:", "equivalent": "At value == high both branches return high."}


def run_tool(*arguments):
    stdout, stderr = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
        code = mutate.main([str(argument) for argument in arguments])
    return code, stdout.getvalue(), stderr.getvalue()


def tree_state(root):
    return {str(path.relative_to(root)): hashlib.sha256(path.read_bytes()).hexdigest()
            for path in sorted(Path(root).rglob("*")) if path.is_file()}


class MutateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.code = self.base / "code"
        self.code.mkdir()
        (self.code / "calc.py").write_text(CALC)
        (self.code / "test_calc.py").write_text(TEST_CALC)
        self.outputs = 0

    def table(self, mutants, **fields):
        table = {"schema_version": 1, "language": "python", "test": UNITTEST, "timeout_seconds": 60,
                 "copy": ["*.py"], "mutants": mutants, **fields}
        path = self.code / "calc.mutants.json"
        path.write_text(json.dumps(table))
        return path

    def run_table(self, mutants, *extra, **fields):
        self.outputs += 1
        output = self.base / f"run-{self.outputs}"
        code, stdout, stderr = run_tool(self.table(mutants, **fields), "--output", output, *extra)
        report = json.loads((output / "mutants.json").read_text()) if (output / "mutants.json").exists() else None
        return code, report, stdout, stderr

    def row(self, report, name):
        return next(row for row in report["mutants"] if row["name"] == name)

    def test_killed_and_labeled_equivalent_mutants_pass_with_named_controls(self):
        code, report, stdout, _ = self.run_table([LOW, EQUIVALENT])
        self.assertEqual(code, 0, report)
        self.assertEqual(report["result"], "PASS")
        self.assertEqual(json.loads(stdout)["result"], "PASS")
        killed = self.row(report, "low bound")
        self.assertEqual(killed["status"], "KILLED")
        self.assertEqual(killed["killed_by"], ["test_calc.CalcTests.test_low"])
        self.assertTrue(any(line.startswith("AssertionError: -1 != 0") for line in killed["evidence"]), killed)
        self.assertTrue(any(line.startswith('File "') and "test_calc.py" in line for line in killed["evidence"]))
        self.assertEqual(self.row(report, "high bound inclusive")["status"], "EQUIVALENT")
        self.assertEqual(report["counts"]["KILLED"], 1)
        self.assertEqual(report["baseline"]["status"], "PASS")
        self.assertEqual(report["table"]["sha256"], hashlib.sha256((self.code / "calc.mutants.json").read_bytes()).hexdigest())
        self.assertEqual({row["path"] for row in report["sources"]}, {"calc.py", "test_calc.py"})
        self.assertTrue((self.base / "run-1" / "logs" / "000.test.log").is_file())

    def test_compile_and_import_failures_are_build_failed_never_kills(self):
        broken = {"name": "syntax", "file": "calc.py", "original": "if value < low:", "replacement": "if value < low"}
        unimportable = {"name": "import", "file": "calc.py", "original": "def clamp(",
                        "replacement": "missing_name\ndef clamp("}
        code, report, _, _ = self.run_table([broken, unimportable, LOW])
        self.assertEqual(code, 1)
        self.assertEqual(report["result"], "FAIL")
        for name, message in (("syntax", "SyntaxError"), ("import", "NameError")):
            row = self.row(report, name)
            self.assertEqual(row["status"], "BUILD_FAILED")
            self.assertNotIn("killed_by", row)
            self.assertTrue(any(message in line for line in row["evidence"]), row)
        self.assertEqual(report["counts"]["BUILD_FAILED"], 2)
        self.assertEqual(report["counts"]["KILLED"], 1)

    def test_empty_replacement_deletes_the_fragment(self):
        delete = {"name": "delete low branch", "file": "calc.py", "original": "    if value < low:\n        return low\n",
                  "replacement": ""}
        code, report, _, _ = self.run_table([delete])
        self.assertEqual(code, 0, report)
        self.assertEqual(self.row(report, "delete low branch")["killed_by"], ["test_calc.CalcTests.test_low"])

    def test_unlabeled_survivor_and_mislabeled_equivalent_fail(self):
        survivor = dict(EQUIVALENT)
        del survivor["equivalent"]
        code, report, _, _ = self.run_table([survivor])
        self.assertEqual((code, self.row(report, survivor["name"])["status"]), (1, "SURVIVED"))
        mislabeled = dict(LOW, equivalent="wrongly assumed equivalent")
        code, report, _, _ = self.run_table([mislabeled])
        self.assertEqual(code, 1)
        self.assertEqual(report["mislabeled_equivalent"], ["low bound"])
        self.assertEqual(report["result"], "FAIL")

    def test_fragment_must_match_exactly_once(self):
        absent = {"name": "absent", "file": "calc.py", "original": "value <= low", "replacement": "True"}
        twice = {"name": "twice", "file": "calc.py", "original": "return ", "replacement": "return 0 or "}
        code, report, _, _ = self.run_table([absent, twice])
        self.assertEqual(code, 1)
        self.assertEqual([(row["status"], row["matches"]) for row in report["mutants"]], [("NOT_APPLIED", 0), ("NOT_APPLIED", 3)])

    def test_mutated_file_must_be_copied(self):
        code, report, _, stderr = self.run_table([LOW], copy=["test_*.py"])
        self.assertEqual((code, report), (2, None))
        self.assertIn("not copied by the table", stderr)

    def test_failure_without_a_named_test_is_not_a_kill(self):
        silent = ["{python}", "-c", "import calc, sys; sys.exit(0 if calc.clamp(-1, 0, 10) == 0 else 3)"]
        code, report, _, _ = self.run_table([LOW], test=silent)
        self.assertEqual(code, 1)
        row = self.row(report, "low bound")
        self.assertEqual((row["status"], row["exit_code"]), ("UNNAMED_FAILURE", 3))

    def test_timeout_is_a_named_kill(self):
        hang = {"name": "hang", "file": "calc.py", "original": "    return value\n",
                "replacement": "    while value == 5:\n        pass\n    return value\n"}
        code, report, _, _ = self.run_table([hang], timeout_seconds=3)
        self.assertEqual(code, 0, report)
        self.assertEqual(self.row(report, "hang")["killed_by"], ["timeout after 3 s"])

    def test_timeout_kills_the_whole_test_process_group(self):
        holder = self.code / "holder.py"
        holder.write_text("import subprocess, sys, unittest\n"
                          "import calc\n"
                          "if calc.clamp(5, 0, 10) != 5:\n"
                          "    subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)'])\n"
                          "    while True:\n"
                          "        pass\n")
        hang = {"name": "hang", "file": "calc.py", "original": "    return value\n", "replacement": "    return value + 1\n"}
        started = time.monotonic()
        code, report, _, _ = self.run_table([hang], test=["{python}", "holder.py"], timeout_seconds=2)
        self.assertLess(time.monotonic() - started, 15, "a grandchild kept the test pipe open after the timeout")
        self.assertEqual(code, 0, report)
        self.assertEqual(self.row(report, "hang")["killed_by"], ["timeout after 2 s"])

    def test_timeout_removes_children_in_their_own_session(self):
        marker = self.base / "detached.pid"
        holder = self.code / "holder.py"
        holder.write_text("import subprocess, sys\n"
                          "from pathlib import Path\n"
                          "import calc\n"
                          "if calc.clamp(5, 0, 10) != 5:\n"
                          "    child = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)'],\n"
                          "                             start_new_session=True)\n"
                          f"    Path({str(marker)!r}).write_text(str(child.pid))\n"
                          "    while True:\n"
                          "        pass\n")
        hang = {"name": "hang", "file": "calc.py", "original": "    return value\n", "replacement": "    return value + 1\n"}
        started = time.monotonic()
        code, report, _, _ = self.run_table([hang], test=["{python}", "holder.py"], timeout_seconds=2)
        self.assertLess(time.monotonic() - started, 20, "a detached child kept the run open after the timeout")
        self.assertEqual(code, 0, report)
        self.assertEqual(self.row(report, "hang")["killed_by"], ["timeout after 2 s"])
        self.assertFalse(mutate.alive(int(marker.read_text())), "the detached child survived the timeout")

    def test_same_group_leftovers_of_a_finished_command_are_removed(self):
        marker = self.base / "leftover.pid"
        leaver = ["{python}", "-c", "import subprocess, sys\n"
                  "from pathlib import Path\n"
                  "child = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(60)'])\n"
                  f"Path({str(marker)!r}).write_text(str(child.pid))\n"]
        result = mutate.run([sys.executable if item == "{python}" else item for item in leaver], self.code,
                            mutate.Session(*mutate.load_table(self.table([LOW]))).environment, 30)
        self.assertEqual((result["exit_code"], result["timed_out"]), (0, False))
        deadline = time.monotonic() + 5
        while mutate.alive(int(marker.read_text())) and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertFalse(mutate.alive(int(marker.read_text())), "a same-group child outlived its finished command")

    def test_a_stopped_command_is_not_a_finished_command(self):
        environment = mutate.Session(*mutate.load_table(self.table([LOW]))).environment
        result = mutate.run([sys.executable, "-c", "import os, signal; os.kill(os.getpid(), signal.SIGSTOP)"],
                            self.code, environment, 2)
        self.assertEqual((result["timed_out"], result["exit_code"], result["survivors"]), (True, None, []))

    def test_surviving_processes_fail_the_mutant_and_the_baseline(self):
        spawner = ["{python}", "-c", "import calc, subprocess, sys, time\n"
                   "if calc.clamp(5, 0, 10) != 5:\n"
                   "    subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)'])\n"
                   "    time.sleep(30)\n"]
        hang = {"name": "hang", "file": "calc.py", "original": "    return value\n", "replacement": "    return value + 1\n"}
        with patch.object(mutate, "alive", return_value=True):
            code, report, _, _ = self.run_table([hang], test=spawner, timeout_seconds=2)
        self.assertEqual(code, 1)
        self.assertEqual(report["baseline"]["status"], "PASS")
        row = self.row(report, "hang")
        self.assertEqual(row["status"], "CLEANUP_FAILED")
        self.assertTrue(row["survivors"])
        always = ["{python}", "-c", "import subprocess, sys, time; subprocess.Popen([sys.executable, '-c', "
                  "'import time; time.sleep(30)']); time.sleep(30)"]
        with patch.object(mutate, "alive", return_value=True):
            code, report, _, _ = self.run_table([LOW], test=always, timeout_seconds=2)
        self.assertEqual((code, report["baseline"]["status"], report["mutants"]), (1, "BASELINE_CLEANUP_FAILED", []))

    def test_colored_output_keeps_test_names(self):
        colored = ["{python}", "-c", "import calc, sys\n"
                   "if calc.clamp(-1, 0, 10) != 0:\n"
                   "    print('\\x1b[31mFAIL\\x1b[0m: test_low (test_calc.CalcTests.test_low)')\n"
                   "    sys.exit(1)\n"]
        code, report, _, _ = self.run_table([LOW], test=colored)
        self.assertEqual(code, 0, report)
        self.assertEqual(self.row(report, "low bound")["killed_by"], ["test_calc.CalcTests.test_low"])
        with patch.dict(mutate.os.environ, {"PYTHONPATH": "/shadow", "PYTHONHOME": "/home", "FORCE_COLOR": "1"}):
            for name in ("NO_COLOR", "PYTHON_COLORS"):
                mutate.os.environ.pop(name, None)
            environment = mutate.Session(*mutate.load_table(self.table([LOW]))).environment
        self.assertFalse({"PYTHONPATH", "PYTHONHOME", "FORCE_COLOR"} & set(environment))
        self.assertEqual((environment["NO_COLOR"], environment["PYTHON_COLORS"]), ("1", "0"))

    def test_import_option_and_baseline_build_failure(self):
        code, report, _, _ = self.run_table([LOW], **{"import": ["calc", "test_calc"]})
        self.assertEqual(code, 0, report)
        code, report, _, _ = self.run_table([LOW], **{"import": ["calc", "missing_module"]})
        self.assertEqual((code, report["baseline"]["status"]), (1, "BASELINE_BUILD_FAILED"))
        self.assertTrue(any("missing_module" in line for line in report["baseline"]["evidence"]))
        self.assertEqual(report["mutants"], [])

    def test_symlink_outside_root_is_rejected(self):
        outside = self.base / "outside"
        outside.mkdir()
        (outside / "calc.py").write_text(CALC)
        (self.code / "calc.py").unlink()
        (self.code / "calc.py").symlink_to(outside / "calc.py")
        code, report, _, stderr = self.run_table([LOW])
        self.assertEqual((code, report), (2, None))
        self.assertIn("not inside root", stderr)

    def test_missing_go_toolchain_is_a_usage_error(self):
        table = self.table([LOW], language="go", test=["go", "test", "."])
        with patch.object(mutate.shutil, "which", return_value=None):
            code, _, stderr = run_tool(table, "--output", self.base / "no-go")
        self.assertEqual(code, 2)
        self.assertIn("Go toolchain", stderr)
        self.assertFalse((self.base / "no-go").exists())

    def test_git_and_bytecode_directories_are_not_copied(self):
        for name in (".git/config", "__pycache__/calc.cpython.pyc", "nested/__pycache__/x.pyc"):
            (self.code / name).parent.mkdir(parents=True, exist_ok=True)
            (self.code / name).write_text("ignored\n")
        code, report, _, _ = self.run_table([LOW], copy=None)
        self.assertEqual(code, 0, report)
        self.assertEqual({row["path"] for row in report["sources"]}, {"calc.py", "test_calc.py", "calc.mutants.json"})

    def test_failing_baseline_runs_no_mutant(self):
        (self.code / "test_calc.py").write_text(TEST_CALC.replace("calc.clamp(5, 0, 10), 5", "calc.clamp(5, 0, 10), 6"))
        code, report, _, _ = self.run_table([LOW])
        self.assertEqual(code, 1)
        self.assertEqual(report["baseline"]["status"], "BASELINE_FAILED")
        self.assertEqual(report["mutants"], [])
        self.assertTrue(any("5 != 6" in line for line in report["baseline"]["evidence"]))

    def test_sources_are_never_modified(self):
        table = self.table([LOW, EQUIVALENT, {"name": "syntax", "file": "calc.py", "original": "if value < low:",
                                              "replacement": "if value < low"}])
        before = tree_state(self.code)
        self.assertEqual(run_tool(table, "--output", self.base / "isolated")[0], 1)
        self.assertEqual(tree_state(self.code), before)
        self.assertFalse((self.code / "__pycache__").exists())

    def test_source_head_is_recorded_only_for_tracked_tables(self):
        def git(*arguments):
            subprocess.run(["git", *arguments], cwd=self.base, check=True, capture_output=True)

        git("init", "-q")
        git("config", "user.email", "test@example.invalid")
        git("config", "user.name", "Test")
        (self.base / ".gitignore").write_text("run-*\nextracted/\n")
        table = self.table([LOW])
        git("add", ".gitignore", "code")
        git("commit", "-q", "-m", "fixture")
        head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=self.base, capture_output=True, text=True).stdout.strip()
        code, report, _, _ = self.run_table([LOW])
        self.assertEqual(code, 0)
        self.assertEqual(report["source"], {"head": head, "clean": True})
        (self.code / "calc.py").write_text(CALC + "\n")
        code, report, _, _ = self.run_table([LOW])
        self.assertEqual(report["source"], {"head": head, "clean": False})
        extracted = self.base / "extracted"
        shutil.copytree(self.code, extracted)
        output = self.base / "run-extracted"
        self.assertEqual(run_tool(extracted / table.name, "--output", output)[0], 0)
        self.assertIsNone(json.loads((output / "mutants.json").read_text())["source"])

    def test_existing_output_is_refused_and_left_unchanged(self):
        output = self.base / "existing"
        output.mkdir()
        (output / "keep.txt").write_text("owned by someone else\n")
        code, _, stderr = run_tool(self.table([LOW]), "--output", output)
        self.assertEqual(code, 2)
        self.assertIn("output already exists", stderr)
        self.assertEqual(tree_state(output), {"keep.txt": hashlib.sha256(b"owned by someone else\n").hexdigest()})

    def test_only_selects_named_mutants(self):
        code, report, _, _ = self.run_table([LOW, EQUIVALENT], "--only", "low bound")
        self.assertEqual(code, 0)
        self.assertEqual([row["name"] for row in report["mutants"]], ["low bound"])
        self.assertEqual(report["selected"], ["low bound"])
        code, _, stderr = run_tool(self.table([LOW]), "--output", self.base / "unknown", "--only", "missing")
        self.assertEqual(code, 2)
        self.assertFalse((self.base / "unknown").exists())

    def test_invalid_tables_are_rejected_before_any_output(self):
        outside = self.base / "outside.py"
        outside.write_text("x = 1\n")
        cases = {
            "unknown field": {"extra": 1},
            "language": {"language": "rust"},
            "duplicate": {"mutants": [LOW, LOW]},
            "no-op": {"mutants": [dict(LOW, replacement=LOW["original"])]},
            "empty original": {"mutants": [dict(LOW, original="")]},
            "empty name": {"mutants": [dict(LOW, name="")]},
            "empty reason": {"mutants": [dict(LOW, equivalent=" ")]},
            "outside root": {"mutants": [dict(LOW, file="../outside.py")]},
            "missing file": {"mutants": [dict(LOW, file="absent.py")]},
            "empty test": {"test": []},
            "timeout": {"timeout_seconds": 0},
            "no mutants": {"mutants": []},
            "extra mutant field": {"mutants": [dict(LOW, note="x")]},
            "test item": {"test": ["{python}", 1]},
            "mutants not list": {"mutants": {"name": "x"}},
            "equivalent not string": {"mutants": [dict(LOW, equivalent=True)]},
            "long timeout": {"timeout_seconds": 3601},
            "import not list": {"import": "calc"},
            "removed option": {"env": {"A": "1"}},
        }
        for name, change in cases.items():
            with self.subTest(name):
                fields = {"schema_version": 1, "language": "python", "test": UNITTEST, "mutants": [LOW]}
                fields.update(change)
                path = self.code / "bad.json"
                path.write_text(json.dumps(fields))
                output = self.base / ("bad-" + name.replace(" ", "-"))
                code, _, stderr = run_tool(path, "--output", output)
                self.assertEqual(code, 2, stderr)
                self.assertFalse(output.exists())
        (self.code / "bad.json").write_text("{")
        self.assertEqual(run_tool(self.code / "bad.json", "--output", self.base / "bad-json")[0], 2)
        self.assertEqual(run_tool(self.table([LOW]), "--output", self.base / "jobs", "--jobs", "0")[0], 2)
        self.assertEqual(run_tool(self.table([LOW]), "--output", self.base / "jobs", "--jobs", "33")[0], 2)
        for text in ("[]", "1"):
            (self.code / "bad.json").write_text(text)
            self.assertEqual(run_tool(self.code / "bad.json", "--output", self.base / "bad-list")[0], 2)
        self.assertFalse((self.base / "jobs").exists())

    def test_parallel_jobs_match_serial_results(self):
        _, serial, _, _ = self.run_table([LOW, EQUIVALENT])
        _, parallel, _, _ = self.run_table([LOW, EQUIVALENT], "--jobs", "2")
        self.assertEqual([(row["name"], row["status"]) for row in serial["mutants"]],
                         [(row["name"], row["status"]) for row in parallel["mutants"]])

    def test_evidence_parsers(self):
        go = ("=== RUN   TestSettled\n    driver_test.go:474: paused=true settled\n"
              "--- FAIL: TestSettled (0.01s)\n    --- FAIL: TestSettled/sub (0.00s)\n"
              "panic: boom\nFAIL\tnatsimage\t2.298s\nok  \tother\t0.1s\nPASS\n")
        self.assertEqual(mutate.failing_tests(go), ["TestSettled", "TestSettled/sub"])
        self.assertEqual(mutate.evidence_lines(go), ["driver_test.go:474: paused=true settled",
                                                     "--- FAIL: TestSettled (0.01s)", "--- FAIL: TestSettled/sub (0.00s)",
                                                     "panic: boom", "FAIL\tnatsimage\t2.298s"])
        python = ("FAIL: test_low (test_calc.CalcTests.test_low)\nERROR: test_x (CalcTests)\n"
                  '  File "/tmp/x/test_calc.py", line 8, in test_low\n'
                  '  File "/tmp/x/calc.py", line 2, in clamp\nAssertionError: 10 != 0\nRan 3 tests\n')
        self.assertEqual(mutate.failing_tests(python), ["test_calc.CalcTests.test_low", "CalcTests.test_x"])
        self.assertEqual(mutate.evidence_lines(python), ["FAIL: test_low (test_calc.CalcTests.test_low)",
                                                         "ERROR: test_x (CalcTests)",
                                                         'File "/tmp/x/test_calc.py", line 8, in test_low',
                                                         "AssertionError: 10 != 0"])


@unittest.skipUnless(shutil.which("go"), "Go toolchain is not installed")
class GoMutateTests(unittest.TestCase):
    def test_go_compile_failure_is_build_failed_and_kills_are_named(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            code = base / "code"
            code.mkdir()
            (code / "calc.go").write_text(GO_CODE)
            (code / "calc_test.go").write_text(GO_TEST)
            table = {"schema_version": 1, "language": "go", "go_module": "calc", "copy": ["*.go", "go.mod"],
                     "test": ["go", "test", "-count=1", "-v", "."], "timeout_seconds": 300, "mutants": [
                         {"name": "low", "file": "calc.go", "original": "if value < low {",
                          "replacement": "if value < low && false {"},
                         {"name": "unused", "file": "calc.go", "original": "\treturn value\n}",
                          "replacement": "\tunused := value\n\treturn value\n}"},
                         {"name": "high inclusive", "file": "calc.go", "original": "if value > high {",
                          "replacement": "if value >= high {", "equivalent": "At value == high both branches return high."}]}
            path = code / "mutants.json"
            path.write_text(json.dumps(table))
            result, _, _ = run_tool(path, "--output", base / "run")
            report = json.loads((base / "run" / "mutants.json").read_text())
            self.assertEqual(result, 1)
            rows = {row["name"]: row for row in report["mutants"]}
            self.assertEqual(rows["low"]["status"], "KILLED")
            self.assertEqual(rows["low"]["killed_by"], ["TestClamp"])
            self.assertIn("calc_test.go:7: low clamp = -1", rows["low"]["evidence"])
            self.assertEqual(rows["unused"]["status"], "BUILD_FAILED")
            self.assertTrue(any("declared and not used" in line for line in rows["unused"]["evidence"]), rows["unused"])
            self.assertEqual(rows["high inclusive"]["status"], "EQUIVALENT")
            self.assertFalse((code / "go.mod").exists())
            (code / "go.mod").write_text("module calc\n\ngo 1.21\n")
            result, _, _ = run_tool(path, "--output", base / "with-module", "--only", "low")
            report = json.loads((base / "with-module" / "mutants.json").read_text())
            self.assertEqual((result, report["baseline"]["status"]), (0, "PASS"))


if __name__ == "__main__":
    unittest.main()
