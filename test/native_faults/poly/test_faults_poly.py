"""Portable controls of the Bor fault probe driver (E01-04.early_faults, Bor block)."""

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

import faults_poly  # noqa: E402

SOURCE = {"native_build": "test", "tool": "test_faults_poly.py"}
HANDOFFS = {"bor.pass-at-golive", "bor.below-h-rewind", "bor.golive-order", "bor.bootstrap-vote-pass",
            "bor.probe-flag-help", "bor.power-loss-model"}


def sample():
    """The development probe report with each case's export rebuilt."""
    data = json.loads((HERE / "sample_report.json").read_text())
    shared = data.pop("shared_blocks")
    events = data.pop("shared_state_sync")
    for case in data["cases"]:
        case["export"] = dict(case.pop("export_rest"), blocks=copy.deepcopy(shared))
        case["state_sync"] = copy.deepcopy(events)
    return data


def case_of(probe, point, variant):
    for case in probe["cases"]:
        if case["point"] == point and case["variant"] == variant:
            return case
    raise KeyError(point)


class JudgeTests(unittest.TestCase):
    def verdict(self, probe):
        return faults_poly.judge(probe, source=SOURCE)[0]

    def test_sample_passes_every_case_and_waits_for_the_handoffs(self):
        verdict = self.verdict(sample())
        self.assertEqual((verdict["failures"], verdict["native_errors"]), ([], []))
        self.assertEqual(set(verdict["cases"].values()), {"PASS"})
        self.assertEqual(len(verdict["cases"]), 30)
        self.assertEqual(verdict["ac"], {"AC-SCP-03-01": "PASS"})
        self.assertEqual(verdict["result"], "INCOMPLETE")
        self.assertEqual(set(verdict["handoffs"]), HANDOFFS)

    def test_handoffs_pass_through(self):
        probe = sample()
        probe["handoffs"] = [{"id": hid, "verdict": "PASS", "evidence": "x"} for hid in sorted(HANDOFFS)]
        self.assertEqual(self.verdict(probe)["result"], "PASS")

    def failing(self, change, message, point="adm.after-admitted-sync", variant="power_loss"):
        probe = sample()
        change(case_of(probe, point, variant))
        verdict = self.verdict(probe)
        self.assertEqual(verdict["result"], "FAIL")
        self.assertIn(message, " ".join(verdict["failures"] + verdict["native_errors"]))

    def test_native_step_errors_fail(self):
        self.failing(lambda c: c.update(child_error="exit status 2: panic"), "child_error exit status 2")
        self.failing(lambda c: c.update(restart_error="boom"), "restart_error boom")
        self.failing(lambda c: c.update(golive_error="refused"), "golive_error refused")
        self.failing(lambda c: c["import"].update(error="bad block"), "import error bad block")

    def test_export_errors_fail(self):
        self.failing(lambda c: c["export"].pop("undecided_intents"), "undecided intents None")
        self.failing(lambda c: c["export"].update(undecided_intents=["k"]), "undecided intents ['k']")
        self.failing(lambda c: c["export"].update(reimport_error="x"), "re-import error")
        self.failing(lambda c: c["export"].update(export_error="trace"), "export error trace")
        self.failing(lambda c: c["export"]["blocks"].pop(), "one row per block")

    def test_corpus_difference_fails_the_case(self):
        def receipt_gas(case):
            case["export"]["blocks"][0]["transactions"][0]["receipt"]["gas_used"] += 1
        self.failing(receipt_gas, "native result does not match the smoke corpus")
        self.failing(lambda c: c.pop("export"), "native result does not match the smoke corpus")

    def test_state_sync_counts(self):
        self.failing(lambda c: c.update(state_sync=[]), "native result does not match the smoke corpus")

        # The comparator checks the event id, its receipt logs and
        # lastStateId(); the raw event data reaches it through the logs.
        def event_id(case):
            case["state_sync"][0]["id"] += 1
        self.failing(event_id, "native result does not match the smoke corpus")

        def no_receipt(case):
            case["export"]["blocks"][3]["state_sync_receipt"] = False
        self.failing(no_receipt, "native result does not match the smoke corpus")

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
        case["export"]["decisions"] = [{"block": 1, "kind": "CANONICAL"}] + case["export"]["decisions"][1:]
        result, rows = faults_poly.corpus_result(json.loads(faults_poly.EXPECTED.read_text()), case, SOURCE)
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
        report, corpus = faults_poly.to_faults_report(sample(), json.loads(faults_poly.EXPECTED.read_text()), SOURCE)
        self.assertEqual(report["storage_model"], "pebble-v1-strict-mem")
        self.assertEqual(report["family"], "poly")
        self.assertEqual(len(report["cases"]), 30)
        self.assertTrue(all(case["final"]["corpus"] == "PASS" and "export" not in case for case in report["cases"]))
        self.assertEqual(set(map(len, corpus.values())), {0})


