"""Raw block shape and complete-body commitments, independent of outer metadata."""

import copy
import unittest

import test_crossing_report as reports

j = reports.j


class CrossingBlockControls(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        reports.CrossingNativeReport.setUpClass()
        cls.native = reports.CrossingNativeReport.native

    def test_body_shape_and_root(self):
        original = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(self.native["cases"][0]["crossing"]["blocks"][0]["raw_block"])))
        changes = [lambda body: body.append([]), lambda body: body.__setitem__(1, b""),
                   lambda body: body.__setitem__(2, [body[0]]), lambda body: body[0].append(b""),
                   lambda body: body[0].__setitem__(4, j.codec.EMPTY_ROOT)]
        for index, change in enumerate(changes):
            body = copy.deepcopy(original)
            change(body)
            with self.subTest(index=index), self.assertRaises((ValueError, TypeError)):
                j.h.raw_block(j.codec.rlp(body).hex(), 15)

    def test_genesis_tail(self):
        original = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(self.native["cases"][2]["genesis_raw"])))
        changes = [lambda body: body.__setitem__(3, [b""]), lambda body: body[0].__setitem__(15, b"\x01"),
                   lambda body: body[0].__setitem__(16, bytes(32)), lambda body: body[0].__setitem__(17, b"\x01"),
                   lambda body: body[0].__setitem__(18, b"\x01"), lambda body: body[0].__setitem__(19, bytes([1]) * 32)]
        for index, change in enumerate(changes):
            body = copy.deepcopy(original)
            change(body)
            with self.subTest(index=index), self.assertRaises(ValueError):
                j.h.raw_block(j.codec.rlp(body).hex(), 20)


if __name__ == "__main__":
    unittest.main()
