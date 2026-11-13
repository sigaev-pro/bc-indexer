#!/usr/bin/env python3
"""Portable controls of the Bor block of E01-02.lifecycle_seams: the fixture
derivation, the evidence digests, the coverage sum and gaps, the judge on the
development sample outputs (sample/, not measured) and on single changes of
them, the AC matrix, the installer and the patch set."""

import copy
import json
from pathlib import Path
import re
import sys
import tempfile
import unittest

NATIVE = Path(__file__).resolve().parent
sys.path.insert(0, str(NATIVE))
import lifecycle_bor as lb  # noqa: E402
import lifecycle_tree  # noqa: E402
from bor_admission_tree import TreeError  # noqa: E402

CALLERS = json.loads((NATIVE.parent / "callers.json").read_text())
SAMPLE = NATIVE / "sample"


def sample():
    return {name: json.loads((SAMPLE / file).read_text()) for name, file in lb.OUTPUT_FILES.items()}


def genesis(**config_extra):
    config = {"chainId": 15001, "bor": {"period": {"0": 1}}, **config_extra}
    return json.dumps({"config": config, "alloc": {"0x0000000000000000000000000000000000001000": {"balance": "0x1"}},
                       "timestamp": "0x5ce28211"}).encode()


class Fixture(unittest.TestCase):
    def test_derived_inputs(self):
        document, config = lb.derived_inputs(genesis())
        document, config = json.loads(document), json.loads(config)
        self.assertEqual(document["config"]["bor"]["stateSyncConfirmationDelay"], {"0": 0})
        self.assertEqual(config, document["config"])
        self.assertEqual(document["alloc"]["0x" + lb.SENDER], {"balance": hex(10 ** 20)})
        self.assertEqual(len(document["alloc"]), 2)

    def test_derived_inputs_refusals(self):
        for chain in (1, 2, 15002):
            with self.assertRaises(Exception):
                lb.derived_inputs(genesis(chainId=chain))
        with self.assertRaises(Exception):
            lb.derived_inputs(json.dumps({"config": {"chainId": 15001, "bor": {"stateSyncConfirmationDelay": {}}},
                                          "alloc": {}}).encode())
        with self.assertRaises(Exception):
            lb.derived_inputs(json.dumps({"config": {"chainId": 15001, "bor": {}},
                                          "alloc": {"0x" + lb.SENDER.upper(): {}}}).encode())
        with self.assertRaises(Exception):
            lb.derived_inputs(json.dumps({"config": {"chainId": 15001}, "alloc": {}}).encode())


class Digests(unittest.TestCase):
    def test_vectors_match_the_go_test(self):
        go = (NATIVE / "borlifecycle/borlifecycle_test.go").read_text()
        quoted = re.findall(r'got != "([0-9a-f]{64})"', go)
        self.assertEqual([lb.evidence_digest(e) for e in lb.VECTORS], quoted)

    def test_digest_framing(self):
        self.assertNotEqual(lb.digest("t", "ab", "c"), lb.digest("t", "a", "bc"))
        self.assertNotEqual(lb.digest("t", "a"), lb.digest("u", "a"))
        changed = dict(lb.VECTORS[1], old="ab")
        self.assertNotEqual(lb.evidence_digest(changed), lb.evidence_digest(lb.VECTORS[1]))


class Coverage(unittest.TestCase):
    def test_sum_and_gaps_of_the_sample(self):
        outputs = sample()
        coverage = lb.summed_coverage(outputs)
        self.assertEqual(lb.gaps(CALLERS, coverage), [])
        self.assertEqual(coverage["bor.parallel"], {"Passes": 0, "Intents": 0, "Refusals": 1})
        self.assertEqual(coverage["bor.seal-write"]["Refusals"], 4)
        self.assertEqual(coverage["bor.manual-commands"]["Refusals"], 3)
        self.assertEqual(coverage["bor.recovery"]["Passes"], 13)

    def test_gaps_rules(self):
        coverage = lb.summed_coverage(sample())
        for caller, field in (("bor.import", "Passes"), ("bor.known-block", "Intents"),
                              ("bor.snap-sync", "Refusals"), ("bor.startup-reconcile", "Intents")):
            changed = copy.deepcopy(coverage)
            changed[caller][field] = 0
            self.assertEqual(lb.gaps(CALLERS, changed), [caller])
        changed = copy.deepcopy(coverage)
        changed["bor.known-block"] = {"Passes": 3, "Intents": 0, "Refusals": 0}
        self.assertEqual(lb.gaps(CALLERS, changed), ["bor.known-block"])
        del changed["bor.known-block"]
        changed["bor.eth-call"] = {"Passes": 0, "Intents": 0, "Refusals": 0}
        self.assertEqual(lb.gaps(CALLERS, changed), ["bor.known-block"])

    def test_profile_and_manual_refusals_count(self):
        outputs = sample()
        outputs["manual"]["commands"][0]["refused"] = False
        outputs["profile"]["sets"][1]["refusals"] = []
        coverage = lb.summed_coverage(outputs)
        self.assertEqual(coverage["bor.manual-commands"]["Refusals"], 2)
        self.assertEqual(coverage["bor.snap-sync"]["Refusals"], 1)


