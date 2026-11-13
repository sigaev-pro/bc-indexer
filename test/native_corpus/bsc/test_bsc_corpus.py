"""Portable controls of the BSC corpus driver (bsc_corpus.py).

No native client runs here. The files under `sample/` are the probe outputs
of a development run of the accepted patched BSC build (not a measured
record); the measured records are in the evidence. The controls check the
fixture derivation, the observation built from the probe outputs, and the
decision bridge checks, each against a single changed probe field.
"""

import copy
import json
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

import bsc_corpus  # noqa: E402
from bsc_corpus import Rejected, corpus_compare  # noqa: E402

SAMPLE = HERE / "sample"
SOURCE = {"test": "portable control"}
SENDER = "7e5f4552091a69125d5dfcb7b8c2659029395bdf"
PARENT = "3000000000000000000000000000000000000001"


def sample():
    return (json.loads((SAMPLE / "import.json").read_text()), json.loads((SAMPLE / "reimport.json").read_text()))


def expected():
    return json.loads(bsc_corpus.EXPECTED.read_text())


class FixtureTests(unittest.TestCase):
    def test_genesis_adds_only_the_corpus_contracts(self):
        fixture = json.loads(bsc_corpus.FIXTURE_GENESIS.read_text())
        derived = json.loads(bsc_corpus.corpus_genesis(bsc_corpus.FIXTURE_GENESIS.read_bytes(), expected()))
        added = set(derived["alloc"]) - set(fixture["alloc"])
        self.assertEqual(added, {"0x" + PARENT, "0x3000000000000000000000000000000000000002"})
        self.assertEqual(derived["config"], fixture["config"])
        for address in fixture["alloc"]:
            self.assertEqual(derived["alloc"][address], fixture["alloc"][address])
        rows = {row["address"]: row for row in expected()["genesis_additions"]}
        self.assertEqual(derived["alloc"]["0x" + PARENT]["code"], "0x" + rows[PARENT]["code"])

    def test_sender_must_match_the_fixture(self):
        changed = expected()
        changed["genesis_additions"][0]["balance"] = "1"
        with self.assertRaises(Rejected):
            bsc_corpus.corpus_genesis(bsc_corpus.FIXTURE_GENESIS.read_bytes(), changed)
        changed = expected()
        changed["genesis_additions"][0]["code"] = "00"
        with self.assertRaises(Rejected):
            bsc_corpus.corpus_genesis(bsc_corpus.FIXTURE_GENESIS.read_bytes(), changed)

    def test_other_chain_is_refused(self):
        changed = expected()
        changed["chain_id"] = 56
        with self.assertRaises(Rejected):
            bsc_corpus.corpus_genesis(bsc_corpus.FIXTURE_GENESIS.read_bytes(), changed)

    def test_quantity(self):
        self.assertEqual(bsc_corpus.quantity("0x10"), 16)
        self.assertEqual(bsc_corpus.quantity("0X10"), 16)
        self.assertEqual(bsc_corpus.quantity("10"), 10)

    def test_accounts_are_addresses_only(self):
        accounts = bsc_corpus.accounts_of(expected())
        self.assertIn(SENDER, accounts)
        self.assertIn("fffffffffffffffffffffffffffffffffffffffe", accounts)
        self.assertEqual(accounts, sorted(set(accounts)))
        self.assertTrue(all(len(a) == 40 for a in accounts))
        # An account listed only at a block end is read too.
        changed = expected()
        changed["blocks"][1]["state_after_block"]["ab" * 20] = {}
        self.assertIn("ab" * 20, bsc_corpus.accounts_of(changed))


