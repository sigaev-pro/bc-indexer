#!/usr/bin/env python3
"""Prepare, run and mutate the BSC probe of E01-02.inputs in owned containers.

`prepare` installs the shared source inputs core (adapters/bsc/source_inputs)
and the BSC wiring probe (test/harness/fixtures/source_inputs_bsc) into
extractor/source_inputs of the accepted BSC source tree, checks the format,
runs `go vet` and compiles one test binary with the accepted offline
compiler, and writes a tool manifest that the caller must pin.

`run` executes that binary against the accepted private BSC Parlia fixture,
then checks the report and the event traces with its own rules: the static
capsule is synced before execution, each new dependency response is durable
before Parlia receives it, a SIGKILL at a hand-off keeps the response, and
replay hands Parlia the saved bytes. Private local evidence gives no
public-profile credit and no AC verdict.

`mutants` runs single-check mutants of the Go probe from a table inside a
fresh copy of the accepted BSC tree; each mutant must pass `go vet` and compile.
"""

import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tarfile
import time
import uuid

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE))
from artifacts import (Rejected, artifact, check_deadline, copy_artifact, load, pinned_json, read_bytes, reference,
                       require, source_snapshot, sources_unchanged)
from native import Native
import pins
import profile_manifest
import source_inputs

FIXTURE = HERE / "fixtures" / "source_inputs_bsc"
CORE = ROOT / "adapters" / "bsc" / "source_inputs"
CORE_FILES = ("inputs.go", "inputs_test.go")
PROBE_FILES = ("bsc_inputs.go", "bsc_inputs_test.go")
INSTALL = "extractor/source_inputs"
PACKAGE = "./" + INSTALL + "/"
HARNESS = ("bsc_source_inputs.py", "pins.py", "artifacts.py", "native.py", "owned.py", "source_inputs.py",
           "profile_manifest.py")
CHILD_TEST = "TestNativeBSCInputsChild"
# Independent copies of the probe's declared constants.
STATE_PROFILE = b"state.scheme=hash;gcmode=full;snapshots=off"
GET_TAG = b"bsc-parlia-db-get-v1\x00"
OBSERVATIONS = ("bsc-block-1", "bsc-block-2")
CONTROL_ERROR = "unauthorized validator"
PREPARATION_SECONDS = 3600
RUN_SECONDS = 1800
MUTANT_SECONDS = 4 * 3600
TEST_CAP = 900
TRACES = ("trace-capture.jsonl", "trace-resume.jsonl", "trace-control.jsonl", "trace-replay.jsonl")


def test_names(paths):
    names = []
    for path in paths:
        names += re.findall(r"^func (Test\w+)\(t \*testing\.T\)", path.read_text(), re.M)
    require(len(names) == len(set(names)), "Duplicate Go test names")
    return sorted(name for name in names if name != CHILD_TEST)


def install(source, destination_root, deadline):
    """Copy the core and probe files into the native tree; never replace a native file."""
    target_dir = source / INSTALL
    require(not target_dir.exists() and not target_dir.is_symlink(), f"{INSTALL} already exists in the native tree")
    target_dir.mkdir(parents=True)
    rows = []
    for origin in [CORE / name for name in CORE_FILES] + [FIXTURE / name for name in PROBE_FILES]:
        expected = reference(origin, 1024 * 1024, deadline)
        retained = destination_root / origin.name
        retained.parent.mkdir(parents=True, exist_ok=True)
        copy_artifact(origin, retained, expected, 1024 * 1024, deadline)
        copy_artifact(origin, target_dir / origin.name, expected, 1024 * 1024, deadline)
        rows.append({"relative": f"{INSTALL}/{origin.name}", "source": expected,
                     "retained": reference(retained, 1024 * 1024, deadline)})
    return rows


def extract_source(manifest, manifest_path, root, deadline):
    closure = manifest["module_closures"]["bsc"]
    archive = artifact(closure["source_archive"], 256 * 1024 * 1024, manifest_path.parent, deadline)
    copied = copy_artifact(archive, root / "source.tar", closure["source_archive"], 256 * 1024 * 1024, deadline)
    source = root / "source"
    source.mkdir()
    data = read_bytes(root / "source.tar", closure["source_archive"]["size_bytes"], deadline)
    with tarfile.open(fileobj=io.BytesIO(data)) as stream:
        for member in stream:
            check_deadline(deadline)
            stream.extract(member, source, filter="data")
    del data
    for row in closure["artifacts"]:
        for field in ("info", "mod", "zip"):
            artifact(row[field], 1024 * 1024 * 1024, manifest_path.parent, deadline)
    return closure, copied, source