class Judge(unittest.TestCase):
    def failures(self, change=None):
        outputs = sample()
        if change:
            change(outputs)
        return lb.judge(outputs, CALLERS)[0]

    def test_sample_passes(self):
        self.assertEqual(self.failures(), [])

    def test_rows_keep_decided_decisions_only(self):
        row = {"kind": "CANONICAL", "old_head": "A1", "new_head": "A2", "members": []}
        self.assertEqual(lb.rows([dict(row, state="INTENT"), dict(row, state="NATIVE_REJECTED")]), [])
        self.assertEqual(lb.rows([dict(row, state="NATIVE_DECIDED")]), [("CANONICAL", "A1", "A2", [])])

    def assertFails(self, change, text):
        failures = self.failures(change)
        self.assertTrue(any(text in f for f in failures), failures)

    def test_scenario_changes(self):
        self.assertFails(lambda o: o["scenario"].update(error="x"), "the scenario reported an error")
        self.assertFails(lambda o: o["scenario"]["reconcile"].update(phase="RECOVERY"), "BOOTSTRAP to LIVE")
        self.assertFails(lambda o: o["scenario"]["reconcile"]["boundary"]["head"].update(number=1), "not the genesis")
        self.assertFails(lambda o: o["scenario"]["steps"][5].update(head_name="A2"), "steps or heads differ")
        self.assertFails(lambda o: o["scenario"]["steps"][8].update(error="boom"), "step set-canonical-C4 failed")
        self.assertFails(lambda o: o["scenario"]["decisions"][6].update(kind="IMPORT"), "scenario decisions differ")
        self.assertFails(lambda o: o["scenario"]["decisions"][6]["members"].append("B4"), "scenario decisions differ")
        self.assertFails(lambda o: o["scenario"]["decisions"].pop(), "scenario decisions differ")

    def test_restart_changes(self):
        self.assertFails(lambda o: o["restart"]["reconcile"].update(phase="BOOTSTRAP"), "restart did not go")
        self.assertFails(lambda o: o["restart"]["reconcile"]["boundary"].update(root="00"), "restart did not keep H")
        self.assertFails(lambda o: o["restart"]["native_head"].update(name="H6"), "restart ended at H6")
        self.assertFails(lambda o: o["restart"]["reconcile"].update(repaired={"hash": "x"}), "settled or repaired")
        self.assertFails(lambda o: o["restart"]["decisions"].pop(), "clean restart changed the decisions")
        self.assertFails(lambda o: o["restart"].update(error="x"), "restart reported an error")

    def test_crash_and_settle_changes(self):
        for name in ("crash", "crash-resolve", "crash-import"):
            self.assertFails(lambda o, n=name: o[n].update(exit=0), f"the {name} child did not stop")
            self.assertFails(lambda o, n=name: o[n].update(stderr_tail=""), f"the {name} child did not stop")
        self.assertFails(lambda o: o["crash-resolve"].update(stderr_tail="crash point: marker of bor.set-head"),
                         "the crash-resolve child did not stop")
        self.assertFails(lambda o: o["settle"]["reconcile"]["settled"][0].update(outcome="NATIVE_REJECTED"),
                         "settle the set-head intent")
        self.assertFails(lambda o: o["settle"]["reconcile"].update(settled=[]), "settle the set-head intent")
        self.assertFails(lambda o: o["settle"]["decisions"][-1].update(new_head="H5"), "set-head from H7 to H6")
        self.assertFails(lambda o: o["settle"]["native_head"].update(name="H7"), "settle ended at H7")

    def test_repair_changes(self):
        self.assertFails(lambda o: o["fault"].update(number=7), "did not hit the head H5")
        self.assertFails(lambda o: o["repair"]["reconcile"].update(repaired=None), "did not decide the native repair")
        self.assertFails(lambda o: o["repair"]["native_head_at_start"].update(number=6),
                         "did not decide the native repair")
        self.assertFails(lambda o: o["repair"]["decisions"][-1].update(old_head="H7"), "repair decision")
        self.assertFails(lambda o: o["repair"]["coverage"].pop("bor.startup-reconcile"), "made no intent")
        self.assertFails(lambda o: o["repair"]["reconcile"]["boundary"]["head"].update(number=2),
                         "repair did not keep H")
        self.assertFails(lambda o: o["repair"].update(phase_after_start="RECOVERY"), "repair did not go")
        self.assertFails(lambda o: o["repair"].update(error="x"), "repair reported an error")

    def test_marked_intent_recovery_changes(self):
        for name in ("resolved", "imported"):
            self.assertFails(lambda o, n=name: o[n]["decision_recovery"].update(resolved=[]),
                             f"{name}: the decision recovery did not resolve")
            self.assertFails(lambda o, n=name: o[n]["reconcile"].update(repaired={"hash": "x"}),
                             f"{name}: the decision recovery did not resolve")
            self.assertFails(lambda o, n=name: o[n]["decisions"].pop(), f"{name}: the resolved decision")
            self.assertFails(lambda o, n=name: o[n]["decisions"][-1].update(state="INTENT"),
                             f"{name} left a decision unresolved")
        self.assertFails(lambda o: o["resolved"]["native_head"].update(name="H6"), "resolved ended at H6")
        self.assertFails(lambda o: o["imported"]["native_head"].update(name="H5"), "imported ended at H5")

    def test_stateless_rewind_changes(self):
        self.assertFails(lambda o: o["stateless"]["native_head"].update(name="D4"), "did not import A1 to D5")
        self.assertFails(lambda o: o["stateless"]["steps"][3].update(error="x"), "did not import A1 to D5")
        self.assertFails(lambda o: o["stateless"].update(error="x"), "did not import A1 to D5")
        self.assertFails(lambda o: o["fault-stateless"].update(block="D5"), "did not hit D4")
        self.assertFails(lambda o: o["refuse"]["steps"][0].update(error="not refused"), "was not refused")
        self.assertFails(lambda o: o["refuse"]["native_head"].update(name="D4"), "was not refused")
        self.assertFails(lambda o: o["refuse"]["decisions"].append(dict(o["refuse"]["decisions"][-1])),
                         "was not refused")
        self.assertFails(lambda o: o["refuse"].update(phase_after_start="RECOVERY"), "was not refused")
        self.assertFails(lambda o: o["refuse"].update(error="x"), "was not refused")
        for state in ("INTENT", "NATIVE_REJECTED"):
            def leftover(o, state=state):
                row = dict(o["refuse"]["decisions"][-1], kind="CANONICAL", new_head="D4", state=state)
                o["refuse"]["decisions"].append(row)
            self.assertFails(leftover, "the refused rewind left an intent")
        self.assertFails(lambda o: o["refuse"]["coverage"].update({"bor.set-head": {"Passes": 1, "Intents": 1,
                                                                                    "Refusals": 0}}),
                         "the refused rewind left an intent")
        self.assertFails(lambda o: o["stateless"]["decisions"][-1].update(state="INTENT"),
                         "the refused rewind left an intent")
        self.assertFails(lambda o: o["refuse"]["decisions"][-1].update(sequence=999),
                         "the refused rewind left an intent")

        def both_open(o):
            for name in ("stateless", "refuse"):
                o[name]["decisions"][-1]["state"] = "INTENT"
        self.assertFails(both_open, "the refused rewind left an intent")

    def test_interrupted_rewind_changes(self):
        self.assertFails(lambda o: o["completed"]["reconcile"]["settled"][0].update(completed=False),
                         "the interrupted rewind was not completed")
        self.assertFails(lambda o: o["completed"]["reconcile"]["settled"][0].update(outcome="NATIVE_REJECTED"),
                         "the interrupted rewind was not completed")
        self.assertFails(lambda o: o["completed"]["decisions"][-1].update(old_head="H7"),
                         "the interrupted rewind was not completed")
        self.assertFails(lambda o: o["completed"]["native_head_at_start"].update(name="H8"),
                         "left the head at H8")
        self.assertFails(lambda o: o["completed"]["native_head"].update(name="H6"), "completed ended at H6")
        self.assertFails(lambda o: o["crash-mid"].update(stderr_tail="crash point: marker of bor.set-head"),
                         "the crash-mid child did not stop")
        self.assertFails(lambda o: o["advance"]["steps"][1].update(error="x"), "did not import H7 and H8")
        self.assertFails(lambda o: o["advance"]["decisions"].pop(), "did not import H7 and H8")

    def test_evidence_records(self):
        for name in lb.COVERAGE_FILES:
            self.assertFails(lambda o, n=name: o[n].update(evidence_records=1), f"{name} left lifecycle evidence")
        self.assertFails(lambda o: o["repair"].pop("evidence_records"), "repair left lifecycle evidence")

    def test_corpus_and_plain_server(self):
        self.assertFails(lambda o: o["scenario"]["steps"][4].update(error="not refused by the corpus rule"),
                         "step known-A3-refused failed")
        self.assertFails(lambda o: o["profile"]["plain_server"].update(refused=False), "without --extractor.capture")
        self.assertFails(lambda o: o["profile"].pop("plain_server"), "without --extractor.capture")

    def test_bootstrap_changes(self):
        self.assertFails(lambda o: o["crash-bootstrap"].update(exit=1), "BOOTSTRAP child did not stop")
        self.assertFails(lambda o: o["crash-bootstrap"].update(stderr_tail=""), "BOOTSTRAP child did not stop")
        self.assertFails(lambda o: o["bootstrap"].update(bootstrap_at_start=False), "did not sync natively")
        self.assertFails(lambda o: o["bootstrap"]["reconcile"].update(phase="RECOVERY"), "did not sync natively")
        self.assertFails(lambda o: o["bootstrap"].update(phase_after_start="BOOTSTRAP"), "did not sync natively")
        self.assertFails(lambda o: o["bootstrap"]["steps"][5].update(error="x"), "did not sync natively")
        self.assertFails(lambda o: o["bootstrap"]["steps"].pop(5), "did not sync natively")
        self.assertFails(lambda o: o["bootstrap"].update(error="x"), "did not sync natively")
        self.assertFails(lambda o: o["bootstrap"]["head_at_golive"].update(name="A4"), "H is not the native head")
        self.assertFails(lambda o: o["bootstrap"]["reconcile"]["boundary"]["head"].update(hash="00"),
                         "H is not the native head")
        self.assertFails(lambda o: o["bootstrap"]["decisions"].insert(0, dict(o["bootstrap"]["decisions"][0],
                                                                              kind="IMPORT", members=["A1"])),
                         "the initial sync was captured")
        self.assertFails(lambda o: o["bootstrap"]["decisions"].pop(), "the initial sync was captured")
        self.assertFails(lambda o: o["bootstrap-restart"].update(bootstrap_at_start=True), "did not keep H")
        self.assertFails(lambda o: o["bootstrap-restart"]["reconcile"].update(phase="BOOTSTRAP"), "did not keep H")
        self.assertFails(lambda o: o["bootstrap-restart"].update(phase_after_start="RECOVERY"), "did not keep H")
        self.assertFails(lambda o: o["bootstrap-restart"]["reconcile"]["boundary"]["head"].update(number=4),
                         "did not keep H")
        self.assertFails(lambda o: o["bootstrap-restart"]["native_head"].update(name="A4"), "did not keep H")
        self.assertFails(lambda o: o["bootstrap-restart"]["decisions"].pop(), "did not keep H")
        self.assertFails(lambda o: o["bootstrap-restart"].update(error="x"), "did not keep H")
        self.assertFails(lambda o: o["scenario"].update(bootstrap_at_start=False), "BOOTSTRAP to LIVE")
        self.assertFails(lambda o: o["bootstrap"].update(whitelist_calls_during_golive=19), "while BOOTSTRAP ended")
        self.assertFails(lambda o: o["bootstrap"].pop("whitelist_calls_during_golive"), "while BOOTSTRAP ended")
        self.assertFails(lambda o: o["bootstrap"]["coverage"].pop("bor.milestone"), "while BOOTSTRAP ended")

    def test_manual_messages(self):
        self.assertFails(lambda o: o["manual"]["commands"][0].update(output="flag provided but not defined"),
                         "capture-store or retention guard")
        self.assertFails(lambda o: o["manual"]["commands"][1].update(output="datadir already used by another process"),
                         "capture-store or retention guard")
        self.assertFails(lambda o: o["manual"]["commands"][2].update(caller="bor.prune-commands"),
                         "capture-store or retention guard")

    def test_refusal_changes(self):
        self.assertFails(lambda o: o["manual"]["commands"][2].update(refused=False), "manual command")
        self.assertFails(lambda o: o["manual"].update(chaindata_kept=False), "manual command")
        self.assertFails(lambda o: o["profile"]["sets"][9].update(refusals=[]), "profile refusals differ")
        self.assertFails(lambda o: o["profile"]["sets"][0].update(refusals=[{"caller": "bor.preconf"}]),
                         "profile refusals differ")
        self.assertFails(lambda o: o["profile"]["sets"][3].update(config_error="unknown flag"), "did not parse")
        self.assertFails(lambda o: o["profile"]["server"].update(refused=False), "before eth.New")
        self.assertFails(lambda o: o["profile"]["server"].update(chaindata_created=True), "before eth.New")

    def test_native_caller_changes(self):
        self.assertFails(lambda o: o["native-caller"]["after_import"]["head"].update(name="A4"),
                         "admin_importChain and the harness import differ")
        self.assertFails(lambda o: o["native-caller"]["after_import"]["decisions"][2].update(kind="SIDE"),
                         "admin_importChain and the harness import differ")
        self.assertFails(lambda o: o["native-caller"]["steps"][0].update(error="x"), "did not import A1 to A5")
        self.assertFails(lambda o: o["native-caller"].update(error="x"), "admin_importChain node reported")

    def test_gap_changes(self):
        self.assertFails(lambda o: o["scenario"]["coverage"].pop("bor.milestone-reorg"),
                         "lifecycle gaps: ['bor.milestone-reorg']")
        self.assertFails(lambda o: o["gaps"].update(gaps=["bor.import"]), "the Go and Python gaps differ")


