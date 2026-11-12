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
import decision
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
# Native tests of the patch set in the Bor packages (tree/**/zz_extractor_*_test.go).
NATIVE_TESTS = ("TestExtractorFreezeThreshold", "TestExtractorGoroutineIDs", "TestExtractorGateOwnerPassesOthersWait",
                "TestExtractorGateOpenWaitsForGatedCalls", "TestExtractorGateNestedCallsDoNotWaitForAnOpeningWindow",
                "TestExtractorGateMisuse", "TestExtractorGateFinalizeOfTheObservedBlockIsTheImport",
                "TestExtractorBlockGateHashesTheHeader", "TestExtractorCreator",
                "TestExtractorGateWaitsForDetachedFetches", "TestExtractorGateOneWindowAtATime",
                "TestExtractorSpanRangeFetchIsCountedFromTheStart", "TestExtractorSpanRangeFetchCountPrecedesDoChan",
                "TestExtractorGateCoversEveryHeimdallEntryPoint",
                # E01-02.decision_bridge (core/zz_extractor_decision_test.go).
                "TestExtractorDecisionImportOrder", "TestExtractorDecisionSideAndCanonical",
                "TestExtractorDecisionNativeRejection", "TestExtractorDecisionIntendFailureWritesNothing",
                "TestExtractorDecisionWithoutDeciderIsNative", "TestExtractorRecoverImportsUnderTheChainLock",
                "TestExtractorMarkBatchOnlyMarksTheArmedBatch", "TestExtractorCommitOnceUsesTheBarrierCommit",
                "TestExtractorRootDurable")
PACKAGE = "github.com/ethereum/go-ethereum"
BODY_FILES = ("span-0.json", "span-1.json", "span-latest.json", "status.json", "clerk-a-from-1.json",
              "clerk-b-from-1.json", "clerk-empty.json")
FIXTURE_FILES = ("expected.json", "ORACLE.md", "main.go")
STUB_FILES = ("stub.go", "stub_test.go")
TESTED_PACKAGES = ("extractor/admission", "extractor/admission/native/obligation", "extractor/source_inputs",
                   "extractor/decision", "extractor/decision/native/decider", "extractor/bordecision",
                   "extractor/capture", "extractor/borimport")
PREPARATION_SECONDS = 3600
RUN_SECONDS = 1800
PRODUCER, RUN = "bor", "run-1"
ADMISSION = "extractor-admission-v1/bor.run-1/"
INPUTS = "extractor-source-inputs-v1/"
PIN = "extractor-state-provider-pin-v1-"
TAG_RESULT = "extractor-bor-native-result-v1"
TAG_REJECTION = "extractor-bor-native-rejection-v1"
DECISION = "extractor-decision-v1/bor.run-1/"
MARKER = "extractor-decision-marker-v1/bor.run-1/"
STORE_ID = "0123456789abcdef0123456789abcdef"
TAG_EVIDENCE = "extractor-bor-decision-evidence-v1"
TAG_DECISION_REJECTION = "extractor-bor-decision-rejection-v1"
CHECKPOINT_INTERVAL = 1024


