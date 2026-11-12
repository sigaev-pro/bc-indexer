#!/usr/bin/env python3
"""Build and run the E01-02.admission Geth probe on the product import path.

`prepare` extracts the accepted Geth source, applies the product patch set of
`adapters/geth/admission/native/patches.json`, installs the shared packages
`admission`, `source_inputs` and `state_provider` and the Geth wiring under
`extractor/`, and compiles them offline with the accepted compiler: the unit
tests, the patched `geth` command and the probe (with `core/capture` in a Go
workspace). It also builds the binaries of every native mutant. `run` executes
the probe and every mutant in the pinned runtime image and judges their
observations with the oracle in this file and `expected.json`. The probe must
pass; every mutant must fail. Preparation is not part of the measured run.
Private local evidence gives no AC verdict and no public-profile credit.
"""

import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import tarfile
import time
import uuid
import zlib

FIXTURE = Path(__file__).resolve().parent
HARNESS = FIXTURE.parents[1]
ROOT = HARNESS.parents[1]
sys.path.insert(0, str(HARNESS))
from artifacts import artifact, check_deadline, copy_artifact, json_artifact, load, pinned_json, read_bytes, reference, require  # noqa: E402
import admission  # noqa: E402
import capture_probe  # noqa: E402
import decision  # noqa: E402
from capture_probe import Interrupted, block_stops, single_writer, unblock_stops, write_until_done  # noqa: E402
from native import Native, new_output  # noqa: E402
import source_inputs  # noqa: E402
from source_inputs_geth import STATE_ROOT, header_fields, keccak256  # noqa: E402

NATIVE = ROOT / "adapters" / "geth" / "admission" / "native"
FIXTURE_FILES = ("probe.go", "genesis.json", "expected.json", "ORACLE.md", "native_mutants.json")
NATIVE_FILES = ("patches.json", "core_hook.go.txt", "rawdb_hook.go.txt", "utils_hook.go.txt", "geth.go", "geth_test.go",
                "README.md")
PACKAGES = {
    "extractor/admission": (ROOT / "adapters" / "geth" / "admission",
                            ("admission.go", "admission_test.go", "canonical.go", "canonical_test.go", "vectors.json")),
    "extractor/source_inputs": (ROOT / "adapters" / "geth" / "source_inputs", ("inputs.go", "inputs_test.go")),
    "extractor/state_provider": (ROOT / "adapters" / "geth" / "state_provider", ("provider.go", "family.go")),
    "extractor/admission/native": (NATIVE, ("geth.go", "geth_test.go")),
}
CAPTURE_FILES = ("go.mod", "capture.go")
HARNESS_FILES = ("source_inputs_geth.py", "source_inputs.py", "admission.py", "capture_probe.py", "native.py",
                 "owned.py", "artifacts.py")
PROBE_PATH = "cmd/extractor-admission-probe/probe.go"
UNIT_PACKAGES = ("./extractor/admission/...", "./extractor/source_inputs")
PREPARATION_SECONDS = 5400
RUN_SECONDS = 1800
BINARY_LIMIT = 512 * 1024 * 1024
OBSERVATION_LIMIT = 64 * 1024 * 1024


# ---------------------------------------------------------------- oracle


class Checker:
    """Collects failures with a label."""

    def __init__(self):
        self.failures = []

    def fail(self, label, message):
        self.failures.append(f"{label}: {message}")


def records(result):
    """Admission records of an attempt by sequence: {seq: {kind: record}}, plus the counter."""
    out, counter = {}, None
    for key, value in (result.get("records") or {}).items():
        parts = key.split("/")
        if len(parts) == 3 and parts[2] == "counter":
            counter = json.loads(value)["next"]
        elif len(parts) == 4 and re.fullmatch(r"[0-9a-f]{16}", parts[2]):
            out.setdefault(int(parts[2], 16), {})[parts[3]] = json.loads(value)
    return out, counter


def candidate(oracle, observation, number, bad=False):
    """The candidate of block number: its hash, parent hash and the parent state root."""
    hashes, headers = observation["generated_hashes"], observation["generated_headers"]
    parent = bytes.fromhex(headers[number - 2])
    return {"number": number, "hash": (observation["bad_hash"] if bad else hashes[number])[2:],
            "parent_hash": hashes[number - 1][2:], "parent_root": header_fields(parent)[STATE_ROOT].hex()}


def capsule_digest(refs):
    data = source_inputs.tag("extractor-source-inputs-capsule-v1") + source_inputs.u64(len(refs))
    for ref in refs:
        name = ref["name"].encode()
        data += source_inputs.u64(len(name)) + name + source_inputs.u64(ref["length"]) + bytes.fromhex(ref["sha256"])
    return hashlib.sha256(data).hexdigest()


def item_names(oracle, number):
    """Capsule item names of block number: the parent and its window ancestors are static items."""
    start = max(0, number - 256)
    return oracle["capsule_head"] + [f"ancestor-header-{n}" for n in range(start, number - 1)] + oracle["capsule_tail"]


def key_of(oracle, sequence):
    return admission.observation_key(oracle["producer_id"], oracle["run_id"], sequence)


def check_observation(c, label, oracle, observation, result, sequence, number, state, bad=False):
    """Check the records, pin and capsule of one observation against independent values.
    state is EXEC_READY, NATIVE_REJECTED, ADMITTED, INPUT_PREPARING or ABANDONED. Return its records."""
    rows = records(result)[0].get(sequence, {})
    key = key_of(oracle, sequence)
    want = candidate(oracle, observation, number, bad)
    prep = rows.get("preparing")
    if prep != {"observation": key, "sequence": sequence, "candidate": want}:
        c.fail(label, f"{key} INPUT_PREPARING record differs from the candidate of block {number}")
        return rows
    kinds = {"EXEC_READY": {"preparing", "admitted", "exec-ready"}, "NATIVE_REJECTED": {"preparing", "admitted", "rejected"},
             "ADMITTED": {"preparing", "admitted"}, "INPUT_PREPARING": {"preparing"},
             "ABANDONED": {"preparing", "abandoned"}}[state]
    if set(rows) != kinds:
        c.fail(label, f"{key} has records {sorted(rows)}, want {sorted(kinds)} for {state}")
        return rows
    capsule = ((result.get("capsules") or {}).get(key) or {}).get("capsule")
    if "admitted" in rows:
        record = rows["admitted"]
        if not capsule or capsule.get("digest") != capsule_digest(capsule.get("items") or []) or \
                [ref.get("name") for ref in capsule["items"]] != item_names(oracle, number):
            c.fail(label, f"{key} capsule differs from the independent layout or digest")
            return rows
        expected = admission.admitted_digest(key, sequence, want, record.get("pin_digest", "0" * 64), capsule["digest"])
        if record.get("candidate") != want or record.get("capsule_digest") != capsule["digest"] or \
                record.get("digest") != expected or record.get("observation") != key:
            c.fail(label, f"{key} ADMITTED digest differs from the independent formula")
    pin = (result.get("pins") or {}).get(oracle["pin_prefix"] + key)
    if state in ("EXEC_READY", "ADMITTED"):
        if pin is None:
            c.fail(label, f"{key} lost its pin while {state}")
        else:
            parsed = json.loads(pin)
            if hashlib.sha256(pin.encode()).hexdigest() != rows["admitted"].get("pin_digest") or \
                    parsed.get("parent_root") != "0x" + want["parent_root"] or \
                    parsed.get("headers") != [{"hash": "0x" + want["parent_hash"], "number": number - 1}] or \
                    parsed.get("store", {}).get("store_id") != oracle["store_id"]:
                c.fail(label, f"{key} pin record differs from the parent of block {number}")
    elif pin is not None:
        c.fail(label, f"{key} keeps its pin while {state}")
    if state == "EXEC_READY":
        record = rows["exec-ready"]
        chain = source_inputs.chain_start(bytes.fromhex(capsule["digest"]))
        consumed = source_inputs.consumed_digest(bytes.fromhex(capsule["digest"]), 0, chain).hex()
        admitted = rows["admitted"]["digest"]
        if record.get("consumed_digest") != consumed or record.get("admitted_digest") != admitted or \
                record.get("digest") != admission.exec_ready_digest(admitted, consumed, record.get("exec_digest", "0" * 64)):
            c.fail(label, f"{key} EXEC_READY does not bind the admitted digest and the closed consumed manifest")
    if state == "NATIVE_REJECTED":
        record = rows["rejected"]
        admitted = rows["admitted"]["digest"]
        if record.get("admitted_digest") != admitted or \
                record.get("digest") != admission.rejected_digest(admitted, record.get("evidence_digest", "0" * 64)):
            c.fail(label, f"{key} NATIVE_REJECTED does not bind the admitted digest")
    return rows


