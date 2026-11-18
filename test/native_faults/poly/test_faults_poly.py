"""Portable controls of the Bor fault probe driver (E01-04.early_faults, Bor block)."""

import contextlib
import copy
import io
import json
import sys
import tempfile
import unittest
from unittest import mock
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


def handoff_sample():
    """Synthetic native facts for judge controls, never measured evidence."""
    blocks = json.loads((HERE / "sample_report.json").read_text())["blocks"]
    block = {"hash": blocks["1"], "root": "cd" * 32}
    boundary = dict(block, number=1)
    reach = {"bor.milestone-lock": {"Passes": 1, "Intents": 0, "Refusals": 0}}
    common = {"phase_at_start": "BOOTSTRAP", "block1": block, "head": 2}
    vote = dict(common, vote_locked=True, guard_matches_vote=True,
                order=["chain-locked", "guard-contended", "vote-release", "guard-acquired"], boundary_held=None,
                boundary_before_vote_release=None, decisions_held={}, decisions_before_golive={},
                coverage_before={}, coverage_held=reach, coverage_released=reach,
                golive_error=None, phase="LIVE", boundary=boundary)
    rows = {
        "bor.pass-at-golive": dict(common, native_head_write_held=True, golive_attempted=True,
                                   boundary_during_import=None, import_error=None, golive_error=None,
                                   block2_error=None, boundary=boundary, observations_before_block2=0,
                                   decided_before_block2=[], decided=[2]),
        "bor.below-h-rewind": dict(common, rewind_error="rewind to 0 is below the live boundary 1",
                                   boundary_before=boundary, boundary_after=boundary,
                                   head_before="ef" * 32, head_after="ef" * 32,
                                   decisions_before={"intent": "native"}, decisions_after={"intent": "native"}),
        "bor.bootstrap-vote-pass": copy.deepcopy(vote),
        "bor.golive-order": copy.deepcopy(vote),
        "bor.power-loss-model": dict(common, root_present_before_fixture=True, fixture_deletion_synced=True,
                                     sentinel_before_reset=True, sentinel_after_reset=False, reset_to_synced=True,
                                     fixture_root_after_reset=False, restart_error=None, phase_after="LIVE",
                                     head_before=4, head_after=3, decided_before=[1, 2, 3, 4], undecided=[],
                                     roots_distinct=True, predecessor_state_before=True, predecessor_state_after=True,
                                     predecessor={"number": 3, "hash": blocks["3"], "root": "12" * 32},
                                     boundary_before={"number": 0}, boundary_after={"number": 0},
                                     recovery={"repaired": {"number": 3, "hash": blocks["3"]}}),
    }
    for hid, row in rows.items():
        row["id"] = hid
    return rows


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

    def test_supplied_handoff_verdicts_are_not_trusted(self):
        probe = sample()
        probe["handoffs"] = [{"id": hid, "verdict": "PASS", "evidence": "x"} for hid in sorted(HANDOFFS)]
        self.assertEqual(self.verdict(probe)["result"], "INCOMPLETE")

    def test_handoff_facts_pass_and_each_required_fact_is_bound(self):
        probe = sample()
        probe["handoff_runs"] = handoff_sample()
        self.assertEqual(self.verdict(probe)["result"], "PASS")
        for hid, row in handoff_sample().items():
            for key in row:
                if key == "head" and hid != "bor.pass-at-golive" and hid != "bor.below-h-rewind":
                    continue
                changed = {"blocks": probe["blocks"], "handoff_runs": copy.deepcopy(probe["handoff_runs"])}
                del changed["handoff_runs"][hid][key]
                with self.subTest(handoff=hid, key=key):
                    verdicts = {r["id"]: r["verdict"] for r in faults_poly.handoff_verdicts(changed)}
                    self.assertEqual(verdicts[hid], "FAIL")

    def test_handoff_hashes_bind_native_corpus_and_repair(self):
        for field in ("block1", "predecessor", "repaired"):
            probe = sample()
            probe["handoff_runs"] = handoff_sample()
            row = probe["handoff_runs"]["bor.power-loss-model"]
            target = row["recovery"] if field == "repaired" else row
            target[field]["hash"] = "00" * 32
            verdicts = {r["id"]: r["verdict"] for r in faults_poly.handoff_verdicts(probe)}
            self.assertEqual(verdicts["bor.power-loss-model"], "FAIL", field)

        probe = sample()
        probe["handoff_runs"] = handoff_sample()
        row = probe["handoff_runs"]["bor.power-loss-model"]
        row["predecessor"]["hash"] = row["recovery"]["repaired"]["hash"] = "00" * 32
        verdicts = {r["id"]: r["verdict"] for r in faults_poly.handoff_verdicts(probe)}
        self.assertEqual(verdicts["bor.power-loss-model"], "FAIL")

    def test_handoff_faults_and_refusals_fail_closed(self):
        for hid in handoff_sample():
            for broken in (None, {}, {"child_error": "timeout"}):
                probe = sample()
                probe["handoff_runs"] = handoff_sample()
                probe["handoff_runs"][hid] = broken
                self.assertEqual(self.verdict(probe)["result"], "FAIL")
        for hid, key, value in (("bor.pass-at-golive", "decided", [1, 2]),
                                ("bor.pass-at-golive", "golive_error", "failure"),
                                ("bor.below-h-rewind", "head_after", "different"),
                                ("bor.bootstrap-vote-pass", "vote_locked", False),
                                ("bor.golive-order", "order", ["chain-locked", "guard-acquired", "vote-release"]),
                                ("bor.golive-order", "decisions_held", {"intent": "unexpected"}),
                                ("bor.power-loss-model", "sentinel_after_reset", True)):
            probe = sample()
            probe["handoff_runs"] = handoff_sample()
            probe["handoff_runs"][hid][key] = value
            self.assertEqual(self.verdict(probe)["result"], "FAIL")

    def test_stale_probe_help_fails_doc_handoff(self):
        probe = sample()
        probe["handoff_runs"] = handoff_sample()
        with mock.patch.object(faults_poly, "HELP_PROBE") as help_probe:
            help_probe.read_text.return_value = "stale text"
            self.assertEqual(self.verdict(probe)["handoffs"]["bor.probe-flag-help"], "FAIL")

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

    def test_copy_differs_from_bsc_only_by_the_directories(self):
        bsc = (HERE.parent / "bsc" / "faults_test.go").read_text()
        ours = go_block(self.ours, "func copyFS(")
        for old, new in (("src *vfs.MemFS, from, to string", "src *vfs.MemFS"), ("freshFS(to)", "freshFS()"),
                         ("List(from)", "List(memDir)"),
                         ("\t\tin, err := src.Open(filepath.Join(from, name))\n",
                          "\t\tpath := filepath.Join(memDir, name)\n\t\tin, err := src.Open(path)\n"),
                         ("dst.Create(filepath.Join(to, name))", "dst.Create(path)"),
                         ("syncDirs(dst, to)", "syncDirs(dst)")):
            self.assertEqual(ours.count(old), 1, old)
            ours = ours.replace(old, new)
        self.assertEqual(ours, go_block(bsc, "func copyFS("))

    def test_directory_sync_walks_up_to_the_root(self):
        # BSC syncs its fixed chaindata path and parents; each Bor node has its
        # own path, so the Bor helper syncs dir and every parent up to "/".
        self.assertEqual(go_block(self.ours, "func syncDirs("), """func syncDirs(mem *vfs.MemFS, dir string) {
\tfor current := dir; ; current = filepath.Dir(current) {
\t\tf, err := mem.OpenDir(current)
\t\tcheck(err)
\t\tcheck(f.Sync())
\t\tcheck(f.Close())
\t\tif current == "/" {
\t\t\treturn
\t\t}
\t}
}""")

    def test_fail_closed_probe_steps(self):
        # A failed child stays in the report as a case with child_error, so
        # the judge fails it instead of finding a case missing.
        self.assertIn('cases = append(cases, map[string]any{"point": point, "variant": variant,\n'
                      '\t\t\t\t\t"child_error": fmt.Sprintf("%v: %s", err, tail)})', go_block(self.ours, "func run("))
        start = go_block(self.ours, "func start(")
        self.assertIn('if n.fs == nil {\n\t\treturn n, errors.New("the node did not open its store through the fault '
                      'store")', start)
        self.assertIn("""\tif r.run.Bootstrapping() {
\t\tafter["mode"] = string(lifecycle.Bootstrap)
\t} else {
\t\tafter["mode"] = r.run.Lifecycle.Report().Phase
\t}""", go_block(self.ours, "func runCase("))

    def test_restart_resets_the_process_global_hooks(self):
        start = go_block(self.ours, "func start(")
        self.assertIn("rawdb.ExtractorRequire, rawdb.ExtractorFinality, whitelist.ExtractorEnter = nil, nil, nil",
                      start)
        self.assertLess(start.index("whitelist.ExtractorEnter = nil"), start.index("newStack(dataDir)"))

    def test_each_case_runs_in_its_own_process(self):
        self.assertIn('cmd.Args[1] = "case"', go_block(self.ours, "func run("))

    def test_contention_hook_is_test_only_and_reports_the_actual_mutex(self):
        patches = json.loads((faults_poly.NATIVE / "patches.json").read_text())["patches"]
        patch = next(p for p in patches if p["name"] == "guard_contention_probe")
        self.assertEqual(patch["file"], "extractor/admission/admission.go")
        shared = (faults_poly.ROOT / "adapters/bor/admission/admission.go").read_text()
        self.assertEqual(shared.count(patch["original"]), 1)
        self.assertNotIn("ExtractorFaultGuardAcquire", shared)
        self.assertIn('if !g.lock.TryLock() {\n\t\t\thook(g, "contended")', patch["replacement"])
        self.assertIn('\t\t\tg.lock.Lock()\n\t\t}\n\t\thook(g, "acquired")', patch["replacement"])
        self.assertTrue(patch["replacement"].endswith('\t} else {\n\t\tg.lock.Lock()\n\t}\n'))

    def test_child_deadlines_and_error_last_contract(self):
        run = go_block(self.ours, "func run(")
        self.assertEqual(run.count("context.WithTimeout(context.Background(), 30*time.Second)"), 2)
        self.assertEqual(run.count("exec.CommandContext(ctx, self,"), 2)
        self.assertEqual(run.count("cancel()"), 2)
        self.assertIn("func (n *instance) insert(blocks []*types.Block) (int, []map[string]any, error)", self.ours)


class CommandTests(unittest.TestCase):
    def test_run_refuses_corpus_from_another_native_source_before_creating_output(self):
        tool = {"kind": "bor-faults-tool", "native_source_commit": "accepted"}
        corpus = {"kind": "poly-corpus-run", "result": "PASS",
                  "preparation": {"path": "/fixture/preparation.json", "sha256": "0" * 64}}
        preparation = {"kind": "poly-corpus-preparation", "result": "PASS",
                       "bor": {"source_commit": "other"}}
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "must-not-exist"
            with mock.patch.object(faults_poly, "pinned_json", side_effect=[({}, tool), ({}, corpus),
                                                                         ({}, preparation)]):
                with self.assertRaisesRegex(ValueError, "another Bor source"):
                    faults_poly.run(Path("/fixture/tool.json"), "0" * 64, Path("/fixture/run.json"),
                                    "0" * 64, output)
            self.assertFalse(output.exists())

    def test_judge_command_exit_code(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "report.json"
            path.write_text(json.dumps(sample()))
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(faults_poly.main(["judge", str(path)]), 1)


if __name__ == "__main__":
    unittest.main()
