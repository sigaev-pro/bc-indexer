"""Independent input and source-derived fee controls for the crossing judge."""

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


j = load("crossing_strict_judge", HERE / "crossing_judge.py")
vectors = load("crossing_signature_vectors", HERE.parent / "test_poly_judge.py")
c, num = j.codec, vectors.num


def fields(kind, nonce):
    to = j.octets(j.prior.SUCCESS)
    if kind == 0:
        return [num(nonce), num(5_000_000_000), num(21000), to, b"\x07", b"", b"", b"", b""]
    if kind == 1:
        return [num(15001), num(nonce), num(5_000_000_000), num(21000), to, b"\x07", b"", [], b"", b"", b""]
    value = [num(15001), num(nonce), num(2_000_000_000), num(5_000_000_000), num(100000 if kind == 4 else 21000), to, b"\x07", b"", []]
    if kind == 4:
        auth = [num(15001), to, b""]
        auth += vectors.sign_fixture(c.keccak256(b"\x05" + c.rlp(auth)), 2)
        value.append([auth])
    return value + [b"", b"", b""]


class CrossingJudgeInputs(unittest.TestCase):
    def test_signed_before_and_at_inputs(self):
        for kind in (0, 1, 2, 4):
            for nonce in (0, 1):
                with self.subTest(kind=kind, nonce=nonce):
                    j.h.signed_input(vectors.signed_wire(kind, fields(kind, nonce)), kind, nonce)

    def test_resigned_wrong_fixture_fields_fail(self):
        for kind in (0, 1, 2, 4):
            original = fields(kind, 1)
            for index in range({0: 6, 1: 8, 2: 9, 4: 9}[kind]):
                changed = copy.deepcopy(original)
                changed[index] = [b"\x01"] if isinstance(changed[index], list) else b"\x01"
                if changed[index] == original[index]:
                    changed[index] = b"\x02"
                with self.subTest(kind=kind, field=index), self.assertRaises((ValueError, TypeError)):
                    j.h.signed_input(vectors.signed_wire(kind, changed), kind, 1)

    def test_fee_oracle_uses_effective_purchase_and_actual_gas(self):
        for kind, base, gas, price, limit in ((0, 0, 21000, 5_000_000_000, 21000),
                                             (1, 0, 21000, 5_000_000_000, 21000),
                                             (2, 10**9, 21000, 3_000_000_000, 21000),
                                             (4, 900_000_000, 46000, 2_900_000_000, 100000)):
            with self.subTest(kind=kind):
                result = j.receipt_oracle(kind, base, 10**19, 7, 10**21)
                self.assertEqual((result["gas"], result["price"]), (gas, price))
                self.assertEqual(result["sender_after"], 10**19 - gas * price - 7)
                self.assertEqual(result["signer_after"], 10**21 + gas * (price - base))
                values = [int.from_bytes(result["logs"][0][2][i:i + 32], "big") for i in range(0, 160, 32)]
                self.assertEqual(values, [7, 10**19 - limit * price, 7, 10**19 - limit * price - 7, 14])
                values = [int.from_bytes(result["logs"][1][2][i:i + 32], "big") for i in range(0, 160, 32)]
                tip = gas * (price - base)
                self.assertEqual(values, [tip, 10**19, 10**21, 10**19 - tip, 10**21 + tip])
                payload = result["wire"] if kind == 0 else result["wire"][1:]
                if kind:
                    self.assertEqual(result["wire"][0], kind)
                decoded = j.prior.plain_rlp(c.rlp_list(payload))
                self.assertEqual(decoded, [b"\x01", num(gas), result["bloom"], result["logs"]])

    def test_resigned_authorization_domain_and_count(self):
        for index, value in ((0, num(15002)), (1, j.octets(j.prior.TARGET)), (2, num(1))):
            changed = fields(4, 1)
            changed[9][0][index] = value
            changed[9][0][-3:] = vectors.sign_fixture(c.keccak256(b"\x05" + c.rlp(changed[9][0][:3])), 2)
            with self.subTest(field=index), self.assertRaises(ValueError):
                j.h.signed_input(vectors.signed_wire(4, changed), 4, 1)
        changed = fields(4, 1)
        changed[9].append(copy.deepcopy(changed[9][0]))
        with self.assertRaises(ValueError):
            j.h.signed_input(vectors.signed_wire(4, changed), 4, 1)

    def test_type_tag_and_extra_signed_fields(self):
        for kind in (1, 2, 4):
            wire = vectors.signed_wire(kind, fields(kind, 1))
            with self.subTest(tag=kind), self.assertRaises(ValueError):
                j.h.signed_input(bytes([kind + 1]) + wire[1:], kind, 1)
            extended = fields(kind, 1)
            extended.insert(-3, b"\x01")
            with self.subTest(extra=kind), self.assertRaises(ValueError):
                j.h.signed_input(vectors.signed_wire(kind, extended), kind, 1)

    def test_incomplete_and_malformed_reports_fail(self):
        for value in (None, [], {}, {"native_source": j.prior.NATIVE, "scope": "private-ordinary-activation-crossing", "public_profile_credit": False, "cases": []}):
            self.assertEqual(j.judge(value)["result"], "FAIL")


if __name__ == "__main__":
    unittest.main()
