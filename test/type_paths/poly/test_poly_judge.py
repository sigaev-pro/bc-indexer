"""Independent synthetic corruption controls; these confer no native credit."""

import copy
import base64
import gzip
import hashlib
import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("type_paths_poly_judge", HERE / "poly_judge.py")
judge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(judge)
c = judge.codec


def num(value):
    return value.to_bytes((value.bit_length() + 7) // 8, "big")


def leaf(key, value):
    return c.rlp([b"\x20" + key, value])


def account_vector():
    address = judge.SENDER
    storage = leaf(c.keccak256(bytes(32)), c.rlp(b"\x09"))
    storage_root = c.keccak256(storage)
    encoded = c.rlp([b"\x02", b"\x64", storage_root, c.keccak256(b"")])
    account = leaf(c.keccak256(bytes.fromhex(address[2:])), encoded)
    proof = {"address": address, "nonce": "0x2", "balance": "0x64", "codeHash": "0x" + c.keccak256(b"").hex(),
             "storageHash": "0x" + storage_root.hex(), "accountProof": ["0x" + account.hex()],
             "storageProof": [{"key": hex(i), "value": hex(9 if i == 0 else 0), "proof": ["0x" + storage.hex()]} for i in range(4)]}
    row = {"account_proofs": {address: proof}, "state_after": {address[2:]: {
        "nonce": 2, "balance": "100", "code": "", "storage": {str(i): f"{9 if i == 0 else 0:064x}" for i in range(4)}}}}
    return row, c.keccak256(account)


def receipt_vector():
    tx = b"\x7f\xc0"
    tx_hash = "0x" + c.keccak256(tx).hex()
    block_hash = "0x" + "11" * 32
    wire = b"\x7f" + c.rlp([b"\x01", b"", bytes(256), []])
    key = c.rlp(b"")
    node = leaf(key, wire)
    receipt = {"type": "0x7f", "status": "0x1", "cumulativeGasUsed": "0x0", "gasUsed": "0x0",
               "logsBloom": "0x" + bytes(256).hex(), "logs": [], "transactionHash": tx_hash,
               "blockHash": block_hash, "blockNumber": "0x1", "transactionIndex": "0x0"}
    row = {"number": 1, "hash": block_hash, "receipts": [receipt],
           "receipt_proofs": [{"index": 0, "key": key.hex(), "value": wire.hex(), "nodes": [node.hex()]}],
           "transactions": [{"hash": tx_hash, "type": 127, "raw": tx.hex(), "receipt": copy.deepcopy(receipt)}]}
    header = [b""] * 16
    header[5] = c.keccak256(node)
    return row, header, [tx]


def block_vector():
    tx = b"\x7f\xc0"
    key = c.rlp(b"")
    node = leaf(key, tx)
    header = [bytes(32)] * 16
    header[4], header[8] = c.keccak256(node), b"\x01"
    raw = c.rlp([header, [tx], []])
    return {"number": 1, "hash": "0x" + c.keccak256(c.rlp(header)).hex(), "parent": "0x" + header[0].hex(),
            "root": "0x" + header[3].hex(), "raw_block": raw.hex(),
            "transaction_proofs": [{"index": 0, "key": key.hex(), "value": tx.hex(), "nodes": [node.hex()]}]}


def tx_wire(kind, index):
    to = bytes.fromhex((judge.SUCCESS if index % 2 == 0 else judge.REVERT)[2:])
    if kind == 0:
        fields = [num(index), num(5_000_000_000), num(150000), to, b"\x07", b"", num(30037), b"\x01", b"\x01"]
    elif kind == 1:
        fields = [num(15001), num(index), num(5_000_000_000), num(150000), to, b"\x07", b"",
                  [[bytes.fromhex(judge.REVERT[2:]), [bytes(32)]]], b"", b"\x01", b"\x01"]
    else:
        fields = [num(15001), num(index), num(2_000_000_000), num(5_000_000_000), num(150000), to, b"\x07", b"", []]
        if kind == 4:
            fields.append([[num(15001), bytes.fromhex(judge.SUCCESS[2:]), num(index - 6), b"", b"\x01", b"\x01"]])
        fields += [b"", b"\x01", b"\x01"]
    return (bytes([kind]) if kind else b"") + c.rlp(fields)


class PolygonJudgeProofTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        vector = json.loads((HERE / "semantic_vector.json").read_text())
        data = gzip.decompress(base64.b64decode(vector["data"]))
        if hashlib.sha256(data).hexdigest() != vector["sha256"]:
            raise ValueError("retained native vector digest differs")
        cls.native = json.loads(data)

    def test_retained_native_diagnostic_still_cannot_pass_complete_judge(self):
        self.assertEqual(judge.judge(self.native)["result"], "FAIL")

    def test_actual_native_ordinary_semantics_and_corruptions(self):
        row = copy.deepcopy(self.native["blocks"][0])
        outer = c.rlp_list(bytes.fromhex(row["raw_block"]))
        header = c.header_fields(outer[0][1])
        txs = [raw if isinstance(value, list) else value for value, raw in outer[1][0]]
        judge.ordinary_state(row, header, txs)
        for index in range(8):
            for target in ("sender_nonce", "sender_balance", "recipient_balance", "status", "price", "call", "continuity"):
                bad = copy.deepcopy(row)
                tx = bad["transactions"][index]
                if target == "sender_nonce": tx["after"][judge.SENDER]["nonce"] += 1
                elif target == "sender_balance": tx["after"][judge.SENDER]["balance"] = "0"
                elif target == "recipient_balance": tx["after"][judge.SUCCESS]["balance"] = "1000"
                elif target == "status": bad["receipts"][index]["status"] = hex(index % 2)
                elif target == "price": bad["receipts"][index]["effectiveGasPrice"] = "0x1"
                elif target == "call": bad["calls"][index]["to"] = judge.RECEIVER
                else: tx["before"][judge.SENDER]["nonce"] += 1
                with self.subTest(index=index, target=target), self.assertRaises(ValueError):
                    judge.ordinary_state(bad, header, txs)

    def test_actual_native_system_semantics_and_corruptions(self):
        row = copy.deepcopy(self.native["blocks"][3])
        txs = [bytes.fromhex(row["transactions"][0]["raw"])]
        parent = judge.snapshot(self.native["blocks"][2]["state_after"], False)
        judge.system_state(row, txs, parent)
        changes = [lambda x: x["transactions"][0].update({"from": judge.SENDER}),
                   lambda x: x["receipts"][0].update(status="0x0"),
                   lambda x: x["receipts"][0].update(gasUsed="0x1"),
                   lambda x: x["receipts"][0].update(effectiveGasPrice="0x1"),
                   lambda x: x["receipts"][0]["logs"].clear(),
                   lambda x: x["transactions"][0]["after"][judge.SENDER].update(nonce=9)]
        for index in range(5):
            changes += [lambda x, i=index: x["calls"][i].update(depth=99),
                        lambda x, i=index: x["calls"][i].update(transaction="0x" + "11" * 32),
                        lambda x, i=index: x["calls"][i].update(reverted=not x["calls"][i]["reverted"])]
        for index in (1, 2, 3, 4):
            changes += [lambda x, i=index: x["calls"][i].update(input="00"),
                        lambda x, i=index: x["calls"][i].update({"from": judge.SUCCESS}),
                        lambda x, i=index: x["calls"][i]["before"][judge.RECEIVER]["storage"].update({"0x" + "00" * 32: "0x09"}),
                        lambda x, i=index: x["calls"][i]["after"][judge.TARGET]["storage"].update({"0x" + "00" * 32: "0x09"})]
        for index, change in enumerate(changes):
            bad = copy.deepcopy(row)
            change(bad)
            with self.subTest(change=index), self.assertRaises(ValueError):
                judge.system_state(bad, txs, parent)

    def test_actual_native_decisions_and_correlated_views(self):
        r = self.native
        judge.check_records(r["decisions"], r["admission"], r["blocks"])
        for field in ("native_store_id", "producer", "sequence", "kind", "old_head", "new_head", "members", "digest"):
            records = copy.deepcopy(r["decisions"])
            key = "extractor-decision-v1/bor.run-1/0000000000000000/intent"
            intent = json.loads(records[key])
            intent[field] = None
            records[key] = json.dumps(intent)
            with self.subTest(field=field), self.assertRaises((ValueError, TypeError, KeyError)):
                judge.check_records(records, r["admission"], r["blocks"])
        with self.assertRaises(ValueError):
            judge.check_records({k: "not JSON" for k in r["decisions"]}, r["admission"], r["blocks"])
        with self.assertRaises(ValueError):
            judge.check_records(r["decisions"], {"arbitrary": "\"not an admission record\""}, r["blocks"])

    def test_account_and_storage_claims_bind_to_header_state_root(self):
        row, root = account_vector()
        with patch.object(judge, "WATCHED", {judge.SENDER}):
            judge.account_claims(row, root)
            changes = [lambda x: x["state_after"][judge.SENDER[2:]].update(balance="101"),
                       lambda x: x["state_after"][judge.SENDER[2:]].update(nonce=3),
                       lambda x: x["state_after"][judge.SENDER[2:]].update(code="01"),
                       lambda x: x["account_proofs"][judge.SENDER].update(balance="0x65"),
                       lambda x: x["account_proofs"][judge.SENDER].update(storageHash="0x" + "00" * 32),
                       lambda x: x["account_proofs"][judge.SENDER].update(codeHash="0x" + "00" * 32),
                       lambda x: x["account_proofs"][judge.SENDER].update(accountProof=[]),
                       lambda x: x["account_proofs"][judge.SENDER]["storageProof"][0].update(value="0xa"),
                       lambda x: x["account_proofs"][judge.SENDER]["storageProof"][0].update(proof=[]),
                       lambda x: x["account_proofs"][judge.SENDER]["storageProof"].append(copy.deepcopy(x["account_proofs"][judge.SENDER]["storageProof"][0]))]
            for change in changes:
                bad = copy.deepcopy(row)
                change(bad)
                with self.subTest(change=change), self.assertRaises(ValueError):
                    judge.account_claims(bad, root)

    def test_receipt_wire_and_metadata_are_both_bound(self):
        row, header, txs = receipt_vector()
        judge.receipt_claims(row, header, txs)
        for field, value in {"status": "0x0", "type": "0x2", "gasUsed": "0x1", "cumulativeGasUsed": "0x1",
                             "logsBloom": "0x" + "ff" * 256, "transactionHash": "0x" + "33" * 32,
                             "blockHash": "0x" + "44" * 32, "blockNumber": "0x2", "transactionIndex": "0x1"}.items():
            bad = copy.deepcopy(row)
            bad["receipts"][0][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                judge.receipt_claims(bad, header, txs)
        for field, value in {"index": 1, "key": "01", "value": "7fc0", "nodes": []}.items():
            bad = copy.deepcopy(row)
            bad["receipt_proofs"][0][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                judge.receipt_claims(bad, header, txs)

    def test_correlated_receipt_and_proof_substitution_cannot_keep_header(self):
        row, header, txs = receipt_vector()
        wire = b"\x7f" + c.rlp([b"", b"", bytes(256), []])
        row["receipts"][0]["status"] = row["transactions"][0]["receipt"]["status"] = "0x0"
        row["receipt_proofs"][0].update(value=wire.hex(), nodes=[leaf(c.rlp(b""), wire).hex()])
        with self.assertRaises(ValueError):
            judge.receipt_claims(row, header, txs)

    def test_raw_header_and_body_bind_views(self):
        row = block_vector()
        judge.native_block(row)
        for field, value in {"hash": "0x" + "ff" * 32, "root": "0x" + "ee" * 32,
                             "parent": "0x" + "dd" * 32, "number": 2}.items():
            bad = copy.deepcopy(row)
            bad[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                judge.native_block(bad)
        outer = judge.plain_rlp(c.rlp_list(bytes.fromhex(row["raw_block"])))
        outer[1][0] = b"\x7f\xc1\x01"
        row["raw_block"] = c.rlp(outer).hex()
        row["transaction_proofs"][0]["value"] = outer[1][0].hex()
        with self.assertRaisesRegex(ValueError, "authenticated"):
            judge.native_block(row)

    def test_ordinary_source_field_controls_cover_each_fee_and_authorization(self):
        for index, kind in enumerate((0, 0, 1, 1, 2, 2, 4, 4)):
            raw = tx_wire(kind, index)
            judge.ordinary_fields(raw, index)
            fields = judge.plain_rlp(c.rlp_list(raw if kind == 0 else raw[1:]))
            positions = list(range(6)) if kind == 0 else list(range(7 if kind == 1 else 8))
            for position in positions:
                bad = copy.deepcopy(fields)
                bad[position] = b"\x09"
                encoded = (bytes([kind]) if kind else b"") + c.rlp(bad)
                with self.subTest(kind=kind, position=position), self.assertRaises(ValueError):
                    judge.ordinary_fields(encoded, index)
            if kind == 4:
                for position in range(6):
                    bad = copy.deepcopy(fields)
                    bad[9][0][position] = b"\x09" if position < 4 else b""
                    with self.subTest(authorization=position), self.assertRaises(ValueError):
                        judge.ordinary_fields(b"\x04" + c.rlp(bad), index)

    def test_native_decision_digest_vectors(self):
        vectors = json.loads((HERE.parents[2] / "adapters/bor/decision/vectors.json").read_text())
        for kind, delivery in (("import", "BATCH"), ("finality", "CHECKPOINT")):
            intent = vectors[kind + "_intent"]
            self.assertEqual(judge.intent_digest(intent), intent["digest"])
            self.assertEqual(judge.decided_digest(intent["digest"], delivery), vectors[kind + "_decided_digest"])

    def test_malformed_report_cannot_pass(self):
        self.assertEqual(judge.judge({})["result"], "FAIL")
        with self.assertRaises(ValueError):
            judge.native_json('{"x":1,"x":2}')


if __name__ == "__main__":
    unittest.main()
