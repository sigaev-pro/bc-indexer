#!/usr/bin/env python3
"""Build and run the E01-02.inputs Geth probe inside the accepted Geth import.

`prepare` extracts the accepted Geth source, adds the shared source_inputs
package, a test-only BLOCKHASH hook in the serial state processor and the
probe, then compiles them offline with the accepted compiler. It also builds
one probe binary per native mutant. `run` executes the probe and every mutant
in the pinned runtime image and judges their observations with the oracle in
this file and `fixtures/source_inputs_geth/expected.json`. The probe must pass;
every mutant must fail. Preparation is not part of the measured run. Private
local evidence gives no AC verdict and no public-profile credit.
"""

import argparse
import functools
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

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from artifacts import artifact, check_deadline, copy_artifact, json_artifact, load, pinned_json, read_bytes, reference, require
import capture_probe
from capture_probe import Interrupted, block_stops, single_writer, unblock_stops, write_until_done
from native import Native, new_output
import source_inputs

ROOT = HERE.parents[1]
FIXTURE = HERE / "fixtures" / "source_inputs_geth"
SHARED = ROOT / "adapters" / "geth" / "source_inputs"
FIXTURE_FILES = ("probe.go", "hook.go.txt", "genesis.json", "expected.json", "ORACLE.md", "native_mutants.json")
SHARED_FILES = ("inputs.go", "inputs_test.go")
HARNESS_FILES = ("source_inputs_geth.py", "source_inputs.py", "capture_probe.py", "native.py", "owned.py",
                 "artifacts.py")
ANCHOR_FILE = "core/state_processor.go"
ANCHOR = "\tdefer evm.Release()\n"
HOOK_CALL = ("\tif extractorGetHash != nil {\n"
             "\t\tevm.Context.GetHash = extractorGetHash(header, evm.Context.GetHash)\n"
             "\t}\n")
TREE_FILES = {"probe.go": "cmd/extractor-inputs-probe/probe.go", "hook.go.txt": "core/extractor_inputs_probe.go",
              "inputs.go": "extractor/source_inputs/inputs.go", "inputs_test.go": "extractor/source_inputs/inputs_test.go"}
PREPARATION_SECONDS = 3600
RUN_SECONDS = 1800
BINARY_LIMIT = 512 * 1024 * 1024


# ---------------------------------------------------------------- oracle

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
        if size == 0 or data[offset + 1] == 0 or length < 56:
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


def capsule_digest(refs):
    """Capsule digest from item references, with the formula of the shared README."""
    data = source_inputs.tag("extractor-source-inputs-capsule-v1") + source_inputs.u64(len(refs))
    for ref in refs:
        name = ref["name"].encode()
        data += source_inputs.u64(len(name)) + name + source_inputs.u64(ref["length"]) + bytes.fromhex(ref["sha256"])
    return hashlib.sha256(data).hexdigest()


def poison(oracle, number):
    """The changed extra data of a poisoned header, from the formula in expected.json."""
    return oracle["poison_format"].format(number=number).encode()


def window(oracle):
    """Ancestor numbers below the parent that BLOCKHASH of the target block can read."""
    return list(range(max(0, oracle["target"] - 256), oracle["target"] - 1))


def item_names(oracle):
    names = oracle["capsule_items"]
    split = names.index("parent-root") + 1
    return names[:split] + [f"ancestor-header-{number}" for number in window(oracle)] + names[split:]


def headers(oracle):
    """Capsule item name of every BLOCKHASH window header, by block number."""
    rows = {number: f"ancestor-header-{number}" for number in window(oracle)}
    rows[oracle["target"] - 1] = "parent-header"
    return rows


