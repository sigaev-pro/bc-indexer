"""Portable input-boundary checks for explicit native controls."""

from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import Rejected
from bsc_exercise import first_rlp_item


class BscControlInputTests(unittest.TestCase):
    def test_first_complete_native_rlp_list_keeps_exact_bytes(self):
        first = b"\xf8\x38" + b"x" * 56
        self.assertEqual(first_rlp_item(first + b"\xc0"), first)
        self.assertEqual(first_rlp_item(b"\xc1\x00\xc0"), b"\xc1\x00")

    def test_truncated_noncanonical_or_nonlist_input_rejects(self):
        for value in (b"", b"\x80", b"\xf8", b"\xf8\x38x", b"\xf8\x01x", b"\xf9\x00\x38" + b"x" * 56):
            with self.subTest(value=value[:4]), self.assertRaises(Rejected):
                first_rlp_item(value)


if __name__ == "__main__":
    unittest.main()