def check_sequences(c, label, result, sequences, counter):
    found, next_sequence = records(result)
    if sorted(found) != sorted(sequences) or next_sequence != counter:
        c.fail(label, f"sequences {sorted(found)} counter {next_sequence}, want {sorted(sequences)} counter {counter}")


def check_pins(c, label, oracle, result, sequences):
    want = sorted(oracle["pin_prefix"] + key_of(oracle, sequence) for sequence in sequences)
    if sorted(result.get("pins") or {}) != want:
        c.fail(label, f"live pins {sorted(result.get('pins') or {})}, want {want}")


def traces(attempt):
    return [row.get("event") for row in attempt.get("events") or [] if row.get("op") == "trace" and row.get("event") != "lookup"]


def check_trace(c, label, attempt, want):
    if traces(attempt) != want:
        c.fail(label, f"admission steps {traces(attempt)}, want {want}")


def check_lookups(c, label, oracle, observation, attempt, numbers):
    """Every BLOCKHASH lookup of each executed reader block is answered with the hash of the saved header,
    which equals the generated chain; the block below the window is never asked."""
    hashes = observation["generated_hashes"]
    want = [n - back for n in numbers for back in oracle["lookbacks"] if n - back >= max(0, n - 256)]
    rows = [row for row in attempt.get("events") or [] if row.get("event") == "lookup"]
    if [row.get("number") for row in rows] != want or \
            any(row.get("found") is not True or "0x" + str(row.get("hash")) != hashes[row["number"]] for row in rows):
        c.fail(label, "BLOCKHASH lookups differ from the saved window headers")


def check_head(c, label, oracle, observation, result, number):
    hashes = observation["generated_hashes"]
    if result.get("head_number") != number or result.get("head_hash") != hashes[number]:
        c.fail(label, f"head {result.get('head_number')}, want block {number}")
        return
    if number >= oracle["target"]:
        want = [hashes[number - back] if number - back >= number - 256 else "0x" + "0" * 64
                for back in oracle["lookbacks"]]
        if result.get("reader_slots") != want:
            c.fail(label, "native execution did not store the expected BLOCKHASH answers")


def check_captured(c, label, result, numbers):
    want = [f"{n:064x}" for n in numbers]
    if result.get("captured_inputs") != want:
        c.fail(label, "the VM safe point did not capture exactly one CALL input per executed reader block")


def check_closing_sync(c, label, attempt):
    """In every admission, the capsule is sealed and the ADMITTED record is synced before execution."""
    rows = attempt.get("events") or []
    for index, row in enumerate(rows):
        if row.get("op") == "put" and str(row.get("key", "")).endswith("/admitted"):
            later = rows[index + 1:]
            stop = next((i for i, r in enumerate(later) if r.get("event") in ("lookup", "admitted")), len(later))
            if not any(r.get("op") == "sync" for r in later[:stop]) or \
                    not any(r.get("event") == "sealed" for r in rows[:index]):
                c.fail(label, "the ADMITTED record was not synced after the seal and before execution")


def check_capsule_items(c, label, oracle, observation, result, sequence, number, commit):
    """Check every saved capsule item byte for byte against the generated chain and the oracle."""
    key = key_of(oracle, sequence)
    rows = ((result.get("capsules") or {}).get(key) or {}).get("items") or []
    refs = ((result.get("capsules") or {}).get(key) or {}).get("capsule", {}).get("items") or []
    if [row.get("name") for row in rows] != item_names(oracle, number) or len(refs) != len(rows):
        c.fail(label, f"{key} saved capsule items differ from the layout")
        return
    items = {row["name"]: bytes.fromhex(row["data"]) for row in rows}
    if any(hashlib.sha256(items[ref["name"]]).hexdigest() != ref["sha256"] or len(items[ref["name"]]) != ref["length"]
           for ref in refs):
        c.fail(label, f"{key} saved capsule bytes differ from the manifest")
        return
    headers, hashes = observation["generated_headers"], observation["generated_hashes"]
    config = items["chain-config"]
    try:
        canonical = admission.canonical(config)
    except ValueError:
        canonical = None
    try:
        parent_root = header_fields(items["parent-header"])[STATE_ROOT]
    except (IndexError, ValueError):
        c.fail(label, f"{key} capsule item parent-header does not decode")
        return
    checks = {
        "header-rlp": items["header-rlp"].hex() == headers[number - 1]
        and "0x" + keccak256(items["header-rlp"]).hex() == hashes[number],
        "parent-header": items["parent-header"].hex() == headers[number - 2],
        "parent-hash": "0x" + items["parent-hash"].hex() == hashes[number - 1],
        "parent-root": items["parent-root"] == parent_root,
        "encodings": items["encodings"] == oracle["encodings"].encode(),
        # A canonical config keeps every integer literal, so value equality also fixes terminalTotalDifficulty.
        "chain-config": canonical == config and json.loads(config) == oracle["chain_config"],
        "fork-id": items["fork-id"] == json.dumps(
            {"hash": f"{zlib.crc32(bytes.fromhex(hashes[0][2:])):08x}", "next": "0"}, separators=(",", ":")).encode(),
        "native-commit": items["native-commit"] == commit.encode(),
        "normalization-profile": items["normalization-profile"] == oracle["profile"].encode(),
    }
    for n in range(max(0, number - 256), number - 1):
        checks[f"ancestor-header-{n}"] = items[f"ancestor-header-{n}"].hex() == headers[n - 1] if n else \
            "0x" + keccak256(items["ancestor-header-0"]).hex() == hashes[0]
    for name, ok in checks.items():
        if not ok:
            c.fail(label, f"{key} capsule item {name} differs from the expected bytes")


def ended(c, label, attempt, point=None):
    """Check how an attempt ended: killed at a crash point, or a clean exit with a result."""
    events = attempt.get("events") or []
    if point:
        if attempt.get("signal") != "killed" or "result" in attempt or not events or \
                events[-1].get("op") != "crash" or events[-1].get("point") != point:
            c.fail(label, f"the attempt was not killed at {point}")
        return None
    result = attempt.get("result")
    if attempt.get("exit_code") != 0 or attempt.get("signal") or not isinstance(result, dict) or \
            result.get("attach_error") is not None:
        c.fail(label, "the attempt did not finish cleanly")
        return None
    return result


def imports(result):
    return [(row.get("name"), row.get("error")) for row in result.get("imports") or []]


def check_live_result(c, label, oracle, result, run=True):
    if result.get("mode") != "LIVE" or (run and result.get("run") != oracle["run_id"]):
        c.fail(label, f"mode {result.get('mode')} run {result.get('run')}, want LIVE in the saved run")


def scenario_clean(c, oracle, observation, attempts, commit):
    first, restart, rewind = attempts
    t = oracle["target"]
    result = ended(c, "clean-1", first)
    if result:
        check_live_result(c, "clean-1", oracle, result)
        if imports(result) != [("target", None), ("next", None)]:
            c.fail("clean-1", f"imports {imports(result)}")
        check_sequences(c, "clean-1", result, [0, 1], 2)
        check_observation(c, "clean-1", oracle, observation, result, 0, t, "EXEC_READY")
        check_observation(c, "clean-1", oracle, observation, result, 1, t + 1, "EXEC_READY")
        check_pins(c, "clean-1", oracle, result, [0, 1])
        check_capsule_items(c, "clean-1", oracle, observation, result, 0, t, commit)
        check_capsule_items(c, "clean-1", oracle, observation, result, 1, t + 1, commit)
        check_trace(c, "clean-1", first, ["opened", "live"] + ["begin", "pinned", "sealed", "admitted", "exec_ready"] * 2)
        check_lookups(c, "clean-1", oracle, observation, first, [t, t + 1])
        check_head(c, "clean-1", oracle, observation, result, t + 1)
        check_captured(c, "clean-1", result, [t, t + 1])
        check_closing_sync(c, "clean-1", first)
        if result.get("run_record") != {oracle["run_key"]: json.dumps(
                {"store_id": oracle["store_id"], "producer_id": oracle["producer_id"], "run_id": oracle["run_id"]},
                separators=(",", ":"))}:
            c.fail("clean-1", "the run record differs")
    for name, attempt in (("clean-2-restart", restart), ("clean-3-rewind", rewind)):
        later = ended(c, name, attempt)
        if not later or not result:
            continue
        check_live_result(c, name, oracle, later)
        check_trace(c, name, attempt, ["opened", "live"])
        if later.get("records") != result.get("records") or later.get("pins") != result.get("pins"):
            c.fail(name, "a restart or the shipped binary changed the admission records or pins")
        check_head(c, name, oracle, observation, later, t + 1)
    later = rewind.get("result") or {}
    if oracle["messages"]["live_pins"] not in str(later.get("rewind_error")) or later.get("rewind_head") != [t + 1, t + 1]:
        c.fail("clean-3-rewind", "a native rewind below the pinned headers was not refused")
    if "witness_error" not in later or later.get("witness_error") is not None:
        c.fail("clean-3-rewind", "a witness execution without a state write did not run natively without an observation")