def check_header(name, data, number, hashes, poisoned, oracle):
    """Return a failure for one saved header, or None. A poisoned header differs from the native one only
    in its extra data, so its hash differs and its other links stay intact."""
    fields = header_fields(data)
    if int.from_bytes(fields[NUMBER], "big") != number or (number and fields[PARENT_HASH].hex() != hashes[number - 1][2:]):
        return f"capsule item {name} is not the header of block {number}"
    if poisoned != (fields[EXTRA] == poison(oracle, number)) or \
            poisoned == ("0x" + keccak256(data).hex() == hashes[number]):
        return f"capsule item {name} is not the {'poisoned' if poisoned else 'native'} header of block {number}"
    return None


def check_body(data, transactions):
    """The body is the RLP list [transactions, uncles] with the expected transactions and no uncles."""
    fields = rlp_list(data)
    if len(fields) != 2 or not isinstance(fields[0][0], list) or fields[1][0] != []:
        return False
    # A legacy transaction is an RLP list and its hash covers that encoding. A typed one is a byte string
    # whose first byte, the type, is at most 0x7f (EIP-2718).
    if any(not isinstance(value, list) and value[:1] > b"\x7f" for value, _ in fields[0][0]):
        return False
    found = ["0x" + keccak256(raw if isinstance(value, list) else value).hex() for value, raw in fields[0][0]]
    return found == transactions


def check_capsule(result, observation, oracle, commit, want, fail):
    """Check the saved capsule bytes against independent values; return the digest and the saved items."""
    capsule = result.get("capsule") or {}
    refs = capsule.get("items") or []
    names = item_names(oracle)
    if [ref.get("name") for ref in refs] != names:
        fail("capsule items differ from the oracle")
        return None, None
    # Each item is one chunk, which check_seal_events relies on.
    if not all(type(ref.get("length")) is int and 0 < ref["length"] <= source_inputs.CHUNK_BYTES for ref in refs):
        fail("a capsule item is empty or longer than one chunk")
        return None, None
    if capsule.get("observation") != oracle["observation"] or capsule.get("digest") != capsule_digest(refs):
        fail("capsule digest differs from the independent formula")
        return None, None
    rows = result.get("capsule_items") or []
    if [row.get("name") for row in rows] != names:
        fail("saved capsule bytes differ from the manifest")
        return None, None
    items = {row["name"]: bytes.fromhex(row["data"]) for row in rows}
    if any(len(items[ref["name"]]) != ref["length"] or hashlib.sha256(items[ref["name"]]).hexdigest() != ref["sha256"]
           for ref in refs):
        fail("saved capsule bytes differ from the manifest")
        return None, None
    hashes, target, saved = observation["generated_hashes"], oracle["target"], want.get("saved", [])
    try:
        checks = {
            "header-rlp": "0x" + keccak256(items["header-rlp"]).hex() == hashes[target],
            "body-rlp": check_body(items["body-rlp"], observation["generated_transactions"]
                                   [target - 1 if "body-rlp" in saved else target]),
            "encodings": items["encodings"] == oracle["encodings"].encode(),
            "parent-hash": items["parent-hash"].hex() == hashes[target - 1][2:],
            "parent-root": items["parent-root"] == header_fields(items["parent-header"])[STATE_ROOT],
            "chain-config": all(json.loads(items["chain-config"]).get(key) == value
                                for key, value in oracle["chain_config"].items()),
            "fork-id": json.loads(items["fork-id"]) == {
                "hash": f"{zlib.crc32(bytes.fromhex(hashes[0][2:])):08x}", "next": "0"},
            "native-commit": items["native-commit"] == commit.encode(),
            "normalization-profile": items["normalization-profile"] == oracle["normalization_profile"].encode(),
        }
        for name, ok in checks.items():
            if not ok:
                fail(f"capsule item {name} differs from the expected bytes")
        for number, name in headers(oracle).items():
            problem = check_header(name, items[name], number, hashes, name in saved, oracle)
            if problem:
                fail(problem)
    except (AttributeError, IndexError, KeyError, TypeError, ValueError):
        fail("a capsule item does not decode")
        return None, None
    return capsule["digest"], items


