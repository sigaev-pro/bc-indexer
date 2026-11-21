"""Synthetic Madhugiri body-StateSync carrier controls; no native execution."""

import base64
import copy
import importlib.util
import json
from pathlib import Path
import unittest

import test_crossing_judge as inputs
import test_crossing_paired as paired_module
import test_crossing_report as reports

j = reports.j
c, vectors, num = j.codec, reports.vectors, reports.vectors.num
PRIOR = j.prior
DATA = PRIOR.EVENT_DATA
PADDED = int.from_bytes(DATA.ljust(32, b"\0"), "big")
RECEIVER_ADDRESS = int(PRIOR.RECEIVER, 16)
ZERO_STORAGE = [0, 0, 0, 0]
VERDICT = {"result": "PASS", "private_activation_crossings": [1, 2, 4],
           "reserved127": {"madhugiri_crossing": True, "pre_madhugiri_separate_receipt": True},
           "public_profile_credit": False}

_spec = importlib.util.spec_from_file_location("madhugiri_profile", Path(__file__).resolve().parent.parent / "poly_profile.py")
profile = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(profile)


def local_items():
    """Fixture-local body items; deliberately independent of the judge helpers."""
    return [[bytes([i]), bytes.fromhex(PRIOR.TARGET[2:]) if i == 1 else bytes.fromhex(PRIOR.REVERT[2:]),
             DATA, i.to_bytes(32, "big")] for i in (1, 2)]


def local_wire(items=None):
    return b"\x7f" + c.rlp(local_items() if items is None else items)


def local_event(identifier, target):
    return c.rlp([bytes([identifier]), bytes.fromhex(target[2:]), DATA, identifier.to_bytes(32, "big"),
                  bytes([identifier]), b"15001"])


def local_abi(selector, number, data):
    return selector + number.to_bytes(32, "big") + (64).to_bytes(32, "big") + len(data).to_bytes(32, "big") + data + bytes(-len(data) % 32)


def local_sync_receipt(cumulative):
    """Fixture-local 127 receipt oracle; independent of the judge's copy."""
    logs = [[bytes.fromhex(PRIOR.TARGET[2:]), [m := (94).to_bytes(32, "big")], (1).to_bytes(32, "big")]]
    bloom = 0
    for address, topics, _ in logs:
        for item in [address] + topics:
            digest = c.keccak256(item)
            for offset in (0, 2, 4):
                bloom |= 1 << (int.from_bytes(digest[offset:offset + 2], "big") & 2047)
    bloom = bloom.to_bytes(256, "big")
    return {"gas": 0, "price": 0, "logs": logs, "bloom": bloom,
            "wire": b"\x7f" + c.rlp([b"\x01", num(cumulative), bloom, logs])}


def nibbles(key):
    return [n for byte in key for n in (byte >> 4, byte & 15)]


def compact(path, leaf):
    flag = (2 if leaf else 0) + len(path) % 2
    parts = [flag] + path if len(path) % 2 else [flag, 0] + path
    return bytes(parts[i] * 16 + parts[i + 1] for i in range(0, len(parts), 2))


def make_trie(entries):
    """Build the hexary trie of key/value bytes; return (root, proof_for)."""
    values = dict(entries)
    paths = {key: nibbles(key) for key in values}
    registry = {}

    def reference(node):
        return node if len(node) < 32 else c.keccak256(node)

    def build(items):
        if len(items) == 1:
            key, remaining = items[0]
            node = c.rlp([compact(remaining, True), values[key]])
        else:
            shared = 0
            while all(len(path) > shared and path[shared] == items[0][1][shared] for _, path in items):
                shared += 1
            if shared:
                child = build([(key, path[shared:]) for key, path in items])
                node = c.rlp([compact(items[0][1][:shared], False), reference(child)])
            else:
                branch = [b""] * 17
                buckets = {}
                for key, path in items:
                    if not path:
                        branch[16] = values[key]
                    else:
                        buckets.setdefault(path[0], []).append((key, path[1:]))
                for digit, bucket in buckets.items():
                    branch[digit] = reference(build(bucket))
                node = c.rlp(branch)
        registry[c.keccak256(node)] = node
        return node

    root_node = build([(key, paths[key]) for key in values])
    if len(root_node) < 32:
        raise ValueError("fixture trie root must be hashed")

    def proof_for(key):
        proof, path = [], list(paths.get(key, nibbles(key)))
        node_ref = c.keccak256(root_node)
        while isinstance(node_ref, bytes) and len(node_ref) == 32:
            node = registry[node_ref]
            proof.append(node)
            children = [value for value, _ in c.rlp_list(node)]
            if len(children) == 17:
                if not path:
                    break
                node_ref = children[path.pop(0)]
            else:
                segment = nibbles(children[0])[2 - (children[0][0] >> 4 & 1):]
                if path[:len(segment)] != segment:
                    break
                path = path[len(segment):]
                node_ref = children[1]
                if children[0][0] >> 4 & 2:
                    break
        return proof

    return c.keccak256(root_node), proof_for


def single_proof(wire):
    key = c.rlp(b"")
    leaf_node = c.rlp([compact(nibbles(key), True), wire])
    root = c.keccak256(leaf_node)
    if root != c.ordered_root([wire]):
        raise ValueError("single-leaf fixture root differs")
    return root, [{"index": 0, "key": key.hex(), "value": wire.hex(), "nodes": [leaf_node.hex()]}]


def seal(fields, txs):
    raw = c.rlp([fields, txs, []])
    return {"number": j.integer(fields[8]), "hash": "0x" + c.keccak256(c.rlp(fields)).hex(),
            "parent": "0x" + fields[0].hex(), "root": "0x" + fields[3].hex(), "raw_block": raw.hex()}


def madhugiri_config(bor_prefix, gated):
    eth = {name: 0 for name in profile.ETHEREUM_SCHEDULE}
    bor = {name: 0 if index < bor_prefix else 8 for index, name in enumerate(profile.BOR_SCHEDULE)}
    if gated:
        bor["madhugiriBlock"] = 2
    bor.update({"hampiBlock": None, "sprint": {"0": 2}, "period": {"0": 1}, "producerDelay": {"0": 6},
                "backupMultiplier": {"0": 1}, "stateSyncConfirmationDelay": {"0": 0},
                "coinbase": {"0": "0x" + "00" * 20}, "burntContract": {"0": "0x" + "00" * 20},
                "stateReceiverContract": PRIOR.RECEIVER,
                "validatorContract": "0x0000000000000000000000000000000000001000"})
    eth.update({name: 0 for name in ("homesteadBlock", "eip150Block", "eip155Block", "eip158Block",
                                     "byzantiumBlock", "constantinopleBlock", "petersburgBlock",
                                     "istanbulBlock", "muirGlacierBlock")})
    eth.update({"eip150Hash": "0x" + "00" * 32, "chainId": 15001, "bor": bor})
    return eth


def account_spec(nonce=0, balance=0, storage=None):
    return [nonce, balance, list(storage or ZERO_STORAGE)]


