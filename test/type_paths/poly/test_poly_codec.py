"""Known native trie roots and independent corruption controls."""

import importlib.util
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("type_paths_poly_codec", HERE / "poly_codec.py")
codec = importlib.util.module_from_spec(spec)
spec.loader.exec_module(codec)


class PolygonProofTests(unittest.TestCase):
    def test_codec_is_exact_accepted_reuse(self):
        accepted = (HERE.parent / "eth/eth_type_judge.py").read_text()
        actual = (HERE / "poly_codec.py").read_text()
        start, end = "KECCAK_ROUNDS =", "\ndef quantity("
        section = accepted[accepted.index(start):accepted.index(end)].strip()
        self.assertIn(section, actual)
        self.assertEqual(codec.keccak256(b"").hex(), "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")

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

    def test_correlated_leaf_change_cannot_keep_header_root(self):
        original = codec.rlp([b"\x20", b"correct"])
        changed = codec.rlp([b"\x20", b"substituted"])
        with self.assertRaisesRegex(ValueError, "reference"):
            codec.proof_value(codec.keccak256(original), b"", [changed])


if __name__ == "__main__":
    unittest.main()
