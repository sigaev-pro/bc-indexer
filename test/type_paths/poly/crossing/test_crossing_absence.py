"""The authorization gas oracle requires an absent account, not an empty EOA."""

import unittest

import test_crossing_report as reports

j = reports.j


class AuthorityAbsenceControls(unittest.TestCase):
    def test_native_absence(self):
        reports.CrossingNativeReport.setUpClass()
        row = reports.CrossingNativeReport.native["cases"][2]["crossing"]["blocks"][0]
        j.absent_authority(row, bytes.fromhex(row["root"][2:]))

    def test_authenticated_empty_account_is_not_absence(self):
        account = j.codec.rlp([b"", b"", j.codec.EMPTY_ROOT, j.codec.keccak256(b"")])
        node = reports.vectors.leaf(j.codec.keccak256(j.octets(j.prior.AUTHORITY)), account)
        row = {"account_proofs": {j.prior.AUTHORITY: {"accountProof": [node.hex()]}}}
        root = j.codec.keccak256(node)
        self.assertEqual(j.codec.proof_value(root, j.codec.keccak256(j.octets(j.prior.AUTHORITY)), [node]), account)
        with self.assertRaises(ValueError):
            j.absent_authority(row, root)


if __name__ == "__main__":
    unittest.main()