class Matrix(unittest.TestCase):
    def test_ac_matrix(self):
        passing = {row["ac"]: row for row in lb.ac_matrix("PASS")}
        self.assertEqual(passing["AC-CAP-01-01"]["poly_private_local"], "PASS")
        self.assertEqual(lb.ac_matrix("FAIL")[0]["poly_private_local"], "FAIL")
        for ac in ("AC-LIF-05-02", "AC-LIF-05-03", "AC-LIF-06-04", "AC-LIF-06-05"):
            self.assertEqual(passing[ac]["poly_private_local"], "NOT_APPLICABLE")
        for row in lb.ac_matrix("PASS"):
            self.assertEqual((row["polygon_mainnet"], row["amoy"]), ("NOT_RUN", "NOT_RUN"))

    def test_observe_writes_new_files_only(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory).resolve()
            judgment = lb.observe(SAMPLE, CALLERS, output)
            self.assertEqual(judgment["result"], "PASS")
            with self.assertRaises(FileExistsError):
                lb.observe(SAMPLE, CALLERS, output)

    def test_observe_fails_a_failing_run(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory).resolve() / "outputs"
            source.mkdir()
            for file in lb.OUTPUT_FILES.values():
                (source / file).write_bytes((SAMPLE / file).read_bytes())
            crash = json.loads((source / "crash.json").read_text())
            crash["exit"] = 0
            (source / "crash.json").write_text(json.dumps(crash))
            judgment = lb.observe(source, CALLERS, Path(directory).resolve())
            self.assertEqual(judgment["result"], "FAIL")
            self.assertEqual(judgment["ac_matrix"][0]["poly_private_local"], "FAIL")


