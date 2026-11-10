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
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import tarfile
import time
import uuid

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


def ops(events):
    """Compact form of an event list: ("put", key suffix) or (op, sequence)."""
    rows = []
    for event in events:
        if event.get("op") == "put":
            rows.append(("put", event.get("key", "").split("/", 2)[-1]))
        else:
            rows.append((event.get("op"), event.get("sequence")))
    return rows


def index_of(rows, op):
    found = [index for index, row in enumerate(rows) if row[0] == op]
    return found[0] if len(found) == 1 else None


def capsule_digest(refs):
    """Capsule digest from item references, with the formula of the shared README."""
    data = source_inputs.tag("extractor-source-inputs-capsule-v1") + source_inputs.u64(len(refs))
    for ref in refs:
        name = ref["name"].encode()
        data += source_inputs.u64(len(name)) + name + source_inputs.u64(ref["length"]) + bytes.fromhex(ref["sha256"])
    return hashlib.sha256(data).hexdigest()


def poison(oracle, number):
    """The changed source answer of the poisoned modes, from the formula in expected.json."""
    return hashlib.sha256(oracle["poison_format"].format(number=number).encode()).hexdigest()


def expected_log(observation, oracle, poisoned=False):
    """The requests and responses the target block must consume: the generated chain's
    ancestor hashes, or the poisoned answers when the saved log holds those."""
    hashes = observation["generated_hashes"]
    ref = hashes[oracle["target"] - 1]
    rows = []
    for number in oracle["requested_numbers"]:
        request = oracle["request_format"].format(ref=ref, number=number)
        rows.append((request, poison(oracle, number) if poisoned else hashes[number - 1][2:]))
    return rows


def expected_consumed(capsule, log):
    chain = source_inputs.chain_start(bytes.fromhex(capsule))
    for sequence, (request, response) in enumerate(log):
        chain = source_inputs.chain_next(chain, source_inputs.entry_digest(sequence, request.encode(),
                                                                           bytes.fromhex(response)))
    return source_inputs.consumed_digest(bytes.fromhex(capsule), len(log), chain).hex()


def check_capsule(result, observation, oracle, commit, fail):
    capsule = result.get("capsule") or {}
    refs = capsule.get("items") or []
    if [ref.get("name") for ref in refs] != oracle["capsule_items"]:
        fail("capsule items differ from the oracle")
        return None
    by_name = {ref["name"]: ref for ref in refs}
    hashes = observation["generated_hashes"]
    fixed = {"header-rlp": observation["header_rlp_sha256"],
             "parent-hash": hashlib.sha256(bytes.fromhex(hashes[oracle["target"] - 2][2:])).hexdigest(),
             "native-commit": hashlib.sha256(commit.encode()).hexdigest(),
             "normalization-profile": hashlib.sha256(oracle["normalization_profile"].encode()).hexdigest()}
    for name, sha in fixed.items():
        if by_name[name].get("sha256") != sha:
            fail(f"capsule item {name} differs from the expected bytes")
    # Each item is one chunk, which check_seal_events relies on.
    if not all(type(ref.get("length")) is int and 0 < ref["length"] <= source_inputs.CHUNK_BYTES for ref in refs):
        fail("a capsule item is empty or longer than one chunk")
        return None
    if capsule.get("observation") != oracle["observation"] or capsule.get("digest") != capsule_digest(refs):
        fail("capsule digest differs from the independent formula")
        return None
    return capsule["digest"]


def check_seal_events(rows, existed, items, fail):
    """A new seal writes one chunk per item, syncs, writes the manifest and syncs before it returns."""
    sealed = index_of(rows, "sealed")
    if sealed is None:
        fail("the attempt did not seal exactly once")
        return None
    before = rows[:sealed]
    if existed:
        if any(row[0] == "put" for row in before) or ("sync", None) not in before:
            fail("a reseal rewrote the capsule or did not sync")
        return sealed
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


def check_dependencies(rows, start, log, fetched, fail, handed=None):
    """Each fetched response is persisted and synced, frontier last, before its hand-off."""
    requests = [index for index, row in enumerate(rows) if row[0] == "request"]
    count = len(log) if handed is None else len(handed) + 1
    if [rows[index][1] for index in requests] != list(range(count)) or (requests and requests[0] < start):
        fail("requests are not the expected sequences in order")
        return
    for position, index in enumerate(requests):
        sequence = rows[index][1]
        stop = requests[position + 1] if position + 1 < len(requests) else len(rows)
        segment = rows[index + 1:stop]
        handoffs = [offset for offset, row in enumerate(segment) if row[0] == "handoff"]
        if handed is not None and sequence not in handed:
            between = segment
        elif len(handoffs) != 1 or segment[handoffs[0]][1] != sequence:
            fail(f"sequence {sequence} was not handed off exactly once")
            continue
        else:
            between = segment[:handoffs[0]]
            if position + 1 < len(requests) and segment[handoffs[0] + 1:]:
                fail(f"events follow the hand-off of sequence {sequence}")
        persisted = [("fetch", sequence), ("put", f"log/{sequence:016x}/0000000000000000"),
                     ("put", f"log/{sequence:016x}"), ("sync", None), ("put", "frontier"), ("sync", None)]
        if sequence in fetched:
            want = persisted if handed is None or sequence in handed else persisted + [("crash", None)]
            if between != want:
                fail(f"sequence {sequence} was not persisted and synced before its hand-off")
        elif between:
            fail(f"replayed sequence {sequence} fetched or wrote data")