def check_shipped(c, oracle, observation):
    rows = {row.get("name"): row for row in observation.get("shipped") or []}
    want = oracle["shipped"]
    if sorted(rows) != sorted(want):
        c.fail("shipped", f"runs {sorted(rows)}, want {sorted(want)}")
        return
    for name, message in want.items():
        row = rows[name]
        if message == "deleted":
            # Without a live pin or an open obligation, removedb deletes the key-value store natively: no file stays
            # directly in chaindata.
            before, after = row.get("store_before"), row.get("store_after")
            if row.get("exit_code") != 0 or row.get("timed_out") or not isinstance(before, list) or \
                    not isinstance(after, list) or not any("/" not in str(entry) for entry in before) or \
                    any("/" not in str(entry) for entry in after):
                c.fail(f"shipped/{name}", "removedb did not delete a store without pins or open obligations")
            continue
        if row.get("exit_code") in (0, None) or row.get("timed_out") or oracle["messages"][message] not in str(row.get("output_tail")):
            c.fail(f"shipped/{name}", f"the shipped geth binary did not refuse with {oracle['messages'][message]!r}")
    for name in oracle["store_unchanged"]:
        before = rows[name].get("store_before")
        if not isinstance(before, list) or not before or rows[name].get("store_after") != before:
            c.fail(f"shipped/{name}", "the refused command changed the chaindata files")


def scenario_crash_before_admission(name, point, abandoned, pinned):
    """Power loss before the closing ADMITTED sync (AC-CAP-02-01): nothing is accepted, the retry admits a new
    observation of the same block."""
    def check(c, oracle, observation, attempts, commit):
        crash, retry = attempts
        t = oracle["target"]
        ended(c, f"{name}-1", crash, point)
        # A crash inside Begin comes before its trace event; a crash in Admit comes after the pin and the seal.
        steps = ["opened", "live"] + (["begin", "pinned", "sealed"] if pinned else [])
        check_trace(c, f"{name}-1", crash, steps)
        if any(row.get("event") in ("admitted", "exec_ready") for row in crash.get("events") or []):
            c.fail(f"{name}-1", "an observation was admitted before the crash")
        result = ended(c, f"{name}-2", retry)
        if not result:
            return
        check_live_result(c, f"{name}-2", oracle, result)
        sequence = 1 if abandoned else 0
        check_trace(c, f"{name}-2", retry, ["opened"] + (["abandoned"] if abandoned else []) + ["live", "begin", "pinned",
                                                                                                "sealed", "admitted", "exec_ready"])
        check_sequences(c, f"{name}-2", result, [0, 1] if abandoned else [0], sequence + 1)
        if abandoned:
            check_observation(c, f"{name}-2", oracle, observation, result, 0, t, "ABANDONED")
        check_observation(c, f"{name}-2", oracle, observation, result, sequence, t, "EXEC_READY")
        check_pins(c, f"{name}-2", oracle, result, [sequence])
        check_head(c, f"{name}-2", oracle, observation, result, t)
        check_lookups(c, f"{name}-2", oracle, observation, retry, [t])
        check_captured(c, f"{name}-2", result, [t])
    return check


def scenario_admit_after_sync(c, oracle, observation, attempts, commit):
    """AC-CAP-02-02: power loss right after the closing ADMITTED sync. The same obligation is recovered from its pin
    and capsule; a new H and GoLive are refused until then."""
    crash, hold, changed, recover = attempts
    t = oracle["target"]
    ended(c, "admit-after-sync-1", crash, "after_sync/admitted")
    # A restart with another normalization profile must not resume the saved obligation: the static inputs it would
    # seal now differ from the capsule, so recovery stops and the obligation stays ADMITTED with its pin.
    refused = changed.get("result") or {}
    if changed.get("exit_code") != 0 or changed.get("signal") or \
            oracle["messages"]["recovery"] not in str(refused.get("attach_error")) or \
            any(row.get("event") in ("exec_ready", "rejected", "live") for row in changed.get("events") or []):
        c.fail("admit-after-sync-3", "recovery resumed the obligation with changed static inputs")
    elif hold.get("result") and (refused.get("records") != hold["result"].get("records") or
                                 refused.get("pins") != hold["result"].get("pins")):
        c.fail("admit-after-sync-3", "a refused recovery changed the admission records or pins")
    check_trace(c, "admit-after-sync-1", crash, ["opened", "live", "begin", "pinned", "sealed"])
    held = ended(c, "admit-after-sync-2", hold)
    if held:
        check_trace(c, "admit-after-sync-2", hold, ["opened"])
        if held.get("mode") != "RECOVERY_ONLY" or oracle["messages"]["mode"] not in str(held.get("new_h_error")) or \
                oracle["messages"]["pending"] not in str(held.get("go_live_error")):
            c.fail("admit-after-sync-2", "RECOVERY_ONLY did not refuse a new H and GoLive while ADMITTED")
        check_sequences(c, "admit-after-sync-2", held, [0], 1)
        check_observation(c, "admit-after-sync-2", oracle, observation, held, 0, t, "ADMITTED")
        check_pins(c, "admit-after-sync-2", oracle, held, [0])
        if held.get("head_number") != t - 1 or held.get("captured_inputs") != []:
            c.fail("admit-after-sync-2", "a block was executed in RECOVERY_ONLY")
    result = ended(c, "admit-after-sync-4", recover)
    if not result:
        return
    check_live_result(c, "admit-after-sync-4", oracle, result)
    check_trace(c, "admit-after-sync-4", recover, ["opened", "recovering", "resumed", "exec_ready", "recovered", "live",
                                                   "begin", "pinned", "sealed", "admitted", "exec_ready"])
    check_sequences(c, "admit-after-sync-4", result, [0, 1], 2)
    rows = check_observation(c, "admit-after-sync-4", oracle, observation, result, 0, t, "EXEC_READY")
    check_observation(c, "admit-after-sync-4", oracle, observation, result, 1, t + 1, "EXEC_READY")
    if held and rows.get("admitted") != records(held)[0].get(0, {}).get("admitted"):
        c.fail("admit-after-sync-4", "recovery did not complete the saved ADMITTED obligation")
    check_pins(c, "admit-after-sync-4", oracle, result, [0, 1])
    check_head(c, "admit-after-sync-4", oracle, observation, result, t + 1)
    check_lookups(c, "admit-after-sync-4", oracle, observation, recover, [t, t + 1])
    check_captured(c, "admit-after-sync-4", result, [t, t + 1])


def scenario_exec_ready_before_sync(c, oracle, observation, attempts, commit, clean_exec):
    crash, recover = attempts
    t = oracle["target"]
    ended(c, "exec-ready-before-sync-1", crash, "before_sync/exec-ready")
    check_trace(c, "exec-ready-before-sync-1", crash, ["opened", "live", "begin", "pinned", "sealed", "admitted"])
    result = ended(c, "exec-ready-before-sync-2", recover)
    if not result:
        return
    check_trace(c, "exec-ready-before-sync-2", recover, ["opened", "recovering", "resumed", "exec_ready", "recovered", "live"])
    check_sequences(c, "exec-ready-before-sync-2", result, [0], 1)
    rows = check_observation(c, "exec-ready-before-sync-2", oracle, observation, result, 0, t, "EXEC_READY")
    if clean_exec is None or rows.get("exec-ready", {}).get("exec_digest") != clean_exec:
        c.fail("exec-ready-before-sync-2", "the recovered execution digest differs from the clean import")
    # Recovery writes the block without moving the head (InsertBlockWithoutSetHead).
    check_head(c, "exec-ready-before-sync-2", oracle, observation, result, t - 1)
    if (result.get("stored") or {}).get("target") is not True:
        c.fail("exec-ready-before-sync-2", "recovery did not store the saved block with its state")


def scenario_reject_live(c, oracle, observation, attempts, commit):
    (attempt,) = attempts
    t = oracle["target"]
    result = ended(c, "reject-live-1", attempt)
    if not result:
        return
    rows = imports(result)
    if len(rows) != 2 or rows[0][0] != "bad" or oracle["control_errors"]["bad"] not in str(rows[0][1]) or rows[1] != ("target", None):
        c.fail("reject-live-1", f"imports {rows}")
    check_trace(c, "reject-live-1", attempt, ["opened", "live", "begin", "pinned", "sealed", "admitted", "rejected",
                                              "begin", "pinned", "sealed", "admitted", "exec_ready"])
    check_sequences(c, "reject-live-1", result, [0, 1], 2)
    check_observation(c, "reject-live-1", oracle, observation, result, 0, t, "NATIVE_REJECTED", bad=True)
    check_observation(c, "reject-live-1", oracle, observation, result, 1, t, "EXEC_READY")
    check_pins(c, "reject-live-1", oracle, result, [1])
    check_head(c, "reject-live-1", oracle, observation, result, t)
    check_closing_sync(c, "reject-live-1", attempt)


