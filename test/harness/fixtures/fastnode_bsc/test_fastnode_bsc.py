"""Portable controls for the path-scheme probe driver (fastnode_bsc.py).

No native client runs here. Synthetic reports and traces exercise the
independent rules: each control changes one observation and checks that the
judge refuses it.
"""

import sys
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
for directory in (str(HERE), str(HERE.parents[1])):
    if directory not in sys.path:
        sys.path.insert(0, str(directory))

import bsc_decision  # noqa: E402
import fastnode_bsc as driver  # noqa: E402

LOG = "INFO Extractor lifecycle is live boundary=3 hash=0xA3\nINFO Blockchain stopped\n"


def golive_report():
    decided = [{"block": name, "import_error": None, "head": name, "head_is_block": True,
                "head_state_id": str(number), "persistent_state_id": str(number), "new_decisions": 1}
               for name, number in (("A4", 4), ("A5", 5))]
    return {"schema_version": 1, "kind": "bsc-fastnode-golive", "state_scheme": "path",
            "child": {"scheme": "path", "boundary": 3, "boundary_hash": "0xA3", "head": "A3", "head_state_id": "3",
                      "persistent_state_id": "3", "phase": "LIVE"},
            "stopped": {"head": "A3", "head_state_id": "3", "persistent_state_id": "3"},
            "restart": {"restart_error": None, "phase": "LIVE", "head": "A3", "boundary": 3, "view_head": "A3",
                        "decided": decided}}


def decision_inputs():
    extra = {"state_scheme": "path", "block2_has_the_root_of_block1": "true", "head_state_id": "1",
             "persistent_state_id": "1"}
    report = {"scenarios": [{"name": "live", "extra": extra}]}
    for mode in bsc_decision.CRASH_MODES:
        if mode in driver.BEFORE_BARRIER:
            item = {"state_ids_before_restart": "none/0", "persistent_state_id_after_restart": "0"}
        elif mode in driver.PATH_HEAD_ABOVE:
            item = {"state_ids_before_restart": "1/1", "persistent_state_id_after_restart": "0"}
        else:
            item = {"state_ids_before_restart": "1/1", "persistent_state_id_after_restart": "1"}
        report["scenarios"].append({"name": mode, "extra": item})
    summary = {"live": {"decisions": 2}}
    traces = {"barrier-synced": [{"event": "barrier", "observation": "o"}, {"event": "barrier-check", "observation": "o",
                                                                      "block1_state_id": "1", "persistent_state_id": "1"},
                                 {"event": "crash", "point": "barrier-synced"}]}
    return summary, report, traces


