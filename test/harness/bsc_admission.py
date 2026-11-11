#!/usr/bin/env python3
"""Prepare, run and mutate the BSC block of E01-02.admission in owned containers.

`prepare` applies the extractor patch set (adapters/bsc/admission/native/patches)
to the accepted BSC source tree with a strict unified-diff applier, installs
the shared packages (source_inputs, admission, state_provider) and the BSC
package (adapters/bsc/admission/native/go) under extractor/, checks the format,
vets the patched and installed packages, compiles the probe test binary and
builds the patched geth binary with the accepted offline compiler, and writes
a tool manifest that the caller must pin.

`run` executes the probe binary against the accepted private BSC Parlia
fixture, runs the patched geth on a probe home that holds live pins, and checks
the report and the event traces with its own rules: every capsule, admitted,
consumed, EXEC_READY and NATIVE_REJECTED digest is recomputed from the fixture
and the README formulas, the trace order shows that nothing executes before
the closing ADMITTED sync, and each crash point of AC-CAP-02-01 and
AC-CAP-02-02 recovers as the admission README requires. Private local evidence
gives no public-profile credit and no AC verdict.

`mutants` runs single-check mutants of the BSC package and of the patched
native files from a table inside a fresh copy of the accepted BSC tree.
"""

import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import signal
import sys
import time
import uuid

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE))
from artifacts import (Rejected, artifact, check_deadline, copy_artifact, load, pinned_json, read_bytes, reference,
                       require, source_snapshot, sources_unchanged)
from native import Native
import admission
import bsc_source_inputs as probe14
import pins
import source_inputs

NATIVE = ROOT / "adapters" / "bsc" / "admission" / "native"
GO_DIR = NATIVE / "go"
PATCHES = NATIVE / "patches"
SHARED = ((ROOT / "adapters/bsc/source_inputs/inputs.go", "extractor/source_inputs/inputs.go"),
          (ROOT / "adapters/bsc/admission/admission.go", "extractor/admission/admission.go"),
          (ROOT / "adapters/bsc/admission/canonical.go", "extractor/admission/canonical.go"),
          (ROOT / "adapters/bsc/state_provider/provider.go", "extractor/state_provider/provider.go"),
          (ROOT / "adapters/bsc/state_provider/family.go", "extractor/state_provider/family.go"))
VET = ("./extractor/...", "./core", "./core/vm", "./core/rawdb", "./core/state/pruner", "./consensus/parlia", "./eth",
       "./cmd/geth")
HARNESS = ("bsc_admission.py", "bsc_source_inputs.py", "admission.py", "pins.py", "artifacts.py", "native.py",
           "owned.py", "source_inputs.py", "profile_manifest.py")
CHILD_TEST = "TestNativeBSCAdmissionChild"
CRASH_MODES = ("before-admitted-put-drop", "admitted-unsynced-drop", "admitted-unsynced-keep", "admitted-synced",
               "first-handoff", "exec-ready-unsynced-drop")
# Whether block 2 is ADMITTED after the restart of each crash point.
ADMITTED_AFTER_RESTART = {"before-admitted-put-drop": False, "admitted-unsynced-drop": False,
                          "admitted-unsynced-keep": True, "admitted-synced": True, "first-handoff": True,
                          "exec-ready-unsynced-drop": True}
TRACES = ("live", "reject") + CRASH_MODES
PRODUCER, RUN = "bsc-probe", "run-1"
STORE_ID = "0123456789abcdef0123456789abcdef"
# Independent copies of the BSC package's declared constants and tags.
STATE_PROFILE = b"state.scheme=hash;gcmode=full;snapshots=off"
NORMALIZATION_PROFILE = b"test-only-probe-v1"
EXEC_TAG = b"extractor-bsc-exec-result-v1\x00"
REJECTION_TAG = b"extractor-bsc-rejection-v1\x00"
GET_TAG = b"bsc-parlia-db-get-v1\x00"
SNAPSHOT_PREFIX = b"parlia-"
CHECKPOINT_INTERVAL = 1024
BLOCKHASH_WINDOW = 256
ITEM_NAMES = ("block-rlp", "parent-hash", "parent-header-rlp", "parent-root", "ancestor-headers-rlp", "genesis-json",
              "fork-id", "parlia-snapshot-keys", "native-commit", "normalization-profile", "state-profile")
SYSTEM_TXS_BLOCK_1 = 9  # fixture ORACLE: seven init calls and two fee transfers
PRUNE_REFUSAL = "live pins forbid this native deletion"
START_REFUSAL = "Failed to register the Ethereum service: live pins forbid this native deletion"
PREPARATION_SECONDS = 2 * 3600
RUN_SECONDS = 1800
MUTANT_SECONDS = 6 * 3600
BUILD_CAP = 3000
TEST_CAP = 900


# ---- strict unified diffs ----

HUNK = re.compile(r"^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@")


def parse_patch(text):
    """Return [(path, is_new, hunks)] of a unified diff; each hunk is (old_start, old_lines, new_lines)."""
    files, lines, index = [], text.split("\n"), 0
    require(text.endswith("\n"), "A patch must end with a newline")
    while index < len(lines):
        line = lines[index]
        if not line.startswith("--- "):
            index += 1
            continue
        require(index + 1 < len(lines) and lines[index + 1].startswith("+++ b/"), "Patch file header is malformed")
        old, new = line[4:], lines[index + 1][6:]
        require(old in ("/dev/null", "a/" + new), f"Patch renames a file: {old} -> {new}")
        require(not new.startswith("/") and ".." not in Path(new).parts, f"Unsafe patch path {new}")
        hunks, index = [], index + 2
        while index < len(lines) and lines[index].startswith("@@"):
            match = HUNK.match(lines[index])
            require(match, f"Malformed hunk header in {new}")
            old_count = int(match.group(2) or 1)
            new_count = int(match.group(4) or 1)
            old_lines, new_lines, index = [], [], index + 1
            while len(old_lines) < old_count or len(new_lines) < new_count:
                require(index < len(lines), f"Truncated hunk in {new}")
                body = lines[index]
                require(body[:1] in (" ", "-", "+"), f"Unsupported hunk line in {new}: {body[:40]!r}")
                if body[0] in " -":
                    old_lines.append(body[1:] + "\n")
                if body[0] in " +":
                    new_lines.append(body[1:] + "\n")
                index += 1
            hunks.append((int(match.group(1)), old_lines, new_lines))
        require(hunks, f"No hunk for {new}")
        files.append((new, old == "/dev/null", hunks))
    require(files, "Empty patch")
    return files


