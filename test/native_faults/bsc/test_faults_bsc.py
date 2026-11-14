"""Portable controls of the BSC fault probe driver (E01-04.early_faults, BSC block)."""

import contextlib
import copy
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import faults_bsc  # noqa: E402

SOURCE = {"native_build": "test", "tool": "test_faults_bsc.py"}


def sample():
    """The development probe report with each case's export rebuilt."""
    data = json.loads((HERE / "sample_report.json").read_text())
    shared = data.pop("shared_blocks")
    for case in data["cases"]:
        case["export"] = dict(case.pop("export_rest"), blocks=copy.deepcopy(shared))
    return data


def case_of(probe, point, variant):
    for case in probe["cases"]:
        if case["point"] == point and case["variant"] == variant:
            return case
    raise KeyError(point)


class JudgeTests(unittest.TestCase):
    def verdict(self, probe):
        return faults_bsc.judge(probe, source=SOURCE)[0]

    def test_sample_passes_every_case_and_waits_for_the_handoffs(self):
        verdict = self.verdict(sample())
        self.assertEqual((verdict["failures"], verdict["native_errors"]), ([], []))
        self.assertEqual(set(verdict["cases"].values()), {"PASS"})
        self.assertEqual(len(verdict["cases"]), 30)
        self.assertEqual(verdict["ac"], {"AC-SCP-03-01": "PASS"})
        self.assertEqual(verdict["result"], "INCOMPLETE")
        self.assertEqual(set(verdict["handoffs"]), {"bsc.pass-at-golive", "bsc.store-write-errors", "bsc.skip-block"})

    def test_handoffs_pass_through(self):
        probe = sample()
        probe["handoffs"] = [{"id": hid, "verdict": "PASS", "evidence": "x"}
                             for hid in ("bsc.pass-at-golive", "bsc.store-write-errors", "bsc.skip-block")]
        self.assertEqual(self.verdict(probe)["result"], "PASS")

    def failing(self, change, message, point="adm.after-admitted-sync", variant="power_loss"):
        probe = sample()
        change(case_of(probe, point, variant))
        verdict = self.verdict(probe)
        self.assertEqual(verdict["result"], "FAIL")
        self.assertIn(message, " ".join(verdict["failures"] + verdict["native_errors"]))

    def test_native_step_errors_fail(self):
        self.failing(lambda c: c.update(restart_error="boom"), "restart_error boom")
        self.failing(lambda c: c.update(golive_error="refused"), "golive_error refused")
        self.failing(lambda c: c["import"].update(error="bad block"), "import error bad block")

    def test_export_errors_fail(self):
        self.failing(lambda c: c["export"].pop("undecided_intents"), "undecided intents None")
        self.failing(lambda c: c["export"].update(undecided_intents=["k"]), "undecided intents ['k']")
        self.failing(lambda c: c["export"].update(reimport_error="x"), "re-import error")
        self.failing(lambda c: c["export"]["blocks"].pop(), "one row per block")

    def test_corpus_difference_fails_the_case(self):
        def receipt_gas(case):
            case["export"]["blocks"][0]["transactions"][0]["receipt"]["gas_used"] += 1
        self.failing(receipt_gas, "native result does not match the smoke corpus")
        self.failing(lambda c: c.pop("export"), "native result does not match the smoke corpus")
        self.failing(lambda c: c["export"].update(blocks=[{"number": 1}, {"number": 2}]),
                     "native result does not match the smoke corpus")

    def test_reimport_rows_still_count(self):
        self.failing(lambda c: c["export"]["reimport"].update(new_decisions=1),
                     "native result does not match the smoke corpus")
        self.failing(lambda c: c["export"]["reimport"].update(new_observations=1),
                     "native result does not match the smoke corpus")

    def test_decision_of_another_block_fails_the_corpus(self):
        self.failing(lambda c: c["export"]["decisions"].append({"block": 0, "kind": "IMPORT"}),
                     "native result does not match the smoke corpus")

    def test_decision_kind_alone_does_not_fail_the_corpus(self):
        case = case_of(sample(), "dec.after-decided-sync", "crash")
        case["export"]["decisions"] = [{"block": 1, "kind": "CANONICAL"}, {"block": 2, "kind": "IMPORT"}]
        result, rows = faults_bsc.corpus_result(json.loads(faults_bsc.EXPECTED.read_text()), case["export"], SOURCE)
        self.assertEqual((result, rows), ("PASS", []))

    def test_catalog_failure_is_reported(self):
        self.failing(lambda c: c["after_restart"].update(admission="none"), "admission none after restart")

    def test_storage_model_and_malformed_case(self):
        probe = sample()
        probe["storage_model"] = "pebble-v2-crashable-mem"
        self.assertIn("probe: storage_model is not pebble-v1-strict-mem", self.verdict(probe)["native_errors"])
        probe = sample()
        probe["cases"][0] = "not a case"
        verdict = self.verdict(probe)
        self.assertEqual(verdict["result"], "FAIL")
        self.assertIn("case 0: malformed", verdict["native_errors"])

    def test_faults_report_shape(self):
        report, corpus = faults_bsc.to_faults_report(sample(), json.loads(faults_bsc.EXPECTED.read_text()), SOURCE)
        self.assertEqual(report["storage_model"], "pebble-v1-strict-mem")
        self.assertEqual(len(report["cases"]), 30)
        self.assertTrue(all(case["final"]["corpus"] == "PASS" and "export" not in case for case in report["cases"]))
        self.assertEqual(set(map(len, corpus.values())), {0})