def scenario_reject_after_admit(c, oracle, observation, attempts, commit):
    crash, recover = attempts
    t = oracle["target"]
    ended(c, "reject-after-admit-1", crash, "after_sync/admitted")
    result = ended(c, "reject-after-admit-2", recover)
    if not result:
        return
    check_trace(c, "reject-after-admit-2", recover, ["opened", "recovering", "resumed", "rejected", "recovered", "live",
                                                     "begin", "pinned", "sealed", "admitted", "exec_ready"])
    states = [row.get("state") for row in recover.get("events") or [] if row.get("event") == "recovered"]
    if states != ["NATIVE_REJECTED"]:
        c.fail("reject-after-admit-2", f"recovery ended the saved invalid block as {states}")
    check_sequences(c, "reject-after-admit-2", result, [0, 1], 2)
    check_observation(c, "reject-after-admit-2", oracle, observation, result, 0, t, "NATIVE_REJECTED", bad=True)
    check_observation(c, "reject-after-admit-2", oracle, observation, result, 1, t, "EXEC_READY")
    check_pins(c, "reject-after-admit-2", oracle, result, [1])
    check_head(c, "reject-after-admit-2", oracle, observation, result, t)


def scenario_without_set_head(c, oracle, observation, attempts, commit):
    """The engine path: InsertBlockWithoutSetHead admits and executes the block without moving the head; SetCanonical
    then makes it canonical without a second execution."""
    (attempt,) = attempts
    t = oracle["target"]
    result = ended(c, "without-set-head-1", attempt)
    if not result:
        return
    if imports(result) != [("target-without-head", None), ("canonical-target", None)]:
        c.fail("without-set-head-1", f"imports {imports(result)}")
    check_trace(c, "without-set-head-1", attempt, ["opened", "live", "begin", "pinned", "sealed", "admitted", "exec_ready"])
    check_sequences(c, "without-set-head-1", result, [0], 1)
    check_observation(c, "without-set-head-1", oracle, observation, result, 0, t, "EXEC_READY")
    check_pins(c, "without-set-head-1", oracle, result, [0])
    check_head(c, "without-set-head-1", oracle, observation, result, t)
    check_lookups(c, "without-set-head-1", oracle, observation, attempt, [t])
    check_captured(c, "without-set-head-1", result, [t])
    check_closing_sync(c, "without-set-head-1", attempt)


def scenario_freezer(c, oracle, observation, attempts, commit):
    (attempt,) = attempts
    result = ended(c, "freezer-1", attempt)
    if result and (result.get("frozen") != oracle["side_fork"] + 1 or result.get("side_header_kept") is not True or
                   result.get("canonical_fork_kept") is not True):
        c.fail("freezer-1", f"the freezer froze {result.get('frozen')} blocks and kept the side header "
                            f"{result.get('side_header_kept')}; want {oracle['side_fork'] + 1} and kept")


def scenario_removedb_allowed(c, oracle, observation, attempts, commit):
    """Power loss in the counter sync of Begin: no observation and no pin remain, so the manual database guard lets
    removedb delete the store (checked with the shipped runs)."""
    crash, = attempts
    ended(c, "removedb-allowed-1", crash, "before_sync/counter")
    check_trace(c, "removedb-allowed-1", crash, ["opened", "live"])


# ---------------------------------------------------------------- decision bridge (E01-02.decision_bridge)


def events_of(attempt, name):
    return [row for row in attempt.get("events") or [] if row.get("op") == "trace" and row.get("event") == name]


def header_root(observation, number):
    """The state root of generated block number, read from its header."""
    return header_fields(bytes.fromhex(observation["generated_headers"][number - 1]))[STATE_ROOT].hex()


def block_hash(observation, number):
    return observation["generated_hashes"][number][2:]


def intent_evidence(kind, block, number, root):
    """Independent form of IntentEvidence of the Geth decision wiring."""
    data = admission.tag("extractor-geth-intent-evidence-v1") + admission.u64(len(kind)) + kind.encode()
    return hashlib.sha256(data + bytes.fromhex(block) + admission.u64(number) + bytes.fromhex(root)).hexdigest()


def refusal_evidence(block):
    """Independent form of RefusalEvidence: the refused block recorded as a bad block."""
    return hashlib.sha256(admission.tag("extractor-geth-native-refusal-v1") + bytes.fromhex(block)).hexdigest()


def exec_ready_digests(attempts):
    """The EXEC_READY digest of every observation that reached EXEC_READY in these attempts."""
    out = {}
    for attempt in attempts:
        for row in events_of(attempt, "exec_ready"):
            out[row.get("observation")] = row.get("digest")
    return out


def expected_intent(oracle, observation, sequence, kind, old, new, members, ready):
    """The intent record of the Geth write, built from the generated chain: heads and members are block numbers,
    members are (observation sequence, block number) pairs; ready maps observation keys to EXEC_READY digests."""
    rows = []
    for obs, number in members:
        key = key_of(oracle, obs)
        rows.append({"observation": key, "block": block_hash(observation, number),
                     "exec_ready_digest": ready.get(key, "0" * 64), "child_root": header_root(observation, number)})
    first = members[0][1]
    record = {"native_store_id": oracle["store_id"], "producer": f"{oracle['producer_id']}.{oracle['run_id']}",
              "sequence": sequence, "kind": kind,
              "old_head": {"number": old, "hash": block_hash(observation, old)},
              "new_head": {"number": new, "hash": block_hash(observation, new)}, "members": rows,
              "evidence_digest": intent_evidence(kind, block_hash(observation, first), first,
                                                 header_root(observation, first)),
              "events": kind != "SIDE"}
    record["digest"] = decision.intent_digest(record)
    return record


def check_decisions(c, label, attempt, want):
    """want lists the decisions of the attempt in order: (expected intent record, outcome), outcome "BATCH",
    "CHECKPOINT" or "REJECTED". Each intent marker equals the independent marker bytes, and each NATIVE_DECIDED or
    rejected digest recomputes from it. Return the intent markers seen."""
    intents, decided = events_of(attempt, "intent"), events_of(attempt, "decided")
    rejected = events_of(attempt, "intent_rejected")
    if len(intents) != len(want):
        c.fail(label, f"{len(intents)} intents, want {len(want)}")
        return []
    closing = iter(decided)
    refusals = iter(rejected)
    for row, (record, outcome) in zip(intents, want):
        if row.get("marker") != decision.marker_value(record).decode() or row.get("digest") != record["digest"] or \
                row.get("sequence") != record["sequence"] or row.get("kind") != record["kind"]:
            c.fail(label, f"intent {record['sequence']} differs from the independent {record['kind']} intent")
            continue
        if outcome == "REJECTED":
            done = next(refusals, {})
            evidence = refusal_evidence(record["members"][0]["block"])
            if done.get("sequence") != record["sequence"] or \
                    done.get("digest") != decision.rejected_digest(record["digest"], evidence):
                c.fail(label, f"intent {record['sequence']} was not rejected with the native refusal evidence")
        else:
            done = next(closing, {})
            if done.get("sequence") != record["sequence"] or done.get("delivery") != outcome or \
                    done.get("digest") != decision.decided_digest(record["digest"], outcome):
                c.fail(label, f"intent {record['sequence']} was not decided as {outcome}")
    if next(closing, None) is not None or next(refusals, None) is not None:
        c.fail(label, "a decision was recorded without its intent in this attempt")
    return [row.get("marker") for row in intents]


def check_resolved_after(c, label, attempt, sequence, record, outcome, evidence):
    """A recovery settles saved intent sequence (written by an earlier attempt) from native evidence, without a new
    intent, as outcome."""
    rows = [row for row in events_of(attempt, "settling") if row.get("sequence") == sequence]
    if [row.get("evidence") for row in rows] != [evidence]:
        c.fail(label, f"intent {sequence} was settled from {[row.get('evidence') for row in rows]}, want {evidence}")
    if outcome == "REJECTED":
        done = [row for row in events_of(attempt, "intent_rejected") if row.get("sequence") == sequence]
        want = decision.rejected_digest(record["digest"], refusal_evidence(record["members"][0]["block"]))
        if [row.get("digest") for row in done] != [want]:
            c.fail(label, f"intent {sequence} was not rejected once with the native refusal evidence")
        return
    done = [row for row in events_of(attempt, "decided") if row.get("sequence") == sequence]
    if [(row.get("delivery"), row.get("digest")) for row in done] != [(outcome, decision.decided_digest(record["digest"],
                                                                                                       outcome))]:
        c.fail(label, f"intent {sequence} was not decided once as {outcome}")