def check_seal_events(rows, existed, items, fail):
    """A new seal asks the source, writes one chunk per item, syncs, writes the manifest and syncs before it
    returns. A reseal of saved items asks nothing, writes nothing and syncs."""
    sealed = index_of(rows, "sealed")
    if sealed is None:
        fail("the attempt did not seal exactly once")
        return None
    before = rows[:sealed]
    if existed:
        if any(row[0] in ("put", "source") for row in before) or ("sync", None) not in before:
            fail("a reseal asked the source, rewrote the capsule or did not sync")
        return sealed
    if before[:1] != [("source", None)] or before.count(("source", None)) != 1:
        fail("a new seal did not take its items from the source first")
    manifest = [index for index, row in enumerate(before) if row == ("put", "capsule")]
    chunks = [index for index, row in enumerate(before) if row[0] == "put" and row[1].startswith("capsule/")]
    keys = sorted(before[index][1] for index in chunks)
    if keys != [f"capsule/{item:016x}/0000000000000000" for item in range(items)]:
        fail("the seal did not write one chunk for every capsule item")
        return sealed
    if len(manifest) != 1 or before[-1] != ("sync", None):
        fail("the capsule manifest was not written once and synced before execution")
        return sealed
    if not any(row == ("sync", None) for row in before[max(chunks) + 1:manifest[0]]):
        fail("capsule chunks were not synced before the manifest")
    return sealed


def ops(events):
    """Compact form of an event list: ("put", key suffix) or (op, number)."""
    rows = []
    for event in events:
        if event.get("op") == "put":
            rows.append(("put", event.get("key", "").split("/", 2)[-1]))
        else:
            rows.append((event.get("op"), event.get("number")))
    return rows


def index_of(rows, op):
    found = [index for index, row in enumerate(rows) if row[0] == op]
    return found[0] if len(found) == 1 else None


def expected_consumed(capsule):
    """The consumed manifest of an empty dependency log."""
    chain = source_inputs.chain_start(bytes.fromhex(capsule))
    return source_inputs.consumed_digest(bytes.fromhex(capsule), 0, chain).hex()


def check_lookups(events, items, oracle, fail):
    """Every BLOCKHASH lookup is answered with the Keccak-256 hash of the saved header of that block."""
    lookups = [event for event in events if event.get("op") == "lookup"]
    if [event.get("number") for event in lookups] != oracle["requested_numbers"]:
        fail("BLOCKHASH lookups differ from the oracle")
        return []
    answers = []
    for event in lookups:
        name = headers(oracle)[event["number"]]
        answer = keccak256(items[name]).hex()
        if event.get("item") != name or event.get("response") != answer:
            fail("a BLOCKHASH answer differs from the saved header")
        answers.append("0x" + answer)
    return answers


