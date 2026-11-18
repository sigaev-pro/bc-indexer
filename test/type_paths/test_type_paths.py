"""Controls for source coverage, evidence boundaries, and archive substitution."""

import copy
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

import verify


class InventoryTests(unittest.TestCase):
    def setUp(self):
        self.candidates = verify.load(verify.ROOT / ".project/bootstrap/components/candidate-lock.json")
        self.documents = {c: verify.load(verify.ROOT / f"adapters/{c}/type_mapping/paths.json")
                          for c in verify.CLIENTS}

    def check(self, client, document):
        verify.validate(document, client, self.candidates)

    def test_all_clients_and_seven_profiles(self):
        profiles = set()
        for client, document in self.documents.items():
            self.check(client, document)
            profiles.update(document["public_profiles"])
        self.assertEqual(len(profiles), 7)

    def test_each_omitted_body_type_is_rejected(self):
        for client, document in self.documents.items():
            for index in range(len(document["body_types"])):
                changed = copy.deepcopy(document)
                del changed["body_types"][index]
                with self.subTest(client=client, index=index), self.assertRaisesRegex(ValueError, "Missing native type"):
                    self.check(client, changed)

    def test_each_omitted_special_path_is_rejected(self):
        for client, document in self.documents.items():
            for index in range(len(document["special_paths"])):
                changed = copy.deepcopy(document)
                del changed["special_paths"][index]
                with self.subTest(client=client, index=index), self.assertRaisesRegex(ValueError, "Missing special path"):
                    self.check(client, changed)

    def test_each_public_profile_is_required(self):
        for client, document in self.documents.items():
            for profile in document["public_profiles"]:
                changed = copy.deepcopy(document)
                del changed["public_profiles"][profile]
                with self.subTest(profile=profile), self.assertRaisesRegex(ValueError, "profile set"):
                    self.check(client, changed)

    def test_source_cannot_claim_runtime_or_public_pass(self):
        for client, document in self.documents.items():
            changed = copy.deepcopy(document)
            changed["runtime"] = "PASS"
            with self.assertRaisesRegex(ValueError, "runtime credit"):
                self.check(client, changed)
            for profile in document["public_profiles"]:
                changed = copy.deepcopy(document)
                changed["public_profiles"][profile] = "PASS"
                with self.assertRaisesRegex(ValueError, "public credit"):
                    self.check(client, changed)

    def test_unknown_byte_never_falls_back_to_legacy(self):
        for client, document in self.documents.items():
            for value in range(256):
                if value in verify.expected_types(client):
                    self.assertEqual(verify.lookup(document, value)["native_type"], value)
                else:
                    with self.assertRaisesRegex(ValueError, "Unknown native type"):
                        verify.lookup(document, value)

    def test_non_integer_or_out_of_range_type_is_rejected(self):
        for value in (True, False, None, "0", 0.0, -1, 256, [], {}):
            with self.subTest(value=value), self.assertRaises(ValueError):
                verify.lookup(self.documents["geth"], value)

    def test_duplicate_native_type_is_rejected(self):
        document = self.documents["geth"]
        document["body_types"].append(copy.deepcopy(document["body_types"][0]))
        with self.assertRaisesRegex(ValueError, "Duplicate native type"):
            self.check("geth", document)

    def test_boolean_type_is_not_zero_or_one(self):
        document = self.documents["geth"]
        document["body_types"][0]["native_type"] = False
        with self.assertRaisesRegex(ValueError, "Unknown native type"):
            self.check("geth", document)

    def test_wrong_variant_and_missing_activation_are_rejected(self):
        for field, value in (("variant", "EvmLegacy"), ("activation", ""), ("path", "")):
            document = copy.deepcopy(self.documents["geth"])
            document["body_types"][1][field] = value
            with self.assertRaises(ValueError):
                self.check("geth", document)

    def test_source_revision_and_family_substitution_are_rejected(self):
        for field, value in (("source_commit", "0" * 40), ("family", "BSC"), ("client", "bor")):
            document = copy.deepcopy(self.documents["geth"])
            document[field] = value
            with self.assertRaises(ValueError):
                self.check("geth", document)

    def test_missing_native_source_file_is_rejected(self):
        document = self.documents["bor"]
        del document["files"]["core/types/tx_state_sync.go"]
        with self.assertRaisesRegex(ValueError, "file set mismatch"):
            self.check("bor", document)

    def test_schema_extra_field_and_invalid_digest_are_rejected(self):
        for field, value in (("schema_version", 2), ("schema_version", True), ("archive_sha256", "x" * 64),
                             ("invented_runtime_pass", True)):
            document = copy.deepcopy(self.documents["geth"])
            document[field] = value
            with self.assertRaises(ValueError):
                self.check("geth", document)

    def test_bsc_classification_is_not_a_new_wire_type(self):
        document = self.documents["bsc"]
        document["special_paths"][0]["native_type"] = 0
        with self.assertRaisesRegex(ValueError, "must not invent"):
            self.check("bsc", document)

    def test_legacy_bor_receipt_is_not_body_statesync(self):
        document = self.documents["bor"]
        document["special_paths"][0]["origin"] = "BODY_TRANSACTION"
        document["special_paths"][0]["native_type"] = 127
        with self.assertRaisesRegex(ValueError, "must not invent"):
            self.check("bor", document)

    def test_body_statesync_cannot_be_optional(self):
        document = self.documents["bor"]
        document["special_paths"][1]["owner"] = "E04-02.optional_scope"
        with self.assertRaisesRegex(ValueError, "owner mismatch"):
            self.check("bor", document)

    def test_eth_protocol_calls_do_not_invent_body_transactions(self):
        document = self.documents["geth"]
        document["special_paths"][0]["native_type"] = 0
        with self.assertRaisesRegex(ValueError, "must not invent"):
            self.check("geth", document)

    def test_duplicate_json_keys_rejected(self):
        with self.assertRaisesRegex(ValueError, "Duplicate JSON"):
            json.loads('{"runtime":"NOT_RUN","runtime":"PASS"}', object_pairs_hook=verify.unique_object)

    def test_cli_inventory_and_missing_archive_exit_codes(self):
        run = subprocess.run([sys.executable, str(Path(verify.__file__)), "check"], capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual(json.loads(run.stdout)["runtime"], "NOT_RUN")
        with tempfile.TemporaryDirectory() as directory:
            run = subprocess.run([sys.executable, str(Path(verify.__file__)), "source", "geth", "--archive",
                                  str(Path(directory) / "absent.tar")], capture_output=True, text=True)
        self.assertEqual(run.returncode, 1)
        self.assertEqual(json.loads(run.stdout)["result"], "FAIL")


class SourceTests(unittest.TestCase):
    """Synthetic archives test validation mechanics, never native behavior."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "source.tar"
        self.sources = {
            "core/types/transaction.go": b'LegacyTxType = 0x00\nreturn nil, ErrTxTypeNotSupported\n',
            "core/types/transaction_signing.go": b'func MakeSigner(config Config) Signer {}\n',
        }
        self.document = {"client": "geth", "source_commit": "a" * 40, "body_types": [{"native_type": 0}],
                         "public_profiles": {"ethereum-mainnet": "NOT_RUN"}}

    def archive(self, duplicate=False, link=False, omit=False):
        self.document["files"] = {name: hashlib.sha256(data).hexdigest() for name, data in self.sources.items()}
        with tarfile.open(self.path, "w") as archive:
            rows = list(self.sources.items())
            if omit:
                rows = rows[:-1]
            if duplicate:
                rows.append(rows[0])
            for name, data in rows:
                entry = tarfile.TarInfo(name)
                if link:
                    entry.type, entry.linkname = tarfile.SYMTYPE, "/etc/passwd"
                    archive.addfile(entry)
                else:
                    entry.size = len(data)
                    archive.addfile(entry, io.BytesIO(data))
        self.document["archive_sha256"] = hashlib.sha256(self.path.read_bytes()).hexdigest()

    def test_source_only_result(self):
        self.archive()
        result = verify.verify_source(self.document, self.path)
        self.assertEqual(result["result"], "SOURCE_VERIFIED")
        self.assertEqual(result["runtime"], "NOT_RUN")

    def test_replaced_archive_is_rejected_before_reading_members(self):
        self.archive()
        self.document["archive_sha256"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "archive digest"):
            verify.verify_source(self.document, self.path)

    def test_replaced_member_is_rejected(self):
        self.archive()
        self.document["files"]["core/types/transaction.go"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "file digest"):
            verify.verify_source(self.document, self.path)

    def test_duplicate_member_is_rejected(self):
        self.archive(duplicate=True)
        with self.assertRaisesRegex(ValueError, "Duplicate source"):
            verify.verify_source(self.document, self.path)

    def test_symlink_member_is_rejected(self):
        self.archive(link=True)
        with self.assertRaisesRegex(ValueError, "Invalid source"):
            verify.verify_source(self.document, self.path)

    def test_missing_member_is_rejected(self):
        self.archive(omit=True)
        with self.assertRaisesRegex(ValueError, "Missing source"):
            verify.verify_source(self.document, self.path)

    def test_unlisted_source_type_is_rejected(self):
        self.sources["core/types/transaction.go"] += b'OtherTxType = 0x05\n'
        self.archive()
        with self.assertRaisesRegex(ValueError, "type set differs"):
            verify.verify_source(self.document, self.path)

    def test_missing_decoder_rejection_is_rejected(self):
        self.sources["core/types/transaction.go"] = b'LegacyTxType = 0x00\n'
        self.archive()
        with self.assertRaisesRegex(ValueError, "decoder rejection"):
            verify.verify_source(self.document, self.path)

    def test_missing_block_signer_is_rejected(self):
        self.sources["core/types/transaction_signing.go"] = b'func LatestSigner() {}\n'
        self.archive()
        with self.assertRaisesRegex(ValueError, "block-specific signer"):
            verify.verify_source(self.document, self.path)


if __name__ == "__main__":
    unittest.main()