class Builder:
    """Assemble the two synthetic carrier rows over the retained Prague body."""

    @classmethod
    def prepare(cls):
        reports.CrossingNativeReport.setUpClass()
        paired = paired_module.paired_report(reports.CrossingNativeReport.native)
        prague = copy.deepcopy(paired["cases"][2])
        cls.genesis_raw = prague["genesis_raw"]
        cls.genesis, _, cls.genesis_hash = j.h.raw_block(cls.genesis_raw, 20)
        first = copy.deepcopy(prague["crossing"]["blocks"][0])
        cls.header1 = PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(first["raw_block"])))[0]
        cls.header2 = PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(prague["crossing"]["blocks"][1]["raw_block"])))[0]
        cls.base1, cls.base2 = j.integer(cls.header1[15]), j.integer(cls.header2[15])
        cls.oracle1 = j.receipt_oracle(0, cls.base1, 10**19, 0, 10**21)
        cls.oracle2 = j.receipt_oracle(0, cls.base2, cls.oracle1["sender_after"], 7,
                                       cls.oracle1["signer_after"])
        cls.sync_oracle = local_sync_receipt(cls.oracle2["gas"])
        cls.legacy_wire = vectors.signed_wire(0, inputs.fields(0, 1))
        cls.original = local_wire()
        cls.items = local_items()
        cls.codes = {address: first["state_after"][address[2:]]["code"] for address in PRIOR.WATCHED}
        cls.first = cls.extend(first, ())

    @classmethod
    def genesis_state(cls):
        return {address: account_spec() for address in PRIOR.WATCHED} | {PRIOR.SENDER: account_spec(0, 10**19)}

    @classmethod
    def state1(cls):
        return {address: account_spec() for address in PRIOR.WATCHED} | {
            PRIOR.SENDER: account_spec(1, cls.oracle1["sender_after"]),
            PRIOR.SUCCESS: account_spec(0, 7)}

    @classmethod
    def mid_state(cls):
        return {address: account_spec() for address in PRIOR.WATCHED} | {
            PRIOR.SENDER: account_spec(2, cls.oracle2["sender_after"]),
            PRIOR.SUCCESS: account_spec(0, 14)}

    @classmethod
    def final_state(cls, receiver_slot=2, target_storage=None):
        return cls.mid_state() | {
            PRIOR.RECEIVER: account_spec(0, 0, [receiver_slot, 0, 0, 0]),
            PRIOR.TARGET: account_spec(0, 0, list(target_storage or (1, RECEIVER_ADDRESS, len(DATA), PADDED)))}

    @classmethod
    def state_view(cls, accounts):
        return {address[2:]: {"nonce": row[0], "balance": row[1], "code": cls.codes[address],
                              "storage": {str(index): f"{value:064x}" for index, value in enumerate(row[2])}}
                for address, row in accounts.items()}

    @classmethod
    def traced_view(cls, accounts):
        return {"0x" + address[2:]: {"nonce": row[0], "balance": str(row[1]), "code": cls.codes[address],
                                     "storage": {"0x" + f"{index:064x}": f"{value:064x}"
                                                 for index, value in enumerate(row[2])}}
                for address, row in accounts.items()}

    @classmethod
    def account_proofs(cls, accounts):
        leaves = {}
        for address, (nonce, balance, storage) in accounts.items():
            if address == PRIOR.AUTHORITY:
                continue
            slots = {index: value for index, value in enumerate(storage) if value}
            if slots:
                entries = [(c.keccak256(index.to_bytes(32, "big")), c.rlp(num(value)))
                           for index, value in sorted(slots.items())]
                storage_root, storage_proof = make_trie(entries)
            else:
                storage_root, storage_proof = c.EMPTY_ROOT, None
            leaves[address] = (c.rlp([num(nonce), num(balance), storage_root,
                                      bytes.fromhex(j.INITIAL_CODE_HASHES[address])]), storage_root, storage_proof)
        root, proof_for = make_trie([(c.keccak256(bytes.fromhex(address[2:])), leaf)
                                     for address, (leaf, _, _) in leaves.items()])
        rows = {}
        for address in PRIOR.WATCHED:
            if address == PRIOR.AUTHORITY:
                rows[address] = {"address": address, "nonce": "0x0", "balance": "0x0",
                                 "storageHash": "0x" + bytes(32).hex(), "codeHash": "0x" + bytes(32).hex(),
                                 "accountProof": ["0x" + node.hex() for node in
                                                  proof_for(c.keccak256(bytes.fromhex(address[2:])))],
                                 "storageProof": [{"key": hex(index), "value": "0x0", "proof": []}
                                                  for index in range(4)]}
                continue
            (nonce, balance, storage) = accounts[address]
            leaf, storage_root, storage_proof = leaves[address]
            def storage_nodes(index):
                return (["0x" + node.hex() for node in
                         storage_proof(c.keccak256(index.to_bytes(32, "big")))] if storage_proof else [])

            storage_rows = [{"key": hex(index),
                             "value": hex(storage[index]) if storage[index] else "0x0",
                             "proof": storage_nodes(index)}
                            for index in range(4)]
            rows[address] = {"address": address, "nonce": hex(nonce), "balance": hex(balance),
                             "storageHash": "0x" + storage_root.hex(),
                             "codeHash": "0x" + j.INITIAL_CODE_HASHES[address],
                             "accountProof": ["0x" + node.hex() for node in
                                              proof_for(c.keccak256(bytes.fromhex(address[2:])))],
                             "storageProof": storage_rows}
        return root, rows

    @classmethod
    def wire_for(cls, index):
        return cls.legacy_wire if index == 0 else cls.original

    @classmethod
    def receipt_row(cls, oracle, kind, index, cumulative, block_hash, log_offset=0):
        row = {"type": hex(kind), "status": "0x1", "gasUsed": hex(oracle["gas"]),
               "cumulativeGasUsed": hex(cumulative), "effectiveGasPrice": hex(oracle["price"]),
               "logs": [{"address": "0x" + address.hex(), "topics": ["0x" + topic.hex() for topic in topics],
                         "data": "0x" + data.hex(), "transactionHash": "0x" + c.keccak256(cls.wire_for(index)).hex(),
                         "blockHash": block_hash, "blockNumber": "0x2", "transactionIndex": hex(index),
                         "logIndex": hex(log_offset + position),
                         "blockTimestamp": "0x" + num(j.integer(cls.header2[11])).hex(),
                         "removed": False}
                        for position, (address, topics, data) in enumerate(oracle["logs"])],
               "logsBloom": "0x" + oracle["bloom"].hex(),
               "transactionHash": "0x" + c.keccak256(cls.wire_for(index)).hex(), "blockHash": block_hash,
               "blockNumber": "0x2", "transactionIndex": hex(index), "contractAddress": None, "root": None}
        return row

    @classmethod
    def assemble(cls, header, txs, oracles, kinds, header_bloom, accounts):
        """Seal one synthetic block view with proofs, receipts and state."""
        fields = list(header)
        if len(txs) == 2:
            fields[4], proofs = reports.ordered_pair(txs[0], txs[1])
        else:
            fields[4], proofs = single_proof(txs[0])
        receipts, receipt_proofs = [], []
        if oracles:
            wires = [oracle["wire"] for oracle in oracles]
            if len(wires) == 2:
                fields[5], receipt_proofs = reports.ordered_pair(wires[0], wires[1])
            else:
                fields[5], receipt_proofs = single_proof(wires[0])
            cumulative = 0
            log_offset = 0
            for index, oracle in enumerate(oracles):
                cumulative += oracle["gas"]
                receipts.append(cls.receipt_row(oracle, kinds[index], index, cumulative, "pending", log_offset))
                log_offset += len(oracle["logs"])
        else:
            fields[5] = c.EMPTY_ROOT
        state_root, account_rows = cls.account_proofs(accounts)
        fields[3], fields[6], fields[10] = state_root, header_bloom, num(sum(o["gas"] for o in oracles))
        block = seal(fields, txs)
        block["transaction_proofs"] = proofs
        for receipt in receipts:
            receipt["blockHash"] = block["hash"]
            for log in receipt["logs"]:
                log["blockHash"] = block["hash"]
        block["receipts"], block["receipt_proofs"] = receipts, receipt_proofs
        block["state_after"], block["account_proofs"] = cls.state_view(accounts), account_rows
        return block

    @classmethod
    def extend(cls, view, identifiers):
        view["bor_receipt"] = {"present": False, "logs": []}
        view["state_sync"] = cls.sync_rows(identifiers)
        view.setdefault("transactions", [])
        view.setdefault("calls", [])
        return view

    @classmethod
    def sync_rows(cls, identifiers):
        return [{"ID": identifier, "Contract": PRIOR.TARGET if identifier == 1 else PRIOR.REVERT,
                 "Data": base64.b64encode(DATA).decode(),
                 "TxHash": "0x" + identifier.to_bytes(32, "big").hex()} for identifier in identifiers]

    @classmethod
    def live_row(cls, wire, kind, sender, before, after, receipt=None):
        row = {"hash": "0x" + c.keccak256(wire).hex(), "type": kind, "from": sender, "raw": wire.hex(),
               "before": before, "after": after}
        if receipt is not None:
            row["receipt"] = receipt
        return row

    @classmethod
    def call_row(cls, depth, sender, target, call_input, before, after, reverted=False, output="",
                 transaction=None):
        row = {"transaction": transaction or "0x" + c.keccak256(cls.original).hex(), "depth": depth, "type": 241,
               "from": sender, "to": target, "input": call_input.hex(), "before": before, "after": after,
               "reverted": reverted, "output": output}
        if reverted:
            row["error"] = "execution reverted"
        return row


class Records:
    """Durable decision/admission builders mirroring the pinned decider."""

    @staticmethod
    def intent(sequence, kind, old_head, new_head, members, events=True):
        record = {"native_store_id": "0123456789abcdef0123456789abcdef", "producer": "bor.run-1",
                  "sequence": sequence, "kind": kind, "old_head": old_head, "new_head": new_head,
                  "members": [{"observation": f"bor.run-1.{observation:016x}", "block": block["hash"][2:],
                               "exec_ready_digest": "ab" * 32, "child_root": block["root"][2:]}
                              for observation, block in members],
                  "evidence_digest": "cd" * 32, "events": events}
        record["digest"] = PRIOR.intent_digest(record)
        decided = {"sequence": sequence, "intent_digest": record["digest"], "delivery": "BATCH",
                   "digest": PRIOR.decided_digest(record["digest"], "BATCH")}
        prefix = f"extractor-decision-v1/bor.run-1/{sequence:016x}"
        return {prefix + "/intent": json.dumps(record), prefix + "/decided": json.dumps(decided)}

    @staticmethod
    def plain_decisions(blocks):
        records = {"extractor-decision-v1/bor.run-1/counter": json.dumps({"next": len(blocks)})}
        for sequence, block in enumerate(blocks):
            records.update(Records.intent(
                sequence, "IMPORT", {"number": block["number"] - 1, "hash": block["parent"][2:]},
                {"number": block["number"], "hash": block["hash"][2:]}, [(sequence, block)]))
        return records

    @staticmethod
    def reorg_decisions(genesis_hash, candidate, first, second):
        records = {"extractor-decision-v1/bor.run-1/counter": json.dumps({"next": 3})}
        candidate_head = {"number": 1, "hash": candidate["hash"][2:]}
        generated_head = {"number": 1, "hash": first["hash"][2:]}
        # The accepted candidate imports as canonical block 1; the generated
        # block 1 replaces it at the same height; block 2 imports on top.
        records.update(Records.intent(0, "IMPORT", {"number": 0, "hash": genesis_hash[2:]},
                                      candidate_head, [(0, candidate)]))
        records.update(Records.intent(1, "CANONICAL", candidate_head, generated_head, [(1, first)]))
        records.update(Records.intent(2, "IMPORT", generated_head,
                                      {"number": 2, "hash": second["hash"][2:]}, [(2, second)]))
        return records

    @staticmethod
    def admission(frontier):
        prefix = "extractor-admission-v1/bor.run-1/"
        return {prefix + name: json.dumps({"next" if name == "counter" else "sequence": frontier})
                for name in ("counter", "lowwater", "purged")}


def system_frames(receiver_slot=2, target_storage=None):
    """The synthetic root frame, the two receiver commits and nested calls."""
    outer = lambda index, target: local_abi(bytes.fromhex("19494a17"), 1558348306, local_event(index, target))
    inner = lambda index: local_abi(bytes.fromhex("26c53bea"), index, DATA)
    b = Builder
    mid, after_first = b.mid_state(), b.final_state(receiver_slot=1, target_storage=target_storage)
    inner_first = b.final_state(receiver_slot=1, target_storage=ZERO_STORAGE)
    final = b.final_state(receiver_slot=receiver_slot, target_storage=target_storage)
    traced = lambda accounts: b.traced_view(accounts)
    legacy = "0x" + c.keccak256(b.legacy_wire).hex()
    frames = [
        b.call_row(0, PRIOR.SENDER, PRIOR.SUCCESS, b"", traced(b.state1()), traced(b.mid_state()),
                   transaction=legacy),
        b.call_row(0, PRIOR.SYSTEM, PRIOR.RECEIVER, b"", traced(mid), traced(final)),
        b.call_row(1, PRIOR.SYSTEM, PRIOR.RECEIVER, outer(1, PRIOR.TARGET), traced(mid), traced(after_first),
                   output=(1).to_bytes(32, "big").hex()),
        b.call_row(2, PRIOR.RECEIVER, PRIOR.TARGET, inner(1), traced(inner_first), traced(after_first)),
        b.call_row(1, PRIOR.SYSTEM, PRIOR.RECEIVER, outer(2, PRIOR.REVERT), traced(after_first), traced(final),
                   output=(0).to_bytes(32, "big").hex()),
        b.call_row(2, PRIOR.RECEIVER, PRIOR.REVERT, inner(2), traced(final), traced(final),
                   reverted=True, output="00" * 31 + "ee"),
    ]
    return frames