class ObservationTests(unittest.TestCase):
    def test_sample_passes_every_case(self):
        imported, reimported = sample()
        observation = bsc_corpus.observation_of(imported, reimported, SOURCE)
        report = corpus_compare.compare(expected(), observation)
        self.assertEqual(report["result"], "PASS", report["differences"][:3])
        self.assertEqual({row["case"]: row["verdict"] for row in report["cases"]},
                         {"transfer": "PASS", "nested-revert": "PASS", "fee": "PASS", "system-action": "PASS",
                          "empty-block": "PASS"})
        self.assertEqual(bsc_corpus.bridge_checks(imported, reimported), [])

    def test_decisions_come_from_the_records(self):
        imported, reimported = sample()
        observation = bsc_corpus.observation_of(imported, reimported, SOURCE)
        self.assertEqual(observation["decisions"], [{"block": 1, "kind": "IMPORT"}, {"block": 2, "kind": "IMPORT"}])
        self.assertEqual(observation["reimport"], {"new_observations": 0, "new_decisions": 0})
        # An intent without NATIVE_DECIDED is not a decision.
        changed = copy.deepcopy(imported)
        del changed["bridge"]["decision_records"]["0000000000000001/decided"]
        decisions = bsc_corpus.observation_of(changed, reimported, SOURCE)["decisions"]
        self.assertEqual(decisions, [{"block": 1, "kind": "IMPORT"}])
        # A member block that is not a corpus block maps to -1.
        changed = copy.deepcopy(imported)
        changed["bridge"]["decision_records"]["0000000000000000/intent"]["members"][0]["block"] = "00" * 32
        decisions = bsc_corpus.observation_of(changed, reimported, SOURCE)["decisions"]
        self.assertEqual(decisions[0]["block"], -1)

    def test_reimport_counts_are_counter_differences(self):
        imported, reimported = sample()
        reimported["after"]["counters"] = [3, 4]
        observation = bsc_corpus.observation_of(imported, reimported, SOURCE)
        self.assertEqual(observation["reimport"], {"new_observations": 1, "new_decisions": 2})
        report = corpus_compare.compare(expected(), observation)
        self.assertEqual(report["result"], "FAIL")

    def test_probe_errors_are_refused(self):
        for key, where in (("error", 0), ("recover_error", 1), ("reimport_error", 1)):
            with self.subTest(key=key):
                rows = list(sample())
                rows[where][key] = "boom"
                with self.assertRaises(Rejected):
                    bsc_corpus.observation_of(*rows, SOURCE)

    def test_frames(self):
        trace = {"type": "CALL", "from": "0xAB" + "00" * 19, "to": "0x" + "11" * 20, "value": "0x10",
                 "input": "0x", "output": "0xEE", "error": "execution reverted",
                 "calls": [{"type": "CALL", "from": "0x" + "11" * 20, "to": "0x" + "22" * 20}]}
        frame = bsc_corpus.frame_of(trace)
        self.assertEqual(frame["from"], "ab" + "00" * 19)
        self.assertEqual((frame["value"], frame["input"], frame["output"], frame["error"]),
                         ("16", "", "ee", "execution reverted"))
        self.assertEqual(frame["calls"][0]["value"], "0")
        self.assertEqual(frame["calls"][0]["output"], "")
        self.assertIsNone(frame["calls"][0]["error"])
        with self.assertRaises(Rejected):
            bsc_corpus.hex_digits("0xzz")
        with self.assertRaises(Rejected):
            bsc_corpus.hex_digits(5)

    def test_transaction_keeps_the_fee_fields(self):
        imported, reimported = sample()
        row = copy.deepcopy(reimported["blocks"][0]["transactions"][0])
        self.assertEqual(bsc_corpus.transaction_of(row)["gas_price"], row["gas_price"])
        del row["gas_price"]
        row["max_fee_per_gas"], row["max_priority_fee_per_gas"] = "2", "1"
        tx = bsc_corpus.transaction_of(row)
        self.assertNotIn("gas_price", tx)
        self.assertEqual((tx["max_fee_per_gas"], tx["max_priority_fee_per_gas"]), ("2", "1"))

    def test_a_wrong_native_value_fails_its_case(self):
        imported, reimported = sample()
        reimported["blocks"][0]["transactions"][1]["receipt"]["gas_used"] += 1
        report = corpus_compare.compare(expected(), bsc_corpus.observation_of(imported, reimported, SOURCE))
        verdicts = {row["case"]: row["verdict"] for row in report["cases"]}
        self.assertEqual(verdicts["nested-revert"], "FAIL")
        self.assertEqual(verdicts["transfer"], "PASS")
        imported, reimported = sample()
        reimported["blocks"][0]["system_transactions"][7]["value"] = "1"
        report = corpus_compare.compare(expected(), bsc_corpus.observation_of(imported, reimported, SOURCE))
        verdicts = {row["case"]: row["verdict"] for row in report["cases"]}
        self.assertEqual((verdicts["fee"], verdicts["system-action"]), ("FAIL", "PASS"))


