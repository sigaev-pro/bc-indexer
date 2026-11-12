"""Portable tests of the E01-02.admission Geth oracle and driver.

A synthetic observation, built here from the formulas of the shared README and
the Geth README, must pass the oracle; each negative control changes one fact
and must fail it with the expected message. The driver tests run the patch
installer on a stand-in tree and check the mutant table against the patched
text.
"""

import contextlib
import copy
import functools
import io
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
import zlib

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import admission_geth  # noqa: E402
from admission_geth import admission, decision, source_inputs  # noqa: E402
from source_inputs_geth import keccak256  # noqa: E402

ORACLE = json.loads((HERE / "expected.json").read_text())
COMMIT = "1df30b18d9ed35ee9d28ffd4648442c2204554ad"
T = ORACLE["target"]
ZERO = "0x" + "0" * 64


def rlp(value):
    """Minimal RLP encoder for byte strings and lists."""
    if isinstance(value, list):
        body = b"".join(rlp(item) for item in value)
        prefix = 0xC0
    else:
        if len(value) == 1 and value[0] < 0x80:
            return value
        body, prefix = value, 0x80
    if len(body) < 56:
        return bytes([prefix + len(body)]) + body
    size = len(body).to_bytes((len(body).bit_length() + 7) // 8, "big")
    return bytes([prefix + 55 + len(size)]) + size + body


def number_bytes(value):
    return value.to_bytes((value.bit_length() + 7) // 8, "big")


def sha(data):
    return hashlib.sha256(data).hexdigest()


@functools.lru_cache(maxsize=1)
def chain():
    """Synthetic headers of blocks 1..T+1 that hash and link like a Geth chain."""
    hashes, headers = [ORACLE["genesis_hash"]], []
    for number in range(1, T + 2):
        root = hashlib.sha256(b"root" + number_bytes(number)).digest()
        fields = [bytes.fromhex(hashes[-1][2:]), bytes(32), bytes(20), root, bytes(32), bytes(32), bytes(256), b"\x01",
                  number_bytes(number), number_bytes(30_000_000), b"", number_bytes(number), b"", bytes(32), bytes(8)]
        header = rlp(fields)
        headers.append(header.hex())
        hashes.append("0x" + keccak256(header).hex())
    return hashes, headers


class Synthetic:
    """Builds probe attempts the way the probe reports them."""

    def __init__(self):
        self.hashes, self.headers = chain()
        self.bad_hash = "0x" + sha(b"bad block")

    def header(self, number):
        return bytes.fromhex(self.headers[number - 1])

    def items(self, number, bad=False):
        names = ["header-rlp", "body-rlp", "encodings", "parent-header", "parent-hash", "parent-root"] + \
            [f"ancestor-header-{n}" for n in range(max(0, number - 256), number - 1)] + \
            ["chain-config", "fork-id", "native-commit", "normalization-profile"]
        config = admission.canonical(json.dumps(ORACLE["chain_config"]).encode())
        fork = json.dumps({"hash": f"{zlib.crc32(bytes.fromhex(self.hashes[0][2:])):08x}", "next": "0"},
                          separators=(",", ":")).encode()
        parent = self.header(number - 1)
        data = {"header-rlp": b"bad header" if bad else self.header(number), "body-rlp": b"\xc2\xc0\xc0",
                "encodings": ORACLE["encodings"].encode(), "parent-header": parent,
                "parent-hash": bytes.fromhex(self.hashes[number - 1][2:]),
                "parent-root": admission_geth.header_fields(parent)[admission_geth.STATE_ROOT],
                "chain-config": config, "fork-id": fork, "native-commit": COMMIT.encode(),
                "normalization-profile": ORACLE["profile"].encode()}
        for n in range(max(0, number - 256), number - 1):
            data[f"ancestor-header-{n}"] = self.header(n)
        return [(name, data[name]) for name in names]

    def observation_records(self, sequence, number, state, bad=False):
        """Return (records, pins, capsule) of one observation in the given state."""
        key = admission_geth.key_of(ORACLE, sequence)
        prefix = f"extractor-admission-v1/{ORACLE['producer_id']}.{ORACLE['run_id']}/{sequence:016x}/"
        cand = {"number": number, "hash": (self.bad_hash if bad else self.hashes[number])[2:],
                "parent_hash": self.hashes[number - 1][2:],
                "parent_root": hashlib.sha256(b"root" + number_bytes(number - 1)).hexdigest()}
        records = {prefix + "preparing": {"observation": key, "sequence": sequence, "candidate": cand}}
        items = self.items(number, bad)
        refs = [{"name": name, "length": len(data), "sha256": sha(data)} for name, data in items]
        capsule = {"capsule": {"observation": key, "items": refs, "digest": source_inputs.capsule_digest(items).hex()},
                   "items": [{"name": name, "data": data.hex()} for name, data in items]}
        pin = json.dumps({"id": key, "store": {"store_id": ORACLE["store_id"]}, "profile": "0" * 64,
                          "parent_root": "0x" + cand["parent_root"],
                          "headers": [{"hash": "0x" + cand["parent_hash"], "number": number - 1}], "codes": []},
                         separators=(",", ":"))
        pins = {}
        if state in ("EXEC_READY", "ADMITTED"):
            pins[ORACLE["pin_prefix"] + key] = pin
        if state == "ABANDONED":
            records[prefix + "abandoned"] = {"observation": key, "sequence": sequence}
        if state in ("EXEC_READY", "ADMITTED", "NATIVE_REJECTED"):
            digest = capsule["capsule"]["digest"]
            admitted = admission.admitted_digest(key, sequence, cand, sha(pin.encode()), digest)
            records[prefix + "admitted"] = {"observation": key, "sequence": sequence, "candidate": cand,
                                            "pin_digest": sha(pin.encode()), "capsule_digest": digest, "digest": admitted}
            if state == "EXEC_READY":
                chain_value = source_inputs.chain_start(bytes.fromhex(digest))
                consumed = source_inputs.consumed_digest(bytes.fromhex(digest), 0, chain_value).hex()
                execution = sha(b"exec" + number_bytes(number))
                records[prefix + "exec-ready"] = {"observation": key, "sequence": sequence, "admitted_digest": admitted,
                                                  "consumed_digest": consumed, "exec_digest": execution,
                                                  "digest": admission.exec_ready_digest(admitted, consumed, execution)}
            if state == "NATIVE_REJECTED":
                evidence = sha(b"evidence")
                records[prefix + "rejected"] = {"observation": key, "sequence": sequence, "admitted_digest": admitted,
                                                "evidence_digest": evidence,
                                                "digest": admission.rejected_digest(admitted, evidence)}
        sealed = state in ("EXEC_READY", "ADMITTED", "NATIVE_REJECTED")
        return records, pins, ({key: capsule} if sealed else {}), key

    def result(self, observations, counter, head, executed, mode="LIVE", items=False, **extra):
        records, pins, capsules = {}, {}, {}
        for sequence, number, state, bad in observations:
            rows, live, capsule, _ = self.observation_records(sequence, number, state, bad)
            records.update(rows)
            pins.update(live)
            if not items:
                capsule = {name: {"capsule": row["capsule"]} for name, row in capsule.items()}
            capsules.update(capsule)
        records[f"extractor-admission-v1/{ORACLE['producer_id']}.{ORACLE['run_id']}/counter"] = {"next": counter}
        result = {"run": ORACLE["run_id"], "mode": mode, "mode_error": None,
                  "records": {key: json.dumps(value, separators=(",", ":")) for key, value in records.items()},
                  "pins": pins, "capsules": capsules, "head_number": head, "head_hash": self.hashes[head],
                  "captured_inputs": [f"{n:064x}" for n in executed], "imports": [],
                  "run_record": {ORACLE["run_key"]: json.dumps(
                      {"store_id": ORACLE["store_id"], "producer_id": ORACLE["producer_id"], "run_id": ORACLE["run_id"]},
                      separators=(",", ":"))}}
        if head >= T:
            result["reader_slots"] = [self.hashes[head - back] if back <= 256 else ZERO for back in ORACLE["lookbacks"]]
        result.update(extra)
        return result

    def lookups(self, numbers):
        rows = []
        for n in numbers:
            for back in ORACLE["lookbacks"]:
                if back <= 256:
                    rows.append({"op": "trace", "event": "lookup", "number": n - back,
                                 "hash": self.hashes[n - back][2:], "found": True})
        return rows

    def admission_events(self, numbers, final=("exec_ready",), lookup=True):
        """Events of a sequence of live admissions: begin, pin, seal, the synced ADMITTED record, lookups, end."""
        rows = []
        for index, n in enumerate(numbers):
            rows += [trace("begin"), trace("pinned"), {"op": "put", "key": "x/capsule"}, trace("sealed"),
                     {"op": "put", "key": f"x/{index:016x}/admitted"}, {"op": "sync"}, trace("admitted")]
            rows += self.lookups([n]) if lookup else []
            rows.append(trace(final[index] if index < len(final) else final[-1]))
        return rows


def trace(event, **fields):
    return {"op": "trace", "event": event, **fields}


def attempt(name, result=None, events=(), crash=None):
    row = {"name": name, "events": list(events), "signal": "", "exit_code": 0}
    if crash:
        row.update(signal="killed", exit_code=-1)
        row["events"] = list(events) + [{"op": "crash", "point": crash}]
    else:
        row["result"] = result
    return row


@functools.lru_cache(maxsize=1)
def synthetic_observation():
    s = Synthetic()
    opened = [trace("opened"), trace("live")]
    scenarios = []
    clean = s.result([(0, T, "EXEC_READY", False), (1, T + 1, "EXEC_READY", False)], 2, T + 1, [T, T + 1], items=True,
                     imports=[{"name": "target", "error": None}, {"name": "next", "error": None}])
    restart = copy.deepcopy(clean)
    restart.update(imports=[], captured_inputs=[])
    restart["capsules"] = {key: {"capsule": row["capsule"]} for key, row in clean["capsules"].items()}
    rewind = copy.deepcopy(restart)
    rewind.update(rewind_error=ORACLE["messages"]["live_pins"] + ": rewind", rewind_head=[T + 1, T + 1], witness_error=None)
    scenarios.append({"name": "clean", "attempts": [
        attempt("clean-1", clean, opened + s.admission_events([T, T + 1])),
        attempt("clean-2-restart", restart, opened), attempt("clean-3-rewind", rewind, opened)]})
    for name, point, abandoned, pinned in (("begin_before_counter_sync", "before_sync/counter", False, False),
                                           ("begin_after_counter_sync", "after_sync/counter", True, False),
                                           ("admit_before_sync", "before_sync/admitted", True, True)):
        label = name.replace("_", "-")
        crash_events = opened + ([trace("begin"), trace("pinned"), trace("sealed")] if pinned else [])
        sequence = 1 if abandoned else 0
        rows = ([(0, T, "ABANDONED", False)] if abandoned else []) + [(sequence, T, "EXEC_READY", False)]
        retry = s.result(rows, sequence + 1, T, [T], imports=[{"name": "target", "error": None}])
        retry_events = [trace("opened")] + ([trace("abandoned")] if abandoned else []) + [trace("live")] + \
            s.admission_events([T])
        scenarios.append({"name": name, "attempts": [attempt(f"{label}-1-crash", events=crash_events, crash=point),
                                                     attempt(f"{label}-2-recover", retry, retry_events)]})
    scenarios.append({"name": "removedb_allowed", "attempts": [
        attempt("removedb-allowed-1-crash", events=opened, crash="before_sync/counter")]})
    held = s.result([(0, T, "ADMITTED", False)], 1, T - 1, [], mode="RECOVERY_ONLY",
                    new_h_error=ORACLE["messages"]["mode"] + ": block", go_live_error=ORACLE["messages"]["pending"] + ": x")
    recovered = s.result([(0, T, "EXEC_READY", False), (1, T + 1, "EXEC_READY", False)], 2, T + 1, [T, T + 1],
                         imports=[{"name": "next", "error": None}])
    recover_events = [trace("opened"), trace("recovering"), trace("resumed")] + s.lookups([T]) + \
        [trace("exec_ready"), trace("recovered", state="EXEC_READY"), trace("live")] + s.admission_events([T + 1])
    scenarios.append({"name": "admit_after_sync", "attempts": [
        attempt("admit-after-sync-1-crash", events=opened + [trace("begin"), trace("pinned"), trace("sealed")],
                crash="after_sync/admitted"),
        attempt("admit-after-sync-2-hold", held, [trace("opened")]),
        attempt("admit-after-sync-3-changed-profile",
                dict(copy.deepcopy(held), attach_error=ORACLE["messages"]["recovery"] + ": x"),
                [trace("opened"), trace("recovering")]),
        attempt("admit-after-sync-4-recover", recovered, recover_events)]})
    exec_recovered = s.result([(0, T, "EXEC_READY", False)], 1, T - 1, [T], stored={"target": True})
    scenarios.append({"name": "exec_ready_before_sync", "attempts": [
        attempt("exec-ready-before-sync-1-crash",
                events=opened + [trace("begin"), trace("pinned"), trace("sealed"), trace("admitted")],
                crash="before_sync/exec-ready"),
        attempt("exec-ready-before-sync-2-recover", exec_recovered,
                [trace("opened"), trace("recovering"), trace("resumed")] + s.lookups([T]) +
                [trace("exec_ready"), trace("recovered", state="EXEC_READY"), trace("live")])]})
    rejected = s.result([(0, T, "NATIVE_REJECTED", True), (1, T, "EXEC_READY", False)], 2, T, [T],
                        imports=[{"name": "bad", "error": ORACLE["control_errors"]["bad"] + " (remote: x)"},
                                 {"name": "target", "error": None}])
    scenarios.append({"name": "reject_live", "attempts": [
        attempt("reject-live-1", rejected, opened + s.admission_events([T, T], ("rejected", "exec_ready")))]})
    after = s.result([(0, T, "NATIVE_REJECTED", True), (1, T, "EXEC_READY", False)], 2, T, [T],
                     imports=[{"name": "target", "error": None}])
    scenarios.append({"name": "reject_after_admit", "attempts": [
        attempt("reject-after-admit-1-crash", events=opened + [trace("begin"), trace("pinned"), trace("sealed")],
                crash="after_sync/admitted"),
        attempt("reject-after-admit-2-recover", after,
                [trace("opened"), trace("recovering"), trace("resumed"), trace("rejected"),
                 trace("recovered", state="NATIVE_REJECTED"), trace("live")] + s.admission_events([T]))]})
    without = s.result([(0, T, "EXEC_READY", False)], 1, T, [T],
                       imports=[{"name": "target-without-head", "error": None}, {"name": "canonical-target", "error": None}])
    scenarios.append({"name": "without_set_head", "attempts": [
        attempt("without-set-head-1", without, opened + s.admission_events([T]))]})
    scenarios.append({"name": "freezer", "attempts": [attempt("freezer-1", {
        "frozen": ORACLE["side_fork"] + 1, "side_header_kept": True, "canonical_fork_kept": True})]})
    scenarios += decision_scenarios(s)
    store = ["000004.log 120 " + "ab" * 32, "CURRENT 16 " + "cd" * 32, "ancient/chain/headers.0000.cdat 9 " + "ef" * 32]
    shipped = []
    for name, kind in ORACLE["shipped"].items():
        if kind == "deleted":
            shipped.append({"name": name, "exit_code": 0, "timed_out": False, "output_tail": "Database successfully deleted",
                            "store_before": list(store), "store_after": []})
            continue
        row = {"name": name, "exit_code": 1, "timed_out": False, "output_tail": "Fatal: " + ORACLE["messages"][kind]}
        if name in ORACLE["store_unchanged"]:
            row.update(store_before=list(store), store_after=list(store))
        shipped.append(row)
    return {"family": "geth", "target": T, "lookbacks": ORACLE["lookbacks"], "generated_hashes": s.hashes,
            "generated_headers": s.headers, "bad_hash": s.bad_hash, "scenarios": scenarios, "shipped": shipped}


PRODUCER = f"{ORACLE['producer_id']}.{ORACLE['run_id']}"


def root_of(number):
    """The state root field of synthetic header number."""
    return hashlib.sha256(b"root" + number_bytes(number)).hexdigest()


def ready_of(number):
    """Synthetic EXEC_READY digest of the observation of block number."""
    return sha(b"ready" + number_bytes(number))


def exec_of(number):
    return sha(b"exec" + number_bytes(number))


def geth_intent(s, sequence, kind, old, new, members):
    """An intent record from the Geth intent rules, members as (observation sequence, block number)."""
    first = members[0][1]
    evidence = hashlib.sha256(b"extractor-geth-intent-evidence-v1\x00" + len(kind).to_bytes(8, "big") + kind.encode() +
                              bytes.fromhex(s.hashes[first][2:]) + first.to_bytes(8, "big") +
                              bytes.fromhex(root_of(first))).hexdigest()
    record = {"native_store_id": ORACLE["store_id"], "producer": PRODUCER, "sequence": sequence, "kind": kind,
              "old_head": {"number": old, "hash": s.hashes[old][2:]},
              "new_head": {"number": new, "hash": s.hashes[new][2:]},
              "members": [{"observation": admission_geth.key_of(ORACLE, obs), "block": s.hashes[n][2:],
                           "exec_ready_digest": ready_of(n), "child_root": root_of(n)} for obs, n in members],
              "evidence_digest": evidence, "events": kind != "SIDE"}
    record["digest"] = decision.intent_digest(record)
    return record


def intent_events(record, delivery, point_after="mutated", points=()):
    rows = [trace("committed"), trace("intent", sequence=record["sequence"], kind=record["kind"],
                                     digest=record["digest"], marker=decision.marker_value(record).decode())]
    if point_after:
        rows.append(trace(point_after))
    rows += [trace(point) for point in points]
    if delivery:
        rows.append(trace("decided", sequence=record["sequence"], delivery=delivery,
                          digest=decision.decided_digest(record["digest"], delivery)))
    return rows


def rejection(record):
    evidence = hashlib.sha256(b"extractor-geth-native-refusal-v1\x00" +
                              bytes.fromhex(record["members"][0]["block"])).hexdigest()
    return trace("intent_rejected", sequence=record["sequence"],
                 digest=decision.rejected_digest(record["digest"], evidence))


def admitted_events(s, n, sequence, lookup=True):
    """A live admission of block n up to EXEC_READY."""
    rows = [trace("begin"), trace("pinned"), {"op": "put", "key": "x/capsule"}, trace("sealed"),
            {"op": "put", "key": f"x/{sequence:016x}/admitted"}, {"op": "sync"}, trace("admitted")]
    rows += s.lookups([n]) if lookup else []
    return rows + [trace("exec_ready", observation=admission_geth.key_of(ORACLE, sequence), digest=ready_of(n),
                         exec=exec_of(n))]


def decision_result(s, counter, admitted, head, executed, markers, imports=(), **extra):
    result = s.result([], admitted, head, executed, imports=[{"name": name, "error": error} for name, error in imports])
    del result["records"]
    result["records"] = {f"extractor-admission-v1/{PRODUCER}/counter": json.dumps({"next": admitted})}
    result.update(markers={decision.marker_key(ORACLE["producer_id"], ORACLE["run_id"], seq): "{}" for seq in markers},
                  decision_records={f"extractor-decision-v1/{PRODUCER}/counter": json.dumps({"next": counter})},
                  inputs_left={}, bad_blocks={"target": False, "next": False},
                  roots_at_start={"target": False, "next": False})
    result.update(extra)
    return result


def decision_scenarios(s):
    """The decision scenarios of the probe, built from the decision and Geth intent rules."""
    base = [trace("opened"), trace("bridge_opened"), trace("live")]
    out = []
    i0 = geth_intent(s, 0, "IMPORT", T - 1, T, [(0, T)])
    i1 = geth_intent(s, 1, "IMPORT", T, T + 1, [(1, T + 1)])
    clean = decision_result(s, 2, 2, T + 1, [T, T + 1], [0, 1], [("target", None), ("next", None)])
    done = {f"extractor-decision-v1/{PRODUCER}/{name}": json.dumps({field: 2}, separators=(",", ":"))
            for name, field in (("counter", "next"), ("lowwater", "sequence"), ("purged", "sequence"))}
    restart = decision_result(s, 2, 2, T + 1, [], [], [("target", None), ("next", None)], decision_records=done)
    out.append({"name": "decision_clean", "attempts": [
        attempt("decision-clean-1", clean, base + admitted_events(s, T, 0) + intent_events(i0, "BATCH") +
                admitted_events(s, T + 1, 1) + intent_events(i1, "BATCH") +
                [{"op": "mirror", "sequence": 0}, {"op": "mirror", "sequence": 1}]),
        attempt("decision-clean-2-restart", restart, base + [{"op": "mirror", "sequence": 0}])]})
    side = geth_intent(s, 0, "SIDE", T - 1, T - 1, [(0, T)])
    canonical = geth_intent(s, 1, "CANONICAL", T - 1, T + 1, [(1, T + 1), (0, T)])
    replayed = [trace("replaying"), trace("replayed")] + intent_events(side, "CHECKPOINT") + [trace("live")] + \
        admitted_events(s, T + 1, 1) + intent_events(canonical, "BATCH", points=("intermediate",))
    early = base + [trace("begin"), trace("pinned"), trace("sealed"), trace("admitted")]
    recovered = decision_result(s, 2, 2, T + 1, [T, T + 1], [1], [("next", None)])
    out.append({"name": "decision_exec_ready", "attempts": [
        attempt("decision-exec-ready-1-crash", events=early, crash="before_sync/exec-ready"),
        attempt("decision-exec-ready-2-recover", recovered,
                [trace("opened"), trace("recovering"), trace("resumed")] + s.lookups([T]) +
                [trace("exec_ready", observation=admission_geth.key_of(ORACLE, 0), digest=ready_of(T), exec=exec_of(T)),
                 trace("deferred"), trace("recovered", state="EXEC_READY"), trace("bridge_opened")] + replayed)]})
    rows, _, _, _ = s.observation_records(0, T, "EXEC_READY")
    for key, value in rows.items():
        if key.endswith("/exec-ready"):
            ready = value["digest"]
    side_saved = copy.deepcopy(side)
    side_saved["members"][0]["exec_ready_digest"] = ready
    side_saved["digest"] = decision.intent_digest(side_saved)
    canonical_saved = copy.deepcopy(canonical)
    canonical_saved["members"][1]["exec_ready_digest"] = ready
    canonical_saved["digest"] = decision.intent_digest(canonical_saved)
    child = decision_result(s, 2, 2, T + 1, [T + 1], [1], [("next", None)],
                            records_at_start={key: json.dumps(value, separators=(",", ":")) for key, value in rows.items()})
    out.append({"name": "decision_child_root", "attempts": [
        attempt("decision-child-root-1-crash", events=early, crash="after_sync/exec-ready"),
        attempt("decision-child-root-2-recover", child,
                [trace("opened"), trace("bridge_opened"), trace("replaying"), trace("replayed")] +
                intent_events(side_saved, "CHECKPOINT") + [trace("live")] + admitted_events(s, T + 1, 1) +
                intent_events(canonical_saved, "BATCH", points=("intermediate",)))]})
    live = base + admitted_events(s, T, 0)
    nxt = [trace("live")] + admitted_events(s, T + 1, 1) + intent_events(i1, "BATCH")
    completed = decision_result(s, 2, 2, T + 1, [T, T + 1], [0, 1], [("next", None)],
                                roots_at_start={"target": True, "next": False})
    out.append({"name": "decision_intent", "attempts": [
        attempt("decision-intent-1-crash", events=live + [trace("committed")],
                crash=f"after_sync-decision-v1/{PRODUCER}/counter"),
        attempt("decision-intent-2-recover", completed,
                [trace("opened"), trace("bridge_opened"), trace("settling", sequence=0, evidence="reimport"),
                 trace("replaying"), trace("replayed")] + intent_events(i0, "BATCH") + nxt)]})
    settled = [trace("decided", sequence=0, delivery="BATCH", digest=decision.decided_digest(i0["digest"], "BATCH"))]
    mutation = copy.deepcopy(completed)
    mutation["imports"] = [{"name": "target", "error": ORACLE["messages"]["known_block"] + ": block 300"},
                           {"name": "next", "error": None}]
    out.append({"name": "decision_mutation", "attempts": [
        attempt("decision-mutation-1-crash", events=live + intent_events(i0, None), crash="native/mutated"),
        attempt("decision-mutation-2-recover", mutation,
                [trace("opened"), trace("bridge_opened"), trace("settling", sequence=0, evidence="stored_block")] +
                settled + nxt)]})
    out.append({"name": "decision_marker", "attempts": [
        attempt("decision-marker-1-crash", events=live + intent_events(i0, None),
                crash=f"before_sync-decision-v1/{PRODUCER}/0000000000000000/decided"),
        attempt("decision-marker-2-recover", completed,
                [trace("opened"), trace("bridge_opened"), trace("settling", sequence=0, evidence="marker")] +
                settled + nxt)]})
    refused = decision_result(s, 1, 1, T - 1, [], [], (), stored={"target": False, "next": False, "bad": False},
                              bad_blocks={"target": True, "next": False})
    out.append({"name": "decision_rejected", "attempts": [
        attempt("decision-rejected-1-crash", events=live + intent_events(i0, None, "refused"),
                crash=f"before_sync-decision-v1/{PRODUCER}/0000000000000000/rejected"),
        attempt("decision-rejected-2-recover", refused,
                [trace("opened"), trace("bridge_opened"), trace("settling", sequence=0, evidence="bad_block"),
                 rejection(i0), trace("live")])]})
    # AC-CAP-07-03 on a CANONICAL write over the retained side block 300.
    before = base + admitted_events(s, T, 0) + intent_events(side, "CHECKPOINT") + admitted_events(s, T + 1, 1)
    finished = decision_result(s, 2, 2, T + 1, [T, T + 1], [1], head_at_start=s.hashes[T - 1])
    mid_reorg = decision_result(s, 2, 2, T + 1, [T, T + 1], [1], head_at_start=s.hashes[T])
    settled_canonical = [trace("decided", sequence=1, delivery="BATCH",
                               digest=decision.decided_digest(canonical["digest"], "BATCH"))]
    out.append({"name": "decision_canonical_intent", "attempts": [
        attempt("decision-canonical-intent-1-crash", events=before + [trace("committed")],
                crash=f"after_sync-decision-v1/{PRODUCER}/counter"),
        attempt("decision-canonical-intent-2-recover", finished,
                [trace("opened"), trace("bridge_opened"), trace("settling", sequence=1, evidence="reimport"),
                 trace("replaying"), trace("replayed")] +
                intent_events(canonical, "BATCH", points=("intermediate",)) + [trace("live")])]})
    out.append({"name": "decision_canonical_mutated", "attempts": [
        attempt("decision-canonical-mutated-1-crash", events=before + intent_events(canonical, None),
                crash="native/mutated"),
        attempt("decision-canonical-mutated-2-recover", finished,
                [trace("opened"), trace("bridge_opened"), trace("settling", sequence=1, evidence="stored_block")] +
                settled_canonical + [trace("live")])]})
    out.append({"name": "decision_canonical_intermediate", "attempts": [
        attempt("decision-canonical-intermediate-1-crash",
                events=before + intent_events(canonical, None, points=("intermediate",)), crash="native/intermediate"),
        attempt("decision-canonical-intermediate-2-recover", mid_reorg,
                [trace("opened"), trace("bridge_opened"), trace("settling", sequence=1, evidence="stored_block")] +
                settled_canonical + [trace("live")])]})
    refused_live = copy.deepcopy(refused)
    refused_live["imports"] = [{"name": "target", "error": ORACLE["messages"]["refused"] + "\nx"}]
    out.append({"name": "decision_reject_live", "attempts": [
        attempt("decision-reject-live-1", refused_live, live + intent_events(i0, None, "refused") + [rejection(i0)])]})
    return out


def fresh():
    return copy.deepcopy(synthetic_observation())


def scenario(observation, name):
    return {row["name"]: row for row in observation["scenarios"]}[name]["attempts"]


def reseal(result, sequence):
    """Recompute the capsule, admitted and EXEC_READY digests of one observation after a test changed its items or
    pin, so only the changed fact differs."""
    key = admission_geth.key_of(ORACLE, sequence)
    row = result["capsules"][key]
    if "items" in row:
        for ref, item in zip(row["capsule"]["items"], row["items"]):
            data = bytes.fromhex(item["data"])
            ref["sha256"], ref["length"] = sha(data), len(data)
    pairs = [(ref["name"], None) for ref in row["capsule"]["items"]]
    digest = hashlib.sha256(source_inputs.tag("extractor-source-inputs-capsule-v1") + source_inputs.u64(len(pairs)) +
                            b"".join(source_inputs.u64(len(ref["name"])) + ref["name"].encode() +
                                     source_inputs.u64(ref["length"]) + bytes.fromhex(ref["sha256"])
                                     for ref in row["capsule"]["items"])).hexdigest()
    row["capsule"]["digest"] = digest
    prefix = [name for name in result["records"] if name.endswith(f"{sequence:016x}/admitted")][0][:-len("admitted")]
    admitted = json.loads(result["records"][prefix + "admitted"])
    pin = result["pins"].get(ORACLE["pin_prefix"] + key)
    if pin is not None:
        admitted["pin_digest"] = sha(pin.encode())
    admitted["capsule_digest"] = digest
    admitted["digest"] = admission.admitted_digest(key, sequence, admitted["candidate"], admitted["pin_digest"], digest)
    result["records"][prefix + "admitted"] = json.dumps(admitted)
    if prefix + "exec-ready" in result["records"]:
        record = json.loads(result["records"][prefix + "exec-ready"])
        chain_value = source_inputs.chain_start(bytes.fromhex(digest))
        record["consumed_digest"] = source_inputs.consumed_digest(bytes.fromhex(digest), 0, chain_value).hex()
        record["admitted_digest"] = admitted["digest"]
        record["digest"] = admission.exec_ready_digest(admitted["digest"], record["consumed_digest"], record["exec_digest"])
        result["records"][prefix + "exec-ready"] = json.dumps(record)


def admitted_key(result, sequence):
    return [key for key in result["records"] if key.endswith(f"{sequence:016x}/admitted")][0]


class OracleTests(unittest.TestCase):
    def failures(self, observation):
        return admission_geth.checked_compare(observation, ORACLE, COMMIT)

    def assertFails(self, observation, text):
        failures = self.failures(observation)
        self.assertTrue(any(text in failure for failure in failures), failures)

    def test_synthetic_observation_passes(self):
        self.assertEqual(self.failures(fresh()), [])

    def test_wrong_chain_is_refused(self):
        observation = fresh()
        observation["generated_hashes"][0] = ZERO
        self.assertFails(observation, "does not describe the oracle chain")
        observation = fresh()
        observation["generated_headers"][5] = observation["generated_headers"][6]
        self.assertFails(observation, "generated header 6")

    def test_scenario_and_attempt_sets(self):
        observation = fresh()
        observation["scenarios"].pop()
        self.assertFails(observation, "scenarios")
        observation = fresh()
        scenario(observation, "reject_live")[0]["name"] = "other"
        self.assertFails(observation, "reject_live: attempts differ")

    def test_admitted_digest_and_candidate(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        key = admitted_key(result, 0)
        record = json.loads(result["records"][key])
        record["digest"] = "1" * 64
        result["records"][key] = json.dumps(record)
        self.assertFails(observation, "ADMITTED digest differs")
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        key = [key for key in result["records"] if key.endswith("0000000000000000/preparing")][0]
        record = json.loads(result["records"][key])
        record["candidate"]["parent_root"] = "2" * 64
        result["records"][key] = json.dumps(record)
        self.assertFails(observation, "INPUT_PREPARING record differs")

    def test_state_records(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        del result["records"][[key for key in result["records"] if key.endswith("0000000000000001/exec-ready")][0]]
        self.assertFails(observation, "want ['admitted', 'exec-ready', 'preparing'] for EXEC_READY")
        observation = fresh()
        result = scenario(observation, "admit_before_sync")[1]["result"]
        key = [key for key in result["records"] if key.endswith("0000000000000000/abandoned")][0]
        result["records"][key.replace("abandoned", "exec-ready")] = result["records"].pop(key)
        self.assertFails(observation, "for ABANDONED")

    def test_exec_ready_binding(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        key = [key for key in result["records"] if key.endswith("0000000000000000/exec-ready")][0]
        record = json.loads(result["records"][key])
        record["consumed_digest"] = "3" * 64
        result["records"][key] = json.dumps(record)
        self.assertFails(observation, "EXEC_READY does not bind")

    def test_rejected_binding(self):
        observation = fresh()
        result = scenario(observation, "reject_live")[0]["result"]
        key = [key for key in result["records"] if key.endswith("/rejected")][0]
        record = json.loads(result["records"][key])
        record["digest"] = "4" * 64
        result["records"][key] = json.dumps(record)
        self.assertFails(observation, "NATIVE_REJECTED does not bind")

    def test_pins(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        result["pins"].pop(sorted(result["pins"])[0])
        self.assertFails(observation, "lost its pin while EXEC_READY")
        observation = fresh()
        result = scenario(observation, "reject_live")[0]["result"]
        result["pins"][ORACLE["pin_prefix"] + admission_geth.key_of(ORACLE, 0)] = "{}"
        self.assertFails(observation, "keeps its pin while NATIVE_REJECTED")
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        key = sorted(result["pins"])[1]
        pin = json.loads(result["pins"][key])
        pin["headers"][0]["number"] = 7
        result["pins"][key] = json.dumps(pin, separators=(",", ":"))
        self.assertFails(observation, "pin record differs")

    def test_capsule_layout_and_items(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        capsule = list(result["capsules"].values())[0]
        capsule["capsule"]["items"].pop(6)
        self.assertFails(observation, "capsule differs from the independent layout")
        hashes, headers = chain()
        other_header = headers[100]
        for name, data in (("parent-header", other_header), ("chain-config", (b'{"chainId": 31337}').hex()),
                           ("fork-id", b'{"hash":"00000000","next":"0"}'.hex()), ("native-commit", "61"),
                           ("normalization-profile", "61"), ("ancestor-header-100", other_header),
                           ("parent-root", "00" * 32), ("encodings", "7b7d"), ("header-rlp", other_header),
                           ("parent-hash", "00" * 32)):
            observation = fresh()
            result = scenario(observation, "clean")[0]["result"]
            row = [row for row in result["capsules"][admission_geth.key_of(ORACLE, 0)]["items"] if row["name"] == name][0]
            row["data"] = data
            reseal(result, 0)
            with self.subTest(item=name):
                self.assertFails(observation, f"capsule item {name} differs")
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        row = [row for row in result["capsules"][admission_geth.key_of(ORACLE, 0)]["items"]
               if row["name"] == "parent-header"][0]
        row["data"] = "00"
        reseal(result, 0)
        self.assertFails(observation, "capsule item parent-header does not decode")
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        capsule = list(result["capsules"].values())[0]
        capsule["items"][0]["data"] = "c0"
        self.assertFails(observation, "saved capsule bytes differ from the manifest")

    def test_chain_config_must_be_canonical_and_exact(self):
        for data in (json.dumps(ORACLE["chain_config"]).encode(),
                     admission.canonical(json.dumps(dict(ORACLE["chain_config"], terminalTotalDifficulty=1)).encode())):
            observation = fresh()
            result = scenario(observation, "clean")[0]["result"]
            row = [row for row in result["capsules"][admission_geth.key_of(ORACLE, 0)]["items"]
                   if row["name"] == "chain-config"][0]
            row["data"] = data.hex()
            reseal(result, 0)
            with self.subTest(data=data[:40]):
                self.assertFails(observation, "capsule item chain-config differs")

    def test_lookups_slots_and_safe_point(self):
        observation = fresh()
        first = scenario(observation, "clean")[0]
        [row for row in first["events"] if row.get("event") == "lookup"][0]["number"] = 1
        self.assertFails(observation, "BLOCKHASH lookups differ")
        observation = fresh()
        first = scenario(observation, "clean")[0]
        [row for row in first["events"] if row.get("event") == "lookup"][1]["hash"] = "5" * 64
        self.assertFails(observation, "BLOCKHASH lookups differ")
        observation = fresh()
        first = scenario(observation, "clean")[0]
        [row for row in first["events"] if row.get("event") == "lookup"][2]["found"] = False
        self.assertFails(observation, "BLOCKHASH lookups differ")
        observation = fresh()
        scenario(observation, "clean")[0]["result"]["reader_slots"][2] = observation["generated_hashes"][44]
        self.assertFails(observation, "expected BLOCKHASH answers")
        observation = fresh()
        scenario(observation, "clean")[0]["result"]["captured_inputs"].pop()
        self.assertFails(observation, "VM safe point")
        observation = fresh()
        scenario(observation, "clean")[0]["result"]["head_number"] = T
        self.assertFails(observation, "head 300, want block 301")

    def test_closing_sync_before_execution(self):
        observation = fresh()
        first = scenario(observation, "clean")[0]
        index = [i for i, row in enumerate(first["events"]) if row.get("op") == "sync"][0]
        del first["events"][index]
        self.assertFails(observation, "ADMITTED record was not synced")
        observation = fresh()
        first = scenario(observation, "reject_live")[0]
        index = [i for i, row in enumerate(first["events"]) if row.get("event") == "sealed"][0]
        del first["events"][index]
        self.assertFails(observation, "admission steps")

    def test_trace_order(self):
        observation = fresh()
        first = scenario(observation, "clean")[0]
        events = first["events"]
        a = [i for i, row in enumerate(events) if row.get("event") == "pinned"][0]
        b = [i for i, row in enumerate(events) if row.get("event") == "sealed"][0]
        events[a], events[b] = events[b], events[a]
        self.assertFails(observation, "clean-1: admission steps")

    def test_restart_and_shipped_binary_change_nothing(self):
        observation = fresh()
        result = scenario(observation, "clean")[1]["result"]
        result["pins"] = {}
        self.assertFails(observation, "changed the admission records or pins")
        observation = fresh()
        scenario(observation, "clean")[2]["result"]["run"] = "r2"
        self.assertFails(observation, "want LIVE in the saved run")
        observation = fresh()
        scenario(observation, "clean")[0]["result"]["run_record"] = {}
        self.assertFails(observation, "run record differs")

    def test_rewind_must_be_refused(self):
        observation = fresh()
        scenario(observation, "clean")[2]["result"]["rewind_error"] = "None"
        self.assertFails(observation, "rewind below the pinned headers was not refused")
        observation = fresh()
        scenario(observation, "clean")[2]["result"]["rewind_head"] = [T + 1, T - 2]
        self.assertFails(observation, "rewind below the pinned headers was not refused")

    def test_shipped_refusals(self):
        for field, value in (("exit_code", 0), ("timed_out", True), ("output_tail", "started")):
            observation = fresh()
            observation["shipped"][0][field] = value
            with self.subTest(field=field):
                self.assertFails(observation, "shipped/prune-state")
        observation = fresh()
        observation["shipped"].pop()
        self.assertFails(observation, "shipped: runs")

    def test_manual_database_guard(self):
        rows = lambda observation: {row["name"]: row for row in observation["shipped"]}
        # A refused command must leave every chaindata file as it was.
        for change in (lambda row: row["store_after"].pop(), lambda row: row.update(store_before=[], store_after=[]),
                       lambda row: row["store_after"].__setitem__(0, "000004.log 121 " + "ab" * 32),
                       lambda row: row.pop("store_after")):
            observation = fresh()
            change(rows(observation)["admitted-removedb"])
            self.assertFails(observation, "shipped/admitted-removedb: the refused command changed the chaindata files")
        # The open obligation without a pin needs its own refusal text.
        observation = fresh()
        rows(observation)["removedb-open-obligation"]["output_tail"] = "Fatal: " + ORACLE["messages"]["live_pins"]
        self.assertFails(observation, "shipped/removedb-open-obligation")
        # Without pins or open obligations, removedb deletes the key-value store natively.
        for field, value in (("exit_code", 1), ("timed_out", True), ("store_after", ["CURRENT 16 " + "cd" * 32]),
                             ("store_before", ["ancient/chain/headers.0000.cdat 9 " + "ef" * 32])):
            observation = fresh()
            rows(observation)["removedb-allowed"][field] = value
            with self.subTest(field=field):
                self.assertFails(observation, "shipped/removedb-allowed")
        observation = fresh()
        scenario(observation, "removedb_allowed")[0]["events"][-1]["point"] = "after_sync/counter"
        self.assertFails(observation, "removedb-allowed-1")
        # geth import into a bound store without pins stops at the capture-store refusal of ProcessBlock.
        observation = fresh()
        rows(observation)["import-bound"]["output_tail"] = "Fatal: " + ORACLE["messages"]["live_pins"]
        self.assertFails(observation, "shipped/import-bound")

    def test_crash_points(self):
        observation = fresh()
        scenario(observation, "admit_before_sync")[0]["events"][-1]["point"] = "after_sync/admitted"
        self.assertFails(observation, "was not killed at before_sync/admitted")
        observation = fresh()
        crash = scenario(observation, "begin_after_counter_sync")[0]
        crash["events"].insert(-1, trace("admitted"))
        self.assertFails(observation, "admitted before the crash")
        observation = fresh()
        retry = scenario(observation, "admit_before_sync")[1]
        retry["events"] = [row for row in retry["events"] if row.get("event") != "abandoned"]
        self.assertFails(observation, "admit-before-sync-2: admission steps")
        observation = fresh()
        scenario(observation, "begin_before_counter_sync")[1]["result"]["attach_error"] = "boom"
        self.assertFails(observation, "did not finish cleanly")

    def test_admitted_obligation_recovery(self):
        observation = fresh()
        held = scenario(observation, "admit_after_sync")[1]["result"]
        held["new_h_error"] = "None"
        self.assertFails(observation, "did not refuse a new H")
        observation = fresh()
        held = scenario(observation, "admit_after_sync")[1]["result"]
        held["go_live_error"] = "None"
        self.assertFails(observation, "did not refuse a new H")
        observation = fresh()
        scenario(observation, "admit_after_sync")[1]["result"]["captured_inputs"] = ["00"]
        self.assertFails(observation, "executed in RECOVERY_ONLY")
        observation = fresh()
        held = scenario(observation, "admit_after_sync")[1]["result"]
        key = admitted_key(held, 0)
        record = json.loads(held["records"][key])
        record["observation"] = "other"
        held["records"][key] = json.dumps(record)
        failures = self.failures(observation)
        self.assertTrue(any("did not complete the saved ADMITTED obligation" in failure for failure in failures), failures)
        observation = fresh()
        recover = scenario(observation, "admit_after_sync")[3]
        recover["events"] = [row for row in recover["events"] if row.get("event") != "resumed"]
        self.assertFails(observation, "admit-after-sync-4: admission steps")

    def test_changed_inputs_stop_recovery(self):
        observation = fresh()
        scenario(observation, "admit_after_sync")[2]["result"]["attach_error"] = None
        self.assertFails(observation, "resumed the obligation with changed static inputs")
        observation = fresh()
        scenario(observation, "admit_after_sync")[2]["events"].append(trace("exec_ready"))
        self.assertFails(observation, "resumed the obligation with changed static inputs")
        observation = fresh()
        scenario(observation, "admit_after_sync")[2]["result"]["pins"] = {}
        self.assertFails(observation, "refused recovery changed")

    def test_recovery_keeps_the_head_and_stores_the_block(self):
        observation = fresh()
        result = scenario(observation, "exec_ready_before_sync")[1]["result"]
        result["head_number"], result["head_hash"] = T, observation["generated_hashes"][T]
        self.assertFails(observation, "head 300, want block 299")
        observation = fresh()
        scenario(observation, "exec_ready_before_sync")[1]["result"]["stored"] = {"target": False}
        self.assertFails(observation, "did not store the saved block")

    def test_witness_execution(self):
        observation = fresh()
        scenario(observation, "clean")[2]["result"]["witness_error"] = "observation guard is not held"
        self.assertFails(observation, "witness execution")
        observation = fresh()
        del scenario(observation, "clean")[2]["result"]["witness_error"]
        self.assertFails(observation, "witness execution")

    def test_without_set_head(self):
        observation = fresh()
        scenario(observation, "without_set_head")[0]["result"]["imports"][1]["error"] = "x"
        self.assertFails(observation, "without-set-head-1: imports")
        observation = fresh()
        scenario(observation, "without_set_head")[0]["result"]["captured_inputs"] = []
        self.assertFails(observation, "without-set-head-1: the VM safe point")

    def test_exec_ready_recovery_digest(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        key = [key for key in result["records"] if key.endswith("0000000000000000/exec-ready")][0]
        record = json.loads(result["records"][key])
        record["exec_digest"] = "6" * 64
        record["digest"] = admission.exec_ready_digest(record["admitted_digest"], record["consumed_digest"], "6" * 64)
        result["records"][key] = json.dumps(record)
        self.assertFails(observation, "recovered execution digest differs")

    def test_rejection(self):
        observation = fresh()
        scenario(observation, "reject_live")[0]["result"]["imports"][0]["error"] = None
        self.assertFails(observation, "reject-live-1: imports")
        observation = fresh()
        recover = scenario(observation, "reject_after_admit")[1]
        [row for row in recover["events"] if row.get("event") == "recovered"][0]["state"] = "EXEC_READY"
        self.assertFails(observation, "recovery ended the saved invalid block")

    def test_freezer(self):
        for field, value in (("frozen", 251),
                             ("side_header_kept", False), ("canonical_fork_kept", False)):
            observation = fresh()
            scenario(observation, "freezer")[0]["result"][field] = value
            with self.subTest(field=field):
                self.assertFails(observation, "freezer-1")

    def test_counter_and_sequences(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        key = [key for key in result["records"] if key.endswith("/counter")][0]
        result["records"][key] = json.dumps({"next": 3})
        self.assertFails(observation, "counter 3")
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        result["records"][key.replace("counter", "0000000000000005/preparing")] = json.dumps({"observation": "x"})
        self.assertFails(observation, "sequences [0, 1, 5]")

    def test_extra_state_record(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        key = [key for key in result["records"] if key.endswith("0000000000000000/exec-ready")][0]
        result["records"][key.replace("exec-ready", "rejected")] = result["records"][key]
        self.assertFails(observation, "for EXEC_READY")

    def test_admitted_record_names_the_sealed_capsule(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        key = admitted_key(result, 0)
        record = json.loads(result["records"][key])
        record["capsule_digest"] = "7" * 64
        result["records"][key] = json.dumps(record)
        self.assertFails(observation, "ADMITTED digest differs")

    def test_capsule_layout_must_match_the_block(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        row = result["capsules"][admission_geth.key_of(ORACLE, 1)]
        row["capsule"]["items"].insert(6, dict(row["capsule"]["items"][6], name="ancestor-header-44"))
        row["items"].insert(6, dict(row["items"][6], name="ancestor-header-44"))
        reseal(result, 1)
        self.assertFails(observation, "capsule differs from the independent layout")

    def test_pin_record_digest_and_headers(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        key = ORACLE["pin_prefix"] + admission_geth.key_of(ORACLE, 0)
        result["pins"][key] = result["pins"][key].replace('"codes":[]', '"codes": []')
        self.assertFails(observation, "pin record differs")
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        pin = json.loads(result["pins"][key])
        pin["headers"][0]["number"] = 7
        result["pins"][key] = json.dumps(pin, separators=(",", ":"))
        reseal(result, 0)
        self.assertFails(observation, "pin record differs")
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        result["pins"][ORACLE["pin_prefix"] + "other"] = "{}"
        self.assertFails(observation, "live pins")

    def test_exec_ready_record_digest(self):
        observation = fresh()
        result = scenario(observation, "clean")[0]["result"]
        key = [key for key in result["records"] if key.endswith("0000000000000001/exec-ready")][0]
        record = json.loads(result["records"][key])
        record["digest"] = "8" * 64
        result["records"][key] = json.dumps(record)
        self.assertFails(observation, "EXEC_READY does not bind")

    def test_lookup_order_and_head_hash(self):
        observation = fresh()
        first = scenario(observation, "clean")[0]
        rows = [i for i, row in enumerate(first["events"]) if row.get("event") == "lookup"]
        first["events"][rows[0]], first["events"][rows[1]] = first["events"][rows[1]], first["events"][rows[0]]
        self.assertFails(observation, "BLOCKHASH lookups differ")
        observation = fresh()
        scenario(observation, "clean")[0]["result"]["head_hash"] = ZERO
        self.assertFails(observation, "head 301, want block 301")

    def test_seal_before_the_admitted_record(self):
        observation = fresh()
        events = scenario(observation, "clean")[0]["events"]
        sealed = [i for i, row in enumerate(events) if row.get("event") == "sealed"][0]
        row = events.pop(sealed)
        put = [i for i, row in enumerate(events) if str(row.get("key", "")).endswith("/admitted")][0]
        events.insert(put + 1, row)
        self.assertFails(observation, "ADMITTED record was not synced after the seal")

    def test_generated_chain_links(self):
        observation = fresh()
        header = bytearray(bytes.fromhex(observation["generated_headers"][0]))
        header[3] ^= 1
        observation["generated_headers"][0] = header.hex()
        observation["generated_hashes"][1] = "0x" + keccak256(bytes(header)).hex()
        self.assertFails(observation, "generated header 1")

    def test_extra_scenario(self):
        observation = fresh()
        observation["scenarios"].append({"name": "extra", "attempts": []})
        self.assertFails(observation, "scenarios")

    def events(self, observation, name, index):
        return scenario(observation, name)[index]["events"]

    def edit_event(self, observation, name, index, event, **fields):
        for row in self.events(observation, name, index):
            if row.get("event") == event:
                row.update(fields)
                return
        raise AssertionError(f"no {event} event")

    def test_decision_intent_must_be_the_independent_intent(self):
        observation = fresh()
        row = [r for r in self.events(observation, "decision_clean", 0) if r.get("event") == "intent"][0]
        marker = json.loads(row["marker"])
        marker["members"][0]["child_root"] = "5" * 64
        row["marker"] = json.dumps(marker, separators=(",", ":"))
        self.assertFails(observation, "intent 0 differs from the independent IMPORT intent")
        observation = fresh()
        self.edit_event(observation, "decision_clean", 0, "intent", kind="SIDE")
        self.assertFails(observation, "differs from the independent")

    def test_decision_delivery_and_digest(self):
        observation = fresh()
        self.edit_event(observation, "decision_clean", 0, "decided", delivery="CHECKPOINT")
        self.assertFails(observation, "intent 0 was not decided as BATCH")
        observation = fresh()
        self.edit_event(observation, "decision_child_root", 1, "decided", digest="6" * 64)
        self.assertFails(observation, "intent 0 was not decided as CHECKPOINT")

    def test_decision_end_state(self):
        observation = fresh()
        scenario(observation, "decision_clean")[1]["result"]["markers"] = {"x": "{}"}
        self.assertFails(observation, "decision-clean-2: markers")
        observation = fresh()
        scenario(observation, "decision_intent")[1]["result"]["inputs_left"] = {"geth-probe.r1.0000000000000000": 3}
        self.assertFails(observation, "inputs of closed observations are left")
        observation = fresh()
        scenario(observation, "decision_exec_ready")[1]["result"]["pins"] = {
            ORACLE["pin_prefix"] + admission_geth.key_of(ORACLE, 0): "{}"}
        self.assertFails(observation, "live pins")
        observation = fresh()
        scenario(observation, "decision_marker")[1]["result"]["decision_records"] = {}
        self.assertFails(observation, "decision counter 0, want 2")
        observation = fresh()
        scenario(observation, "decision_clean")[1]["result"]["decision_records"][
            f"extractor-decision-v1/{PRODUCER}/purged"] = '{"sequence":1}'
        self.assertFails(observation, "were not cleaned up to the counter")

    def test_known_block_opens_no_observation(self):
        observation = fresh()
        self.events(observation, "decision_clean", 1).append(trace("begin"))
        self.assertFails(observation, "decision-clean-2: admission steps")
        observation = fresh()
        scenario(observation, "decision_clean")[1]["result"]["records"] = {
            f"extractor-admission-v1/{PRODUCER}/counter": json.dumps({"next": 3})}
        self.assertFails(observation, "admission counter 3, want 2")
        observation = fresh()
        scenario(observation, "decision_mutation")[1]["result"]["imports"][0]["error"] = None
        self.assertFails(observation, "decision-mutation-2: imports")

    def test_child_root_replay(self):
        observation = fresh()
        scenario(observation, "decision_child_root")[1]["result"]["roots_at_start"]["target"] = True
        self.assertFails(observation, "was durable before the replay")
        observation = fresh()
        result = scenario(observation, "decision_child_root")[1]["result"]
        key = [key for key in result["records_at_start"] if key.endswith("/exec-ready")][0]
        record = json.loads(result["records_at_start"][key])
        record["consumed_digest"] = "7" * 64
        result["records_at_start"][key] = json.dumps(record)
        self.assertFails(observation, "did not find the durable EXEC_READY record")
        observation = fresh()
        scenario(observation, "decision_child_root")[1]["result"]["captured_inputs"] = [f"{T:064x}", f"{T + 1:064x}"]
        self.assertFails(observation, "decision-child-root-2: the VM safe point")

    def test_completed_intent(self):
        observation = fresh()
        self.edit_event(observation, "decision_intent", 1, "settling", evidence="marker")
        self.assertFails(observation, "was settled from ['marker'], want reimport")
        observation = fresh()
        row = [r for r in self.events(observation, "decision_clean", 0) if r.get("event") == "intent"][0]
        row["marker"] = row["marker"].replace('"events":true', '"events":false')
        self.assertFails(observation, "differs from the intent of the clean decision import")
        observation = fresh()
        row = [r for r in self.events(observation, "decision_intent", 1) if r.get("event") == "intent"][0]
        row["marker"] = row["marker"].replace('"events":true', '"events":false')
        self.assertFails(observation, "decision-intent-2: the completed intent differs from the intent of the clean")
        observation = fresh()
        scenario(observation, "decision_mutation")[1]["result"]["head_number"] = T
        self.assertFails(observation, "decision-mutation-2: head")

    def test_recovered_rejection(self):
        observation = fresh()
        self.edit_event(observation, "decision_rejected", 1, "intent_rejected", digest="8" * 64)
        self.assertFails(observation, "was not rejected once with the native refusal evidence")
        observation = fresh()
        self.edit_event(observation, "decision_reject_live", 0, "intent_rejected", digest="8" * 64)
        self.assertFails(observation, "the recovered rejection differs from the live rejection")
        self.assertFails(observation, "decision-reject-live-1: intent 0 was not rejected with the native refusal")
        observation = fresh()
        self.events(observation, "decision_rejected", 1).append({"op": "mirror", "sequence": 0})
        self.assertFails(observation, "the mirror was asked about sequences [0]")
        observation = fresh()
        scenario(observation, "decision_rejected")[1]["result"]["bad_blocks"]["target"] = False
        self.assertFails(observation, "not recorded natively as a bad block")
        observation = fresh()
        scenario(observation, "decision_reject_live")[0]["result"]["stored"]["target"] = True
        self.assertFails(observation, "stored blocks")
        observation = fresh()
        scenario(observation, "decision_rejected")[1]["result"]["markers"] = {
            decision.marker_key(ORACLE["producer_id"], ORACLE["run_id"], 0): "{}"}
        self.assertFails(observation, "decision-rejected-2: markers")

    def test_canonical_crash_inside_reorg(self):
        observation = fresh()
        scenario(observation, "decision_canonical_intermediate")[0]["events"].insert(
            -1, trace("decided", sequence=1, delivery="BATCH", digest="0" * 64))
        self.assertFails(observation, "the CANONICAL decision was recorded before the crash")
        observation = fresh()
        self.edit_event(observation, "decision_canonical_intermediate", 1, "settling", evidence="marker")
        self.assertFails(observation, "intent 1 was settled from ['marker'], want stored_block")
        observation = fresh()
        row = [r for r in self.events(observation, "decision_canonical_mutated", 0) if r.get("event") == "intent"][1]
        marker = json.loads(row["marker"])
        marker["members"] = marker["members"][:1]
        row["marker"] = json.dumps(marker, separators=(",", ":"))
        self.assertFails(observation, "the intents differ from the independent SIDE and CANONICAL intents")
        observation = fresh()
        scenario(observation, "decision_canonical_intent")[1]["result"]["head_number"] = T
        self.assertFails(observation, "decision-canonical-intent-2: head")
        observation = fresh()
        scenario(observation, "decision_canonical_intermediate")[1]["result"]["head_at_start"] = ZERO
        self.assertFails(observation, "the native head at the restart is not block 300")

    def test_decision_exec_ready_recovery(self):
        observation = fresh()
        self.edit_event(observation, "decision_exec_ready", 1, "exec_ready", exec="9" * 64)
        self.assertFails(observation, "differs from the clean decision import")
        observation = fresh()
        self.events(observation, "decision_exec_ready", 1)[:] = [
            row for row in self.events(observation, "decision_exec_ready", 1) if row.get("event") != "deferred"]
        self.assertFails(observation, "decision-exec-ready-2: admission steps")

    def test_legacy_store_is_refused(self):
        observation = fresh()
        row = [r for r in observation["shipped"] if r["name"] == "decision-legacy-store"][0]
        row["exit_code"] = 0
        self.assertFails(observation, "shipped/decision-legacy-store")

    def test_malformed_observation_is_a_failure(self):
        observation = fresh()
        scenario(observation, "clean")[0]["result"]["records"] = {"x/y/0000000000000000/preparing": "not json"}
        failures = self.failures(observation)
        self.assertTrue(failures and failures[0].startswith("malformed observation"), failures)


class DriverTests(unittest.TestCase):
    def test_patches_apply_once_and_install_files(self):
        table = json.loads((admission_geth.NATIVE / "patches.json").read_text())
        with tempfile.TemporaryDirectory() as directory:
            tree = Path(directory)
            originals = {}
            for patch in table["patches"]:
                originals.setdefault(patch["file"], "")
                originals[patch["file"]] += "// filler\n" + patch["original"]
            for name, text in originals.items():
                (tree / name).parent.mkdir(parents=True, exist_ok=True)
                (tree / name).write_text(text)
            record = admission_geth.apply_patches(tree, admission_geth.NATIVE)
            self.assertEqual([row["name"] for row in record["patches"]], [patch["name"] for patch in table["patches"]])
            for patch in table["patches"]:
                self.assertIn(patch["replacement"], (tree / patch["file"]).read_text())
            for name, relative in table["files"].items():
                self.assertEqual((tree / relative).read_bytes(), (admission_geth.NATIVE / name).read_bytes())
            with self.assertRaises(Exception):
                admission_geth.apply_patches(tree, admission_geth.NATIVE)

    def test_decision_patches_apply_after_the_admission_patches(self):
        admission_table = json.loads((admission_geth.NATIVE / "patches.json").read_text())
        table = json.loads((admission_geth.DECISION / "patches.json").read_text())
        self.assertEqual(table["source_commit"], admission_table["source_commit"])
        with tempfile.TemporaryDirectory() as directory:
            tree = Path(directory)
            texts = {}
            for patch in admission_table["patches"] + table["patches"]:
                if patch in admission_table["patches"] or \
                        not any(patch["original"] in other["replacement"] for other in admission_table["patches"]):
                    texts.setdefault(patch["file"], "")
                    texts[patch["file"]] += "// filler\n" + patch["original"]
            for name, text in texts.items():
                (tree / name).parent.mkdir(parents=True, exist_ok=True)
                (tree / name).write_text(text)
            admission_geth.apply_patches(tree, admission_geth.NATIVE)
            record = admission_geth.apply_patches(tree, admission_geth.DECISION)
            self.assertEqual([row["name"] for row in record["patches"]], [patch["name"] for patch in table["patches"]])
            for patch in table["patches"]:
                self.assertIn(patch["replacement"], (tree / patch["file"]).read_text())
            for name, relative in table["files"].items():
                self.assertEqual((tree / relative).read_bytes(), (admission_geth.DECISION / name).read_bytes())
        names = [patch["name"] for patch in admission_table["patches"] + table["patches"]]
        self.assertEqual(len(names), len(set(names)))
        for patch in table["patches"]:
            self.assertNotEqual(patch["original"], patch["replacement"])
            self.assertTrue(patch["reason"])

    def test_patch_original_must_occur_exactly_once(self):
        table = json.loads((admission_geth.NATIVE / "patches.json").read_text())
        with tempfile.TemporaryDirectory() as directory:
            tree = Path(directory)
            for patch in table["patches"]:
                path = tree / patch["file"]
                path.parent.mkdir(parents=True, exist_ok=True)
                previous = path.read_text() if path.exists() else ""
                path.write_text(previous + patch["original"] + patch["original"])
            with self.assertRaises(Exception):
                admission_geth.apply_patches(tree, admission_geth.NATIVE)

    def test_patch_set_names_the_accepted_commit_and_unique_originals(self):
        table = json.loads((admission_geth.NATIVE / "patches.json").read_text())
        self.assertEqual(table["source_commit"], COMMIT)
        names = [patch["name"] for patch in table["patches"]]
        self.assertEqual(len(names), len(set(names)))
        for patch in table["patches"]:
            self.assertNotEqual(patch["original"], patch["replacement"])
            self.assertTrue(patch["reason"])

    def test_mutants_target_the_patched_text(self):
        table = json.loads((HERE / "native_mutants.json").read_text())
        patches = json.loads((admission_geth.NATIVE / "patches.json").read_text())
        decisions = json.loads((admission_geth.DECISION / "patches.json").read_text())
        targets = admission_geth.mutant_targets(table, patches, decisions)
        self.assertEqual(sorted(targets), sorted(mutant["name"] for mutant in table["mutants"]))
        # A decision mutant needs the decision patch set or its own file.
        with self.assertRaises(Exception):
            admission_geth.mutant_targets({"mutants": [m for m in table["mutants"] if m["name"] == "head_marker_omitted"]},
                                          patches)
        broken = copy.deepcopy(table)
        broken["mutants"][0]["original"] = "no such text"
        with self.assertRaises(Exception):
            admission_geth.mutant_targets(broken, patches, decisions)
        broken = copy.deepcopy(table)
        broken["mutants"][0]["binaries"] = ["other"]
        with self.assertRaises(Exception):
            admission_geth.mutant_targets(broken, patches, decisions)

    def test_apply_mutant_restores(self):
        with tempfile.TemporaryDirectory() as directory:
            tree = Path(directory)
            path = tree / "extractor/admission/native/geth.go"
            path.parent.mkdir(parents=True)
            path.write_text("a\nb\n")
            restore = admission_geth.apply_mutant(tree, {"name": "m", "file": "geth.go", "original": "b\n",
                                                         "replacement": "c\n"})
            self.assertEqual(path.read_text(), "a\nc\n")
            restore()
            self.assertEqual(path.read_text(), "a\nb\n")
            for name, relative in (("decision.go", "extractor/admission/native/decision.go"),
                                   ("core_decision.go.txt", "core/extractor_decision.go")):
                target = tree / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text("x\n")
                restore = admission_geth.apply_mutant(tree, {"name": "m", "file": name, "original": "x\n",
                                                             "replacement": "y\n"})
                self.assertEqual(target.read_text(), "y\n")
                restore()
                self.assertEqual(target.read_text(), "x\n")
            with self.assertRaises(Exception):
                admission_geth.apply_mutant(tree, {"name": "m", "file": "geth.go", "original": "z", "replacement": ""})

    def test_judge_command(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "probe.json"
            path.write_text(json.dumps(synthetic_observation()))
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(admission_geth.main(["judge", str(path), "--commit", COMMIT]), 0)
            self.assertEqual(json.loads(output.getvalue()), {"failures": []})
            broken = fresh()
            broken["shipped"] = []
            path.write_text(json.dumps(broken))
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(admission_geth.main(["judge", str(path), "--commit", COMMIT]), 1)
            self.assertIn("shipped: runs", output.getvalue())


if __name__ == "__main__":
    unittest.main()
