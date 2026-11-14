"""Portable controls of the E01-04.early_faults catalog and judge."""

import contextlib
import copy
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import faults_judge  # noqa: E402

H = 10
HEAD = 14
HASH_H = "11" * 32
ROOT_H = "22" * 32
DEFAULTS = {"mode": "LIVE", "admission": "none", "pin": "none", "decision": "none"}


def catalog():
    return faults_judge.load_json(faults_judge.CATALOG)


def boundary(number=H):
    return {"number": number, "hash": HASH_H if number == H else "33" * 32, "root": ROOT_H}


def perfect_case(point, variant):
    """Build a case that meets every rule of its point."""
    expected = point["after_restart"][variant]
    present = expected["boundary"][-1] == "present"
    after = dict(DEFAULTS)
    for field in faults_judge.STATE_FIELDS:
        if field in expected:
            after[field] = expected[field][0]
    if not present:
        after["mode"] = "BOOTSTRAP"
    elif after["mode"] == "BOOTSTRAP":
        after["mode"] = "LIVE"
    after["boundary"] = boundary() if present else None
    fault_block = H if point["seam"] == "H" else H + 2
    power = variant == "power_loss"
    return {
        "point": point["id"], "variant": variant,
        "fault": {"hit": True, "block": fault_block},
        "storage": {"reset_to_synced": power, "unsynced_dropped": 1 if power and point["pending"] else 0},
        "after_restart": after,
        "final": {"boundary": boundary(), "head": HEAD, "exported": list(range(H + 1, HEAD + 1)),
                  "duplicates": 0, "corpus": "PASS"},
    }


def perfect_report(family="eth", cat=None):
    cat = cat or catalog()
    cases = [perfect_case(point, variant) for point in cat["points"] for variant in faults_judge.VARIANTS]
    handoffs = [{"id": h["id"], "verdict": "PASS", "evidence": "record 01"}
                for h in cat["handoffs"] if h["block"] == family]
    return {"schema_version": 1, "kind": "native-faults-report", "family": family,
            "storage_model": faults_judge.STORAGE_MODEL, "cases": cases, "handoffs": handoffs}


def case_of(report, point_id, variant):
    for case in report["cases"]:
        if case["point"] == point_id and case["variant"] == variant:
            return case
    raise KeyError(point_id)


class CatalogTests(unittest.TestCase):
    def test_repository_catalog_is_valid(self):
        points = faults_judge.check_catalog(catalog())
        self.assertEqual(len(points), 15)
        seams = {point["seam"] for point in points.values()}
        self.assertEqual(seams, set(faults_judge.SEAMS))

    def test_power_loss_never_allows_more_than_crash(self):
        for point in catalog()["points"]:
            crash, power = point["after_restart"]["crash"], point["after_restart"]["power_loss"]
            for field, allowed in power.items():
                self.assertTrue(set(allowed) <= set(crash.get(field, allowed)), (point["id"], field))

    def test_pending_points_allow_fewer_states_after_power_loss(self):
        for point in catalog()["points"]:
            if not point["pending"] or point["id"] == "dec.before-decided-sync":
                continue
            crash, power = point["after_restart"]["crash"], point["after_restart"]["power_loss"]
            narrower = [f for f in power if set(power[f]) < set(crash.get(f, power[f]))]
            self.assertTrue(narrower, point["id"])

    def test_every_family_has_handoffs(self):
        blocks = {handoff["block"] for handoff in catalog()["handoffs"]}
        self.assertEqual(blocks, set(faults_judge.FAMILIES))

    def bad(self, change, message):
        cat = catalog()
        change(cat)
        with self.assertRaisesRegex(faults_judge.CatalogError, message):
            faults_judge.check_catalog(cat)

    def test_catalog_errors(self):
        self.bad(lambda c: c.update(schema_version=2), "schema_version")
        self.bad(lambda c: c.update(families=["eth", "bsc"]), "families")
        self.bad(lambda c: c["variants"].pop("crash"), "variants")
        self.bad(lambda c: c["states"].pop("pin"), "states")
        self.bad(lambda c: c["points"].append(copy.deepcopy(c["points"][0])), "duplicate point")
        self.bad(lambda c: c["points"][0].update(seam="other"), "unknown seam")
        self.bad(lambda c: c["points"][0].update(pending="yes"), "pending")
        self.bad(lambda c: c["points"][0].update(h_rule="maybe"), "h_rule")
        self.bad(lambda c: c["points"][0].update(source=""), "source")
        self.bad(lambda c: c["points"][0].update(ac="AC-SCP-03-01"), "ac must be a list")
        self.bad(lambda c: c["points"][0]["after_restart"].pop("crash"), "crash and power_loss")
        self.bad(lambda c: c["points"][0]["after_restart"]["crash"].pop("boundary"), "boundary expectation")
        self.bad(lambda c: c["points"][0]["after_restart"]["crash"].update(colour=["red"]), "unknown field")
        self.bad(lambda c: c["points"][0]["after_restart"]["crash"].update(mode=["SLEEP"]), "bad values")
        self.bad(lambda c: c["points"][0]["after_restart"]["crash"].update(mode=[]), "bad values")
        self.bad(lambda c: c["points"][1]["after_restart"]["crash"].update(boundary=["absent"]), "kept needs")
        self.bad(lambda c: c.update(points=[]), "no points")
        self.bad(lambda c: [p.update(ac=[]) for p in c["points"]], "AC-SCP-03-01")
        self.bad(lambda c: c["handoffs"].append(copy.deepcopy(c["handoffs"][0])), "duplicate handoff")
        self.bad(lambda c: c["handoffs"][0].update(block="arb"), "unknown block")
        self.bad(lambda c: c["handoffs"][0].update(item=""), "item and source")