def ordered_proofs(txs):
    """Ordered trie proofs for an arbitrary body list."""
    keys = [c.rlp(num(index) if index else b"") for index in range(len(txs))]
    root, proof_for = make_trie([(key, wire) for key, wire in zip(keys, txs)])
    if root != c.ordered_root(txs):
        raise ValueError("general body root differs")
    return root, [{"index": index, "key": key.hex(), "value": wire.hex(),
                   "nodes": ["0x" + node.hex() for node in proof_for(key)]}
                  for index, (key, wire) in enumerate(zip(keys, txs))]


def rebody(raw_block, txs, gas=21000):
    """Re-seal a refusal candidate body with a consistent transaction root."""
    fields = list(PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(raw_block)))[0])
    fields[4], proofs = ordered_proofs(txs)
    fields[10] = num(gas)
    block = seal(fields, txs)
    block["transaction_proofs"] = proofs
    return block


def refusal_row(name, first):
    changed = [list(item) for item in Builder.items]
    changed[0][3] = (153).to_bytes(32, "big")
    changed_wire = b"\x7f" + c.rlp(changed)
    bodies = {"missing": [Builder.legacy_wire],
              "non-final": [Builder.original, Builder.legacy_wire],
              "mismatched-l1-hash": [Builder.legacy_wire, changed_wire]}
    txs = bodies[name]
    fields = list(Builder.header2)
    fields[0] = bytes.fromhex(first["hash"][2:])
    if len(txs) == 2:
        fields[4], proofs = reports.ordered_pair(txs[0], txs[1])
    else:
        fields[4], proofs = single_proof(txs[0])
    fields[10] = num(21000)
    block = seal(fields, txs)
    error = ("state-sync mismatch: hash mismatch, got 0x" + c.keccak256(changed_wire).hex() +
             " want 0x" + c.keccak256(Builder.original).hex()) if name == "mismatched-l1-hash" else \
            "state-sync mismatch: block body missing state-sync transaction, heimdall reported 2 event(s)"
    return {"case": name, "raw_block": block["raw_block"], "number": 2, "hash": block["hash"],
            "root": block["root"], "parent": block["parent"], "transaction_proofs": proofs, "error": error,
            "head_before": first["hash"], "head_after": first["hash"],
            "decisions": Records.plain_decisions([first]), "admission": Records.admission(2)}


def build_madhugiri_report(pre_target_storage=None):
    """Pair the retained five-case matrix with the two synthetic carrier rows."""
    b = Builder
    paired = paired_module.paired_report(reports.CrossingNativeReport.native)
    final = b.final_state(target_storage=pre_target_storage)
    bor_log = {"address": PRIOR.TARGET[2:], "topics": [j.SYNC_TOPIC.hex()],
               "data": (1).to_bytes(32, "big").hex()}
    pre_second = b.assemble(b.header2, [b.legacy_wire], [b.oracle2], [0], b.oracle2["bloom"], final)
    b.extend(pre_second, (1, 2))
    pre_second["bor_receipt"] = {"present": True, "logs": [bor_log]}
    pre_second["transactions"] = [b.live_row(b.legacy_wire, 0, PRIOR.SENDER,
                                             b.traced_view(b.state1()), b.traced_view(b.mid_state()))]
    # The StateSync effects ride the separate Bor receipt; the only live
    # execution is the legacy transaction's own frame.
    pre_second["calls"] = [b.call_row(0, PRIOR.SENDER, PRIOR.SUCCESS, b"",
                                      b.traced_view(b.state1()), b.traced_view(b.mid_state()),
                                      transaction="0x" + c.keccak256(b.legacy_wire).hex())]
    pre_row = {"name": "PreMadhugiri", "type": 0, "activation_block": 2, "bor_activation": "",
               "config": madhugiri_config(6, False), "genesis_raw": b.genesis_raw,
               "crossing": {"blocks": [copy.deepcopy(b.first), pre_second], "head_hash": pre_second["hash"],
                            "admission": Records.admission(2),
                            "decisions": Records.plain_decisions([b.first, pre_second]),
                            "observed": [1, 2],
                            "keys": {"1": ["bor.run-1.0000000000000000"], "2": ["bor.run-1.0000000000000001"]},
                            "observer_errors": []}}
    sync_receipt = {"status": "0x1", "gasUsed": "0x0", "cumulativeGasUsed": hex(b.oracle2["gas"]),
                    "effectiveGasPrice": "0x0", "logs": [dict(bor_log, address="0x" + bor_log["address"])],
                    "logsBloom": "0x" + b.sync_oracle["bloom"].hex()}
    post_second = b.assemble(b.header2, [b.legacy_wire, b.original], [b.oracle2, b.sync_oracle], [0, 127],
                             bytes(x | y for x, y in zip(b.oracle2["bloom"], b.sync_oracle["bloom"])),
                             b.final_state())
    b.extend(post_second, (1, 2))
    post_second["transactions"] = [
        b.live_row(b.legacy_wire, 0, PRIOR.SENDER, b.traced_view(b.state1()), b.traced_view(b.mid_state())),
        b.live_row(b.original, 127, PRIOR.SYSTEM, b.traced_view(b.mid_state()),
                   b.traced_view(b.final_state()), sync_receipt)]
    post_second["calls"] = system_frames()
    candidate = b.assemble(b.header1, [b.original], [], [], bytes(256), b.genesis_state())
    fields = PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(candidate["raw_block"])))[0]
    fields[3] = b.genesis[3]
    candidate = {**seal(fields, [b.original]), "transaction_proofs": candidate["transaction_proofs"]}
    b.extend(candidate, ())
    candidate["transactions"] = [b.live_row(b.original, 127, PRIOR.SYSTEM,
                                            b.traced_view(b.genesis_state()), b.traced_view(b.genesis_state()))]
    import_record = Records.intent(0, "IMPORT", {"number": 0, "hash": b.genesis_hash[2:]},
                                   {"number": 1, "hash": candidate["hash"][2:]}, [(0, candidate)])
    import_record["extractor-decision-v1/bor.run-1/counter"] = json.dumps({"next": 1})
    negative = {"head_before": b.genesis_hash, "accepted": True,
                "classification": "native-preMadhugiri-observation-no-refusal-credit", "block": candidate,
                "head_hash": candidate["hash"], "decisions": import_record, "admission": Records.admission(1),
                "observed": [1], "keys": {"1": ["bor.run-1.0000000000000000"]}, "observer_errors": []}
    components = {"head_before": post_second["hash"], "head_after": post_second["hash"],
                  "unknown_block_decode": [{"type": value, "error": "rlp: transaction type not supported"}
                                           for value in range(5, 127)]}
    post_row = {"name": "Madhugiri", "type": 127, "activation_block": 2, "bor_activation": "madhugiriBlock",
                "config": madhugiri_config(7, True), "genesis_raw": b.genesis_raw,
                "before_activation": negative,
                "crossing": {"blocks": [copy.deepcopy(b.first), post_second], "head_hash": post_second["hash"],
                             "admission": Records.admission(3),
                             "decisions": Records.reorg_decisions(b.genesis_hash, candidate,
                                                                  b.first, post_second),
                             "observed": [1, 1, 2],
                             "keys": {"1": ["bor.run-1.0000000000000000", "bor.run-1.0000000000000001"],
                                      "2": ["bor.run-1.0000000000000002"]},
                             "observer_errors": []},
                "import_refusals": [refusal_row(name, b.first) for name in
                                    ("missing", "non-final", "mismatched-l1-hash")],
                "components": components}
    report = copy.deepcopy(paired)
    report["cases"].extend((pre_row, post_row))
    return report




def relink(row, second):
    """Refresh every hash-linked metadata field after a block-2 header edit."""
    crossing = row["crossing"]
    for block in crossing["blocks"]:
        header = PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(block["raw_block"])))[0]
        for receipt in block.get("receipts", []):
            receipt["blockHash"] = block["hash"]
            for log in receipt["logs"]:
                log["blockHash"] = block["hash"]
                log["blockTimestamp"] = "0x" + num(j.integer(header[11])).hex()
    crossing["head_hash"] = second["hash"]
    candidate = row["before_activation"]["block"] if "before_activation" in row else None
    if candidate is not None:
        row["before_activation"]["head_hash"] = candidate["hash"]
        crossing["decisions"] = Records.reorg_decisions(Builder.genesis_hash, candidate,
                                                        crossing["blocks"][0], second)
    else:
        crossing["decisions"] = Records.plain_decisions(crossing["blocks"])
    for refusal in row.get("import_refusals", []):
        refusal["head_before"] = refusal["head_after"] = crossing["blocks"][0]["hash"]
        refusal["decisions"] = Records.plain_decisions([crossing["blocks"][0]])
    if "components" in row:
        row["components"]["head_before"] = row["components"]["head_after"] = second["hash"]


def rebuild_second(row, txs, oracles=None, kinds=None, bloom=None, accounts=None, header=None):
    """Re-seal the gated crossing block with a different body or state."""
    b = Builder
    second = b.assemble(header or b.header2, txs,
                        oracles if oracles is not None else [b.oracle2, b.sync_oracle],
                        kinds if kinds is not None else [0, 127],
                        bloom if bloom is not None else bytes(x | y for x, y in zip(
                            b.oracle2["bloom"], b.sync_oracle["bloom"])),
                        accounts if accounts is not None else b.final_state())
    original = row["crossing"]["blocks"][1]
    b.extend(second, (1, 2))
    second["transactions"], second["calls"] = original["transactions"], original["calls"]
    row["crossing"]["blocks"][1] = second
    relink(row, second)


