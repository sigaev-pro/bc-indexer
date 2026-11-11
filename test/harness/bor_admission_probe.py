#!/usr/bin/env python3
"""Build and run the E01-02.admission Bor probe and judge its AC matrix.

`prepare` installs the Bor block into the accepted Bor source tree
(bor_admission_tree.py), runs the native Go tests of the installed packages in
the Bor module graph, builds the probe and the patched shipped `bor` binary
offline with the accepted compiler, derives the private genesis and seals the
fixture chain against the deterministic Heimdall stub. `run` executes each
scenario plan of the oracle in the pinned runtime image, one container per
scenario, and compares the result with values computed here from the stub
files, the sealed blocks and the README formulas, not from the probe output.
Preparation is not part of the measured run.

The judge is the qa oracle of AC-CAP-02-01, AC-CAP-02-02 (POLY, private local)
and of the real EXEC_READY binding credited to AC-CAP-04-03 (D98 item 7).
"""

import argparse
import calendar
from datetime import datetime, timezone
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import tarfile
import time
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
import admission
import bor_admission_tree
import capture_probe
from artifacts import (Rejected, artifact, check_deadline, copy_artifact, json_artifact, pinned_json, read_bytes,
                       reference, require)
from bor_source_inputs_probe import decoded_header, fork_id, keccak256, rlp_encode, rlp_item
from capture_probe import Interrupted, block_stops, unblock_stops, write_until_done
from native import Native, new_output
import source_inputs

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = Path(__file__).with_name("fixtures") / "admission_bor"
STUB_SOURCES = Path(__file__).with_name("fixtures") / "source_inputs_bor" / "heimdallstub"
PROBE_DIR = "cmd/extractor-bor-admission-probe"
PACKAGE = "github.com/ethereum/go-ethereum"
BODY_FILES = ("span-0.json", "span-1.json", "span-latest.json", "status.json", "clerk-a-from-1.json",
              "clerk-b-from-1.json", "clerk-empty.json")
FIXTURE_FILES = ("expected.json", "ORACLE.md", "main.go")
STUB_FILES = ("stub.go", "stub_test.go")
TESTED_PACKAGES = ("extractor/admission", "extractor/admission/native/obligation", "extractor/source_inputs",
                   "extractor/capture", "extractor/borimport")
PREPARATION_SECONDS = 3600
RUN_SECONDS = 1800
PRODUCER, RUN = "bor", "run-1"
ADMISSION = "extractor-admission-v1/bor.run-1/"
INPUTS = "extractor-source-inputs-v1/"
PIN = "extractor-state-provider-pin-v1-"
TAG_RESULT = "extractor-bor-native-result-v1"
TAG_REJECTION = "extractor-bor-native-rejection-v1"
CHECKPOINT_INTERVAL = 1024


def go_environment():
    return {"CGO_ENABLED": "1", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64",
            "GOCACHE": "/work/build-cache", "GOMODCACHE": "/modules", "GOPATH": "/work/gopath",
            "GOFLAGS": "-mod=readonly", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
            "GOTMPDIR": "/work/gotmp", "GOWORK": "off"}


# ---------------------------------------------------------------------------
# Derived fixture inputs.


def derived_inputs(source_genesis, oracle):
    """The genesis and chain-config items: the accepted test genesis with the
    oracle's timestamp, state sync delay and Rio block, in the shared canonical
    JSON form (D99 item 3)."""
    document = json.loads(source_genesis)
    config = document.get("config")
    require(isinstance(config, dict) and isinstance(config.get("bor"), dict), "The genesis has no Bor config")
    require("stateSyncConfirmationDelay" not in config["bor"] and "rioBlock" not in config["bor"],
            "The fixture genesis already sets the delay or Rio")
    moment = calendar.timegm(time.strptime(oracle["genesis_time"], "%Y-%m-%dT%H:%M:%SZ"))
    document["timestamp"] = hex(moment)
    config["bor"]["stateSyncConfirmationDelay"] = {"0": oracle["state_sync_delay"]}
    config["bor"]["rioBlock"] = oracle["rio_block"]
    return admission.canonical(json.dumps(document).encode()), admission.canonical(json.dumps(config).encode())


def prepare(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    require(manifest_path.is_absolute(), "BuildManifest path must be absolute")
    manifest_ref, manifest = pinned_json(manifest_path, expected_sha, 64 * 1024 * 1024, deadline)
    require(manifest.get("evidence_kind") == "actual-build" and manifest.get("task_id") == "E01-01.builds",
            "An accepted actual build is required")
    root = manifest_path.parent
    capture_probe.refuse_inside(output, (root, ROOT))
    state = capture_probe.source_state()
    with capture_probe.single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "bor-admission-probe-preparation", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": state, "build_manifest": manifest_ref}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            record["fixture"] = capture_probe.copy_sources(FIXTURE_FILES, FIXTURE, output / "fixture", deadline)
            record["stub_bodies"] = capture_probe.copy_sources(BODY_FILES, FIXTURE / "stub", output / "stub", deadline)
            record["harness_code"] = capture_probe.copy_sources(
                ("bor_admission_probe.py", "bor_admission_tree.py", "bor_source_inputs_probe.py", "admission.py",
                 "source_inputs.py", "capture_probe.py", "native.py", "owned.py", "artifacts.py"),
                Path(__file__).parent, output / "harness-code", deadline)
            compiler = manifest["image_inputs"]["golang-build-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/golang@sha256:[0-9a-f]{64}", compiler), "Unpinned compiler")
            runtime = manifest["image_inputs"]["debian-runtime-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
            record["compiler_image"], record["runtime_image"] = compiler, runtime
            oracle = json.loads((output / "fixture" / "expected.json").read_text())
            record["bor"] = prepare_bor(manifest, root, output, native, compiler, oracle, deadline)
            record["chain"] = generate_chain(output, native, runtime, oracle, deadline)
            record["result"] = "PASS"
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "preparation.json", record, native, started)
    return output, record