class BridgeTests(unittest.TestCase):
    def failures(self, mutate):
        imported, reimported = sample()
        mutate(imported, reimported)
        return bsc_corpus.bridge_checks(imported, reimported)

    def test_each_bridge_fact(self):
        decided = "0000000000000000/decided"
        intent = "0000000000000001/intent"
        cases = [
            (lambda i, r: i["bridge"].update(mode="RECOVERY_ONLY"), "did not end LIVE"),
            (lambda i, r: i["bridge"].update(head_number=1), "did not end LIVE"),
            (lambda i, r: i["bridge"].update(head_hash="00" * 32), "did not end LIVE"),
            (lambda i, r: i["bridge"].update(counters=[3, 2]), "not one per block"),
            (lambda i, r: i["bridge"].update(open_observations=[{"key": "x"}]), "stayed open"),
            (lambda i, r: i["bridge"]["decision_records"][intent].update(kind="SIDE"), "not an IMPORT"),
            (lambda i, r: i["bridge"]["decision_records"][intent]["members"].append(
                dict(i["bridge"]["decision_records"][intent]["members"][0])), "not an IMPORT with one member"),
            (lambda i, r: i["bridge"]["decision_records"][intent]["members"][0].update(
                block=i["blocks"][0]["hash"]), "not the corpus blocks"),
            (lambda i, r: i["bridge"]["decision_records"][intent]["members"][0].update(
                observation=i["bridge"]["decision_records"]["0000000000000000/intent"]["members"][0]["observation"]),
             "one observation per block"),
            (lambda i, r: i["bridge"]["decision_records"][decided].update(intent_digest="00"), "does not bind"),
            (lambda i, r: i["bridge"]["obligations"].pop(), "not the IMPORT decisions"),
            (lambda i, r: i["bridge"]["obligations"][0].update(intent_digest="00"), "does not bind its intent"),
            (lambda i, r: i["bridge"]["obligations"][1].update(decided_digest="00"),
             "does not bind its NATIVE_DECIDED"),
            (lambda i, r: r["before"]["admission_records"].pop("lowwater"), "changed the admission records"),
            (lambda i, r: r["after"]["admission_records"].pop("lowwater"), "changed the admission records"),
            (lambda i, r: (r["before"].update(counters=[3, 2]), r["after"].update(counters=[3, 2])),
             "the restart recovery opened"),
            (lambda i, r: r["before"].update(open_observations=[{"key": "x"}]), "open after the restart"),
            (lambda i, r: r["after"].update(open_observations=[{"key": "x"}]), "open after the restart"),
            (lambda i, r: r["before"].update(mode="RECOVERY_ONLY"), "did not go LIVE"),
            (lambda i, r: r["before"]["decision_records"].pop(decided), "the restart changed"),
            (lambda i, r: r["after"]["decision_records"].update(x={}), "the re-import changed the decision"),
            (lambda i, r: r["after"]["obligations"].pop(), "changed the delivery obligations"),
            (lambda i, r: r["before"]["obligations"].pop(), "changed the delivery obligations"),
            (lambda i, r: r["after"].update(counters=[3, 2]), "opened an observation or a decision"),
            (lambda i, r: r.update(recover_error="boom"), "recovery or the re-import failed"),
            (lambda i, r: r.update(reimport_error="boom"), "recovery or the re-import failed"),
            (lambda i, r: r["after"].update(head_hash="00" * 32), "moved the head"),
            (lambda i, r: r["after"].update(mode="RECOVERY_ONLY"), "moved the head or left LIVE"),
        ]
        for mutate, text in cases:
            with self.subTest(text=text):
                failures = self.failures(mutate)
                self.assertTrue(any(text in failure for failure in failures), failures)

    def test_verdict(self):
        imported, reimported = sample()
        report = corpus_compare.compare(expected(), bsc_corpus.observation_of(imported, reimported, SOURCE))
        self.assertEqual(bsc_corpus.verdict(report, [])[0], "PASS")
        result, cases = bsc_corpus.verdict(report, ["bridge"])
        self.assertEqual(result, "FAIL")
        self.assertEqual({row["verdict"] for row in cases}, {"FAIL"})
        self.assertEqual(bsc_corpus.verdict({"result": "PASS", "cases": []}, [])[0], "FAIL")
        # A difference outside every case fails the run even when each case passes.
        self.assertEqual(bsc_corpus.verdict({"result": "FAIL", "cases": [{"case": "transfer", "verdict": "PASS"}]},
                                            [])[0], "FAIL")


def retained_run(directory):
    """A retained run layout like `run` writes: run.json, bsc/work and bsc/observation.json."""
    run = Path(directory).resolve() / "run"
    work = run / "bsc" / "work"
    work.mkdir(parents=True)
    for name in ("import.json", "reimport.json"):
        (work / name).write_bytes((SAMPLE / name).read_bytes())
    (run / "expected-copy.json").write_bytes(bsc_corpus.EXPECTED.read_bytes())
    (run / "bsc" / "observation.json").write_text(json.dumps({"source": SOURCE}))
    record = {"kind": "bsc-corpus-run", "expected": bsc_corpus.reference(run / "expected-copy.json"),
              "outputs": {name: bsc_corpus.reference(work / name) for name in ("import.json", "reimport.json")}}
    (run / "run.json").write_text(json.dumps(record))
    return run


