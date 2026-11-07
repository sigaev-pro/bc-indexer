"""Independent raw-input numeric and RFC 3339 controls; no runtime proof."""

import json
from fractions import Fraction
from pathlib import Path
import subprocess
import sys
import unittest

import test_evidence as fixtures
import test_reader_boundary as reader_fixtures


ROOT = Path(__file__).resolve().parent


class InputBoundaryTests(unittest.TestCase):
    def call(self, raw):
        return subprocess.run([sys.executable, "-I", str(ROOT / "validate.py")],
                              input=raw.encode("utf-8"), capture_output=True, cwd="/tmp", timeout=5)

    def manifest(self):
        context = fixtures.make_context()
        return fixtures.bundle("RunManifest", fixtures.make_manifest(context), context)

    def test_raw_integer_tokens_cannot_be_rounded_into_valid_evidence(self):
        cases = [
            (json.dumps(fixtures.result_bundle()), '"schema_version": 3', '"schema_version": 3.0000000000000001'),
            (json.dumps(self.manifest()), '"seed": 193', '"seed": 1e-400'),
        ]
        for control, before, after in cases:
            with self.subTest(token=after):
                self.assertEqual(self.call(control).returncode, 0)
                result = self.call(control.replace(before, after, 1))
                self.assertEqual(result.returncode, 2, result.stderr)

    def test_raw_offset_minutes_cannot_be_normalized(self):
        control = json.dumps(self.manifest())
        self.assertEqual(self.call(control).returncode, 0)
        result = self.call(control.replace("+02:00", "+01:60"))
        self.assertEqual(result.returncode, 2, result.stderr)

    def test_raw_duration_and_deadline_cannot_lose_fractional_meaning(self):
        request = self.manifest()
        request["document"]["phases"]["preparation"].update(duration_seconds=10, deadline_seconds=21)
        request["document"]["phases"]["execution"]["deadline_seconds"] = 20
        control = json.dumps(request)
        self.assertEqual(self.call(control).returncode, 0)
        for before, after in (('"duration_seconds": 10', '"duration_seconds": 10.0000000000000001'),
                              ('"deadline_seconds": 20', '"deadline_seconds": 19.9999999999999999')):
            with self.subTest(token=after):
                result = self.call(control.replace(before, after, 1))
                self.assertEqual(result.returncode, 2, result.stderr)

    def test_supported_integral_spellings_and_ordinary_fractions_remain_valid(self):
        control = json.dumps(fixtures.result_bundle())
        for token in ("3.0", "3.000", "3e0", "30e-1"):
            with self.subTest(token=token):
                result = self.call(control.replace('"schema_version": 3', '"schema_version": ' + token, 1))
                self.assertEqual(result.returncode, 0, result.stderr)
        control = json.dumps(self.manifest())
        for token in ("193.0", "1.93e2", "0e-400", "1e23", "9007199254740992.0"):
            with self.subTest(seed=token):
                result = self.call(control.replace('"seed": 193', '"seed": ' + token, 1))
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_numeric_precision_and_range_rejections_are_general_and_private(self):
        control = json.dumps(self.manifest())
        for token in ("193.0000000000000001", "9007199254740993.0", "1e-400", "-1e-400",
                      "1e400", "-1e400", "0.123456789012345678901", "1e9999999999999999999"):
            with self.subTest(token=token):
                result = self.call(control.replace('"seed": 193', '"seed": ' + token, 1))
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertEqual(result.stdout, b"")
                self.assertLess(len(result.stderr), 1024)
                self.assertNotIn(token.encode(), result.stderr)
                self.assertNotIn(b"Traceback", result.stderr)

    def test_exact_numeric_equality_bounds_and_uniqueness_do_not_use_binary_values(self):
        validator = fixtures.validator
        schemas = validator.SchemaValidator()
        value = validator.parse_json("1e23")
        integer = 10 ** 23
        schemas.check(value, {"type": "integer", "const": integer, "minimum": integer, "maximum": integer},
                      "evidence-definitions.schema.json", "value")
        for schema in ({"exclusiveMinimum": integer}, {"exclusiveMaximum": integer}):
            with self.subTest(schema=schema), self.assertRaises(validator.InvalidEvidence):
                schemas.check(value, schema, "evidence-definitions.schema.json", "value")
        with self.assertRaises(validator.InvalidEvidence):
            schemas.check(validator.parse_json("[100000000000000000000000,1e23]"), {"uniqueItems": True},
                          "evidence-definitions.schema.json", "value")
        self.assertTrue(validator.json_equal(value, integer))
        self.assertFalse(validator.json_equal(True, validator.parse_json("1.0")))

    def test_d91_canonical_bytes_and_hashes_remain_unchanged_for_supported_numbers(self):
        raw = '{"z":[3.0,0.1,1.2300,1e23,1e-323,-0.0,193],"text":"caf\\u00e9"}'
        expected = json.dumps(json.loads(raw), sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False) + "\n"
        parsed = fixtures.validator.parse_json(raw)
        self.assertEqual(fixtures.validator.canonical(parsed), expected.encode("ascii"))
        self.assertEqual(fixtures.validator.digest(parsed), fixtures.sha(expected))

    def fractional_manifest(self):
        request = self.manifest()
        document = request["document"]
        document.update(started_at="2024-02-29T00:00:00Z", finished_at="2024-02-29T00:00:00.4Z")
        for name, start, end, duration in (("preparation", "0", "0.1", 0.1),
                                           ("execution", "0.1", "0.3", 0.2),
                                           ("teardown", "0.3", "0.4", 0.1)):
            document["phases"][name] = {"started_at": "2024-02-29T00:00:0" + start + "Z",
                                         "finished_at": "2024-02-29T00:00:0" + end + "Z",
                                         "deadline_seconds": duration, "duration_seconds": duration}
        return request

    def test_tight_fractional_timing_uses_decimal_value_not_fraction_of_float(self):
        control = json.dumps(self.fractional_manifest())
        self.assertEqual(self.call(control).returncode, 0)
        for before, after in (('"deadline_seconds": 0.1', '"deadline_seconds": 0.09999999999999999'),
                              ('"duration_seconds": 0.1', '"duration_seconds": 0.10000000000000002')):
            with self.subTest(token=after):
                result = self.call(control.replace(before, after, 1))
                self.assertEqual(result.returncode, 2, result.stderr)
        self.assertEqual(fixtures.validator.exact_number(0.1), Fraction(1, 10))
        self.assertEqual(fixtures.validator.exact_number(fixtures.validator.parse_json("0.1")), Fraction(1, 10))

    def test_equivalent_offsets_and_fractional_timestamps_remain_valid(self):
        request = self.fractional_manifest()
        for start, finish in (("2024-02-29T02:00:00+02:00", "2024-02-29T02:00:00.400000000+02:00"),
                              ("2024-02-28T18:30:00-05:30", "2024-02-28T18:30:00.4-05:30"),
                              ("2024-02-29T23:59:00+23:59", "2024-02-29T23:59:00.4+23:59")):
            with self.subTest(offset=start):
                request["document"].update(started_at=start, finished_at=finish)
                result = self.call(json.dumps(request))
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_timestamp_components_are_validated_before_normalization(self):
        control = json.dumps(self.manifest())
        for offset in ("+01:60", "+00:99", "-01:60", "+24:00", "-24:00", "+99:00"):
            with self.subTest(offset=offset):
                self.assertEqual(self.call(control.replace("+02:00", offset)).returncode, 2)
        for timestamp in ("2024-02-30T02:00:00+02:00", "2024-13-01T02:00:00+02:00",
                          "2024-02-29T24:00:00+02:00", "2024-02-29T02:60:00+02:00", "2024-02-29T02:00:60+02:00"):
            with self.subTest(timestamp=timestamp):
                changed = self.manifest()
                changed["document"]["started_at"] = timestamp
                self.assertEqual(self.call(json.dumps(changed)).returncode, 2)

    def test_reader_fractional_deadline_uses_the_same_exact_numeric_boundary(self):
        request = reader_fixtures.reader_bundle()
        for phase in request["document"]["phase_times"].values():
            phase.update(started_at="2024-02-29T00:00:11Z", finished_at="2024-02-29T00:00:11.1Z", deadline_seconds=0.1)
        reader_fixtures.reseal(request)
        self.assertEqual(self.call(json.dumps(request)).returncode, 0)
        request["document"]["phase_times"]["comparison"]["deadline_seconds"] = 0.09999999999999999
        reader_fixtures.reseal(request)
        result = self.call(json.dumps(request))
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn(b"phase deadline", result.stderr)


if __name__ == "__main__":
    unittest.main()