def variant_post_second(receiver_slot=2, target_storage=None):
    """A gated crossing block whose final commitments move consistently."""
    b = Builder
    accounts = b.final_state(receiver_slot=receiver_slot, target_storage=target_storage)
    second = b.assemble(b.header2, [b.legacy_wire, b.original], [b.oracle2, b.sync_oracle], [0, 127],
                        bytes(x | y for x, y in zip(b.oracle2["bloom"], b.sync_oracle["bloom"])), accounts)
    b.extend(second, (1, 2))
    sync_receipt = {"status": "0x1", "gasUsed": "0x0", "cumulativeGasUsed": hex(b.oracle2["gas"]),
                    "effectiveGasPrice": "0x0",
                    "logs": [{"address": "0x" + PRIOR.TARGET[2:], "topics": ["0x" + j.SYNC_TOPIC.hex()],
                              "data": "0x" + (1).to_bytes(32, "big").hex()}],
                    "logsBloom": "0x" + b.sync_oracle["bloom"].hex()}
    second["transactions"] = [
        b.live_row(b.legacy_wire, 0, PRIOR.SENDER, b.traced_view(b.state1()), b.traced_view(b.mid_state())),
        b.live_row(b.original, 127, PRIOR.SYSTEM, b.traced_view(b.mid_state()),
                   b.traced_view(accounts), sync_receipt)]
    second["calls"] = system_frames(receiver_slot=receiver_slot, target_storage=target_storage)
    return second


def variant_pre_second(receiver_slot=2, target_storage=None):
    """A pre-gate crossing block whose final commitments move consistently."""
    b = Builder
    accounts = b.final_state(receiver_slot=receiver_slot, target_storage=target_storage)
    second = b.assemble(b.header2, [b.legacy_wire], [b.oracle2], [0], b.oracle2["bloom"], accounts)
    b.extend(second, (1, 2))
    second["bor_receipt"] = {"present": True,
                             "logs": [{"address": PRIOR.TARGET[2:], "topics": [j.SYNC_TOPIC.hex()],
                                       "data": (1).to_bytes(32, "big").hex()}]}
    second["transactions"] = [b.live_row(b.legacy_wire, 0, PRIOR.SENDER,
                                         b.traced_view(b.state1()), b.traced_view(b.mid_state()))]
    # The StateSync effects ride the separate Bor receipt; the only live
    # execution is the legacy transaction's own frame.
    second["calls"] = [b.call_row(0, PRIOR.SENDER, PRIOR.SUCCESS, b"",
                                  b.traced_view(b.state1()), b.traced_view(b.mid_state()),
                                  transaction="0x" + c.keccak256(b.legacy_wire).hex())]
    return second


class MadhugiriReportTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        Builder.prepare()
        cls.report = build_madhugiri_report()

    def setUp(self):
        # The pinned native base-fee pair binds real observations; the
        # synthetic fixtures carry the builder's own pair.
        original = j.MADHUGIRI_BASE_FEES
        j.MADHUGIRI_BASE_FEES = (Builder.base1, Builder.base2)
        self.addCleanup(setattr, j, "MADHUGIRI_BASE_FEES", original)

    def reject(self, change, index=6):
        report = copy.deepcopy(self.report)
        change(report["cases"][index])
        self.assertEqual(j.judge(report)["result"], "FAIL")

    def crossing(self, row):
        return row["crossing"]

    def second(self, row):
        return row["crossing"]["blocks"][1]

    def test_madhugiri_matrix_passes(self):
        self.assertEqual(j.judge(self.report), VERDICT)

    def test_paired_prefix_alone_still_passes(self):
        report = copy.deepcopy(self.report)
        report["cases"] = report["cases"][:5]
        self.assertEqual(j.judge(report)["result"], "PASS")

    def test_case_identity(self):
        for field, value in (("name", "Madhugiri"), ("type", 127), ("type", True), ("type", 2.0),
                             ("activation_block", 3), ("activation_block", 2.0),
                             ("bor_activation", "madhugiriBlock"), ("bor_activation", "delhiBlock")):
            with self.subTest(pre=field, value=value):
                self.reject(lambda r, f=field, v=value: r.update({f: v}), index=5)
        for field, value in (("name", "PreMadhugiri"), ("type", 0), ("type", True),
                             ("activation_block", 3), ("bor_activation", ""),
                             ("bor_activation", "madhugiriProBlock")):
            with self.subTest(gated=field, value=value):
                self.reject(lambda r, f=field, v=value: r.update({f: v}))
        self.reject(lambda r: r.pop("bor_activation"), index=5)
        self.reject(lambda r: r.pop("bor_activation"))

    def test_matrix_shape(self):
        for change in (lambda r: r.pop(), lambda r: r.pop(5), lambda r: r.append(copy.deepcopy(r[6])),
                       lambda r: r.__setitem__(slice(5, 7), [r[6], r[5]])):
            with self.subTest(change=change):
                report = copy.deepcopy(self.report)
                change(report["cases"])
                self.assertEqual(j.judge(report)["result"], "FAIL")

    def test_configuration_gate_and_sprint(self):
        for value in (0, 3, True, 2.0):
            with self.subTest(pre_gate=value):
                self.reject(lambda r, v=value: r["config"]["bor"].update(madhugiriBlock=v), index=5)
        for value in ({"0": 4}, {"0": 2.0}, 2, "2", {0: 2}):
            with self.subTest(pre_sprint=value):
                self.reject(lambda r, v=value: r["config"]["bor"].update(sprint=v), index=5)
        self.reject(lambda r: r["config"]["bor"].pop("madhugiriBlock"), index=5)
        self.reject(lambda r: r["config"]["bor"].pop("sprint"), index=5)
        self.reject(lambda r: r["config"]["bor"].update(rioBlock=8), index=5)
        self.reject(lambda r: r["config"]["bor"].update(madhugiriProBlock=2), index=5)
        for value in (0, 3, 8, True, 2.0):
            with self.subTest(gate=value):
                self.reject(lambda r, v=value: r["config"]["bor"].update(madhugiriBlock=v))
        for value in ({"0": 4}, 2):
            with self.subTest(gated_sprint=value):
                self.reject(lambda r, v=value: r["config"]["bor"].update(sprint=v))
        self.reject(lambda r: r["config"]["bor"].pop("sprint"))
        self.reject(lambda r: r["config"].update(pragueBlock=8))
        self.reject(lambda r: r["config"].update(chainId=1))

    def test_gated_controls_do_not_leak_into_the_pre_gate_row(self):
        self.reject(lambda r: r.update(before_activation={}), index=5)
        self.reject(lambda r: r.update(import_refusals=[]), index=5)
        self.reject(lambda r: r.update(components={}), index=5)

    def test_pre_gate_live_rows_are_the_legacy_frame_alone(self):
        self.reject(lambda r: self.second(r).update(calls=[]), index=5)
        self.reject(lambda r: self.second(r)["calls"].append(dict(self.second(r)["calls"][0])), index=5)
        self.reject(lambda r: self.second(r)["calls"][0].update(reverted=True), index=5)
        self.reject(lambda r: self.second(r)["calls"][0].update(to=PRIOR.RECEIVER), index=5)
        self.reject(lambda r: self.second(r)["calls"][0].update(depth=1), index=5)

    def test_pending_base_fee_pair_binding(self):
        original = j.MADHUGIRI_BASE_FEES
        self.addCleanup(setattr, j, "MADHUGIRI_BASE_FEES", original)
        j.MADHUGIRI_BASE_FEES = (Builder.base1, Builder.base2)
        self.assertEqual(j.judge(self.report), VERDICT)
        for value in ((Builder.base1 + 1, Builder.base2), (Builder.base1, Builder.base2 - 1),
                      (Builder.base1, Builder.base2, 0)):
            with self.subTest(value=value):
                j.MADHUGIRI_BASE_FEES = value
                self.assertEqual(j.judge(self.report)["result"], "FAIL")
        j.MADHUGIRI_BASE_FEES = None
        self.assertEqual(j.judge(self.report), VERDICT)

    def test_gas_limit_and_timestamp_sequence(self):
        for field, value in ((9, 20_000_000), (11, int.from_bytes(Builder.header2[11], "big") + 2)):
            with self.subTest(field=field):
                def change(row, f=field, v=value):
                    second = self.second(row)
                    reports.edit_header(second, f, num(v))
                    relink(row, second)
                self.reject(change)

    def test_carrier_body_encoding(self):
        b = Builder
        swapped = [b.original, b.legacy_wire]
        self.reject(lambda r: rebuild_second(r, swapped))
        self.reject(lambda r: rebuild_second(r, [b.legacy_wire], [b.oracle2], [0],
                                             b.oracle2["bloom"], b.final_state()))
        items = [list(item) for item in b.items]
        variants = {
            "l1-hash": [items[0][:3] + [(3).to_bytes(32, "big")]] + items[1:],
            "reversed": list(reversed(items)),
            "data": [items[0][:2] + [b"other"] + items[0][3:]] + items[1:],
            "contract": [items[0][:1] + [bytes.fromhex(PRIOR.REVERT[2:])] + items[0][2:]] + items[1:],
            "extra": items + [[b"\x03", bytes.fromhex(PRIOR.TARGET[2:]), DATA, (3).to_bytes(32, "big")]],
        }
        for name, changed in variants.items():
            with self.subTest(variant=name):
                wire = b"\x7f" + c.rlp(changed)
                self.reject(lambda r, w=wire: rebuild_second(r, [b.legacy_wire, w]))
        legacy = vectors.signed_wire(0, inputs.fields(0, 0))
        self.reject(lambda r: rebuild_second(r, [legacy, b.original]))
        self.reject(lambda r: rebuild_second(r, [b.legacy_wire, b.original],
                                             accounts=b.final_state(receiver_slot=3)))
        self.reject(lambda r: rebuild_second(r, [b.legacy_wire, b.original],
                                             accounts=b.final_state(target_storage=ZERO_STORAGE)))

    def test_receipts(self):
        def edit(field, value, position=1):
            self.reject(lambda r: self.second(r)["receipts"][position].update({field: value}))
        for field, value in (("type", "0x0"), ("status", "0x0"), ("gasUsed", "0x1"), ("gasUsed", "0x5208"),
                             ("cumulativeGasUsed", "0x1"), ("cumulativeGasUsed", "0x0"),
                             ("effectiveGasPrice", "0x1"), ("effectiveGasPrice", "0x5208"),
                             ("transactionIndex", "0x0"), ("blockNumber", "0x1"),
                             ("transactionHash", "0x" + "00" * 32)):
            with self.subTest(field=field, value=value):
                edit(field, value)
        edit("logs", [])
        edit("logsBloom", "00" * 256)
        edit("status", "0x0", position=0)
        edit("effectiveGasPrice", "0x0", position=0)

    def test_log_identity(self):
        for field, value in (("logIndex", "0x1"), ("transactionIndex", "0x0"), ("removed", True),
                             ("blockTimestamp", "0x1")):
            with self.subTest(field=field):
                self.reject(lambda r, f=field, v=value:
                            self.second(r)["receipts"][1]["logs"][0].update({f: v}))

    def test_header_gas_and_bloom(self):
        for field, value in ((10, 21001), (6, bytes(256))):
            with self.subTest(field=field):
                def change(row, f=field, v=value):
                    second = self.second(row)
                    reports.edit_header(second, f, num(v) if isinstance(v, int) else v)
                    relink(row, second)
                self.reject(change)

    def test_bor_receipt_carrier_difference(self):
        log = [{"address": PRIOR.TARGET[2:], "topics": [j.SYNC_TOPIC.hex()],
                "data": (1).to_bytes(32, "big").hex()}]
        wrong = [{"address": PRIOR.REVERT[2:], "topics": [j.SYNC_TOPIC.hex()],
                  "data": (2).to_bytes(32, "big").hex()}]
        self.reject(lambda r: self.second(r)["bor_receipt"].update(present=True))
        self.reject(lambda r: self.second(r)["bor_receipt"].update(logs=log))
        self.reject(lambda r: self.second(r)["bor_receipt"].update(present=False), index=5)
        self.reject(lambda r: self.second(r)["bor_receipt"].update(logs=wrong), index=5)
        self.reject(lambda r: self.second(r)["bor_receipt"].update(logs=[]), index=5)
        self.reject(lambda r: r["crossing"]["blocks"][0]["bor_receipt"].update(present=True), index=5)

    def test_state_sync_rows(self):
        def rows(row, block=1):
            return row["crossing"]["blocks"][block]["state_sync"]
        for field, value in (("ID", 3), ("Data", "QQ=="), ("TxHash", "0x" + "00" * 31 + "99"),
                             ("Contract", PRIOR.REVERT), ("extra", 1)):
            with self.subTest(field=field):
                self.reject(lambda r, f=field, v=value: rows(r)[0].update({f: v}), index=5)
        self.reject(lambda r: rows(r).pop(), index=5)
        self.reject(lambda r: rows(r).reverse(), index=5)
        self.reject(lambda r: rows(r, 0).append(dict(rows(r)[0])), index=5)

    def test_live_rows(self):
        def live(row, index=1):
            return self.second(row)["transactions"][index]
        self.reject(lambda r: live(r).update(**{"from": PRIOR.SENDER}))
        self.reject(lambda r: live(r).update(type=0))
        self.reject(lambda r: live(r).update(raw=b"\x7f\xc0".hex()))
        self.reject(lambda r: live(r).update(hash="0x" + "00" * 32))
        self.reject(lambda r: live(r, 0).update(**{"from": PRIOR.SYSTEM}))
        self.reject(lambda r: live(r).update(before=Builder.traced_view(Builder.state1())))
        self.reject(lambda r: live(r).update(after=Builder.traced_view(Builder.mid_state())))
        self.reject(lambda r: live(r, 0).update(after=Builder.traced_view(Builder.final_state())))
        for field, value in (("gasUsed", "0x1"), ("cumulativeGasUsed", "0x0"), ("status", "0x0"),
                             ("effectiveGasPrice", "0x1"), ("logs", [])):
            with self.subTest(receipt=field):
                self.reject(lambda r, f=field, v=value: live(r)["receipt"].update({f: v}))
        self.reject(lambda r: self.second(r)["transactions"].pop())

    def test_call_frames(self):
        def calls(row):
            return self.second(row)["calls"]

        def frame(index, **changes):
            return lambda r: calls(r).__setitem__(index, dict(calls(r)[index], **changes))
        self.reject(frame(0, **{"from": PRIOR.SYSTEM}))
        self.reject(frame(0, to=PRIOR.TARGET))
        self.reject(frame(0, input="00"))
        self.reject(frame(0, transaction="0x" + "00" * 32))
        self.reject(frame(0, type=240))
        self.reject(lambda r: [call.update(depth=call["depth"] + 1) for call in calls(r)])
        self.reject(frame(1, input="00"))
        self.reject(frame(1, input=j.h.abi_bytes(bytes.fromhex("19494a17"), 1558348307,
                                                  j.h.clerk_event(2, PRIOR.TARGET)).hex()))
        self.reject(frame(1, after=None))
        self.reject(frame(2, output="00"))
        self.reject(frame(2, input="00"))
        self.reject(frame(2, input=j.h.abi_bytes(bytes.fromhex("26c53bea"), 2, DATA).hex()))
        self.reject(frame(2, output="00"))
        self.reject(frame(3, before=None))
        self.reject(frame(4, reverted=False, output=""))
        self.reject(frame(4, after=None))
        self.reject(lambda r: calls(r).pop())
        self.reject(lambda r: self.second(r).update(calls=[]))

    def test_pre_gate_live_rows(self):
        self.reject(lambda r: self.second(r)["transactions"].append(
            dict(self.second(r)["transactions"][0])), index=5)
        self.reject(lambda r: self.second(r).update(calls=[{}]), index=5)

    def test_observation_candidate(self):
        def negative(row):
            return row["before_activation"]
        self.reject(lambda r: negative(r).update(accepted=False))
        self.reject(lambda r: negative(r).update(classification="native-observation"))
        self.reject(lambda r: negative(r).update(error="failed"))
        self.reject(lambda r: negative(r).update(head_before="0x" + "00" * 32))
        self.reject(lambda r: negative(r).update(head_hash="0x" + "00" * 32))
        self.reject(lambda r: negative(r).update(observed=[1, 2]))
        self.reject(lambda r: negative(r).update(keys={}))
        self.reject(lambda r: negative(r).update(observer_errors=["x"]))
        self.reject(lambda r: negative(r).update(decisions={}))
        self.reject(lambda r: negative(r).update(admission=Records.admission(2)))
        for field, value in ((3, bytes(32)), (10, b"\x01"), (5, c.keccak256(b"")), (6, b"\x01" + bytes(255))):
            with self.subTest(field=field):
                def change(row, f=field, v=value):
                    block = negative(row)["block"]
                    reports.edit_header(block, f, v)
                    negative(row)["head_hash"] = block["hash"]
                self.reject(change)
        self.reject(lambda r: negative(r)["block"].update(
            raw_block=c.rlp([PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(negative(r)["block"]["raw_block"])))[0],
                             [Builder.legacy_wire], []]).hex()))

    def test_reorg_records(self):
        def crossing(row):
            return row["crossing"]
        self.reject(lambda r: crossing(r).update(head_hash="0x" + "00" * 32))
        self.reject(lambda r: crossing(r).update(admission=Records.admission(2)))
        self.reject(lambda r: crossing(r).update(decisions=Records.plain_decisions(crossing(r)["blocks"])))
        self.reject(lambda r: crossing(r).update(observed=[1, 2]))
        self.reject(lambda r: crossing(r).update(keys={"1": ["bor.run-1.0000000000000000"]}))
        self.reject(lambda r: crossing(r).update(observer_errors=["x"]))
        self.reject(lambda r: crossing(r)["decisions"].pop("extractor-decision-v1/bor.run-1/0000000000000001/intent"))
        self.reject(lambda r: crossing(r)["decisions"].update(
            {"extractor-decision-v1/bor.run-1/counter": json.dumps({"next": 2})}))
        for field, value in (("kind", "IMPORT"), ("events", False), ("old_head", {"number": 0, "hash": "00" * 32}),
                             ("new_head", {"number": 1, "hash": "00" * 32})):
            with self.subTest(field=field, value=value):
                def change(row, f=field, v=value):
                    key = "extractor-decision-v1/bor.run-1/0000000000000001/intent"
                    entry = json.loads(crossing(row)["decisions"][key])
                    entry[f] = v
                    entry["digest"] = PRIOR.intent_digest(entry)
                    crossing(row)["decisions"][key] = json.dumps(entry)
                    decided = json.loads(crossing(row)["decisions"][key.replace("/intent", "/decided")])
                    decided["intent_digest"] = entry["digest"]
                    decided["digest"] = PRIOR.decided_digest(entry["digest"], "BATCH")
                    crossing(row)["decisions"][key.replace("/intent", "/decided")] = json.dumps(decided)
                self.reject(change)

    def test_pre_gate_records(self):
        def crossing(row):
            return row["crossing"]
        self.reject(lambda r: crossing(r).update(head_hash=crossing(r)["blocks"][0]["hash"]), index=5)
        self.reject(lambda r: crossing(r).update(admission=Records.admission(3)), index=5)
        self.reject(lambda r: crossing(r).update(admission=Records.admission(1)), index=5)
        self.reject(lambda r: crossing(r).update(observed=[1, 1, 2]), index=5)
        self.reject(lambda r: crossing(r).update(observer_errors=["x"]), index=5)
        self.reject(lambda r: crossing(r)["decisions"].pop(
            "extractor-decision-v1/bor.run-1/0000000000000000/intent"), index=5)

    def test_import_refusals(self):
        def refusals(row):
            return row["import_refusals"]
        b = Builder
        self.reject(lambda r: refusals(r).pop())
        self.reject(lambda r: refusals(r).__setitem__(0, dict(refusals(r)[0], case="duplicate-body")))
        self.reject(lambda r: refusals(r)[0].update(control_error="seed failure"))
        self.reject(lambda r: refusals(r)[0].update(error="state-sync mismatch: altered detail"))
        self.reject(lambda r: refusals(r)[1].update(error="state-sync mismatch: altered detail"))
        self.reject(lambda r: refusals(r)[0].update(head_after="0x" + "00" * 32))
        self.reject(lambda r: refusals(r)[0].update(head_before="0x" + "00" * 32))
        self.reject(lambda r: refusals(r)[0].update(admission=Records.admission(3)))
        self.reject(lambda r: refusals(r)[0].update(decisions=Records.plain_decisions(r["crossing"]["blocks"])))
        self.reject(lambda r: refusals(r)[0].update(
            **rebody(refusals(r)[0]["raw_block"], [b.legacy_wire, b.original])))
        rebuilt = b"\x7f" + c.rlp([list(item) for item in b.items])
        changed99 = [list(item) for item in b.items]
        changed99[0][3] = (153).to_bytes(32, "big")
        rebuilt99 = b"\x7f" + c.rlp(changed99)
        self.reject(lambda r: refusals(r)[2].update(
            error="state-sync mismatch: hash mismatch, got 0x" + c.keccak256(rebuilt).hex() +
                  " want 0x" + c.keccak256(b.original).hex()))
        self.reject(lambda r: refusals(r)[2].update(**rebody(refusals(r)[2]["raw_block"], [b.legacy_wire, rebuilt])))
        self.reject(lambda r: refusals(r)[2].update(**rebody(refusals(r)[2]["raw_block"], [b.legacy_wire])))
        self.reject(lambda r: refusals(r)[2].update(**rebody(refusals(r)[2]["raw_block"],
                                                             [b.legacy_wire, rebuilt99, b.legacy_wire])))
        self.reject(lambda r: refusals(r)[2].update(**rebody(refusals(r)[2]["raw_block"],
                                                             [rebuilt99, rebuilt99])))
        changed98 = [list(item) for item in b.items]
        changed98[1][3] = (152).to_bytes(32, "big")
        rebuilt98 = b"\x7f" + c.rlp(changed98)
        self.reject(lambda r: refusals(r)[2].update(
            error="state-sync mismatch: hash mismatch, got 0x" + c.keccak256(rebuilt98).hex() +
                  " want 0x" + c.keccak256(b.original).hex(),
            **rebody(refusals(r)[2]["raw_block"], [b.legacy_wire, rebuilt98])))
        nonce_zero = vectors.signed_wire(0, inputs.fields(0, 0))
        self.reject(lambda r: refusals(r)[1].update(**rebody(refusals(r)[1]["raw_block"], [b.original, nonce_zero])))
        self.reject(lambda r: refusals(r)[1].update(**rebody(refusals(r)[1]["raw_block"],
                                                             [rebuilt99, b.legacy_wire])))
        self.reject(lambda r: refusals(r)[1].update(**rebody(refusals(r)[1]["raw_block"],
                                                             [b.original, b.legacy_wire, b.legacy_wire])))

    def test_decode_sweep(self):
        def components(row):
            return row["components"]
        self.reject(lambda r: components(r)["unknown_block_decode"].pop())
        self.reject(lambda r: components(r)["unknown_block_decode"].__setitem__(0, {"type": 4, "error": "x"}))
        self.reject(lambda r: components(r)["unknown_block_decode"].__setitem__(
            0, {"type": 5, "error": "decoding failed"}))
        self.reject(lambda r: components(r).update(head_after="0x" + "00" * 32))
        self.reject(lambda r: components(r).update(head_before="0x" + "00" * 32))

    def test_reorg_intent_internals(self):
        def crossing(row):
            return row["crossing"]

        def edit_intent(field, value, sequence=1, part="intent"):
            def change(row):
                key = f"extractor-decision-v1/bor.run-1/{sequence:016x}"
                if part == "decided":
                    decided = json.loads(crossing(row)["decisions"][key + "/decided"])
                    decided[field] = value
                    crossing(row)["decisions"][key + "/decided"] = json.dumps(decided)
                    return
                entry = json.loads(crossing(row)["decisions"][key + "/intent"])
                entry[field] = value
                entry["digest"] = PRIOR.intent_digest(entry)
                crossing(row)["decisions"][key + "/intent"] = json.dumps(entry)
                decided = json.loads(crossing(row)["decisions"][key + "/decided"])
                decided["intent_digest"] = entry["digest"]
                decided["digest"] = PRIOR.decided_digest(entry["digest"], "BATCH")
                crossing(row)["decisions"][key + "/decided"] = json.dumps(decided)
            return change
        for field, value in (("native_store_id", "ab" * 16), ("producer", "bor.other"),
                             ("sequence", 0)):
            with self.subTest(field=field):
                self.reject(edit_intent(field, value))
        self.reject(edit_intent("members", []))
        self.reject(edit_intent("extra", 1))
        self.reject(edit_intent("events", False))
        self.reject(lambda r: crossing(r)["decisions"].update(
            {"extractor-decision-v1/bor.run-1/unexpected": "{}"}))
        def stale_intent_digest(row):
            key = "extractor-decision-v1/bor.run-1/0000000000000001"
            intent = json.loads(crossing(row)["decisions"][key + "/intent"])
            intent["digest"] = "ab" * 32
            crossing(row)["decisions"][key + "/intent"] = json.dumps(intent)
            decided = json.loads(crossing(row)["decisions"][key + "/decided"])
            decided["intent_digest"] = "ab" * 32
            decided["digest"] = PRIOR.decided_digest("ab" * 32, "BATCH")
            crossing(row)["decisions"][key + "/decided"] = json.dumps(decided)
        self.reject(stale_intent_digest)
        for field, value in (("sequence", 0), ("intent_digest", "ab" * 32), ("delivery", "REPLAY"), ("digest", "ab" * 32)):
            with self.subTest(decided=field):
                self.reject(edit_intent(field, value, part="decided"))
        self.reject(lambda r: crossing(r)["decisions"].__setitem__(
            "extractor-decision-v1/bor.run-1/0000000000000001/decided",
            json.dumps({**json.loads(crossing(r)["decisions"]["extractor-decision-v1/bor.run-1/0000000000000001/decided"]),
                        "extra": 1})))
        for field, value in (("observation", "bor.run-1.0000000000000009"), ("block", "ab" * 32),
                             ("child_root", "ab" * 32), ("extra", 1)):
            with self.subTest(member=field):
                def change(row, f=field, v=value):
                    key = "extractor-decision-v1/bor.run-1/0000000000000002/intent"
                    entry = json.loads(crossing(row)["decisions"][key])
                    entry["members"][0][f] = v
                    entry["digest"] = PRIOR.intent_digest(entry)
                    crossing(row)["decisions"][key] = json.dumps(entry)
                    decided = json.loads(crossing(row)["decisions"]["extractor-decision-v1/bor.run-1/0000000000000002/decided"])
                    decided["intent_digest"] = entry["digest"]
                    decided["digest"] = PRIOR.decided_digest(entry["digest"], "BATCH")
                    crossing(row)["decisions"]["extractor-decision-v1/bor.run-1/0000000000000002/decided"] = json.dumps(decided)
                self.reject(change)

    def test_genesis_and_block_count(self):
        def reseat_genesis(row, mutate):
            body = PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(row["genesis_raw"])))
            mutate(body)
            row["genesis_raw"] = c.rlp(body).hex()
            first = row["crossing"]["blocks"][0]
            reports.edit_header(first, 0, c.keccak256(c.rlp(body[0])))
            second = row["crossing"]["blocks"][1]
            reports.edit_header(second, 0, bytes.fromhex(first["hash"][2:]))
            relink(row, second)
        self.reject(lambda r: reseat_genesis(r, lambda b: b[0].__setitem__(8, b"\x01")), index=5)
        self.reject(lambda r: reseat_genesis(r, lambda b: b[0].__setitem__(0, bytes([1]) * 32)), index=5)

        def add_genesis_tx(body):
            wire = j.octets(self.report["cases"][5]["crossing"]["blocks"][0]["transaction_proofs"][0]["value"])
            body[1] = [PRIOR.plain_rlp(c.rlp_list(wire))]
            body[0][4] = c.ordered_root([wire])
        self.reject(lambda r: reseat_genesis(r, add_genesis_tx), index=5)
        self.reject(lambda r: r["crossing"]["blocks"].pop(), index=5)

    def test_non_list_view_shapes(self):
        self.reject(lambda r: r["crossing"]["blocks"][0].update(state_sync=()), index=5)
        self.reject(lambda r: r["crossing"]["blocks"][1].update(
            transactions=tuple(r["crossing"]["blocks"][1]["transactions"])))
        self.reject(lambda r: r["crossing"]["blocks"][1].update(
            receipts=tuple(r["crossing"]["blocks"][1]["receipts"])))
        self.reject(lambda r: r["crossing"]["blocks"][1].update(
            receipt_proofs=tuple(r["crossing"]["blocks"][1]["receipt_proofs"])))

    def test_plain_admission_domains(self):
        def crossing(row):
            return row["crossing"]
        for value in (True, 1.0, 3.0):
            with self.subTest(value=value):
                self.reject(lambda r, v=value: crossing(r)["admission"].update(
                    {"extractor-admission-v1/bor.run-1/counter": json.dumps({"next": v})}), index=5)

    def test_numeric_alias_domains(self):
        self.reject(lambda r: r.update({"type": 0.0}), index=5)
        self.reject(lambda r: r["crossing"]["admission"].update(
            {"extractor-admission-v1/bor.run-1/counter": json.dumps({"next": 2.0})}), index=5)
        self.reject(lambda r: r["crossing"].update(observed=[1.0, 2]), index=5)
        self.reject(lambda r: r["before_activation"]["admission"].update(
            {"extractor-admission-v1/bor.run-1/purged": json.dumps({"sequence": 2})}))

    def test_genesis_parent_and_first_timestamp(self):
        def reseat(row, field, value, retime=False):
            body = PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(row["genesis_raw"])))
            body[0][field] = value
            row["genesis_raw"] = c.rlp(body).hex()
            first = row["crossing"]["blocks"][0]
            reports.edit_header(first, 0, c.keccak256(c.rlp(body[0])))
            second = row["crossing"]["blocks"][1]
            reports.edit_header(second, 0, bytes.fromhex(first["hash"][2:]))
            relink(row, second)
        self.reject(lambda r: reseat(r, 0, bytes([1]) * 32), index=5)
        def shift_first(row):
            first = row["crossing"]["blocks"][0]
            header = PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(first["raw_block"])))[0]
            reports.edit_header(first, 11, num(j.integer(header[11]) + 1))
            second = row["crossing"]["blocks"][1]
            reports.edit_header(second, 0, bytes.fromhex(first["hash"][2:]))
            reports.edit_header(second, 11, num(j.integer(header[11]) + 2))
            relink(row, second)
        self.reject(shift_first, index=5)

    def test_wrong_envelope_tag(self):
        b = Builder
        wire = bytes([126]) + b.original[1:]
        tagged = "0x" + c.keccak256(wire).hex()
        second = b.assemble(b.header2, [b.legacy_wire, wire], [b.oracle2, b.sync_oracle], [0, 127],
                            bytes(x | y for x, y in zip(b.oracle2["bloom"], b.sync_oracle["bloom"])),
                            b.final_state())
        b.extend(second, (1, 2))
        original = self.report["cases"][6]["crossing"]["blocks"][1]
        second["transactions"] = [dict(row, hash=tagged, type=126, raw=wire.hex())
                                  if row["type"] == 127 else row for row in original["transactions"]]
        # Retag only the system frames: the legacy frame keeps its own
        # transaction hash, so the envelope conjunct alone is isolated.
        system_hash = "0x" + c.keccak256(b.original).hex()
        second["calls"] = [dict(call, transaction=tagged) if call["transaction"] == system_hash else call
                           for call in original["calls"]]
        receipt = second["receipts"][1]
        receipt["type"] = "0x7e"
        receipt["transactionHash"] = tagged
        for log in receipt["logs"]:
            log["transactionHash"] = tagged

        def change(row):
            row["crossing"]["blocks"][1] = second
            relink(row, second)
        self.reject(change)

    def test_candidate_envelope_tag(self):
        b = Builder
        wire = bytes([126]) + b.original[1:]
        fields = list(PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(
            self.report["cases"][6]["before_activation"]["block"]["raw_block"])))[0])
        fields[4], proofs = single_proof(wire)
        candidate = {**seal(fields, [wire]), "transaction_proofs": proofs,
                     "bor_receipt": {"present": False, "logs": []}, "state_sync": [],
                     "transactions": [], "calls": []}

        def change(row):
            row["before_activation"]["block"] = candidate
            row["before_activation"]["head_hash"] = candidate["hash"]
            relink(row, row["crossing"]["blocks"][1])
        self.reject(change)

    def test_receipt_and_log_metadata(self):
        self.reject(lambda r: self.second(r)["receipts"][1].update(blockHash="0x" + "00" * 32))
        self.reject(lambda r: self.second(r)["receipts"][0].update(blockHash="0x" + "00" * 32))
        self.reject(lambda r: self.second(r)["receipts"][1]["logs"][0].update(
            transactionHash="0x" + "00" * 32))
        self.reject(lambda r: self.second(r)["receipts"][1]["logs"][0].update(
            blockHash="0x" + "00" * 32))
        self.reject(lambda r: self.second(r)["receipts"][1]["logs"][0].update(
            blockNumber="0x1"))
        self.reject(lambda r: self.second(r)["receipts"][0]["logs"][0].update(
            logIndex="0x1"))

    def test_extra_receipt_row(self):
        def change(row):
            second = self.second(row)
            second["receipts"].append(copy.deepcopy(second["receipts"][1]))
            relink(row, second)
        self.reject(change)

    def test_authenticated_wire_with_wrong_payload(self):
        def change(row):
            second = self.second(row)
            wrong = b"\x7f" + c.rlp([b"\x01", num(21000), bytes(256), []])
            root, proofs = reports.ordered_pair(Builder.oracle2["wire"], wrong)
            reports.edit_header(second, 5, root)
            second["receipt_proofs"] = proofs
            relink(row, second)
        self.reject(change)

    def test_state_sync_id_domain(self):
        self.reject(lambda r: self.second(r)["state_sync"][0].update(ID=True), index=5)

    def test_frame_fields(self):
        def calls(row):
            return self.second(row)["calls"]

        def frame(index, **changes):
            return lambda r: calls(r).__setitem__(index, dict(calls(r)[index], **changes))
        self.reject(frame(1, **{"from": PRIOR.RECEIVER}))
        self.reject(frame(1, to=PRIOR.TARGET))
        self.reject(frame(1, before=Builder.traced_view(Builder.final_state(receiver_slot=1))))
        self.reject(frame(4, **{"from": PRIOR.RECEIVER}))
        self.reject(frame(4, to=PRIOR.TARGET))
        self.reject(frame(5, **{"from": PRIOR.SYSTEM}))
        self.reject(frame(5, to=PRIOR.RECEIVER))
        self.reject(frame(3, **{"from": PRIOR.SYSTEM}))
        self.reject(frame(3, to=PRIOR.REVERT))
        self.reject(frame(0, reverted=True, output="00"))

        def slot(view, address, value):
            view = dict(view)
            inner = dict(view[address])
            storage = dict(inner["storage"])
            storage["0x" + "0" * 64] = f"{value:064x}"
            inner["storage"] = storage
            view[address] = inner
            return view
        self.reject(lambda r: calls(r).__setitem__(1, dict(
            calls(r)[1], before=slot(calls(r)[1]["before"], PRIOR.RECEIVER, 3))))
        self.reject(lambda r: calls(r).__setitem__(1, dict(
            calls(r)[1], after=slot(calls(r)[1]["after"], PRIOR.RECEIVER, 3))))
        self.reject(lambda r: calls(r).__setitem__(2, dict(
            calls(r)[2], before=slot(calls(r)[2]["before"], PRIOR.RECEIVER, 3))))
        self.reject(lambda r: calls(r).__setitem__(2, dict(
            calls(r)[2], after=slot(calls(r)[2]["after"], PRIOR.RECEIVER, 3))))
        self.reject(lambda r: calls(r).__setitem__(2, dict(
            calls(r)[2], after=slot(calls(r)[2]["after"], PRIOR.TARGET, 3))))

    def test_live_boundary_frames_consistent(self):
        def patch_both(row, index, field, value):
            second = self.second(row)
            second["transactions"][index][field] = value
            second["calls"][0]["before" if field == "before" else "after"] = value

        def change_system_before(row):
            patch_both(row, 1, "before", Builder.traced_view(Builder.state1()))
        self.reject(change_system_before)

        def change_system_after(row):
            patch_both(row, 1, "after", Builder.traced_view(Builder.mid_state()))
        self.reject(change_system_after)

        def change_legacy_before(row):
            self.second(row)["transactions"][0]["before"] = Builder.traced_view(Builder.genesis_state())
        self.reject(change_legacy_before)

    def test_final_state_variant_controls(self):
        def gated(row):
            second = variant_post_second(receiver_slot=3)
            row["crossing"]["blocks"][1] = second
            relink(row, second)
        self.reject(gated)

        def gated_storage(row):
            second = variant_post_second(target_storage=(2, RECEIVER_ADDRESS, len(DATA), PADDED))
            row["crossing"]["blocks"][1] = second
            relink(row, second)
        self.reject(gated_storage)
        def pre(row):
            second = variant_pre_second(receiver_slot=3)
            row["crossing"]["blocks"][1] = second
            relink(row, second)
        self.reject(pre, index=5)

    def test_variant_wire_identity(self):
        b = Builder
        items = [list(item) for item in b.items]
        wire = b"\x7f" + c.rlp([items[0][:3] + [(5).to_bytes(32, "big")]] + items[1:])
        second = b.assemble(b.header2, [b.legacy_wire, wire], [b.oracle2, b.sync_oracle], [0, 127],
                            bytes(x | y for x, y in zip(b.oracle2["bloom"], b.sync_oracle["bloom"])),
                            b.final_state())
        b.extend(second, (1, 2))
        tagged = "0x" + c.keccak256(wire).hex()
        original = self.report["cases"][6]["crossing"]["blocks"][1]
        second["transactions"] = [dict(row, hash=tagged, raw=wire.hex()) if row["type"] == 127 else row
                                  for row in original["transactions"]]
        second["calls"] = [dict(call, transaction=tagged) for call in original["calls"]]
        receipt = second["receipts"][1]
        receipt["transactionHash"] = tagged
        for log in receipt["logs"]:
            log["transactionHash"] = tagged

        def change(row):
            row["crossing"]["blocks"][1] = second
            relink(row, second)
        self.reject(change)

    def test_candidate_bodies(self):
        b = Builder
        def candidate(row, txs, items=None):
            fields = list(PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(
                row["before_activation"]["block"]["raw_block"])))[0])
            fields[4], proofs = ordered_proofs(txs)
            block = {**seal(fields, txs), "transaction_proofs": proofs,
                     "bor_receipt": {"present": False, "logs": []}, "state_sync": [],
                     "transactions": [], "calls": []}
            row["before_activation"]["block"] = block
            row["before_activation"]["head_hash"] = block["hash"]
            relink(row, row["crossing"]["blocks"][1])
        self.reject(lambda r: candidate(r, [b.original, b.legacy_wire]))
        changed_items = [list(b.items)[0][:3] + [(7).to_bytes(32, "big")], list(b.items)[1]]
        self.reject(lambda r: candidate(r, [b"\x7f" + c.rlp(changed_items)]))
        def same_as_first(row):
            block = dict(row["crossing"]["blocks"][0])
            block["bor_receipt"] = {"present": False, "logs": []}
            block["state_sync"] = []
            block["transactions"], block["calls"] = [], []
            row["before_activation"]["block"] = block
            row["before_activation"]["head_hash"] = block["hash"]
            relink(row, row["crossing"]["blocks"][1])
        self.reject(same_as_first)

    def test_candidate_commitments_relink(self):
        for field, value in ((3, bytes(32)), (10, b"\x01"), (5, c.keccak256(b"")), (6, b"\x01" + bytes(255))):
            with self.subTest(field=field):
                def change(row, f=field, v=value):
                    block = row["before_activation"]["block"]
                    reports.edit_header(block, f, v)
                    row["before_activation"]["head_hash"] = block["hash"]
                    relink(row, row["crossing"]["blocks"][1])
                self.reject(change)

    def test_frame_outcome_and_chain_controls(self):
        def calls(row):
            return self.second(row)["calls"]

        def frame(index, **changes):
            return lambda r: calls(r).__setitem__(index, dict(calls(r)[index], **changes))
        self.reject(frame(1, reverted=True))
        self.reject(frame(4, reverted=True))
        self.reject(frame(1, error="execution reverted"))
        self.reject(frame(2, reverted=True))
        self.reject(lambda r: calls(r).__setitem__(5, {k: v for k, v in calls(r)[5].items() if k != "error"}))

        def tweak(view, address=PRIOR.REVERT):
            inner, storage = dict(view[address]), dict(view[address]["storage"])
            storage["0x" + "0" * 63 + "1"] = f"{1:064x}"
            inner["storage"] = storage
            return {**view, address: inner}
        self.reject(lambda r: calls(r).__setitem__(1, dict(calls(r)[1],
                                                           before=tweak(calls(r)[1]["before"]))))
        self.reject(lambda r: [calls(r).__setitem__(1, dict(calls(r)[1], after=tweak(calls(r)[1]["after"]))),
                               calls(r).__setitem__(3, dict(calls(r)[3], before=tweak(calls(r)[3]["before"])))])
        self.reject(lambda r: [calls(r).__setitem__(4, dict(calls(r)[4],
                                                            before=tweak(calls(r)[4]["before"])))])
        self.reject(lambda r: calls(r).__setitem__(2, dict(calls(r)[2],
                                                           before=Builder.traced_view(Builder.final_state(receiver_slot=1)))))
        zeroed_target = Builder.final_state(receiver_slot=1, target_storage=ZERO_STORAGE)
        self.reject(lambda r: [calls(r).__setitem__(1, dict(calls(r)[1],
                                                            after=Builder.traced_view(zeroed_target))),
                               calls(r).__setitem__(2, dict(calls(r)[2],
                                                            after=Builder.traced_view(zeroed_target))),
                               calls(r).__setitem__(3, dict(calls(r)[3],
                                                            before=Builder.traced_view(zeroed_target)))])
        moved = tweak(Builder.traced_view(Builder.final_state()))
        self.reject(lambda r: [self.second(r)["transactions"][1].update(after=moved),
                               calls(r).__setitem__(0, dict(calls(r)[0], after=moved)),
                               calls(r).__setitem__(3, dict(calls(r)[3], after=moved)),
                               calls(r).__setitem__(4, dict(calls(r)[4], after=moved))])

    def test_live_boundary_chain_controls(self):
        def tweak_mid(view):
            inner, storage = dict(view[PRIOR.REVERT]), dict(view[PRIOR.REVERT]["storage"])
            storage["0x" + "0" * 63 + "1"] = f"{1:064x}"
            inner["storage"] = storage
            return {**view, PRIOR.REVERT: inner}
        other_mid = tweak_mid(Builder.traced_view(Builder.mid_state()))
        self.reject(lambda r: [self.second(r)["transactions"][1].update(before=other_mid),
                               self.second(r)["calls"][0].update(before=other_mid),
                               self.second(r)["calls"][1].update(before=other_mid)])
        other_final = tweak_mid(Builder.traced_view(Builder.final_state()))
        self.reject(lambda r: [self.second(r)["transactions"][1].update(after=other_final),
                               self.second(r)["calls"][0].update(after=other_final),
                               self.second(r)["calls"][3].update(after=other_final),
                               self.second(r)["calls"][4].update(before=other_final),
                               self.second(r)["calls"][4].update(after=other_final)])
        self.reject(lambda r: [self.second(r)["calls"][3].update(after=other_final),
                               self.second(r)["calls"][4].update(before=other_final),
                               self.second(r)["calls"][4].update(after=other_final)])
        wrong_hash = "0x" + "11" * 32
        self.reject(lambda r: [self.second(r)["transactions"][1].update(hash=wrong_hash)] +
                   [call.update(transaction=wrong_hash) for call in self.second(r)["calls"]])

    def test_carrier_cases_reach_identical_commitments(self):
        variant = build_madhugiri_report(pre_target_storage=(1, RECEIVER_ADDRESS, len(DATA), PADDED ^ 1))
        self.assertEqual(j.judge(variant)["result"], "FAIL")
        variant = build_madhugiri_report(pre_target_storage=(1, RECEIVER_ADDRESS, len(DATA), PADDED))
        self.assertEqual(j.judge(variant), VERDICT)

    def test_final_state_bindings(self):
        def storage(row, address, slot="0"):
            return self.second(row)["state_after"][address[2:]]["storage"]
        self.reject(lambda r: storage(r, PRIOR.TARGET).update({"0": "00" * 31 + "02"}))
        self.reject(lambda r: storage(r, PRIOR.RECEIVER).update({"0": "00" * 31 + "03"}))
        self.reject(lambda r: self.second(r)["state_after"][PRIOR.SENDER[2:]].update(nonce=3))
        self.reject(lambda r: self.second(r)["state_after"][PRIOR.SUCCESS[2:]].update(balance=21))
        self.reject(lambda r: storage(r, PRIOR.REVERT).update({"0": "00" * 31 + "01"}))
        self.reject(lambda r: storage(r, PRIOR.TARGET).update({"2": "00" * 31 + "1c"}),
                    index=5)