class JudgeTests(unittest.TestCase):
    def setUp(self):
        self.catalog = catalog()

    def verdict(self, report):
        return faults_judge.judge(report, self.catalog)

    def test_perfect_report_passes_for_every_family(self):
        for family in faults_judge.FAMILIES:
            verdict = self.verdict(perfect_report(family, self.catalog))
            self.assertEqual(verdict["result"], "PASS", verdict["failures"])
            self.assertEqual(verdict["ac"], {"AC-SCP-03-01": "PASS"})
            self.assertEqual(len(verdict["cases"]), 30)

    def test_missing_case_is_incomplete(self):
        report = perfect_report()
        report["cases"] = [c for c in report["cases"] if c["point"] != "dec.after-decided-sync"]
        verdict = self.verdict(report)
        self.assertEqual(verdict["result"], "INCOMPLETE")
        self.assertEqual(verdict["cases"]["dec.after-decided-sync/crash"], "NOT_RUN")
        self.assertEqual(verdict["ac"], {"AC-SCP-03-01": "PASS"})

    def test_missing_h_case_leaves_the_ac_not_run(self):
        report = perfect_report()
        report["cases"] = [c for c in report["cases"]
                           if (c["point"], c["variant"]) != ("h.after-boundary-sync", "power_loss")]
        self.assertEqual(self.verdict(report)["ac"], {"AC-SCP-03-01": "NOT_RUN"})

    def test_missing_handoff_is_incomplete(self):
        report = perfect_report("poly")
        report["handoffs"] = report["handoffs"][1:]
        verdict = self.verdict(report)
        self.assertEqual(verdict["result"], "INCOMPLETE")
        self.assertIn("NOT_RUN", verdict["handoffs"].values())

    def failing(self, change, message, point="adm.before-admitted-sync", variant="power_loss",
                family="eth"):
        report = perfect_report(family, self.catalog)
        change(case_of(report, point, variant), report)
        verdict = self.verdict(report)
        self.assertEqual(verdict["result"], "FAIL")
        self.assertTrue(any(message in failure for failure in verdict["failures"]), verdict["failures"])
        return verdict

    def test_report_format_errors(self):
        report = perfect_report()
        report["kind"] = "other"
        self.assertEqual(self.verdict(report)["result"], "FAIL")
        report = perfect_report()
        report["family"] = "arb"
        self.assertEqual(self.verdict(report)["result"], "FAIL")
        self.failing(lambda c, r: r.update(storage_model="lazyfs"), "storage_model")
        self.failing(lambda c, r: c.update(point="adm.unknown"), "unknown point")
        self.failing(lambda c, r: c.update(variant="reboot"), "unknown point or variant")
        self.failing(lambda c, r: r["cases"].append(copy.deepcopy(c)), "reported twice")

    def test_fault_not_reached(self):
        self.failing(lambda c, r: c["fault"].update(hit=False), "not reached")
        self.failing(lambda c, r: c["fault"].update(block=-1), "not reached")

    def test_storage_model_rules(self):
        self.failing(lambda c, r: c["storage"].update(reset_to_synced=False), "reset the store")
        self.failing(lambda c, r: c["storage"].update(unsynced_dropped=0), "dropped no pending write")
        self.failing(lambda c, r: c["storage"].update(unsynced_dropped="1"), "must be a count")
        self.failing(lambda c, r: c["storage"].update(reset_to_synced=True), "keeps every write",
                     variant="crash")
        self.failing(lambda c, r: c["storage"].update(unsynced_dropped=2), "keeps every write",
                     variant="crash")

    def test_non_pending_point_needs_no_dropped_write(self):
        report = perfect_report()
        case_of(report, "adm.after-admitted-sync", "power_loss")["storage"]["unsynced_dropped"] = 0
        self.assertEqual(self.verdict(report)["result"], "PASS")

    def test_power_loss_state_must_be_the_synced_one(self):
        self.failing(lambda c, r: c["after_restart"].update(admission="admitted"), "admission admitted")

    def test_crash_may_keep_the_unsynced_write(self):
        report = perfect_report()
        case_of(report, "adm.before-admitted-sync", "crash")["after_restart"]["admission"] = "admitted"
        self.assertEqual(self.verdict(report)["result"], "PASS")

    def test_unknown_state_value(self):
        self.failing(lambda c, r: c["after_restart"].update(pin="lost"), "unknown pin")

    def test_mode_must_match_boundary(self):
        self.failing(lambda c, r: c["after_restart"].update(mode="BOOTSTRAP"), "does not match the boundary")
        self.failing(lambda c, r: c["after_restart"].update(mode="LIVE"), "does not match the boundary",
                     point="h.before-boundary-sync")

    def test_boundary_presence_after_restart(self):
        self.failing(lambda c, r: c["after_restart"].update(boundary=boundary(), mode="LIVE"),
                     "boundary present", point="h.before-boundary-sync")
        self.failing(lambda c, r: c["after_restart"].update(boundary=None, mode="BOOTSTRAP"),
                     "boundary absent", point="h.after-boundary-sync")

    def test_malformed_boundaries(self):
        self.failing(lambda c, r: c["after_restart"].update(boundary={"number": H}), "malformed boundary")
        self.failing(lambda c, r: c["final"].update(boundary={"number": H, "hash": "xy", "root": ROOT_H}),
                     "malformed boundary")
        self.failing(lambda c, r: c["final"].update(boundary=None), "no durable boundary")

    def test_initial_sync_must_not_be_exported(self):
        self.failing(lambda c, r: c["final"].update(exported=list(range(H, HEAD + 1))), "initial sync")

    def test_h_plus_one_must_not_be_lost(self):
        self.failing(lambda c, r: c["final"].update(exported=list(range(H + 2, HEAD + 1))), "loss window")

    def test_exported_blocks_are_exact(self):
        self.failing(lambda c, r: c["final"].update(exported=[H + 1, H + 2, H + 4]), "exactly H+1")
        self.failing(lambda c, r: c["final"].update(exported=["11"]), "list block numbers")

    def test_duplicates_and_corpus(self):
        self.failing(lambda c, r: c["final"].update(duplicates=1), "more than once")
        self.failing(lambda c, r: c["final"].update(corpus="FAIL"), "smoke corpus")

    def test_final_head_below_h(self):
        self.failing(lambda c, r: c["final"].update(head=H - 1), "final head below H")

    def test_h_must_be_kept_once_durable(self):
        self.failing(lambda c, r: c["final"].update(boundary=boundary(H + 1), exported=list(range(H + 2, HEAD + 1))),
                     "H changed")

    def test_new_h_must_not_be_below_the_fault(self):
        def change(case, report):
            case["fault"]["block"] = H + 1
        self.failing(change, "below the fault point", point="h.before-boundary-sync")

    def test_new_h_at_or_above_the_fault_passes(self):
        report = perfect_report()
        case = case_of(report, "h.before-boundary-sync", "power_loss")
        case["final"]["boundary"] = boundary(H + 1)
        case["final"]["exported"] = list(range(H + 2, HEAD + 1))
        self.assertEqual(self.verdict(report)["result"], "PASS", self.verdict(report)["failures"])

    def test_kept_h_must_be_the_fault_head(self):
        self.failing(lambda c, r: c["fault"].update(block=H - 1), "head of the fault",
                     point="h.after-boundary-sync")

    def test_live_fault_block(self):
        self.failing(lambda c, r: c["fault"].update(block=H), "not a live block")
        self.failing(lambda c, r: c["fault"].update(block=HEAD + 1), "not a live block")

    def test_handoff_rules(self):
        report = perfect_report("bsc")
        report["handoffs"].append({"id": "bor.golive-order", "verdict": "PASS", "evidence": "x"})
        self.assertIn("not a handoff of bsc", " ".join(self.verdict(report)["failures"]))
        report = perfect_report("bsc")
        report["handoffs"][0]["evidence"] = ""
        self.assertIn("needs a verdict", " ".join(self.verdict(report)["failures"]))
        report = perfect_report("bsc")
        report["handoffs"][0]["verdict"] = "FAIL"
        self.assertEqual(self.verdict(report)["result"], "FAIL")


class CommandTests(unittest.TestCase):
    def test_check_and_judge_commands(self):
        with contextlib.redirect_stdout(io.StringIO()):
            self.run_commands()

    def run_commands(self):
        self.assertEqual(faults_judge.main(["check"]), 0)
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "report.json"
            path.write_text(json.dumps(perfect_report("bsc")))
            out = Path(tmp) / "verdict.json"
            self.assertEqual(faults_judge.main(["judge", str(path), "--output", str(out)]), 0)
            self.assertEqual(json.loads(out.read_text())["result"], "PASS")
            bad = perfect_report("bsc")
            bad["cases"][0]["final"]["duplicates"] = 3
            path.write_text(json.dumps(bad))
            self.assertEqual(faults_judge.main(["judge", str(path)]), 1)


if __name__ == "__main__":
    unittest.main()
