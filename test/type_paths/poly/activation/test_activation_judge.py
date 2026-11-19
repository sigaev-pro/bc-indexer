"""Synthetic component controls; native evidence is separately retained."""

import copy
import importlib.util
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


j = load("activation_strict_judge", HERE / "activation_judge.py")
helpers = load("activation_test_vectors", HERE.parent / "test_poly_judge.py")
c, num = j.codec, helpers.num


def fields(kind):
    to = j.octets(j.prior.SUCCESS)
    if kind == 0:
        return [b"", num(5_000_000_000), num(21000), to, b"\x07", b"", b"", b"", b""]
    if kind == 1:
        return [num(15001), b"\x01", num(5_000_000_000), num(21000), to, b"\x07", b"", [], b"", b"", b""]
    value = [num(15001), b"\x01", num(2_000_000_000), num(5_000_000_000), num(100000 if kind == 4 else 21000), to, b"\x07", b"", []]
    if kind == 4:
        auth = [num(15001), to, b""]
        auth += helpers.sign_fixture(c.keccak256(b"\x05" + c.rlp(auth)), 2)
        value.append([auth])
    return value + [b"", b"", b""]


class ActivationJudgeInputs(unittest.TestCase):
    def test_valid_signed_native_domains(self):
        for kind in (0, 1, 2, 4):
            with self.subTest(kind=kind):
                j.signed_input(helpers.signed_wire(kind, fields(kind)), kind, int(kind != 0))

    def test_resigned_bad_inputs_do_not_hide_behind_signature_failure(self):
        for kind in (0, 1, 2, 4):
            original = fields(kind)
            # Independently sign each changed value so the fixture predicate,
            # rather than invalid ECDSA, must reject it.
            indices = {0: (0, 1, 2, 3, 4, 5), 1: (0, 1, 2, 3, 4, 5, 6, 7),
                       2: tuple(range(9)), 4: tuple(range(9))}[kind]
            for index in indices:
                changed = copy.deepcopy(original)
                changed[index] = [b"\x01"] if isinstance(changed[index], list) else b"\x01"
                if changed[index] == original[index]:
                    changed[index] = b"\x02"
                with self.subTest(kind=kind, field=index), self.assertRaises((ValueError, TypeError)):
                    j.signed_input(helpers.signed_wire(kind, changed), kind, int(kind != 0))

    def test_authority_domain_is_checked_after_valid_resigning(self):
        for index, replacement in ((0, b"\x01"), (1, bytes(20)), (2, b"\x01")):
            changed = fields(4)
            auth = changed[9][0]
            auth[index] = replacement
            auth[-3:] = helpers.sign_fixture(c.keccak256(b"\x05" + c.rlp(auth[:3])), 2)
            with self.subTest(field=index), self.assertRaises(ValueError):
                j.signed_input(helpers.signed_wire(4, changed), 4, 1)

    def test_wrong_key_and_parity_are_not_valid_fixture_inputs(self):
        for kind in (0, 1, 2, 4):
            wire = helpers.signed_wire(kind, fields(kind))
            decoded = j.prior.plain_rlp(c.rlp_list(wire if kind == 0 else wire[1:]))
            for index in (-3, -2, -1):
                changed = copy.deepcopy(decoded)
                changed[index] = num(j.integer(changed[index]) ^ 1)
                corrupted = (bytes([kind]) if kind else b"") + c.rlp(changed)
                with self.subTest(kind=kind, signature=index), self.assertRaises(ValueError):
                    j.signed_input(corrupted, kind, int(kind != 0))

    def test_historical_width_body_and_reported_chain_are_authenticated(self):
        row = helpers.block_vector()
        header, txs, digest = j.raw_block(row["raw_block"], 16)
        self.assertEqual(digest, row["hash"])
        j.block(row, 16, 1, row["parent"])
        for field, value in (("hash", "0x" + "12" * 32), ("root", "0x" + "12" * 32),
                             ("parent", "0x" + "12" * 32), ("number", 2),
                             ("transaction_proofs", [])):
            changed = copy.deepcopy(row)
            changed[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                j.block(changed, 16, 1, row["parent"])
        with self.assertRaises(ValueError):
            j.raw_block(row["raw_block"], 15)
        malformed = c.rlp([header, [], []]).hex()
        with self.assertRaises(ValueError):
            j.raw_block(malformed, 16)

    def test_incomplete_reports_never_gain_acceptance(self):
        for report in ({}, {"native_source": j.prior.NATIVE, "scope": "private-preactivation",
                            "public_profile_credit": False, "cases": []}):
            self.assertEqual(j.judge(report)["result"], "FAIL")


if __name__ == "__main__":
    unittest.main()
