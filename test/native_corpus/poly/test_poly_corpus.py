"""Portable controls of the POLY corpus driver (poly_corpus.py).

No native client runs here. The sample under `sample/` is the probe output of
a development run of the accepted patched Bor build (not a measured record);
the measured records are in the evidence. The controls check the fixture
derivation, the observation built from the probe outputs, and the decision
bridge checks, each against a single changed probe field.
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

import poly_corpus  # noqa: E402
from poly_corpus import corpus_compare  # noqa: E402

SAMPLE = HERE / "sample"
SOURCE = {"test": "portable control"}
SIGNER = "71562b71999873db5b286df957af199ec94617f7"


def sample():
    return (json.loads((SAMPLE / "import.json").read_text()), json.loads((SAMPLE / "reimport.json").read_text()))


def expected():
    return json.loads(poly_corpus.EXPECTED.read_text())


def verdicts(imported, reimported):
    report = corpus_compare.compare(expected(), poly_corpus.observation_of(imported, reimported, SOURCE))
    return report["result"], {row["case"]: row["verdict"] for row in report["cases"]}, report["differences"]


def source_genesis():
    return json.dumps({"config": {"chainId": 15001, "bor": {"sprint": {"0": 4}}}, "timestamp": "0x5ce28211",
                       "alloc": {"0x" + SIGNER: {"balance": "0x1"}}}).encode()


class FixtureTests(unittest.TestCase):
    def test_corpus_accounts_and_delay_are_the_only_additions(self):
        genesis, config = poly_corpus.derived_inputs(source_genesis(), expected())
        document = json.loads(genesis)
        self.assertEqual(json.loads(config), document["config"])
        self.assertEqual(document["config"]["bor"], {"sprint": {"0": 4}, "stateSyncConfirmationDelay": {"0": 0}})
        self.assertEqual(document["timestamp"], "0x5ce28211")
        added = {address: row for address, row in document["alloc"].items() if address != "0x" + SIGNER}
        rows = {"0x" + row["address"]: row for row in expected()["genesis_additions"]}
        self.assertEqual(set(added), set(rows))
        for address, row in rows.items():
            self.assertEqual(int(added[address]["balance"], 16), int(row["balance"]))
            self.assertEqual(added[address].get("code", "0x"), "0x" + row["code"])
        self.assertEqual(document["alloc"]["0x" + SIGNER], {"balance": "0x1"})

    def test_output_is_canonical(self):
        genesis, config = poly_corpus.derived_inputs(source_genesis(), expected())
        self.assertEqual(poly_corpus.admission.canonical(genesis), genesis)
        self.assertEqual(poly_corpus.admission.canonical(config), config)

    def test_refuses_a_present_corpus_account(self):
        document = json.loads(source_genesis())
        document["alloc"]["0x3000000000000000000000000000000000000001"] = {"balance": "0x0"}
        with self.assertRaises(Exception):
            poly_corpus.derived_inputs(json.dumps(document).encode(), expected())

    def test_refuses_a_set_delay(self):
        document = json.loads(source_genesis())
        document["config"]["bor"]["stateSyncConfirmationDelay"] = {"0": 5}
        with self.assertRaises(Exception):
            poly_corpus.derived_inputs(json.dumps(document).encode(), expected())

    def test_refuses_another_chain(self):
        document = json.loads(source_genesis())
        document["config"]["chainId"] = 137
        with self.assertRaises(Exception):
            poly_corpus.derived_inputs(json.dumps(document).encode(), expected())

    def test_accounts_join_transaction_and_block_lists(self):
        synthetic = {"blocks": [{"state_after_block": {"aa": {}},
                                 "transactions": [{"state_before": {"bb": {}}, "state_after": {"cc": {}}}]}]}
        self.assertEqual(poly_corpus.accounts_of(synthetic), ["aa", "bb", "cc"])

    def test_accounts_are_every_listed_address(self):
        accounts = poly_corpus.accounts_of(expected())
        self.assertEqual(accounts, sorted(expected()["blocks"][0]["state_after_block"]))
        self.assertEqual(len(accounts), 7)


class FrameTests(unittest.TestCase):
    def test_native_tracer_fields(self):
        trace = {"type": "CALL", "from": "0xAB" + "00" * 19, "to": "0x" + "11" * 20, "value": "0x5", "input": "0x",
                 "output": "0x00ee", "error": "execution reverted", "gas": "0x1", "gasUsed": "0x1"}
        frame = poly_corpus.frame_of(trace)
        self.assertEqual(frame, {"type": "CALL", "from": "ab" + "00" * 19, "to": "11" * 20, "value": "5", "input": "",
                                 "output": "00ee", "error": "execution reverted", "calls": []})

    def test_missing_value_output_and_error(self):
        frame = poly_corpus.frame_of({"type": "STATICCALL", "from": "0x" + "00" * 20, "to": "0x" + "01" * 20})
        self.assertEqual((frame["value"], frame["input"], frame["output"], frame["error"]), ("0", "", "", None))

    def test_nested_calls(self):
        inner = {"type": "CALL", "from": "0x" + "01" * 20, "to": "0x" + "02" * 20, "value": "0x10"}
        frame = poly_corpus.frame_of({"type": "CALL", "from": "0x" + "00" * 20, "to": "0x" + "01" * 20,
                                      "calls": [inner]})
        self.assertEqual(frame["calls"][0]["value"], "16")
        self.assertEqual(frame["calls"][0]["to"], "02" * 20)

    def test_bad_hex_is_refused(self):
        with self.assertRaises(Exception):
            poly_corpus.hex_digits("0xzz")
        with self.assertRaises(Exception):
            poly_corpus.hex_digits(5)


class SampleTests(unittest.TestCase):
    def test_sample_passes_every_case(self):
        imported, reimported = sample()
        result, cases, differences = verdicts(imported, reimported)
        self.assertEqual(differences, [])
        self.assertEqual(result, "PASS")
        self.assertEqual(cases, {"transfer": "PASS", "nested-revert": "PASS", "fee": "PASS", "system-action": "PASS",
                                 "empty-block": "PASS"})
        self.assertEqual(poly_corpus.bridge_checks(imported, reimported), [])

    def test_observation_fields(self):
        imported, reimported = sample()
        observation = poly_corpus.observation_of(imported, reimported, SOURCE)
        corpus_compare.check_observation(observation)
        self.assertEqual(observation["decisions"], [{"block": n, "kind": "IMPORT"} for n in (1, 2, 3, 4)])
        self.assertEqual(observation["reimport"], {"new_observations": 0, "new_decisions": 0})
        sync = observation["blocks"][3]["state_sync"]
        self.assertEqual([row["id"] for row in sync], [1])
        self.assertEqual(sync[0]["state_receiver_last_state_id"], 1)
        self.assertEqual(observation["blocks"][0]["transactions"][1]["max_fee_per_gas"], "5000000000")
        self.assertNotIn("gas_price", observation["blocks"][0]["transactions"][1])
        self.assertEqual(observation["source"], SOURCE)

    def test_changed_call_frame_fails_nested_revert(self):
        imported, reimported = sample()
        reimported["blocks"][0]["transactions"][2]["call_tracer"]["calls"][0]["error"] = None
        result, cases, _ = verdicts(imported, reimported)
        self.assertEqual((result, cases["nested-revert"], cases["transfer"]), ("FAIL", "FAIL", "PASS"))

    def test_changed_state_read_fails_fee(self):
        imported, reimported = sample()
        reimported["blocks"][0]["transactions"][1]["state_after"][SIGNER]["balance"] = "1"
        result, cases, _ = verdicts(imported, reimported)
        self.assertEqual((result, cases["fee"], cases["nested-revert"]), ("FAIL", "FAIL", "PASS"))

    def test_missing_state_sync_event_fails_system_action(self):
        imported, reimported = sample()
        imported["state_sync"] = []
        result, cases, _ = verdicts(imported, reimported)
        self.assertEqual((result, cases["system-action"], cases["empty-block"]), ("FAIL", "FAIL", "PASS"))

    def test_state_sync_without_receipt_is_refused(self):
        imported, reimported = sample()
        reimported["blocks"][3]["state_sync_receipt"] = False
        with self.assertRaises(Exception):
            poly_corpus.observation_of(imported, reimported, SOURCE)

    def test_state_sync_event_of_another_block_fails(self):
        imported, reimported = sample()
        reimported["blocks"][2]["state_sync_receipt"] = True
        imported["state_sync"][0]["block"] = 3
        result, cases, _ = verdicts(imported, reimported)
        self.assertEqual((result, cases["system-action"]), ("FAIL", "FAIL"))

    def test_changed_last_state_id_fails_system_action(self):
        imported, reimported = sample()
        reimported["blocks"][3]["state_receiver_last_state_id"] = "0"
        _, cases, _ = verdicts(imported, reimported)
        self.assertEqual(cases["system-action"], "FAIL")

    def test_undecided_decision_is_left_out(self):
        imported, reimported = sample()
        imported["decisions"][1]["state"] = "INTENT"
        result, cases, _ = verdicts(imported, reimported)
        self.assertEqual((result, cases["empty-block"]), ("FAIL", "FAIL"))
        self.assertIn("a decision is not NATIVE_DECIDED with one member",
                      poly_corpus.bridge_checks(imported, reimported))

    def test_decision_kind_is_the_native_kind(self):
        imported, reimported = sample()
        imported["decisions"][2]["kind"] = "SIDE"
        result, cases, _ = verdicts(imported, reimported)
        self.assertEqual(poly_corpus.observation_of(imported, reimported, SOURCE)["decisions"][2],
                         {"block": 3, "kind": "SIDE"})
        self.assertEqual(result, "FAIL")

    def test_decision_of_an_unknown_block_fails(self):
        imported, reimported = sample()
        imported["decisions"][0]["members"][0]["block"] = "00" * 32
        result, _, differences = verdicts(imported, reimported)
        self.assertEqual(result, "FAIL")
        self.assertIn("decisions of other blocks", [row["field"] for row in differences])

    def test_new_observation_on_reimport_fails(self):
        imported, reimported = sample()
        reimported["observed"] = [1]
        result, _, differences = verdicts(imported, reimported)
        self.assertEqual(result, "FAIL")
        self.assertIn("reimport.new_observations", [row["field"] for row in differences])

    def test_new_decision_on_reimport_fails(self):
        imported, reimported = sample()
        extra = copy.deepcopy(reimported["decisions_after"][0])
        extra["sequence"] = 99
        reimported["decisions_after"].append(extra)
        result, _, differences = verdicts(imported, reimported)
        self.assertEqual(result, "FAIL")
        self.assertIn("reimport.new_decisions", [row["field"] for row in differences])

    def test_probe_error_is_refused(self):
        imported, reimported = sample()
        reimported["error"] = "boom"
        with self.assertRaises(Exception):
            poly_corpus.observation_of(imported, reimported, SOURCE)
        imported, reimported = sample()
        imported["error"] = "boom"
        with self.assertRaises(Exception):
            poly_corpus.observation_of(imported, reimported, SOURCE)


class BridgeTests(unittest.TestCase):
    def check(self, change, message):
        imported, reimported = sample()
        change(imported, reimported)
        failures = poly_corpus.bridge_checks(imported, reimported)
        self.assertTrue(any(message in failure for failure in failures), failures)

    def test_missing_observation(self):
        self.check(lambda i, r: i.__setitem__("observed", [1, 2, 4]), "not one observation per block")

    def test_second_key(self):
        self.check(lambda i, r: i["keys"]["2"].append("bor.run-1.9"), "exactly one observation key")

    def test_observer_error(self):
        self.check(lambda i, r: r.__setitem__("observer_errors", ["x"]), "observer reported an error")

    def test_import_observer_error(self):
        self.check(lambda i, r: i.__setitem__("observer_errors", ["x"]), "observer reported an error")

    def test_two_members(self):
        self.check(lambda i, r: i["decisions"][0]["members"].append(i["decisions"][1]["members"][0]),
                   "not NATIVE_DECIDED with one member")

    def test_members_are_the_observations(self):
        self.check(lambda i, r: i["decisions"][0]["members"][0].__setitem__("observation", "bor.run-1.x"),
                   "members are not the observations")

    def test_obligations(self):
        self.check(lambda i, r: i.__setitem__("obligations", i["obligations"][:-1]),
                   "obligations are not the IMPORT decisions")

    def test_restart_changed_decisions(self):
        self.check(lambda i, r: r.__setitem__("decisions_before", r["decisions_before"][:-1]),
                   "restart changed the decisions")

    def test_reimport_error(self):
        self.check(lambda i, r: r.__setitem__("reimport_error", "x"), "re-import failed")

    def test_reimport_changed_a_decision(self):
        self.check(lambda i, r: r["decisions_after"][1].__setitem__("marker", False), "re-import changed the decisions")

    def test_reimport_changed_obligations(self):
        self.check(lambda i, r: r.__setitem__("obligations", r["obligations"][:-1]),
                   "re-import changed the delivery obligations")

    def test_head_moved(self):
        self.check(lambda i, r: r.__setitem__("head_hash", "0x00"), "moved the head")


class VerdictTests(unittest.TestCase):
    def comparison(self):
        imported, reimported = sample()
        return corpus_compare.compare(expected(), poly_corpus.observation_of(imported, reimported, SOURCE))

    def test_pass_needs_the_comparison_and_the_bridge(self):
        result, cases = poly_corpus.verdict(self.comparison(), [])
        self.assertEqual(result, "PASS")
        self.assertTrue(all(row["verdict"] == "PASS" for row in cases))

    def test_bridge_failure_fails_the_run_and_every_case(self):
        result, cases = poly_corpus.verdict(self.comparison(), ["the re-import moved the head"])
        self.assertEqual(result, "FAIL")
        self.assertEqual({row["verdict"] for row in cases}, {"FAIL"})
        self.assertEqual(cases[0]["bridge_failures"], 1)

    def test_comparison_failure_fails_the_run(self):
        comparison = self.comparison()
        comparison["result"] = "FAIL"
        self.assertEqual(poly_corpus.verdict(comparison, [])[0], "FAIL")

    def test_no_case_is_no_pass(self):
        comparison = self.comparison()
        comparison["cases"] = []
        self.assertEqual(poly_corpus.verdict(comparison, [])[0], "FAIL")


class ObserveTests(unittest.TestCase):
    def directory(self, change=None):
        root = Path(tempfile.mkdtemp()).resolve()
        imported, reimported = sample()
        if change:
            change(imported, reimported)
        (root / "import.json").write_text(json.dumps(imported))
        (root / "reimport.json").write_text(json.dumps(reimported))
        return root

    def test_writes_new_files(self):
        root = self.directory()
        output = root / "out"
        output.mkdir()
        comparison, failures = poly_corpus.observe(root, expected(), SOURCE, output)
        self.assertEqual((comparison["result"], failures), ("PASS", []))
        self.assertEqual(json.loads((output / "comparison.json").read_text()), comparison)
        self.assertEqual(json.loads((output / "observation.json").read_text())["source"], SOURCE)

    def test_never_replaces_a_retained_file(self):
        root = self.directory()
        (root / "comparison.json").write_text("retained")
        with self.assertRaises(FileExistsError):
            poly_corpus.observe(root, expected(), SOURCE, root)
        self.assertEqual((root / "comparison.json").read_text(), "retained")

    def test_format_error_is_a_failed_comparison(self):
        root = self.directory(lambda i, r: r["blocks"][0].__setitem__("hash", "not hex"))
        output = root / "out"
        output.mkdir()
        comparison, _ = poly_corpus.observe(root, expected(), SOURCE, output)
        self.assertEqual(comparison["result"], "FAIL")
        self.assertIn("hash", comparison["format_error"])
        self.assertEqual(poly_corpus.verdict(comparison, [])[0], "FAIL")


if __name__ == "__main__":
    unittest.main()
