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

    def test_handoff_verdicts_cannot_be_supplied_without_native_facts(self):
        probe = sample()
        probe["handoffs"] = [{"id": hid, "verdict": "PASS", "evidence": "x"} for hid in
                             ("geth.pass-at-golive", "geth.sethead-interrupted", "geth.repaired-judge",
                              "geth.mid-reorg-long-branch")]
        self.assertEqual(self.verdict(probe)["result"], "INCOMPLETE")

    def test_malformed_handoff_facts_fail_closed(self):
        for name in ("geth.pass-at-golive", "geth.sethead-interrupted", "geth.mid-reorg-long-branch",
                     "geth.repaired-judge"):
            probe = sample()
            probe["handoff_runs"] = {name: {}}
            self.assertEqual(self.verdict(probe)["handoffs"][name], "FAIL")

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
        rows = lambda c: c["observation"]["native"]["reexecution"]  # noqa: E731
        self.failing(lambda c: rows(c)[0].update(root_equal=False), "block 1 root differs")
        self.failing(lambda c: rows(c)[0].update(root_equal="yes"), "block 1 root differs")
        self.failing(lambda c: rows(c)[0]["receipts"][0].update(receipt_equal=False), "block 1 receipt 0 differs")
        self.failing(lambda c: rows(c)[0].update(cumulative_gas_used=1), "block 1 gas differs from the header")
        self.failing(lambda c: rows(c)[0]["receipts"].pop(), "does not check one receipt per transaction")
        self.failing(lambda c: c["observation"]["native"].update(reexecution=[]), "one row per block")
        self.failing(lambda c: c["observation"]["native"].pop("reexecution"), "one row per block")

    def test_undecided_intents_must_be_an_empty_list(self):
        self.failing(lambda c: c["observation"]["native"].pop("undecided_intents"), "undecided intents None")

    def test_decision_of_another_block_fails_the_corpus(self):
        def change(case):
            case["observation"]["decisions"].append({"block": 0, "kind": "IMPORT"})
        self.failing(change, "native result does not match the smoke corpus")

    def test_storage_model_of_the_probe(self):
        probe = sample()
        probe["storage_model"] = "pebble-v1-strict-mem"
        verdict = self.verdict(probe)
        self.assertEqual(verdict["result"], "FAIL")
        self.assertIn("probe: storage_model is not pebble-v2-crashable-mem", verdict["native_errors"])

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

    def test_malformed_case_fails(self):
        probe = sample()
        probe["cases"][0] = "not a case"
        verdict = self.verdict(probe)
        self.assertEqual(verdict["result"], "FAIL")
        self.assertIn("case 0: malformed", verdict["native_errors"])


