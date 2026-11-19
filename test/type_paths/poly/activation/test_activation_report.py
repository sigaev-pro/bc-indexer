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
_spec = importlib.util.spec_from_file_location("activation_report_vectors", HERE.parent / "test_poly_judge.py")
vectors = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(vectors)


def edit_header(row, index, value):
    body = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(row["raw_block"])))
    body[0][index] = value
    row.update(raw_block=j.codec.rlp(body).hex(), hash="0x" + j.codec.keccak256(j.codec.rlp(body[0])).hex(),
               parent="0x" + body[0][0].hex(), root="0x" + body[0][3].hex(), number=j.integer(body[0][8]))


def relink(case):
    """Rebind correlated header/receipt/decision claims after a deliberate edit."""
    first, empty = case["control"]["blocks"]
    candidate = case["attempt"]["block"]
    genesis = j.codec.rlp_list(bytes.fromhex(case["genesis_raw"]))[0][1]
    edit_header(first, 0, j.codec.keccak256(genesis))
    for row in (empty, candidate):
        edit_header(row, 0, bytes.fromhex(first["hash"][2:]))
    for row in (first, empty, candidate):
        for receipt in row.get("receipts") or []:
            receipt["blockHash"] = row["hash"]
            receipt["blockNumber"] = hex(row["number"])
            for log in receipt["logs"]:
                log["blockHash"] = row["hash"]
                log["blockNumber"] = hex(row["number"])
    case["attempt"]["head_before"] = first["hash"]
    for record, views in ((case["control"], [first, empty]), (case["attempt"], [first, candidate] if case["attempt"]["accepted"] else [first])):
        record["head_hash"] = views[-1]["hash"]
        for sequence, row in enumerate(views):
            prefix = f"extractor-decision-v1/bor.run-1/{sequence:016x}"
            intent = json.loads(record["decisions"][prefix + "/intent"])
            decided = json.loads(record["decisions"][prefix + "/decided"])
            intent["old_head"] = {"number": row["number"] - 1, "hash": row["parent"][2:]}
            intent["new_head"] = {"number": row["number"], "hash": row["hash"][2:]}
            intent["members"][0].update(block=row["hash"][2:], child_root=row["root"][2:])
            intent["digest"] = j.prior.intent_digest(intent)
            decided["intent_digest"] = intent["digest"]
            decided["digest"] = j.prior.decided_digest(intent["digest"], "BATCH")
            record["decisions"][prefix + "/intent"] = json.dumps(intent)
            record["decisions"][prefix + "/decided"] = json.dumps(decided)


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

    def test_correlated_genesis_gas_and_basefee_edits_are_rejected(self):
        for mode in ("genesis_tail", "genesis_number", "gas_limit", "basefee"):
            def change(x):
                row = x["cases"][2]
                genesis = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(row["genesis_raw"])))
                if mode == "genesis_tail":
                    genesis[0][17] = b"\x01"
                elif mode == "genesis_number":
                    genesis[0][8] = b"\x01"
                elif mode == "gas_limit":
                    genesis[0][9] = (9999999).to_bytes(3, "big")
                    for block in row["control"]["blocks"] + [row["attempt"]["block"]]:
                        edit_header(block, 9, genesis[0][9])
                else:
                    for block in (row["control"]["blocks"][1], row["attempt"]["block"]):
                        edit_header(block, 15, b"\x01")
                row["genesis_raw"] = j.codec.rlp(genesis).hex()
                relink(row)
            with self.subTest(mode=mode):
                self.reject(change)

    def test_correlated_reserved_body_and_extra_candidate_are_rejected(self):
        for mode in ("prefix", "event", "extra_candidate"):
            def change(x):
                row = x["cases"][3 if mode != "extra_candidate" else 0]
                block = row["attempt"]["block"]
                raw = bytes.fromhex(block["transaction_proofs"][0]["value"])
                if mode == "prefix":
                    raw = b"\x7e" + raw[1:]
                elif mode == "event":
                    fields = j.prior.plain_rlp(j.codec.rlp_list(raw[1:]))
                    fields[0][0] = b"\x02"
                    raw = b"\x7f" + j.codec.rlp(fields)
                vectors.bind_body(block, [raw, raw] if mode == "extra_candidate" else [raw])
                relink(row)
            with self.subTest(mode=mode):
                self.reject(change)

    def test_authenticated_empty_control_cannot_change_global_state(self):
        def change(x):
            row = x["cases"][0]
            alternate = x["cases"][2]["control"]["blocks"][0]
            empty = row["control"]["blocks"][1]
            empty["account_proofs"] = copy.deepcopy(alternate["account_proofs"])
            empty["state_after"] = copy.deepcopy(alternate["state_after"])
            for block in (empty, row["attempt"]["block"]):
                edit_header(block, 3, bytes.fromhex(alternate["root"][2:]))
            relink(row)
        self.reject(change)

    def test_extra_legacy_transaction_cannot_hide_behind_valid_first_receipt(self):
        def change(x):
            row = x["cases"][0]
            first = row["control"]["blocks"][0]
            tx = bytes.fromhex(first["transaction_proofs"][0]["value"])
            vectors.bind_body(first, [tx, tx])
            relink(row)
        self.reject(change)

    def test_other_authenticated_native_state_is_not_the_control_outcome(self):
        alternate = json.loads((HERE / "alternate_state.json").read_text())["state"]
        # This proof is valid, but its sender nonce/balance and recipient amount
        # follow eight actual transactions from accepted PR189, not one here.
        j.prior.account_claims(alternate, bytes.fromhex(alternate["root"][2:]))
        def change(x):
            row = x["cases"][0]
            for block in row["control"]["blocks"]:
                block["account_proofs"] = copy.deepcopy(alternate["account_proofs"])
                block["state_after"] = copy.deepcopy(alternate["state_after"])
                edit_header(block, 3, bytes.fromhex(alternate["root"][2:]))
            edit_header(row["attempt"]["block"], 3, bytes.fromhex(alternate["root"][2:]))
            relink(row)
        self.reject(change)


if __name__ == "__main__":
    unittest.main()
