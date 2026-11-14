"""Portable controls of the Geth fault probe driver (E01-04.early_faults, Geth block)."""

import copy
import json
import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import faults_geth  # noqa: E402

SOURCE = {"native_build": "test", "tool": "test_faults_geth.py"}


def sample():
    """The development probe report with each case's observation rebuilt."""
    data = json.loads((HERE / "sample_report.json").read_text())
    shared = data.pop("shared_observation")
    for case in data["cases"]:
        case["observation"] = dict(copy.deepcopy(shared), decisions=case.pop("decisions"))
    return data


def case_of(probe, point, variant):
    for case in probe["cases"]:
        if case["point"] == point and case["variant"] == variant:
            return case
    raise KeyError(point)


class JudgeTests(unittest.TestCase):
    def verdict(self, probe):
        return faults_geth.judge(probe, source=SOURCE)[0]

    def test_sample_passes_every_case_and_waits_for_the_handoffs(self):
        verdict = self.verdict(sample())
        self.assertEqual(verdict["failures"], [])
        self.assertEqual(verdict["native_errors"], [])
        self.assertEqual(set(verdict["cases"].values()), {"PASS"})
        self.assertEqual(len(verdict["cases"]), 30)
        self.assertEqual(verdict["ac"], {"AC-SCP-03-01": "PASS"})
        self.assertEqual(verdict["result"], "INCOMPLETE")
        self.assertEqual(set(verdict["handoffs"].values()), {"NOT_RUN"})

    def test_sample_has_recovered_obligations_with_another_kind(self):
        kinds = {row["kind"] for case in sample()["cases"] for row in case["observation"]["decisions"]}
        self.assertIn("CANONICAL", kinds)

    def test_handoffs_pass_through(self):
        probe = sample()
        probe["handoffs"] = [{"id": hid, "verdict": "PASS", "evidence": "x"} for hid in
                             ("geth.pass-at-golive", "geth.sethead-interrupted", "geth.repaired-judge",
                              "geth.mid-reorg-long-branch")]
        self.assertEqual(self.verdict(probe)["result"], "PASS")

    def failing(self, change, message, point="adm.after-admitted-sync", variant="power_loss"):
        probe = sample()
        change(case_of(probe, point, variant))
        verdict = self.verdict(probe)
        self.assertEqual(verdict["result"], "FAIL")
        text = " ".join(verdict["failures"] + verdict["native_errors"])
        self.assertIn(message, text)

    def test_native_step_errors_fail(self):
        self.failing(lambda c: c.update(restart_error="boom"), "restart_error boom")
        self.failing(lambda c: c.update(golive_error="refused"), "golive_error refused")
        self.failing(lambda c: c["import"].update(error="bad block"), "import error bad block")

    def test_undecided_intents_and_reimport_errors_fail(self):
        self.failing(lambda c: c["observation"]["native"].update(undecided_intents=["k"]), "undecided intents")
        self.failing(lambda c: c["observation"]["native"].update(reimport_error="x"), "re-import error")

    def test_native_reexecution_must_match(self):
        self.failing(lambda c: c["observation"]["native"]["reexecution"][0].update(root_equal=False),
                     "re-execution of block 1 differs")
        self.failing(lambda c: c["observation"]["native"]["reexecution"][0]["receipts"][0].update(receipt_equal=False),
                     "re-execution of block 1 differs")

    def test_corpus_difference_fails_the_case(self):
        def change(case):
            case["observation"]["blocks"][0]["gas_used"] += 1
        self.failing(change, "native result does not match the smoke corpus")

    def test_missing_or_malformed_observation_fails(self):
        self.failing(lambda c: c.pop("observation"), "native result does not match the smoke corpus")
        self.failing(lambda c: c.update(observation={"family": "eth"}), "native result does not match the smoke corpus")

    def test_decision_kind_alone_does_not_fail_the_corpus(self):
        probe = sample()
        case = case_of(probe, "dec.after-decided-sync", "crash")
        case["observation"]["decisions"] = [{"block": 1, "kind": "CANONICAL"}, {"block": 2, "kind": "IMPORT"}]
        result, rows = faults_geth.corpus_result(json.loads(faults_geth.EXPECTED.read_text()),
                                                 case["observation"], SOURCE)
        self.assertEqual((result, rows), ("PASS", []))

    def test_catalog_failure_is_reported(self):
        self.failing(lambda c: c["after_restart"].update(admission="none"), "admission none after restart")

    def test_faults_report_shape(self):
        report, corpus = faults_geth.to_faults_report(sample(), json.loads(faults_geth.EXPECTED.read_text()), SOURCE)
        self.assertEqual(report["storage_model"], "pebble-v2-crashable-mem")
        self.assertEqual(len(report["cases"]), 30)
        self.assertTrue(all("observation" not in case and case["final"]["corpus"] == "PASS"
                            for case in report["cases"]))
        self.assertEqual(set(map(len, corpus.values())), {0})

    def test_non_dict_cases_are_skipped_and_fail_as_missing(self):
        probe = sample()
        probe["cases"][0] = "not a case"
        verdict = self.verdict(probe)
        self.assertIn("NOT_RUN", verdict["cases"].values())


class CommandTests(unittest.TestCase):
    def test_judge_command_exit_code(self):
        import contextlib
        import io
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "report.json"
            path.write_text(json.dumps(sample()))
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(faults_geth.main(["judge", str(path)]), 1)


if __name__ == "__main__":
    unittest.main()
