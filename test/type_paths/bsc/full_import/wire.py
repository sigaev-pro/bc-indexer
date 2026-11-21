"""Signed fixture envelopes and ordered roots; this is not native admission policy."""
P = 2**256 - 2**32 - 977
N = 0xfffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141
G = (0x79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798,
     0x483ada7726a3c4655da4fbfc0e1108a8fd17b448a68554199c47d08ffb10d4b8)


def require(ok, message):
    if not ok:
        raise ValueError(message)


def encode(value):
    if isinstance(value, int):
        require(value >= 0, "negative RLP integer")
        value = value.to_bytes((value.bit_length() + 7) // 8, "big")
    if isinstance(value, list):
        payload, base = b"".join(encode(item) for item in value), 192
    else:
        require(isinstance(value, bytes), "invalid RLP value")
        if len(value) == 1 and value[0] < 128:
            return value
        payload, base = value, 128
    size = len(payload)
    if size <= 55:
        return bytes([base + size]) + payload
    length = size.to_bytes((size.bit_length() + 7) // 8, "big")
    return bytes([base + 55 + len(length)]) + length + payload


def unpack(values):
    return [unpack(value) if isinstance(value, list) else value for value, _ in values]


def integer(value):
    require(isinstance(value, bytes) and (not value or value[0] != 0), "noncanonical RLP integer")
    return int.from_bytes(value, "big")


def address(value):
    require(isinstance(value, bytes) and len(value) == 20, "invalid address")
    return "0x" + value.hex()


def add(a, b):
    if a is None:
        return b
    if b is None:
        return a
    x, y = a; u, v = b
    if x == u and (y != v or y == 0):
        return None
    slope = ((3 * x * x) * pow(2 * y, -1, P) if a == b else (v - y) * pow(u - x, -1, P)) % P
    z = (slope * slope - x - u) % P
    return z, (slope * (x - z) - y) % P


def multiply(scalar, point):
    result = None
    while scalar:
        if scalar & 1:
            result = add(result, point)
        point = add(point, point)
        scalar >>= 1
    return result


def recover(digest, parity, r, s, keccak):
    require(parity in (0, 1) and 0 < r < N and 0 < s <= N // 2, "invalid signature scalar")
    y2 = (r**3 + 7) % P
    y = pow(y2, (P + 1) // 4, P)
    require(y * y % P == y2, "invalid signature point")
    if y % 2 != parity:
        y = P - y
    point = (r, y)
    require(multiply(N, point) is None, "invalid signature subgroup")
    inverse = pow(r, -1, N)
    public = add(multiply(s * inverse % N, point), multiply(-int.from_bytes(digest, "big") * inverse % N, G))
    require(public is not None, "invalid recovered key")
    return "0x" + keccak(public[0].to_bytes(32, "big") + public[1].to_bytes(32, "big"))[-20:].hex()


def access_list(value):
    require(isinstance(value, list), "invalid access list")
    result = []
    for item in value:
        require(isinstance(item, list) and len(item) == 2 and isinstance(item[1], list), "invalid access entry")
        require(all(isinstance(slot, bytes) and len(slot) == 32 for slot in item[1]), "invalid access slot")
        result.append({"address": address(item[0]), "storageKeys": ["0x" + slot.hex() for slot in item[1]]})
    return result


def transaction(raw, encoding):
    require(bool(raw), "empty transaction")
    kind = raw[0] if raw[0] < 128 else 0
    require(kind in range(5) and (kind != 0 or raw[0] >= 192), "unsupported envelope")
    fields = unpack(encoding.rlp_list(raw if kind == 0 else raw[1:]))
    require(len(fields) == {0: 9, 1: 11, 2: 12, 3: 14, 4: 13}[kind], "wrong transaction arity")
    expected = {"type": kind}
    if kind == 0:
        nonce, price, gas, to, value, data, v, r, s = fields
        v = integer(v)
        require(v >= 35, "unprotected fixture transaction")
        chain, parity = (v - 35) // 2, (v - 35) % 2
        unsigned = fields[:6] + [chain, 0, 0]
        digest = encoding.keccak256(encode(unsigned))
        expected["gasPrice"] = integer(price)
    else:
        chain, nonce = map(integer, fields[:2])
        if kind == 1:
            price, gas, to, value, data, access = fields[2:8]
            expected["gasPrice"] = integer(price)
        else:
            tip, cap, gas, to, value, data, access = fields[2:9]
            expected.update(maxPriorityFeePerGas=integer(tip), maxFeePerGas=integer(cap))
        expected["accessList"] = access_list(access)
        parity, r, s = fields[-3:]
        parity = integer(parity)
        digest = encoding.keccak256(bytes([kind]) + encode(fields[:-3]))
        if kind == 3:
            require(isinstance(fields[10], list) and all(isinstance(h, bytes) and len(h) == 32 for h in fields[10]), "invalid blob hashes")
            expected.update(maxFeePerBlobGas=integer(fields[9]), blobVersionedHashes=["0x" + h.hex() for h in fields[10]])
        if kind == 4:
            require(isinstance(fields[9], list), "invalid authorization list")
            auths = []
            for auth in fields[9]:
                require(isinstance(auth, list) and len(auth) == 6, "invalid authorization")
                ac, target, an, av, ar, ass = auth
                # EIP-7702 skips invalid authorization signatures; they do not
                # make the containing transaction structurally undecodable.
                try:
                    recovered = recover(encoding.keccak256(b"\x05" + encode(auth[:3])), integer(av), integer(ar), integer(ass), encoding.keccak256)
                except ValueError:
                    recovered = None
                auths.append({"chainId": integer(ac), "address": address(target), "nonce": integer(an),
                              "yParity": integer(av), "r": integer(ar), "s": integer(ass), "authority": recovered})
            expected["authorizationList"] = auths
    if kind == 0:
        nonce = integer(nonce)
    expected.update(chainId=chain, nonce=nonce, gas=integer(gas), to=address(to), value=integer(value), input="0x" + data.hex(),
                    r=integer(r), s=integer(s), v=v if kind == 0 else parity)
    expected["from"] = recover(digest, parity, integer(r), integer(s), encoding.keccak256)
    expected["hash"] = "0x" + encoding.keccak256(raw).hex()
    return expected


def trie_root(values, keccak):
    """Ethereum ordered MPT root, including embedded short child nodes."""
    pairs = [(tuple(n for byte in encode(index) for n in (byte >> 4, byte & 15)), value) for index, value in enumerate(values)]

    def compact(path, leaf):
        flags = 2 * leaf + len(path) % 2
        nibbles = (flags,) + path if len(path) % 2 else (flags, 0) + path
        return bytes(nibbles[i] * 16 + nibbles[i + 1] for i in range(0, len(nibbles), 2))

    def reference(node):
        raw = encode(node)
        return node if len(raw) < 32 else keccak(raw)

    def build(items):
        if len(items) == 1:
            path, value = items[0]
            return [compact(path, True), value]
        common = 0
        while all(len(path) > common and path[common] == items[0][0][common] for path, _ in items):
            common += 1
        if common:
            return [compact(items[0][0][:common], False), reference(build([(path[common:], value) for path, value in items]))]
        branch = [b""] * 17
        for nibble in range(16):
            children = [(path[1:], value) for path, value in items if path and path[0] == nibble]
            if children:
                branch[nibble] = reference(build(children))
        for path, value in items:
            if not path:
                branch[16] = value
        return branch

    return keccak(encode(build(pairs) if pairs else b""))