def decision_counter(result):
    value = (result.get("decision_records") or {}).get(f"extractor-decision-v1/geth-probe.r1/counter")
    return json.loads(value)["next"] if value else 0


def check_decision_end(c, label, oracle, result, counter, admitted, markers, pins=(), stored=None):
    """End state of a decision attempt: the decision and admission counters, the native markers left, the live pins
    (observation sequences), and no inputs of closed observations."""
    if decision_counter(result) != counter:
        c.fail(label, f"decision counter {decision_counter(result)}, want {counter}")
    if records(result)[1] != admitted:
        c.fail(label, f"admission counter {records(result)[1]}, want {admitted}")
    want = sorted(decision.marker_key(oracle["producer_id"], oracle["run_id"], seq) for seq in markers)
    if sorted(result.get("markers") or {}) != want:
        c.fail(label, f"markers {sorted(result.get('markers') or {})}, want {want}")
    check_pins(c, label, oracle, result, list(pins))
    left = sorted(result.get("inputs_left") or {})
    if left != sorted(key_of(oracle, seq) for seq in pins):
        c.fail(label, f"inputs of closed observations are left: {left}")
    if stored is not None and result.get("stored") != stored:
        c.fail(label, f"stored blocks {result.get('stored')}, want {stored}")


def check_mirror(c, label, attempt, allowed):
    """The mirror is asked only about NATIVE_DECIDED BATCH sequences, never about a rejected intent."""
    asked = sorted({row.get("sequence") for row in attempt.get("events") or [] if row.get("op") == "mirror"})
    if any(seq not in allowed for seq in asked):
        c.fail(label, f"the mirror was asked about sequences {asked}, allowed {sorted(allowed)}")


LIVE_IMPORT = ["begin", "pinned", "sealed", "admitted", "exec_ready", "committed", "intent", "mutated", "decided"]


def scenario_decision_clean(c, oracle, observation, attempts, commit):
    first, restart = attempts
    t = oracle["target"]
    result = ended(c, "decision-clean-1", first)
    if result:
        check_live_result(c, "decision-clean-1", oracle, result)
        if imports(result) != [("target", None), ("next", None)]:
            c.fail("decision-clean-1", f"imports {imports(result)}")
        check_trace(c, "decision-clean-1", first, ["opened", "bridge_opened", "live"] + LIVE_IMPORT * 2)
        ready = exec_ready_digests([first])
        check_decisions(c, "decision-clean-1", first, [
            (expected_intent(oracle, observation, 0, "IMPORT", t - 1, t, [(0, t)], ready), "BATCH"),
            (expected_intent(oracle, observation, 1, "IMPORT", t, t + 1, [(1, t + 1)], ready), "BATCH")])
        # The mirror has applied nothing: both obligations keep their markers.
        check_decision_end(c, "decision-clean-1", oracle, result, 2, 2, [0, 1])
        check_mirror(c, "decision-clean-1", first, {0, 1})
        check_head(c, "decision-clean-1", oracle, observation, result, t + 1)
        check_lookups(c, "decision-clean-1", oracle, observation, first, [t, t + 1])
        check_captured(c, "decision-clean-1", result, [t, t + 1])
        check_closing_sync(c, "decision-clean-1", first)
    later = ended(c, "decision-clean-2", restart)
    if not later:
        return
    check_live_result(c, "decision-clean-2", oracle, later)
    # Known blocks: no observation, no decision, no execution.
    if imports(later) != [("target", None), ("next", None)]:
        c.fail("decision-clean-2", f"imports {imports(later)}")
    check_trace(c, "decision-clean-2", restart, ["opened", "bridge_opened", "live"])
    check_decision_end(c, "decision-clean-2", oracle, later, 2, 2, [])
    check_mirror(c, "decision-clean-2", restart, {0, 1})
    done = {f"extractor-decision-v1/geth-probe.r1/{name}": json.dumps({field: 2}, separators=(",", ":"))
            for name, field in (("counter", "next"), ("lowwater", "sequence"), ("purged", "sequence"))}
    if later.get("decision_records") != done:
        c.fail("decision-clean-2", "the applied decisions were not cleaned up to the counter")
    check_head(c, "decision-clean-2", oracle, observation, later, t + 1)
    check_captured(c, "decision-clean-2", later, [])


def recovered_side_then_canonical(c, label, oracle, observation, attempt, result, ready, prefix):
    """After recovery the saved EXEC_READY observation 0 of the target is decided as SIDE; the import of the next
    block makes it canonical with one CANONICAL decision that closes both."""
    t = oracle["target"]
    check_trace(c, label, attempt, prefix + ["replaying", "replayed", "committed", "intent", "mutated", "decided",
                                             "live"] + LIVE_IMPORT)
    ready = dict(ready, **exec_ready_digests([attempt]))
    check_decisions(c, label, attempt, [
        (expected_intent(oracle, observation, 0, "SIDE", t - 1, t - 1, [(0, t)], ready), "CHECKPOINT"),
        (expected_intent(oracle, observation, 1, "CANONICAL", t - 1, t + 1, [(1, t + 1), (0, t)], ready), "BATCH")])
    check_decision_end(c, label, oracle, result, 2, 2, [1])
    check_head(c, label, oracle, observation, result, t + 1)
    if (result.get("roots_at_start") or {}).get("target") is not False:
        c.fail(label, "the child root of the target was durable before the replay")


def scenario_decision_exec_ready(c, oracle, observation, attempts, commit, clean_exec):
    """AC-CAP-07-01: power loss before the closing EXEC_READY sync. Nothing is decided; recovery re-executes the same
    ADMITTED obligation (the write waits for the bridge), then replays it natively and decides it."""
    crash, recover = attempts
    t = oracle["target"]
    ended(c, "decision-exec-ready-1", crash, "before_sync/exec-ready")
    check_trace(c, "decision-exec-ready-1", crash, ["opened", "bridge_opened", "live", "begin", "pinned", "sealed",
                                                    "admitted"])
    result = ended(c, "decision-exec-ready-2", recover)
    if not result:
        return
    recovered_side_then_canonical(c, "decision-exec-ready-2", oracle, observation, recover, result, {},
                                  ["opened", "recovering", "resumed", "exec_ready", "deferred", "recovered",
                                   "bridge_opened"])
    execs = [row.get("exec") for row in events_of(recover, "exec_ready") if row.get("observation") == key_of(oracle, 0)]
    if clean_exec is None or execs != [clean_exec]:
        c.fail("decision-exec-ready-2", "the recovered execution digest differs from the clean decision import")
    # The capture runs once for the recovered obligation and once for the new block; the replay captures nothing.
    check_captured(c, "decision-exec-ready-2", result, [t, t + 1])


def scenario_decision_child_root(c, oracle, observation, attempts, commit):
    """AC-CAP-07-02: EXEC_READY is durable, the child root is not. Native-only replay makes the same root durable;
    the EXEC_READY record and the observation stay."""
    crash, recover = attempts
    t = oracle["target"]
    ended(c, "decision-child-root-1", crash, "after_sync/exec-ready")
    # The crash comes right after the EXEC_READY sync, before its trace event.
    check_trace(c, "decision-child-root-1", crash, ["opened", "bridge_opened", "live", "begin", "pinned", "sealed",
                                                    "admitted"])
    result = ended(c, "decision-child-root-2", recover)
    if not result:
        return
    # The durable EXEC_READY record found at the restart binds the admitted record and the closed consumed manifest;
    # the replayed decision names exactly this record.
    saved, _ = records({"records": result.get("records_at_start")})
    rows = saved.get(0, {})
    ready = rows.get("exec-ready") or {}
    capsule = (rows.get("admitted") or {}).get("capsule_digest", "0" * 64)
    consumed = source_inputs.consumed_digest(bytes.fromhex(capsule), 0,
                                             source_inputs.chain_start(bytes.fromhex(capsule))).hex()
    if set(rows) != {"preparing", "admitted", "exec-ready"} or ready.get("consumed_digest") != consumed or \
            ready.get("digest") != admission.exec_ready_digest(rows["admitted"].get("digest", "0" * 64), consumed,
                                                               ready.get("exec_digest", "0" * 64)):
        c.fail("decision-child-root-2", "the restart did not find the durable EXEC_READY record of the target")
    recovered_side_then_canonical(c, "decision-child-root-2", oracle, observation, recover, result,
                                  {key_of(oracle, 0): ready.get("digest")}, ["opened", "bridge_opened"])
    # The replay neither captures nor answers BLOCKHASH from the capsule: only the next block does.
    check_captured(c, "decision-child-root-2", result, [t + 1])
    check_lookups(c, "decision-child-root-2", oracle, observation, recover, [t + 1])


