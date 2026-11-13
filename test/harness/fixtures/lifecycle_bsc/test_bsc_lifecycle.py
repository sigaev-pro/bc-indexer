"""Portable controls of the BSC lifecycle driver (bsc_lifecycle.py).

No native client runs here. The files under `sample/` are the probe outputs
of a development run of the patched BSC build (not a measured record); the
measured records are in the evidence. Each control changes one probe field
and checks that the judge refuses it, or checks one independent formula.
"""

import copy
import json
import shutil
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

import bsc_lifecycle  # noqa: E402

SAMPLE = HERE / "sample"
GENESIS = "16d03930cdabc5b474bb63eb69fb6e8a803f4a606310a42e743dc5bd45441e77"


def sample():
    report = json.loads((SAMPLE / "report.json").read_text())
    trace = [json.loads(line) for line in (SAMPLE / "trace.jsonl").read_text().splitlines()]
    return report, trace, (SAMPLE / "blocks.rlp").read_bytes()


def callers():
    return json.loads((bsc_lifecycle.SHARED / "callers.json").read_text())


def judge(report, trace, raw, genesis=GENESIS):
    return bsc_lifecycle.judge(report, trace, raw, genesis, callers())


def step(report, path, name):
    for row in report[path]["steps"]:
        if row["name"] == name:
            return row
    raise KeyError(name)


class SampleTests(unittest.TestCase):
    def test_sample_passes_with_the_qa_matrix(self):
        summary = judge(*sample())
        self.assertEqual(summary["result"], "PASS", summary["failures"][:3])
        matrix = summary["qa_matrix"]
        self.assertEqual(matrix["AC-CAP-01-01"]["bsc-private-local"], "FAIL")
        self.assertEqual(matrix["AC-CAP-01-01"]["gaps"], ["bsc.known-block", "bsc.side-chain"])
        rows = matrix["AC-CAP-01-01"]["rows"]
        self.assertEqual({k for k, v in rows.items() if v["verdict"] == "DEFERRED"},
                         {"bsc.known-block", "bsc.side-chain"})
        self.assertEqual(rows["bsc.notify-finalized"]["verdict"], "NOT_APPLICABLE")
        self.assertEqual({v["verdict"] for k, v in rows.items()
                          if k not in ("bsc.known-block", "bsc.side-chain", "bsc.notify-finalized")}, {"PASS"})
        for case in ("AC-LIF-05-02", "AC-LIF-05-03"):
            self.assertEqual(matrix[case]["bsc-private-local"], "NOT_APPLICABLE")
        for case in ("AC-LIF-06-04", "AC-LIF-06-05"):
            self.assertEqual(matrix[case]["bsc-private-local"], "PASS")
        for case in matrix.values():
            self.assertEqual((case["bsc-mainnet"], case["bsc-testnet"]), ("NOT_RUN", "NOT_RUN"))

    def test_every_row_of_the_map_has_a_verdict(self):
        rows = judge(*sample())["qa_matrix"]["AC-CAP-01-01"]["rows"]
        self.assertEqual(sorted(rows), sorted(c["id"] for c in callers()["callers"] if c["client"] == "bsc"))


class FormulaTests(unittest.TestCase):
    def test_canonical_evidence_matches_the_go_vector(self):
        a, b = {"number": 5, "hash": "11" * 32}, {"number": 4, "hash": "22" * 32}
        self.assertEqual(bsc_lifecycle.canonical_evidence("bsc.set-head", a, b),
                         "ea27763178e260b33123a27f8f3fdf8464a87ce28fe142cf925515dc8919ef9e")

    def test_attestation_digest_matches_the_shared_rule(self):
        g = {"number": 0, "hash": GENESIS}
        _, trace, _ = sample()
        events = [e for e in trace if e.get("event") == "attestation"]
        self.assertTrue(events)
        self.assertEqual({e["digest"] for e in events},
                         {bsc_lifecycle.attestation_digest("GENESIS_FALLBACK", g, g)})

    def test_blocks_decode(self):
        _, _, raw = sample()
        blocks = bsc_lifecycle.decode_blocks(raw)
        self.assertEqual([b["number"] for b in blocks], [1, 2, 3, 4, 5, 6, 7, 3, 4])
        self.assertEqual(blocks[0]["parent"], GENESIS)

    def test_canonical_of(self):
        self.assertEqual(bsc_lifecycle.canonical_of("A3"), ["G", "A1", "A2", "A3"])
        self.assertEqual(bsc_lifecycle.canonical_of("S4"), ["G", "A1", "A2", "S3", "S4"])


