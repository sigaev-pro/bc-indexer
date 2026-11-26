#!/usr/bin/env python3
"""Golden vectors of one BSC block event.

Writes expected.json next to this file from chapel-block.json. This script
is the independent expected result of the builder in
core/normalization/bsc/blockrecord: it restates the logical schema registry
and the D73 fragment encoding from the documents (MESSAGE_CONTRACT section
6.1, CONTROL_WIRE_CONTRACT "READY manifest and reservation") and computes
the semantic digest, the record digest and the fragment digests with
hashlib only. It reads no Go code.

Usage: python3 -I generate.py [--check]

With --check nothing is written and the exit code is 1 when expected.json
differs from what the script computes.
"""

import hashlib
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent

# ---------------------------------------------------------------- keccak

MASK = (1 << 64) - 1
ROUND_CONSTANTS = [
    0x0000000000000001, 0x0000000000008082, 0x800000000000808A, 0x8000000080008000,
    0x000000000000808B, 0x0000000080000001, 0x8000000080008081, 0x8000000000008009,
    0x000000000000008A, 0x0000000000000088, 0x0000000080008009, 0x000000008000000A,
    0x000000008000808B, 0x800000000000008B, 0x8000000000008089, 0x8000000000008003,
    0x8000000000008002, 0x8000000000000080, 0x000000000000800A, 0x800000008000000A,
    0x8000000080008081, 0x8000000000008080, 0x0000000080000001, 0x8000000080008008,
]
ROTATIONS = [
    [0, 36, 3, 41, 18],
    [1, 44, 10, 45, 2],
    [62, 6, 43, 15, 61],
    [28, 55, 25, 21, 56],
    [27, 20, 39, 8, 14],
]


def rotate(value, count):
    return ((value << count) | (value >> (64 - count))) & MASK if count else value