def check_complete(attempt, want, observation, oracle, commit, fail):
    """An attempt that ran block 4 to the end: imported, or rejected by native validation."""
    result = attempt.get("result")
    if attempt.get("exit_code") != 0 or attempt.get("signal") or not isinstance(result, dict):
        fail("the attempt did not finish")
        return
    target = oracle["target"]
    hashes = observation["generated_hashes"]
    rejected = want.get("rejected")
    if result.get("errors"):
        fail("the attempt reported errors")
    if result.get("capsule_existed") is not want["capsule_existed"] or result.get("frontier_at_open") != 0:
        fail("durable state at open differs from the oracle")
    capsule, items = check_capsule(result, observation, oracle, commit, want, fail)
    events = attempt.get("events") or []
    rows = ops(events)
    sources = [event for event in events if event.get("op") == "source"]
    if not want["capsule_existed"] and [event.get("poison") or [] for event in sources] != [want.get("saved", [])]:
        fail("a new seal used the wrong source")
    sealed = check_seal_events(rows, want["capsule_existed"], len(item_names(oracle)), fail)
    opened, started = index_of(rows, "opened"), index_of(rows, "process_start")
    # The sync must lie between the seal and the open, so the slice is empty unless sealed < opened.
    # A failed seal check was already reported; rows[None:opened] then starts at the beginning.
    if (started is None) is not (rejected == "body"):
        fail("block execution started only if the saved body was valid" if started is None else
             "block execution started with a saved body that native validation must reject")
        return
    if opened is None or ("sync", None) not in rows[sealed:opened] or (started is not None and not opened < started):
        fail("the capsule and log were not durable before execution started")
        return
    answers = check_lookups(events, items, oracle, fail) if items is not None and started is not None else []
    if rejected:
        error = oracle["control_errors"][rejected]
        if error not in str(result.get("import_error")) or result.get("head_number") != target - 1 \
                or "consumed" in result or "reader_slots" in result:
            fail("native validation did not reject the saved poisoned item")
    else:
        if result.get("import_error") is not None:
            fail("the attempt reported errors")
        if result.get("head_number") != target or result.get("head_hash") != hashes[target]:
            fail("the imported head differs from the generated block")
        if result.get("reader_slots") != answers:
            fail("native execution did not receive the expected BLOCKHASH bytes")
        consumed = result.get("consumed") or {}
        if capsule is None or consumed.get("digest") != expected_consumed(capsule) or \
                consumed.get("count") != 0 or consumed.get("capsule_digest") != capsule:
            fail("the consumed manifest differs from the independent digest")
        if result.get("cursor") != 0 or \
                not str(result.get("extra_after_finish", "")).startswith(oracle["closed_error_prefix"]):
            fail("the closed log accepted a new response")
    last = started if started is not None else opened
    tail = [row for row in rows[last + 1:] if row[0] != "lookup"]
    expected_tail = [] if rejected else \
        ([("put", "closed"), ("sync", None)] if want["closes"] else []) + [("finished", None)]
    if tail != expected_tail:
        fail("the consumed manifest was not closed as expected")


def check_crash(attempt, want, oracle, fail):
    if attempt.get("signal") != "killed" or "result" in attempt:
        fail("the attempt was not killed at its crash point")
    events = attempt.get("events") or []
    rows = ops(events)
    if not events or events[-1].get("op") != "crash" or events[-1].get("point") != want["point"]:
        fail("the last event is not the expected crash")
        return
    if rows[-2:-1] != [("sealed", None)] or check_seal_events(rows, False, len(item_names(oracle)), fail) is None:
        fail("the crash did not follow a completed seal")


def check_control(attempt, oracle, fail):
    result = attempt.get("result") or {}
    poisoned = [event for event in attempt.get("events") or [] if event.get("op") == "direct_poison"]
    if oracle["control_errors"]["blockhash"] not in str(result.get("import_error")) or \
            result.get("head_number") != oracle["target"] - 1 or \
            [event.get("number") for event in poisoned] != oracle["requested_numbers"]:
        fail("the poisoned control was not rejected by native validation")


def compare(observation, oracle, commit):
    """Return the differences between one probe observation and the oracle."""
    failures = []
    target = oracle["target"]
    if observation.get("family") != oracle["family"] or observation.get("observation") != oracle["observation"] or \
            observation.get("target") != target or len(observation.get("generated_hashes", [])) != target + 1 or \
            len(observation.get("generated_transactions", [])) != target + 1:
        return ["the observation does not describe the oracle chain"]
    scenarios = {row.get("name"): row.get("attempts") for row in observation.get("scenarios", [])}
    if set(scenarios) != set(oracle["scenarios"]) or len(scenarios) != len(observation["scenarios"]):
        return ["scenario set differs from the oracle"]
    for name, wants in oracle["scenarios"].items():
        attempts = scenarios[name] or []
        if [row.get("name") for row in attempts] != [want["name"] for want in wants]:
            failures.append(f"{name}: attempts differ from the oracle")
            continue
        for attempt, want in zip(attempts, wants):
            def fail(message, label=f"{name}/{want['name']}"):
                failures.append(f"{label}: {message}")
            if want["kind"] == "complete":
                check_complete(attempt, want, observation, oracle, commit, fail)
            elif want["kind"] == "crash":
                check_crash(attempt, want, oracle, fail)
            elif want["kind"] == "control":
                check_control(attempt, oracle, fail)
            else:
                fail(f"unknown attempt kind {want['kind']!r}")
    return failures