class RefusalTests(unittest.TestCase):
    def refused(self, change, genesis=GENESIS):
        report, trace, raw = sample()
        report, trace = copy.deepcopy(report), copy.deepcopy(trace)
        raw = change(report, trace, raw) or raw
        summary = judge(report, trace, raw, genesis)
        self.assertEqual(summary["result"], "FAIL")
        self.assertNotEqual(summary["qa_matrix"]["AC-LIF-06-04"]["bsc-private-local"], "PASS")
        return summary

    def test_known_block_that_moves_the_head(self):
        def change(report, trace, raw):
            row = step(report, "harness", "InsertChain S3 (known block)")
            row["class"], row["head"] = "", "S3"
        self.refused(change)

    def test_known_block_with_another_error(self):
        def change(report, trace, raw):
            step(report, "native_caller", "InsertChain S3 (known block)")["class"] = "other"
        self.refused(change)

    def test_side_branch_admitted(self):
        def change(report, trace, raw):
            step(report, "native_caller", "import S4 (side branch, pruned parent S3)")["class"] = ""
        self.refused(change)

    def test_pruned_parent_not_pruned(self):
        def change(report, trace, raw):
            step(report, "harness", "import S4 (side branch, pruned parent S3)")["extra"]["s3_has_state"] = True
        self.refused(change)

    def test_paths_differ(self):
        def change(report, trace, raw):
            report["ac_cap_01_01"]["native"][3]["head"] = "A3"
        self.refused(change)

    def test_missing_decision(self):
        def change(report, trace, raw):
            step(report, "harness", "setHead 4")["new_decisions"] = []
        self.refused(change)

    def test_decision_without_marker(self):
        def change(report, trace, raw):
            step(report, "native_caller", "import A6 A7")["new_decisions"][1]["marker"] = False
        self.refused(change)

    def test_import_evidence_not_the_genesis_fallback(self):
        def change(report, trace, raw):
            step(report, "native_caller", "import A3 A4 A5")["new_decisions"][0]["evidence_digest"] = "00" * 32
        self.refused(change)

    def test_canonical_evidence_of_another_caller(self):
        def change(report, trace, raw):
            row = step(report, "harness", "restart 1 (shutdown, RECOVERY, live at H)")["new_decisions"][0]
            row["evidence_digest"] = bsc_lifecycle.canonical_evidence("bsc.set-head", row["old_head"], row["new_head"])
        self.refused(change)

    def test_open_observation_left(self):
        def change(report, trace, raw):
            step(report, "harness", "import A6 A7")["open_observations"] = [{"key": "k", "state": "EXEC_READY",
                                                                            "block": "A7"}]
        self.refused(change)

    def test_view_behind_the_head(self):
        def change(report, trace, raw):
            step(report, "native_caller", "setHead 4")["view"] = {"head": "A5", "finalized": "G"}
        self.refused(change)

    def test_attestation_not_genesis(self):
        def change(report, trace, raw):
            for event in trace:
                if event.get("event") == "attestation" and event.get("scope") == "harness":
                    event["justified"] = {"number": 1, "hash": "aa" * 32}
                    break
        self.refused(change)

    def test_attestation_missing(self):
        def change(report, trace, raw):
            index = next(i for i, e in enumerate(trace) if e.get("event") == "attestation" and e["number"] == 7)
            del trace[index]
        self.refused(change)

    def test_lookup_error_not_a_source_error(self):
        report, trace, raw = sample()
        report = copy.deepcopy(report)
        report["attestation_fault"]["steps"][3]["class"] = ""
        self.assertEqual(judge(report, trace, raw)["result"], "FAIL")

    def test_lookup_error_writes(self):
        report, trace, raw = sample()
        report = copy.deepcopy(report)
        report["attestation_fault"]["steps"][3]["head"] = "A3"
        self.assertEqual(judge(report, trace, raw)["result"], "FAIL")

    def test_database_guard_passes_with_a_pin(self):
        report, trace, raw = sample()
        report = copy.deepcopy(report)
        report["attestation_fault"]["steps"][4]["class"] = ""
        self.assertEqual(judge(report, trace, raw)["result"], "FAIL")

    def test_gap_hidden(self):
        def change(report, trace, raw):
            report["coverage"]["bsc.side-chain"]["passes"] = 1
        self.refused(change)

    def test_extra_gap(self):
        def change(report, trace, raw):
            report["coverage"]["bsc.shutdown"]["intents"] = 0
        self.refused(change)

    def test_refusal_outside_the_gate_missing(self):
        def change(report, trace, raw):
            report["outside_gate_refusals"]["bsc.database-commands"] = None
        self.refused(change)

    def test_refused_caller_accepted(self):
        def change(report, trace, raw):
            step(report, "native_caller", "StartMining")["class"] = ""
        self.refused(change)

    def test_profile_accepts_snap_sync(self):
        def change(report, trace, raw):
            report["profile"]["snap_sync"] = None
        self.refused(change)

    def test_decided_events_miss_the_verified_block(self):
        def change(report, trace, raw):
            report["events"]["decided"]["A5"] = [e for e in report["events"]["decided"]["A5"]
                                                 if not e.startswith("verified")]
        self.refused(change)

    def test_crash_intent_completed_instead_of_rejected(self):
        def change(report, trace, raw):
            report["crashes"][0]["settle"][0]["outcome"] = "completed"
        self.refused(change)

    def test_deadlock(self):
        def change(report, trace, raw):
            report["deadlock"]["finished"] = False
        self.refused(change)

    def test_exit_error(self):
        def change(report, trace, raw):
            report["native_caller"]["exit_errors"] = ["pass used out of order"]
        self.refused(change)

    def test_block_hash(self):
        def change(report, trace, raw):
            report["blocks"]["A4"]["hash"] = "00" * 32
        self.refused(change)

    def test_tampered_block(self):
        def change(report, trace, raw):
            data = bytearray(raw)
            data[12] ^= 1  # inside the parent hash of A1
            return bytes(data)
        self.refused(change)

    def test_other_genesis(self):
        self.refused(lambda report, trace, raw: None, genesis="00" * 32)

    def test_extra_block(self):
        def change(report, trace, raw):
            last = bsc_lifecycle.probe14.split_rlp_list_items(raw)[-1]
            return raw + last
        self.refused(change)

    def test_report_genesis(self):
        def change(report, trace, raw):
            report["genesis"] = "00" * 32
        self.refused(change)

    def test_parent_with_a_matching_hash(self):
        def change(report, trace, raw):
            data = bytearray(raw)
            data[12] ^= 1  # inside the parent hash of A1
            report["blocks"]["A1"]["hash"] = bsc_lifecycle.decode_blocks(bytes(data))[0]["hash"]
            return bytes(data)
        self.refused(change)

    def test_step_head_only(self):
        def change(report, trace, raw):
            step(report, "harness", "setHead 4")["head"] = "A5"
        self.refused(change)

    def test_decision_members(self):
        def change(report, trace, raw):
            step(report, "native_caller", "import A6 A7")["new_decisions"][0]["members"] = ["S4"]
        self.refused(change)

    def test_native_canonical_evidence(self):
        def change(report, trace, raw):
            row = step(report, "native_caller", "restart 1 (shutdown, RECOVERY, live at H)")["new_decisions"][0]
            row["evidence_digest"] = bsc_lifecycle.canonical_evidence("bsc.set-head", row["old_head"], row["new_head"])
        self.refused(change)

    def test_reported_gaps_differ(self):
        def change(report, trace, raw):
            report["gaps"] = []
        self.refused(change)

    def test_gaps_other_than_d104(self):
        def change(report, trace, raw):
            report["coverage"]["bsc.side-chain"]["passes"] = 1
            report["gaps"] = ["bsc.known-block"]
        self.refused(change)

    def test_reference_events_differ(self):
        def change(report, trace, raw):
            report["events"]["reference"]["A5"] = report["events"]["reference"]["A5"] + ["logs 9"]
        self.refused(change)

    def test_no_verified_event_anywhere(self):
        def change(report, trace, raw):
            for side in ("reference", "decided"):
                report["events"][side]["A5"] = [e for e in report["events"][side]["A5"] if not e.startswith("verified")]
        self.refused(change)

    def test_runtime_rejection_missing(self):
        def change(report, trace, raw):
            trace[:] = [e for e in trace if e.get("event") != "lifecycle-rejected"]
        self.refused(change)

    def test_guard_not_refused_by_the_pin(self):
        def change(report, trace, raw):
            report["attestation_fault"]["database_guard"]["pinned"] = "open extractor obligations forbid"
        self.refused(change)

    def test_guard_passes_a_decided_store(self):
        def change(report, trace, raw):
            report["attestation_fault"]["database_guard"]["decided"] = None
        self.refused(change)

    def test_guard_refuses_a_fresh_store(self):
        def change(report, trace, raw):
            report["attestation_fault"]["database_guard"]["fresh"] = "live pins forbid this native deletion"
        self.refused(change)

    def test_malformed_report_fails_without_a_crash(self):
        def change(report, trace, raw):
            del report["deadlock"]
            del report["coverage"]["bsc.known-block"]
            row = step(report, "harness", "setHead 4")["new_decisions"][0]
            row["old"], row["new"] = "A6", "A1"
        summary = self.refused(change)
        self.assertTrue(any("well-formed" in f["check"] for f in summary["failures"]))

    def test_map_differs(self):
        def change(report, trace, raw):
            report["routes"]["bsc.sealing"] = "READ_ONLY"
        self.refused(change)


