"""Check trusted D87 oracle inputs, not a product schema implementation."""

import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import evidence_task_verifier as verifier
from test_initial_task import protocol_stub


class ReaderOracleTests(unittest.TestCase):
    def setUp(self):
        self.rows = verifier.vectors()
        self.reader = {row["id"]: row for row in self.rows if row["id"].startswith("reader-")}

    def test_preserves_the_accepted_original_pack(self):
        self.assertEqual(len(self.rows[:54]), 54)
        # Accepted BOOT-09 pack at 6a486bf, before adding D87 artifact cases.
        self.assertEqual(verifier.digest(self.rows[:54]), "b02e755d6e728366a742fc16fff5d64946cbfa6a3d3e1f8d0c1ddfb1a39fc27c")
        self.assertEqual(len({row["id"] for row in self.rows}), len(self.rows))

    def test_positive_controls_cover_seven_distinct_profiles(self):
        controls = [self.reader["reader-positive-" + profile] for profile in verifier.PROFILES]
        self.assertTrue(all(row["valid"] for row in controls))
        self.assertEqual({row["request"]["document"]["profile"] for row in controls}, set(verifier.PROFILES))
        for row in controls:
            document = row["request"]["document"]
            context = row["request"]["context"]
            self.assertEqual(document["network"], verifier.PROFILES[document["profile"]])
            self.assertEqual(set(document["conditions"]), set(verifier.reader_vectors.CONDITIONS))
            manifest = context["manifests"][document["run_manifest_path"]]
            self.assertEqual(manifest["case_identities"], document["case_identities"])
            self.assertEqual(manifest["case_registry_digest"], verifier.digest(context["registry"]))

    def test_mutations_do_not_accidentally_rely_on_stale_self_hashes(self):
        intentional = {"reader-wrong-boundary-output-hash", "reader-duplicate-boundary-output-reference"}
        for name, row in self.reader.items():
            document, context = row["request"]["document"], row["request"]["context"]
            manifest = context["manifests"].get(document["run_manifest_path"])
            if not document["run_started"] or manifest is None or name in intentional:
                continue
            with self.subTest(vector=name):
                content = verifier.canonical(document).decode()
                self.assertEqual(context["artifacts"][document["artifact_path"]], content)
                self.assertEqual(manifest["actual_artifacts"], [{"path": document["artifact_path"],
                    "sha256": hashlib.sha256(content.encode()).hexdigest()}])

    def test_not_run_control_has_no_fabricated_execution(self):
        row = self.reader["reader-not-run"]
        self.assertTrue(row["valid"])
        doc, ctx = row["request"]["document"], row["request"]["context"]
        self.assertFalse(doc["run_started"])
        self.assertEqual(ctx["manifests"], {})
        self.assertNotIn(doc["artifact_path"], ctx["artifacts"])
        self.assertTrue(all(condition["status"] == "NOT_RUN" for condition in doc["conditions"].values()))
        self.assertTrue(all(phase["started_at"] is None and phase["finished_at"] is None for phase in doc["phase_times"].values()))

    def test_reader_completion_is_separate_from_semantic_success(self):
        row = self.reader["reader-completed-reader-semantic-fail"]
        self.assertTrue(row["valid"])
        doc, ctx = row["request"]["document"], row["request"]["context"]
        self.assertTrue(doc["reader_complete"])
        self.assertFalse(doc["comparison_passed"])
        self.assertEqual(ctx["manifests"][doc["run_manifest_path"]]["case_outcomes"][0]["verdict"], "FAIL")
        self.assertFalse(self.reader["reader-semantic-failure-cannot-pass"]["valid"])

    def test_positive_incomplete_and_declared_negative_controls_differ(self):
        positive = self.reader["reader-incomplete-delivery-fail"]
        negative = self.reader["reader-declared-negative"]
        self.assertTrue(positive["valid"] and negative["valid"])
        self.assertFalse(self.reader["reader-incomplete-delivery-cannot-pass"]["valid"])
        self.assertFalse(self.reader["reader-negative-without-predicate"]["valid"])
        doc, ctx = negative["request"]["document"], negative["request"]["context"]
        ref = doc["expectation"]["predicate_ref"]
        self.assertEqual(ref["sha256"], hashlib.sha256(ctx["artifacts"][ref["path"]].encode()).hexdigest())
        self.assertEqual(doc["conditions"]["terminal_fence"]["status"], "FAILED")

    def test_expected_outcomes_stay_outside_candidate_requests(self):
        for row in self.reader.values():
            self.assertEqual(set(row["request"]), {"contract", "synthetic", "kind", "document", "context"})
            self.assertEqual(row["request"]["kind"], "ReaderBoundaryManifest")
        self.assertGreater(sum(not row["valid"] for row in self.reader.values()), 40)

    def test_reader_sources_are_part_of_the_frozen_pack(self):
        source = Path(__file__).resolve().parents[2]
        pack = verifier.pack(source)
        for filename in ("reader_evidence_vectors.py", "reader-evidence-contract.md"):
            path = ".project/automation/" + filename
            self.assertEqual(pack[path], hashlib.sha256((source / path).read_bytes()).hexdigest())

    def test_missing_and_wrong_version_reader_schema_fail_output_preflight(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            protocol_stub(root)
            verifier.validate_outputs(root)
            reader = root / "test/evidence/reader-boundary-manifest.schema.json"
            data = json.loads(reader.read_text())
            data["properties"]["schema_version"]["const"] = 3
            reader.write_text(json.dumps(data))
            with self.assertRaises(ValueError):
                verifier.validate_outputs(root)
            reader.unlink()
            with self.assertRaises(ValueError):
                verifier.validate_outputs(root)

    def test_bad_schema_cannot_omit_any_required_boundary_field(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            protocol_stub(root)
            reader = root / "test/evidence/reader-boundary-manifest.schema.json"
            schema = json.loads(reader.read_text())
            for field in verifier.reader_vectors.REQUIRED:
                bad = copy.deepcopy(schema)
                bad["required"].remove(field)
                reader.write_text(json.dumps(bad))
                with self.subTest(field=field), self.assertRaises(ValueError):
                    verifier.validate_outputs(root)

    def test_reader_schema_version_requires_integer_one(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            protocol_stub(root)
            reader = root / "test/evidence/reader-boundary-manifest.schema.json"
            schema = json.loads(reader.read_text())
            for value in (True, 1.0, "1", None):
                schema["properties"]["schema_version"]["const"] = value
                reader.write_text(json.dumps(schema))
                with self.subTest(value=value), self.assertRaises(ValueError):
                    verifier.validate_outputs(root)
            schema["properties"]["schema_version"]["const"] = 1
            reader.write_text(json.dumps(schema))
            self.assertIn("test/evidence/reader-boundary-manifest.schema.json", verifier.validate_outputs(root))

    def test_driver_rejects_boolean_reader_schema_version(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            protocol_stub(root)
            reader = root / "test/evidence/reader-boundary-manifest.schema.json"
            schema = json.loads(reader.read_text())
            schema["properties"]["schema_version"]["const"] = True
            reader.write_text(json.dumps(schema))
            result = subprocess.run([sys.executable, "-I", verifier.__file__, "--candidate", str(root)],
                                    capture_output=True, text=True, timeout=20)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("reader boundary schema", result.stdout)


if __name__ == "__main__":
    unittest.main()