def build_mounts(source, modules, root):
    mounts = [(source, "/source", True), (modules, "/modules", True), (root, "/work", True)]
    return mounts + [(root / name, "/work/" + name, False)
                     for name in ("metadata", "generated", "build-cache", "build-temp", "gopath")]


def compile_package(runner, compiler, mounts, binary, check_format=True):
    """Optionally gofmt, then vet and compile the installed package; a failure raises Rejected."""
    if check_format:
        stdout = runner.command(compiler, ["/usr/local/go/bin/gofmt", "-l", INSTALL], mounts, cap=300, memory="2g",
                                cwd="/source", environment=pins.environment())
        require(not stdout.read_bytes().strip(), "gofmt reports unformatted files")
    runner.command(compiler, ["/usr/local/go/bin/go", "vet", PACKAGE], mounts, cap=1500, memory="6g", cwd="/source",
                   environment=pins.environment())
    runner.command(compiler, ["/usr/local/go/bin/go", "test", "-c", "-trimpath", "-o", "/work/generated/" + binary,
                              PACKAGE], mounts, cap=1500, memory="6g", cwd="/source", environment=pins.environment())


def prepare(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    require(manifest_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    manifest_ref = reference(manifest_path, 64 * 1024 * 1024, deadline)
    require(manifest_ref["sha256"] == expected_sha, "BuildManifest digest mismatch")
    manifest = load(manifest_path, 64 * 1024 * 1024, deadline)
    source_inputs.check_identical(ROOT)
    require(not output.is_relative_to(manifest_path.parent), "Preparation cannot write into accepted build storage")
    proof = {"schema_version": 1, "kind": "bsc-source-inputs-preparation", "result": "FAIL",
             "build_manifest": manifest_ref, "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex,
             "native_owner_nonce": None, "deadline_seconds": PREPARATION_SECONDS, "runtime": "NOT_RUN", "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS, deadline)
        root = output / "bsc"
        root.mkdir()
        closure, copied, source = extract_source(manifest, manifest_path, root, deadline)
        installed = install(source, root / "installed", deadline)
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (root / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            copy_artifact(source / name, root / "metadata" / name, reference(source / name, deadline=deadline),
                          deadline=deadline)
        compiler = manifest["builds"]["bsc"]["compiler_image"]
        require(compiler == manifest["image_inputs"]["golang-build-image"]["reference"], "Unexpected compiler image")
        compile_package(runner, compiler, build_mounts(source, manifest_path.parent / "cache" / "bsc" / "mod", root),
                        "inputs.test")
        sources_unchanged(origins, deadline)
        for row in installed:
            require(reference(Path(row["source"]["path"]), 1024 * 1024, deadline) == row["source"],
                    "Installed source changed during preparation")
        tool = {"schema_version": 1, "kind": "bsc-source-inputs-tool", "build_manifest": manifest_ref,
                "native_source_commit": closure["source_commit"], "source_archive": copied, "compiler": compiler,
                "installed": installed, "source_root": str(source),
                "tests": test_names([Path(row["retained"]["path"]) for row in installed if
                                     row["relative"].endswith("_test.go")]),
                "test_binary": reference(root / "generated/inputs.test", deadline=deadline),
                "metadata": [reference(root / "metadata" / name, deadline=deadline) for name in ("go.mod", "go.sum")]}
        pins.save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "preparation.json", body)


# ---- independent checks of one run ----

def split_rlp_list_items(data):
    """Split a concatenation of RLP lists into the raw encoding of each list."""
    items, offset = [], 0
    while offset < len(data):
        first = data[offset]
        require(first >= 0xc0, "Block encoding is not an RLP list")
        if first <= 0xf7:
            header, length = 1, first - 0xc0
        else:
            size = first - 0xf7
            header, length = 1 + size, int.from_bytes(data[offset + 1:offset + 1 + size], "big")
        end = offset + header + length
        require(end <= len(data), "Truncated block encoding")
        items.append(data[offset:end])
        offset = end
    return items


def expected_items(raw_block, parent_hash, genesis, native_commit):
    return [("block-rlp", raw_block), ("parent-hash", bytes.fromhex(parent_hash.removeprefix("0x"))),
            ("genesis-json", genesis), ("native-commit", native_commit.encode()), ("state-profile", STATE_PROFILE)]


def check_capsule(capsule, observation, items):
    require(type(capsule) is dict and capsule.get("observation") == observation, f"{observation}: capsule identity")
    rows = [{"name": name, "length": len(data), "sha256": hashlib.sha256(data).hexdigest()} for name, data in items]
    require(capsule.get("items") == rows, f"{observation}: capsule items differ from the fixture inputs")
    require(capsule.get("digest") == source_inputs.capsule_digest(items).hex(),
            f"{observation}: capsule digest differs from the independent value")


def consumed_digest(capsule_digest, handoffs):
    """Recompute the consumed-manifest digest from the handed requests and responses."""
    chain = source_inputs.chain_start(bytes.fromhex(capsule_digest))
    for sequence, row in enumerate(handoffs):
        entry = source_inputs.entry_digest(sequence, bytes.fromhex(row["request_hex"]),
                                           bytes.fromhex(row["response_hex"]))
        chain = source_inputs.chain_next(chain, entry)
    return source_inputs.consumed_digest(bytes.fromhex(capsule_digest), len(handoffs), chain).hex()


def handoff_steps(events, observation):
    """Return the hand-offs of one observation with the events since the previous anchor.

    An anchor is the observation's execute event or its previous hand-off.
    Each hand-off must be either a replay (no fetch and no write since the
    anchor) or a new durable response (fetch, data chunks, entry, sync,
    frontier, sync), with nothing else in between.
    """
    executes = [index for index, event in enumerate(events)
                if event.get("event") == "execute" and event.get("observation") == observation]
    require(len(executes) == 1, f"{observation}: expected one execute event")
    rows, anchor = [], executes[0]
    for index in range(anchor + 1, len(events)):
        event = events[index]
        if event.get("event") != "handoff" or event.get("observation") != observation:
            continue
        between = events[anchor + 1:index]
        names = [item["event"] + (":" + item["record"] if "record" in item else "") for item in between]
        require(event.get("sequence") == len(rows), f"{observation}: hand-off sequence {event.get('sequence')}")
        require(hashlib.sha256(bytes.fromhex(event["response_hex"])).hexdigest() == event.get("response_sha256"),
                f"{observation}: hand-off digest")
        if not names:
            kind = "replay"
        else:
            require(names[0] == "fetch" and set(names[1:-4]) == {"put:entry-chunk"} and
                    names[-4:] == ["put:entry", "sync", "put:frontier", "sync"],
                    f"{observation}: hand-off {len(rows)} was not durable first: {names}")
            require(all(item.get("observation") == observation for item in between if item["event"] == "put"),
                    f"{observation}: a write of another observation")
            kind = "new"
        rows.append(dict(event, kind=kind))
        anchor = index
    return rows


def capsule_synced_before_execute(events, observation):
    """Every capsule write of the observation is followed by a sync before its execute event."""
    execute = next(index for index, event in enumerate(events)
                   if event.get("event") == "execute" and event.get("observation") == observation)
    writes = [index for index, event in enumerate(events[:execute]) if event.get("event") == "put" and
              event.get("observation") == observation and event.get("record") in ("capsule", "capsule-chunk")]
    syncs = [index for index, event in enumerate(events[:execute]) if event.get("event") == "sync"]
    require(syncs and (not writes or max(writes) < max(syncs)), f"{observation}: capsule not synced before execution")
    require(not any(event.get("event") in ("fetch", "handoff") for event in events[:execute]
                    if event.get("observation") == observation), f"{observation}: a read before execution")
    return len(writes)


def finish_steps(events, observation, closed_before):
    """After the last hand-off: no unlogged fetch, then the close record and a sync, or nothing if already closed."""
    finished = [index for index, event in enumerate(events)
                if event.get("event") == "finished" and event.get("observation") == observation]
    require(len(finished) == 1, f"{observation}: expected one finished event")
    last = max(index for index, event in enumerate(events[:finished[0]])
               if event.get("observation") == observation and event.get("event") in ("execute", "handoff"))
    names = [item["event"] + (":" + item["record"] if "record" in item else "") for item in events[last + 1:finished[0]]]
    require(names == ([] if closed_before else ["put:closed", "sync"]), f"{observation}: close steps {names}")
    return events[finished[0]]["consumed"]


def evaluate(report, traces, fixture, raw_blocks, genesis, native_commit, genesis_hash):
    """Check the probe report and traces against the fixture; return a summary or raise Rejected."""
    blocks = fixture["blocks"]
    hashes = [row["hash"] for row in blocks]
    parents = [genesis_hash, hashes[0]]
    require(report.get("public_profile_credit") is False, "Report claims public-profile credit")
    require(report.get("genesis_hash") == genesis_hash and report.get("block_hashes") == hashes,
            "Report names another chain")
    items = [expected_items(raw_blocks[index], parents[index], genesis, native_commit) for index in range(2)]

    # Capture child: block 1 completes, block 2 is killed right after its first durable hand-off.
    require(report["child"].get("killed_by_sigkill") is True, "Capture child was not killed by SIGKILL")
    capture = traces["trace-capture.jsonl"]
    for observation in OBSERVATIONS:
        capsule_synced_before_execute(capture, observation)
    first = handoff_steps(capture, OBSERVATIONS[0])
    require(first and all(row["kind"] == "new" for row in first), "Block 1 capture must fetch every response")
    require(sum(1 for event in capture if event.get("event") == "fetch" and event.get("observation") == OBSERVATIONS[0])
            == len(first), "Block 1 has a fetch without a hand-off")
    first_consumed = finish_steps(capture, OBSERVATIONS[0], False)
    second = handoff_steps(capture, OBSERVATIONS[1])
    require(len(second) == 1 and second[0]["kind"] == "new", "Block 2 capture must stop at its first new hand-off")
    require(capture[-1] == {"event": "crash", "observation": OBSERVATIONS[1], "sequence": 0} and
            capture[-2].get("event") == "handoff", "The crash is not the hand-off of block 2")
    request = bytes.fromhex(second[0]["request_hex"])
    require(request.startswith(GET_TAG + b"parlia-"), "The crashed read is not a Parlia snapshot read")
    saved = bytes.fromhex(second[0]["response_hex"])
    require(saved[:1] == b"\x01" and len(saved) > 1, "The crashed read did not return a stored snapshot")

    # After the crash the capsules and the response are durable.
    after = report["after_crash"]
    for index, observation in enumerate(OBSERVATIONS):
        check_capsule(after[observation + "_capsule"], observation, items[index])
    check_capsule_digest = after[OBSERVATIONS[0] + "_capsule"]["digest"]
    require(first_consumed["capsule_digest"] == check_capsule_digest and first_consumed["count"] == len(first) and
            first_consumed["digest"] == consumed_digest(check_capsule_digest, first),
            "Block 1 consumed manifest differs from the independent value")
    require(after[OBSERVATIONS[0] + "_frontier"] == len(first) and after[OBSERVATIONS[1] + "_frontier"] == 1,
            "Durable frontiers after the crash")
    require(after["saved_response_sha256"] == hashlib.sha256(saved).hexdigest(),
            "The saved response differs from the bytes handed before the crash")
    require(after["saved_equals_live"] is True, "The saved response is not the stored snapshot")

    # Resume on the crashed home: the saved prefix is replayed without a fetch;
    # a read beyond it is a new logged response.
    resume = report["resume"]
    require(resume.get("error") == "" and resume.get("frontier_at_start") == 1, f"Resume failed: {resume.get('error')}")
    require(resume["head_number"] == 2 and resume["head_hash"] == hashes[1] and
            resume["head_root"] == blocks[1]["state_root"], "Resume did not import block 2")
    check_capsule(resume["capsule"], OBSERVATIONS[1], items[1])
    trace = traces["trace-resume.jsonl"]
    capsule_synced_before_execute(trace, OBSERVATIONS[1])
    resumed = handoff_steps(trace, OBSERVATIONS[1])
    require(resumed and resumed[0]["kind"] == "replay" and resumed[0]["request_hex"] == second[0]["request_hex"] and
            resumed[0]["response_hex"] == second[0]["response_hex"], "Resume did not replay the saved response")
    require(all(row["kind"] == "new" for row in resumed[1:]) and
            resume["fetches"] == len(resumed) - 1 ==
            sum(1 for event in trace if event.get("event") == "fetch"), "Resume fetched below the saved frontier")
    second_consumed = finish_steps(trace, OBSERVATIONS[1], False)
    require(resume["consumed"] == second_consumed and second_consumed["count"] == len(resumed) and
            second_consumed["digest"] == consumed_digest(resume["capsule"]["digest"], resumed),
            "Block 2 consumed manifest differs from the independent value")

    # The replay home's snapshot was tampered before block 2, and a live read of
    # the same tampered snapshot breaks block 2 in the control.
    tamper = report["replay_tamper"]
    require(tamper["live_before_tamper_sha256"] == hashlib.sha256(saved[1:]).hexdigest() and
            tamper["live_after_tamper_sha256"] != tamper["live_before_tamper_sha256"],
            "The replay home's snapshot was not tampered")
    control = report["control"]
    require(control.get("stage") in ("open", "insert") and CONTROL_ERROR in control.get("error", ""),
            "Control did not fail on the tampered snapshot")
    require(control["live_before_tamper_sha256"] == tamper["live_before_tamper_sha256"] and
            control["live_after_tamper_sha256"] == tamper["live_after_tamper_sha256"],
            "Control used another snapshot")
    require(control.get("head_number", 1) == 1, "Control imported block 2")

    # Native-only replay on a fresh home: no fetch, no write, same manifests.
    replay = report["replay"]
    trace = traces["trace-replay.jsonl"]
    require(len(replay) == 2, "Replay must cover both blocks")
    require(not any(event.get("event") in ("fetch", "put") for event in trace), "Replay fetched or wrote")
    for index, observation in enumerate(OBSERVATIONS):
        row = replay[index]
        require(row.get("error") == "" and row["fetches"] == 0 and row["head_number"] == index + 1 and
                row["head_hash"] == hashes[index] and row["head_root"] == blocks[index]["state_root"],
                f"Replay of {observation} failed: {row.get('error')}")
        check_capsule(row["capsule"], observation, items[index])
        capsule_synced_before_execute(trace, observation)
        steps = handoff_steps(trace, observation)
        require(all(step["kind"] == "replay" for step in steps), f"Replay of {observation} fetched")
        require(all(step["response_sha256"] != tamper["live_after_tamper_response_sha256"] for step in steps),
                f"Replay of {observation} handed the tampered snapshot")
        require(row["consumed"] == finish_steps(trace, observation, True) ==
                (first_consumed if index == 0 else second_consumed), f"Replay of {observation} changed its manifest")
    return {"block1_responses": len(first), "block2_responses": len(resumed),
            "crash_request": request[len(GET_TAG):].hex(), "saved_response_sha256": after["saved_response_sha256"],
            "live_after_tamper_response_sha256": tamper["live_after_tamper_response_sha256"],
            "control_error": control["error"], "consumed": [first_consumed["digest"], second_consumed["digest"]]}


def checked_evaluate(*args):
    """Evaluate, turning a malformed report into a rejection instead of an exception."""
    try:
        return evaluate(*args)
    except (KeyError, TypeError, ValueError, AttributeError, IndexError, StopIteration) as error:
        raise Rejected(f"Malformed probe report: {type(error).__name__}: {error}") from None


def read_jsonl(path, deadline):
    return [json.loads(line) for line in read_bytes(path, 16 * 1024 * 1024, deadline).decode().splitlines()]


def load_inputs(manifest_path, manifest_sha, tool, deadline):
    family, fixture = pins.load_fixture(manifest_path, manifest_sha, deadline)
    require(family == "bsc", "A BSC ProfileManifest is required")
    require(fixture["build_manifest"] == tool["build_manifest"], "Fixture and tool use different builds")
    manifest = profile_manifest.load_pinned(manifest_path, manifest_sha, deadline)
    return fixture, manifest["identity"]["genesis_hash"]


def probe_run(tool, fixture, genesis_hash, output, runner, deadline, binary=None):
    """Run the probe binary once and check it; return the family record."""
    root = output / "bsc"
    for name in ("inputs", "work", "temp"):
        (root / name).mkdir(parents=True)
    inputs = {name: copy_artifact(Path(ref["path"]), root / "inputs" / name, ref, 64 * 1024 * 1024, deadline)
              for name, ref in fixture["inputs"].items()}
    binary = binary or artifact(tool["test_binary"], 1024 * 1024 * 1024, deadline=deadline)
    mounts = [(binary.parent, "/tool", True), (root / "inputs", "/inputs", True), (root / "work", "/work", False),
              (root / "temp", "/scratch", False)]
    env = {"EXTRACTOR_INPUTS_INPUTS": "/inputs", "EXTRACTOR_INPUTS_WORK": "/work",
           "EXTRACTOR_INPUTS_REPORT": "/work/report.json", "EXTRACTOR_INPUTS_NATIVE_COMMIT": tool["native_source_commit"],
           "TMPDIR": "/scratch", "HOME": "/scratch"}
    started = time.monotonic()
    stdout = runner.command(tool["compiler"], ["/tool/" + binary.name, "-test.v", "-test.count=1", "-test.run", "^Test",
                                               "-test.skip", f"^{CHILD_TEST}$", "-test.timeout", f"{TEST_CAP - 60}s"],
                            mounts, cap=TEST_CAP, memory="2g", cwd="/scratch", environment=env)
    elapsed = round(time.monotonic() - started, 3)
    passed = pins.parse_go_test(read_bytes(stdout, 8 * 1024 * 1024, deadline).decode(), tool["tests"])
    report_path = root / "work/report.json"
    traces = {name: read_jsonl(root / "work" / name, deadline) for name in TRACES}
    raw_blocks = split_rlp_list_items(read_bytes(root / "inputs/blocks.rlp", 1024 * 1024, deadline))
    require(len(raw_blocks) == 2, "Fixture must hold two blocks")
    genesis = read_bytes(root / "inputs/genesis.json", 4 * 1024 * 1024, deadline)
    summary = checked_evaluate(load(report_path, 4 * 1024 * 1024, deadline), traces, fixture, raw_blocks, genesis,
                               tool["native_source_commit"], genesis_hash)
    return {"inputs": inputs, "passed": passed, "elapsed_seconds": elapsed,
            "report": reference(report_path, deadline=deadline), "stdout": reference(stdout, deadline=deadline),
            "traces": {name: reference(root / "work" / name, deadline=deadline) for name in TRACES},
            "child_output": reference(root / "work/child-output.txt", deadline=deadline), "summary": summary}


def run(tool_path, tool_sha, manifest_path, manifest_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(tool_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "bsc-source-inputs-tool", "Not a BSC source inputs tool")
    fixture, genesis_hash = load_inputs(manifest_path, manifest_sha, tool, deadline)
    proof = {"schema_version": 1, "kind": "bsc-source-inputs-run", "result": "FAIL", "tool": tool_ref,
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

    return pins.owned_attempt(output, started, deadline, proof, "run.json", body)


# ---- native mutants of the Go probe ----

def load_table(path):
    table = json.loads(path.read_text())
    require(table.get("schema_version") == 1 and table.get("language") == "go-native-bsc", "Unsupported mutant table")
    names = [row["name"] for row in table["mutants"]]
    require(len(names) == len(set(names)) and names, "Mutant names must be unique")
    for row in table["mutants"]:
        require(row["file"] in PROBE_FILES and set(row) <= {"name", "file", "original", "replacement", "equivalent"},
                f"Mutant {row['name']} has an unsupported shape")
        require((FIXTURE / row["file"]).read_text().count(row["original"]) == 1,
                f"Mutant {row['name']} original must occur exactly once")
        require(row["original"] != row["replacement"], f"Mutant {row['name']} changes nothing")
    return table


def failing_tests(stdout):
    return sorted({line.strip().split()[2] for line in stdout.splitlines() if line.strip().startswith("--- FAIL: ")})


def mutants(tool_path, tool_sha, manifest_path, manifest_sha, table_path, output, only=()):
    """Run each mutant: install it, gofmt/vet/compile, run the probe, and classify the outcome."""
    started = time.monotonic()
    deadline = started + MUTANT_SECONDS
    require(tool_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "bsc-source-inputs-tool", "Not a BSC source inputs tool")
    fixture, genesis_hash = load_inputs(manifest_path, manifest_sha, tool, deadline)
    table = load_table(table_path)
    selected = [row for row in table["mutants"] if not only or row["name"] in only]
    require(selected, "No mutant selected")
    _, manifest = pinned_json(Path(tool["build_manifest"]["path"]), tool["build_manifest"]["sha256"],
                              64 * 1024 * 1024, deadline)
    proof = {"schema_version": 1, "kind": "bsc-source-inputs-mutants", "result": "FAIL", "tool": tool_ref,
             "table": reference(table_path, deadline=deadline), "source_commit": pins.source_commit(),
             "run_id": uuid.uuid4().hex, "native_owner_nonce": None, "deadline_seconds": MUTANT_SECONDS,
             "mutants": [], "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS, deadline)
        build = output / "build"
        build.mkdir()
        _, _, source = extract_source(manifest, Path(tool["build_manifest"]["path"]), build, deadline)
        install(source, build / "installed", deadline)
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (build / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            shutil.copyfile(source / name, build / "metadata" / name)
        mounts = build_mounts(source, Path(tool["build_manifest"]["path"]).parent / "cache" / "bsc" / "mod", build)
        target = source / INSTALL
        for index, row in enumerate(selected):
            check_deadline(deadline)
            original = (FIXTURE / row["file"]).read_text()
            for name in PROBE_FILES:
                shutil.copyfile(FIXTURE / name, target / name)
            (target / row["file"]).write_text(original.replace(row["original"], row["replacement"], 1))
            directory = output / "mutants" / f"{index:03d}"
            directory.mkdir(parents=True)
            result = {"name": row["name"], "file": row["file"], "equivalent": row.get("equivalent")}
            runners = [Native(directory, deadline)]
            binary = f"mutant-{index:03d}.test"
            try:
                compile_package(runners[0], tool["compiler"], mounts, binary, check_format=False)
            except Rejected as error:
                result.update(outcome="BUILD_FAILED", evidence=str(error)[:300])
            else:
                (directory / "probe").mkdir()
                runners.append(Native(directory / "probe", deadline))
                try:
                    probe_run(tool, fixture, genesis_hash, directory / "probe", runners[1], deadline,
                              binary=build / "generated" / binary)
                    result["outcome"] = "SURVIVED"
                except Rejected as error:
                    # A kill names the failing Go test, or the driver control that rejected the run.
                    logs = sorted((directory / "probe" / "logs").glob("*.stdout"))
                    failed = failing_tests(logs[-1].read_text(errors="replace")) if logs else []
                    control = str(error)[:300]
                    named = failed or ("Native command failed" not in control and control)
                    result.update(outcome="KILLED" if named else "NO_EVIDENCE", killed_by=failed or [control])
                finally:
                    (build / "generated" / binary).unlink(missing_ok=True)
            result["commands"] = [command for item in runners for command in item.commands]
            result["cleanup_complete"] = all(item.cleanup_complete for item in runners)
            proof["mutants"].append(result)
            require(result["cleanup_complete"], f"Mutant {row['name']} left resources behind")
        for name in PROBE_FILES:
            shutil.copyfile(FIXTURE / name, target / name)
        sources_unchanged(origins, deadline)
        bad = [row["name"] for row in proof["mutants"] if row["outcome"] in ("BUILD_FAILED", "NO_EVIDENCE") or
               (row["outcome"] == "SURVIVED" and not row["equivalent"])]
        proof["counts"] = {outcome: sum(1 for row in proof["mutants"] if row["outcome"] == outcome)
                           for outcome in ("KILLED", "SURVIVED", "BUILD_FAILED", "NO_EVIDENCE")}
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
            command.add_argument("--table", type=Path, default=FIXTURE / "source_inputs_bsc.mutants.json")
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
        # Refused before this command created its output: no record exists.
        print(json.dumps({"result": "REFUSED", "error": f"{type(error).__name__}: {str(error)[:400]}"}))
        return 2
    print(json.dumps({"result": proof["result"], "error": proof["error"]}))
    return 0 if proof["result"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
