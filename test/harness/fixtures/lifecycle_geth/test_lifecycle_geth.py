"""Portable controls of the Geth lifecycle driver; no Docker or Go toolchain needed.

sample_report.json is the report of a development run of the probe (not
measured evidence); the tests judge it and single defects of it.
"""

import copy
import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import lifecycle_geth  # noqa: E402

SAMPLE = json.loads((lifecycle_geth.FIXTURE / "sample_report.json").read_text())
PATCHES = json.loads((lifecycle_geth.NATIVE / "patches.json").read_text())
CALLERS = json.loads((lifecycle_geth.SHARED / "callers.json").read_text())


def step(report, path, name):
    return [st for st in report[path]["steps"] if st["name"] == name][0]


class Judge(unittest.TestCase):
    def test_sample_passes(self):
        failures, matrix, result = lifecycle_geth.judge(copy.deepcopy(SAMPLE))
        self.assertEqual(failures, [])
        self.assertEqual(result, "PASS")
        self.assertEqual(matrix["verdicts"], {"AC-CAP-01-01": "PASS", "AC-LIF-05-02": "PASS", "AC-LIF-05-03": "PASS",
                                              "AC-LIF-06-04": "NOT_APPLICABLE", "AC-LIF-06-05": "NOT_APPLICABLE"})
        self.assertEqual(matrix["family"], "PASS")
        for slot in lifecycle_geth.PUBLIC_SLOTS:
            self.assertEqual(set(matrix["public"][slot].values()), {"NOT_RUN"})

    def test_each_defect_fails(self):
        defects = {
            "native-commit stamp": lambda r: r.update(native_identity="0" * 40),
            "flags_dev": lambda r: r["profile"].update(flags_dev=None),
            "full sync is refused": lambda r: r["profile"].update(flags_full="refused"),
            "another native commit": lambda r: r["profile"].update(eth_new_wrong_commit=None),
            "native: the first start is not BOOTSTRAP": lambda r: r["native_caller"]["bootstrap"].update(
                phase_at_open="RECOVERY"),
            "initial native sync left": lambda r: r["native_caller"]["bootstrap"].update(
                decision_records_before_live=1),
            "boundary is not A3": lambda r: r["native_caller"]["bootstrap"].update(boundary_root_durable=False),
            "harness: going live failed": lambda r: r["harness"]["bootstrap"].update(golive_error="x"),
            "decisions": lambda r: step(r, "native_caller", "forkchoice head A4")["decisions"].pop(),
            "not NATIVE_DECIDED": lambda r: step(r, "native_caller", "newPayload A4")["decisions"][0].update(
                state="INTENT"),
            "unexpected error": lambda r: step(r, "native_caller", "newPayload B3").update(error="boom"),
            "expected an error": lambda r: step(r, "native_caller", "Reset").update(error=None),
            "references": lambda r: step(r, "native_caller", "SetHead 3")["refs"].update(safe="S4"),
            "are not the expected steps": lambda r: r["native_caller"]["steps"].pop(),
            "keep the boundary": lambda r: r["native_caller"]["restart_boundary"].update(hash="C4"),
            "state of the BOOTSTRAP blocks": lambda r: r["native_caller"]["state_after_restart"].update(S3=True),
            "deadlock": lambda r: r["native_caller"]["deadlock"].update(completed=False),
            "out of order": lambda r: r["native_caller"].update(exit_errors_restart=["x"]),
            "geth.manual-commands": lambda r: r["native_caller"]["outside_gate_refusals"].update(
                {"geth.manual-commands": None}),
            "required callers": lambda r: r.update(gaps=["geth.side-chain"]),
            "native: the gate is not LIVE": lambda r: r["native_caller"]["bootstrap"].update(phase_after="BOOTSTRAP"),
            "harness: the gate is not LIVE": lambda r: r["harness"]["bootstrap"].update(boundary_present=False),
            "crash point": lambda r: r["crashes"][0].update(exit_code=0),
            "did not stop as expected": lambda r: r["crashes"][3].update(attach_error="boom"),
            "did not stop as expected ": lambda r: r["crashes"][5].update(attach_error=None),
            "reconciliation decision": lambda r: r["crashes"][1].update(settled=r["crashes"][1]["settled"][:1]),
            "safe is not": lambda r: r["crashes"][1]["refs"].update(safe="A5"),
            "forward forkchoice": lambda r: r["crashes"][4].update(forward_error=None),
            "did not settle": lambda r: r["crashes"][1].update(settled=[]),
            "native head is not": lambda r: r["crashes"][2]["refs"].update(head_hash="A5"),
            "stays unresolved": lambda r: r["crashes"][2]["decisions"][0].update(state="INTENT"),
        }
        for message, change in defects.items():
            report = copy.deepcopy(SAMPLE)
            change(report)
            failures, matrix, result = lifecycle_geth.judge(report)
            self.assertEqual(result, "FAIL", message)
            self.assertTrue(any(message.strip() in row for row in failures), (message, failures))
            self.assertEqual(matrix["family"], "FAIL", message)

    def test_ac_cap_01_01_needs_equal_paths(self):
        report = copy.deepcopy(SAMPLE)
        report["ac_cap_01_01"]["harness"]["final"]["head_hash"] = "B3"
        _, matrix, result = lifecycle_geth.judge(report)
        self.assertEqual(matrix["verdicts"]["AC-CAP-01-01"], "FAIL")
        self.assertEqual(result, "FAIL")

    def test_ac_cap_01_01_needs_the_bridge(self):
        report = copy.deepcopy(SAMPLE)
        for side in ("native", "harness"):
            for row in report["ac_cap_01_01"][side]["decisions"]:
                row[1] = []
        _, matrix, _ = lifecycle_geth.judge(report)
        self.assertEqual(matrix["verdicts"]["AC-CAP-01-01"], "FAIL")

    def test_zero_hash_noop_rows(self):
        for path in ("native_caller", "harness"):
            report = copy.deepcopy(SAMPLE)
            step(report, path, "AC-LIF-05-02 forkchoice zero safe and finalized")["refs"]["safe"] = ""
            self.assertEqual(lifecycle_geth.qa_matrix(report, [])["verdicts"]["AC-LIF-05-02"], "FAIL", path)

    def test_finality_rows_are_checked_by_field(self):
        report = copy.deepcopy(SAMPLE)
        rows = step(report, "native_caller", "SetHead 3")["decisions"]
        rows[1]["finality"], rows[2]["finality"] = rows[2]["finality"], rows[1]["finality"]
        failures, _, result = lifecycle_geth.judge(report)
        self.assertEqual(result, "FAIL")
        self.assertTrue(any("SetHead 3: decisions" in row for row in failures), failures)

    def test_zero_hash_noop_makes_no_decision(self):
        for path, name in (("native_caller", "AC-LIF-05-02 forkchoice zero safe and finalized"),
                           ("harness", "forkchoice unchanged safe A2 finalized A1")):
            report = copy.deepcopy(SAMPLE)
            step(report, path, name)["decisions"] = [{"kind": "FINALITY", "state": "NATIVE_DECIDED"}]
            self.assertEqual(lifecycle_geth.qa_matrix(report, [])["verdicts"]["AC-LIF-05-02"], "FAIL", path)

    def test_setter_then_error_rows(self):
        for path, change in (("native_caller", {"error": None}), ("harness", {"decisions": []})):
            report = copy.deepcopy(SAMPLE)
            step(report, path, "AC-LIF-05-03 forkchoice finalized A2 then unknown safe").update(change)
            self.assertEqual(lifecycle_geth.qa_matrix(report, [])["verdicts"]["AC-LIF-05-03"], "FAIL", path)
        report = copy.deepcopy(SAMPLE)
        step(report, "native_caller", "AC-LIF-05-03 forkchoice finalized A2 then unknown safe")["refs"]["safe"] = "ff"
        self.assertEqual(lifecycle_geth.qa_matrix(report, [])["verdicts"]["AC-LIF-05-03"], "FAIL")

    def test_failures_fail_every_applicable_row(self):
        matrix = lifecycle_geth.qa_matrix(copy.deepcopy(SAMPLE), ["x"])
        self.assertEqual({k: v for k, v in matrix["verdicts"].items() if v != "NOT_APPLICABLE"},
                         {"AC-CAP-01-01": "FAIL", "AC-LIF-05-02": "FAIL", "AC-LIF-05-03": "FAIL"})

    def test_not_a_report(self):
        self.assertEqual(lifecycle_geth.native_failures({}), ["the report is not a lifecycle probe report"])