def scenario_decision_completed(name, point, evidence, prefix_events, crash_events, recover_imports):
    """AC-CAP-07-03 and the marker crash: the saved IMPORT intent of the target is completed from native evidence,
    with the intent bytes of the clean decision import, then the next block is imported."""
    label = name.replace("_", "-")

    def check(c, oracle, observation, attempts, commit, clean_markers):
        crash, recover = attempts
        t = oracle["target"]
        ended(c, f"{label}-1", crash, point)
        check_trace(c, f"{label}-1", crash, ["opened", "bridge_opened", "live", "begin", "pinned", "sealed",
                                             "admitted", "exec_ready", "committed"] + crash_events)
        result = ended(c, f"{label}-2", recover)
        if not result:
            return
        ready = exec_ready_digests([crash, recover])
        record = expected_intent(oracle, observation, 0, "IMPORT", t - 1, t, [(0, t)], ready)
        markers = [row.get("marker") for row in events_of(crash, "intent")] + \
            [row.get("marker") for row in events_of(recover, "intent") if row.get("sequence") == 0]
        if not clean_markers or any(marker != clean_markers[0] for marker in markers) or \
                clean_markers[0] != decision.marker_value(record).decode():
            c.fail(f"{label}-2", "the completed intent differs from the intent of the clean decision import")
        check_resolved_after(c, f"{label}-2", recover, 0, record, "BATCH", evidence)
        check_trace(c, f"{label}-2", recover, ["opened", "bridge_opened", "settling"] + prefix_events +
                    ["decided", "live"] + LIVE_IMPORT)
        rows = imports(result)
        if [name for name, _ in rows] != [name for name, _ in recover_imports] or \
                any((error is None) != (want is None) or (want is not None and oracle["messages"][want] not in error)
                    for (_, error), (_, want) in zip(rows, recover_imports)):
            c.fail(f"{label}-2", f"imports {rows}, want {recover_imports}")
        nxt = expected_intent(oracle, observation, 1, "IMPORT", t, t + 1, [(1, t + 1)], ready)
        intents = [row for row in events_of(recover, "intent") if row.get("sequence") == 1]
        if [row.get("marker") for row in intents] != [decision.marker_value(nxt).decode()]:
            c.fail(f"{label}-2", "the next block was not imported as decision 1")
        check_decision_end(c, f"{label}-2", oracle, result, 2, 2, [0, 1])
        check_head(c, f"{label}-2", oracle, observation, result, t + 1)
    return check


def scenario_decision_rejected(c, oracle, observation, attempts, commit, live_rejection):
    """AC-CAP-07-06: the native side refused the transition after the intent and recorded the bad block; the
    rejection of the intent was lost. Recovery records the rejection from the native evidence: no NATIVE_DECIDED,
    no marker, no obligation, the mirror is never asked."""
    crash, recover = attempts
    t = oracle["target"]
    ended(c, "decision-rejected-1", crash,
          f"before_sync-decision-v1/{oracle['producer_id']}.{oracle['run_id']}/0000000000000000/rejected")
    check_trace(c, "decision-rejected-1", crash, ["opened", "bridge_opened", "live", "begin", "pinned", "sealed",
                                                  "admitted", "exec_ready", "committed", "intent", "refused"])
    result = ended(c, "decision-rejected-2", recover)
    if not result:
        return
    record = expected_intent(oracle, observation, 0, "IMPORT", t - 1, t, [(0, t)], exec_ready_digests([crash]))
    if [row.get("marker") for row in events_of(crash, "intent")] != [decision.marker_value(record).decode()]:
        c.fail("decision-rejected-1", "the refused intent differs from the independent IMPORT intent")
    check_resolved_after(c, "decision-rejected-2", recover, 0, record, "REJECTED", "bad_block")
    check_trace(c, "decision-rejected-2", recover, ["opened", "bridge_opened", "settling", "intent_rejected", "live"])
    rejected = [row.get("digest") for row in events_of(recover, "intent_rejected")]
    if rejected != [live_rejection]:
        c.fail("decision-rejected-2", "the recovered rejection differs from the live rejection")
    check_mirror(c, "decision-rejected-2", recover, set())
    check_decision_end(c, "decision-rejected-2", oracle, result, 1, 1, [],
                       stored={"target": False, "next": False, "bad": False})
    if result.get("bad_blocks") != {"target": True, "next": False}:
        c.fail("decision-rejected-2", "the refused block is not recorded natively as a bad block")
    check_head(c, "decision-rejected-2", oracle, observation, result, t - 1)


def scenario_decision_reject_live(c, oracle, observation, attempts, commit):
    (attempt,) = attempts
    t = oracle["target"]
    result = ended(c, "decision-reject-live-1", attempt)
    if not result:
        return
    rows = imports(result)
    if len(rows) != 1 or rows[0][0] != "target" or oracle["messages"]["refused"] not in str(rows[0][1]):
        c.fail("decision-reject-live-1", f"imports {rows}")
    check_trace(c, "decision-reject-live-1", attempt, ["opened", "bridge_opened", "live", "begin", "pinned", "sealed",
                                                       "admitted", "exec_ready", "committed", "intent", "refused",
                                                       "intent_rejected"])
    record = expected_intent(oracle, observation, 0, "IMPORT", t - 1, t, [(0, t)], exec_ready_digests([attempt]))
    check_decisions(c, "decision-reject-live-1", attempt, [(record, "REJECTED")])
    check_mirror(c, "decision-reject-live-1", attempt, set())
    check_decision_end(c, "decision-reject-live-1", oracle, result, 1, 1, [],
                       stored={"target": False, "next": False, "bad": False})
    if result.get("bad_blocks") != {"target": True, "next": False}:
        c.fail("decision-reject-live-1", "the refused block is not recorded natively as a bad block")
    check_head(c, "decision-reject-live-1", oracle, observation, result, t - 1)


CHECKS = {
    "clean": scenario_clean,
    "begin_before_counter_sync": scenario_crash_before_admission("begin-before-counter-sync", "before_sync/counter",
                                                                 abandoned=False, pinned=False),
    "begin_after_counter_sync": scenario_crash_before_admission("begin-after-counter-sync", "after_sync/counter",
                                                                abandoned=True, pinned=False),
    "admit_before_sync": scenario_crash_before_admission("admit-before-sync", "before_sync/admitted",
                                                         abandoned=True, pinned=True),
    "removedb_allowed": scenario_removedb_allowed,
    "admit_after_sync": scenario_admit_after_sync,
    "reject_live": scenario_reject_live,
    "reject_after_admit": scenario_reject_after_admit,
    "without_set_head": scenario_without_set_head,
    "freezer": scenario_freezer,
    "decision_clean": scenario_decision_clean,
    "decision_child_root": scenario_decision_child_root,
    "decision_reject_live": scenario_decision_reject_live,
}

COMPLETED = {
    "decision_intent": scenario_decision_completed(
        "decision_intent", "after_sync-decision-v1/geth-probe.r1/counter", "reimport",
        ["replaying", "replayed", "committed", "intent", "mutated"], [], [("next", None)]),
    "decision_mutation": scenario_decision_completed(
        "decision_mutation", "native/mutated", "stored_block", [], ["intent", "mutated"],
        # The target is the head now without its snapshot layer: Geth would re-execute it, and the known-block
        # guard refuses to open a second observation of it.
        [("target", "known_block"), ("next", None)]),
    "decision_marker": scenario_decision_completed(
        "decision_marker", "before_sync-decision-v1/geth-probe.r1/0000000000000000/decided", "marker", [],
        ["intent", "mutated"], [("next", None)]),
}


