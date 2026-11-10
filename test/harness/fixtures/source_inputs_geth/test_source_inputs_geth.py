"""Portable checks for the E01-02.inputs Geth probe oracle and tree install. No native runtime claim."""

import copy
import hashlib
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
import source_inputs  # noqa: E402
import source_inputs_geth as geth  # noqa: E402

ORACLE = json.loads((geth.FIXTURE / "expected.json").read_text())
COMMIT = "1df30b18d9ed35ee9d28ffd4648442c2204554ad"
TARGET = ORACLE["target"]
OBS = ORACLE["observation"]


def sha(data):
    return hashlib.sha256(data).hexdigest()


def rlp(value):
    """Minimal RLP encoder for synthetic headers and bodies."""
    if isinstance(value, list):
        data = b"".join(rlp(item) for item in value)
        base = 0xC0
    else:
        data = value
        if len(data) == 1 and data[0] < 0x80:
            return data
        base = 0x80
    if len(data) < 56:
        return bytes([base + len(data)]) + data
    size = (len(data).bit_length() + 7) // 8
    return bytes([base + 55 + size]) + len(data).to_bytes(size, "big") + data


def number_bytes(value):
    return value.to_bytes((value.bit_length() + 7) // 8, "big")


def transaction(number):
    """A synthetic legacy transaction: an RLP list of nine fields."""
    return [number_bytes(number), b"\x77", b"\x30\xd4", b"\x10" * 20, b"\x01", b"", b"\x1b", b"\x02" * 32, b"\x03" * 32]


def header(number, parent, extra=b""):
    fields = [bytes.fromhex(parent[2:]), b"\x1d" * 32, b"\x20" * 20, bytes([number + 1]) * 32, b"\x04" * 32,
              b"\x05" * 32, bytes(256), b"\x02\x00\x00", number_bytes(number), b"\x98\x96\x80", b"\x52\x08",
              number_bytes(10 * number), extra, bytes(32), bytes(8), b"\x3b\x9a\xca\x00"]
    return rlp(fields)


def chain():
    """A synthetic chain whose hashes are the Keccak-256 of its header bytes."""
    hashes, encoded = [], []
    for number in range(TARGET + 1):
        data = header(number, hashes[-1] if hashes else "0x" + "00" * 32)
        encoded.append(data)
        hashes.append("0x" + geth.keccak256(data).hex())
    transactions = [[]] + [["0x" + geth.keccak256(rlp(transaction(n))).hex()] for n in range(1, TARGET + 1)]
    return hashes, encoded, transactions


HASHES, HEADERS, TRANSACTIONS = chain()


def item_data(saved=()):
    hashes = HASHES
    parent = HEADERS[TARGET - 1]
    if "parent-header" in saved:
        parent = header(TARGET - 1, hashes[TARGET - 2], geth.poison(ORACLE, TARGET - 1))
    body_number = TARGET - 1 if "body-rlp" in saved else TARGET
    data = {"header-rlp": HEADERS[TARGET], "body-rlp": rlp([[transaction(body_number)], []]),
            "encodings": ORACLE["encodings"].encode(), "parent-header": parent,
            "parent-hash": bytes.fromhex(hashes[TARGET - 1][2:]), "parent-root": bytes([TARGET]) * 32,
            "chain-config": json.dumps(ORACLE["chain_config"]).encode(),
            "fork-id": json.dumps({"hash": f"{geth.zlib.crc32(bytes.fromhex(hashes[0][2:])):08x}", "next": "0"}).encode(),
            "native-commit": COMMIT.encode(), "normalization-profile": ORACLE["normalization_profile"].encode()}
    for number in geth.window(ORACLE):
        name = f"ancestor-header-{number}"
        data[name] = HEADERS[number] if name not in saved else \
            header(number, hashes[number - 1] if number else "0x" + "00" * 32, geth.poison(ORACLE, number))
    return [(name, data[name]) for name in geth.item_names(ORACLE)]


def capsule(saved=()):
    refs = [{"name": name, "length": len(data), "sha256": sha(data)} for name, data in item_data(saved)]
    return {"observation": OBS, "items": refs, "digest": geth.capsule_digest(refs)}


def put(key):
    return {"op": "put", "key": f"extractor-source-inputs-v1/{OBS}/{key}", "length": 1}


SYNC = {"op": "sync"}


def seal_events(existed, poison=()):
    if existed:
        return [SYNC, {"op": "sealed"}]
    chunks = [put(f"capsule/{index:016x}/0000000000000000") for index in range(len(geth.item_names(ORACLE)))]
    return [{"op": "source", "poison": list(poison)}] + chunks + [SYNC, put("capsule"), SYNC, {"op": "sealed"}]


def lookups(saved=()):
    data = dict(item_data(saved))
    rows = []
    for number in ORACLE["requested_numbers"]:
        name = geth.headers(ORACLE)[number]
        rows.append({"op": "lookup", "number": number, "item": name, "response": geth.keccak256(data[name]).hex()})
    return rows


def consumed(capsule_digest):
    chain_value = source_inputs.chain_start(bytes.fromhex(capsule_digest))
    digest = source_inputs.consumed_digest(bytes.fromhex(capsule_digest), 0, chain_value).hex()
    return {"observation": OBS, "capsule_digest": capsule_digest, "count": 0, "chain": chain_value.hex(), "digest": digest}


def complete(name, want):
    saved = want["saved"]
    seal = capsule(saved)
    rows = [{"name": item, "data": data.hex()} for item, data in item_data(saved)]
    events = seal_events(want["capsule_existed"], saved) + [SYNC, {"op": "opened", "frontier": 0}]
    base = {"errors": [], "capsule_existed": want["capsule_existed"], "frontier_at_open": 0, "head_before": TARGET - 1,
            "capsule": seal, "capsule_items": rows}
    if want.get("rejected") == "body":
        base.update(head_number=TARGET - 1, head_hash=HASHES[TARGET - 1],
                    import_error="transaction root hash mismatch (header value aa, calculated bb)")
        return {"name": name, "exit_code": 0, "signal": "", "events": events, "result": base}
    events += [{"op": "process_start", "number": TARGET}] + lookups(saved)
    if want.get("rejected"):
        base.update(head_number=TARGET - 1, head_hash=HASHES[TARGET - 1],
                    import_error="invalid merkle root (remote: aa local: bb)")
        return {"name": name, "exit_code": 0, "signal": "", "events": events, "result": base}
    if want["closes"]:
        events += [put("closed"), SYNC]
    events.append({"op": "finished"})
    base.update(head_number=TARGET, head_hash=HASHES[TARGET], reader_slots=[HASHES[n] for n in ORACLE["requested_numbers"]],
                consumed=consumed(seal["digest"]), cursor=0,
                extra_after_finish=f"dependency log is closed: {OBS} at 0")
    return {"name": name, "exit_code": 0, "signal": "", "events": events, "result": base}


def crash(name, want):
    events = seal_events(False) + [{"op": "crash", "point": want["point"]}]
    return {"name": name, "exit_code": -1, "signal": "killed", "events": events}


def control(name):
    events = [{"op": "process_start", "number": TARGET}] + [{"op": "direct_poison", "number": n}
                                                             for n in ORACLE["requested_numbers"]]
    result = {"errors": [], "head_before": TARGET - 1, "head_number": TARGET - 1, "head_hash": HASHES[TARGET - 1],
              "import_error": "invalid merkle root (remote: aa local: bb)"}
    return {"name": name, "exit_code": 0, "signal": "", "events": events, "result": result}


def observation():
    scenarios = []
    for name, wants in ORACLE["scenarios"].items():
        attempts = []
        for want in wants:
            builder = {"complete": lambda: complete(want["name"], want), "crash": lambda: crash(want["name"], want),
                       "control": lambda: control(want["name"])}[want["kind"]]
            attempts.append(builder())
        scenarios.append({"name": name, "attempts": attempts})
    return {"family": "geth", "observation": OBS, "target": TARGET, "lookbacks": [1, 3, 1],
            "generated_hashes": list(HASHES), "generated_transactions": copy.deepcopy(TRANSACTIONS),
            "scenarios": scenarios}


def attempt(value, scenario, name):
    rows = {row["name"]: row for row in value["scenarios"]}[scenario]["attempts"]
    return {row["name"]: row for row in rows}[name]


CLEAN = ("clean_then_closed_replay", "clean-1")
REPLAY = ("clean_then_closed_replay", "clean-2-closed-replay")
PARENT = ("poisoned_parent_header", "poisoned-parent-header-2-replay")
ANCESTOR = ("poisoned_ancestor_header", "poisoned-ancestor-header-2-replay")
BODY = ("poisoned_body", "poisoned-body-2-replay")


def set_item(value, where, name, data):
    """Replace the saved bytes of one item and keep the manifest and digest consistent with them."""
    result = attempt(value, *where)["result"]
    row = next(row for row in result["capsule_items"] if row["name"] == name)
    row["data"] = data.hex()
    ref = next(ref for ref in result["capsule"]["items"] if ref["name"] == name)
    ref.update(length=len(data), sha256=sha(data))
    result["capsule"]["digest"] = geth.capsule_digest(result["capsule"]["items"])


class KeccakAndRlpTests(unittest.TestCase):
    def test_keccak_vectors(self):
        # Known Ethereum values, and two multi-block values from crypto.Keccak256 of the accepted Geth tree.
        self.assertEqual(geth.keccak256(b"").hex(), "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")
        self.assertEqual(geth.keccak256(b"abc").hex(), "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45")
        self.assertEqual(geth.keccak256(b"a" * 200).hex(),
                         "96ea54061def936c4be90b518992fdc6f12f535068a256229aca54267b4d084d")
        self.assertEqual(geth.keccak256(b"\x5a" * 136).hex(),
                         "ddc757d2caa82320e140f35833c18e8cc3b230b2b9a48def3d98461ffae81716")

    def test_rlp_decoding(self):
        data = rlp([b"\x01", b"abc", [b"", b"x" * 60], b"y" * 70])
        self.assertEqual([value for value, _ in geth.rlp_list(data)][:2], [b"\x01", b"abc"])
        self.assertEqual(geth.rlp_list(data)[2][1], rlp([b"", b"x" * 60]))
        self.assertEqual(geth.rlp_list(data)[3][0], b"y" * 70)
        for bad in (b"\x81\x01", b"\xb8\x01a", b"\xb9\x00\x40" + b"a" * 64, b"\x83ab", b"\xc2\x83a", data + b"\x00",
                    b"\x80", b"\xc1\xc5"):
            with self.assertRaises((ValueError, IndexError)):
                geth.rlp_list(bad)
        self.assertEqual(geth.rlp_list(rlp([b"\x7f"])), [(b"\x7f", b"\x7f")])
        # Each rule on its own: without it, the bytes would decode or fail differently.
        for bad, text in ((b"\xf8\x01\x80", "noncanonical RLP length"), (b"\xf9\x00\x01\x80", "noncanonical RLP length"),
                          (b"\xc4\x80\x80", "truncated RLP item"), (b"\xc2\x81\x01", "noncanonical RLP byte"),
                          (b"\xc4\xc1\x82ab", "RLP list overruns its length")):
            with self.assertRaisesRegex(ValueError, text):
                geth.rlp_list(bad)

    def test_header_fields(self):
        self.assertEqual(geth.header_fields(HEADERS[2])[geth.NUMBER], b"\x02")
        for bad in (rlp([b"a"] * 14), rlp([[b"a"]] + [b"a"] * 15)):
            with self.assertRaises(ValueError):
                geth.header_fields(bad)


class OracleTests(unittest.TestCase):
    def failures(self, change):
        value = observation()
        change(value)
        return geth.checked_compare(value, ORACLE, COMMIT)

    def assertRejected(self, change, text):
        failures = self.failures(change)
        self.assertTrue(any(text in failure for failure in failures), failures)

    def test_matching_observation_passes(self):
        self.assertEqual(geth.checked_compare(observation(), ORACLE, COMMIT), [])

    def test_item_names_cover_the_blockhash_window(self):
        self.assertEqual(geth.window(ORACLE), [0, 1, 2])
        self.assertEqual(geth.window(dict(ORACLE, target=300)), list(range(44, 299)))
        names = geth.item_names(ORACLE)
        self.assertEqual(names[names.index("parent-root") + 1:names.index("chain-config")],
                         ["ancestor-header-0", "ancestor-header-1", "ancestor-header-2"])
        self.assertEqual(geth.headers(ORACLE), {0: "ancestor-header-0", 1: "ancestor-header-1", 2: "ancestor-header-2",
                                                3: "parent-header"})

    def test_oracle_chain_config_is_the_genesis_config(self):
        self.assertEqual(ORACLE["chain_config"], json.loads((geth.FIXTURE / "genesis.json").read_text())["config"])

    def test_wrong_commit_is_rejected(self):
        self.assertIn("capsule item native-commit differs", " ".join(geth.checked_compare(observation(), ORACLE, "x")))

    def test_chain_identity(self):
        self.assertRejected(lambda v: v.update(family="bsc"), "does not describe the oracle chain")
        self.assertRejected(lambda v: v.update(observation="other"), "does not describe the oracle chain")
        self.assertRejected(lambda v: v.update(target=5), "does not describe the oracle chain")
        self.assertRejected(lambda v: v["generated_hashes"].pop(), "does not describe the oracle chain")
        self.assertRejected(lambda v: v["generated_transactions"].pop(), "does not describe the oracle chain")

    def test_scenario_set(self):
        self.assertRejected(lambda v: v["scenarios"].pop(), "scenario set differs")
        self.assertRejected(lambda v: v["scenarios"].append(copy.deepcopy(v["scenarios"][0])), "scenario set differs")
        self.assertRejected(lambda v: v["scenarios"][0]["attempts"].pop(), "attempts differ from the oracle")

    def test_complete_attempt_must_finish_cleanly(self):
        self.assertRejected(lambda v: attempt(v, *CLEAN).update(exit_code=1), "did not finish")
        self.assertRejected(lambda v: attempt(v, *CLEAN).update(signal="killed"), "did not finish")
        self.assertRejected(lambda v: attempt(v, *CLEAN).pop("result"), "did not finish")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"].update(errors=["x"]), "reported errors")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"].update(import_error="x"), "reported errors")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"].update(head_number=3), "imported head differs")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"].update(head_hash=HASHES[3]), "imported head differs")

    def test_durable_state_at_open(self):
        self.assertRejected(lambda v: attempt(v, *REPLAY)["result"].update(frontier_at_open=1), "durable state at open")
        self.assertRejected(lambda v: attempt(v, *REPLAY)["result"].update(capsule_existed=False),
                            "durable state at open")

    def test_capsule_manifest_checks(self):
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"]["capsule"]["items"].pop(), "capsule items differ")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"]["capsule"].update(digest="00" * 32),
                            "capsule digest differs")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"]["capsule"].update(observation="x"),
                            "capsule digest differs")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"]["capsule"]["items"][3].update(length=1),
                            "capsule digest differs")
        for length in (0, 65537, "1"):
            def change(value, length=length):
                attempt(value, *CLEAN)["result"]["capsule"]["items"][1]["length"] = length
            self.assertRejected(change, "empty or longer than one chunk")

    def test_saved_bytes_match_the_manifest(self):
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"].pop("capsule_items"), "saved capsule bytes differ")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"]["capsule_items"].pop(), "saved capsule bytes differ")

        def changed_bytes(value):
            row = attempt(value, *CLEAN)["result"]["capsule_items"][0]
            row["data"] = row["data"][:-2] + ("ff" if row["data"][-2:] != "ff" else "00")
        self.assertRejected(changed_bytes, "saved capsule bytes differ")

        def manifest_length(value):
            result = attempt(value, *CLEAN)["result"]
            result["capsule"]["items"][0]["length"] += 1
            result["capsule"]["digest"] = geth.capsule_digest(result["capsule"]["items"])
        self.assertRejected(manifest_length, "saved capsule bytes differ")

        def longer_bytes(value):
            attempt(value, *CLEAN)["result"]["capsule_items"][0]["data"] += "00"
        self.assertRejected(longer_bytes, "saved capsule bytes differ")

    def test_each_static_item_has_a_negative_control(self):
        # Every item replaced by wrong but well-formed bytes, with a consistent manifest, is rejected.
        wrong = {
            "header-rlp": HEADERS[3], "body-rlp": rlp([[transaction(3)], []]),
            "encodings": ORACLE["encodings"].replace("rlp(types.Body)", "ssz(types.Body)").encode(),
            "parent-header": HEADERS[2], "parent-hash": bytes.fromhex(HASHES[2][2:]),
            "parent-root": bytes([9]) * 32, "ancestor-header-0": HEADERS[1], "ancestor-header-1": HEADERS[0],
            "ancestor-header-2": HEADERS[1],
            "chain-config": json.dumps(dict(ORACLE["chain_config"], chainId=1)).encode(),
            "fork-id": json.dumps({"hash": "00000000", "next": "0"}).encode(),
            "native-commit": b"0" * 40, "normalization-profile": b"other-profile",
        }
        self.assertEqual(sorted(wrong), sorted(geth.item_names(ORACLE)))
        for name, data in wrong.items():
            failures = self.failures(lambda v, name=name, data=data: set_item(v, CLEAN, name, data))
            self.assertTrue(any(f"capsule item {name} " in failure for failure in failures), (name, failures))

    def test_header_checks(self):
        poisoned_native = header(3, HASHES[2])
        for where, name, data, text in (
                (CLEAN, "ancestor-header-2", header(1, HASHES[0]), "not the header of block 2"),
                (CLEAN, "ancestor-header-2", header(2, HASHES[0]), "not the header of block 2"),
                (CLEAN, "ancestor-header-2", header(5, HASHES[1]), "not the header of block 2"),
                (CLEAN, "ancestor-header-0", header(0, HASHES[0]), "not the native header of block 0"),
                (CLEAN, "parent-header", header(3, HASHES[2], geth.poison(ORACLE, 3)), "not the native header of block 3"),
                (PARENT, "parent-header", poisoned_native, "not the poisoned header of block 3"),
                (PARENT, "parent-header", header(3, HASHES[2], b"other"), "not the poisoned header of block 3"),
                (ANCESTOR, "ancestor-header-1", HEADERS[1], "not the poisoned header of block 1")):
            self.assertRejected(lambda v, w=where, n=name, d=data: set_item(v, w, n, d), text)

    def test_undecodable_items(self):
        for name, data in (("parent-header", b"\x01\x02"), ("body-rlp", b"\xc3\x01"), ("chain-config", b"{"),
                           ("ancestor-header-1", rlp([b"a"] * 3))):
            self.assertRejected(lambda v, n=name, d=data: set_item(v, CLEAN, n, d), "does not decode")

    def test_body_shape(self):
        for data in (rlp([[transaction(4)], [], []]), rlp([[transaction(4)], [rlp([b"a"])]]), rlp([b"x", []]),
                     rlp([[transaction(4), transaction(4)], []]), rlp([[rlp(transaction(4))], []])):
            self.assertRejected(lambda v, d=data: set_item(v, CLEAN, "body-rlp", d),
                                "capsule item body-rlp differs")
        typed = b"\x02" + rlp(transaction(4))
        transactions = copy.deepcopy(TRANSACTIONS)

        def typed_body(value):
            set_item(value, CLEAN, "body-rlp", rlp([[typed], []]))
            value["generated_transactions"][4] = ["0x" + geth.keccak256(typed).hex()]
        failures = self.failures(typed_body)
        self.assertFalse(any("clean-1: capsule item body-rlp" in failure for failure in failures), failures)
        self.assertEqual(transactions, TRANSACTIONS)

    def test_lookups(self):
        self.assertRejected(lambda v: attempt(v, *CLEAN)["events"].pop(-6), "BLOCKHASH lookups differ")

        def wrong_item(value):
            next(e for e in attempt(value, *CLEAN)["events"] if e["op"] == "lookup")["item"] = "ancestor-header-2"
        self.assertRejected(wrong_item, "BLOCKHASH answer differs from the saved header")

        def native_answer(value):
            next(e for e in attempt(value, *PARENT)["events"] if e["op"] == "lookup")["response"] = HASHES[3][2:]
        self.assertRejected(native_answer, "BLOCKHASH answer differs from the saved header")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"]["reader_slots"].__setitem__(1, HASHES[3]),
                            "did not receive the expected BLOCKHASH bytes")

        def poisoned_slots(value):
            # Lookups and slots that agree with each other but not with the generated chain.
            for where in (CLEAN,):
                for event in attempt(value, *where)["events"]:
                    if event["op"] == "lookup" and event["number"] == 1:
                        event["response"] = "00" * 32
        self.assertRejected(poisoned_slots, "BLOCKHASH answer differs")

    def test_rejections(self):
        for where in (PARENT, ANCESTOR):
            self.assertRejected(lambda v, w=where: attempt(v, *w)["result"].update(import_error=None),
                                "did not reject the saved poisoned item")
            self.assertRejected(lambda v, w=where: attempt(v, *w)["result"].update(
                import_error="transaction root hash mismatch"), "did not reject the saved poisoned item")
            self.assertRejected(lambda v, w=where: attempt(v, *w)["result"].update(head_number=4),
                                "did not reject the saved poisoned item")
            self.assertRejected(lambda v, w=where: attempt(v, *w)["result"].update(reader_slots=[]),
                                "did not reject the saved poisoned item")
            self.assertRejected(lambda v, w=where: attempt(v, *w)["result"].update(consumed={}),
                                "did not reject the saved poisoned item")
            self.assertRejected(lambda v, w=where: attempt(v, *w)["events"].append({"op": "finished"}),
                                "consumed manifest was not closed")
        self.assertRejected(lambda v: attempt(v, *BODY)["result"].update(import_error="invalid merkle root"),
                            "did not reject the saved poisoned item")

        def body_executed(value):
            attempt(value, *BODY)["events"].append({"op": "process_start", "number": 4})
        self.assertRejected(body_executed, "started with a saved body that native validation must reject")

        def parent_not_executed(value):
            events = attempt(value, *PARENT)["events"]
            events[:] = [e for e in events if e["op"] != "process_start"]
        self.assertRejected(parent_not_executed, "started only if the saved body was valid")

    def test_source_use(self):
        def retry_asks_source(value):
            attempt(value, *REPLAY)["events"].insert(0, {"op": "source", "poison": []})
        self.assertRejected(retry_asks_source, "a reseal asked the source")

        def wrong_poison(value):
            next(e for e in attempt(value, "poisoned_body", "poisoned-body-1")["events"] if e["op"] == "source")[
                "poison"] = []
        self.assertRejected(wrong_poison, "a new seal used the wrong source")

        def no_source(value):
            events = attempt(value, *CLEAN)["events"]
            events[:] = [e for e in events if e["op"] != "source"]
        self.assertRejected(no_source, "a new seal used the wrong source")
        self.assertRejected(no_source, "did not take its items from the source first")

        def late_source(value):
            events = attempt(value, *CLEAN)["events"]
            events.insert(3, events.pop(0))
        self.assertRejected(late_source, "did not take its items from the source first")

        def two_sources(value):
            attempt(value, *CLEAN)["events"].insert(0, {"op": "source", "poison": []})
        self.assertRejected(two_sources, "did not take its items from the source first")

    def test_consumed_manifest(self):
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"]["consumed"].update(digest="00" * 32),
                            "consumed manifest differs")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"]["consumed"].update(count=1), "consumed manifest differs")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"]["consumed"].update(capsule_digest="00" * 32),
                            "consumed manifest differs")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"].update(cursor=1), "closed log accepted")
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"].update(extra_after_finish="<nil>"),
                            "closed log accepted")

    def test_seal_order(self):
        def drop_last_sync(value):
            events = attempt(value, *CLEAN)["events"]
            del events[events.index({"op": "sealed"}) - 1]
        self.assertRejected(drop_last_sync, "manifest was not written once and synced")

        def drop_chunk_sync(value):
            events = attempt(value, *CLEAN)["events"]
            index = next(i for i, e in enumerate(events) if e.get("key", "").endswith("/capsule"))
            del events[index - 1]
        self.assertRejected(drop_chunk_sync, "chunks were not synced before the manifest")

        def no_chunks(value):
            events = attempt(value, *CLEAN)["events"]
            events[:] = [e for e in events if "/capsule/" not in e.get("key", "")]
        self.assertRejected(no_chunks, "did not write one chunk for every capsule item")

        def missing_chunk(value):
            events = attempt(value, *CLEAN)["events"]
            events[:] = [e for e in events if not e.get("key", "").endswith("/capsule/0000000000000003/0000000000000000")]
        self.assertRejected(missing_chunk, "did not write one chunk for every capsule item")

        def missing_chunk_in_crash(value):
            del attempt(value, "crash_after_seal", "seal-1-crash")["events"][1]
        self.assertRejected(missing_chunk_in_crash, "did not write one chunk for every capsule item")

        def two_manifests(value):
            attempt(value, *CLEAN)["events"].insert(1, put("capsule"))
        self.assertRejected(two_manifests, "manifest was not written once and synced")

        def double_seal(value):
            attempt(value, *CLEAN)["events"].insert(1, {"op": "sealed"})
        self.assertRejected(double_seal, "did not seal exactly once")

        def reseal_writes(value):
            attempt(value, "crash_after_seal", "seal-2-retry")["events"].insert(0, put("capsule"))
        self.assertRejected(reseal_writes, "rewrote the capsule")

        def reseal_no_sync(value):
            attempt(value, "crash_after_seal", "seal-2-retry")["events"].pop(0)
        self.assertRejected(reseal_no_sync, "rewrote the capsule or did not sync")

    def test_durable_before_execution(self):
        def move_start(value):
            events = attempt(value, *CLEAN)["events"]
            start = events.pop(next(i for i, e in enumerate(events) if e["op"] == "process_start"))
            events.insert(0, start)
        self.assertRejected(move_start, "not durable before execution started")

        def open_without_sync(value):
            events = attempt(value, *CLEAN)["events"]
            del events[events.index({"op": "sealed"}) + 1]
        self.assertRejected(open_without_sync, "not durable before execution started")

        def seal_after_open(value):
            events = attempt(value, *CLEAN)["events"]
            sealed = events.pop(events.index({"op": "sealed"}))
            events.insert(next(i for i, e in enumerate(events) if e["op"] == "opened") + 1, sealed)
        self.assertRejected(seal_after_open, "not durable before execution started")

        def no_open(value):
            events = attempt(value, *CLEAN)["events"]
            events[:] = [e for e in events if e["op"] != "opened"]
        self.assertRejected(no_open, "not durable before execution started")

        def body_no_open(value):
            events = attempt(value, *BODY)["events"]
            events[:] = [e for e in events if e["op"] != "opened"]
        self.assertRejected(body_no_open, "not durable before execution started")

    def test_close_tail(self):
        self.assertRejected(lambda v: attempt(v, *CLEAN)["events"].pop(), "consumed manifest was not closed")
        self.assertRejected(lambda v: attempt(v, *REPLAY)["events"].insert(-1, put("closed")),
                            "consumed manifest was not closed")
        self.assertRejected(lambda v: attempt(v, *BODY)["events"].append(put("closed")), "consumed manifest was not closed")

    def test_crash_attempt(self):
        seal = ("crash_after_seal", "seal-1-crash")
        self.assertRejected(lambda v: attempt(v, *seal).update(signal=""), "was not killed")
        self.assertRejected(lambda v: attempt(v, *seal).update(result={}), "was not killed")
        self.assertRejected(lambda v: attempt(v, *seal)["events"].pop(), "last event is not the expected crash")
        self.assertRejected(lambda v: attempt(v, *seal)["events"][-1].update(point="before_seal"),
                            "last event is not the expected crash")
        self.assertRejected(lambda v: attempt(v, *seal).update(events=[]), "last event is not the expected crash")
        self.assertRejected(lambda v: attempt(v, *seal)["events"][-1].update(op="sealed"),
                            "last event is not the expected crash")
        self.assertRejected(lambda v: attempt(v, *seal)["events"].insert(-1, SYNC), "did not follow a completed seal")
        self.assertRejected(lambda v: attempt(v, *seal)["events"].pop(-3), "manifest was not written once and synced")

    def test_control(self):
        control_attempt = ("control_direct_poison", "control-1")
        self.assertRejected(lambda v: attempt(v, *control_attempt)["result"].update(import_error=None),
                            "poisoned control was not rejected")
        self.assertRejected(lambda v: attempt(v, *control_attempt)["result"].update(head_number=4),
                            "poisoned control was not rejected")
        self.assertRejected(lambda v: attempt(v, *control_attempt)["events"].pop(), "poisoned control was not rejected")
        self.assertRejected(lambda v: attempt(v, *control_attempt)["events"][1].update(number=2),
                            "poisoned control was not rejected")

    def test_unknown_attempt_kind(self):
        oracle = copy.deepcopy(ORACLE)
        oracle["scenarios"]["control_direct_poison"][0]["kind"] = "other"
        self.assertIn("unknown attempt kind", " ".join(geth.checked_compare(observation(), oracle, COMMIT)))

    def test_malformed_observation(self):
        self.assertRejected(lambda v: v["scenarios"][0].update(attempts=None), "attempts differ")
        self.assertTrue(geth.checked_compare({"family": "geth", "observation": OBS, "target": 4,
                                              "generated_hashes": None}, ORACLE, COMMIT))
        self.assertRejected(lambda v: attempt(v, *CLEAN)["result"].update(capsule=None), "capsule items differ")