COPIED = ("exportBlock", "sameLogs", "accountsAt", "logsOf", "addressText")


def go_block(text, header):
    """The source of one top-level Go declaration starting with header, to its closing brace at column 0."""
    start = text.index("\n" + header) + 1
    if text[start:].split("\n", 1)[0].endswith("}"):
        return text[start:text.index("\n", start) + 1]
    return text[start:text.index("\n}\n", start) + 2]


def go_function(text, name):
    return go_block(text, f"func {name}(")


# The fault store, its batch, the arming table and the catalog order of the
# Geth probe, by declaration header.
GETH_SHARED = ("type faultHit struct", "type arm struct", "type faultStore struct", "type faultBatch struct",
               "func (s *faultStore) note(", "func (s *faultStore) nativeWrite(", "func (s *faultStore) record(",
               "func (s *faultStore) Put(", "func (s *faultStore) Delete(", "func (s *faultStore) SyncKeyValue(",
               "func (s *faultStore) NewBatch(", "func (s *faultStore) NewBatchWithSize(",
               "func (b *faultBatch) Put(", "func (b *faultBatch) Delete(", "func (b *faultBatch) Reset(",
               "func (b *faultBatch) Write(", "func suffix(", "func pinPut(", "func pinDrop(", "func arms(",
               "var points = ", "func below(", "func states(", "func guarded(", "func boundaryOf(",
               "func nativeAt(", "func has(", "func prefixed(", "func counted(")


class CopiedCodeTests(unittest.TestCase):
    def test_export_helpers_equal_the_accepted_bsc_corpus_probe(self):
        ours = (HERE / "faults_test.go").read_text()
        accepted = (HERE.parents[1] / "native_corpus" / "bsc" / "corpus_test.go").read_text()
        for name in COPIED:
            self.assertEqual(go_function(ours, name), go_function(accepted, name), name)

    def test_fault_store_equals_the_geth_probe(self):
        ours = (HERE / "faults_test.go").read_text()
        geth = (HERE.parent / "eth" / "probe.go").read_text()
        for header in GETH_SHARED:
            self.assertEqual(go_block(ours, header), go_block(geth, header), header)

    def test_bsc_specific_differences(self):
        ours = (HERE / "faults_test.go").read_text()
        geth = (HERE.parent / "eth" / "probe.go").read_text()
        # settleWAL skips a WAL file that the abandoned store removed between
        # the listing and the stat.
        self.assertEqual(go_function(ours, "settleWAL"), go_function(geth, "settleWAL").replace(
            """\t\t\t\tinfo, err := mem.Stat(filepath.Join(memDir, name))
\t\t\t\tcheck(err)
\t\t\t\ttotal += info.Size()
""", """\t\t\t\tif info, err := mem.Stat(filepath.Join(memDir, name)); err == nil {
\t\t\t\t\ttotal += info.Size()
\t\t\t\t}
"""))
        # decisions gives one row per member, so a decision without members
        # (a FINALITY change of a BSC import) gives none.
        self.assertIn("for _, member := range intent.Members {", go_function(ours, "decisions"))
        self.assertNotIn("has %d members, want 1", go_function(ours, "decisions"))


class CommandTests(unittest.TestCase):
    def test_judge_command_exit_code(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "report.json"
            path.write_text(json.dumps(sample()))
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(faults_bsc.main(["judge", str(path)]), 1)


if __name__ == "__main__":
    unittest.main()