def apply_patch(source, text):
    """Apply a parsed diff with exact context and no fuzz; return the changed files with digests."""
    rows = []
    for path, is_new, hunks in parse_patch(text):
        target = source / path
        if is_new:
            require(not target.exists(), f"Patch creates an existing file {path}")
            require(len(hunks) == 1 and hunks[0][0] == 0 and not hunks[0][1], f"New file {path} must be one hunk")
            original = []
        else:
            require(target.is_file() and not target.is_symlink(), f"Patch target {path} is missing")
            original = target.read_text().splitlines(keepends=True)
        result, cursor = [], 0
        for start, old_lines, new_lines in hunks:
            begin = start - 1 if old_lines else start
            require(begin >= cursor, f"Overlapping hunks in {path}")
            require(original[begin:begin + len(old_lines)] == old_lines, f"Patch context differs in {path} at {start}")
            result += original[cursor:begin] + new_lines
            cursor = begin + len(old_lines)
        result += original[cursor:]
        before = hashlib.sha256("".join(original).encode()).hexdigest() if not is_new else None
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text("".join(result))
        rows.append({"path": path, "new": is_new, "before_sha256": before,
                     "after_sha256": hashlib.sha256("".join(result).encode()).hexdigest()})
    return rows


def patch_files():
    paths = sorted(PATCHES.glob("*.diff"))
    require(paths, "No patch files")
    return paths


def bsc_files():
    """The BSC package files and their install paths."""
    rows = [(path, "extractor/bsc/" + path.name) for path in sorted(GO_DIR.glob("*.go"))]
    rows += [(path, "extractor/bsc/retention/" + path.name) for path in sorted((GO_DIR / "retention").glob("*.go"))]
    require(rows, "No BSC package files")
    return rows


def install(source, destination_root, deadline):
    """Patch the native tree and install the packages; never replace a native file."""
    require(not (source / "extractor").exists(), "extractor already exists in the native tree")
    patched = []
    for path in patch_files():
        expected = reference(path, 1024 * 1024, deadline)
        retained = destination_root / "patches" / path.name
        retained.parent.mkdir(parents=True, exist_ok=True)
        copy_artifact(path, retained, expected, 1024 * 1024, deadline)
        patched.append({"patch": expected, "files": apply_patch(source, retained.read_text())})
    installed = []
    for origin, relative in SHARED + tuple(bsc_files()):
        expected = reference(origin, 1024 * 1024, deadline)
        target = source / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        retained = destination_root / "installed" / relative
        retained.parent.mkdir(parents=True, exist_ok=True)
        copy_artifact(origin, retained, expected, 1024 * 1024, deadline)
        copy_artifact(origin, target, expected, 1024 * 1024, deadline)
        installed.append({"relative": relative, "source": expected,
                          "retained": reference(retained, 1024 * 1024, deadline)})
    return patched, installed


def chain_config_forks(source):
    """Mirror forkid.gatherForks of the pinned source from params/config.go.

    gatherForks takes every ChainConfig field whose Go name ends in Block or
    Time: a *big.Int value is a block fork and a *uint64 value is a time fork.
    The result maps each such field's JSON key to "block" or "time", and lists
    every JSON key of ChainConfig, so a genesis config with an unknown key is
    refused instead of being read differently from Go.
    """
    text = (source / "params" / "config.go").read_text()
    match = re.search(r"\ntype ChainConfig struct \{\n(.*?)\n\}\n", text, re.S)
    require(match, "ChainConfig not found in params/config.go")
    forks, keys = {}, []
    for line in match.group(1).splitlines():
        field = re.match(r"\t(\w+)\s+(\S+)\s+`json:\"([^\",]+)[^`]*`", line)
        if not field:
            continue
        name, kind, key = field.groups()
        keys.append(key)
        if name.endswith(("Block", "Time")):
            if kind == "*big.Int":
                forks[key] = "block"
            elif kind == "*uint64":
                forks[key] = "time"
    require(forks and len(keys) == len(set(keys)), "ChainConfig fork table is empty or ambiguous")
    return {"forks": forks, "keys": sorted(keys)}


