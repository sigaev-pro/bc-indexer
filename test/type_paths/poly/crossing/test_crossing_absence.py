"""The authorization gas oracle requires an absent account, not an empty EOA."""

import copy
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

    def test_transition_binds_absence_and_every_account_field(self):
        reports.CrossingNativeReport.setUpClass()
        for case in reports.CrossingNativeReport.native["cases"]:
            first, second = case["crossing"]["blocks"]
            state1 = j.prior.snapshot(first["state_after"], False)
            state2 = j.prior.snapshot(second["state_after"], False)
            root = bytes.fromhex(first["root"][2:])
            after = state2[j.prior.SENDER]["balance"]
            j.transition_state(first, root, state1, state2, case["type"], after)
            for address in j.prior.WATCHED:
                for field, value in (("nonce", 9), ("balance", 123), ("code", b"bad"), ("storage", (1, 0, 0, 0))):
                    changed = copy.deepcopy(state2)
                    changed[address][field] = value
                    with self.subTest(kind=case["type"], address=address, field=field), self.assertRaises(ValueError):
                        j.transition_state(first, root, state1, changed, case["type"], after)
            if case["type"] == 4:
                account = j.codec.rlp([b"", b"", j.codec.EMPTY_ROOT, j.codec.keccak256(b"")])
                node = reports.vectors.leaf(j.codec.keccak256(j.octets(j.prior.AUTHORITY)), account)
                changed = copy.deepcopy(first)
                changed["account_proofs"][j.prior.AUTHORITY]["accountProof"] = [node.hex()]
                with self.assertRaises(ValueError):
                    j.transition_state(changed, j.codec.keccak256(node), state1, state2, 4, after)


if __name__ == "__main__":
    unittest.main()
