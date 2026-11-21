"""Strict private type 1/2/4 crossing proof; no public-profile gate credit."""

import argparse
import base64
import copy
import importlib.util
import json
from pathlib import Path

_spec = importlib.util.spec_from_file_location("crossing_expected_helpers", Path(__file__).with_name("crossing_helpers.py"))
h = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(h)
prior, codec = h.prior, h.codec
check, octets, integer, quantity = h.check, h.octets, h.integer, h.quantity
CASES = (("Berlin", "berlinBlock", "", 1, 0, 0), ("London", "londonBlock", "", 2, 1, 0), ("Prague", "pragueBlock", "", 4, 4, 4))
# The paired producer retains the frozen unpaired three and appends two rows
# that also schedule Jaipur or Bhilai at the crossing block itself.
PAIRED_CASES = CASES + (("LondonJaipur", "londonBlock", "jaipurBlock", 2, 1, 0),
                        ("PragueBhilai", "pragueBlock", "bhilaiBlock", 4, 4, 4))
# The Madhugiri producer appends the pre-gate carrier control (Bor prefix 6:
# six forks precede Madhugiri, so it stays scheduled at 8) and the gated
# body-StateSync crossing (prefix 7 parks Madhugiri at 0 and the Bor gate
# override schedules it at 2). Both move the sprint to 2 through the seam.
MADHUGIRI_CASES = PAIRED_CASES + (("PreMadhugiri", "", "", 0, 5, 6),
                                  ("Madhugiri", "", "madhugiriBlock", 127, 5, 7))
SYNC_TOPIC = (94).to_bytes(32, "big")
# The Delhi-denominator base-fee pair, pinned from native run01:
# pair of the sprint-2 profiles. The receipts remain fully pinned to the
# header values, so only the two integers are deferred; the coordinator
# derives them from the native run and replaces the None.
MADHUGIRI_BASE_FEES = (984_375_000, 969_058_741)
SIGNER = "71562b71999873db5b286df957af199ec94617f7"
INITIAL_CODE_HASHES = {
    prior.SENDER: "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470",
    prior.AUTHORITY: "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470",
    prior.SUCCESS: "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470",
    prior.REVERT: "5cf04cb8c25808b54e86ef1f67aefd4648843b8233bf76efccfcc2e415f749f1",
    prior.TARGET: "2baa572e83d64a532a4358ed7cc1743dfea2794b392bc3dc35eb500e66a4850a",
    prior.RECEIVER: "710f0472f8b6b03ec96db2536b77758501cd97da610bc1bb2a522eccb3a7aced",
}


def legacy_state(state, sender_after):
    expected = {address: {"nonce": 0, "balance": 0, "storage": (0, 0, 0, 0), "code_hash": digest}
                for address, digest in INITIAL_CODE_HASHES.items()}
    expected[prior.SENDER].update(nonce=1, balance=sender_after)
    expected[prior.SUCCESS]["balance"] = 7
    actual = {address: {"nonce": row["nonce"], "balance": row["balance"], "storage": row["storage"],
                        "code_hash": codec.keccak256(row["code"]).hex()} for address, row in state.items()}
    check(actual == expected, "crossing legacy state differs from source genesis")


def absent_authority(row, root):
    proof = row["account_proofs"][prior.AUTHORITY]
    leaf = codec.proof_value(root, codec.keccak256(octets(prior.AUTHORITY)),
                             [octets(node) for node in proof["accountProof"]])
    check(leaf is None, "crossing authorization authority already exists")


def transition_state(first, first_root, state1, state2, kind, sender_after):
    if kind == 4:
        absent_authority(first, first_root)
    expected = copy.deepcopy(state1)
    expected[prior.SENDER].update(nonce=2, balance=sender_after)
    expected[prior.SUCCESS]["balance"] = 14
    if kind == 4:
        expected[prior.AUTHORITY].update(nonce=1, code=b"\xef\x01\x00" + octets(prior.SUCCESS))
    check(state2 == expected, "crossing final authenticated state differs")


