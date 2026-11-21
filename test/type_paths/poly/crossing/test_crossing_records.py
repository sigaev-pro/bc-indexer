"""Independent corruptions of retained native decision records."""

import copy
import json
import unittest

import test_crossing_report as reports

j = reports.j


class CrossingRecordControls(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        reports.CrossingNativeReport.setUpClass()
        cls.row = reports.CrossingNativeReport.native["cases"][0]["crossing"]

    def verify(self, row):
        j.h.check_records(row["decisions"], row["admission"], row["blocks"], 3, observation_offset=1)

    def corrupt(self, change, refresh=True):
        row = copy.deepcopy(self.row)
        key = "extractor-decision-v1/bor.run-1/0000000000000000"
        intent = json.loads(row["decisions"][key + "/intent"])
        decided = json.loads(row["decisions"][key + "/decided"])
        change(intent, decided)
        if refresh:
            intent["digest"] = j.prior.intent_digest(intent)
            decided["intent_digest"] = intent["digest"]
            decided["digest"] = j.prior.decided_digest(intent["digest"], "BATCH")
        row["decisions"][key + "/intent"] = json.dumps(intent)
        row["decisions"][key + "/decided"] = json.dumps(decided)
        with self.assertRaises((ValueError, TypeError, KeyError)):
            self.verify(row)

    def test_native_records(self):
        self.verify(self.row)

    def test_semantic_intent_corruptions_with_recomputed_digests(self):
        for field, value in (("native_store_id", "ab" * 16), ("producer", "bor.other"),
                             ("sequence", 1), ("kind", "CANONICAL"), ("events", False)):
            with self.subTest(field=field, value=value):
                self.corrupt(lambda intent, decided: intent.update({field: value}))
        self.corrupt(lambda intent, decided: intent.update(extra=0))
        for field in ("old_head", "new_head"):
            self.corrupt(lambda intent, decided: intent[field].update(hash="ab" * 32))
        self.corrupt(lambda intent, decided: intent["members"].append(copy.deepcopy(intent["members"][0])))
        self.corrupt(lambda intent, decided: intent["members"][0].update(extra=0))
        for field, value in (("observation", "bor.run-1.0000000000000009"), ("block", "ab" * 32), ("child_root", "ab" * 32)):
            with self.subTest(member=field):
                self.corrupt(lambda intent, decided: intent["members"][0].update({field: value}))

    def test_digest_and_decided_corruptions(self):
        self.corrupt(lambda intent, decided: intent.update(sequence=False), refresh=False)
        self.corrupt(lambda intent, decided: intent.update(digest="ab" * 32), refresh=False)
        for field, value in (("extra", 0), ("sequence", 1), ("sequence", False), ("intent_digest", "ab" * 32),
                             ("delivery", "REPLAY"), ("digest", "ab" * 32)):
            with self.subTest(field=field, value=value):
                self.corrupt(lambda intent, decided: decided.update({field: value}), refresh=False)
        def correlated(intent, decided):
            intent["digest"] = "ab" * 32
            decided["intent_digest"] = intent["digest"]
            decided["digest"] = j.prior.decided_digest(intent["digest"], "BATCH")
        self.corrupt(correlated, refresh=False)

    def test_extra_records_and_counter(self):
        for key, value in (("unexpected", "{}"), ("extractor-decision-v1/bor.run-1/counter", '{"next":3}')):
            row = copy.deepcopy(self.row)
            row["decisions"][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.verify(row)

    def test_durable_header_numeric_domains(self):
        for field in ("old_head", "new_head"):
            row = copy.deepcopy(self.row)
            key = "extractor-decision-v1/bor.run-1/0000000000000000/intent"
            intent = json.loads(row["decisions"][key])
            intent[field]["number"] = float(intent[field]["number"])
            row["decisions"][key] = json.dumps(intent)
            with self.subTest(field=field), self.assertRaises(ValueError):
                j.records(row, row["blocks"], row["head_hash"], 3, row["observer_errors"][0])


if __name__ == "__main__":
    unittest.main()