class MadhugiriBindingKillers(MadhugiriReportTests):
    """Direct negative controls for every single-check conjunct of the
    Madhugiri bindings; each rejects a report whose only broken aspect is
    one conjunct, so the matching mutant alone stops catching it."""

    def traced(self, accounts):
        return Builder.traced_view(accounts)

    def test_live_row_identity_and_boundaries(self):
        def second_tx(row, index):
            return self.second(row)["transactions"][index]

        # A wrong live hash must also move the correlated frame transactions,
        # so only the hash-identity conjunct of the live rows can catch it.
        def wrong_hash(index):
            def change(row):
                block = self.second(row)
                old = block["transactions"][index]["hash"]
                block["transactions"][index]["hash"] = "0x" + "01" * 32
                for call in block["calls"]:
                    if call["transaction"] == old:
                        call["transaction"] = "0x" + "01" * 32
            return change
        self.reject(wrong_hash(0))
        self.reject(wrong_hash(1))

        # A wrong system boundary must move every frame state that cites it,
        # so only the boundary conjunct of the live rows can catch it.
        def wrong_boundary(field, replacement):
            def change(row):
                block = self.second(row)
                old = block["transactions"][1][field]
                block["transactions"][1][field] = replacement
                for call in block["calls"]:
                    if call[field] == old:
                        call[field] = replacement
            return change
        self.reject(wrong_boundary("before", self.traced(Builder.state1())))
        self.reject(wrong_boundary("after", self.traced(Builder.mid_state())))

    def test_call_order_and_correlation(self):
        def call(index, **changes):
            return lambda r: self.second(r)["calls"][index].update(**changes)
        self.reject(call(0, depth=3))
        self.reject(call(2, depth=3))
        self.reject(call(2, transaction="0x" + "00" * 32))

    def test_frame_identity_outcomes_and_states(self):
        def call(index, **changes):
            return lambda r: self.second(r)["calls"][index].update(**changes)
        self.reject(call(2, input="00"))
        self.reject(call(2, input=j.h.abi_bytes(bytes.fromhex("19494a17"), 1558348307,
                                                  j.h.clerk_event(1, PRIOR.REVERT)).hex()))
        self.reject(call(2, error="x"))
        self.reject(call(2, after=self.traced(Builder.final_state(receiver_slot=9))))
        self.reject(call(3, input="00"))
        self.reject(call(3, input=j.h.abi_bytes(bytes.fromhex("26c53bea"), 2, DATA).hex()))
        self.reject(call(3, reverted=True))
        self.reject(call(3, output="00"))
        self.reject(call(3, before=self.traced(Builder.final_state(receiver_slot=2))))
        self.reject(call(3, after=self.traced(Builder.final_state(receiver_slot=2))))
        self.reject(call(4, after=self.traced(Builder.final_state(receiver_slot=9))))
        self.reject(call(5, before=self.traced(Builder.final_state(receiver_slot=1))))
        self.reject(call(5, output="00"))

    def post_variant(self, wire, kinds):
        b = Builder
        second = b.assemble(b.header2, [b.legacy_wire, wire],
                            [b.oracle2, b.sync_oracle], kinds,
                            bytes(x | y for x, y in zip(b.oracle2["bloom"], b.sync_oracle["bloom"])),
                            b.final_state())
        b.extend(second, (1, 2))
        return second

    def test_carrier_envelope_and_items(self):
        b = Builder
        def replace_second(wire, kinds):
            def change(row):
                second = self.post_variant(wire, kinds)
                second["transactions"] = self.second(row)["transactions"]
                second["calls"] = self.second(row)["calls"]
                row["crossing"]["blocks"][1] = second
            return change
        self.reject(replace_second(b.legacy_wire, (0, 0)))
        self.reject(replace_second(local_wire(list(reversed(local_items()))), (0, 127)))

    def candidate_variant(self, txs):
        b = Builder
        candidate = b.assemble(b.header1, txs, [], [], bytes(256), b.genesis_state())
        fields = PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(candidate["raw_block"])))[0]
        fields[3] = b.genesis[3]
        return {**seal(fields, txs), "transaction_proofs": candidate["transaction_proofs"]}

    def test_observation_body_items_and_commitments(self):
        b = Builder
        def swap_candidate(candidate):
            def change(row):
                negative = row["before_activation"]
                negative["block"] = candidate
                negative["head_hash"] = candidate["hash"]
            return change
        self.reject(swap_candidate(self.candidate_variant([b.legacy_wire, b.original])))
        self.reject(swap_candidate(self.candidate_variant([b.legacy_wire])))
        swapped = local_wire(list(reversed(local_items())))
        self.reject(swap_candidate(self.candidate_variant([swapped])))

        def edit_field(field, value):
            def change(row):
                candidate = dict(row["before_activation"]["block"])
                reports.edit_header(candidate, field, value)
                row["before_activation"]["block"] = candidate
                row["before_activation"]["head_hash"] = candidate["hash"]
            return change
        self.reject(edit_field(3, bytes(32)))
        self.reject(edit_field(10, b"\x01"))
        self.reject(edit_field(6, b"\x01" + bytes(255)))
        self.reject(edit_field(5, c.keccak256(b"")))


if __name__ == "__main__":
    unittest.main()