def retained_run(directory):
    """A valid retained run of the sample under directory; returns its path."""
    run = Path(directory) / "run"
    work = run / "bsc" / "work"
    work.mkdir(parents=True)
    for name in ("report.json", "trace.jsonl", "blocks.rlp"):
        shutil.copyfile(SAMPLE / name, work / name)
    tool = Path(directory) / "tool.json"
    tool.write_text(json.dumps({"callers": bsc_lifecycle.reference(bsc_lifecycle.SHARED / "callers.json")}))
    outputs = {name: bsc_lifecycle.reference(work / name) for name in ("report.json", "trace.jsonl", "blocks.rlp")}
    (run / "run.json").write_text(json.dumps({"kind": "bsc-lifecycle-run", "outputs": outputs, "genesis_hash": GENESIS,
                                              "tool": bsc_lifecycle.reference(tool)}))
    return run


class RetainedTests(unittest.TestCase):
    def test_valid_run_judged(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory).resolve()
            result = bsc_lifecycle.judge_retained(retained_run(directory), directory / "new")
            self.assertEqual(result["result"], "PASS")
            self.assertTrue((directory / "new" / "judge.json").exists())

    def test_relative_output_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory).resolve()
            with self.assertRaises(bsc_lifecycle.Rejected):
                bsc_lifecycle.judge_retained(retained_run(directory), Path("new"))
            self.assertFalse(Path("new").exists())

    def test_existing_output_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory).resolve()
            run = retained_run(directory)
            (directory / "new").mkdir()
            with self.assertRaises(bsc_lifecycle.Rejected):
                bsc_lifecycle.judge_retained(run, directory / "new")

    def test_output_not_the_recorded_one(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory).resolve()
            run = retained_run(directory)
            (run / "bsc" / "work" / "report.json").write_text("{}")
            with self.assertRaises(bsc_lifecycle.Rejected):
                bsc_lifecycle.judge_retained(run, directory / "new")
            self.assertFalse((directory / "new").exists())


if __name__ == "__main__":
    unittest.main()
