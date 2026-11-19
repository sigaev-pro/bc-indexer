"""Receipt checks isolated from outer block validation and redundant metadata."""

import copy
import unittest

import test_crossing_report as reports

j = reports.j


class CrossingReceiptControls(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        reports.CrossingNativeReport.setUpClass()
        cls.row = reports.CrossingNativeReport.native["cases"][0]["crossing"]["blocks"][0]

    def inputs(self):
        row = copy.deepcopy(self.row)
        header, txs, _ = j.h.raw_block(row["raw_block"], 15)
        oracle = j.receipt_oracle(0, 0, 10**19, 0, 10**21)
        return row, header, txs[0], oracle

    def test_receipt_collection_shapes(self):
        for field in ("receipts", "receipt_proofs"):
            row, header, tx, oracle = self.inputs()
            row[field] = tuple(row[field])
            with self.subTest(field=field), self.assertRaises(ValueError):
                j.stored(row, header, tx, 0, oracle)
        row, header, tx, oracle = self.inputs()
        row["receipts"].append(copy.deepcopy(row["receipts"][0]))
        with self.assertRaises(ValueError):
            j.stored(row, header, tx, 0, oracle)

    def test_authenticated_receipt_must_match_execution_oracle(self):
        row, header, tx, oracle = self.inputs()
        fields = j.prior.plain_rlp(j.codec.rlp_list(oracle["wire"]))
        fields[0] = b""
        wire = j.codec.rlp(fields)
        node = reports.vectors.leaf(j.codec.rlp(b""), wire)
        header[5] = j.codec.keccak256(node)
        row["receipt_proofs"][0].update(value=wire.hex(), nodes=[node.hex()])
        j.h.inclusion(row["receipt_proofs"], [wire], header[5])
        with self.assertRaises(ValueError):
            j.stored(row, header, tx, 0, oracle)

    def test_header_gas_and_bloom_are_independent_commitments(self):
        for field, value in ((10, b"\x01"), (6, bytes(256))):
            row, header, tx, oracle = self.inputs()
            header[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                j.stored(row, header, tx, 0, oracle)


if __name__ == "__main__":
    unittest.main()