def compare(observation, oracle, commit):
    """Return the differences between one probe observation and the oracle."""
    c = Checker()
    t = oracle["target"]
    if observation.get("family") != "geth" or observation.get("target") != t or \
            observation.get("lookbacks") != oracle["lookbacks"] or \
            len(observation.get("generated_hashes", [])) != t + 2 or len(observation.get("generated_headers", [])) != t + 1 or \
            observation["generated_hashes"][0] != oracle["genesis_hash"]:
        return ["the observation does not describe the oracle chain"]
    # The generated chain is checked independently: every header hashes to its block hash and links to its parent.
    for number in range(1, t + 2):
        header = bytes.fromhex(observation["generated_headers"][number - 1])
        if "0x" + keccak256(header).hex() != observation["generated_hashes"][number] or \
                "0x" + header_fields(header)[0].hex() != observation["generated_hashes"][number - 1]:
            return [f"generated header {number} does not match its hash or parent"]
    scenarios = {row.get("name"): row.get("attempts") or [] for row in observation.get("scenarios", [])}
    if list(scenarios) != oracle["scenarios"]:
        return [f"scenarios {list(scenarios)} differ from the oracle"]
    for name in oracle["scenarios"]:
        attempts = scenarios[name]
        if [row.get("name") for row in attempts] != oracle["attempts"][name]:
            c.fail(name, "attempts differ from the oracle")
            continue
        clean_decisions = scenarios["decision_clean"][0]
        if name == "exec_ready_before_sync":
            clean = records(scenarios["clean"][0].get("result") or {})[0].get(0, {}).get("exec-ready", {})
            scenario_exec_ready_before_sync(c, oracle, observation, attempts, commit, clean.get("exec_digest"))
        elif name == "decision_exec_ready":
            execs = [row.get("exec") for row in events_of(clean_decisions, "exec_ready")]
            scenario_decision_exec_ready(c, oracle, observation, attempts, commit, execs[0] if execs else None)
        elif name in COMPLETED:
            COMPLETED[name](c, oracle, observation, attempts, commit,
                            [row.get("marker") for row in events_of(clean_decisions, "intent")])
        elif name == "decision_rejected":
            live = [row.get("digest") for row in events_of(scenarios["decision_reject_live"][0], "intent_rejected")]
            scenario_decision_rejected(c, oracle, observation, attempts, commit, live[0] if live else None)
        else:
            CHECKS[name](c, oracle, observation, attempts, commit)
    check_shipped(c, oracle, observation)
    return c.failures


def checked_compare(observation, oracle, commit):
    """Compare, turning a malformed observation into a failure instead of an exception."""
    try:
        return compare(observation, oracle, commit)
    except (AttributeError, IndexError, KeyError, TypeError, ValueError) as error:
        return [f"malformed observation: {type(error).__name__}: {error}"]


# ---------------------------------------------------------------- preparation


def apply_patches(source, native_dir):
    """Apply the product patch set and install its new files; return the record of every change."""
    table = json.loads((native_dir / "patches.json").read_text())
    rows = []
    for patch in table["patches"]:
        path = source / patch["file"]
        before = path.read_text()
        require(before.count(patch["original"]) == 1, f"Patch {patch['name']} must match exactly once")
        after = before.replace(patch["original"], patch["replacement"])
        path.write_text(after)
        rows.append({"name": patch["name"], "file": patch["file"],
                     "sha256_before": hashlib.sha256(before.encode()).hexdigest(),
                     "sha256_after": hashlib.sha256(after.encode()).hexdigest()})
    files = []
    for name, relative in table["files"].items():
        target = source / relative
        # copy_artifact opens the target with O_EXCL, so an existing native path is refused.
        files.append({"relative": relative, "copy": copy_artifact(native_dir / name, target,
                                                                  reference(native_dir / name, 4 * 1024 * 1024))})
    return {"patches": rows, "files": files}


def install(source, copies):
    """Install the patch set, the extractor packages and the probe into an extracted Geth tree."""
    record = apply_patches(source, copies["native"])
    rows = []
    for package, (origin, names) in PACKAGES.items():
        directory = copies["native"] if package.endswith("/native") else copies[package]
        (source / package).mkdir(parents=True, exist_ok=True)
        for name in names:
            rows.append({"relative": f"{package}/{name}",
                         "copy": copy_artifact(directory / name, source / package / name,
                                               reference(directory / name, 4 * 1024 * 1024))})
    probe = source / PROBE_PATH
    probe.parent.mkdir(parents=True)
    rows.append({"relative": PROBE_PATH, "copy": copy_artifact(copies["fixture"] / "probe.go", probe,
                                                               reference(copies["fixture"] / "probe.go"))})
    record["packages"] = rows
    return record


def mutant_targets(table, patches):
    """Map each native mutant to the text it changes; the original must occur exactly once there."""
    texts = {"geth.go": (NATIVE / "geth.go").read_text()}
    for patch in patches["patches"]:
        texts.setdefault(patch["file"], "")
        texts[patch["file"]] += patch["replacement"]
    out = {}
    for mutant in table["mutants"]:
        text = texts.get(mutant["file"], "")
        require(text.count(mutant["original"]) == 1, f"Mutant {mutant['name']} must match its patched text exactly once")
        require(set(mutant["binaries"]) <= {"probe", "geth"} and mutant["binaries"], f"Mutant {mutant['name']} binaries")
        out[mutant["name"]] = mutant["file"]
    return out


def apply_mutant(source, mutant):
    """Apply one native mutant to the installed tree; return a function that restores it."""
    relative = "extractor/admission/native/geth.go" if mutant["file"] == "geth.go" else mutant["file"]
    path = source / relative
    before = path.read_text()
    require(before.count(mutant["original"]) == 1, f"Mutant {mutant['name']} must match exactly once")
    path.write_text(before.replace(mutant["original"], mutant["replacement"]))
    return lambda: path.write_text(before)


def go_environment(workspace=None):
    return {"CGO_ENABLED": "1", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64", "GOCACHE": "/work/build-cache",
            "GOMODCACHE": "/modules", "GOPATH": "/work/gopath", "GOFLAGS": "-mod=readonly", "GOPROXY": "off",
            "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOTMPDIR": "/work/gotmp",
            "GOWORK": workspace or "off"}