def permute(lanes):
    for constant in ROUND_CONSTANTS:
        column = [lanes[x] ^ lanes[x + 5] ^ lanes[x + 10] ^ lanes[x + 15] ^ lanes[x + 20] for x in range(5)]
        theta = [column[(x - 1) % 5] ^ rotate(column[(x + 1) % 5], 1) for x in range(5)]
        lanes = [lanes[index] ^ theta[index % 5] for index in range(25)]
        moved = [0] * 25
        for x in range(5):
            for y in range(5):
                moved[y + 5 * ((2 * x + 3 * y) % 5)] = rotate(lanes[x + 5 * y], ROTATIONS[x][y])
        lanes = [moved[index] ^ (~moved[(index + 1) % 5 + 5 * (index // 5)] & moved[(index + 2) % 5 + 5 * (index // 5)])
                 for index in range(25)]
        lanes[0] ^= constant
    return lanes


def keccak256(data):
    """Keccak-256 (Ethereum hashing; SHA3-256 uses another padding)."""
    rate = 136
    padded = bytearray(data) + b"\x01"
    padded += b"\x00" * (-len(padded) % rate)
    padded[-1] |= 0x80
    lanes = [0] * 25
    for offset in range(0, len(padded), rate):
        for index in range(rate // 8):
            lanes[index] ^= int.from_bytes(padded[offset + 8 * index:offset + 8 * index + 8], "little")
        lanes = permute(lanes)
    return b"".join(lane.to_bytes(8, "little") for lane in lanes[:4])


# ------------------------------------------------------------ native header

def rlp_string(data):
    if len(data) == 1 and data[0] < 0x80:
        return data
    if len(data) <= 55:
        return bytes([0x80 + len(data)]) + data
    length = len(data).to_bytes((len(data).bit_length() + 7) // 8, "big")
    return bytes([0xB7 + len(length)]) + length + data


def rlp_list(items):
    payload = b"".join(items)
    if len(payload) <= 55:
        return bytes([0xC0 + len(payload)]) + payload
    length = len(payload).to_bytes((len(payload).bit_length() + 7) // 8, "big")
    return bytes([0xF7 + len(length)]) + length + payload


def rlp_integer(value):
    return rlp_string(value.to_bytes((value.bit_length() + 7) // 8, "big"))


def word(value):
    """The bytes of a 0x quantity of the RPC."""
    return bytes.fromhex(value[2:])


def uint256(value):
    """An RPC quantity as the 32 big-endian bytes of a UInt256."""
    return int(value, 16).to_bytes(32, "big")


OPTIONAL_HEADER_FIELDS = ["baseFeePerGas", "withdrawalsRoot", "blobGasUsed", "excessBlobGas",
                          "parentBeaconBlockRoot", "requestsHash"]


def header_rlp(block):
    """rlp(types.Header) of the pinned BSC client: the common fields, then
    each optional field up to the last one the header carries."""
    items = [
        rlp_string(word(block["parentHash"])), rlp_string(word(block["sha3Uncles"])),
        rlp_string(word(block["miner"])), rlp_string(word(block["stateRoot"])),
        rlp_string(word(block["transactionsRoot"])), rlp_string(word(block["receiptsRoot"])),
        rlp_string(word(block["logsBloom"])), rlp_integer(int(block["difficulty"], 16)),
        rlp_integer(int(block["number"], 16)), rlp_integer(int(block["gasLimit"], 16)),
        rlp_integer(int(block["gasUsed"], 16)), rlp_integer(int(block["timestamp"], 16)),
        rlp_string(word(block["extraData"])), rlp_string(word(block["mixHash"])),
        rlp_string(word(block["nonce"])),
    ]
    carried = [name in block for name in OPTIONAL_HEADER_FIELDS]
    for index, name in enumerate(OPTIONAL_HEADER_FIELDS):
        if not any(carried[index:]):
            break
        if not carried[index]:
            items.append(b"\x80")
        elif name in ("baseFeePerGas", "blobGasUsed", "excessBlobGas"):
            items.append(rlp_integer(int(block[name], 16)))
        else:
            items.append(rlp_string(word(block[name])))
    return rlp_list(items)


SYSTEM_CONTRACTS = {"0x" + word.rjust(40, "0") for word in [
    "1000", "1001", "1002", "1003", "1004", "1005", "1006", "1007", "1008",
    "2000", "2001", "2002", "2003", "2004", "2005", "2006", "3000"]}


def system_transaction_count(block):
    """The pinned BSC criterion: the recipient is a system contract, the
    effective gas price is zero, and the recovered sender is the coinbase."""
    count = 0
    for transaction in block["transactions"]:
        if transaction.get("to") in SYSTEM_CONTRACTS and int(transaction["gasPrice"], 16) == 0 \
                and transaction["from"] == block["miner"]:
            count += 1
    return count


def identity(name):
    """A test-only identity of 16 bytes: SHA-256 of the name."""
    return hashlib.sha256(name.encode()).digest()[:16]


def u64(value):
    return value.to_bytes(8, "big")


def head(number, digest):
    return u64(number) + digest


def attestation_digest(status, justified, finalized):
    """extractor-lifecycle-attestation-v1 of the native lifecycle package."""
    return hashlib.sha256(b"extractor-lifecycle-attestation-v1\x00" + u64(len(status)) + status.encode()
                          + head(*justified) + head(*finalized)).hexdigest()


# ------------------------------------------------------------------ registry

MESSAGE, LIST, BOOL, U32, U64, ENUM, BYTES, UTF8 = ("message", "list", "bool", "uint32", "uint64", "enum",
                                                     "bytes", "utf8")

# A field is [tag, name, kind, element or message type key, optional, enum, byte length, oneof].


def field(tag, name, kind, type_key=0, optional=False, enum="", length=0, oneof=""):
    return [tag, name, kind, type_key, optional, enum, length, oneof]


def message(tag, name, type_key):
    return field(tag, name, MESSAGE, type_key)


def uint64_field(tag, name):
    return field(tag, name, U64)


def uint32_field(tag, name):
    return field(tag, name, U32)


def bytes_field(tag, name, length=0):
    return field(tag, name, BYTES, length=length)


def utf8_field(tag, name):
    return field(tag, name, UTF8)


def enum_field(tag, name, enum):
    return field(tag, name, ENUM, enum=enum)


def optional(item):
    item[4] = True
    return item


def variant(item, group):
    item[7] = group
    return item


MESSAGES = {
    1: ("Envelope", [uint32_field(1, "schema_version"), message(2, "network", 2), message(3, "profile_ref", 3),
                     bytes_field(4, "event_id", 40), bytes_field(5, "producer_id", 16), bytes_field(6, "run_id", 16),
                     message(7, "source", 4), enum_field(8, "capture_mode", "CaptureMode"),
                     variant(message(104, "block_applied", 10), "payload")]),
    2: ("NetworkIdentity", [enum_field(1, "family", "NetworkFamily"), bytes_field(2, "chain_id", 32),
                            bytes_field(3, "genesis_hash", 32)]),
    3: ("ProfileRef", [utf8_field(1, "network_profile"), utf8_field(2, "client_commit"),
                       utf8_field(3, "adapter_version"), utf8_field(4, "fork_rules_version"),
                       utf8_field(5, "normalization_version")]),
    4: ("SourceRef", [bytes_field(1, "node_instance_id", 16), bytes_field(2, "writer_epoch", 16),
                      uint64_field(3, "source_sequence")]),
    10: ("BlockApplied", [message(1, "activation", 11), message(2, "admission", 13), message(3, "metadata", 14),
                          message(4, "initial_snapshot", 23)]),
    11: ("BlockActivationRef", [message(1, "network", 2), bytes_field(2, "block_hash", 32),
                                bytes_field(3, "producer_id", 16), bytes_field(4, "run_id", 16),
                                message(5, "activation_id", 12)]),
    12: ("ActivationID", [uint64_field(1, "decision_sequence"), uint64_field(2, "entity_ordinal")]),
    13: ("ObservationRef", [bytes_field(1, "producer_id", 16), bytes_field(2, "run_id", 16),
                            uint64_field(3, "observation_sequence")]),
    14: ("BlockMetadata", [uint64_field(1, "number"), bytes_field(2, "hash", 32),
                           bytes_field(3, "parent_hash", 32), message(4, "timestamp", 15),
                           uint64_field(5, "source_transaction_count"), message(6, "header", 16),
                           message(7, "details", 17)]),
    15: ("Timestamp", [uint64_field(1, "seconds"), uint32_field(2, "nanos"),
                       enum_field(3, "precision", "TimestampPrecision")]),
    16: ("BlockHeader", [bytes_field(1, "parent_hash", 32), bytes_field(2, "uncle_hash", 32),
                         bytes_field(3, "beneficiary", 20), bytes_field(4, "state_root", 32),
                         bytes_field(5, "transactions_root", 32), bytes_field(6, "receipts_root", 32),
                         bytes_field(7, "logs_bloom", 256), bytes_field(8, "difficulty", 32),
                         uint64_field(9, "number"), uint64_field(10, "gas_limit"), uint64_field(11, "gas_used"),
                         uint64_field(12, "timestamp_seconds"), bytes_field(13, "extra_data"),
                         bytes_field(14, "mix_digest", 32), bytes_field(15, "nonce", 8),
                         optional(bytes_field(16, "base_fee", 32)),
                         optional(bytes_field(17, "withdrawals_root", 32)),
                         optional(uint64_field(18, "blob_gas_used")),
                         optional(uint64_field(19, "excess_blob_gas")),
                         optional(bytes_field(20, "parent_beacon_block_root", 32)),
                         optional(bytes_field(21, "requests_hash", 32)), bytes_field(22, "source_rlp")]),
    17: ("FamilyDetails", [variant(message(1, "eth", 18), "family"), variant(message(2, "bsc", 19), "family"),
                           variant(message(3, "bor", 21), "family")]),
    18: ("EthBlockDetails", [optional(bytes_field(1, "block_access_list_hash", 32)),
                             optional(uint64_field(2, "slot_number"))]),
    19: ("BscBlockDetails", [optional(uint64_field(1, "parlia_system_transaction_count")),
                             optional(message(2, "vote_snapshot", 20))]),
    20: ("ParliaVoteSnapshot", [message(1, "justified", 25), message(2, "finalized", 25)]),
    21: ("BorBlockDetails", [optional(uint64_field(1, "sprint_length")), optional(field(2, "sprint_end", BOOL)),
                             optional(field(3, "side_chain_block", BOOL)),
                             optional(uint64_field(4, "state_sync_count")),
                             optional(enum_field(5, "state_sync_mode", "StateSyncMode")),
                             field(6, "heimdall_checkpoints", LIST, 22)]),
    22: ("HeimdallCheckpointRef", [enum_field(1, "kind", "CheckpointKind"), message(2, "end", 25),
                                   optional(uint64_field(3, "checkpoint_id"))]),
    23: ("LifecycleSnapshot", [message(1, "native_decision", 24), message(2, "observed_execution_head", 25),
                               message(3, "markers", 26)]),
    24: ("NativeDecisionRef", [bytes_field(1, "node_instance_id", 16), bytes_field(2, "producer_id", 16),
                               bytes_field(3, "run_id", 16), uint64_field(4, "decision_sequence")]),
    25: ("AnchorRef", [uint64_field(1, "height"), bytes_field(2, "hash", 32)]),
    26: ("FinalityMarkers", [variant(message(1, "eth", 27), "family"), variant(message(2, "bsc", 28), "family"),
                             variant(message(3, "polygon", 30), "family")]),
    27: ("EthereumMarkers", [message(1, "safe", 31), message(2, "finalized", 31),
                             enum_field(3, "safe_basis", "EthereumSafeBasis")]),
    28: ("BscMarkers", [optional(message(1, "attested", 29)), message(2, "effective_finalized", 31),
                        enum_field(3, "effective_basis", "BscEffectiveBasis")]),
    29: ("AttestedSnapshot", [message(1, "snapshot_head", 25), message(2, "justified", 25),
                              message(3, "finalized", 25)]),
    30: ("PolygonMarkers", [message(1, "milestone", 31), message(2, "checkpoint", 31),
                            message(3, "native_finalized", 31), message(4, "l1_checkpoint", 31)]),
    31: ("ReferenceObservation", [enum_field(1, "kind", "ReferenceKind"),
                                  optional(message(2, "anchor", 25)),
                                  optional(enum_field(3, "origin", "EvidenceOrigin")),
                                  optional(enum_field(4, "reason", "UnavailableReason"))]),
}

ENUMS = {
    "CaptureMode": {"LIVE": 1},
    "NetworkFamily": {"EVM": 1},
    "TimestampPrecision": {"SECONDS": 1, "MILLISECONDS": 2, "NANOSECONDS": 3},
    "StateSyncMode": {"END_OF_BLOCK_COMMIT": 1, "BODY_TRANSACTION": 2},
    "CheckpointKind": {"MILESTONE": 1, "CHECKPOINT": 2},
    "EvidenceOrigin": {"NATIVE_LIVE": 1, "NATIVE_RESTART": 2, "RETAINED_DURABLE": 3},
    "UnavailableReason": {"SOURCE_OFFLINE": 1, "SOURCE_SYNCING": 2, "STATE_UNAVAILABLE": 3,
                          "CHECKPOINT_PENDING": 4},
    "ReferenceKind": {"KNOWN": 1, "NOT_OBSERVED": 2, "NATIVE_REFERENCE_UNSET": 3, "UNAVAILABLE": 4},
    "EthereumSafeBasis": {"CONSENSUS_SETTER": 1, "STARTUP_FINALIZED_FALLBACK": 2, "UNSET": 3},
    "BscEffectiveBasis": {"HEADER_SNAPSHOT": 1, "VOTE_POOL_QUORUM": 2, "GENESIS_FALLBACK": 3, "UNSET": 4},
}

SCHEMA_VERSION = 2
ROOT_TYPE = 1  # Envelope

PROFILE = {
    "network_profile": "bsc-testnet",
    "client_commit": "54afaff4da31ac9e49ba646a4f65104edac5a2f3",
    "adapter_version": "bsc-adapter-v1",
    "fork_rules_version": "bsc-fork-rules-v1",
    "normalization_version": "bsc-normalization-v1",
}
PROFILE_CHAIN_ID = 97


# --------------------------------------------------------------------- input

EVENT_SEQUENCE = 1
SOURCE_SEQUENCE = 1
DECISION_SEQUENCE = 12
OBSERVATION_SEQUENCE = 11


def input_facts(document):
    """The Input of the builder as JSON values, from the stored header responses."""
    block = document["block"]
    number = int(block["number"], 16)
    source_rlp = header_rlp(block)
    # The header encoding is exact: its Keccak-256 is the block hash.
    if "0x" + keccak256(source_rlp).hex() != block["hash"]:
        raise SystemExit("the native header encoding does not hash to the block hash")
    genesis_hash = word(document["genesis_hash"])
    justified = (int(document["parent"]["number"], 16), word(document["parent"]["hash"]))
    finalized = (int(document["grandparent"]["number"], 16), word(document["grandparent"]["hash"]))
    header = {
        "hash": block["hash"], "parent_hash": block["parentHash"], "uncle_hash": block["sha3Uncles"],
        "beneficiary": block["miner"], "state_root": block["stateRoot"],
        "transactions_root": block["transactionsRoot"], "receipts_root": block["receiptsRoot"],
        "logs_bloom": block["logsBloom"], "difficulty": "0x" + uint256(block["difficulty"]).hex(),
        "number": number, "gas_limit": int(block["gasLimit"], 16), "gas_used": int(block["gasUsed"], 16),
        "time": int(block["timestamp"], 16), "extra_data": block["extraData"], "mix_digest": block["mixHash"],
        "nonce": block["nonce"], "source_rlp": "0x" + source_rlp.hex(),
        # Present exactly when the native header carries the field; a
        # present zero stays a present value.
        "base_fee": "0x" + uint256(block["baseFeePerGas"]).hex() if "baseFeePerGas" in block else None,
        "withdrawals_root": block.get("withdrawalsRoot"),
        "blob_gas_used": int(block["blobGasUsed"], 16) if "blobGasUsed" in block else None,
        "excess_blob_gas": int(block["excessBlobGas"], 16) if "excessBlobGas" in block else None,
        "parent_beacon_block_root": block.get("parentBeaconBlockRoot"),
        "requests_hash": block.get("requestsHash"),
    }
    return {
        "network": {"chain_id": (97).to_bytes(32, "big").hex(), "genesis_hash": genesis_hash.hex()},
        "node_instance_id": identity("chapel-node-instance").hex(),
        "producer_id": identity("chapel-producer").hex(),
        "run_id": identity("chapel-run").hex(),
        "writer_epoch": identity("chapel-writer-epoch").hex(),
        "source_sequence": SOURCE_SEQUENCE,
        "decision_sequence": DECISION_SEQUENCE,
        "observation_sequence": OBSERVATION_SEQUENCE,
        "header": header,
        "tx_count": len(block["transactions"]),
        "system_tx_count": system_transaction_count(block),
        "old_head": {"number": justified[0], "hash": "0x" + justified[1].hex()},
        "new_head": {"number": number, "hash": block["hash"]},
        "justified": {"number": justified[0], "hash": "0x" + justified[1].hex()},
        "finalized": {"number": finalized[0], "hash": "0x" + finalized[1].hex()},
        "attestation": {"status": "ATTESTED", "justified": {"number": justified[0], "hash": "0x" + justified[1].hex()},
                        "finalized": {"number": finalized[0], "hash": "0x" + finalized[1].hex()},
                        "digest": attestation_digest("ATTESTED", justified, finalized)},
    }


# ---------------------------------------------------------------------- tree


class Tree:
    """The Envelope tree in depth-first preorder; every field is resolved
    through the registry tables by name."""

    def __init__(self):
        self.nodes = [{"kind": MESSAGE, "type": ROOT_TYPE, "parent": None, "tag": None, "value": None,
                       "children": 0, "last_tag": 0}]
        self.open = [0]

    def resolve(self, name):
        parent = self.nodes[self.open[-1]]
        for candidate in MESSAGES[parent["type"]][1]:
            if candidate[1] == name:
                return candidate
        raise KeyError("message %d has no field %s" % (parent["type"], name))

    def add(self, spec, kind, value):
        assert spec[2] == kind, spec
        parent = self.nodes[self.open[-1]]
        # The children of a message carry ascending field tags (tree rule 1).
        assert parent["children"] == 0 or parent["last_tag"] < spec[0], (parent["type"], spec)
        parent["last_tag"] = spec[0]
        index = len(self.nodes)
        self.nodes.append({"kind": kind, "type": spec[3], "parent": self.open[-1], "tag": spec[0],
                           "value": value, "children": 0, "last_tag": 0})
        parent["children"] += 1
        return index

    def put(self, name, kind, value):
        """Write value as the leaf field name of the open message."""
        spec = self.resolve(name)
        if kind == BYTES and spec[6] and len(value) != spec[6]:
            raise ValueError("field %s holds %d bytes, want %d" % (name, len(value), spec[6]))
        if kind == ENUM:
            value = ENUMS[spec[5]][value]
        self.add(spec, kind, value)

    def close(self, path, write):
        """Open the message at path, let write fill it, and close it."""
        for name in path.split("."):
            self.open.append(self.add(self.resolve(name), MESSAGE, None))
        write()
        del self.open[-len(path.split(".")):]


def build_tree(facts, event_id):
    header = facts["header"]
    chain_id, genesis = bytes.fromhex(facts["network"]["chain_id"]), bytes.fromhex(facts["network"]["genesis_hash"])
    producer, run = bytes.fromhex(facts["producer_id"]), bytes.fromhex(facts["run_id"])
    instance, epoch = bytes.fromhex(facts["node_instance_id"]), bytes.fromhex(facts["writer_epoch"])
    block_hash, parent_hash = word(header["hash"]), word(header["parent_hash"])
    old, justified, finalized = facts["old_head"], facts["justified"], facts["finalized"]
    milliseconds = header["time"] * 1000 + int.from_bytes(word(header["mix_digest"])[24:], "big")

    def network():
        tree.put("family", ENUM, "EVM")
        tree.put("chain_id", BYTES, chain_id)
        tree.put("genesis_hash", BYTES, genesis)

    def anchor(name, item):
        tree.close(name, lambda: (tree.put("height", U64, item["number"]),
                                  tree.put("hash", BYTES, word(item["hash"]))))

    def header_fields():
        # Every field of the header in registry order; an optional field is a
        # node only when the native header carried it.
        for spec in MESSAGES[16][1]:
            name = spec[1]
            if spec[2] == U64:
                value = header["time"] if name == "timestamp_seconds" else header[name]
            else:
                value = None if header.get(name) is None else word(header[name])
            if value is not None:
                tree.put(name, spec[2], value)

    def reference(name, kind, item=None, origin=None):
        def body():
            tree.put("kind", ENUM, kind)
            if kind == "KNOWN":
                anchor("anchor", item)
                tree.put("origin", ENUM, origin)
        tree.close(name, body)

    def profile_ref():
        for name, value in PROFILE.items():
            tree.put(name, UTF8, value.encode())

    def source():
        tree.put("node_instance_id", BYTES, instance)
        tree.put("writer_epoch", BYTES, epoch)
        tree.put("source_sequence", U64, facts["source_sequence"])

    tree = Tree()
    tree.put("schema_version", U32, SCHEMA_VERSION)
    tree.close("network", network)
    tree.close("profile_ref", profile_ref)
    tree.put("event_id", BYTES, event_id)
    tree.put("producer_id", BYTES, producer)
    tree.put("run_id", BYTES, run)
    tree.close("source", source)
    tree.put("capture_mode", ENUM, "LIVE")

    def activation():
        tree.close("network", network)
        tree.put("block_hash", BYTES, block_hash)
        tree.put("producer_id", BYTES, producer)
        tree.put("run_id", BYTES, run)
        tree.close("activation_id", lambda: (tree.put("decision_sequence", U64, facts["decision_sequence"]),
                                            tree.put("entity_ordinal", U64, 0)))

    def admission():
        tree.put("producer_id", BYTES, producer)
        tree.put("run_id", BYTES, run)
        tree.put("observation_sequence", U64, facts["observation_sequence"])

    def metadata():
        tree.put("number", U64, header["number"])
        tree.put("hash", BYTES, block_hash)
        tree.put("parent_hash", BYTES, parent_hash)
        tree.close("timestamp", lambda: (tree.put("seconds", U64, milliseconds // 1000),
                                         tree.put("nanos", U32, (milliseconds % 1000) * 1_000_000),
                                         tree.put("precision", ENUM, "MILLISECONDS")))
        tree.put("source_transaction_count", U64, facts["tx_count"])
        tree.close("header", header_fields)

        def details():
            def bsc():
                if facts["system_tx_count"] is not None:
                    tree.put("parlia_system_transaction_count", U64, facts["system_tx_count"])
            tree.close("bsc", bsc)
        tree.close("details", details)

    def snapshot():
        tree.close("native_decision", lambda: (tree.put("node_instance_id", BYTES, instance),
                                               tree.put("producer_id", BYTES, producer),
                                               tree.put("run_id", BYTES, run),
                                               tree.put("decision_sequence", U64, facts["decision_sequence"])))
        anchor("observed_execution_head", facts["new_head"])

        def markers():
            def bsc():
                status = facts["attestation"]["status"]
                if status == "ATTESTED":
                    tree.close("attested", lambda: (anchor("snapshot_head", facts["new_head"]),
                                                    anchor("justified", facts["attestation"]["justified"]),
                                                    anchor("finalized", facts["attestation"]["finalized"])))
                    basis = "HEADER_SNAPSHOT"
                else:
                    basis = "GENESIS_FALLBACK"
                reference("effective_finalized", "KNOWN", finalized, "NATIVE_LIVE")
                tree.put("effective_basis", ENUM, basis)
            tree.close("bsc", bsc)
        tree.close("markers", markers)

    tree.close("block_applied", lambda: (tree.close("activation", activation),
                                            tree.close("admission", admission),
                                            tree.close("metadata", metadata),
                                            tree.close("initial_snapshot", snapshot)))
    assert tree.open == [0]
    return tree.nodes


# ------------------------------------------------------------- D73 encoding


def varint(value):
    out = bytearray()
    while value >= 0x80:
        out.append((value & 0x7F) | 0x80)
        value >>= 7
    out.append(value)
    return bytes(out)


def varint_field(tag, value):
    return varint(tag << 3) + varint(value)


def bytes_field_wire(tag, data):
    return varint(tag << 3 | 2) + varint(len(data)) + data


def sha256(data):
    return hashlib.sha256(data).digest()


def typed_item(index, node):
    item = varint_field(1, index)
    if index:
        item += varint_field(2, node["parent"])
    if node["tag"] is not None:
        item += varint_field(3, node["tag"])
    kind, value = node["kind"], node["value"]
    if kind == MESSAGE:
        item += bytes_field_wire(5, varint_field(1, node["type"]) + varint_field(2, node["children"]))
    elif kind == U64:
        item += varint_field(9, value)
    elif kind == U32:
        item += varint_field(8, value)
    elif kind == ENUM:
        item += varint_field(10, value)
    elif kind in (BYTES, UTF8):
        reference = varint_field(1, index) + varint_field(2, len(value)) + bytes_field_wire(3, sha256(value))
        item += bytes_field_wire(11 if kind == BYTES else 12, reference)
    else:
        raise ValueError(kind)
    return item


CHUNK_DATA = 16384
FRAGMENT_MAX = 65536


def fragment_size(index, inner_length):
    """The serialized size of a fragment: version, 40-byte event id,
    fragment index and one length-delimited payload."""
    return 2 + 42 + len(varint(index)) + 1 + len(varint(inner_length)) + inner_length


def wrap(event_id, index, payload_tag, payload):
    return (varint_field(1, 1) + bytes_field_wire(2, event_id) + varint_field(3, index)
            + bytes_field_wire(payload_tag, payload))


def pack(event_id, next_index, payload_tag, wires):
    """The maximal non-empty prefixes of the stream that fit one fragment."""
    fragments, inner = [], b""
    for wire in wires:
        if inner and fragment_size(next_index, len(inner) + len(wire)) > FRAGMENT_MAX:
            fragments.append(wrap(event_id, next_index, payload_tag, inner))
            next_index += 1
            inner = b""
        inner += wire
    if inner:
        fragments.append(wrap(event_id, next_index, payload_tag, inner))
        next_index += 1
    return fragments, next_index


def encode_record(facts, event_id, nodes):
    chain_id, genesis = bytes.fromhex(facts["network"]["chain_id"]), bytes.fromhex(facts["network"]["genesis_hash"])
    items = [bytes_field_wire(1, typed_item(index, node)) for index, node in enumerate(nodes)]
    blobs = []
    for index, node in enumerate(nodes):
        if node["kind"] not in (BYTES, UTF8) or not node["value"]:
            continue
        for offset in range(0, len(node["value"]), CHUNK_DATA):
            data = node["value"][offset:offset + CHUNK_DATA]
            chunk = varint_field(1, index) + varint_field(2, offset) + bytes_field_wire(3, data)
            blobs.append(bytes_field_wire(1, chunk))
    item_fragments, _ = pack(event_id, 1, 5, items)
    blob_fragments, _ = pack(event_id, 1 + len(item_fragments), 6, blobs)
    count = 2 + len(item_fragments) + len(blob_fragments)
    blob_count = sum(1 for node in nodes if node["kind"] in (BYTES, UTF8))
    total_blob_bytes = sum(len(node["value"]) for node in nodes if node["kind"] in (BYTES, UTF8))

    network = varint_field(1, 1) + bytes_field_wire(2, chain_id) + bytes_field_wire(3, genesis)
    header = (varint_field(1, SCHEMA_VERSION) + bytes_field_wire(2, network) + varint_field(3, 2)
              + varint_field(4, 1) + varint_field(5, ROOT_TYPE) + varint_field(6, len(nodes))
              + varint_field(7, blob_count) + varint_field(8, total_blob_bytes) + varint_field(9, count))
    first = wrap(event_id, 0, 4, header)
    content = [first] + item_fragments + blob_fragments
    content_bytes = sum(len(fragment) for fragment in content)
    preimage = b"extractor.record.v1\x00" + b"".join(
        u64(index) + u64(len(fragment)) + fragment for index, fragment in enumerate(content))
    record_digest = sha256(preimage)
    seal = varint_field(1, count) + varint_field(2, content_bytes) + bytes_field_wire(3, record_digest)
    return content + [wrap(event_id, count - 1, 7, seal)], record_digest, content_bytes


def semantic_digest(facts):
    """extractor.semantic.v1 of protocol/semantic."""
    parts = [b"extractor.semantic.v1\x00", (2).to_bytes(2, "big"), (1).to_bytes(2, "big"), b"\x01",
             bytes.fromhex(facts["network"]["chain_id"]), bytes.fromhex(facts["network"]["genesis_hash"]),
             bytes.fromhex(facts["producer_id"]), bytes.fromhex(facts["run_id"]),
             u64(facts["decision_sequence"]), u64(0), word(facts["header"]["hash"]), b"\x00", u64(0),
             bytes.fromhex(facts["node_instance_id"]), bytes.fromhex(facts["writer_epoch"]),
             u64(facts["source_sequence"])]
    return sha256(b"".join(parts))


def event_descriptor(facts, ordinal, event_id, record_length, record_digest, fragments, fragment_manifest_digest):
    return (u64(ordinal) + event_id + (2).to_bytes(2, "big") + (1).to_bytes(2, "big") + u64(record_length)
            + record_digest + u64(len(fragments)) + fragment_manifest_digest)


def vectors(document):
    facts = input_facts(document)
    event_id = bytes.fromhex(facts["producer_id"]) + bytes.fromhex(facts["run_id"]) + u64(EVENT_SEQUENCE)
    nodes = build_tree(facts, event_id)
    fragments, record_digest, content_bytes = encode_record(facts, event_id, nodes)
    descriptors = [u64(index) + u64(len(fragment)) + sha256(fragment) for index, fragment in enumerate(fragments)]
    fragment_manifest_digest = sha256(b"extractor.fragments.v1\x00" + event_id + u64(len(fragments))
                                      + b"".join(descriptors))
    record_length = sum(len(fragment) for fragment in fragments)
    descriptor = event_descriptor(facts, 0, event_id, record_length, record_digest, fragments,
                                  fragment_manifest_digest)
    manifest = (b"extractor.manifest.v2\x00" + u64(1) + descriptor + b"".join(descriptors))
    block = document["block"]
    return {
        "schema_version": 1,
        "description": "Golden vectors of one BSC Chapel block event: a fixed Input and the semantic digest, "
                       "record digest and fragment digests an independent implementation computes from it. "
                       "generate.py writes this file; the Go test of core/normalization/bsc/blockrecord reads it.",
        "fixture": {
            "endpoint": document["endpoint"],
            "acquired_at": document["acquired_at"],
            "block_number": int(block["number"], 16),
            "block_hash": block["hash"],
            "genesis_hash": document["genesis_hash"],
            "header_rlp_sha256": sha256(word(facts["header"]["source_rlp"])).hex(),
        },
        "registry": {
            "schema_version": SCHEMA_VERSION,
            "messages": {str(key): {"name": name, "fields": fields} for key, (name, fields) in MESSAGES.items()},
            "enums": ENUMS,
            "profile": dict(PROFILE, chain_id=(PROFILE_CHAIN_ID).to_bytes(32, "big").hex()),
        },
        "input": facts,
        "event_sequence": EVENT_SEQUENCE,
        "expected": {
            "semantic_digest": semantic_digest(facts).hex(),
            "node_count": len(nodes),
            "blob_count": sum(1 for node in nodes if node["kind"] in (BYTES, UTF8)),
            "total_blob_bytes": sum(len(node["value"]) for node in nodes if node["kind"] in (BYTES, UTF8)),
            "fragment_count": len(fragments),
            "record_length": record_length,
            "content_bytes": content_bytes,
            "record_digest": record_digest.hex(),
            "fragment_manifest_digest": fragment_manifest_digest.hex(),
            "fragments": [{"index": index, "length": len(fragment), "sha256": sha256(fragment).hex()}
                          for index, fragment in enumerate(fragments)],
            "manifest_digest": sha256(manifest).hex(),
        },
    }


def main(argv):
    document = json.loads((HERE / "chapel-block.json").read_text())
    text = json.dumps(vectors(document), indent=2) + "\n"
    target = HERE / "expected.json"
    if "--check" in argv:
        if target.read_text() != text:
            print("expected.json differs from the computed vectors", file=sys.stderr)
            return 1
        return 0
    target.write_text(text)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