def go_environment():
    # The external linker writes its temporary files to TMPDIR; the container's
    # /tmp is a small tmpfs, too small for the probe and bor binaries.
    return {"CGO_ENABLED": "1", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64",
            "GOCACHE": "/work/build-cache", "GOMODCACHE": "/modules", "GOPATH": "/work/gopath",
            "GOFLAGS": "-mod=readonly", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
            "GOTMPDIR": "/work/gotmp", "TMPDIR": "/work/gotmp", "GOWORK": "off"}


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
    probe.parent.mkdir(parents=True, exist_ok=True)
    row["probe_sources"] = capture_probe.copy_sources(("main.go",), FIXTURE, probe, deadline)
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
    go(["vet", "./extractor/...", "./consensus/bor", "./internal/cli", f"./{PROBE_DIR}/..."], 1200)
    stdout = go(["test", "-race", "-count=1", "-cover", *[f"./{name}" for name in TESTED_PACKAGES],
                 f"./{PROBE_DIR}/heimdallstub"], 1800)
    row["unit_tests"] = reference(stdout)
    go_test_passed(stdout.read_text(errors="replace"), (*TESTED_PACKAGES, f"{PROBE_DIR}/heimdallstub"))
    stdout = go(["test", "-race", "-count=1", "-run", "^TestExtractor", "-v", "./core/rawdb", "./consensus/bor", "./core"],
                1800)
    row["rawdb_tests"] = reference(stdout)
    text = stdout.read_text(errors="replace")
    go_test_passed(text, ("core/rawdb", "consensus/bor", "core"))
    for name in NATIVE_TESTS:
        require(f"--- PASS: {name} " in text, f"The native test {name} did not pass")
    stdout = go(["list", "-deps", "./cmd/cli"], 600)
    row["shipped_binary_deps"] = reference(stdout)
    require(f"{PACKAGE}/extractor/borwire" in stdout.read_text().split(),
            "The shipped bor binary does not install the retention hooks")
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
               "gas_used": int.from_bytes(block[0][10], "big"), "time": int.from_bytes(block[0][11], "big"),
               "difficulty": int.from_bytes(block[0][7], "big")}
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
    cache = context.setdefault("encoded_ancestors", {})
    if number not in cache:
        cache[number] = rlp_encode([decoded_header(h) for h in ancestors(chain, genesis_header, context["config"], number)])
    oracle = context["oracle"]
    items = (("genesis", context["genesis"]), ("block", row["rlp"]), ("parent-header", parent),
             ("parent-hash", keccak256(parent)), ("parent-root", parent_fields[3]),
             ("ancestor-headers", cache[number]),
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


# ---------------------------------------------------------------------------
# Decision bridge (E01-02.decision_bridge): independent expected decisions.


def decision_records(dump):
    """Decision records by (sequence, kind), and the counters by (name,)."""
    out = {}
    for row in dump or []:
        key = row["key"]
        if key.startswith(DECISION):
            parts = key[len(DECISION):].split("/")
            index = (int(parts[0], 16), parts[1]) if len(parts) == 2 else (parts[0],)
            out[index] = json.loads(row["value"])
    return out


def markers(dump):
    return {int(row["key"][len(MARKER):], 16): row["value"] for row in dump or [] if row["key"].startswith(MARKER)}


def total_difficulty(context, number):
    """Total difficulty of block `number`: the sum of the header difficulties from genesis."""
    total = int.from_bytes(decoded_header(context["genesis_header"])[7], "big")
    for row in context["chain"][:number]:
        total += row["difficulty"]
    return total


def head_of(context, number):
    header = context["genesis_header"] if number == 0 else context["chain"][number - 1]["header"]
    return {"number": number, "hash": keccak256(header).hex()}


def expected_intent(context, sequence, number, key, ready_digest):
    """The IMPORT intent of block `number` onto its parent, the head (README of
    adapters/bor/decision/native): the member binds the EXEC_READY digest and
    the state root of the block; the evidence binds the fork choice inputs."""
    block, old = head_of(context, number), head_of(context, number - 1)
    child = context["chain"][number - 1]["state_root"].hex()
    evidence = tagged_digest(TAG_EVIDENCE, "IMPORT", str(number), block["hash"], child, str(number - 1), old["hash"],
                             str(number), block["hash"], str(total_difficulty(context, number - 1)),
                             str(total_difficulty(context, number)))
    record = {"native_store_id": STORE_ID, "producer": f"{PRODUCER}.{RUN}", "sequence": sequence, "kind": "IMPORT",
              "old_head": old, "new_head": block,
              "members": [{"observation": key, "block": block["hash"], "exec_ready_digest": ready_digest,
                           "child_root": child}],
              "evidence_digest": evidence, "events": True}
    record["digest"] = decision.intent_digest(record)
    return record


def intents_naming(dump, key):
    recs = decision_records(dump)
    return sorted(seq for (seq, *kind), value in recs.items() if kind == ["intent"] and
                  any(m.get("observation") == key for m in value.get("members") or []))


def check_decision(context, dump, number, key, ready_digest, fail, label, outcome="DECIDED"):
    """Block `number` has exactly one decision, over observation `key` with
    its EXEC_READY digest: the IMPORT intent computed here, and either
    NATIVE_DECIDED (BATCH, its marker present with exactly the intent bytes:
    no mirror applied it) or the native rejection with its evidence digest and
    no marker. The observation is closed: no pin, capsule or log remains, and
    an admission record that remains is the DECIDED record with the decision
    digest. Returns the decision sequence."""
    found = intents_naming(dump, key)
    if len(found) != 1:
        fail(f"{label}: block {number} has decisions {found} over {key}, expected one")
        return None
    seq = found[0]
    recs = decision_records(dump)
    intent = recs[(seq, "intent")]
    want = expected_intent(context, seq, number, key, ready_digest)
    if intent != want:
        fail(f"{label}: the intent of block {number} differs from the independent IMPORT intent: {intent}")
    decided, rejected, marker = recs.get((seq, "decided")), recs.get((seq, "rejected")), markers(dump).get(seq)
    if outcome == "DECIDED":
        record = {"sequence": seq, "intent_digest": want["digest"], "delivery": "BATCH",
                  "digest": decision.decided_digest(want["digest"], "BATCH")}
        if decided != record or rejected is not None:
            fail(f"{label}: block {number} is not NATIVE_DECIDED with one BATCH obligation: {decided} {rejected}")
        if marker != decision.marker_value(want).decode():
            fail(f"{label}: the native marker of block {number} is not the intent bytes")
        closing = record["digest"]
    else:
        evidence = tagged_digest(TAG_DECISION_REJECTION, want["digest"], context["oracle"]["decision_rejection_error"])
        record = {"sequence": seq, "intent_digest": want["digest"], "evidence_digest": evidence,
                  "digest": decision.rejected_digest(want["digest"], evidence)}
        if rejected != record or decided is not None or marker is not None:
            fail(f"{label}: block {number} is not a native rejection without a marker: {rejected} {decided} {marker}")
        closing = record["digest"]
    seqhex = key.rsplit(".", 1)[1]
    admitted = records(dump)
    left = admitted.get((seqhex, "decided"))
    if has(dump, PIN + key) or any(row["key"].startswith(INPUTS + key + "/") for row in dump) or \
            (left is not None and left.get("decision_digest") != closing) or \
            any(len(index) == 2 and index[0] == seqhex and index[1] != "decided" for index in admitted):
        fail(f"{label}: the decided observation {key} kept a pin, inputs or another admission record")
    return seq


def check_decisions_once(context, dump, fail, label, result=None):
    """One decision per block in the store, and one delivery obligation per
    NATIVE_DECIDED BATCH decision (the bridge's list, when given)."""
    recs = decision_records(dump)
    blocks = [m["block"] for (seq, *kind), value in recs.items() if kind == ["intent"]
              for m in value.get("members") or []]
    if len(blocks) != len(set(blocks)):
        fail(f"{label}: a block has two decisions")
    if result is not None:
        batch = sorted(seq for (seq, *kind), value in recs.items() if kind == ["decided"] and value["delivery"] == "BATCH")
        if sorted(result.get("obligations") or []) != batch:
            fail(f"{label}: the obligations {result.get('obligations')} are not the BATCH decisions {batch}")


def snapshot_of(result, key):
    return ((result or {}).get("snapshots") or {}).get(key)


def ready_of(snapshot, key):
    ready = records((snapshot or {}).get("rows") or []).get((key.rsplit(".", 1)[1], "exec-ready"))
    return (ready or {}).get("digest")


def check_completed(context, attempt, numbers, variant, fail, label):
    """Every listed block was observed in this attempt, reached EXEC_READY with
    the expected counted calls fetched live (checked on the snapshot taken at
    its EXEC_READY sync), was imported, and has exactly one IMPORT decision."""
    result, dump = attempt["result"] or {}, attempt["store_after"]
    observed = observations_of(result)
    for number in numbers:
        obs = observed.get(number)
        if obs is None or obs.get("error") or not obs.get("key"):
            fail(f"{label}: block {number} was not observed and imported")
            continue
        key = obs["key"]
        sequence = int(key.rsplit(".", 1)[1], 16)
        want = expected_requests(context["oracle"], context["config"], context["chain"][number - 1], variant)
        check_rows(obs.get("counted_calls"), want, ["fetch"] * len(want), fail, f"{label}: block {number}",
                   context["body_sha256"])
        snap = snapshot_of(result, key)
        if snap is None or check_observation(context, snap["rows"], sequence, number, variant, fail,
                                             f"{label}: block {number}") != "EXEC_READY":
            fail(f"{label}: block {number} did not reach EXEC_READY")
            continue
        check_decision(context, dump, number, key, ready_of(snap, key), fail, f"{label}: block {number}")
    if numbers:
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


def check_decision_recovery(result, fail, label, **want):
    report = (result or {}).get("decision_recovery")
    expected = {"resolved": [], "completed": [], "rejected": [], "imported": [], **want}
    if report != expected:
        fail(f"{label}: the decision recovery did {report}, expected {expected}")


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
        check_lost_obligation(context, plan, attempts, block, kind, variant, fail)
    elif kind == "native-rejection":
        first, second = attempts
        result, dump = first["result"] or {}, first["store_after"]
        sequence, key = block - 1, key_of(block - 1)
        obs = observations_of(result).get(block) or {}
        snap = snapshot_of(result, key) or {}
        rejected = records(snap.get("rows") or []).get((f"{sequence:016x}", "rejected"))
        if "invalid merkle root" not in (obs.get("error") or "") or result.get("inserted") != block - 1:
            fail("native validation did not refuse the block with the changed state sync response")
        if not rejected or rejected.get("evidence_digest") != rejection_digest(key, context["chain"][block - 1]["hash"].hex(),
                                                                                obs.get("error") or ""):
            fail("NATIVE_REJECTED does not bind the native rejection evidence")
        if check_observation(context, snap.get("rows") or [], sequence, block, variant(0), fail, "rejected") != \
                "NATIVE_REJECTED":
            fail("the refused observation is not NATIVE_REJECTED")
        want = expected_requests(context["oracle"], context["config"], context["chain"][block - 1], variant(0))
        check_rows(obs.get("counted_calls"), want, ["fetch"] * len(want), fail, "rejected", context["body_sha256"])
        if has(dump, PIN + key) or any(row["key"].startswith(INPUTS + key + "/") for row in dump) or intents_naming(dump, key):
            fail("the rejected observation kept its pin or inputs, or has a decision")
        check_completed(context, first, list(range(1, block)), variant(0), fail, "first attempt")
        check_completed(context, second, list(range(block, plan["attempts"][1]["to"] + 1)), variant(1), fail, "retry")
        check_decisions_once(context, second["store_after"], fail, "retry", second["result"])
    elif kind == "checkpoint-rio":
        check_checkpoint_rio(context, plan, attempts, block, fail)
    elif kind == "rpc-window":
        check_rpc_window(context, plan, attempts, block, fail)
    elif kind == "prune-block":
        check_prune_block(context, plan, attempts, fail)
    elif kind == "prune-block-frozen":
        check_prune_block_frozen(context, plan, attempts, fail)
    elif kind == "decision-crash":
        check_decision_crash(context, plan, attempts, block, fail)
    elif kind == "decision-rejected":
        check_decision_rejected(context, plan, attempts, block, fail)
    elif kind == "known-block":
        check_known_block(context, plan, attempts, fail)
    else:
        fail(f"unknown scenario kind {kind}")
    return failures


def check_lost_obligation(context, plan, attempts, block, kind, variant, fail):
    """AC-CAP-02-01, AC-CAP-02-02, and with the decision bridge AC-CAP-07-01
    (loss of a partly written corpus before EXEC_READY: no decision exists for
    it, recovery reuses the saved obligation, and the native import of the
    block completes in RECOVERY_ONLY with one decision) and AC-CAP-07-02 (loss
    after EXEC_READY with the child state volatile: native-only replay from the
    capsule and the closed log, the same EXEC_READY record bound by the
    decision)."""
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
    if intents_naming(dump, key):
        fail("the lost observation has a decision before the restart")
    result = retry["result"] or {}
    if result.get("pending_at_open") != [f"{key} {want_state}"]:
        fail(f"the restart did not reopen the saved run with its obligation: {result.get('pending_at_open')}")
    recovery = result.get("recovery") or {}
    after = retry["store_after"]
    want = expected_requests(context["oracle"], context["config"], context["chain"][block - 1], variant(0))
    if kind == "lost-before-admitted":
        check_refused_before_recovery(result, fail, "restart")
        if recovery.get("abandoned") != [key] or recovery.get("exec_ready") or recovery.get("rejected"):
            fail(f"recovery did not abandon only the INPUT_PREPARING observation: {recovery}")
        snap = snapshot_of(result, key) or {}
        if state_of(records(snap.get("rows") or []), sequence) != "ABANDONED" or has(after, PIN + key) or \
                any(row["key"].startswith(INPUTS + key + "/") for row in after) or intents_naming(after, key):
            fail("the abandoned observation kept a pin or inputs, has a decision, or was not ABANDONED")
        check_decision_recovery(result, fail, "restart")
        check_completed(context, retry, list(range(block, plan["attempts"][1]["to"] + 1)), variant(1), fail, "retry")
    else:
        if kind == "lost-after-admitted":
            check_refused_before_recovery(result, fail, "restart")
            if recovery.get("exec_ready") != [key] or recovery.get("abandoned") or recovery.get("rejected"):
                fail(f"recovery did not resolve the ADMITTED obligation to EXEC_READY: {recovery}")
            origins = ["replay"] * len(want_saved) + ["fetch"] * (len(want) - len(want_saved))
            check_rows((result.get("recovery_counted_calls") or [])[:len(want)], want, origins, fail, "recovery",
                       context["body_sha256"])
            changed = context["body_sha256"].get(plan.get("changed_body", ""))
            if changed and any(r.get("response_sha256") == changed for r in result.get("recovery_counted_calls") or []):
                fail("the changed external response reached recovery")
            snap = snapshot_of(result, key) or {}
            if records(snap.get("rows") or []).get((f"{sequence:016x}", "admitted")) != \
                    records(dump).get((f"{sequence:016x}", "admitted")):
                fail("recovery changed the ADMITTED record")
            if check_observation(context, snap.get("rows") or [], sequence, block, variant(0), fail, "recovery") != \
                    "EXEC_READY":
                fail("the recovered obligation is not EXEC_READY")
            ready = ready_of(snap, key)
            # The obligation's replay rows come first; the completion replays
            # the now closed log again before the native import.
            rows = result.get("recovery_counted_calls") or []
            check_rows(rows[len(want):], want, ["replay"] * len(want), fail, "completion replay", context["body_sha256"])
        else:
            if recovery.get("exec_ready") or recovery.get("abandoned") or key not in (recovery.get("kept") or []):
                fail(f"recovery did not keep the EXEC_READY observation for the decision bridge: {recovery}")
            # AC-CAP-07-02: the child state was volatile; the completion
            # replays the capsule and closed log natively, fetching nothing.
            if (result.get("child_roots_at_open") or {}).get(key) is not False:
                fail(f"the child state of the lost block was durable at the restart: {result.get('child_roots_at_open')}")
            check_rows(result.get("recovery_counted_calls"), want, ["replay"] * len(want), fail, "completion replay",
                       context["body_sha256"])
            ready = (records(dump).get((f"{sequence:016x}", "exec-ready")) or {}).get("digest")
        if block in (result.get("stored_at_open") or []) or result.get("head_number_at_open") != block - 1:
            fail("the lost block was stored before the recovery")
        seq = check_decision(context, after, block, key, ready, fail, "recovery")
        check_decision_recovery(result, fail, "restart",
                                imported=[{"observation": key, "sequence": seq, "outcome": "NATIVE_DECIDED"}])
        # The recovery imported the block; the live import does not observe it again.
        if block in observations_of(result):
            fail("the block decided in recovery was observed again by the live import")
        check_completed(context, retry, list(range(block + 1, plan["attempts"][1]["to"] + 1)), variant(1), fail,
                        "retry")
        if len(attempts) > 2:
            third = attempts[2]["result"] or {}
            if third.get("observations") or third.get("head_hash") != "0x" + context["chain"][
                    plan["attempts"][2]["to"] - 1]["hash"].hex():
                fail("the third attempt observed a block again or moved the head")
    check_decisions_once(context, attempts[-1]["store_after"], fail, "final", attempts[-1]["result"])


def check_decision_crash(context, plan, attempts, block, fail):
    """AC-CAP-07-02 and AC-CAP-07-03: a loss at one step of the decision of
    `block` after its EXEC_READY. The restart settles it in RECOVERY_ONLY from
    native evidence: before the intent, the import completes with a new
    decision; with an intent and no marker (before or inside the native
    mutation), native recovery completes exactly the saved transition; with
    the marker, the intent is resolved; with the decided record, the opening
    closes the observation. In every case the block has one decision, the
    same EXEC_READY record, and one obligation."""
    lost, retry = attempts[0], attempts[1]
    crash, dump = lost["crash_result"], lost["store_after"]
    check_completed(context, lost_attempt(context, crash, dump, 1, block), list(range(1, block)), "a", fail,
                    "lost attempt")
    sequence, key, step = block - 1, key_of(block - 1), plan["crash_step"]
    snap = snapshot_of(crash, key)
    if snap is None or check_observation(context, snap["rows"], sequence, block, "a", fail, "lost block") != "EXEC_READY":
        fail("the lost block did not reach EXEC_READY before the loss")
        return
    ready = ready_of(snap, key)
    recs, marks = decision_records(dump), markers(dump)
    found = intents_naming(dump, key)
    seq = block - 1
    want_intent = expected_intent(context, seq, block, key, ready)
    result = retry["result"] or {}
    stored, head = block in (result.get("stored_at_open") or []), result.get("head_number_at_open")
    if step == "child-root":
        if found or state_of(records(dump), sequence) != "EXEC_READY":
            fail("after a loss at the child-root barrier the block has a decision or is not EXEC_READY")
        if (result.get("child_roots_at_open") or {}).get(key) is not True or stored or head != block - 1:
            fail("the child state was not durable, or the block was stored, before the intent")
        report = {"imported": [{"observation": key, "sequence": seq, "outcome": "NATIVE_DECIDED"}]}
    else:
        if found != [seq] or recs.get((seq, "intent")) != want_intent:
            fail(f"after a loss at {step} the saved intent is not the independent intent: {found}")
        decided = recs.get((seq, "decided"))
        marker = marks.get(seq)
        want_marker = {"intent": None, "block-batch": None}.get(step, decision.marker_value(want_intent).decode())
        if marker != want_marker or (decided is not None) != (step == "decided"):
            fail(f"after a loss at {step} the marker is {marker!r} and the decided record {decided}")
        want_stored = step in ("block-batch", "marker", "decided")
        want_head = block if step in ("marker", "decided") else block - 1
        if stored != want_stored or head != want_head:
            fail(f"after a loss at {step} the block is stored {stored} with head {head}, expected {want_stored} "
                 f"with head {want_head}")
        report = {"intent": {"completed": [seq]}, "block-batch": {"completed": [seq]}, "marker": {"resolved": [seq]},
                  "decided": {}}[step]
        if step == "decided" and result.get("pending_at_open") != [f"{key} EXEC_READY"]:
            fail(f"the decided observation was not open at the restart: {result.get('pending_at_open')}")
    check_decision_recovery(result, fail, "restart", **report)
    want = expected_requests(context["oracle"], context["config"], context["chain"][block - 1], "a")
    replays = ["replay"] * len(want) if step in ("child-root", "intent", "block-batch") else []
    check_rows(result.get("recovery_counted_calls"), want if replays else [], replays, fail, "recovery",
               context["body_sha256"])
    after = retry["store_after"]
    if check_decision(context, after, block, key, ready, fail, "recovery") != seq:
        fail("the recovered decision does not have the saved sequence")
    if block in observations_of(result):
        fail("the decided block was observed again")
    check_completed(context, retry, list(range(block + 1, plan["attempts"][1]["to"] + 1)), "a", fail, "retry")
    check_decisions_once(context, after, fail, "final", result)


def check_decision_rejected(context, plan, attempts, block, fail):
    """AC-CAP-07-06: a whitelisted milestone that conflicts with `block`
    arrives after its EXEC_READY; the native check refuses the write after the
    intent. Live, the intent is rejected at once; after a loss before
    RejectIntent, the restart repeats the native check from the saved inputs
    (the milestone is stored natively) and records the rejection. Either way:
    no marker, no NATIVE_DECIDED record and no obligation for the block, the
    head stays below it, and the observation is closed."""
    key, seq = key_of(block - 1), block - 1
    first = attempts[0]
    crash = first["crash_result"] if plan["attempts"][0]["crash"] else first["result"]
    check_completed(context, lost_attempt(context, crash, first["store_after"], 1, block), list(range(1, block)), "a",
                    fail, "first attempt")
    snap = snapshot_of(crash, key)
    if snap is None or check_observation(context, snap["rows"], seq, block, "a", fail, "refused block") != "EXEC_READY":
        fail("the refused block did not reach EXEC_READY")
        return
    ready = ready_of(snap, key)
    if plan["attempts"][0]["crash"]:
        dump = first["store_after"]
        want_intent = expected_intent(context, seq, block, key, ready)
        recs = decision_records(dump)
        if intents_naming(dump, key) != [seq] or recs.get((seq, "intent")) != want_intent or \
                recs.get((seq, "rejected")) or recs.get((seq, "decided")) or seq in markers(dump):
            fail("after the loss the refused intent is not saved unresolved without a marker")
        final = attempts[1]
        result = final["result"] or {}
        if block in (result.get("stored_at_open") or []) or result.get("head_number_at_open") != block - 1:
            fail("the refused block was stored before the recovery")
        check_decision_recovery(result, fail, "restart", rejected=[seq])
        want = expected_requests(context["oracle"], context["config"], context["chain"][block - 1], "a")
        check_rows(result.get("recovery_counted_calls"), want, ["replay"] * len(want), fail, "recovery",
                   context["body_sha256"])
        if block in observations_of(result):
            fail("the refused block opened a new observation after the restart")
    else:
        final = first
        result = final["result"] or {}
    if context["oracle"]["decision_rejection_error"] not in (result.get("import_error") or "") or \
            result.get("head_hash") != "0x" + context["chain"][block - 2]["hash"].hex():
        fail(f"the refused block was imported or the import did not stop: {result.get('import_error')}")
    check_decision(context, final["store_after"], block, key, ready, fail, "rejection", outcome="REJECTED")
    check_decisions_once(context, final["store_after"], fail, "final", result)
    if seq in (result.get("obligations") or []):
        fail("the rejected intent has a delivery obligation")


def check_known_block(context, plan, attempts, fail):
    """One decision per block after a restart (D100): the native import of a
    block already in the chain with its state opens no observation and no
    decision."""
    first, second = attempts
    numbers = list(range(plan["attempts"][0]["from"], plan["attempts"][0]["to"] + 1))
    check_completed(context, first, numbers, "a", fail, "import")
    result = second["result"] or {}
    if result.get("observations") or result.get("reimport_error") or \
            result.get("stored_at_open") != numbers or result.get("head_number_at_open") != numbers[-1]:
        fail(f"the re-import of stored blocks opened an observation or failed: {result.get('observations')} "
             f"{result.get('reimport_error')}")
    before, after = first["store_after"], second["store_after"]
    counters = lambda dump: (value(dump, ADMISSION + "counter"), decision_records(dump).get(("counter",)))
    if counters(after) != counters(before) or decision_records(after) != decision_records(before) or \
            markers(after) != markers(before):
        fail("the re-import of stored blocks changed an admission or decision record")
    check_decision_recovery(result, fail, "restart")
    check_decisions_once(context, after, fail, "final", result)


def check_checkpoint_rio(context, plan, attempts, block, fail):
    """Post-Rio loss with two saved span reads (AC-CAP-02-02, AC-CAP-04-03),
    the native snapshot walk, and the retention guards while a pin is live:
    SetHead before the recovery (the saved obligation keeps its pin), then a
    loss after the EXEC_READY of the last block keeps that pin live for the
    store identity, startup prune recovery and prune-state refusals."""
    lost, second = attempts[0], attempts[1]
    first = plan["attempts"][0]["from"]
    last = plan["attempts"][1]["to"]
    crash = lost["crash_result"]
    check_completed(context, lost_attempt(context, crash, lost["store_after"], first, block), list(range(first, block)),
                    "a", fail, "lost attempt")
    sequence = block - first
    key = key_of(sequence)
    dump = lost["store_after"]
    if state_of(records(dump), sequence) != "ADMITTED" or log_entries(dump, key) != [0, 1]:
        fail("the lost post-Rio observation is not ADMITTED with two durable entries")
    walk = crash.get("walk_check") or {}
    if walk.get("block") != plan["attempts"][0]["walk_check"] or walk.get("complete_list_error") != "<nil>" or \
            "unknown ancestor" not in (walk.get("truncated_list_error") or ""):
        fail(f"the native snapshot walk does not stop at the checkpoint and need every header above it: {walk}")
    result = second["crash_result"] or {}
    check_refused_before_recovery(result, fail, "restart")
    if "live pins forbid" not in (result.get("set_head_error") or ""):
        fail("native SetHead below a pinned header was not refused")
    want = expected_requests(context["oracle"], context["config"], context["chain"][block - 1], "a")
    rows = result.get("recovery_counted_calls") or []
    check_rows(rows[:len(want)], want, ["replay", "replay"] + ["fetch"] * (len(want) - 2), fail, "recovery",
               context["body_sha256"])
    check_rows(rows[len(want):], want, ["replay"] * len(want), fail, "completion replay", context["body_sha256"])
    snap = snapshot_of(result, key)
    if (result.get("recovery") or {}).get("exec_ready") != [key] or snap is None or \
            check_observation(context, snap["rows"], sequence, block, "a", fail, "recovery") != "EXEC_READY":
        fail("recovery did not resolve the post-Rio obligation from its capsule and saved span reads")
        return
    seq = check_decision(context, second["store_after"], block, key, ready_of(snap, key), fail, "recovery")
    check_decision_recovery(result, fail, "restart",
                            imported=[{"observation": key, "sequence": seq, "outcome": "NATIVE_DECIDED"}])
    check_completed(context, lost_attempt(context, result, second["store_after"], first, last),
                    list(range(block + 1, last)), "a", fail, "second attempt")
    pinned = key_of(last - first)
    final = second["store_after"]
    if state_of(records(final), last - first) != "EXEC_READY" or not has(final, PIN + pinned):
        fail(f"the loss after the EXEC_READY of block {last} did not keep its pin live")
    for index, reason in ((2, "native store identity mismatch"), (3, "guarded state pruning recovery: live pins forbid")):
        row = attempts[index]
        if reason not in ((row["result"] or {}).get("error") or "") or row["store_after"] != final:
            fail(f"attempt {index} was not refused with {reason!r} before any store change")
    patched, accepted = attempts[4]["prune_state"], attempts[5]["prune_state"]
    if patched.get("refused_live_pins") is not True or patched.get("exit_code") != 1 or attempts[4]["store_after"] != final:
        fail("the patched bor snapshot prune-state did not refuse the live pins")
    if accepted.get("refused_live_pins") is not False:
        fail("the accepted bor binary refused the live pins, so the refusal does not prove the patch")
    end = attempts[6]
    ready = (records(final).get((f"{last - first:016x}", "exec-ready")) or {}).get("digest")
    seq = check_decision(context, end["store_after"], last, pinned, ready, fail, "final recovery")
    check_decision_recovery(end["result"], fail, "final restart",
                            imported=[{"observation": pinned, "sequence": seq, "outcome": "NATIVE_DECIDED"}])
    check_decisions_once(context, end["store_after"], fail, "final", end["result"])


def request_path(request):
    """The URL path of a counted request identity `GET <path>?<query>`."""
    return request.split(" ", 1)[1].split("?", 1)[0]


def check_rpc_window(context, plan, attempts, block, fail):
    """D100 item 1: a bor RPC call sent while the window of `block` is open
    waits at the window gate until the window closes, and a reader already in
    a Heimdall read when a window starts finishes before the window routes
    reads to the log, so each observation's log holds only the import's own
    reads (check_completed binds them to the independent consumed digest);
    every EXEC_READY then replays from its saved inputs alone; and the same
    RPC call with cold caches outside every window reads Heimdall through the
    native client."""
    step, row = plan["attempts"][0], attempts[0]
    numbers = list(range(step["from"], step["to"] + 1))
    check_completed(context, row, numbers, "a", fail, "rpc attempt")
    result = row["result"] or {}
    oracle, config, chain = context["oracle"], context["config"], context["chain"]
    rpc = result.get("rpc_in_window") or {}
    first = expected_requests(oracle, config, chain[block - 1], "a")[0]["request"]
    if rpc.get("block") != block or rpc.get("target") != block - 1 or rpc.get("trigger_request") != first:
        fail(f"the RPC call was not started by the first Heimdall read of the window of block {block}: {rpc}")
    if rpc.get("waiting_at_gate") is not True or not (rpc.get("waiting_goroutine") or "").startswith("goroutine ") or \
            rpc.get("returned_in_window") is not False:
        fail(f"the RPC call did not wait at the window gate until the window closed: {rpc}")
    if rpc.get("error") or rpc.get("snapshot_number") != block - 1 or \
            rpc.get("snapshot_hash") != "0x" + chain[block - 2]["hash"].hex():
        fail(f"the RPC call did not return the native snapshot of block {block - 1}: {rpc}")
    # A reader already inside a Heimdall read when the window of `rpc_before`
    # starts: the window waits for it, so none of its reads is in the log of
    # that block (check_completed above).
    before, at = result.get("rpc_before_window") or {}, step["rpc_before"]
    rio_paths = {request_path(call["request"]) for call in oracle["requests"]["rio_header_check"]}
    held = before.get("held_request") or ""
    if before.get("block") != at or before.get("target") != at - 1 or before.get("held_at_observe") is not True or \
            not held.startswith("GET ") or request_path(held) not in rio_paths:
        fail(f"no counted Heimdall read was in flight when the window of block {at} started: {before}")
    if before.get("error") or before.get("snapshot_number") != at - 1 or \
            before.get("snapshot_hash") != "0x" + chain[at - 2]["hash"].hex():
        fail(f"the RPC call in flight at the window of block {at} did not return the native snapshot: {before}")
    # A native span range lookup with an expired context just before the
    # window of `expired_fetch`: the lookup returns at once, its detached
    # fetch of span 1 is in flight when the import starts, and the window
    # waits for it, so the read is in no log (check_completed above).
    expired, at = result.get("expired_fetch") or {}, step["expired_fetch"]
    if expired.get("block") != at or "context canceled" not in (expired.get("lookup_error") or "") or \
            expired.get("held_request") != "GET /bor/spans/1" or expired.get("held_at_observe") is not True:
        fail(f"no detached span fetch of an expired lookup was in flight when the window of block {at} started: "
             f"{expired}")
    # Each observation replays from its saved inputs right after its
    # EXEC_READY, before its decision closes it.
    replays = result.get("replays") or []
    keys = [observations_of(result).get(number, {}).get("key") for number in numbers]
    if [r.get("key") for r in replays] != keys:
        fail(f"the replays do not cover every EXEC_READY observation: {[r.get('key') for r in replays]}")
    for replay in replays:
        number = step["from"] + int(replay["key"].rsplit(".", 1)[1], 16)
        if replay.get("error"):
            fail(f"the EXEC_READY of block {number} does not replay: {replay['error']}")
        want = expected_requests(oracle, config, chain[number - 1], "a")
        check_rows(replay.get("counted_calls"), want, ["replay"] * len(want), fail, f"replay of block {number}",
                   context["body_sha256"])
    # Other non-import readers (the native block producer) may read the same
    # paths in that interval, so each path must be read at least once.
    control = result.get("rpc_control") or {}
    target = step["rpc_control"]
    want_paths = {request_path(call["request"]) for call in oracle["requests"]["rio_header_check"]}
    passed = control.get("passed_heimdall_calls") or {}
    if control.get("error") or control.get("snapshot_number") != target or \
            control.get("snapshot_hash") != "0x" + chain[target - 1]["hash"].hex() or \
            any(passed.get(path, 0) < 1 for path in want_paths):
        fail(f"the RPC call with cold caches outside a window did not read {sorted(want_paths)} "
             f"through the native client: {control}")


def check_prune_block(context, plan, attempts, fail):
    """D100 item 1: the patched `bor snapshot prune-block` refuses while a pin
    is live, before any native step and without a store change; the accepted
    binary does not refuse (it reaches the native pruner), so the refusal comes
    from the patch; without live pins the patched binary reaches the native
    pruner too."""
    want = context["oracle"]["prune_block"]
    first = attempts[0]
    numbers = list(range(plan["attempts"][0]["from"], plan["attempts"][0]["to"] + 1))
    tool = lambda index: attempts[index]["prune_state"]
    tail = lambda index: tool(index).get("output_tail") or ""
    if plan["pins"]:
        # A loss right after the EXEC_READY of the last block keeps its pin
        # live: decided blocks hold none.
        last = numbers[-1]
        check_completed(context, lost_attempt(context, first["crash_result"], first["store_after"], numbers[0], last),
                        numbers[:-1], "a", fail, "import")
        pins = [row["key"] for row in first["store_after"] if row["key"].startswith(PIN)]
        if pins != [PIN + key_of(last - numbers[0])]:
            fail(f"the live pins before the prune are {pins}, expected the pin of block {last}")
        patched = attempts[1]
        if tool(1).get("command") != "prune-block" or tool(1).get("refused_live_pins") is not True or \
                want["refusal"] not in tail(1) or want["native_error"] in tail(1):
            fail(f"the patched bor snapshot prune-block did not refuse the live pins first: {tool(1)}")
        if patched["store_before"] != first["store_after"] or patched["store_after"] != patched["store_before"] or \
                patched["ancient_after"] != patched["ancient_before"]:
            fail("the refused prune-block changed the store or the ancient directory")
        if tool(2).get("command") != "prune-block" or tool(2).get("refused_live_pins") is not False or \
                want["native_error"] not in tail(2):
            fail(f"the accepted bor binary did not reach the native pruner, so the refusal does not prove the patch: "
                 f"{tool(2)}")
    else:
        result = first["result"] or {}
        if result.get("observations") or any(row["key"].startswith(PIN) for row in first["store_after"]) or \
                result.get("inserted") != len(numbers) or \
                result.get("head_hash") != "0x" + context["chain"][numbers[-1] - 1]["hash"].hex():
            fail("the unobserved import left an observation or a pin, or did not import the range")
        if tool(1).get("command") != "prune-block" or tool(1).get("refused_live_pins") is not False or \
                want["refusal"] in tail(1) or want["native_error"] not in tail(1):
            fail(f"without live pins the patched prune-block did not reach the native pruner: {tool(1)}")


def check_prune_block_frozen(context, plan, attempts, fail):
    """D100 item 1 with a non-empty freezer: the native freezer freezes the
    blocks up to the finalized block `finalize`, then a loss right after the
    EXEC_READY of the next block keeps its pin live; the patched prune-block
    refuses for that pin before any native step, with no store or ancient
    change. Control: the accepted binary reaches its native store open, which
    in this source cannot open a store whose freezer holds blocks
    (OpenDatabaseWithFreezer drops its ancient argument), so it stops there
    and prunes nothing. The final restart decides the lost block."""
    want = context["oracle"]["prune_block"]
    steps = plan["attempts"]
    first = attempts[0]
    check_completed(context, first, list(range(steps[0]["from"], steps[0]["to"] + 1)), "a", fail, "import")
    lost = attempts[1]
    frozen = (lost["crash_result"] or {}).get("pins_after_freeze") or {}
    if (frozen.get("ancients") or 0) <= plan["finalize"] or any(p.get("verify_error") for p in frozen.get("pins") or []):
        fail(f"the native freezer did not freeze the finalized blocks: {frozen}")
    block = steps[1]["from"]
    key = key_of(block - 1)
    if [row["key"] for row in lost["store_after"] if row["key"].startswith(PIN)] != [PIN + key] or \
            state_of(records(lost["store_after"]), block - 1) != "EXEC_READY":
        fail(f"the loss after the EXEC_READY of block {block} did not keep its pin live")
    tool = lambda index: attempts[index]["prune_state"]
    tail = lambda index: tool(index).get("output_tail") or ""
    patched = attempts[2]
    if tool(2).get("command") != "prune-block" or tool(2).get("refused_live_pins") is not True or \
            want["refusal"] not in tail(2) or want["native_frozen_open_error"] in tail(2):
        fail(f"the patched prune-block did not refuse the live pins over a non-empty freezer: {tool(2)}")
    if patched["store_before"] != lost["store_after"] or patched["store_after"] != patched["store_before"] or \
            patched["ancient_after"] != patched["ancient_before"] or not patched["ancient_before"]:
        fail("the refused prune-block changed the store or the ancient directory, or the freezer was empty")
    accepted = attempts[3]
    if tool(3).get("refused_live_pins") is not False or want["native_frozen_open_error"] not in tail(3) or \
            accepted["ancient_after"] != accepted["ancient_before"]:
        fail(f"the accepted bor binary did not stop at its native store open over the frozen store: {tool(3)}")
    end = attempts[4]
    after = (end["result"] or {}).get("pins_after_freeze") or {}
    if after.get("pins") is None or any(p.get("verify_error") for p in after["pins"]):
        fail(f"a pin is unreadable after the prune attempts: {after}")
    ready = (records(lost["store_after"]).get((f"{block - 1:016x}", "exec-ready")) or {}).get("digest")
    seq = check_decision(context, end["store_after"], block, key, ready, fail, "final recovery")
    check_decision_recovery(end["result"], fail, "final restart",
                            imported=[{"observation": key, "sequence": seq, "outcome": "NATIVE_DECIDED"}])
    check_decisions_once(context, end["store_after"], fail, "final", end["result"])


def checked_compare(name, observed, context):
    try:
        return compare(name, observed, context)
    except (AttributeError, IndexError, KeyError, TypeError, ValueError, Rejected) as error:
        return [f"{name}: malformed observation: {type(error).__name__}: {error}"]


def checkpoints(observed, scenario=None):
    """Observed checkpoint-snapshot items by (block, SHA-256), from the probe's
    item dumps of the capsules left at the end and of the EXEC_READY
    snapshots (a decision deletes the capsule); the judge checks each against
    the ancestor rule."""
    out = {}
    rows = list(observed.get("checkpoint_items") or [])
    for attempt in (scenario or {}).get("attempts") or []:
        for result in (attempt.get("result"), attempt.get("crash_result")):
            for snap in ((result or {}).get("snapshots") or {}).values():
                if snap.get("block"):
                    rows.append({"block": snap["block"], "data": snap.get("checkpoint_item") or ""})
    for row in rows:
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
                context["checkpoints"] = checkpoints(observed_checkpoints(output / name, deadline), observed)
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