class ObserveRetainedTests(unittest.TestCase):
    def test_rebuild_names_its_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            run = retained_run(directory)
            summary = bsc_corpus.observe_retained(run, run.parent / "new")
            self.assertEqual((summary["result"], summary["differences"], summary["bridge_failures"]), ("PASS", 0, []))
            written = json.loads((run.parent / "new" / "observe.json").read_text())
            self.assertEqual(written["expected"], json.loads((run / "run.json").read_text())["expected"])
            self.assertEqual(written["kind"], "bsc-corpus-observe")

    def test_changed_inputs_are_refused(self):
        for mutate in (lambda run: (run / "bsc/work/import.json").write_text("{}"),
                       lambda run: (run / "bsc/work/reimport.json").write_text("{}"),
                       lambda run: (run / "expected-copy.json").write_text("{}")):
            with self.subTest():
                with tempfile.TemporaryDirectory() as directory:
                    run = retained_run(directory)
                    mutate(run)
                    with self.assertRaises(Rejected):
                        bsc_corpus.observe_retained(run, run.parent / "new")
                    self.assertFalse((run.parent / "new").exists())

    def test_the_recorded_expected_copy_is_used(self):
        with tempfile.TemporaryDirectory() as directory:
            run = retained_run(directory)
            changed = expected()
            changed["blocks"][0]["transactions"][0]["receipt"]["gas_used"] = 1
            (run / "expected-copy.json").write_text(json.dumps(changed))
            record = json.loads((run / "run.json").read_text())
            record["expected"] = bsc_corpus.reference(run / "expected-copy.json")
            (run / "run.json").write_text(json.dumps(record))
            self.assertEqual(bsc_corpus.observe_retained(run, run.parent / "new")["result"], "FAIL")

    def test_existing_output_and_other_records_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            run = retained_run(directory)
            (run.parent / "new").mkdir()
            with self.assertRaises(Rejected):
                bsc_corpus.observe_retained(run, run.parent / "new")
            record = json.loads((run / "run.json").read_text())
            record["kind"] = "poly-corpus-run"
            (run / "run.json").write_text(json.dumps(record))
            with self.assertRaises(Rejected):
                bsc_corpus.observe_retained(run, run.parent / "other")


class SnapshotTests(unittest.TestCase):
    def test_corpus_files_are_kept(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory).resolve()
            origins = bsc_corpus.corpus_snapshot(out)
            self.assertEqual(len(origins), len(bsc_corpus.CORPUS_FILES))
            for name in bsc_corpus.CORPUS_FILES:
                self.assertEqual((out / "corpus-code" / name).read_bytes(), (bsc_corpus.CORPUS / name).read_bytes())


class FileTests(unittest.TestCase):
    def test_observe_writes_new_files_only(self):
        with tempfile.TemporaryDirectory() as directory:
            work, out = Path(directory).resolve() / "work", Path(directory).resolve() / "out"
            work.mkdir()
            out.mkdir()
            for name in ("import.json", "reimport.json"):
                (work / name).write_bytes((SAMPLE / name).read_bytes())
            comparison, failures = bsc_corpus.observe(work, expected(), SOURCE, out)
            self.assertEqual((comparison["result"], failures), ("PASS", []))
            self.assertEqual(json.loads((out / "comparison.json").read_text())["result"], "PASS")
            with self.assertRaises(FileExistsError):
                bsc_corpus.observe(work, expected(), SOURCE, out)

    def test_format_error_gives_a_failing_comparison(self):
        with tempfile.TemporaryDirectory() as directory:
            work, out = Path(directory).resolve() / "work", Path(directory).resolve() / "out"
            work.mkdir()
            out.mkdir()
            imported, reimported = sample()
            reimported["blocks"][0]["state_root"] = "0x00"
            (work / "import.json").write_text(json.dumps(imported))
            (work / "reimport.json").write_text(json.dumps(reimported))
            comparison, _ = bsc_corpus.observe(work, expected(), SOURCE, out)
            self.assertEqual(comparison["result"], "FAIL")
            self.assertIn("format_error", comparison)


if __name__ == "__main__":
    unittest.main()