class RepairedHandoffTests(unittest.TestCase):
    """The geth.repaired-judge verdict must bind the one unresolved intent."""

    def verdict_of(self, row):
        verdicts = faults_geth.handoff_verdicts({"handoff_runs": {"geth.repaired-judge": row}})
        self.assertEqual(len(verdicts), 1)
        return verdicts[0]["verdict"]

    def row(self, **changes):
        intent = {"kind": "CANONICAL", "sequence": 7,
                  "old_head": {"number": 4, "hash": "a4"}, "new_head": {"number": 6, "hash": "j6"},
                  "members": [{"block": "j5"}, {"block": "j6"}]}
        row = {"hit": "geth.repaired-judge", "step_error": None, "old_head": "a4",
               "target": {"number": 6, "hash": "j6", "root": "r6"},
               "lowered_head": {"number": 3, "hash": "a3", "root": "r3"},
               "member_blocks": ["j5", "j6"], "head_before_restart": "a3",
               "records_before": {"p/0000000000000007/intent": json.dumps(intent)},
               "restart_error": "lifecycle: native head 3 a3 is neither the old nor the new head of "
                                "intent 7, nor on its branch",
               "settled": []}
        row.update(changes)
        return row

    def test_the_saved_reorganization_passes(self):
        self.assertEqual(self.verdict_of(self.row()), "PASS")

    def refusing(self, changes):
        self.assertEqual(self.verdict_of(self.row(**changes)), "FAIL")

    def test_the_intent_must_be_the_saved_reorganization(self):
        intent = json.loads(self.row()["records_before"]["p/0000000000000007/intent"])
        intent["new_head"]["hash"] = "other"
        self.refusing({"records_before": {"p/0000000000000007/intent": json.dumps(intent)}})
        intent = json.loads(self.row()["records_before"]["p/0000000000000007/intent"])
        intent["old_head"]["hash"] = "other"
        self.refusing({"records_before": {"p/0000000000000007/intent": json.dumps(intent)}})
        intent = json.loads(self.row()["records_before"]["p/0000000000000007/intent"])
        intent["kind"] = "SIDE"
        self.refusing({"records_before": {"p/0000000000000007/intent": json.dumps(intent)}})

    def test_the_members_must_be_the_two_branch_blocks(self):
        intent = json.loads(self.row()["records_before"]["p/0000000000000007/intent"])
        intent["members"] = [{"block": "j5"}]
        self.refusing({"records_before": {"p/0000000000000007/intent": json.dumps(intent)}})
        self.refusing({"member_blocks": ["j5", "other"]})

    def test_a_second_pending_intent_refuses(self):
        records = dict(self.row()["records_before"])
        records["p/0000000000000008/intent"] = records["p/0000000000000007/intent"]
        self.refusing({"records_before": records})
        self.refusing({"records_before": {}})

    def test_the_head_must_be_lowered_below_the_fork_point(self):
        self.refusing({"lowered_head": {"number": 4, "hash": "a3", "root": "r3"}})
        self.refusing({"lowered_head": {"number": 3, "hash": "a4", "root": "r3"},
                       "head_before_restart": "a4"})
        self.refusing({"head_before_restart": "a4"})
        self.refusing({"head_before_restart": "j6"})

    def test_the_restart_must_refuse_and_name_the_intent(self):
        self.refusing({"restart_error": None})
        self.refusing({"restart_error": "of intent 7, settled differently"})
        self.refusing({"restart_error": "lifecycle: native head 3 a3 is neither the old nor the new head of "
                                        "intent 8, nor on its branch"})
        self.refusing({"restart_error": "lifecycle: native head 3 a3 stopped for another reason, intent 7"})

    def test_the_restart_must_settle_nothing(self):
        self.refusing({"settled": [{"event": "settling", "evidence": "completed_inside_reorg",
                                    "sequence": 7}]})

    def test_the_step_must_have_stopped_at_the_point(self):
        self.refusing({"hit": "geth.mid-reorg-long-branch"})
        self.refusing({"step_error": "boom"})


COPIED = ("plain", "accounts", "hexField", "frame", "logs", "receiptView", "txView", "exportBlocks", "counted")


def go_function(text, name):
    """The source of one top-level Go function, from its line to the closing brace at column 0."""
    start = text.index(f"\nfunc {name}(") + 1
    return text[start:text.index("\n}\n", start) + 2]


class CopiedCodeTests(unittest.TestCase):
    def test_export_helpers_equal_the_accepted_corpus_probe(self):
        ours = (HERE / "probe.go").read_text()
        accepted = (HERE.parents[1] / "native_corpus" / "eth" / "probe.go").read_text()
        for name in COPIED:
            self.assertEqual(go_function(ours, name), go_function(accepted, name), name)

    def test_decisions_differs_only_by_rejected_intents(self):
        ours = go_function((HERE / "probe.go").read_text(), "decisions")
        accepted = go_function((HERE.parents[1] / "native_corpus" / "eth" / "probe.go").read_text(), "decisions")
        self.assertNotEqual(ours, accepted)
        self.assertEqual(ours.replace("""\t\t\tif _, rejected := records[base+"/rejected"]; !rejected {
\t\t\t\tundecided = append(undecided, key)
\t\t\t}
""", """\t\t\tundecided = append(undecided, key)
"""), accepted)


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