class InstallTests(unittest.TestCase):
    def tree(self, text="\tdefer evm.Release()\n"):
        directory = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, directory)
        (directory / "core").mkdir()
        (directory / geth.ANCHOR_FILE).write_text("func Process() {\n" + text + "}\n")
        return directory

    def test_install_adds_hook_and_files(self):
        source = self.tree()
        row = geth.install(source, geth.FIXTURE, geth.SHARED)
        patched = (source / geth.ANCHOR_FILE).read_text()
        self.assertIn(geth.ANCHOR + geth.HOOK_CALL, patched)
        self.assertEqual(row["sha256_after"], sha(patched.encode()))
        for name, relative in geth.TREE_FILES.items():
            origin = (geth.SHARED if name in geth.SHARED_FILES else geth.FIXTURE) / name
            self.assertEqual((source / relative).read_bytes(), origin.read_bytes())

    def test_install_refuses_bad_anchor_or_existing_paths(self):
        for text in ("", "\tdefer evm.Release()\n\tdefer evm.Release()\n", "\tdefer evm.Release()\n// extractorGetHash\n"):
            with self.assertRaises(Exception):
                geth.install(self.tree(text), geth.FIXTURE, geth.SHARED)
        source = self.tree()
        (source / "cmd/extractor-inputs-probe").mkdir(parents=True)
        (source / "cmd/extractor-inputs-probe/probe.go").write_text("x")
        with self.assertRaises(Exception):
            geth.install(source, geth.FIXTURE, geth.SHARED)

    def test_native_mutants_apply_once_and_restore(self):
        source = self.tree()
        geth.install(source, geth.FIXTURE, geth.SHARED)
        table = json.loads((geth.FIXTURE / "native_mutants.json").read_text())
        self.assertGreaterEqual(len(table["mutants"]), 3)
        for mutant in table["mutants"]:
            path = source / geth.TREE_FILES[mutant["file"]]
            before = path.read_bytes()
            restore = geth.apply_mutant(source, mutant)
            self.assertNotEqual(path.read_bytes(), before)
            restore()
            self.assertEqual(path.read_bytes(), before)
        with self.assertRaises(Exception):
            geth.apply_mutant(source, {"name": "x", "file": "probe.go", "original": "no such text", "replacement": ""})

    def test_fixture_files_exist(self):
        for name in geth.FIXTURE_FILES:
            self.assertTrue((geth.FIXTURE / name).is_file(), name)


if __name__ == "__main__":
    unittest.main()
