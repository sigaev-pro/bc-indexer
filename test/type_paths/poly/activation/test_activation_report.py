"""Retained native positive and independently corrupted complete report controls."""

import base64
import copy
import gzip
import hashlib
import importlib.util
import json
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("activation_report_judge", HERE / "activation_judge.py")
j = importlib.util.module_from_spec(spec)
spec.loader.exec_module(j)


def edit_header(row, index, value):
    body = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(row["raw_block"])))
    body[0][index] = value
    row.update(raw_block=j.codec.rlp(body).hex(), hash="0x" + j.codec.keccak256(j.codec.rlp(body[0])).hex(),
               parent="0x" + body[0][0].hex(), root="0x" + body[0][3].hex(), number=j.integer(body[0][8]))


class ActivationNativeReport(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        vector = json.loads((HERE / "native_vector.json").read_text())
        raw = gzip.decompress(base64.b64decode(vector["data"]))
        if hashlib.sha256(raw).hexdigest() != "7aed710938f1167d02ca783eae2a3a05450fa18f9f9c28527b811ee55df2d945" or vector["sha256"] != hashlib.sha256(raw).hexdigest():
            raise ValueError("activation native vector identity differs")
        cls.native = json.loads(raw)

    def reject(self, change):
        value = copy.deepcopy(self.native)
        change(value)
        self.assertEqual(j.judge(value)["result"], "FAIL")

    def test_actual_native_profiles_refusals_and_reserved_limitation(self):
        result = j.judge(self.native)
        self.assertEqual(result, {"result": "PASS", "ordinary_preactivation_refusals": [1, 2, 4],
                                 "reserved127": "accepted_without_execution_no_activation_refusal_credit",
                                 "paired_boundary_crossing": "NOT_RUN", "public_profile_credit": False})

    def test_native_identity_scope_and_case_completeness(self):
        for field, value in (("native_source", "00" * 20), ("scope", "public"), ("public_profile_credit", True),
                             ("error", "native failed"), ("cases", [])):
            with self.subTest(field=field):
                self.reject(lambda x: x.update({field: value}))
        self.reject(lambda x: x["cases"][0].update(name="preLondon"))
        self.reject(lambda x: x["cases"][0].update(type=2))

    def test_actual_failed_capture_frontiers_and_exact_error_binding(self):
        for index in range(3):
            original = self.native["cases"][index]["attempt"]
            errors = ("unrelated: transaction type not supported", original["error"].replace("tx 0", "tx 1"),
                      original["error"].replace("[0x", "[0xab"))
            for error in errors:
                with self.subTest(case=index, error=error):
                    self.reject(lambda x: x["cases"][index]["attempt"].update(error=error, observer_errors=[error]))
            for field, value in (("head_before", original["block"]["hash"]), ("head_hash", original["block"]["hash"]),
                                 ("accepted", True), ("observer_errors", []), ("observed", [1]), ("keys", {}),
                                 ("decisions", []), ("decisions", {"arbitrary": "not JSON"}), ("admission", {"arbitrary": "not JSON"})):
                with self.subTest(case=index, field=field):
                    self.reject(lambda x: x["cases"][index]["attempt"].update({field: value}))
            self.reject(lambda x: x["cases"][index]["attempt"]["block"].update(receipts=[]))
            self.reject(lambda x: x["cases"][index]["attempt"]["admission"].update({"extractor-admission-v1/bor.run-1/counter": '{"next":1}'}))

    def test_malformed_native_collections_become_structured_failures(self):
        for value in (False, 0, "", {}, [None]):
            with self.subTest(receipts=value):
                self.reject(lambda x: x["cases"][3]["attempt"]["block"].update(receipts=value))
        self.reject(lambda x: x["cases"][0]["control"]["blocks"][0]["account_proofs"][j.prior.SENDER].update(address=0))
        for field in ("account_proofs", "state_after", "transaction_proofs", "receipt_proofs"):
            self.reject(lambda x: x["cases"][0]["control"]["blocks"][0].update({field: None}))

    def test_empty_commitment_scope_and_reserved_no_execution_are_bound(self):
        for index in range(4):
            self.reject(lambda x: edit_header(x["cases"][index]["attempt"]["block"], 9, (9999999).to_bytes(3, "big")))
        for field, value in (("accepted", False), ("classification", "refused"), ("error", "not supported")):
            self.reject(lambda x: x["cases"][3]["attempt"].update({field: value}))
        self.reject(lambda x: x["cases"][3]["attempt"]["block"].update(receipts=[{}]))
        self.reject(lambda x: x["cases"][3]["attempt"]["block"]["state_after"][j.prior.RECEIVER[2:]]["storage"].update({"0": "01" * 32}))
        self.reject(lambda x: x["cases"][3]["attempt"].update(head_hash=x["cases"][3]["control"]["blocks"][1]["hash"]))

    def test_genesis_and_control_authentication_cannot_be_skipped(self):
        for index in range(4):
            self.reject(lambda x: x["cases"][index]["control"].update(blocks=[]))
            self.reject(lambda x: x["cases"][index]["control"]["blocks"][0].update(root="0x" + "ab" * 32))
            self.reject(lambda x: x["cases"][index]["config"].update(blobSchedule={"cancun": {"target": 3}}))
        def genesis_tail(x):
            row = x["cases"][2]
            block = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(row["genesis_raw"])))
            block[0][17] = b"\x01"
            row["genesis_raw"] = j.codec.rlp(block).hex()
        self.reject(genesis_tail)


if __name__ == "__main__":
    unittest.main()