def extract(data, source, deadline):
    with tarfile.open(fileobj=io.BytesIO(data)) as stream:
        for member in stream:
            check_deadline(deadline)
            stream.extract(member, source, filter="data")


def go_test_passed(text, packages):
    """Every package must report `ok` in `go test` output, and no test may fail."""
    lines = text.splitlines()
    missing = [name for name in packages if not any(line.startswith(f"ok  \t{PACKAGE}/{name}\t") for line in lines)]
    require(not missing, f"Go tests did not pass in the Bor module graph: {missing}")
    require(not any(line.startswith("--- FAIL") or line.startswith("FAIL") for line in lines), "A Go test failed")


def prepare_bor(manifest, root, output, native, compiler, oracle, deadline):
    closure = manifest["module_closures"]["bor"]
    require(closure["source_commit"] == manifest["builds"]["bor"]["source_commit"], "Source closure mismatch")
    require(manifest["builds"]["bor"]["compiler_image"] == compiler, "Bor compiler mismatch")
    directory = output / "bor"
    directory.mkdir()
    archive = artifact(closure["source_archive"], 512 * 1024 * 1024, root, deadline)
    row = {"source_commit": closure["source_commit"],
           "source_archive": copy_artifact(archive, directory / "source.tar", closure["source_archive"],
                                           512 * 1024 * 1024, deadline)}
    for module in closure["artifacts"]:
        for field in ("info", "mod", "zip"):
            artifact(module[field], 1024 * 1024 * 1024, root, deadline)
    row["module_artifacts_verified"] = len(closure["artifacts"])
    source = directory / "source"
    source.mkdir()
    extract(read_bytes(directory / "source.tar", closure["source_archive"]["size_bytes"], deadline), source, deadline)
    row["installed"] = bor_admission_tree.install(source)
    probe = source / PROBE_DIR
    probe.mkdir(parents=True)
    row["probe_sources"] = capture_probe.copy_sources(("main.go",), FIXTURE, probe / "main", deadline)
    (probe / "main" / "main.go").rename(probe / "main.go")
    (probe / "main").rmdir()
    row["stub_sources"] = capture_probe.copy_sources(STUB_FILES, STUB_SOURCES, probe / "heimdallstub", deadline)
    for name in ("bin", "build-cache", "gopath", "gotmp"):
        (directory / name).mkdir()
    mounts = [(directory, "/work", True), (root / "cache" / "bor" / "mod", "/modules", True)]
    for name in ("bin", "build-cache", "gopath", "gotmp"):
        mounts.append((directory / name, f"/work/{name}", False))
    environment = go_environment()

    def go(argv, cap):
        return native.command(compiler, ["/usr/local/go/bin/go", *argv], mounts, cap=cap, memory="6g",
                              cwd="/work/source", environment=environment)

    go(["mod", "verify"], 300)
    go(["vet", "./extractor/...", f"./{PROBE_DIR}/..."], 1200)
    stdout = go(["test", "-race", "-count=1", "-cover", *[f"./{name}" for name in TESTED_PACKAGES],
                 f"./{PROBE_DIR}/heimdallstub"], 1800)
    row["unit_tests"] = reference(stdout)
    go_test_passed(stdout.read_text(errors="replace"), (*TESTED_PACKAGES, f"{PROBE_DIR}/heimdallstub"))
    stdout = go(["test", "-race", "-count=1", "-run", "^TestExtractor", "-v", "./core/rawdb"], 1200)
    row["rawdb_tests"] = reference(stdout)
    text = stdout.read_text(errors="replace")
    go_test_passed(text, ("core/rawdb",))
    require("--- PASS: TestExtractorFreezeThreshold" in text, "The freezer threshold test did not run")
    go(["build", "-p", "2", "-trimpath", "-o", "/work/bin/probe", f"./{PROBE_DIR}"], 1800)
    go(["build", "-p", "2", "-trimpath", "-o", "/work/bin/bor", "./cmd/cli"], 1800)
    bin_directory = directory / "bin"
    source_genesis = (source / "tests/bor/testdata/genesis.json").read_bytes()
    genesis, config = derived_inputs(source_genesis, oracle)
    (bin_directory / "genesis.json").write_bytes(genesis)
    (bin_directory / "chain-config.json").write_bytes(config)
    copy_artifact(source / "tests/bor/helper.go", bin_directory / "helper.go",
                  reference(source / "tests/bor/helper.go", 4 * 1024 * 1024, deadline), deadline=deadline)
    accepted = manifest["builds"]["bor"]["binary"]
    (bin_directory / "accepted").mkdir()
    row["accepted_binary"] = copy_artifact(artifact(accepted, 512 * 1024 * 1024, root, deadline),
                                           bin_directory / "accepted" / "bor", accepted, 512 * 1024 * 1024, deadline)
    (bin_directory / "accepted" / "bor").chmod(0o755)
    for name in ("probe", "bor", "genesis.json", "chain-config.json", "helper.go"):
        row[name] = reference(bin_directory / name, deadline=deadline)
    row["source_genesis"] = {"sha256": hashlib.sha256(source_genesis).hexdigest(), "size_bytes": len(source_genesis)}
    return row