def checked_compare(observation, oracle, commit):
    """Compare, turning a malformed observation into a failure instead of an exception."""
    try:
        return compare(observation, oracle, commit)
    except (AttributeError, IndexError, KeyError, TypeError, ValueError) as error:
        return [f"malformed observation: {type(error).__name__}: {error}"]


# ---------------------------------------------------------------- preparation


def install(source, fixture_dir, shared_dir):
    """Add the hook call, the hook file, the shared package and the probe to a Geth tree."""
    path = source / ANCHOR_FILE
    before = path.read_text()
    require(before.count(ANCHOR) == 1, "State processor anchor must occur exactly once")
    require("extractorGetHash" not in before, "State processor already has the hook")
    after = before.replace(ANCHOR, ANCHOR + HOOK_CALL)
    path.write_text(after)
    rows = []
    for name, relative in TREE_FILES.items():
        origin = (shared_dir if name in SHARED_FILES else fixture_dir) / name
        target = source / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        # copy_artifact opens the target with O_EXCL, so an existing native path is refused.
        rows.append({"relative": relative, "copy": copy_artifact(origin, target, reference(origin, 4 * 1024 * 1024))})
    return {"file": ANCHOR_FILE, "sha256_before": hashlib.sha256(before.encode()).hexdigest(),
            "sha256_after": hashlib.sha256(after.encode()).hexdigest(), "inserted": HOOK_CALL, "files": rows}


def apply_mutant(source, mutant):
    """Apply one native mutant to the installed tree; return a function that restores it."""
    relative = TREE_FILES[mutant["file"]]
    path = source / relative
    before = path.read_text()
    require(before.count(mutant["original"]) == 1, f"Mutant {mutant['name']} must match exactly once")
    path.write_text(before.replace(mutant["original"], mutant["replacement"]))
    return lambda: path.write_text(before)


