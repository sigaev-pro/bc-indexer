"""Portable native-vector checks; no BSC import or execution acceptance."""
import copy
import importlib.util
import json
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("bsc_full_import_codec_judge", HERE / "judge.py")
judge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(judge)
wire, encoding = judge.wire, judge.encoding
VECTORS = json.loads((HERE / "codec_vectors.json").read_text())


def transaction_row(tx):
    return dict(copy.deepcopy(tx), to=tx["fields"]["to"], value=tx["fields"]["value"], data=tx["fields"]["input"][2:])


def block_rows(block):
    return [{"wire": r["transaction_wire"], "stored_receipt_wire": r["wire"]} for r in block["receipts"]]


class CodecControls(unittest.TestCase):
    def test_native_vectors_all_types_and_signers(self):
        self.assertEqual({t["type"] for t in VECTORS["transactions"]}, set(range(5)))
        for tx in VECTORS["transactions"]:
            with self.subTest(kind=tx["type"], nonce=tx["nonce"]):
                judge.check_wire(transaction_row(tx))
                decoded = wire.transaction(bytes.fromhex(tx["wire"]), encoding)
                self.assertEqual(decoded["from"], tx["from"])
                self.assertEqual(decoded["hash"], tx["hash"])
                for auth in decoded.get("authorizationList", []):
                    self.assertEqual(auth["authority"], judge.AUTHORITY)

    def test_each_native_json_field_is_bound(self):
        for tx in VECTORS["transactions"]:
            for field in wire.transaction(bytes.fromhex(tx["wire"]), encoding):
                if field == "from":
                    continue
                row = transaction_row(tx)
                value = row["fields"][field]
                if isinstance(value, list):
                    row["fields"][field] = [] if value else [None]
                elif field in ("hash", "to"):
                    row["fields"][field] = "0x" + "f" * (len(value) - 2)
                elif field == "input":
                    row["fields"][field] = "0xff"
                else:
                    row["fields"][field] = hex(judge.q(value) + 1)
                with self.subTest(kind=tx["type"], field=field), self.assertRaises((ValueError, TypeError)):
                    judge.check_wire(row)

    def test_snapshot_context_is_bound(self):
        for name, value in (("from", judge.AUTHORITY), ("to", judge.AUTHORITY), ("type", 4), ("nonce", 999),
                            ("hash", "0x" + "1" * 64), ("value", "999"), ("data", "ff")):
            row = transaction_row(VECTORS["transactions"][0]); row[name] = value
            with self.subTest(field=name), self.assertRaises(ValueError):
                judge.check_wire(row)

    def test_signature_and_envelope_rejections(self):
        raw = bytes.fromhex(VECTORS["transactions"][2]["wire"])
        fields = wire.unpack(encoding.rlp_list(raw[1:]))
        for index, value in ((-3, b"\x02"), (-2, b""), (-1, b""), (-1, wire.N.to_bytes(32, "big")), (0, b"\x00\x01")):
            bad = copy.deepcopy(fields); bad[index] = value
            with self.subTest(index=index, value=value.hex()), self.assertRaises(ValueError):
                wire.transaction(b"\x02" + wire.encode(bad), encoding)
        for raw in (b"", b"\x05\xc0", b"\x00\xc0", b"\x02\xc0", b"\x02\xc1\x80", b"\x02\xc0\x00"):
            with self.subTest(raw=raw.hex()), self.assertRaises(ValueError):
                wire.transaction(raw, encoding)

    def test_native_transaction_and_receipt_roots(self):
        self.assertEqual(wire.trie_root([], encoding.keccak256).hex(), "56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
        for block in VECTORS["blocks"]:
            judge.check_block_wires({"wire": block["raw_block"]}, block_rows(block))

    def test_correlated_substitution_and_reordering(self):
        block = VECTORS["blocks"][0]
        for mutation in ("swap", "wire-swap", "substitute", "receipt", "remove"):
            rows = block_rows(block)
            if mutation == "swap": rows[0], rows[1] = rows[1], rows[0]
            if mutation == "wire-swap": rows[0]["wire"], rows[1]["wire"] = rows[1]["wire"], rows[0]["wire"]
            if mutation == "substitute": rows[0] = block_rows(VECTORS["blocks"][1])[0]
            if mutation == "receipt": rows[0]["stored_receipt_wire"] = rows[1]["stored_receipt_wire"]
            if mutation == "remove": rows.pop()
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                judge.check_block_wires({"wire": block["raw_block"]}, rows)
        # Even a coherently changed body and corresponding row cannot retain the old header root.
        body = wire.unpack(encoding.rlp_list(bytes.fromhex(block["raw_block"])))
        body[1][0], body[1][1] = body[1][1], body[1][0]
        rows = block_rows(block); rows[0], rows[1] = rows[1], rows[0]
        with self.assertRaisesRegex(ValueError, "transaction trie"):
            judge.check_block_wires({"wire": wire.encode(body).hex()}, rows)


if __name__ == "__main__":
    unittest.main()