def generate_chain(output, native, runtime, oracle, deadline):
    chains = output / "chains"
    chains.mkdir()
    native.command(runtime, ["/probe/probe", "generate", "-genesis", "/probe/genesis.json", "-config",
                             "/probe/chain-config.json", "-keysource", "/probe/helper.go", "-bodies", "/stub",
                             "-blocks", str(oracle["chain_blocks"]), "-out", "/chains/a"],
                   [(output / "bor/bin", "/probe", True), (output / "stub", "/stub", True), (chains, "/chains", False)],
                   cap=900, memory="2g")
    return {"blocks": reference(chains / "a/blocks.rlp", deadline=deadline),
            "generated": reference(chains / "a/generated.json", deadline=deadline),
            "genesis_header": reference(chains / "a/genesis-header.rlp", deadline=deadline)}


# ---------------------------------------------------------------------------
# Independent expected values (qa).


def field(value):
    return len(value).to_bytes(8, "big") + value


def tagged_digest(tag, *values):
    return hashlib.sha256(tag.encode() + b"\0" + b"".join(field(v.encode()) for v in values)).hexdigest()


def result_digest(block_hash, state_root, receipt_root, gas_used):
    """EXEC_READY native result digest (README of adapters/bor/admission/native)."""
    return tagged_digest(TAG_RESULT, block_hash, state_root, receipt_root, str(gas_used))


def rejection_digest(key, block_hash, error):
    """NATIVE_REJECTED evidence digest: observation key, block hash, native error text."""
    return tagged_digest(TAG_REJECTION, key, block_hash, error)


def sealed_chain(blocks, genesis_header):
    """Every sealed block decoded here: its RLP, header RLP and fields, and its Keccak-256 hash."""
    rows, offset = [], 0
    previous = {"hash": keccak256(genesis_header), "header": genesis_header}
    fields = decoded_header(genesis_header)
    require(int.from_bytes(fields[8], "big") == 0, "The genesis header is not block 0")
    while offset < len(blocks):
        start = offset
        block, offset = rlp_item(blocks, offset)
        require(isinstance(block, list) and block and isinstance(block[0], list) and len(block[0]) > 11,
                "Malformed block")
        header = rlp_encode(block[0])
        number = int.from_bytes(block[0][8], "big")
        require(number == len(rows) + 1 and block[0][0] == previous["hash"], "The sealed blocks are not one chain")
        row = {"number": number, "rlp": blocks[start:offset], "header": header, "hash": keccak256(header),
               "parent_hash": block[0][0], "state_root": block[0][3], "receipt_root": block[0][5],
               "gas_used": int.from_bytes(block[0][10], "big"), "time": int.from_bytes(block[0][11], "big")}
        rows.append(row)
        previous = row
    require(rows, "No sealed blocks")
    return rows


def sprint(config, number):
    """Bor CalculateSprint: the value of the highest sprint key at or below number."""
    keys = sorted(int(key) for key in config["bor"]["sprint"])
    return config["bor"]["sprint"][str(max(key for key in keys if key <= number))]


def is_rio(config, number):
    return number >= config["bor"]["rioBlock"]