def go_environment():
    return {"CGO_ENABLED": "1", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64", "GOCACHE": "/work/build-cache",
            "GOMODCACHE": "/modules", "GOPATH": "/work/gopath", "GOFLAGS": "-mod=readonly", "GOPROXY": "off",
            "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOTMPDIR": "/work/gotmp", "GOWORK": "off"}


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
        record = {"schema_version": 1, "kind": "source-inputs-geth-preparation", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": state, "build_manifest": manifest_ref,
                  "public_profile_credit": False}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            record["fixture"] = capture_probe.copy_sources(FIXTURE_FILES, FIXTURE, output / "fixture", deadline)
            record["shared"] = capture_probe.copy_sources(SHARED_FILES, SHARED, output / "shared", deadline)
            record["harness_code"] = capture_probe.copy_sources(HARNESS_FILES, HERE, output / "harness-code", deadline)
            compiler = manifest["image_inputs"]["golang-build-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/golang@sha256:[0-9a-f]{64}", compiler), "Unpinned compiler")
            require(manifest["builds"]["geth"]["compiler_image"] == compiler, "Geth compiler mismatch")
            record["compiler_image"] = compiler
            closure = manifest["module_closures"]["geth"]
            require(closure["source_commit"] == manifest["builds"]["geth"]["source_commit"], "Source closure mismatch")
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
            fixture_copy, shared_copy = output / "fixture", output / "shared"
            record["install"] = install(source, fixture_copy, shared_copy)
            for name in ("bin", "build-cache", "gopath", "gotmp"):
                (directory / name).mkdir()
            mounts = [(directory, "/work", True), (root / "cache" / "geth" / "mod", "/modules", True)]
            mounts += [(directory / name, f"/work/{name}", False) for name in ("bin", "build-cache", "gopath", "gotmp")]
            go = "/usr/local/go/bin/go"
            native.command(compiler, [go, "mod", "verify"], mounts, cap=300, memory="4g", cwd="/work/source",
                           environment=go_environment())
            vet = native.command(compiler, [go, "vet", "./cmd/extractor-inputs-probe", "./extractor/source_inputs"],
                                 mounts, cap=900, memory="4g", cwd="/work/source", environment=go_environment())
            record["vet"] = reference(vet)
            # The shared unit tests compiled inside the native module with the accepted compiler.
            tests = native.command(compiler, [go, "test", "-race", "-count=1", "-cover", "-v", "./extractor/source_inputs"],
                                   mounts, cap=900, memory="4g", cwd="/work/source", environment=go_environment())
            record["unit_tests"] = reference(tests)
            require(b"\nok  \tgithub.com/ethereum/go-ethereum/extractor/source_inputs" in b"\n" + tests.read_bytes(),
                    "Shared unit tests did not pass inside the native module")
            build = [go, "build", "-p", "2", "-trimpath", "-o"]
            native.command(compiler, build + ["/work/bin/probe", "./cmd/extractor-inputs-probe"], mounts, cap=1200,
                           memory="4g", cwd="/work/source", environment=go_environment())
            record["binary"] = reference(directory / "bin/probe", deadline=deadline)
            _, table = json_artifact([row["copy"] for row in record["fixture"]
                                      if row["copy"]["path"].endswith("/native_mutants.json")][0], deadline=deadline)
            record["mutants"] = {}
            for mutant in table["mutants"]:
                restore = apply_mutant(source, mutant)
                try:
                    # Every mutant must compile: a failed build fails the preparation.
                    native.command(compiler, build + [f"/work/bin/mutant-{mutant['name']}", "./cmd/extractor-inputs-probe"],
                                   mounts, cap=1200, memory="4g", cwd="/work/source", environment=go_environment())
                finally:
                    restore()
                record["mutants"][mutant["name"]] = reference(directory / f"bin/mutant-{mutant['name']}",
                                                              deadline=deadline)
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
    require(preparation.get("kind") == "source-inputs-geth-preparation" and preparation.get("result") == "PASS" and
            preparation.get("cleanup_complete") is True, "A passing preparation is required")
    manifest_root = Path(preparation["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (preparation_path.parent, manifest_root, ROOT))
    state = capture_probe.source_state()
    with single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "source-inputs-geth-run", "result": "FAIL", "run_id": uuid.uuid4().hex,
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
            binary = artifact(preparation["binary"], BINARY_LIMIT, deadline=deadline)
            artifact(preparation["genesis"], 1024 * 1024, deadline=deadline)
            mutants = {name: artifact(ref, BINARY_LIMIT, deadline=deadline) for name, ref in preparation["mutants"].items()}
            require(mutants, "The preparation built no mutants")
            record["phases"]["execution_started"] = time.time()

            def execute(name, path):
                directory = output / name
                directory.mkdir()
                begun = time.monotonic()
                native.command(runtime, [f"/probe/{path.name}", "run", "/probe/genesis.json", "/out/work",
                                         "/out/probe.json", commit],
                               [(binary.parent, "/probe", True), (directory, "/out", False)], cap=300, memory="1g")
                failures = checked_compare(load(directory / "probe.json", 16 * 1024 * 1024, deadline), oracle, commit)
                return {"observation": reference(directory / "probe.json", 16 * 1024 * 1024, deadline),
                        "failures": failures, "elapsed_seconds": time.monotonic() - begun}

            record["probe"] = execute("probe", binary)
            record["probe"]["result"] = "PASS" if not record["probe"]["failures"] else "FAIL"
            for name, path in mutants.items():
                row = execute(f"mutant-{name}", path)
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
    args = parser.parse_args(argv)
    try:
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