def receipt_oracle(kind, base_fee, sender_before, recipient_before, signer_before):
    # Pinned core/state_transition.go: 21000 plus 25000 for a newly created
    # authorization authority. The absent authority earns no existing-account
    # refund. Empty calldata adds no EVM execution gas to this plain transfer.
    gas = 46000 if kind == 4 else 21000
    gas_limit = 100000 if kind == 4 else 21000
    price = base_fee + 2_000_000_000 if kind in (2, 4) else 5_000_000_000
    bought, tip = gas_limit * price, gas * (price - base_fee)
    address = octets("0000000000000000000000000000000000001010")
    sender = octets(prior.SENDER).rjust(32, b"\x00")
    signatures = ("e6497e3ee548a3372136af2fcb0696db31fc6cf20260707645068bd3fe97f3c4", "4dfe1bbbcf077ddc3e01291eea2d5c70c2b422b415d95645b9adcfd678cb1d63")
    amounts = ((7, sender_before - bought, recipient_before, sender_before - bought - 7, recipient_before + 7),
               (tip, sender_before, signer_before, sender_before - tip, signer_before + tip))
    logs = [[address, [octets(signature), address.rjust(32, b"\x00"), sender, target],
             b"".join(value.to_bytes(32, "big") for value in values)]
            for signature, target, values in zip(signatures, (octets(prior.SUCCESS).rjust(32, b"\x00"), octets(SIGNER).rjust(32, b"\x00")), amounts)]
    bloom = 0
    for address, topics, _ in logs:
        for item in [address] + topics:
            digest = codec.keccak256(item)
            for offset in (0, 2, 4):
                bloom |= 1 << (int.from_bytes(digest[offset:offset + 2], "big") & 2047)
    bloom = bloom.to_bytes(256, "big")
    wire = codec.rlp([b"\x01", gas.to_bytes((gas.bit_length() + 7) // 8, "big"), bloom, logs])
    if kind:
        wire = bytes([kind]) + wire
    return {"gas": gas, "price": price, "logs": logs, "bloom": bloom, "wire": wire,
            "sender_after": sender_before - gas * price - 7, "signer_after": signer_before + tip}


def stored(row, header, tx, kind, oracle):
    receipts, proofs = row["receipts"], row["receipt_proofs"]
    check(isinstance(receipts, list) and isinstance(proofs, list) and len(receipts) == len(proofs) == 1, "crossing receipt collection differs")
    wires = [octets(proofs[0]["value"])]
    check(wires == [oracle["wire"]] and codec.ordered_root(wires) == header[5], "crossing receipt wire/root differs")
    h.inclusion(proofs, wires, header[5])
    check(integer(header[10]) == oracle["gas"] and header[6] == oracle["bloom"], "crossing header gas/bloom differs")
    receipt = receipts[0]
    check(quantity(receipt.get("type", "0x0")) == kind and quantity(receipt["status"]) == 1, "crossing receipt status/type differs")
    check(quantity(receipt["gasUsed"]) == quantity(receipt["cumulativeGasUsed"]) == oracle["gas"] and
          quantity(receipt["effectiveGasPrice"]) == oracle["price"], "crossing receipt fees differ")
    check(prior.consensus_logs(receipt["logs"]) == oracle["logs"] and octets(receipt["logsBloom"]) == oracle["bloom"], "crossing receipt logs differ")
    tx_hash = "0x" + codec.keccak256(tx).hex()
    check(receipt["transactionHash"] == tx_hash and receipt["blockHash"] == row["hash"] and
          quantity(receipt["blockNumber"]) == row["number"] and quantity(receipt["transactionIndex"]) == 0, "crossing receipt identity differs")
    for index, log in enumerate(receipt["logs"]):
        check(log["transactionHash"] == tx_hash and log["blockHash"] == row["hash"] and
              quantity(log["blockNumber"]) == row["number"] and quantity(log["transactionIndex"]) == 0 and
              quantity(log["logIndex"]) == index and quantity(log["blockTimestamp"]) == integer(header[11]) and
              log["removed"] is False, "crossing log identity differs")
    prior.account_claims(row, header[3])
    return prior.snapshot(row["state_after"], False)


def records(row, views, head, frontier, error):
    check(row["head_hash"] == head, "crossing actual head differs")
    admission = {key: prior.native_json(value) for key, value in row["admission"].items()}
    check(all(type(value) is int for record in admission.values() for value in record.values()), "crossing admission counters are not integers")
    if views:
        decoded = {key: prior.native_json(value) for key, value in row["decisions"].items()}
        prefix = "extractor-decision-v1/bor.run-1/"
        check(type(decoded[prefix + "counter"]["next"]) is int, "crossing decision counter is not an integer")
        for sequence in range(len(views)):
            intent = decoded[prefix + f"{sequence:016x}/intent"]
            check(type(intent["old_head"]["number"]) is int and type(intent["new_head"]["number"]) is int, "crossing durable head numbers are not integers")
        h.check_records(row["decisions"], row["admission"], views, frontier, observation_offset=1)
    else:
        # decision.Open reads an absent counter as zero; only the first intent
        # writes it. The rejected first admission creates no decision records.
        check(row["decisions"] == {}, "crossing refusal created decision records")
        prefix = "extractor-admission-v1/bor.run-1/"
        check(admission == {prefix + "counter": {"next": 1}, prefix + "lowwater": {"sequence": 1},
                            prefix + "purged": {"sequence": 1}}, "crossing refused admission frontier differs")
    observed = [1] if frontier == 1 else [1, 1, 2]
    keys = {"1": ["bor.run-1.0000000000000000"]} if frontier == 1 else {
        "1": ["bor.run-1.0000000000000000", "bor.run-1.0000000000000001"],
        "2": ["bor.run-1.0000000000000002"]}
    check(row["observed"] == observed and all(type(x) is int for x in row["observed"]) and
          row["keys"] == keys and row["observer_errors"] == [error], "crossing observation frontier differs")


def configuration_numbers(value):
    check(type(value) not in (bool, float), "crossing configuration has a noninteger numeric value")
    if isinstance(value, dict):
        for child in value.values():
            configuration_numbers(child)


def state_sync_receipt_oracle(cumulative):
    # Pinned core/state_processor.go insertStateSyncTransactionAndCalculateReceipt:
    # status 1, no gas, the header cumulative total, only the successful
    # item's log (the reverting recipient emits nothing) and no fee payment.
    logs = [[octets(prior.TARGET), [SYNC_TOPIC], (1).to_bytes(32, "big")]]
    bloom = 0
    for address, topics, _ in logs:
        for item in [address] + topics:
            digest = codec.keccak256(item)
            for offset in (0, 2, 4):
                bloom |= 1 << (int.from_bytes(digest[offset:offset + 2], "big") & 2047)
    bloom = bloom.to_bytes(256, "big")
    wire = b"\x7f" + codec.rlp([b"\x01", cumulative.to_bytes((cumulative.bit_length() + 7) // 8, "big"), bloom, logs])
    return {"gas": 0, "price": 0, "logs": logs, "bloom": bloom, "wire": wire}


def madhugiri_receipts(row, header, txs, oracles, header_bloom):
    # Generalization of stored() for the one- and two-receipt carrier bodies;
    # the second receipt of the gated body is the appended 127 receipt.
    receipts, proofs = row["receipts"], row["receipt_proofs"]
    check(isinstance(receipts, list) and isinstance(proofs, list) and
          len(receipts) == len(proofs) == len(oracles) == len(txs), "madhugiri receipt collection differs")
    wires = [octets(proof["value"]) for proof in proofs]
    check(wires == [oracle["wire"] for oracle in oracles] and codec.ordered_root(wires) == header[5],
          "madhugiri receipt wire/root differs")
    h.inclusion(proofs, wires, header[5])
    cumulative = 0
    log_offset = 0
    for index, (receipt, tx, oracle) in enumerate(zip(receipts, txs, oracles)):
        cumulative += oracle["gas"]
        tx_hash = "0x" + codec.keccak256(tx).hex()
        check(quantity(receipt.get("type", "0x0")) == (tx[0] if tx[0] < 128 else 0) and
              quantity(receipt["status"]) == 1 and quantity(receipt["gasUsed"]) == oracle["gas"] and
              quantity(receipt["cumulativeGasUsed"]) == cumulative and
              quantity(receipt["effectiveGasPrice"]) == oracle["price"], "madhugiri receipt fees differ")
        check(prior.consensus_logs(receipt["logs"]) == oracle["logs"] and
              octets(receipt["logsBloom"]) == oracle["bloom"], "madhugiri receipt logs differ")
        check(receipt["transactionHash"] == tx_hash and receipt["blockHash"] == row["hash"] and
              quantity(receipt["blockNumber"]) == row["number"] and
              quantity(receipt["transactionIndex"]) == index, "madhugiri receipt identity differs")
        for position, log in enumerate(receipt["logs"]):
            # The log index counts across the whole block, receipt by receipt.
            check(log["transactionHash"] == tx_hash and log["blockHash"] == row["hash"] and
                  quantity(log["blockNumber"]) == row["number"] and quantity(log["transactionIndex"]) == index and
                  quantity(log["logIndex"]) == log_offset + position and
                  quantity(log["blockTimestamp"]) == integer(header[11]) and
                  log["removed"] is False, "madhugiri log identity differs")
        log_offset += len(receipt["logs"])
    check(cumulative == integer(header[10]), "madhugiri header gas differs")
    if header_bloom is not None:
        # The pre-gate header bloom may also fold in the separate Bor receipt
        # log; that binding stays open by design (the folded bloom is not
        check(header[6] == header_bloom, "madhugiri header bloom differs")
    prior.account_claims(row, header[3])
    return prior.snapshot(row["state_after"], False)


def state_sync_claims(view, identifiers):
    # Pinned HeaderChain().GetStateSync() rows: both events in either case
    # because SetStateSync runs before the Madhugiri branch.
    rows = view["state_sync"]
    check(type(rows) is list and len(rows) == len(identifiers), "madhugiri state-sync row count differs")
    for row, identifier in zip(rows, identifiers):
        check(set(row) == {"ID", "Contract", "Data", "TxHash"}, "madhugiri state-sync row shape differs")
        check(type(row["ID"]) is int and row["ID"] == identifier and
              octets(row["Contract"], 20) == octets(prior.TARGET if identifier == 1 else prior.REVERT) and
              base64.b64decode(row["Data"]) == prior.EVENT_DATA and
              octets(row["TxHash"], 32) == identifier.to_bytes(32, "big"), "madhugiri state-sync rows differ")


def observation_claims(row, observed, keys):
    check(row["observed"] == observed and all(type(x) is int for x in row["observed"]) and
          row["keys"] == keys and row["observer_errors"] == [], "madhugiri observation frontier differs")


def plain_records(row, head, views, frontier):
    # The pre-gate carrier control imports both generated blocks onto the
    # genesis head: every transition is a plain parent-derived IMPORT.
    check(row["head_hash"] == head, "madhugiri plain head differs")
    admission = {key: prior.native_json(value) for key, value in row["admission"].items()}
    check(all(type(value) is int for record in admission.values() for value in record.values()),
          "madhugiri admission counters are not integers")
    h.check_records(row["decisions"], row["admission"], views, frontier)
    observation_claims(row, [1, 2], {"1": ["bor.run-1.0000000000000000"], "2": ["bor.run-1.0000000000000001"]})


def reorg_records(row, genesis_hash, candidate, first, second):
    # Pinned core/zz_extractor_decision.go extractorTransition around the
    # accepted pre-gate observation: the candidate is IMPORTed as canonical
    # block 1, the generated block 1 lands beside it as a no-event SIDE
    # write, and block 2 reorgs the head through a CANONICAL transition whose
    # member list carries both branch blocks.
    check(row["head_hash"] == second["hash"], "madhugiri reorg head differs")
    records = {key: prior.native_json(value) for key, value in row["decisions"].items()}
    prefix = "extractor-decision-v1/bor.run-1/"
    check(set(records) == {prefix + "counter"} | {prefix + f"{sequence:016x}/{part}"
          for sequence in range(3) for part in ("intent", "decided")}, "madhugiri reorg decision keys differ")
    check(records[prefix + "counter"] == {"next": 3}, "madhugiri reorg decision counter differs")
    candidate_head = {"number": 1, "hash": candidate["hash"][2:]}
    generated_head = {"number": 1, "hash": first["hash"][2:]}
    # The accepted observation candidate imports as canonical block 1; the
    # generated block 1 then replaces it at the same height through a
    # CANONICAL transition, and block 2 imports on top.
    transitions = (("IMPORT", {"number": 0, "hash": genesis_hash[2:]}, candidate_head, [(0, candidate)]),
                   ("CANONICAL", candidate_head, generated_head, [(1, first)]),
                   ("IMPORT", generated_head, {"number": 2, "hash": second["hash"][2:]}, [(2, second)]))
    for sequence, (kind, old_head, new_head, members) in enumerate(transitions):
        key = prefix + f"{sequence:016x}"
        intent, decided = records[key + "/intent"], records[key + "/decided"]
        check(set(intent) == {"native_store_id", "producer", "sequence", "kind", "old_head", "new_head", "members", "evidence_digest", "events", "digest"},
              "invalid madhugiri intent schema")
        check(intent["native_store_id"] == "0123456789abcdef0123456789abcdef" and intent["producer"] == "bor.run-1",
              "wrong madhugiri native identity")
        check(type(intent["sequence"]) is int and intent["sequence"] == sequence and intent["kind"] == kind and
              intent["events"] is (kind != "SIDE"), "wrong madhugiri decision kind/sequence")
        check(intent["old_head"] == old_head and intent["new_head"] == new_head, "madhugiri decision head mismatch")
        check(len(intent["members"]) == len(members), "wrong madhugiri member count")
        for (index, block), member in zip(members, intent["members"]):
            check(set(member) == {"observation", "block", "exec_ready_digest", "child_root"},
                  "invalid madhugiri member schema")
            check(member["observation"] == f"bor.run-1.{index:016x}", "wrong madhugiri member observation")
            check(member["block"] == block["hash"][2:] and member["child_root"] == block["root"][2:],
                  "madhugiri member/root mismatch")
        check(intent["digest"] == prior.intent_digest(intent), "invalid madhugiri intent digest")
        check(set(decided) == {"sequence", "intent_digest", "delivery", "digest"}, "invalid madhugiri decided schema")
        check(type(decided["sequence"]) is int and decided["sequence"] == sequence and
              decided["intent_digest"] == intent["digest"] and decided["delivery"] == "BATCH" and
              decided["digest"] == prior.decided_digest(intent["digest"], "BATCH"), "invalid madhugiri decided binding")
    admission = {key: prior.native_json(value) for key, value in row["admission"].items()}
    check(admission == {"extractor-admission-v1/bor.run-1/counter": {"next": 3},
                        "extractor-admission-v1/bor.run-1/lowwater": {"sequence": 3},
                        "extractor-admission-v1/bor.run-1/purged": {"sequence": 3}},
          "madhugiri reorg admission frontier differs")
    observation_claims(row, [1, 1, 2], {"1": ["bor.run-1.0000000000000000", "bor.run-1.0000000000000001"],
                                        "2": ["bor.run-1.0000000000000002"]})


def system_actions(row, txs, state1, mid_state, state2, cumulative):
    # Pinned core/state_processor.go: the wrapper fires OnTxStart for the
    # final body 127 with the system sender, so the live rows are the legacy
    # transfer followed by the system row, and the five frames are the
    # synthetic root plus the two receiver commits at depth 1 with their
    # nested target calls at depth 2.
    live = row["transactions"]
    check(type(live) is list and len(live) == len(txs), "madhugiri live transaction rows differ")
    for trace, tx, sender in zip(live, txs, (prior.SENDER, prior.SYSTEM)):
        check(trace["hash"] == "0x" + codec.keccak256(tx).hex() and trace["type"] == (tx[0] if tx[0] < 128 else 0) and
              octets(trace["raw"]) == tx and trace["from"].lower() == sender, "madhugiri live row identity differs")
    legacy_trace, system_trace = live
    legacy_before, legacy_after = prior.snapshot(legacy_trace["before"], True), prior.snapshot(legacy_trace["after"], True)
    system_before, system_after = prior.snapshot(system_trace["before"], True), prior.snapshot(system_trace["after"], True)
    check(legacy_before == state1 and legacy_after == mid_state and system_before == mid_state and
          system_after == state2, "madhugiri live state boundaries differ")
    observed = system_trace["receipt"]
    # A free system transaction marshals its effective gas price as null.
    check(quantity(observed["status"]) == 1 and quantity(observed["gasUsed"]) == 0 and
          quantity(observed["cumulativeGasUsed"]) == cumulative and
          (observed["effectiveGasPrice"] is None or quantity(observed["effectiveGasPrice"]) == 0) and
          prior.consensus_logs(observed["logs"]) == [[octets(prior.TARGET), [SYNC_TOPIC], (1).to_bytes(32, "big")]],
          "madhugiri live system receipt differs")
    calls = row["calls"]
    # Every live transaction owns a depth-0 frame; the system call then fans
    # out into the receiver and its two nested commits.
    check([call["depth"] for call in calls] == [0, 0, 1, 2, 1, 2], "wrong madhugiri native call order")
    legacy_frame, root, first, target, second, rejected = calls
    system_hash = system_trace["hash"]
    for call in calls:
        correlated = call["transaction"] in (system_hash, legacy_trace["hash"])
        check(correlated and call["type"] == 241, "uncorrelated madhugiri system call")
    check(legacy_frame["transaction"] == legacy_trace["hash"] and legacy_frame["from"].lower() == prior.SENDER and
          legacy_frame["to"].lower() == prior.SUCCESS and legacy_frame["input"] == "" and
          legacy_frame["reverted"] is False, "invalid madhugiri legacy frame")
    check(root["from"].lower() == prior.SYSTEM and root["to"].lower() == prior.RECEIVER and root["input"] == "" and
          root["reverted"] is False and root.get("error") is None, "invalid madhugiri synthetic root call")
    check(prior.snapshot(root["before"], True) == system_before and
          prior.snapshot(root["after"], True) == system_after, "madhugiri synthetic root state differs")
    previous = system_before
    for index, outer, inner, target_address in ((1, first, target, prior.TARGET), (2, second, rejected, prior.REVERT)):
        outer_before, outer_after = prior.snapshot(outer["before"], True), prior.snapshot(outer["after"], True)
        inner_before, inner_after = prior.snapshot(inner["before"], True), prior.snapshot(inner["after"], True)
        check(outer["from"].lower() == prior.SYSTEM and outer["to"].lower() == prior.RECEIVER and
              octets(outer["input"]) == h.abi_bytes(bytes.fromhex("19494a17"), 1558348306, h.clerk_event(index, target_address)),
              "wrong madhugiri receiver call identity/data")
        check(outer_before == previous and outer_before[prior.RECEIVER]["storage"][0] == index - 1 and
              outer_after[prior.RECEIVER]["storage"][0] == index, "madhugiri receiver call state discontinuity")
        check(inner["from"].lower() == prior.RECEIVER and inner["to"].lower() == target_address and
              octets(inner["input"]) == h.abi_bytes(bytes.fromhex("26c53bea"), index, prior.EVENT_DATA),
              "wrong madhugiri nested call identity/data")
        check(inner_before[prior.RECEIVER]["storage"][0] == index and
              inner_after[prior.RECEIVER]["storage"][0] == index, "madhugiri receiver progress unstable during call")
        check(outer_after == inner_after and outer["reverted"] is False and outer.get("error") is None and
              outer["output"] == (1 if index == 1 else 0).to_bytes(32, "big").hex(), "madhugiri receiver call outcome differs")
        check(inner["reverted"] is (index == 2) and inner["output"] == ("" if index == 1 else "00" * 31 + "ee") and
              (inner.get("error") is None if index == 1 else inner.get("error") == "execution reverted"),
              "madhugiri nested call outcome differs")
        if index == 2:
            check(inner_before == inner_after, "failed madhugiri nested call retained state")
        else:
            check(inner_before[prior.TARGET]["storage"] == (0, 0, 0, 0) and
                  inner_after[prior.TARGET] == system_after[prior.TARGET], "successful madhugiri action state differs")
        previous = outer_after
    check(previous == system_after, "last madhugiri action differs from authenticated end state")


def refusal_claims(rows, first, legacy_wire):
    names = ["missing", "non-final", "mismatched-l1-hash"]
    check([row["case"] for row in rows] == names, "madhugiri import refusal cases differ")
    original = h.state_sync_wire()
    items = h.state_sync_items()
    for row in rows:
        case = row["case"]
        check("control_error" not in row and isinstance(row.get("error"), str) and
              "state-sync mismatch" in row["error"], "missing madhugiri native import refusal")
        check(row["head_before"] == row["head_after"] == first["hash"], "madhugiri refusal changed canonical head")
        header, txs = h.block(row, 16, 2, first["hash"])
        if case == "missing":
            check(txs == [legacy_wire] and
                  "state-sync mismatch: block body missing state-sync transaction, heimdall reported 2 event(s)" in row["error"],
                  "madhugiri missing-carrier control differs")
        elif case == "non-final":
            check(len(txs) == 2 and txs[0] == original and txs[1][0] >= 192, "madhugiri non-final control body differs")
            h.signed_input(txs[1], 0, 1)
            check("state-sync mismatch: block body missing state-sync transaction, heimdall reported 2 event(s)" in row["error"],
                  "madhugiri non-final refusal differs")
        else:
            changed = [list(item) for item in items]
            changed[0][3] = (153).to_bytes(32, "big")
            check(len(txs) == 2 and txs[0] == legacy_wire and
                  prior.plain_rlp(codec.rlp_list(txs[1][1:])) == changed, "madhugiri L1-hash control body differs")
            # Pinned bor.go:1330: got is the body's final carrier, want the
            # heimdall-rebuilt original; both digests are re-derived here.
            expected = ("state-sync mismatch: hash mismatch, got 0x" + codec.keccak256(txs[1]).hex() +
                        " want 0x" + codec.keccak256(original).hex())
            check(expected in row["error"], "madhugiri L1-hash refusal binding differs")
        h.check_records(row["decisions"], row["admission"], [first], frontier=2)


def decode_sweep(components, second):
    # Type 127 itself decodes, so the sweep of unknown envelope tags 5..126
    # is a decode-layer refusal kept separate from the import refusals.
    check(components["head_before"] == components["head_after"] == second["hash"], "madhugiri decode sweep moved head")
    rows = components["unknown_block_decode"]
    check([row["type"] for row in rows] == list(range(5, 127)), "madhugiri unknown block-decoder coverage differs")
    check(all(isinstance(row["error"], str) and "transaction type not supported" in row["error"] for row in rows),
          "madhugiri unknown block-decoder refusal differs")


def madhugiri_case(row, expected):
    name, gate, bor_gate, kind, eth_prefix, bor_prefix = expected
    check(row["name"] == name and type(row["type"]) is int and row["type"] == kind and
          type(row["activation_block"]) is int and row["activation_block"] == 2, "madhugiri case identity differs")
    check(row["bor_activation"] == bor_gate, "madhugiri bor activation differs")
    config = copy.deepcopy(row["config"])
    configuration_numbers(config)
    check(type(config["bor"]["madhugiriBlock"]) is int and
          config["bor"]["madhugiriBlock"] == (2 if bor_gate else 8), "madhugiri gate schedule differs")
    check(config["bor"].get("sprint") == {"0": 2}, "madhugiri sprint schedule differs")
    h.historical_config(config, eth_prefix, bor_prefix, bor_gate, sprint=2)
    genesis, txg, genesis_hash = h.raw_block(row["genesis_raw"], 20)
    check(integer(genesis[8]) == 0 and genesis[0] == bytes(32) and not txg, "madhugiri genesis differs")
    positive = row["crossing"]
    check(len(positive["blocks"]) == 2, "madhugiri needs two successful blocks")
    first, second = positive["blocks"]
    h1, tx1 = h.block(first, 16, 1, genesis_hash)
    h2, tx2 = h.block(second, 16, 2, first["hash"])
    h.signed_input(tx1[0], 0, 0)
    check(integer(h1[9]) == integer(h2[9]) == integer(genesis[9]) == 10_000_000, "madhugiri gas limit differs")
    check(integer(h1[11]) == integer(genesis[11]) + 1 and integer(h2[11]) == integer(h1[11]) + 1,
          "madhugiri timestamp sequence differs")
    base1, base2 = integer(h1[15]), integer(h2[15])
    if MADHUGIRI_BASE_FEES is not None:
        check((base1, base2) == MADHUGIRI_BASE_FEES, "madhugiri native base-fee pair differs")
    oracle1 = receipt_oracle(0, base1, 10**19, 0, 10**21)
    oracle2 = receipt_oracle(0, base2, oracle1["sender_after"], 7, oracle1["signer_after"])
    sync_oracle = state_sync_receipt_oracle(oracle2["gas"])
    state1 = madhugiri_receipts(first, h1, tx1, [oracle1], oracle1["bloom"])
    legacy_state(state1, oracle1["sender_after"])
    state_sync_claims(first, ())
    check(first["bor_receipt"]["present"] is False, "madhugiri first-block bor receipt differs")
    items = h.state_sync_items()
    final_state = copy.deepcopy(state1)
    final_state[prior.SENDER].update(nonce=2, balance=oracle2["sender_after"])
    final_state[prior.SUCCESS]["balance"] = 14
    final_state[prior.RECEIVER]["storage"] = (2, 0, 0, 0)
    final_state[prior.TARGET]["storage"] = (1, int(prior.RECEIVER, 16), len(prior.EVENT_DATA),
                                            int.from_bytes(prior.EVENT_DATA.ljust(32, b"\0"), "big"))
    if kind == 127:
        check(len(tx2) == 2 and tx2[1][0] == 127, "madhugiri carrier envelope differs")
        check(prior.plain_rlp(codec.rlp_list(tx2[1][1:])) == items, "madhugiri body StateSync items differ")
        h.signed_input(tx2[0], 0, 1)
        state2 = madhugiri_receipts(second, h2, tx2, [oracle2, sync_oracle],
                                    bytes(a | b for a, b in zip(oracle2["bloom"], sync_oracle["bloom"])))
        check(state2 == final_state, "madhugiri final authenticated state differs")
        state_sync_claims(second, (1, 2))
        check(second["bor_receipt"]["present"] is False and second["bor_receipt"]["logs"] == [],
              "madhugiri post-gate bor receipt differs")
        mid_state = copy.deepcopy(state1)
        mid_state[prior.SENDER].update(nonce=2, balance=oracle2["sender_after"])
        mid_state[prior.SUCCESS]["balance"] = 14
        system_actions(second, tx2, state1, mid_state, state2, oracle2["gas"])
        negative = row["before_activation"]
        candidate = negative["block"]
        hn, txn = h.block(candidate, 16, 1, genesis_hash)
        check(len(txn) == 1 and txn[0][0] == 127 and candidate["hash"] != first["hash"],
              "madhugiri observation body is not distinct")
        check(prior.plain_rlp(codec.rlp_list(txn[0][1:])) == items, "madhugiri observation items differ")
        # Pinned skip-execution commitments: the pre-gate processor skips the
        # reserved envelope, so the candidate declares the unchanged parent
        # state root, no gas, no bloom and the empty receipt root.
        check(hn[3] == genesis[3] and integer(hn[10]) == 0 and hn[6] == bytes(256) and hn[5] == codec.EMPTY_ROOT,
              "madhugiri observation commitments differ")
        check(negative["accepted"] is True and
              negative["classification"] == "native-preMadhugiri-observation-no-refusal-credit" and
              "error" not in negative, "madhugiri pre-gate observation differs")
        check(negative["head_before"] == genesis_hash, "madhugiri observation seeded head differs")
        admission = {key: prior.native_json(value) for key, value in negative["admission"].items()}
        records = {key: prior.native_json(value) for key, value in negative["decisions"].items()}
        prefix = "extractor-decision-v1/bor.run-1/"
        # The accepted candidate is a real import: the decider records one
        # IMPORT transition onto it before the crossing resumes; reorg_records
        # later validates the same sequence's full digest binding.
        check(set(records) == {prefix + "counter", prefix + "0000000000000000/intent",
                               prefix + "0000000000000000/decided"} and
              records[prefix + "counter"] == {"next": 1} and
              admission == {"extractor-admission-v1/bor.run-1/counter": {"next": 1},
                            "extractor-admission-v1/bor.run-1/lowwater": {"sequence": 1},
                            "extractor-admission-v1/bor.run-1/purged": {"sequence": 1}} and
              records[prefix + "0000000000000000/intent"]["kind"] == "IMPORT" and
              records[prefix + "0000000000000000/intent"]["old_head"] == {"number": 0, "hash": genesis_hash[2:]} and
              records[prefix + "0000000000000000/intent"]["new_head"] == {"number": 1, "hash": candidate["hash"][2:]} and
              len(records[prefix + "0000000000000000/intent"]["members"]) == 1,
              "madhugiri observation durable records differ")
        observation_claims(negative, [1], {"1": ["bor.run-1.0000000000000000"]})
        check(negative["head_hash"] == candidate["hash"], "madhugiri observation head differs")
        reorg_records(positive, genesis_hash, candidate, first, second)
        refusal_claims(row["import_refusals"], first, tx2[0])
        decode_sweep(row["components"], second)
    else:
        check("before_activation" not in row and "import_refusals" not in row and "components" not in row,
              "pre-madhugiri carrier claims gated controls")
        check(len(tx2) == 1, "pre-madhugiri carrier body differs")
        h.signed_input(tx2[0], 0, 1)
        state2 = madhugiri_receipts(second, h2, tx2, [oracle2], None)
        check(state2 == final_state, "pre-madhugiri final authenticated state differs")
        state_sync_claims(second, (1, 2))
        bor = second["bor_receipt"]
        check(bor["present"] is True and prior.consensus_logs(bor["logs"]) == sync_oracle["logs"],
              "pre-madhugiri separate bor receipt differs")
        # The StateSync effects ride the separate Bor receipt, so the only
        # live execution is the legacy transaction's own frame.
        legacy = h.prior.plain_rlp(h.codec.rlp_list(tx2[0]))
        check(len(second["transactions"]) == 1 and len(second["calls"]) == 1 and
              second["calls"][0]["depth"] == 0 and
              second["calls"][0]["to"].lower() == "0x" + legacy[3].hex() and
              second["calls"][0]["reverted"] is False, "pre-madhugiri live carrier rows differ")
        plain_records(positive, second["hash"], [first, second], 2)
    check(h1[3] != h2[3], "madhugiri state root did not advance")
    return h2[3], state2


def case(row, expected, paired):
    name, gate, bor_gate, kind, eth_prefix, bor_prefix = expected
    check(row["name"] == name and type(row["type"]) is int and row["type"] == kind and
          type(row["activation_block"]) is int and row["activation_block"] == 2, "crossing case identity differs")
    check(("bor_activation" in row) == paired and
          (not paired or row["bor_activation"] == bor_gate), "crossing bor activation differs")
    config = copy.deepcopy(row["config"])
    configuration_numbers(config)
    check(type(config[gate]) is int and config[gate] == 2, "crossing activation schedule differs")
    config[gate] = 8
    h.historical_config(config, eth_prefix, bor_prefix, bor_gate)
    width1, width2 = (16, 16) if kind == 4 else ((15, 16) if kind == 2 else (15, 15))
    genesis, txg, genesis_hash = h.raw_block(row["genesis_raw"], 20 if kind == 4 else 15)
    check(integer(genesis[8]) == 0 and genesis[0] == bytes(32) and not txg, "crossing genesis differs")
    positive, negative = row["crossing"], row["before_activation"]
    check(len(positive["blocks"]) == 2, "crossing needs two successful blocks")
    first, second = positive["blocks"]
    h1, tx1 = h.block(first, width1, 1, genesis_hash)
    h2, tx2 = h.block(second, width2, 2, first["hash"])
    check(len(tx1) == len(tx2) == 1, "crossing transaction count differs")
    h.signed_input(tx1[0], 0, 0)
    h.signed_input(tx2[0], kind, 1)
    check(integer(genesis[9]) == integer(h1[9]) == 10_000_000 and
          integer(h2[9]) == (20_000_000 if kind == 2 else 10_000_000), "crossing London gas-limit transition differs")
    check(integer(h1[11]) == integer(genesis[11]) + 1 and integer(h2[11]) == integer(h1[11]) + 1, "crossing timestamp sequence differs")
    base1 = 937_500_000 if kind == 4 else 0
    base2 = base1 - base1 * (5_000_000 - 21000) // 5_000_000 // 16 if kind == 4 else (10**9 if kind == 2 else 0)
    check((integer(h1[15]) if width1 == 16 else 0) == base1 and
          (integer(h2[15]) if width2 == 16 else 0) == base2, "crossing native base-fee transition differs")
    oracle1 = receipt_oracle(0, base1, 10**19, 0, 10**21)
    oracle2 = receipt_oracle(kind, base2, oracle1["sender_after"], 7, oracle1["signer_after"])
    state1 = stored(first, h1, tx1[0], 0, oracle1)
    state2 = stored(second, h2, tx2[0], kind, oracle2)
    legacy_state(state1, oracle1["sender_after"])
    transition_state(first, h1[3], state1, state2, kind, oracle2["sender_after"])
    check(h1[3] != h2[3], "crossing state root did not advance")
    candidate = negative["block"]
    hn, txn = h.block(candidate, width1, 1, genesis_hash)
    check(len(txn) == 1 and candidate["hash"] != first["hash"] and hn[4] != h1[4], "crossing negative body is not distinct")
    h.signed_input(txn[0], kind, 0)
    check(all(hn[i] == h1[i] for i in range(width1) if i not in (4, 12)), "crossing negative changed control header commitments")
    expected_error = "could not apply tx 0 [0x" + codec.keccak256(txn[0]).hex() + "]: transaction type not supported"
    check(negative["accepted"] is False and negative["error"] == expected_error and negative["head_before"] == genesis_hash, "crossing native refusal differs")
    check(not any(key in candidate for key in ("receipts", "receipt_proofs", "account_proofs", "state_after")), "crossing refused block has durable state claims")
    records(negative, [], genesis_hash, 1, expected_error)
    records(positive, [first, second], second["hash"], 3, expected_error)


def judge(report):
    try:
        check(report["native_source"] == prior.NATIVE and report["scope"] == "private-ordinary-activation-crossing" and
              report["public_profile_credit"] is False and "error" not in report, "crossing native identity/result differs")
        # The paired producer appends two paired rows to the frozen unpaired
        # three; every paired-report row declares bor_activation, naming the
        # Bor gate only for the appended pair. The Madhugiri producer appends
        # the pre-gate carrier control and the gated body-StateSync crossing.
        paired = len(report["cases"]) > len(CASES)
        madhugiri = len(report["cases"]) > len(PAIRED_CASES)
        if madhugiri:
            check(len(report["cases"]) == len(MADHUGIRI_CASES), "crossing madhugiri case matrix differs")
            matrix = MADHUGIRI_CASES
        elif paired:
            check(len(report["cases"]) == len(PAIRED_CASES), "crossing paired case matrix differs")
            matrix = PAIRED_CASES
        else:
            check(len(report["cases"]) == len(CASES), "crossing case matrix differs")
            matrix = CASES
        final_commitments = []
        for row, expected in zip(report["cases"], matrix):
            if expected[0] in ("PreMadhugiri", "Madhugiri"):
                final_commitments.append(madhugiri_case(row, expected))
                continue
            case(row, expected, paired)
        reserved = "NOT_EVALUATED"
        if madhugiri:
            # The same native transition reaches the same final commitments
            # through either carrier: the pre-gate control commits the two
            # events off-body, the gated crossing appends the body 0x7f.
            check(final_commitments[0] == final_commitments[1], "madhugiri carrier cases differ in final commitments")
            reserved = {"madhugiri_crossing": True, "pre_madhugiri_separate_receipt": True}
        return {"result": "PASS", "private_activation_crossings": [1, 2, 4], "reserved127": reserved, "public_profile_credit": False}
    except (ValueError, TypeError, KeyError, IndexError, OverflowError, AttributeError) as error:
        return {"result": "FAIL", "error": str(error), "public_profile_credit": False}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("report", type=Path)
    args = parser.parse_args()
    result = judge(prior.native_json(args.report.read_text()))
    print(json.dumps(result, indent=2))
    raise SystemExit(result["result"] != "PASS")