def to_time(timestamp, delay):
    """The state sync `to_time` query value (bor.go CommitStates)."""
    return datetime.fromtimestamp(timestamp - delay, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def expected_requests(oracle, config, row, variant):
    """The counted Heimdall calls of the observation of one block, by the
    oracle rules read from the pinned Bor source (ORACLE.md)."""
    number, rules, calls = row["number"], oracle["requests"], []
    if is_rio(config, number):
        calls += rules["rio_header_check"]
    if number % sprint(config, number) == 0:
        if not is_rio(config, number) and number == rules["first_span_commit"]:
            calls += rules["span_commit"]
        if not is_rio(config, number):
            calls += rules["status"]
        pages = rules["first_state_sync"][variant] if number == rules["first_state_sync_block"] else \
            rules["later_state_sync"]
        calls += pages
    out = []
    for call in calls:
        out.append({"request": call["request"].replace("{to_time}", to_time(row["time"], oracle["state_sync_delay"])),
                    "body": call["body"]})
    return out


def ancestors(chain, genesis_header, config, number):
    """The ancestor rule: before Rio, the checkpoint at or below the parent up
    to the parent; from Rio on, the parent only."""
    parent = number - 1
    first = parent if is_rio(config, number) else parent - parent % CHECKPOINT_INTERVAL
    return [genesis_header if n == 0 else chain[n - 1]["header"] for n in range(first, parent + 1)]


def capsule_items(context, number, checkpoint):
    """The expected capsule items of the observation of one block, from the
    derived genesis, the sealed chain and the oracle. The checkpoint snapshot
    is native Bor data; `checkpoint` is the observed item, which the caller
    checks separately."""
    chain, genesis_header = context["chain"], context["genesis_header"]
    row = chain[number - 1]
    parent = genesis_header if number == 1 else chain[number - 2]["header"]
    parent_fields = decoded_header(parent)
    listed = ancestors(chain, genesis_header, context["config"], number)
    oracle = context["oracle"]
    items = (("genesis", context["genesis"]), ("block", row["rlp"]), ("parent-header", parent),
             ("parent-hash", keccak256(parent)), ("parent-root", parent_fields[3]),
             ("ancestor-headers", rlp_encode([decoded_header(h) for h in listed])),
             ("checkpoint-snapshot", checkpoint), ("chain-config", context["chain_config"]),
             ("fork-id", fork_id(context["config"], context["genesis_hash"], number)),
             ("native-commit", context["commit"].encode()),
             ("normalization-profile", oracle["normalization_profile"].encode()))
    require([name for name, _ in items] == oracle["capsule_items"], "The oracle capsule items differ")
    return items


def checkpoint_snapshot_ok(data, context, number):
    """Before Rio the item is Bor's stored snapshot of the first ancestor; it
    must name that header and the one fixture signer. From Rio on it is empty."""
    if is_rio(context["config"], number):
        return data == b""
    first = ancestors(context["chain"], context["genesis_header"], context["config"], number)[0]
    try:
        snapshot = json.loads(data)
    except ValueError:
        return False
    signers = [v.get("signer") for v in (snapshot.get("validatorSet") or {}).get("validators") or []]
    return snapshot.get("number") == int.from_bytes(decoded_header(first)[8], "big") and \
        snapshot.get("hash") == "0x" + keccak256(first).hex() and signers == [context["oracle"]["signer"]]


def expected_consumed(capsule, requests, bodies):
    chain = source_inputs.chain_start(capsule)
    for sequence, call in enumerate(requests):
        chain = source_inputs.chain_next(chain, source_inputs.entry_digest(sequence, call["request"].encode(),
                                                                             bodies[call["body"]]))
    return source_inputs.consumed_digest(capsule, len(requests), chain).hex()


def build_context(oracle, bodies, genesis, chain_config, blocks, genesis_header, commit, generated):
    chain = sealed_chain(blocks, genesis_header)
    require(len(chain) == oracle["chain_blocks"], "The sealed chain length differs from the oracle")
    require([row["hash"].hex() for row in chain] == [b["hash"].removeprefix("0x") for b in generated["blocks"]],
            "The generation record differs from the sealed blocks")
    document = json.loads(genesis)
    config = json.loads(chain_config)
    require(document["config"] == config, "The chain-config item is not the genesis config")
    fields = decoded_header(genesis_header)
    require(int.from_bytes(fields[11], "big") == int(document["timestamp"], 16) and
            int.from_bytes(fields[9], "big") == int(document["gasLimit"], 16), "The genesis header differs from the genesis")
    return {"oracle": oracle, "bodies": bodies, "genesis": genesis, "chain_config": chain_config, "config": config,
            "chain": chain, "genesis_header": genesis_header, "genesis_hash": keccak256(genesis_header),
            "commit": commit, "body_sha256": {name: hashlib.sha256(data).hexdigest() for name, data in bodies.items()},
            "plans": {plan["name"]: plan for plan in oracle["plans"]}}


# ---------------------------------------------------------------------------
# Store dumps.


def records(dump):
    """Admission records by (sequence, kind), and the counter."""
    out = {}
    for row in dump:
        key = row["key"]
        if key.startswith(ADMISSION):
            rest = key[len(ADMISSION):]
            out[tuple(rest.split("/"))] = json.loads(row["value"]) if "value" in row else row
    return out


def key_of(sequence):
    return admission.observation_key(PRODUCER, RUN, sequence)


def state_of(recs, sequence):
    """Highest admission state of one observation in a store dump."""
    seq = f"{sequence:016x}"
    for kind, state in (("abandoned", "ABANDONED"), ("rejected", "NATIVE_REJECTED"), ("exec-ready", "EXEC_READY"),
                        ("admitted", "ADMITTED"), ("preparing", "INPUT_PREPARING")):
        if (seq, kind) in recs:
            return state
    return None


def has(dump, key):
    return any(row["key"] == key for row in dump)


def value(dump, key):
    for row in dump:
        if row["key"] == key:
            return json.loads(row["value"])
    return None


def log_entries(dump, key):
    pattern = re.compile(re.escape(INPUTS + key) + r"/log/([0-9a-f]{16})$")
    return sorted(int(m.group(1), 16) for row in dump if (m := pattern.match(row["key"])))


def store_keys(dump):
    return {row["key"]: row["value_sha256"] for row in dump}


# ---------------------------------------------------------------------------
# Judge.


def check_rows(rows, want, origins, fail, label, bodies_sha):
    rows = rows or []
    if len(rows) != len(want) or len(origins) != len(want):
        fail(f"{label}: {len(rows)} counted calls, expected {len(want)}")
        return
    for index, (row, call, origin) in enumerate(zip(rows, want, origins)):
        if row.get("sequence") != index or row.get("request") != call["request"]:
            fail(f"{label}: call {index} is {row.get('request')!r}, expected {call['request']!r}")
        elif row.get("request_sha256") != hashlib.sha256(call["request"].encode()).hexdigest():
            fail(f"{label}: call {index} request digest differs")
        elif row.get("error"):
            fail(f"{label}: call {index} failed: {row['error']}")
        elif row.get("response_sha256") != bodies_sha[call["body"]]:
            fail(f"{label}: call {index} returned bytes that differ from {call['body']}")
        elif row.get("origin") != origin:
            fail(f"{label}: call {index} origin {row.get('origin')}, expected {origin}")


def check_observation(context, dump, sequence, number, variant, fail, label):
    """A durable observation of block `number`: candidate, capsule, admitted
    digest, and, when EXEC_READY, the binding of the independent consumed
    manifest and native result. Returns its state."""
    recs = records(dump)
    seq = f"{sequence:016x}"
    key = key_of(sequence)
    row = context["chain"][number - 1]
    parent = context["genesis_header"] if number == 1 else context["chain"][number - 2]["header"]
    candidate = {"number": number, "hash": row["hash"].hex(), "parent_hash": keccak256(parent).hex(),
                 "parent_root": decoded_header(parent)[3].hex()}
    preparing = recs.get((seq, "preparing"))
    if not preparing or preparing.get("candidate") != candidate or preparing.get("observation") != key:
        fail(f"{label}: {key} is not the observation of block {number}")
        return None
    state = state_of(recs, sequence)
    admitted = recs.get((seq, "admitted"))
    if admitted is None:
        return state
    capsule = value(dump, INPUTS + key + "/capsule")
    if capsule is None:
        fail(f"{label}: {key} is ADMITTED without a sealed capsule")
        return state
    checkpoint_ref = capsule["items"][6] if len(capsule.get("items") or []) == 11 else {}
    checkpoint = context["checkpoints"].get((number, checkpoint_ref.get("sha256")))
    if checkpoint is None or not checkpoint_snapshot_ok(checkpoint, context, number):
        fail(f"{label}: {key} checkpoint-snapshot item does not follow the ancestor rule")
        return state
    items = capsule_items(context, number, checkpoint)
    refs = [{"name": name, "length": len(data), "sha256": hashlib.sha256(data).hexdigest()} for name, data in items]
    digest = source_inputs.capsule_digest(items).hex()
    if capsule.get("items") != refs or capsule.get("digest") != digest or capsule.get("observation") != key:
        fail(f"{label}: {key} capsule differs from the independent items")
    if admitted.get("candidate") != candidate or admitted.get("capsule_digest") != digest:
        fail(f"{label}: {key} ADMITTED record differs from the candidate or capsule")
    pin = next((r for r in dump if r["key"] == PIN + key), None)
    pin_digest = admitted.get("pin_digest")
    if pin is not None and pin["value_sha256"] != pin_digest:
        fail(f"{label}: {key} ADMITTED pin digest is not the ledger record digest")
    if admitted.get("digest") != admission.admitted_digest(key, sequence, candidate, pin_digest, digest):
        fail(f"{label}: {key} ADMITTED digest does not recompute")
    ready = recs.get((seq, "exec-ready"))
    if ready is not None:
        consumed = expected_consumed(bytes.fromhex(digest), expected_requests(context["oracle"], context["config"], row,
                                                                              variant), context["bodies"])
        result = result_digest(row["hash"].hex(), row["state_root"].hex(), row["receipt_root"].hex(), row["gas_used"])
        if ready.get("consumed_digest") != consumed or ready.get("exec_digest") != result or \
                ready.get("admitted_digest") != admitted.get("digest") or \
                ready.get("digest") != admission.exec_ready_digest(admitted["digest"], consumed, result):
            fail(f"{label}: {key} EXEC_READY does not bind the independent consumed manifest and result")
        closed = value(dump, INPUTS + key + "/closed")
        if not closed or closed.get("digest") != consumed:
            fail(f"{label}: {key} closed manifest differs from the independent value")
        if pin is None:
            fail(f"{label}: {key} EXEC_READY lost its pin")
    rejected = recs.get((seq, "rejected"))
    if rejected is not None and (ready is not None or pin is not None):
        fail(f"{label}: {key} NATIVE_REJECTED kept EXEC_READY or its pin")
    return state


def observations_of(result):
    return {o["block"]: o for o in (result or {}).get("observations") or []}


def check_completed(context, attempt, numbers, variant, fail, label):
    """Every listed block was observed in this attempt, reached EXEC_READY with
    the expected counted calls fetched live, and was imported."""
    result, dump = attempt["result"] or {}, attempt["store_after"]
    observed = observations_of(result)
    for number in numbers:
        obs = observed.get(number)
        if obs is None or obs.get("error") or not obs.get("key"):
            fail(f"{label}: block {number} was not observed and imported")
            continue
        sequence = int(obs["key"].rsplit(".", 1)[1], 16)
        want = expected_requests(context["oracle"], context["config"], context["chain"][number - 1], variant)
        check_rows(obs.get("counted_calls"), want, ["fetch"] * len(want), fail, f"{label}: block {number}",
                   context["body_sha256"])
        if check_observation(context, dump, sequence, number, variant, fail, f"{label}: block {number}") != "EXEC_READY":
            fail(f"{label}: block {number} did not reach EXEC_READY")
    last = context["chain"][numbers[-1] - 1]
    if result.get("head_hash") != "0x" + last["hash"].hex():
        fail(f"{label}: the imported head is not block {numbers[-1]}")


def lost_attempt(context, crash, dump, first, block):
    """The completed part of a lost attempt: the crash result has no store
    keys, so the observations before the lost block get their sequence keys
    (one observation per block from the first block of the plan)."""
    observations = [{**o, "key": key_of(o["block"] - first)} for o in crash["observations"] if o["block"] < block]
    return {"result": {**crash, "head_hash": "0x" + context["chain"][block - 2]["hash"].hex(),
                       "observations": observations}, "store_after": dump}


def check_refused_before_recovery(result, fail, label):
    if "still pending" not in (result.get("golive_before_recovery") or ""):
        fail(f"{label}: GoLive was not refused while an obligation was pending")
    if "RECOVERY_ONLY" not in (result.get("begin_before_recovery") or ""):
        fail(f"{label}: Begin was not refused in RECOVERY_ONLY")


def compare(name, observed, context):
    """Return the differences between one scenario result and the oracle."""
    failures = []

    def fail(message):
        failures.append(f"{name}: {message}")

    plan = context["plans"][name]
    attempts = observed.get("attempts") or []
    if observed.get("plan", {}).get("attempts") != plan["attempts"] or len(attempts) != len(plan["attempts"]):
        return failures + [f"{name}: the scenario did not run the oracle plan"]
    for index, (step, row) in enumerate(zip(plan["attempts"], attempts)):
        status = row.get("status") or row.get("prune_state") or {}
        if step["crash"]:
            if status.get("signal") != "killed" or not row.get("crash_result"):
                fail(f"attempt {index} was not lost at its crash point")
        elif index in plan.get("refused", []):
            if status.get("exit_code") != 1 or status.get("signal"):
                fail(f"attempt {index} exited with {status}, expected a refusal")
        elif status.get("exit_code") != 0 or status.get("signal"):
            fail(f"attempt {index} exited with {status}")
    if failures:
        return failures
    kind, block = plan["kind"], plan.get("block")
    variant = lambda index: plan["attempts"][index]["variant"]
    if kind in ("lost-before-admitted", "lost-after-admitted", "lost-after-exec-ready"):
        lost, retry = attempts[0], attempts[1]
        dump = lost["store_after"]
        crash = lost["crash_result"]
        check_completed(context, lost_attempt(context, crash, dump, 1, block), list(range(1, block)), variant(0), fail,
                        "lost attempt")
        sequence = block - 1
        key = key_of(sequence)
        state = check_observation(context, dump, sequence, block, variant(0), fail, "lost attempt")
        step = plan["crash_step"]
        want_state = {"lost-before-admitted": "INPUT_PREPARING", "lost-after-admitted": "ADMITTED",
                      "lost-after-exec-ready": "EXEC_READY"}[kind]
        if state != want_state:
            fail(f"the lost observation is {state}, expected {want_state}")
        pinned, sealed = has(dump, PIN + key), has(dump, INPUTS + key + "/capsule")
        if (pinned, sealed) != {"begin": (False, False), "pin": (True, False), "seal": (True, True)}.get(step, (True, True)):
            fail(f"after a loss at {step} the pin is {pinned} and the capsule {sealed}")
        saved = log_entries(dump, key)
        want_saved = {"log-2": [0, 1, 2], "finish": [0, 1, 2, 3], "exec-ready": [0, 1, 2, 3]}.get(step, [])
        if saved != want_saved:
            fail(f"after a loss at {step} the durable log entries are {saved}, expected {want_saved}")
        result = retry["result"] or {}
        pending = [f"{key_of(s)} EXEC_READY" for s in range(sequence)] + [f"{key} {want_state}"]
        if result.get("pending_at_open") != pending:
            fail(f"the restart did not reopen the saved run with its obligations: {result.get('pending_at_open')}")
        recovery = result.get("recovery") or {}
        after = retry["store_after"]
        if kind == "lost-before-admitted":
            check_refused_before_recovery(result, fail, "restart")
            if recovery.get("abandoned") != [key] or recovery.get("exec_ready") or recovery.get("rejected"):
                fail(f"recovery did not abandon only the INPUT_PREPARING observation: {recovery}")
            if state_of(records(after), sequence) != "ABANDONED" or has(after, PIN + key) or log_entries(after, key):
                fail("the abandoned observation kept a pin, a log entry or another state")
        elif kind == "lost-after-admitted":
            check_refused_before_recovery(result, fail, "restart")
            if recovery.get("exec_ready") != [key] or recovery.get("abandoned") or recovery.get("rejected"):
                fail(f"recovery did not resolve the ADMITTED obligation to EXEC_READY: {recovery}")
            want = expected_requests(context["oracle"], context["config"], context["chain"][block - 1], variant(0))
            origins = ["replay"] * len(want_saved) + ["fetch"] * (len(want) - len(want_saved))
            check_rows(result.get("recovery_counted_calls"), want, origins, fail, "recovery", context["body_sha256"])
            changed = context["body_sha256"].get(plan.get("changed_body", ""))
            if changed and any(r.get("response_sha256") == changed for r in result.get("recovery_counted_calls") or []):
                fail("the changed external response reached recovery")
            if records(after).get((f"{sequence:016x}", "admitted")) != records(dump).get((f"{sequence:016x}", "admitted")):
                fail("recovery changed the ADMITTED record")
            if check_observation(context, after, sequence, block, variant(0), fail, "recovery") != "EXEC_READY":
                fail("the recovered obligation is not EXEC_READY")
        else:
            if recovery.get("exec_ready") or recovery.get("abandoned") or key not in (recovery.get("kept") or []):
                fail(f"recovery did not keep the EXEC_READY observation for the decision bridge: {recovery}")
        if kind == "lost-after-admitted" and variant(1) == "b":
            # The live re-import of the block runs a new observation with the
            # changed response; native validation rejects it.
            obs = observations_of(result).get(block) or {}
            new = sequence + 1
            rejected = records(after).get((f"{new:016x}", "rejected"))
            if not obs.get("error") or obs.get("key") != key_of(new) or not rejected or \
                    rejected.get("evidence_digest") != rejection_digest(key_of(new), context["chain"][block - 1]["hash"].hex(),
                                                                         obs["error"]):
                fail("the live re-import with the changed response was not rejected with its evidence digest")
            check_observation(context, after, new, block, variant(1), fail, "re-import")
            done = attempts[2]
            check_completed(context, done, list(range(block, plan["attempts"][2]["to"] + 1)), variant(2), fail, "third attempt")
        else:
            check_completed(context, retry, list(range(block, plan["attempts"][1]["to"] + 1)), variant(1), fail, "retry")
    elif kind == "native-rejection":
        first, second = attempts
        result, dump = first["result"] or {}, first["store_after"]
        sequence, key = block - 1, key_of(block - 1)
        obs = observations_of(result).get(block) or {}
        rejected = records(dump).get((f"{sequence:016x}", "rejected"))
        if "invalid merkle root" not in (obs.get("error") or "") or result.get("inserted") != block - 1:
            fail("native validation did not refuse the block with the changed state sync response")
        if not rejected or rejected.get("evidence_digest") != rejection_digest(key, context["chain"][block - 1]["hash"].hex(),
                                                                                obs.get("error") or ""):
            fail("NATIVE_REJECTED does not bind the native rejection evidence")
        if check_observation(context, dump, sequence, block, variant(0), fail, "rejected") != "NATIVE_REJECTED":
            fail("the refused observation is not NATIVE_REJECTED")
        want = expected_requests(context["oracle"], context["config"], context["chain"][block - 1], variant(0))
        check_rows(obs.get("counted_calls"), want, ["fetch"] * len(want), fail, "rejected", context["body_sha256"])
        if has(dump, PIN + key) or has(dump, INPUTS + key + "/closed"):
            fail("the rejected observation kept its pin or closed its log")
        check_completed(context, second, list(range(block, plan["attempts"][1]["to"] + 1)), variant(1), fail, "retry")
    elif kind == "checkpoint-rio":
        check_checkpoint_rio(context, plan, attempts, block, fail)
    else:
        fail(f"unknown scenario kind {kind}")
    return failures


def check_checkpoint_rio(context, plan, attempts, block, fail):
    lost, retry = attempts[0], attempts[1]
    first = plan["attempts"][0]["from"]
    crash = lost["crash_result"]
    check_completed(context, lost_attempt(context, crash, lost["store_after"], first, block), list(range(first, block)),
                    "a", fail, "lost attempt")
    sequence = block - first
    key = key_of(sequence)
    dump = lost["store_after"]
    if state_of(records(dump), sequence) != "ADMITTED" or log_entries(dump, key) != [0, 1]:
        fail("the lost post-Rio observation is not ADMITTED with two durable entries")
    result = retry["result"] or {}
    check_refused_before_recovery(result, fail, "restart")
    want = expected_requests(context["oracle"], context["config"], context["chain"][block - 1], "a")
    check_rows(result.get("recovery_counted_calls"), want, ["replay", "replay"] + ["fetch"] * (len(want) - 2), fail,
               "recovery", context["body_sha256"])
    if (result.get("recovery") or {}).get("exec_ready") != [key] or \
            check_observation(context, retry["store_after"], sequence, block, "a", fail, "recovery") != "EXEC_READY":
        fail("recovery did not resolve the post-Rio obligation from its capsule and saved span reads")
    check_completed(context, retry, list(range(block, plan["attempts"][1]["to"] + 1)), "a", fail, "retry")
    walk = result.get("walk_check") or {}
    if walk.get("block") != plan["attempts"][1]["walk_check"] or walk.get("complete_list_error") != "<nil>" or \
            "unknown ancestor" not in (walk.get("truncated_list_error") or ""):
        fail(f"the native snapshot walk does not stop at the checkpoint and need every header above it: {walk}")
    if "live pins forbid" not in (result.get("set_head_error") or ""):
        fail("native SetHead below a pinned header was not refused")
    final = retry["store_after"]
    for index, reason in ((2, "native store identity mismatch"), (3, "guarded state pruning recovery: live pins forbid")):
        row = attempts[index]
        if reason not in ((row["result"] or {}).get("error") or "") or row["store_after"] != final:
            fail(f"attempt {index} was not refused with {reason!r} before any store change")
    patched, accepted = attempts[4]["prune_state"], attempts[5]["prune_state"]
    if patched.get("refused_live_pins") is not True or patched.get("exit_code") != 1 or attempts[4]["store_after"] != final:
        fail("the patched bor snapshot prune-state did not refuse the live pins")
    if accepted.get("refused_live_pins") is not False:
        fail("the accepted bor binary refused the live pins, so the refusal does not prove the patch")


def checked_compare(name, observed, context):
    try:
        return compare(name, observed, context)
    except (AttributeError, IndexError, KeyError, TypeError, ValueError, Rejected) as error:
        return [f"{name}: malformed observation: {type(error).__name__}: {error}"]


def checkpoints(observed):
    """Observed checkpoint-snapshot items by (block, SHA-256), from the probe's
    item dumps; the judge checks each against the ancestor rule."""
    out = {}
    for row in observed.get("checkpoint_items") or []:
        data = bytes.fromhex(row["data"])
        out[(row["block"], hashlib.sha256(data).hexdigest())] = data
    return out


def load_scenario(directory, deadline):
    from artifacts import load
    scenario = load(directory / "scenario.json", 256 * 1024 * 1024, deadline)
    for index, row in enumerate(scenario.get("attempts") or []):
        for key, name in (("result", f"attempt-{index}.json"), ("crash_result", f"attempt-{index}.json.crash.json")):
            path = directory / name
            row[key] = load(path, 64 * 1024 * 1024, deadline) if path.exists() else None
    return scenario


def run(preparation_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(preparation_path.is_absolute(), "Preparation path must be absolute")
    preparation_ref, preparation = pinned_json(preparation_path, expected_sha, 16 * 1024 * 1024, deadline)
    require(preparation.get("kind") == "bor-admission-probe-preparation" and preparation.get("result") == "PASS"
            and preparation.get("cleanup_complete") is True, "A passing preparation is required")
    manifest_root = Path(preparation["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (preparation_path.parent, manifest_root, ROOT))
    state = capture_probe.source_state()
    with capture_probe.single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "bor-admission-probe-run", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": state, "preparation": preparation_ref,
                  "profile": "private local Bor fixture chain 15001 (POLY private local; not a public profile)",
                  "scenarios": {}, "phases": {"admission_started": time.time()}}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            oracles = [row["copy"] for row in preparation["fixture"] if row["copy"]["path"].endswith("/expected.json")]
            require(len(oracles) == 1, "The preparation must retain exactly one oracle")
            _, oracle = json_artifact(oracles[0], 1024 * 1024, deadline=deadline)
            record["oracle"] = oracles[0]
            runtime = preparation["runtime_image"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
            bodies = {}
            for row in preparation["stub_bodies"]:
                path = artifact(row["copy"], 1024 * 1024, deadline=deadline)
                bodies[path.name] = read_bytes(path, 1024 * 1024, deadline)
            bor = preparation["bor"]
            binary = artifact(bor["probe"], 512 * 1024 * 1024, deadline=deadline)
            artifact(bor["bor"], 512 * 1024 * 1024, deadline=deadline)
            artifact(bor["accepted_binary"], 512 * 1024 * 1024, deadline=deadline)
            genesis = read_bytes(artifact(bor["genesis.json"], 4 * 1024 * 1024, deadline=deadline), 4 * 1024 * 1024, deadline)
            config = read_bytes(artifact(bor["chain-config.json"], 1024 * 1024, deadline=deadline), 1024 * 1024, deadline)
            blocks_path = artifact(preparation["chain"]["blocks"], 256 * 1024 * 1024, deadline=deadline)
            blocks = read_bytes(blocks_path, 256 * 1024 * 1024, deadline)
            genesis_header = read_bytes(artifact(preparation["chain"]["genesis_header"], 1024 * 1024, deadline=deadline),
                                        1024 * 1024, deadline)
            _, generated = json_artifact(preparation["chain"]["generated"], 64 * 1024 * 1024, deadline=deadline)
            derived = derived_inputs(read_source_genesis(preparation, deadline), oracle)
            require(derived == (genesis, config), "The prepared genesis items differ from the independent derivation")
            context = build_context(oracle, bodies, genesis, config, blocks, genesis_header, bor["source_commit"],
                                    generated)
            record["phases"]["admission_finished"] = time.time()
            plans = output / "plans"
            plans.mkdir()
            record["phases"]["execution_started"] = time.time()
            for plan in oracle["plans"]:
                name = plan["name"]
                plan_path = plans / f"{name}.json"
                plan_path.write_text(json.dumps(plan, indent=2) + "\n")
                scenario_started = time.monotonic()
                native.command(runtime,
                               ["/probe/probe", "scenario", "-plan", f"/plans/{name}.json", "-genesis",
                                "/probe/genesis.json", "-config", "/probe/chain-config.json", "-blocks",
                                "/chains/a/blocks.rlp", "-bodies", "/stub", "-commit", bor["source_commit"],
                                "-tools", "/probe", "-out", f"/out/{name}"],
                               [(binary.parent, "/probe", True), (blocks_path.parent.parent, "/chains", True),
                                (Path(preparation["stub_bodies"][0]["copy"]["path"]).parent, "/stub", True),
                                (plans, "/plans", True), (output, "/out", False)],
                               cap=900, memory="4g")
                observed = load_scenario(output / name, deadline)
                context["checkpoints"] = checkpoints(observed_checkpoints(output / name, deadline))
                failures = checked_compare(name, observed, context)
                record["scenarios"][name] = {"ac": plan["ac"], "scenario": reference(output / name / "scenario.json"),
                                             "failures": failures,
                                             "elapsed_seconds": time.monotonic() - scenario_started,
                                             "result": "PASS" if not failures else "FAIL"}
            record["phases"]["execution_finished"] = time.time()
            # An oracle without scenarios proves nothing, so it cannot pass.
            if record["scenarios"] and all(row["result"] == "PASS" for row in record["scenarios"].values()):
                record["result"] = "PASS"
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "run-record.json", record, native, started)
    return output, record


def read_source_genesis(preparation, deadline):
    """The accepted test genesis, read again from the retained source archive."""
    archive = artifact(preparation["bor"]["source_archive"], 512 * 1024 * 1024, deadline=deadline)
    with tarfile.open(archive) as stream:
        member = stream.getmember("tests/bor/testdata/genesis.json")
        data = stream.extractfile(member).read()
    require(hashlib.sha256(data).hexdigest() == preparation["bor"]["source_genesis"]["sha256"],
            "The source genesis differs from the preparation record")
    return data


def observed_checkpoints(directory, deadline):
    from artifacts import load
    path = directory / "checkpoints.json"
    return load(path, 64 * 1024 * 1024, deadline) if path.exists() else {}


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