class PatchSet(unittest.TestCase):
    def test_shape(self):
        self.assertEqual(PATCHES["source_commit"], lifecycle_geth.NATIVE_SOURCE)
        self.assertEqual(PATCHES["files"], {"core_lifecycle.go.txt": "core/extractor_lifecycle.go"})
        names = [p["name"] for p in PATCHES["patches"]]
        self.assertEqual(len(names), len(set(names)))
        # Patches that only rewrite a native line keep no anchor line.
        rewrites = ("side_chain_err_scope", "process_block_observing", "process_block_bootstrap", "head_batch_lifecycle",
                    "backend_attach_lifecycle")
        for p in PATCHES["patches"]:
            self.assertEqual(set(p), {"name", "file", "original", "replacement", "reason"})
            self.assertEqual(sum(row["original"] == p["original"] for row in PATCHES["patches"]), 1, p["name"])
            if p["name"] not in rewrites:
                self.assertIn(p["original"].strip().splitlines()[0].strip(), p["replacement"], p["name"])
            if p["name"] not in rewrites + ("geth_flags_import",):
                self.assertIn("E01-02.lifecycle_seams", p["replacement"], p["name"])

    def test_every_routed_geth_caller_is_wired(self):
        text = "".join(p["replacement"] for p in PATCHES["patches"])
        text += (lifecycle_geth.NATIVE / "core_lifecycle.go.txt").read_text()
        text += (lifecycle_geth.NATIVE / "lifecycle.go").read_text()
        for caller in CALLERS["callers"]:
            if caller["client"] == "geth" and caller["route"] in ("OBSERVATION", "DECISION", "REFUSE") and \
                    caller["id"] not in ("geth.manual-commands",):
                self.assertIn(f'"{caller["id"]}"', text, caller["id"])

    def test_probe_scenario_matches_expectations(self):
        probe = (lifecycle_geth.FIXTURE / "probe.go").read_text()
        for name in lifecycle_geth.EXPECTED_STEPS:
            self.assertIn(f'"{name}"' if not name.startswith("bootstrap newPayload") else '"bootstrap newPayload "',
                          probe, name)


class CommandLine(unittest.TestCase):
    def test_usage(self):
        import unittest.mock
        with self.assertRaises(SystemExit) as raised, unittest.mock.patch("sys.stderr"):
            lifecycle_geth.main([])
        self.assertEqual(raised.exception.code, 2)


if __name__ == "__main__":
    unittest.main()