class Installer(unittest.TestCase):
    def test_patch_set(self):
        patches = json.loads((NATIVE / "patches.json").read_text())
        names = [edit["name"] for edit in patches["edits"]]
        self.assertEqual(len(names), len(set(names)))
        self.assertTrue(all(name.startswith("lifecycle-") for name in names))
        for tree_path, origin in patches["files"].items():
            self.assertEqual(origin, "tree/" + tree_path)
            self.assertTrue((NATIVE / origin).is_file())
        on_disk = sorted(p.relative_to(NATIVE / "tree").as_posix() for p in (NATIVE / "tree").rglob("*.go"))
        self.assertEqual(sorted(patches["files"]), on_disk)
        earlier = [e["name"] for p in ("admission", "decision")
                   for e in json.loads((NATIVE.parents[1] / p / "native/patches.json").read_text())["edits"]]
        self.assertFalse(set(names) & set(earlier))

    def test_install_refuses_a_changed_tree(self):
        edit = json.loads((NATIVE / "patches.json").read_text())["edits"][0]
        with self.assertRaises(TreeError):
            lifecycle_tree.apply_edit(edit["anchor"] + edit["anchor"], edit)
        with self.assertRaises(TreeError):
            lifecycle_tree.apply_edit("no anchor", edit)
        self.assertIn(edit["text"], lifecycle_tree.apply_edit("prefix " + edit["anchor"] + " suffix", edit))
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(TreeError):
                lifecycle_tree.install_lifecycle(Path(directory))

    def test_install_refuses_a_repeated_edit_name(self):
        patches = json.loads((NATIVE / "patches.json").read_text())
        with self.assertRaises(TreeError):
            lifecycle_tree.install_lifecycle(Path("/nonexistent"), earlier=[patches["edits"][0]["name"]])


if __name__ == "__main__":
    unittest.main()
