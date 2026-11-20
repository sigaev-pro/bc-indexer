"""Synthetic paired-matrix controls; no new native execution."""

import copy
import unittest

import test_crossing_report as reports

j = reports.j

PASS = {"result": "PASS", "private_activation_crossings": [1, 2, 4], "reserved127": "NOT_EVALUATED", "public_profile_credit": False}


def paired_report(native):
    # The paired producer reuses the unpaired generator with the paired Bor
    # gate scheduled at the crossing block. Its retained runtime is pending,
    # so these controls pair the retained unpaired payloads with the paired
    # identity and configuration only; block seals are not judged offline.
    report = copy.deepcopy(native)
    for row in report["cases"]:
        row["bor_activation"] = ""
    for name, bor_gate, origin in (("LondonJaipur", "jaipurBlock", 1), ("PragueBhilai", "bhilaiBlock", 2)):
        row = copy.deepcopy(report["cases"][origin])
        row.update(name=name, bor_activation=bor_gate)
        row["config"]["bor"][bor_gate] = 2
        report["cases"].append(row)
    return report


class CrossingPairedReports(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        reports.CrossingNativeReport.setUpClass()
        cls.paired = paired_report(reports.CrossingNativeReport.native)

    def reject(self, change):
        report = copy.deepcopy(self.paired)
        change(report)
        self.assertEqual(j.judge(report)["result"], "FAIL")

    def test_paired_matrix_passes(self):
        self.assertEqual(j.judge(self.paired), PASS)

    def test_paired_bor_gates(self):
        for index, gate in ((3, "jaipurBlock"), (4, "bhilaiBlock")):
            for value in (0, 3, 8, 2.0, True):
                with self.subTest(gate=gate, value=value):
                    self.reject(lambda r, i=index, g=gate, v=value: r["cases"][i]["config"]["bor"].update({g: v}))
            with self.subTest(gate=gate, missing=True):
                self.reject(lambda r, i=index, g=gate: r["cases"][i]["config"]["bor"].pop(g))
        for index, gate in ((3, "londonBlock"), (4, "pragueBlock")):
            with self.subTest(gate=gate):
                self.reject(lambda r, i=index, g=gate: r["cases"][i]["config"].update({g: 8}))

    def test_paired_bor_activation_fields(self):
        for value in ("", "delhiBlock", "bhilaiBlock", None, 2):
            with self.subTest(value=value):
                self.reject(lambda r, v=value: r["cases"][3].update(bor_activation=v))
        with self.subTest(missing=True):
            self.reject(lambda r: r["cases"][3].pop("bor_activation"))
        with self.subTest(unpaired_index=0):
            self.reject(lambda r: r["cases"][0].update(bor_activation="jaipurBlock"))
        for index in range(3):
            with self.subTest(unpaired_index=index):
                self.reject(lambda r, i=index: r["cases"][i].update(bor_activation="delhiBlock"))

    def test_paired_case_count_and_order(self):
        for change in (lambda r: r["cases"].pop(), lambda r: r["cases"].pop(0),
                       lambda r: r["cases"].append(copy.deepcopy(r["cases"][4]))):
            with self.subTest(change=change):
                self.reject(change)

        def swap(r):
            r["cases"][3], r["cases"][4] = r["cases"][4], r["cases"][3]
        with self.subTest(order=True):
            self.reject(swap)

    def test_unpaired_rows_reject_bor_activation(self):
        value = copy.deepcopy(reports.CrossingNativeReport.native)
        for row in value["cases"]:
            row["bor_activation"] = ""
        self.assertEqual(j.judge(value)["result"], "FAIL")

    def test_paired_blocks_keep_kind_semantics(self):
        for index, field, value in ((3, 9, 10_000_000), (3, 15, 999_999_999), (4, 15, 879_152_343)):
            def change(r, i=index, f=field, v=value):
                row = r["cases"][i]
                reports.edit_header(row["crossing"]["blocks"][1], f, reports.vectors.num(v))
                reports.relink(row)
            with self.subTest(index=index, field=field):
                self.reject(change)


if __name__ == "__main__":
    unittest.main()
