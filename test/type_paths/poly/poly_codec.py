"""Independent fixture codec: accepted RLP/Keccak plus bounded MPT proof verification."""

import functools

# Exact accepted section from test/harness/source_inputs_geth.py via ETH judge.
KECCAK_ROUNDS = (
    0x0000000000000001, 0x0000000000008082, 0x800000000000808A, 0x8000000080008000, 0x000000000000808B,
    0x0000000080000001, 0x8000000080008081, 0x8000000000008009, 0x000000000000008A, 0x0000000000000088,
    0x0000000080008009, 0x000000008000000A, 0x000000008000808B, 0x800000000000008B, 0x8000000000008089,
    0x8000000000008003, 0x8000000000008002, 0x8000000000000080, 0x000000000000800A, 0x800000008000000A,
    0x8000000080008081, 0x8000000000008080, 0x0000000080000001, 0x8000000080008008)
KECCAK_ROTATIONS = ((0, 36, 3, 41, 18), (1, 44, 10, 45, 2), (62, 6, 43, 15, 61), (28, 55, 25, 21, 56),
                    (27, 20, 39, 8, 14))
MASK = (1 << 64) - 1


@functools.lru_cache(maxsize=4096)
def keccak256(data):
    """Keccak-256 as Ethereum uses it (original padding, not SHA3-256), written from the specification.
    It is slow in pure Python, so results are cached; data must be bytes."""
    rate = 136
    padded = bytearray(data) + b"\x01" + bytes(-(len(data) + 1) % rate)
    padded[-1] |= 0x80
    lanes = [[0] * 5 for _ in range(5)]
    for offset in range(0, len(padded), rate):
        for index in range(rate // 8):
            lanes[index % 5][index // 5] ^= int.from_bytes(padded[offset + 8 * index:offset + 8 * index + 8], "little")
        for constant in KECCAK_ROUNDS:
            parity = [lanes[x][0] ^ lanes[x][1] ^ lanes[x][2] ^ lanes[x][3] ^ lanes[x][4] for x in range(5)]
            for x in range(5):
                mix = parity[(x - 1) % 5] ^ (((parity[(x + 1) % 5] << 1) | (parity[(x + 1) % 5] >> 63)) & MASK)
                for y in range(5):
                    lanes[x][y] ^= mix
            moved = [[0] * 5 for _ in range(5)]
            for x in range(5):
                for y in range(5):
                    shift = KECCAK_ROTATIONS[x][y]
                    moved[y][(2 * x + 3 * y) % 5] = ((lanes[x][y] << shift) | (lanes[x][y] >> (64 - shift))) & MASK
            for x in range(5):
                for y in range(5):
                    lanes[x][y] = moved[x][y] ^ (~moved[(x + 1) % 5][y] & moved[(x + 2) % 5][y])
            lanes[0][0] ^= constant
    return b"".join(lanes[index % 5][index // 5].to_bytes(8, "little") for index in range(4))


def rlp_item(data, offset=0):
    """Decode one RLP item at offset. Return (value, raw encoding, next offset); a list value is a list of
    (value, raw) pairs. Noncanonical lengths are refused."""
    first = data[offset]
    if first < 0x80:
        return data[offset:offset + 1], data[offset:offset + 1], offset + 1
    short, base = (first < 0xB8, 0x80) if first < 0xC0 else (first < 0xF8, 0xC0)
    if short:
        start, length = offset + 1, first - base
    else:
        size = first - base - 55
        length = int.from_bytes(data[offset + 1:offset + 1 + size], "big")
        if data[offset + 1] == 0 or length < 56:
            raise ValueError("noncanonical RLP length")
        start = offset + 1 + size
    end = start + length
    if end > len(data):
        raise ValueError("truncated RLP item")
    if base == 0x80:
        if length == 1 and data[start] < 0x80:
            raise ValueError("noncanonical RLP byte")
        return data[start:end], data[offset:end], end
    values, cursor = [], start
    while cursor < end:
        value, raw, cursor = rlp_item(data, cursor)
        values.append((value, raw))
    if cursor != end:
        raise ValueError("RLP list overruns its length")
    return values, data[offset:end], end


def rlp_list(data):
    """Decode bytes that must hold exactly one RLP list."""
    value, _, end = rlp_item(data)
    if end != len(data) or not isinstance(value, list):
        raise ValueError("not exactly one RLP list")
    return value


# Positions in the RLP list of a Geth header.
PARENT_HASH, STATE_ROOT, NUMBER, EXTRA = 0, 3, 8, 12


def header_fields(data):
    fields = [value for value, _ in rlp_list(data)]
    if len(fields) < 15 or any(isinstance(field, list) for field in fields[:15]):
        raise ValueError("not a header")
    return fields


# Exact encoder from accepted test/type_paths/eth/test_type_paths_eth.py.
def rlp(value):
    if isinstance(value, list):
        data, base = b"".join(rlp(item) for item in value), 0xc0
    else:
        data, base = value, 0x80
        if len(data) == 1 and data[0] < 0x80:
            return data
    if len(data) < 56:
        return bytes([base + len(data)]) + data
    size = len(data).to_bytes((len(data).bit_length() + 7) // 8, "big")
    return bytes([base + 55 + len(size)]) + size + data


EMPTY_ROOT = bytes.fromhex("56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")


def proof_value(root, key, proof):
    """Authenticate one raw trie key; return leaf bytes or proven absence.

    Test-only verifier of Ethereum hexary MPT proofs. Keys are already hashed
    for state/storage; receipt keys are RLP indices. No trie construction here.
    Native source reference: pinned Bor trie/proof.go VerifyProof/get plus
    trie/encoding.go compactToHex. Explicit canonical path/reference checks
    prevent malformed proof nodes from being accepted as fixture evidence.
    """
    if not isinstance(root, bytes) or len(root) != 32 or not isinstance(key, bytes):
        raise ValueError("invalid proof root or key")
    if not isinstance(proof, list) or any(not isinstance(node, bytes) or not node for node in proof):
        raise ValueError("invalid proof node list")
    nodes = {keccak256(node): node for node in proof}
    if len(nodes) != len(proof):
        raise ValueError("duplicate proof node")
    if root == EMPTY_ROOT:
        if proof:
            raise ValueError("nonempty proof of empty trie")
        return None
    path = [part for byte in key for part in (byte >> 4, byte & 15)]
    reference, encoded = root, None
    used = set()
    first = True

    def finish(value):
        if used != set(nodes):
            raise ValueError("unused proof nodes")
        return value

    for _ in range(len(path) + len(nodes) + 2):
        if isinstance(reference, bytes):
            if not reference:
                return finish(None)
            if len(reference) != 32 or reference not in nodes:
                raise ValueError("missing or invalid proof reference")
            encoded = nodes[reference]
            if not first and len(encoded) < 32:
                raise ValueError("hashed short child")
            used.add(reference)
            node = rlp_list(encoded)
        else:
            if not isinstance(reference, list) or encoded is None or len(encoded) >= 32:
                raise ValueError("invalid embedded child")
            node = reference
        first = False
        if len(node) == 17:
            if not path:
                value = node[16][0]
                if not isinstance(value, bytes):
                    raise ValueError("non-byte branch value")
                return finish(value or None)
            reference, encoded = node[path.pop(0)]
            continue
        if len(node) != 2 or not isinstance(node[0][0], bytes) or not node[0][0]:
            raise ValueError("invalid short trie node")
        compact = node[0][0]
        flag = compact[0] >> 4
        if flag > 3 or (not flag & 1 and compact[0] & 15):
            raise ValueError("invalid compact path flags or padding")
        segment = [part for byte in compact for part in (byte >> 4, byte & 15)][2 - (flag & 1):]
        leaf = bool(flag & 2)
        if not leaf and not segment:
            raise ValueError("empty extension path")
        if path[:len(segment)] != segment:
            return finish(None)
        path = path[len(segment):]
        if leaf:
            value = node[1][0]
            if not isinstance(value, bytes) or not value:
                raise ValueError("empty or non-byte leaf value")
            return finish(value if not path else None)
        reference, encoded = node[1]
        if reference == b"":
            raise ValueError("empty extension child")
    raise ValueError("cyclic or excessive proof path")