def check_complete(attempt, want, observation, oracle, commit, fail):
    """An attempt that ran block 4 to the end: imported, or rejected by native validation."""
    result = attempt.get("result")
    if attempt.get("exit_code") != 0 or attempt.get("signal") or not isinstance(result, dict):
        fail("the attempt did not finish")
        return
    target = oracle["target"]
    hashes = observation["generated_hashes"]
    rejected = want.get("rejected", False)
    log = expected_log(observation, oracle, poisoned=want.get("saved", "native") == "poisoned")
    if result.get("errors"):
        fail("the attempt reported errors")
    if rejected:
        if oracle["control_error"] not in str(result.get("import_error")) or result.get("head_number") != target - 1 \
                or "consumed" in result or "reader_slots" in result:
            fail("native validation did not reject the saved poisoned answers")
    else:
        if result.get("import_error") is not None:
            fail("the attempt reported errors")
        if result.get("head_number") != target or result.get("head_hash") != hashes[target - 1]:
            fail("the imported head differs from the generated block")
        if result.get("reader_slots") != ["0x" + response for _, response in log]:
            fail("native execution did not receive the expected BLOCKHASH bytes")
    if result.get("capsule_existed") is not want["capsule_existed"] or \
            result.get("frontier_at_open") != want["frontier_at_open"]:
        fail("durable state at open differs from the oracle")
    capsule = check_capsule(result, observation, oracle, commit, fail)
    if not rejected:
        consumed = result.get("consumed") or {}
        if capsule is None or consumed.get("digest") != expected_consumed(capsule, log) or \
                consumed.get("count") != len(log) or consumed.get("capsule_digest") != capsule:
            fail("the consumed manifest differs from the independent digest")
        if result.get("cursor") != len(log) or \
                not str(result.get("extra_after_finish", "")).startswith(oracle["closed_error_prefix"]):
            fail("the closed log accepted a new response")
    events = attempt.get("events") or []
    rows = ops(events)
    fetch_poisoned = want.get("fetch_poisoned", False)
    if any(event.get("op") == "fetch" and event.get("poisoned") is not fetch_poisoned for event in events):
        fail("a fetch used the wrong source")
    sealed = check_seal_events(rows, want["capsule_existed"], len(oracle["capsule_items"]), fail)
    opened, started = index_of(rows, "opened"), index_of(rows, "process_start")
    # The sync must lie between the seal and the open, so the slice is empty unless sealed < opened.
    # A failed seal check was already reported; rows[None:opened] then starts at the beginning.
    if opened is None or started is None or not opened < started or \
            ("sync", None) not in rows[sealed:opened]:
        fail("the capsule and log were not durable before execution started")
        return
    check_dependencies(rows, started, log, want["fetched"], fail)
    for event in events:
        if event.get("op") == "request" and event.get("request") != log[event["sequence"]][0]:
            fail("a request differs from the expected identity")
        if event.get("op") == "handoff" and event.get("response") != log[event["sequence"]][1]:
            fail("a hand-off differs from the saved bytes")
    tail = rows[[index for index, row in enumerate(rows) if row[0] == "handoff"][-1] + 1:]
    expected_tail = [] if rejected else \
        ([("put", "closed"), ("sync", None)] if want["closes"] else []) + [("finished", None)]
    if tail != expected_tail:
        fail("the consumed manifest was not closed as expected")


def check_crash(attempt, want, observation, oracle, fail):
    if attempt.get("signal") != "killed" or "result" in attempt:
        fail("the attempt was not killed at its crash point")
    events = attempt.get("events") or []
    rows = ops(events)
    if not events or events[-1].get("op") != "crash" or events[-1].get("point") != want["point"]:
        fail("the last event is not the expected crash")
        return
    if want["point"] == "after_seal":
        if rows[-2:-1] != [("sealed", None)] or \
                check_seal_events(rows, False, len(oracle["capsule_items"]), fail) is None:
            fail("the crash did not follow a completed seal")
        return
    started = index_of(rows, "process_start")
    if started is None:
        fail("execution did not start before the crash")
        return
    check_dependencies(rows, started, expected_log(observation, oracle), want["handed"] + [want["sequence"]], fail,
                       handed=want["handed"])


def check_control(attempt, oracle, fail):
    result = attempt.get("result") or {}
    poisoned = [event for event in attempt.get("events") or [] if event.get("op") == "direct_poison"]
    if oracle["control_error"] not in str(result.get("import_error")) or \
            result.get("head_number") != oracle["target"] - 1 or len(poisoned) != len(oracle["requested_numbers"]):
        fail("the poisoned control was not rejected by native validation")


def compare(observation, oracle, commit):
    """Return the differences between one probe observation and the oracle."""
    failures = []
    if observation.get("family") != oracle["family"] or observation.get("observation") != oracle["observation"] or \
            observation.get("target") != oracle["target"] or len(observation.get("generated_hashes", [])) != oracle["target"]:
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
                check_crash(attempt, want, observation, oracle, fail)
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
