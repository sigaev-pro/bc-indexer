"""Known native trie roots and independent corruption controls."""

import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("type_paths_poly_codec", HERE / "poly_codec.py")
codec = importlib.util.module_from_spec(spec)
spec.loader.exec_module(codec)


class PolygonProofTests(unittest.TestCase):
    def test_ordered_root_empty_tiny_and_index_extension_vectors(self):
        self.assertEqual(codec.ordered_root([]), codec.EMPTY_ROOT)
        branch = [b""] * 17
        branch[8], branch[0] = [b"\x30", b"a"], [b"\x31", b"b"]
        self.assertEqual(codec.ordered_root([b"a", b"b"]), codec.keccak256(codec.rlp(branch)))
        values = [bytes([97 + i % 26]) for i in range(130)]
        def ref(node):
            raw = codec.rlp(node)
            return node if len(raw) < 32 else codec.keccak256(raw)
        root = [b""] * 17
        for first in range(8):
            group = [b""] * 17
            for last in range(16):
                index = first * 16 + last
                if index:
                    group[last] = [b"\x20", values[index]]
            root[first] = ref(group)
        ending = [b""] * 17
        ending[0], ending[1] = [b"\x20", values[128]], [b"\x20", values[129]]
        self.assertLess(len(codec.rlp(ending)), 32)
        group8 = [b""] * 17
        group8[0], group8[1] = [b"\x20", values[0]], [b"\x18", ending]
        root[8] = ref(group8)
        expected = codec.keccak256(codec.rlp(root))
        self.assertEqual(codec.ordered_root(values), expected)
        for bad in (values[:-1], values + [b"extra"], list(reversed(values))):
            self.assertNotEqual(codec.ordered_root(bad), expected)

    def test_pinned_native_signature_vector_and_recovery_parity(self):
        # Bor 3a018e3 crypto/signature_test.go TestEcrecover/VerifySignature.
        digest = bytes.fromhex("ce0677bb30baa8cf067c88db9811f4333d131bf8bcf12fe7065d211dce971008")
        r = int("90f27b8b488db00b00606796d2987f6a5f59ae62ea05effe84fef5b8b0e54998", 16)
        s = int("4a691139ad57a3f0b906637673aa2f63d1f55cb1a69199d4009eea23ceaddc93", 16)
        public = (int("e32df42865e97135acfb65f3bae71bdc86f4d49150ad6a440b6f15878109880a", 16),
                  int("a2b2667f7e725ceea70c673093bf67663e0312623c8e091b13cf2c0f11ef652", 16))
        codec.fixture_signature.cache_clear()
        try:
            with patch.object(codec, "FIXTURE_PUBLIC_KEYS", (public, codec.FIXTURE_PUBLIC_KEYS[1])):
                codec.fixture_signature(digest, 1, r, s, 1)
                for args in ((digest, 0, r, s, 1), (digest, 1, r, s, 2), (digest, 1, r + 1, s, 1),
                             (bytes(32), 1, r, s, 1), (digest, 0, r, codec.ORDER - s, 1)):
                    with self.subTest(args=args), self.assertRaises(ValueError):
                        codec.fixture_signature(*args)
        finally:
            codec.fixture_signature.cache_clear()
    def test_codec_is_exact_accepted_reuse(self):
        accepted = (HERE.parent / "eth/eth_type_judge.py").read_text()
        actual = (HERE / "poly_codec.py").read_text()
        start, end = "KECCAK_ROUNDS =", "\ndef quantity("
        section = accepted[accepted.index(start):accepted.index(end)].strip()
        self.assertIn(section, actual)
        self.assertEqual(codec.keccak256(b"").hex(), "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")

    def test_recovery_x_coordinate_is_not_only_modulo_order(self):
        # Construct a valid ECDSA equation with R.x = n+2. Ethereum's
        # recovery encoding has only the parity bit, so r=2 cannot encode R.
        x = codec.ORDER + 2
        y = pow((x**3 + 7) % codec.FIELD, (codec.FIELD + 1) // 4, codec.FIELD)
        self.assertEqual(y*y % codec.FIELD, (x**3 + 7) % codec.FIELD)
        negative_g = (codec.GENERATOR[0], -codec.GENERATOR[1] % codec.FIELD)
        public = codec.point_multiply(pow(2, -1, codec.ORDER), codec.point_add((x, y), negative_g))
        self.assertEqual(codec.point_add(codec.GENERATOR, codec.point_multiply(2, public)), (x, y))
        codec.fixture_signature.cache_clear()
        try:
            with patch.object(codec, "FIXTURE_PUBLIC_KEYS", (public, codec.FIXTURE_PUBLIC_KEYS[1])):
                with self.assertRaisesRegex(ValueError, "expected fixture key"):
                    codec.fixture_signature((1).to_bytes(32, "big"), y % 2, 2, 1, 1)
        finally:
            codec.fixture_signature.cache_clear()

    def test_pinned_native_single_leaf_vector(self):
        # Bor 3a018e3 trie/trie_test.go TestInsert: A => fifty 'a' bytes.
        # The expected root is fixed in native source, never learned from runs.
        node = codec.rlp([bytes.fromhex("2041"), b"a" * 50])
        root = bytes.fromhex("d23786fb4a010da3ce639d66d5e904a11dbc02746d1ce25029e53290cabf28ab")
        self.assertEqual(codec.keccak256(node), root)
        self.assertEqual(codec.proof_value(root, b"A", [node]), b"a" * 50)
        self.assertIsNone(codec.proof_value(root, b"B", [node]))

    def test_empty_root_and_root_authentication(self):
        self.assertEqual(codec.keccak256(b"\x80"), codec.EMPTY_ROOT)
        self.assertIsNone(codec.proof_value(codec.EMPTY_ROOT, b"key", []))
        leaf = codec.rlp([b"\x20", b"value"])
        root = codec.keccak256(leaf)
        with self.assertRaises(ValueError):
            codec.proof_value(codec.EMPTY_ROOT, b"", [leaf])
        for bad_root, proof in ((b"x" * 32, [leaf]), (root, []), (root, [leaf + b"x"]), (root, [leaf, leaf])):
            with self.subTest(proof=proof), self.assertRaises(ValueError):
                codec.proof_value(bad_root, b"", proof)

    def test_pinned_native_multi_key_vector(self):
        # Bor trie/trie_test.go TestInsert, manually laid out hexary nodes.
        tail = [b"\x37" + b"lesworth", b"cat"]
        dog = [b""] * 17
        dog[6], dog[16] = tail, b"puppy"
        dog_raw = codec.rlp(dog)
        branch = [b""] * 17
        branch[5], branch[7] = [b"\x20", b"reindeer"], codec.keccak256(dog_raw)
        branch_raw = codec.rlp(branch)
        root_node = codec.rlp([bytes.fromhex("1646f6"), codec.keccak256(branch_raw)])
        root = bytes.fromhex("8aad789dff2f538bca5d8ea56e8abe10f4c7ba3a5dea95fea4cd6e7c3a1168d3")
        self.assertEqual(codec.keccak256(root_node), root)
        self.assertEqual(codec.proof_value(root, b"doe", [root_node, branch_raw]), b"reindeer")
        for key, value in ((b"dog", b"puppy"), (b"dogglesworth", b"cat")):
            self.assertEqual(codec.proof_value(root, key, [root_node, branch_raw, dog_raw]), value)

    def test_branch_embedded_and_hashed_children(self):
        for value in (b"short", b"long" * 20):
            child = [b"\x30", value]  # remaining nibble 0, leaf
            encoded = codec.rlp(child)
            branch = [b""] * 17
            branch[1] = child if len(encoded) < 32 else codec.keccak256(encoded)
            root_node = codec.rlp(branch)
            proof = [root_node] + ([] if len(encoded) < 32 else [encoded])
            self.assertEqual(codec.proof_value(codec.keccak256(root_node), b"\x10", proof), value)
            self.assertIsNone(codec.proof_value(codec.keccak256(root_node), b"\x20", [root_node]))

    def test_extension_and_branch_terminal(self):
        branch = [b""] * 16 + [b"terminal"]
        root_node = codec.rlp([b"\x00\xab", branch])
        self.assertEqual(codec.proof_value(codec.keccak256(root_node), b"\xab", [root_node]), b"terminal")
        self.assertIsNone(codec.proof_value(codec.keccak256(root_node), b"\xac", [root_node]))

    def test_ordered_index_encoding_boundary(self):
        # RLP(index) changes from a direct byte at 127 to a length-prefixed
        # integer at 128. Both manually constructed leaves share one branch.
        branch = [b""] * 17
        branch[7] = [b"\x3f", b"index127"]
        branch[8] = [b"\x31\x80", b"index128"]
        node = codec.rlp(branch)
        root = codec.keccak256(node)
        self.assertEqual(codec.rlp(b"\x7f"), b"\x7f")
        self.assertEqual(codec.rlp(b"\x80"), b"\x81\x80")
        self.assertEqual(codec.proof_value(root, b"\x7f", [node]), b"index127")
        self.assertEqual(codec.proof_value(root, b"\x81\x80", [node]), b"index128")
        self.assertIsNone(codec.proof_value(root, b"\x80", [node]))
        changed = list(branch)
        changed[7], changed[8] = branch[8], branch[7]
        with self.assertRaisesRegex(ValueError, "reference"):
            codec.proof_value(root, b"\x7f", [codec.rlp(changed)])

    def test_noncanonical_paths_and_references_are_refused(self):
        for node in ([b"\x40", b"x"], [b"\x21", b"x"], [b"\x00", b"x" * 32],
                     [b"\x00\xab", b""], [b"\x00\xab", b"short"], [b"\x20", b""]):
            raw = codec.rlp(node)
            with self.subTest(node=node), self.assertRaises(ValueError):
                codec.proof_value(codec.keccak256(raw), b"\xab" if node[0].endswith(b"\xab") else b"", [raw])
        short = codec.rlp([b"\x20", b"x"])
        root = codec.rlp([b"\x00\xab", codec.keccak256(short)])
        with self.assertRaisesRegex(ValueError, "hashed short"):
            codec.proof_value(codec.keccak256(root), b"\xab", [root, short])
        long_child = [b"\x20", b"x" * 50]
        root = codec.rlp([b"\x00\xab", long_child])
        with self.assertRaisesRegex(ValueError, "embedded"):
            codec.proof_value(codec.keccak256(root), b"\xab", [root])
        child = [b"\x20", b"value"]
        root = codec.rlp([b"\x00", child])
        with self.assertRaisesRegex(ValueError, "empty extension"):
            codec.proof_value(codec.keccak256(root), b"", [root])
        leaf = codec.rlp(child)
        unused = codec.rlp([b"\x20", b"unused"])
        with self.assertRaisesRegex(ValueError, "unused"):
            codec.proof_value(codec.keccak256(leaf), b"", [leaf, unused])

    def test_correlated_leaf_change_cannot_keep_header_root(self):
        original = codec.rlp([b"\x20", b"correct"])
        changed = codec.rlp([b"\x20", b"substituted"])
        with self.assertRaisesRegex(ValueError, "reference"):
            codec.proof_value(codec.keccak256(original), b"", [changed])


if __name__ == "__main__":
    unittest.main()