def prepare(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    require(manifest_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    manifest_ref = reference(manifest_path, 64 * 1024 * 1024, deadline)
    require(manifest_ref["sha256"] == expected_sha, "BuildManifest digest mismatch")
    manifest = load(manifest_path, 64 * 1024 * 1024, deadline)
    source_inputs.check_identical(ROOT)
    admission.check_identical(ROOT)
    require(not output.is_relative_to(manifest_path.parent), "Preparation cannot write into accepted build storage")
    proof = {"schema_version": 1, "kind": "bsc-admission-preparation", "result": "FAIL", "build_manifest": manifest_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": PREPARATION_SECONDS, "runtime": "NOT_RUN", "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS, deadline)
        root = output / "bsc"
        root.mkdir()
        closure, copied, source = probe14.extract_source(manifest, manifest_path, root, deadline)
        forks = chain_config_forks(source)
        patched, installed = install(source, root, deadline)
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (root / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            copy_artifact(source / name, root / "metadata" / name, reference(source / name, deadline=deadline),
                          deadline=deadline)
        compiler = manifest["builds"]["bsc"]["compiler_image"]
        require(compiler == manifest["image_inputs"]["golang-build-image"]["reference"], "Unexpected compiler image")
        mounts = probe14.build_mounts(source, manifest_path.parent / "cache" / "bsc" / "mod", root)
        build(runner, compiler, mounts, "bsc.test", "geth", sorted({row["path"] for item in patched
                                                                     for row in item["files"]}))
        sources_unchanged(origins, deadline)
        for row in installed:
            require(reference(Path(row["source"]["path"]), 1024 * 1024, deadline) == row["source"],
                    "Installed source changed during preparation")
        tests = probe14.test_names([Path(row["retained"]["path"]) for row in installed
                                    if row["relative"].startswith("extractor/bsc/") and
                                    row["relative"].endswith("_test.go")])
        tool = {"schema_version": 1, "kind": "bsc-admission-tool", "build_manifest": manifest_ref,
                "native_source_commit": closure["source_commit"], "source_archive": copied, "compiler": compiler,
                "patched": patched, "installed": installed, "source_root": str(source), "chain_config": forks,
                "tests": [name for name in tests if name != CHILD_TEST],
                "test_binary": reference(root / "generated/bsc.test", deadline=deadline),
                "geth_binary": reference(root / "generated/geth", deadline=deadline),
                "metadata": [reference(root / "metadata" / name, deadline=deadline) for name in ("go.mod", "go.sum")]}
        pins.save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "preparation.json", body)


def build(runner, compiler, mounts, test_binary, geth_binary, patched_paths, check_format=True):
    """gofmt (optional), vet, compile the probe binary and, if named, build geth; a failure raises Rejected."""
    env = pins.environment()
    if check_format:
        stdout = runner.command(compiler, ["/usr/local/go/bin/gofmt", "-l", "extractor", *patched_paths], mounts,
                                cap=300, memory="2g", cwd="/source", environment=env)
        require(not read_bytes(stdout, 1024 * 1024).strip(), "gofmt reports unformatted files")
        runner.command(compiler, ["/usr/local/go/bin/go", "vet", *VET], mounts, cap=BUILD_CAP, memory="6g",
                       cwd="/source", environment=env)
    runner.command(compiler, ["/usr/local/go/bin/go", "test", "-c", "-trimpath", "-o", "/work/generated/" + test_binary,
                              "./extractor/bsc/"], mounts, cap=BUILD_CAP, memory="6g", cwd="/source", environment=env)
    if geth_binary:
        runner.command(compiler, ["/usr/local/go/bin/go", "build", "-trimpath", "-o", "/work/generated/" + geth_binary,
                                  "./cmd/geth"], mounts, cap=BUILD_CAP, memory="6g", cwd="/source", environment=env)


# ---- independent checks ----

def keccak(data):
    return probe14.keccak256(data)


def rlp_int(raw):
    return int.from_bytes(probe14.rlp_bytes(raw), "big")


def header_fields(raw):
    fields = probe14.rlp_list(raw)
    return {"hash": keccak(raw), "parent": probe14.rlp_bytes(fields[0]), "root": probe14.rlp_bytes(fields[3]),
            "receipts": probe14.rlp_bytes(fields[5]), "number": rlp_int(fields[8]), "time": rlp_int(fields[11])}


def fork_id(genesis_canonical, genesis_hash, number, time, chain_config):
    """EIP-2124 fork id with the forks that the pinned forkid.gatherForks gathers."""
    import zlib
    genesis = json.loads(genesis_canonical)
    config = genesis["config"]
    unknown = sorted(set(config) - set(chain_config["keys"]))
    require(not unknown, f"Genesis config has keys that ChainConfig does not know: {unknown}")
    blocks, times = set(), set()
    for key, kind in chain_config["forks"].items():
        if key in config:
            value = config[key]
            require(type(value) is int and value >= 0, f"Fork {key} is not a non-negative integer")
            (blocks if kind == "block" else times).add(value)
    genesis_time = int(genesis["timestamp"], 16)
    checksum = zlib.crc32(genesis_hash)
    for forks, head in ((sorted(b for b in blocks if b != 0), number), (sorted(t for t in times if t > genesis_time), time)):
        for fork in forks:
            if fork > head:
                return json.dumps({"hash": f"{checksum:08x}", "next": str(fork)}, separators=(",", ":")).encode()
            checksum = zlib.crc32(fork.to_bytes(8, "big"), checksum)
    return json.dumps({"hash": f"{checksum:08x}", "next": "0"}, separators=(",", ":")).encode()


def ancestor_floor(number):
    return min((number - 1) // CHECKPOINT_INTERVAL * CHECKPOINT_INTERVAL, max(0, number - BLOCKHASH_WINDOW))


def expected_items(number, headers, raw_blocks, genesis_canonical, native_commit, chain_config):
    """The static items of block `number` from the fixture bytes and the canonical genesis."""
    parent = headers[number - 1]
    ancestors = [headers[k] for k in range(number - 2, ancestor_floor(number) - 1, -1)] if number >= 2 else []
    checkpoint = (number - 1) // CHECKPOINT_INTERVAL * CHECKPOINT_INTERVAL
    snapshot = json.dumps({"checkpoint": checkpoint, "keys": [(SNAPSHOT_PREFIX + keccak(headers[checkpoint])).hex()]},
                          separators=(",", ":")).encode()
    info = header_fields(headers[number])
    return [("block-rlp", raw_blocks[number - 1]), ("parent-hash", keccak(parent)), ("parent-header-rlp", parent),
            ("parent-root", header_fields(parent)["root"]), ("ancestor-headers-rlp", probe14.rlp_encode_list(ancestors)),
            ("genesis-json", genesis_canonical),
            ("fork-id", fork_id(genesis_canonical, keccak(headers[0]), number, info["time"], chain_config)),
            ("parlia-snapshot-keys", snapshot), ("native-commit", native_commit.encode()),
            ("normalization-profile", NORMALIZATION_PROFILE), ("state-profile", STATE_PROFILE)]


def text(data):
    return len(data).to_bytes(8, "big") + data


def exec_digest(header, receipts):
    info = header_fields(header)
    data = EXEC_TAG + info["hash"] + info["root"] + info["receipts"] + len(receipts).to_bytes(8, "big")
    for receipt in receipts:
        data += text(receipt)
    return hashlib.sha256(data).hexdigest()


def rejection_evidence(block_hash, cause):
    return hashlib.sha256(REJECTION_TAG + block_hash + text(cause.encode())).hexdigest()


def candidate(headers, number):
    info, parent = header_fields(headers[number]), header_fields(headers[number - 1])
    return {"number": number, "hash": info["hash"].hex(), "parent_hash": info["parent"].hex(),
            "parent_root": parent["root"].hex()}


def key_of(sequence):
    return admission.observation_key(PRODUCER, RUN, sequence)


def records_by_sequence(scenario):
    out = {}
    for name, value in scenario["records"].items():
        if name in ("counter", "lowwater"):
            continue
        sequence, kind = name.split("/")
        out.setdefault(int(sequence, 16), {})[kind] = value
    return out


def attempt_handoffs(events, key):
    """The hand-offs of the last execution attempt of an observation, in order."""
    starts = [index for index, event in enumerate(events) if event.get("event") == "execute" and
              event.get("observation") == key]
    require(starts, f"{key}: no execution")
    rows = [event for event in events[starts[-1]:] if event.get("event") == "handoff" and event.get("observation") == key]
    require([row["sequence"] for row in rows] == list(range(len(rows))), f"{key}: hand-off sequence")
    return rows


def consumed(capsule_digest, rows):
    chain = source_inputs.chain_start(bytes.fromhex(capsule_digest))
    for sequence, row in enumerate(rows):
        request, response = bytes.fromhex(row["request_hex"]), bytes.fromhex(row["response_hex"])
        require(hashlib.sha256(response).hexdigest() == row["response_sha256"], "Hand-off digest")
        chain = source_inputs.chain_next(chain, source_inputs.entry_digest(sequence, request, response))
    return source_inputs.consumed_digest(bytes.fromhex(capsule_digest), len(rows), chain).hex()


def names(events):
    return [event["event"] + (":" + event["record"] if "record" in event else "") for event in events]


def check_order(events, key, scenario):
    """Nothing of an observation executes before the sync that follows its ADMITTED record."""
    label = f"{scenario}/{key}"
    index = {name: [i for i, e in enumerate(events) if e.get("observation") == key and
                    names([e])[0] == name] for name in ("begin", "pinned", "sealed", "put:admitted", "admitted",
                                                        "put:exec-ready", "exec-ready", "rejected", "put:closed")}
    executed = [i for i, e in enumerate(events) if e.get("observation") == key and
                e.get("event") in ("execute", "handoff", "system-tx")]
    # The closing ADMITTED sync: the first ADMITTED record directly followed
    # by a sync. A record that a crash cut off before its sync does not count.
    synced = [i for i in index["put:admitted"] if names(events[i + 1:i + 2]) == ["sync"]]
    if not synced:
        require(not executed, f"{label}: executed without a synced ADMITTED record")
        return
    admitted = synced[0]
    require(all(i > admitted + 1 for i in executed), f"{label}: execution before the closing ADMITTED sync")
    if index["begin"]:
        require(index["begin"][0] < index["pinned"][0] < index["sealed"][0] < admitted,
                f"{label}: begin, pin and seal are not in order before ADMITTED")
        capsule = [i for i, e in enumerate(events) if e.get("observation") == key and
                   e.get("record") in ("capsule", "capsule-chunk")]
        require(capsule and "sync" in names(events[capsule[-1]:admitted]), f"{label}: capsule not synced before ADMITTED")
    for put in index["put:exec-ready"]:
        follow = names(events[put + 1:put + 2])
        if follow == ["crash"]:
            continue
        require(follow == ["sync"], f"{label}: EXEC_READY record without its sync")
        closed = [i for i in index["put:closed"] if i < put]
        if closed:
            require("sync" in names(events[closed[-1]:put]), f"{label}: log closed without a sync before EXEC_READY")


def check_child_end(events, mode):
    """The child trace ends with the crash at its declared point."""
    require(events and events[-1].get("event") == "crash" and events[-1].get("point") == mode, f"{mode}: no crash")
    block2 = key_of(1)
    before = names(events[-3:-1])
    last = events[-2]
    if mode == "before-admitted-put-drop":
        require(not any(e.get("observation") == block2 and e.get("record") == "admitted" for e in events),
                f"{mode}: ADMITTED was written")
    elif mode == "admitted-unsynced-drop":
        require(names([last]) == ["put:admitted"] and last.get("observation") == block2, f"{mode}: {before}")
    elif mode == "admitted-unsynced-keep":
        # The keep model writes every record through at once and says so.
        require(before == ["put:admitted", "sync"] and last.get("model") == "keep" and
                events[-3].get("observation") == block2, f"{mode}: {before}")
    elif mode == "admitted-synced":
        require(before == ["put:admitted", "sync"], f"{mode}: {before}")
    elif mode == "first-handoff":
        require(last.get("event") == "handoff" and last.get("sequence") == 0 and last.get("observation") == block2,
                f"{mode}: {before}")
    elif mode == "exec-ready-unsynced-drop":
        require(names([last]) == ["put:exec-ready"] and last.get("observation") == block2, f"{mode}: {before}")


def evaluate(report, traces, raw_blocks, genesis_file, native_commit, genesis_hash, chain_config, prepared, geth):
    """Check the probe report and traces; return the summary."""
    scenarios = {row["name"]: row for row in report["scenarios"]}
    require(sorted(scenarios) == sorted(TRACES), f"Scenarios {sorted(scenarios)}")
    live = scenarios["live"]
    genesis_header = bytes.fromhex(live["extra"]["genesis_header_rlp"])
    require(keccak(genesis_header).hex() == genesis_hash.removeprefix("0x"), "Genesis header does not hash to the manifest")
    headers = [genesis_header] + [probe14.rlp_list(raw)[0] for raw in raw_blocks]
    for number in (1, 2):
        info = header_fields(headers[number])
        require(info["number"] == number and info["parent"] == keccak(headers[number - 1]), f"Fixture header {number}")
        require(info["hash"].hex() == prepared[number - 1]["hash"].removeprefix("0x"), f"Prepared hash {number}")
    canonical = admission.canonical(genesis_file)
    require(bytes.fromhex(live["extra"]["genesis_canonical_hex"]) == canonical, "Canonical genesis differs")
    items = {number: expected_items(number, headers, raw_blocks, canonical, native_commit, chain_config)
             for number in (1, 2)}
    capsule_digests = {number: source_inputs.capsule_digest(items[number]).hex() for number in (1, 2)}
    receipts = {1: [], 2: []}
    for row in live["extra"]["receipts"].split(","):
        number, encoded = row.split(":")
        receipts[int(number)].append(bytes.fromhex(encoded))
    execs = {number: exec_digest(headers[number], receipts[number]) for number in (1, 2)}
    alt_hash = bytes.fromhex(scenarios["reject"]["extra"]["alt_hash"].removeprefix("0x"))
    summary = {"capsule_digests": capsule_digests, "exec_digests": execs, "scenarios": {}}
    for name, scenario in scenarios.items():
        # A crash scenario's trace is the child's events, then the restart.
        events = traces[name] if name not in CRASH_MODES else (
            traces["child-" + name] + [{"event": "restart"}] + traces[name])
        rows = records_by_sequence(scenario)
        result = {"exec_ready": {}, "rejected": [], "abandoned": []}
        for sequence, kinds in sorted(rows.items()):
            key = key_of(sequence)
            prep = kinds["preparing"]
            require(prep["observation"] == key and prep["sequence"] == sequence, f"{name}/{key}: preparing record")
            number = prep["candidate"]["number"]
            if "abandoned" in kinds:
                require(set(kinds) == {"preparing", "abandoned"} and key not in scenario["pins"],
                        f"{name}/{key}: abandoned with other records or a pin")
                result["abandoned"].append(number)
                check_order(events, key, name)
                continue
            admitted = kinds["admitted"]
            if "rejected" in kinds:
                require(prep["candidate"]["hash"] == alt_hash.hex() and key not in scenario["pins"],
                        f"{name}/{key}: rejected candidate or pin")
                cause = [e for e in events if e.get("event") == "rejected" and e.get("observation") == key]
                require(len(cause) == 1, f"{name}/{key}: rejection event")
                evidence = rejection_evidence(alt_hash, cause[0]["cause"])
                record = kinds["rejected"]
                require(record["evidence_digest"] == evidence and
                        record["digest"] == admission.rejected_digest(admitted["digest"], evidence),
                        f"{name}/{key}: NATIVE_REJECTED digest")
                result["rejected"].append(cause[0]["cause"])
                check_order(events, key, name)
                continue
            require(prep["candidate"] == candidate(headers, number), f"{name}/{key}: candidate")
            capsule = scenario["capsules"][key]
            refs = [{"name": n, "length": len(d), "sha256": hashlib.sha256(d).hexdigest()} for n, d in items[number]]
            require(capsule == {"observation": key, "items": refs, "digest": capsule_digests[number]},
                    f"{name}/{key}: capsule differs from the fixture items")
            require(admitted["capsule_digest"] == capsule_digests[number], f"{name}/{key}: admitted capsule digest")
            require(admitted["digest"] == admission.admitted_digest(key, sequence, prep["candidate"],
                                                                    admitted["pin_digest"], capsule_digests[number]),
                    f"{name}/{key}: admitted digest")
            require("exec-ready" in kinds, f"{name}/{key}: no EXEC_READY")
            record = kinds["exec-ready"]
            pin = bytes.fromhex(scenario["pins"][key])
            require(hashlib.sha256(pin).hexdigest() == admitted["pin_digest"], f"{name}/{key}: pin digest")
            pin_record = json.loads(pin)
            expected_headers = [{"hash": "0x" + keccak(headers[number - 1]).hex(), "number": number - 1}]
            expected_headers += [{"hash": "0x" + keccak(headers[k]).hex(), "number": k}
                                 for k in range(number - 2, ancestor_floor(number) - 1, -1)] if number >= 2 else []
            require(pin_record["id"] == key and pin_record["parent_root"] == "0x" + prep["candidate"]["parent_root"] and
                    pin_record["headers"] == expected_headers and
                    pin_record["profile"] == hashlib.sha256(STATE_PROFILE).hexdigest() and
                    pin_record["store"]["store_id"] == STORE_ID, f"{name}/{key}: pin record")
            handoffs = attempt_handoffs(events, key)
            declared = {bytes.fromhex(k) for k in json.loads(dict(items[number])["parlia-snapshot-keys"])["keys"]}
            for row in handoffs:
                request = bytes.fromhex(row["request_hex"])
                require(request.startswith(GET_TAG) and request[len(GET_TAG):] in declared,
                        f"{name}/{key}: read of an undeclared key")
            consumed_digest = consumed(capsule_digests[number], handoffs)
            require(record["consumed_digest"] == consumed_digest and record["exec_digest"] == execs[number] and
                    record["admitted_digest"] == admitted["digest"] and
                    record["digest"] == admission.exec_ready_digest(admitted["digest"], consumed_digest, execs[number]),
                    f"{name}/{key}: EXEC_READY digests")
            result["exec_ready"][number] = {"key": key, "consumed": consumed_digest, "digest": record["digest"],
                                            "responses": len(handoffs)}
            check_order(events, key, name)
        require(sorted(result["exec_ready"]) == [1, 2], f"{name}: EXEC_READY blocks {sorted(result['exec_ready'])}")
        require(sorted(scenario["pins"]) == sorted(row["key"] for row in result["exec_ready"].values()),
                f"{name}: live pins are not exactly the EXEC_READY observations")
        require(scenario["head_number"] == 2 and scenario["head_hash"].removeprefix("0x") == prepared[1]["hash"].removeprefix("0x"),
                f"{name}: head")
        require(scenario["safe_points_outside"] == 0 and scenario["system_txs_outside"] == 0,
                f"{name}: safe points or system transactions outside an observation")
        result["consumed_block_2"] = result["exec_ready"][2]["consumed"]
        summary["scenarios"][name] = result
    # The same inputs give the same consumed manifest in every scenario.
    require(len({row["consumed_block_2"] for row in summary["scenarios"].values()}) == 1,
            "Block 2 consumed digests differ between scenarios")
    block1 = live["records"][f"{0:016x}/exec-ready"]
    exec_events = [e for e in traces["live"] if e.get("event") == "exec-ready"]
    require([e["safe_points"] > 0 for e in exec_events] == [True, False] and
            [e["digest"] for e in exec_events][0] == block1["digest"], "Safe points by observation")
    require(live["system_txs"] == {key_of(0): SYSTEM_TXS_BLOCK_1}, f"System transactions {live['system_txs']}")
    extra = live["extra"]
    require("RECOVERY_ONLY" in extra["import_before_recovery"], "Live import before recovery was not refused")
    require("outside an admitted observation" in extra["unobserved_execution"], "Unobserved execution was not refused")
    require(PRUNE_REFUSAL in extra["rewind_below_pin"], "Rewind below a pin was not refused")
    require("saved producer run differs" in extra["foreign_run"], "Foreign run was not refused")
    require("identity mismatch" in extra["preflight_other_store"], "Preflight of another store was not refused")
    require(extra["restart_observations"] == "2", "Restart does not list the EXEC_READY observations")
    require("outside the sequential import profile" in extra["not_on_head"], "A block off the head was admitted")
    require(extra["known_block"] == ";2", "A known block was observed again")
    reject = summary["scenarios"]["reject"]
    require(len(reject["rejected"]) == 1 and scenarios["reject"]["import_errors"][1], "Reject scenario")
    for mode in CRASH_MODES:
        scenario = scenarios[mode]
        check_child_end(traces["child-" + mode], mode)
        before = {(row["candidate"]["number"], row["state"]) for row in scenario["observations_after_restart"]}
        require((1, "EXEC_READY") in before and (2, "EXEC_READY") not in before, f"{mode}: states after restart {before}")
        admitted = (2, "ADMITTED") in before
        require(admitted == ADMITTED_AFTER_RESTART[mode], f"{mode}: block 2 ADMITTED after restart: {admitted}")
        require(admitted or (2, "INPUT_PREPARING") in before, f"{mode}: block 2 lost its INPUT_PREPARING record")
        require(scenario["mode_at_start"] == "RECOVERY_ONLY" and "pending" in scenario["go_live_error"],
                f"{mode}: GoLive was not refused")
        if admitted:
            require("unresolved" in scenario["alt_block_error"], f"{mode}: a new candidate passed the obligation")
            require(summary["scenarios"][mode]["exec_ready"][2]["key"] == key_of(1), f"{mode}: another obligation")
        else:
            require(summary["scenarios"][mode]["abandoned"] == [2], f"{mode}: INPUT_PREPARING not abandoned")
    require(summary["scenarios"]["first-handoff"]["exec_ready"][2]["responses"] >= 1, "First hand-off scenario")
    require(geth["prune_exit"] != 0 and PRUNE_REFUSAL in geth["prune_output"], "geth prune-state was not refused")
    require(geth["start_exit"] == 0 and "2" in geth["start_output"].splitlines(), "geth did not start on pins")
    require(geth["bloom_exit"] != 0 and START_REFUSAL in geth["bloom_output"],
            "geth started with a pending prune and live pins")
    return summary


def checked_evaluate(*args):
    """Evaluate, turning a malformed report into a rejection instead of an exception."""
    try:
        return evaluate(*args)
    except Rejected:
        raise
    except (KeyError, TypeError, ValueError, AttributeError, IndexError, StopIteration) as error:
        raise Rejected(f"Malformed probe report: {type(error).__name__}: {error}") from None


GETH_SCRIPT = ("/tool/geth {args} > /work/geth-{name}.out 2>&1; echo $? > /work/geth-{name}.exit")
GETH_FLAGS = "--datadir /work/geth-home --nodiscover --maxpeers 0 --port 0 --ipcdisable"


def geth_checks(tool, root, runner, deadline, geth_binary):
    """Run the patched geth on a copy of the live probe home, which holds two live pins."""
    home = root / "work" / "geth-home"
    shutil.copytree(root / "work" / "home-live", home, symlinks=True)
    mounts = [(geth_binary.parent, "/tool", True), (root / "work", "/work", False), (root / "temp", "/scratch", False)]
    env = {"HOME": "/scratch", "TMPDIR": "/scratch"}
    result = {}
    steps = (("prune", "snapshot prune-state --datadir /work/geth-home"),
             ("start", f"{GETH_FLAGS} --exec eth.blockNumber console"))
    if geth_binary.name != "geth":
        shutil.copyfile(geth_binary, geth_binary.with_name("geth"))
    for name, args in steps + (("bloom", f"{GETH_FLAGS} --exec eth.blockNumber console"),):
        if name == "bloom":
            (home / "geth" / ("statebloom.0x" + "00" * 31 + "01.bf.gz")).write_bytes(b"")
        runner.command(tool["compiler"], ["/bin/sh", "-c", GETH_SCRIPT.format(args=args, name=name)], mounts, cap=300,
                       memory="2g", cwd="/scratch", environment=env)
        result[name + "_exit"] = int(read_bytes(root / "work" / f"geth-{name}.exit", 64, deadline).decode().strip())
        result[name + "_output"] = read_bytes(root / "work" / f"geth-{name}.out", 4 * 1024 * 1024, deadline).decode(
            errors="replace")
    return result


def probe_run(tool, fixture, genesis_hash, output, runner, deadline, binary=None, geth_binary=None):
    """Run the probe binary and the geth checks once and check them; return the family record."""
    root = output / "bsc"
    for name in ("inputs", "work", "temp", "tool"):
        (root / name).mkdir(parents=True)
    inputs = {name: copy_artifact(Path(ref["path"]), root / "inputs" / name, ref, 64 * 1024 * 1024, deadline)
              for name, ref in fixture["inputs"].items()}
    binary = binary or artifact(tool["test_binary"], 1024 * 1024 * 1024, deadline=deadline)
    geth_binary = geth_binary or artifact(tool["geth_binary"], 1024 * 1024 * 1024, deadline=deadline)
    mounts = [(binary.parent, "/tool", True), (root / "inputs", "/inputs", True), (root / "work", "/work", False),
              (root / "temp", "/scratch", False)]
    env = {"EXTRACTOR_ADMISSION_INPUTS": "/inputs", "EXTRACTOR_ADMISSION_WORK": "/work",
           "EXTRACTOR_ADMISSION_NATIVE_COMMIT": tool["native_source_commit"], "TMPDIR": "/scratch", "HOME": "/scratch"}
    started = time.monotonic()
    stdout = runner.command(tool["compiler"], ["/tool/" + binary.name, "-test.v", "-test.count=1", "-test.run", "^Test",
                                               "-test.skip", f"^{CHILD_TEST}$", "-test.timeout", f"{TEST_CAP - 60}s"],
                            mounts, cap=TEST_CAP, memory="2g", cwd="/scratch", environment=env)
    elapsed = round(time.monotonic() - started, 3)
    passed = pins.parse_go_test(read_bytes(stdout, 8 * 1024 * 1024, deadline).decode(), tool["tests"])
    geth = geth_checks(tool, root, runner, deadline, geth_binary)
    traces = {name: probe14.read_jsonl(root / "work" / f"trace-{name}.jsonl", deadline) for name in TRACES}
    # A crash trace holds the child's events, the restart marker and the parent's events.
    for mode in CRASH_MODES:
        events = traces[mode]
        marks = [index for index, event in enumerate(events) if event.get("event") == "restart"]
        require(len(marks) == 1, f"{mode}: restart marker")
        traces["child-" + mode], traces[mode] = events[:marks[0]], events[marks[0] + 1:]
    raw_blocks = probe14.split_rlp_list_items(read_bytes(root / "inputs/blocks.rlp", 1024 * 1024, deadline))
    require(len(raw_blocks) == 2, "Fixture must hold two blocks")
    genesis = read_bytes(root / "inputs/genesis.json", 4 * 1024 * 1024, deadline)
    report_path = root / "work/report.json"
    summary = checked_evaluate(load(report_path, 16 * 1024 * 1024, deadline), traces, raw_blocks, genesis,
                               tool["native_source_commit"], genesis_hash, tool["chain_config"], fixture["blocks"], geth)
    return {"inputs": inputs, "passed": passed, "elapsed_seconds": elapsed,
            "report": reference(report_path, 16 * 1024 * 1024, deadline), "stdout": reference(stdout, deadline=deadline),
            "traces": {name: reference(root / "work" / f"trace-{name}.jsonl", deadline=deadline) for name in TRACES},
            "geth": {name: value for name, value in geth.items() if name.endswith("_exit")},
            "geth_outputs": {name: reference(root / "work" / f"geth-{name}.out", deadline=deadline)
                             for name in ("prune", "start", "bloom")},
            "summary": summary}


def run(tool_path, tool_sha, manifest_path, manifest_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(tool_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "bsc-admission-tool", "Not a BSC admission tool")
    fixture, genesis_hash = probe14.load_inputs(manifest_path, manifest_sha, tool, deadline)
    proof = {"schema_version": 1, "kind": "bsc-admission-run", "result": "FAIL", "tool": tool_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": RUN_SECONDS, "public_profile_credit": False, "ac_verdict": None, "error": None,
             "fixture": {key: value for key, value in fixture.items() if key != "build_root"}}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS, deadline)
        proof["phases"] = {"execution_started": time.time()}
        proof["bsc"] = probe_run(tool, fixture, genesis_hash, output, runner, deadline)
        proof["phases"]["execution_finished"] = time.time()
        sources_unchanged(origins, deadline)
        artifact(tool["test_binary"], 1024 * 1024 * 1024, deadline=deadline)
        artifact(tool["geth_binary"], 1024 * 1024 * 1024, deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "run.json", body)


# ---- mutants ----

def load_table(path, source=None):
    """Mutant rows target a BSC package file ("go/<name>") or a patched native file ("native/<path>")."""
    table = json.loads(path.read_text())
    require(table.get("schema_version") == 1 and table.get("language") == "go-native-bsc-admission",
            "Unsupported mutant table")
    rows = table["mutants"]
    names_ = [row["name"] for row in rows]
    require(names_ and len(names_) == len(set(names_)), "Mutant names must be unique")
    for row in rows:
        require(set(row) <= {"name", "file", "original", "replacement", "equivalent", "geth"},
                f"Mutant {row['name']} has an unsupported shape")
        require(row["file"].startswith(("go/", "native/")) and ".." not in Path(row["file"]).parts,
                f"Mutant {row['name']} file {row['file']}")
        require(row["original"] != row["replacement"], f"Mutant {row['name']} changes nothing")
        if row["file"].startswith("go/"):
            require((GO_DIR / row["file"][3:]).read_text().count(row["original"]) == 1,
                    f"Mutant {row['name']} original must occur exactly once")
        elif source is not None:
            require((source / row["file"][7:]).read_text().count(row["original"]) == 1,
                    f"Mutant {row['name']} original must occur exactly once in the patched tree")
    return table


def mutant_target(source, row):
    return source / ("extractor/bsc/" + row["file"][3:] if row["file"].startswith("go/") else row["file"][7:])


def mutants(tool_path, tool_sha, manifest_path, manifest_sha, table_path, output, only=()):
    started = time.monotonic()
    deadline = started + MUTANT_SECONDS
    require(tool_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "bsc-admission-tool", "Not a BSC admission tool")
    fixture, genesis_hash = probe14.load_inputs(manifest_path, manifest_sha, tool, deadline)
    table = load_table(table_path)
    selected = [row for row in table["mutants"] if not only or row["name"] in only]
    require(selected, "No mutant selected")
    manifest_ref = Path(tool["build_manifest"]["path"])
    _, manifest = pinned_json(manifest_ref, tool["build_manifest"]["sha256"], 64 * 1024 * 1024, deadline)
    proof = {"schema_version": 1, "kind": "bsc-admission-mutants", "result": "FAIL", "tool": tool_ref,
             "table": reference(table_path, deadline=deadline), "source_commit": pins.source_commit(),
             "run_id": uuid.uuid4().hex, "native_owner_nonce": None, "deadline_seconds": MUTANT_SECONDS,
             "mutants": [], "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS, deadline)
        root = output / "build"
        root.mkdir()
        _, _, source = probe14.extract_source(manifest, manifest_ref, root, deadline)
        install(source, root, deadline)
        load_table(table_path, source)
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (root / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            shutil.copyfile(source / name, root / "metadata" / name)
        mounts = probe14.build_mounts(source, manifest_ref.parent / "cache" / "bsc" / "mod", root)
        for index, row in enumerate(selected):
            check_deadline(deadline)
            target = mutant_target(source, row)
            original = target.read_text()
            target.write_text(original.replace(row["original"], row["replacement"], 1))
            binary = root / "generated" / f"mutant-{index:03d}.test"
            geth = root / "generated" / f"mutant-{index:03d}.geth" if row.get("geth") else None

            def probe(probe_runner, directory):
                try:
                    probe_run(tool, fixture, genesis_hash, directory, probe_runner, deadline, binary=binary,
                              geth_binary=geth)
                finally:
                    binary.unlink(missing_ok=True)
                    if geth:
                        geth.unlink(missing_ok=True)

            try:
                result = probe14.run_mutant(
                    index, row, output, runner, proof["mutants"], lambda path: Native(path, deadline),
                    lambda build_runner: build(build_runner, tool["compiler"], mounts, binary.name,
                                               geth.name if geth else None, [], check_format=False), probe)
            finally:
                target.write_text(original)
            require(result["cleanup_complete"], f"Mutant {row['name']} left resources behind")
        sources_unchanged(origins, deadline)
        bad, proof["counts"] = probe14.tally(proof["mutants"])
        require(not bad, f"Mutants not killed: {bad}")

    return pins.owned_attempt(output, started, deadline, proof, "mutants.json", body)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prep = commands.add_parser("prepare")
    prep.add_argument("--build-manifest", type=Path, required=True)
    prep.add_argument("--sha256", required=True)
    prep.add_argument("--output", type=Path, required=True)
    for name in ("run", "mutants"):
        command = commands.add_parser(name)
        command.add_argument("tool", type=Path)
        command.add_argument("--sha256", required=True)
        command.add_argument("--profile-manifest", nargs=2, metavar=("PATH", "SHA256"), required=True)
        command.add_argument("--output", type=Path, required=True)
        if name == "mutants":
            command.add_argument("--table", type=Path, default=NATIVE / "bsc_admission.mutants.json")
            command.add_argument("--only", nargs="*", default=())
    args = parser.parse_args()

    def interrupt(number, _frame):
        raise InterruptedError("Interrupted by " + signal.Signals(number).name)
    for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(number, interrupt)
    try:
        if args.command == "prepare":
            proof = prepare(args.build_manifest, args.sha256, args.output)
        else:
            manifest = (Path(args.profile_manifest[0]), args.profile_manifest[1])
            if args.command == "run":
                proof = run(args.tool, args.sha256, *manifest, args.output)
            else:
                proof = mutants(args.tool, args.sha256, *manifest, args.table.resolve(), args.output, set(args.only))
    except pins.FAILURES as error:
        print(json.dumps({"result": "REFUSED", "error": f"{type(error).__name__}: {str(error)[:400]}"}))
        return 2
    print(json.dumps({"result": proof["result"], "error": proof["error"]}))
    return 0 if proof["result"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