def go_block(text, header):
    """The source of one top-level Go declaration starting with header, to its closing brace at column 0."""
    start = text.index("\n" + header) + 1
    if text[start:].split("\n", 1)[0].endswith("}"):
        return text[start:text.index("\n", start) + 1]
    return text[start:text.index("\n}\n", start) + 2]


# Declarations copied from the accepted POLY corpus probe, by header.
CORPUS_SHARED = ("func readBounded(", "type inputs struct", "func loadInputs(", "type stubTransport struct",
                 "func (s stubTransport) RoundTrip(", "func newStack(", "func backend(", "func readBlocks(",
                 "func preTransaction(", "type Account struct", "func readAccounts(", "type LogRow struct",
                 "func logRows(", "type TxRow struct", "type BlockRow struct", "func exportChain(")
# The fault store, its batch, the arming table, the catalog order and the
# state reading of the Geth probe, by header.
GETH_SHARED = ("type faultHit struct", "type arm struct", "type faultStore struct", "type faultBatch struct",
               "func (s *faultStore) note(", "func (s *faultStore) nativeWrite(", "func (s *faultStore) record(",
               "func (s *faultStore) Put(", "func (s *faultStore) Delete(", "func (s *faultStore) SyncKeyValue(",
               "func (s *faultStore) NewBatch(", "func (s *faultStore) NewBatchWithSize(",
               "func (b *faultBatch) Put(", "func (b *faultBatch) Delete(", "func (b *faultBatch) Reset(",
               "func (b *faultBatch) Write(", "func suffix(", "func pinPut(", "func pinDrop(", "func arms(",
               "var points = ", "func below(", "func states(", "func guarded(", "func boundaryOf(",
               "func nativeAt(", "func has(", "func prefixed(", "func check(", "func errText(", "func plainHash(")
# The decisions of the BSC probe (one row per member), by header.
BSC_SHARED = ("type intentRow struct", "func decisions(", "func counted(")


class CopiedCodeTests(unittest.TestCase):
    def setUp(self):
        self.ours = (HERE / "main.go").read_text()

    def same(self, other, headers):
        for header in headers:
            self.assertEqual(go_block(self.ours, header), go_block(other, header), header)

    def test_inputs_and_export_equal_the_accepted_poly_corpus_probe(self):
        self.same((HERE.parents[1] / "native_corpus" / "poly" / "main.go").read_text(), CORPUS_SHARED)

    def test_fault_store_equals_the_geth_probe(self):
        self.same((HERE.parent / "eth" / "probe.go").read_text(), GETH_SHARED)

    def test_decisions_equal_the_bsc_probe(self):
        self.same((HERE.parent / "bsc" / "faults_test.go").read_text(), BSC_SHARED)

    def test_storage_helpers_differ_from_bsc_only_by_the_directory(self):
        bsc = (HERE.parent / "bsc" / "faults_test.go").read_text()
        # The BSC probe keeps one chaindata path; each Bor node runs on its own
        # OS data directory, so the Bor helpers take the directory.
        self.assertEqual(go_block(self.ours, "func settleWAL(").replace("mem *vfs.MemFS, dir string", "mem *vfs.MemFS")
                         .replace("List(dir)", "List(memDir)").replace("Join(dir, name)", "Join(memDir, name)"),
                         go_block(bsc, "func settleWAL("))
        self.assertEqual(go_block(self.ours, "func freshFS(").replace("freshFS(dir string)", "freshFS()")
                         .replace("MkdirAll(dir,", "MkdirAll(memDir,").replace("syncDirs(mem, dir)", "syncDirs(mem)"),
                         go_block(bsc, "func freshFS("))

    def test_restart_resets_the_process_global_hooks(self):
        start = go_block(self.ours, "func start(")
        self.assertIn("rawdb.ExtractorRequire, rawdb.ExtractorFinality, whitelist.ExtractorEnter = nil, nil, nil",
                      start)
        self.assertLess(start.index("whitelist.ExtractorEnter = nil"), start.index("newStack(dataDir)"))

    def test_each_case_runs_in_its_own_process(self):
        self.assertIn('cmd.Args[1] = "case"', go_block(self.ours, "func run("))


class CommandTests(unittest.TestCase):
    def test_judge_command_exit_code(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "report.json"
            path.write_text(json.dumps(sample()))
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(faults_poly.main(["judge", str(path)]), 1)


if __name__ == "__main__":
    unittest.main()