class JudgeTests(unittest.TestCase):
    def judge_golive(self, report, text=LOG):
        c = driver.Checks()
        driver.judge_golive(c, report, text)
        return c.failures()

    def test_golive_report_passes(self):
        failures = self.judge_golive(golive_report())
        self.assertEqual(failures, [])

    def test_each_broken_golive_observation_rejects(self):
        def child(report, key, value):
            report["child"][key] = value

        def stopped(report, key, value):
            report["stopped"][key] = value

        def restart(report, key, value):
            report["restart"][key] = value

        def row(report, key, value):
            report["restart"]["decided"][1][key] = value

        mutations = {
            "child hash scheme": lambda r: child(r, "scheme", "hash"),
            "child boundary": lambda r: child(r, "boundary", 2),
            "child head": lambda r: child(r, "head", "A2"),
            "child not live": lambda r: child(r, "phase", "BOOTSTRAP"),
            "child state not persistent": lambda r: child(r, "persistent_state_id", "2"),
            "child state unknown": lambda r: child(r, "head_state_id", "none"),
            "stopped head": lambda r: stopped(r, "head", "A2"),
            "stopped state": lambda r: stopped(r, "head_state_id", "2"),
            "stopped persistent": lambda r: stopped(r, "persistent_state_id", "2"),
            "restart error": lambda r: restart(r, "restart_error", "native head 0 is below the boundary H 3"),
            "restart phase": lambda r: restart(r, "phase", "RECOVERY"),
            "restart head": lambda r: restart(r, "head", "A2"),
            "restart boundary": lambda r: restart(r, "boundary", 2),
            "restart view": lambda r: restart(r, "view_head", "A2"),
            "one decided block": lambda r: restart(r, "decided", r["restart"]["decided"][:1]),
            "import error": lambda r: row(r, "import_error", "failed"),
            "head is not the block": lambda r: row(r, "head_is_block", False),
            "no decision": lambda r: row(r, "new_decisions", 0),
            "head state not persistent": lambda r: row(r, "persistent_state_id", "4"),
        }
        for label, change in mutations.items():
            report = golive_report()
            change(report)
            with self.subTest(label):
                self.assertTrue(self.judge_golive(report), label)

    def test_golive_node_log_rejects_a_repair_and_an_invariant_error(self):
        for text in ("INFO Head state missing, repairing number=3\n",
                     "ERROR native head 0 is below the boundary H 3: source invariant\n", "INFO started\n"):
            with self.subTest(text):
                self.assertTrue(self.judge_golive(golive_report(), text))

    def test_decision_rules_pass(self):
        c = driver.Checks()
        driver.judge_decision(c, *decision_inputs())
        self.assertEqual(c.failures(), [])
        self.assertEqual(len(c.rows), 4 + len(bsc_decision.CRASH_MODES))

    def test_each_broken_decision_observation_rejects(self):
        def set_live(inputs, key, value):
            inputs[1]["scenarios"][0]["extra"][key] = value

        def set_mode(inputs, mode, key, value):
            next(sc for sc in inputs[1]["scenarios"] if sc["name"] == mode)["extra"][key] = value

        def barrier(inputs, key, value):
            inputs[2]["barrier-synced"][1][key] = value

        mutations = {
            "hash store": lambda i: set_live(i, "state_scheme", "hash"),
            "block 2 changes the state": lambda i: set_live(i, "block2_has_the_root_of_block1", "false"),
            "one decision": lambda i: i[0]["live"].__setitem__("decisions", 1),
            "head state not persistent": lambda i: set_live(i, "persistent_state_id", "0"),
            "barrier state not persistent": lambda i: barrier(i, "persistent_state_id", "0"),
            "barrier state unknown": lambda i: barrier(i, "block1_state_id", "none"),
            "barrier not observed": lambda i: i[2].__setitem__("barrier-synced", []),
            "state committed before the barrier": lambda i: set_mode(i, "first-handoff", "state_ids_before_restart", "1/1"),
            "persistent state moved before the barrier": lambda i: set_mode(i, "exec-ready-synced",
                                                                           "persistent_state_id_after_restart", "1"),
            "barrier state not persistent before the restart": lambda i: set_mode(i, "barrier-synced",
                                                                                 "state_ids_before_restart", "1/0"),
            "persistent layer not rolled back": lambda i: set_mode(i, "intent-synced",
                                                                   "persistent_state_id_after_restart", "1"),
            "persistent layer rolled back below the head": lambda i: set_mode(i, "head-written",
                                                                              "persistent_state_id_after_restart", "0"),
            "missing restart observation": lambda i: set_mode(i, "block-written", "state_ids_before_restart", None),
            "unparsable restart observation": lambda i: set_mode(i, "native-rejection", "state_ids_before_restart", "1"),
            "not a number": lambda i: set_mode(i, "block-written", "persistent_state_id_after_restart", "none"),
        }
        for label, change in mutations.items():
            inputs = decision_inputs()
            change(inputs)
            c = driver.Checks()
            driver.judge_decision(c, *inputs)
            with self.subTest(label):
                self.assertTrue(c.failures(), label)

    def test_the_path_restart_table_differs_where_the_persistent_layer_is_above_the_head(self):
        self.assertEqual(set(driver.PATH_RESTART), set(bsc_decision.EXPECTED_RESTART))
        for mode, (state, on_disk, head) in bsc_decision.EXPECTED_RESTART.items():
            want = (state, False if mode in driver.PATH_HEAD_ABOVE else on_disk, head)
            self.assertEqual(driver.PATH_RESTART[mode], want, mode)
        self.assertEqual(sorted(driver.PATH_HEAD_ABOVE + driver.PATH_HEAD_MOVED + driver.BEFORE_BARRIER),
                         sorted(bsc_decision.CRASH_MODES))
        saved = bsc_decision.EXPECTED_RESTART
        with driver.PathRestartRules():
            self.assertIs(bsc_decision.EXPECTED_RESTART, driver.PATH_RESTART)
            self.assertIsNot(bsc_decision.EXPECTED_RESTART, saved)
        self.assertIs(bsc_decision.EXPECTED_RESTART, saved)

    def test_a_malformed_report_is_a_failed_check(self):
        # The decision judge needs the scenarios and the live summary: without
        # them it fails as one malformed report, never as an exception.
        c = driver.Checks()
        driver.judge_part(c, "decision", driver.judge_decision, {"live": {}}, {"scenarios": []}, {})
        self.assertEqual([row["check"] for row in c.failures()], ["decision: well-formed report"])
        # The live boundary judge names every missing observation.
        c = driver.Checks()
        driver.judge_part(c, "golive", driver.judge_golive, {}, LOG)
        self.assertTrue(c.failures())
        self.assertNotIn("golive: well-formed report", [row["check"] for row in c.failures()])
        c = driver.Checks()
        driver.judge_part(c, "golive", driver.judge_golive, golive_report(), LOG)
        self.assertEqual(c.failures(), [])


if __name__ == "__main__":
    unittest.main()