def prepare(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    # pinned_json refuses a relative path before anything is written.
    manifest_ref, manifest = pinned_json(manifest_path, expected_sha, 64 * 1024 * 1024, deadline)
    require(manifest.get("evidence_kind") == "actual-build" and manifest.get("task_id") == "E01-01.builds",
            "An accepted actual build is required")
    root = manifest_path.parent
    capture_probe.refuse_inside(output, (root, ROOT))
    state = capture_probe.source_state()
    with single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "admission-geth-preparation", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": state, "build_manifest": manifest_ref,
                  "public_profile_credit": False}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            copies = {"fixture": output / "fixture", "native": output / "native", "capture": output / "capture"}
            record["fixture"] = capture_probe.copy_sources(FIXTURE_FILES, FIXTURE, copies["fixture"], deadline)
            record["native"] = capture_probe.copy_sources(NATIVE_FILES, NATIVE, copies["native"], deadline)
            record["capture"] = capture_probe.copy_sources(CAPTURE_FILES, ROOT / "core" / "capture", copies["capture"],
                                                           deadline)
            record["shared"] = {}
            for package, (origin, names) in PACKAGES.items():
                if package.endswith("/native"):
                    continue
                copies[package] = output / "shared" / package.split("/")[-1]
                copies[package].parent.mkdir(exist_ok=True)
                record["shared"][package] = capture_probe.copy_sources(names, origin, copies[package], deadline)
            record["harness_code"] = capture_probe.copy_sources(HARNESS_FILES, HARNESS, output / "harness-code", deadline)
            record["driver_code"] = capture_probe.copy_sources(("admission_geth.py",), FIXTURE, output / "driver-code",
                                                               deadline)
            patches = json.loads((copies["native"] / "patches.json").read_text())
            _, table = json_artifact([row["copy"] for row in record["fixture"]
                                      if row["copy"]["path"].endswith("/native_mutants.json")][0], deadline=deadline)
            mutant_targets(table, patches)
            compiler = manifest["image_inputs"]["golang-build-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/golang@sha256:[0-9a-f]{64}", compiler), "Unpinned compiler")
            require(manifest["builds"]["geth"]["compiler_image"] == compiler, "Geth compiler mismatch")
            record["compiler_image"] = compiler
            closure = manifest["module_closures"]["geth"]
            require(closure["source_commit"] == manifest["builds"]["geth"]["source_commit"], "Source closure mismatch")
            require(closure["source_commit"] == patches["source_commit"], "The patch set names another Geth commit")
            record["source_commit"] = closure["source_commit"]
            directory = output / "geth"
            directory.mkdir()
            archive = artifact(closure["source_archive"], BINARY_LIMIT, root, deadline)
            record["source_archive"] = copy_artifact(archive, directory / "source.tar", closure["source_archive"],
                                                     BINARY_LIMIT, deadline)
            for module in closure["artifacts"]:
                for field in ("info", "mod", "zip"):
                    artifact(module[field], 1024 * 1024 * 1024, root, deadline)
            record["module_artifacts_verified"] = len(closure["artifacts"])
            source = directory / "source"
            source.mkdir()
            data = read_bytes(directory / "source.tar", closure["source_archive"]["size_bytes"], deadline)
            with tarfile.open(fileobj=io.BytesIO(data)) as stream:
                for member in stream:
                    check_deadline(deadline)
                    stream.extract(member, source, filter="data")
            del data
            record["install"] = install(source, copies)
            capture_dir = directory / "capture"
            capture_dir.mkdir()
            for name in CAPTURE_FILES:
                copy_artifact(copies["capture"] / name, capture_dir / name, reference(copies["capture"] / name))
            for name in ("bin", "build-cache", "gopath", "gotmp", "workspace"):
                (directory / name).mkdir()
            # The probe imports core/capture, a separate module, through a workspace. The geth command and the
            # unit tests build without it. Workspace mode may record checksums in go.work.sum, so the workspace
            # file lives in its own writable directory.
            version = capture_probe.go_directive((source / "go.mod").read_text())
            (directory / "workspace/go.work").write_text(f"go {version}\n\nuse (\n\t../source\n\t../capture\n)\n")
            record["go_work"] = reference(directory / "workspace/go.work")
            mounts = [(directory, "/work", True), (root / "cache" / "geth" / "mod", "/modules", True)]
            mounts += [(directory / name, f"/work/{name}", False)
                       for name in ("bin", "build-cache", "gopath", "gotmp", "workspace")]
            go = "/usr/local/go/bin/go"
            workspace = "/work/workspace/go.work"
            native.command(compiler, [go, "mod", "verify"], mounts, cap=300, memory="4g", cwd="/work/source",
                           environment=go_environment())
            vet = native.command(compiler, [go, "vet", "./extractor/...", "./core", "./core/vm", "./core/rawdb", "./eth",
                                            "./cmd/utils", "./cmd/geth"],
                                 mounts, cap=1800, memory="6g", cwd="/work/source", environment=go_environment())
            record["vet"] = reference(vet)
            probe_vet = native.command(compiler, [go, "vet", "./cmd/extractor-admission-probe"], mounts, cap=900,
                                       memory="4g", cwd="/work/source", environment=go_environment(workspace))
            record["probe_vet"] = reference(probe_vet)
            tests = native.command(compiler, [go, "test", "-race", "-count=1", "-cover", "-v", *UNIT_PACKAGES],
                                   mounts, cap=1800, memory="6g", cwd="/work/source", environment=go_environment())
            record["unit_tests"] = reference(tests)
            text = b"\n" + tests.read_bytes()
            for package in ("extractor/admission", "extractor/admission/native", "extractor/source_inputs"):
                require(f"\nok  \tgithub.com/ethereum/go-ethereum/{package}\t".encode() in text,
                        f"Unit tests of {package} did not pass inside the native module")
            build = [go, "build", "-p", "2", "-trimpath", "-o"]

            def compile_binaries(suffix, binaries):
                paths = {}
                if "geth" in binaries:
                    native.command(compiler, build + [f"/work/bin/geth{suffix}", "./cmd/geth"], mounts, cap=1800,
                                   memory="6g", cwd="/work/source", environment=go_environment())
                    paths["geth"] = reference(directory / f"bin/geth{suffix}", deadline=deadline)
                if "probe" in binaries:
                    native.command(compiler, build + [f"/work/bin/probe{suffix}", "./cmd/extractor-admission-probe"],
                                   mounts, cap=1800, memory="6g", cwd="/work/source",
                                   environment=go_environment(workspace))
                    paths["probe"] = reference(directory / f"bin/probe{suffix}", deadline=deadline)
                return paths

            record["binaries"] = compile_binaries("", ("geth", "probe"))
            record["mutants"] = {}
            for mutant in table["mutants"]:
                restore = apply_mutant(source, mutant)
                try:
                    # Every mutant must compile: a failed build fails the preparation.
                    record["mutants"][mutant["name"]] = compile_binaries(f"-{mutant['name']}", mutant["binaries"])
                finally:
                    restore()
            workspace_sum = directory / "workspace/go.work.sum"
            record["go_work_sum"] = reference(workspace_sum) if workspace_sum.exists() else None
            copy_artifact(FIXTURE / "genesis.json", directory / "bin/genesis.json", reference(FIXTURE / "genesis.json"),
                          deadline=deadline)
            record["genesis"] = reference(directory / "bin/genesis.json", deadline=deadline)
            record["result"] = "PASS"
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "preparation.json", record, native, started)
    return output, record


# ---------------------------------------------------------------- measured run


def run(preparation_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    # pinned_json refuses a relative path before anything is written.
    preparation_ref, preparation = pinned_json(preparation_path, expected_sha, 16 * 1024 * 1024, deadline)
    require(preparation.get("kind") == "admission-geth-preparation" and preparation.get("result") == "PASS" and
            preparation.get("cleanup_complete") is True, "A passing preparation is required")
    manifest_root = Path(preparation["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (preparation_path.parent, manifest_root, ROOT))
    state = capture_probe.source_state()
    with single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "admission-geth-run", "result": "FAIL", "run_id": uuid.uuid4().hex,
                  "source": state, "preparation": preparation_ref, "public_profile_credit": False, "ac_verdict": None,
                  "phases": {"admission_started": time.time()}, "mutants": {}}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            oracles = [row["copy"] for row in preparation["fixture"] if row["copy"]["path"].endswith("/expected.json")]
            require(len(oracles) == 1, "The preparation must retain exactly one oracle")
            _, oracle = json_artifact(oracles[0], 1024 * 1024, deadline=deadline)
            record["oracle"] = oracles[0]
            _, manifest = json_artifact(preparation["build_manifest"], 64 * 1024 * 1024, deadline=deadline)
            runtime = manifest["image_inputs"]["debian-runtime-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
            commit = preparation["source_commit"]
            base = {name: artifact(ref, BINARY_LIMIT, deadline=deadline) for name, ref in preparation["binaries"].items()}
            artifact(preparation["genesis"], 1024 * 1024, deadline=deadline)
            mutants = {name: {kind: artifact(ref, BINARY_LIMIT, deadline=deadline) for kind, ref in refs.items()}
                       for name, refs in preparation["mutants"].items()}
            require(mutants, "The preparation built no mutants")
            bin_dir = base["probe"].parent
            record["phases"]["execution_started"] = time.time()

            def execute(name, binaries):
                directory = output / name
                directory.mkdir()
                begun = time.monotonic()
                probe, geth = binaries.get("probe", base["probe"]), binaries.get("geth", base["geth"])
                native.command(runtime, [f"/probe/{probe.name}", "run", "/probe/genesis.json", "/out/work",
                                         "/out/probe.json", commit, f"/probe/{geth.name}"],
                               [(bin_dir, "/probe", True), (directory, "/out", False)], cap=600, memory="2g")
                failures = checked_compare(load(directory / "probe.json", OBSERVATION_LIMIT, deadline), oracle, commit)
                return {"observation": reference(directory / "probe.json", OBSERVATION_LIMIT, deadline),
                        "binaries": {kind: path.name for kind, path in {"probe": probe, "geth": geth}.items()},
                        "failures": failures, "elapsed_seconds": time.monotonic() - begun}

            record["probe"] = execute("probe", {})
            record["probe"]["result"] = "PASS" if not record["probe"]["failures"] else "FAIL"
            for name, binaries in mutants.items():
                row = execute(f"mutant-{name}", binaries)
                row["result"] = "KILLED" if row["failures"] else "SURVIVED"
                record["mutants"][name] = row
            record["phases"]["execution_finished"] = time.time()
            if record["probe"]["result"] == "PASS" and all(row["result"] == "KILLED" for row in record["mutants"].values()):
                record["result"] = "PASS"
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "run-record.json", record, native, started)
    return output, record


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    first = commands.add_parser("prepare")
    first.add_argument("--build-manifest", type=Path, required=True)
    first.add_argument("--sha256", required=True)
    first.add_argument("--output", type=Path, required=True)
    second = commands.add_parser("run")
    second.add_argument("preparation", type=Path)
    second.add_argument("--sha256", required=True)
    second.add_argument("--output", type=Path, required=True)
    third = commands.add_parser("judge", help="judge a saved probe observation with the oracle")
    third.add_argument("observation", type=Path)
    third.add_argument("--commit", required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "judge":
            oracle = json.loads((FIXTURE / "expected.json").read_text())
            failures = checked_compare(json.loads(args.observation.read_text()), oracle, args.commit)
            print(json.dumps({"failures": failures}, indent=2))
            return 0 if not failures else 1
        if args.command == "prepare":
            path, record = prepare(args.build_manifest, args.sha256, args.output)
        else:
            path, record = run(args.preparation, args.sha256, args.output)
    except (Interrupted, Exception) as error:
        print(json.dumps({"valid": False, "error": f"{type(error).__name__}: {error}"}))
        return 2
    print(json.dumps({"result": record["result"], "output": str(path), "error": record.get("error")}))
    return 0 if record["result"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
