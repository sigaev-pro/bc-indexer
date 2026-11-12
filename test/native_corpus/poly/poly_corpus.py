#!/usr/bin/env python3
"""Build and run the POLY (Bor) block of E01-03.smoke_corpus.

`prepare` installs the accepted E01-02.admission and E01-02.decision_bridge
Bor blocks into the accepted Bor source tree (test/harness/bor_admission_tree.py),
copies the corpus probe (main.go) and the Heimdall stub into it, and builds
the probe offline with the accepted compiler. It derives the corpus genesis:
the accepted test genesis of the Bor source with only the corpus accounts of
`expected/poly.json` added, and `stateSyncConfirmationDelay` {"0": 0} (README,
Fixture).

`run`, under the host lock, runs the probe in the pinned runtime image: it
generates the corpus blocks with native Bor, imports them through the
accepted decision-bridge import path, restarts the node, imports them again
and exports the native output. It then writes the observation
(`observation.json`, README "Observation format" of test/native_corpus) and
compares it with `expected/poly.json` (`corpus_compare.compare`).

`observe DIR` rebuilds the observation of a retained run directory from its
probe outputs and compares it again, without Docker.

The expected file is never read for a value: the driver takes from it only
the corpus accounts to add to the genesis and the account list to read.
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

POLY = Path(__file__).resolve().parent
CORPUS = POLY.parent
ROOT = POLY.parents[2]
HARNESS = ROOT / "test/harness"
sys.path.insert(0, str(HARNESS))
sys.path.insert(0, str(CORPUS))
import admission  # noqa: E402
import bor_admission_tree  # noqa: E402
import capture_probe  # noqa: E402
import corpus_compare  # noqa: E402
from artifacts import (artifact, check_deadline, copy_artifact, json_artifact, load, pinned_json,  # noqa: E402
                       read_bytes, reference, require)
from capture_probe import Interrupted, block_stops, unblock_stops, write_until_done  # noqa: E402
from native import Native, new_output  # noqa: E402

EXPECTED = CORPUS / "expected/poly.json"
STUB_SOURCES = HARNESS / "fixtures/source_inputs_bor/heimdallstub"
PROBE_DIR = "cmd/extractor-bor-corpus-probe"
BODY_FILES = ("status.json", "span-latest.json", "span-0.json", "span-1.json", "clerk-corpus-from-1.json",
              "clerk-empty.json")
PREPARATION_SECONDS = 3600
RUN_SECONDS = 900
STATE_SYNC_DELAY = {"0": 0}
TRACER = "callTracer (eth/tracers/native) through the native tracers.API.TraceTransaction"
STATE_READS = ("native state reads at the parent root and after each transaction: a native re-execution of the "
               "block prefix on the parent state (core.ApplyTransaction with the engine's author, the recovered "
               "signer), checked against the stored receipts; block-end reads at the block root")


def go_environment():
    return {"CGO_ENABLED": "1", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64",
            "GOCACHE": "/work/build-cache", "GOMODCACHE": "/modules", "GOPATH": "/work/gopath",
            "GOFLAGS": "-mod=readonly", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
            "GOTMPDIR": "/work/gotmp", "TMPDIR": "/work/gotmp", "GOWORK": "off"}


# ---------------------------------------------------------------------------
# Fixture.


def derived_inputs(source_genesis, expected):
    """The corpus genesis and chain-config items in the shared canonical JSON
    form: the accepted test genesis with the corpus accounts added and the
    state sync delay 0."""
    document = json.loads(source_genesis)
    config = document.get("config")
    require(isinstance(config, dict) and isinstance(config.get("bor"), dict), "The genesis has no Bor config")
    require(config.get("chainId") == expected["chain_id"], "The source genesis is not the corpus chain")
    require("stateSyncConfirmationDelay" not in config["bor"], "The source genesis already sets the delay")
    config["bor"]["stateSyncConfirmationDelay"] = dict(STATE_SYNC_DELAY)
    alloc = document["alloc"]
    present = {address.lower().removeprefix("0x") for address in alloc}
    for row in expected["genesis_additions"]:
        require(row["address"] not in present, f"The corpus account {row['address']} is already in the genesis")
        account = {"balance": hex(int(row["balance"]))}
        if row["code"]:
            account["code"] = "0x" + row["code"]
        alloc["0x" + row["address"]] = account
    return admission.canonical(json.dumps(document).encode()), admission.canonical(json.dumps(config).encode())


def accounts_of(expected):
    """Every account the expected file lists (addresses only, never values)."""
    listed = set()
    for block in expected["blocks"]:
        listed |= set(block["state_after_block"])
        for tx in block["transactions"]:
            listed |= set(tx["state_before"]) | set(tx["state_after"])
    return sorted(listed)


# ---------------------------------------------------------------------------
# Preparation (not measured).


def extract(data, source, deadline):
    with tarfile.open(fileobj=io.BytesIO(data)) as stream:
        for member in stream:
            check_deadline(deadline)
            stream.extract(member, source, filter="data")


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
        record = {"schema_version": 1, "kind": "poly-corpus-preparation", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": state, "build_manifest": manifest_ref}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            record["fixture"] = capture_probe.copy_sources(("main.go", "poly_corpus.py"), POLY, output / "fixture",
                                                           deadline)
            record["expected"] = capture_probe.copy_sources(("poly.json",), EXPECTED.parent, output / "expected",
                                                            deadline)
            record["stub_bodies"] = capture_probe.copy_sources(BODY_FILES, POLY / "stub", output / "stub", deadline)
            record["harness_code"] = capture_probe.copy_sources(
                ("bor_admission_tree.py", "admission.py", "capture_probe.py", "native.py", "owned.py", "artifacts.py"),
                HARNESS, output / "harness-code", deadline)
            record["corpus_code"] = capture_probe.copy_sources(("corpus_compare.py",), CORPUS, output / "corpus-code",
                                                               deadline)
            compiler = manifest["image_inputs"]["golang-build-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/golang@sha256:[0-9a-f]{64}", compiler), "Unpinned compiler")
            runtime = manifest["image_inputs"]["debian-runtime-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
            record["compiler_image"], record["runtime_image"] = compiler, runtime
            expected = json.loads((output / "expected/poly.json").read_text())
            record["bor"] = prepare_bor(manifest, root, output, native, compiler, expected, deadline)
            record["result"] = "PASS"
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "preparation.json", record, native, started)
    return output, record


def prepare_bor(manifest, root, output, native, compiler, expected, deadline):
    closure = manifest["module_closures"]["bor"]
    require(closure["source_commit"] == manifest["builds"]["bor"]["source_commit"], "Source closure mismatch")
    require(manifest["builds"]["bor"]["compiler_image"] == compiler, "Bor compiler mismatch")
    require(expected["fixture"]["native_source"] == f"bor {closure['source_commit']}",
            "The expected file names another Bor source")
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
    row["probe_sources"] = capture_probe.copy_sources(("main.go",), POLY, probe, deadline)
    row["stub_sources"] = capture_probe.copy_sources(("stub.go",), STUB_SOURCES, probe / "heimdallstub", deadline)
    for name in ("bin", "build-cache", "gopath", "gotmp"):
        (directory / name).mkdir()
    mounts = [(directory, "/work", True), (root / "cache" / "bor" / "mod", "/modules", True)]
    for name in ("bin", "build-cache", "gopath", "gotmp"):
        mounts.append((directory / name, f"/work/{name}", False))

    def go(argv, cap):
        return native.command(compiler, ["/usr/local/go/bin/go", *argv], mounts, cap=cap, memory="6g",
                              cwd="/work/source", environment=go_environment())

    go(["mod", "verify"], 300)
    go(["vet", f"./{PROBE_DIR}/..."], 1200)
    go(["build", "-p", "2", "-trimpath", "-o", "/work/bin/probe", f"./{PROBE_DIR}"], 1800)
    bin_directory = directory / "bin"
    source_genesis = (source / "tests/bor/testdata/genesis.json").read_bytes()
    genesis, config = derived_inputs(source_genesis, expected)
    (bin_directory / "genesis.json").write_bytes(genesis)
    (bin_directory / "chain-config.json").write_bytes(config)
    copy_artifact(source / "tests/bor/helper.go", bin_directory / "helper.go",
                  reference(source / "tests/bor/helper.go", 4 * 1024 * 1024, deadline), deadline=deadline)
    for name in ("probe", "genesis.json", "chain-config.json", "helper.go"):
        row[name] = reference(bin_directory / name, deadline=deadline)
    row["source_genesis"] = {"sha256": hashlib.sha256(source_genesis).hexdigest(), "size_bytes": len(source_genesis)}
    return row


# ---------------------------------------------------------------------------
# Observation (from the probe outputs only).


def hex_digits(value):
    """0x-prefixed or bare hex from the native tracer, as lowercase hex without 0x."""
    require(isinstance(value, str), f"Expected a hex string, got {value!r}")
    text = value.lower().removeprefix("0x")
    require(re.fullmatch(r"[0-9a-f]*", text) is not None, f"Bad hex {value!r}")
    return text


def frame_of(trace):
    """A native callTracer frame in the observation format."""
    value = trace.get("value")
    frame = {"type": trace["type"], "from": hex_digits(trace["from"]), "to": hex_digits(trace["to"]),
             "value": str(int(value, 16)) if value else "0", "input": hex_digits(trace.get("input", "0x")),
             "output": hex_digits(trace.get("output", "0x")), "error": trace.get("error"),
             "calls": [frame_of(call) for call in trace.get("calls", [])]}
    return frame


def transaction_of(row):
    tx = {"index": row["index"], "hash": row["hash"], "type": row["type"], "from": row["from"], "nonce": row["nonce"],
          "to": row["to"], "value": row["value"], "gas": row["gas"], "data": row["data"],
          "receipt": row["receipt"], "frame": frame_of(row["call_tracer"]),
          "state_before": row["state_before"], "state_after": row["state_after"]}
    for key in ("gas_price", "max_fee_per_gas", "max_priority_fee_per_gas"):
        if key in row:
            tx[key] = row[key]
    return tx


def observation_of(imported, reimported, source):
    """Build the observation from the import and re-import outputs of the probe."""
    require(not imported.get("error") and not reimported.get("error"), "A probe command reported an error")
    by_hash = {row["hash"]: row["number"] for row in reimported["blocks"]}
    syncs = {}
    for event in imported["state_sync"]:
        syncs.setdefault(event["block"], []).append(event)
    blocks = []
    for row in reimported["blocks"]:
        state_sync = []
        for event in syncs.get(row["number"], []):
            require(row["state_sync_receipt"], f"Block {row['number']} has state sync events and no receipt")
            state_sync.append({"id": event["id"], "contract": event["contract"], "data": event["data"],
                               "logs": row["state_sync_receipt_logs"],
                               "state_receiver_last_state_id": int(row["state_receiver_last_state_id"])})
        blocks.append({"number": row["number"], "hash": row["hash"], "state_root": row["state_root"],
                       "base_fee": row["base_fee"], "gas_limit": row["gas_limit"], "gas_used": row["gas_used"],
                       "body_tx_count": row["body_tx_count"],
                       "transactions": [transaction_of(tx) for tx in row["transactions"]],
                       "system_transactions": [], "state_sync": state_sync,
                       "state_after_block": row["state_after_block"]})
    decisions = []
    for row in imported["decisions"]:
        if row["state"] != "NATIVE_DECIDED":
            continue
        for member in row["members"]:
            decisions.append({"block": by_hash.get(member["block"], -1), "kind": row["kind"]})
    before = {row["sequence"] for row in reimported["decisions_before"]}
    new_decisions = sum(1 for row in reimported["decisions_after"] if row["sequence"] not in before)
    return {"schema_version": 1, "kind": "native-corpus-observation", "family": "poly", "chain_id": 15001,
            "source": source, "blocks": blocks, "decisions": decisions,
            "reimport": {"new_observations": len(reimported["observed"]), "new_decisions": new_decisions}}


def bridge_checks(imported, reimported):
    """Facts of the decision bridge that the comparator does not see; each
    failure is named in the run record and fails the run."""
    failures = []
    numbers = [row["number"] for row in reimported["blocks"]]
    if imported["observed"] != numbers:
        failures.append(f"the import observed {imported['observed']}, not one observation per block {numbers}")
    if any(len(imported["keys"].get(str(n), [])) != 1 for n in numbers):
        failures.append("a block does not have exactly one observation key")
    if imported["observer_errors"] or reimported["observer_errors"]:
        failures.append("the observer reported an error")
    if any(row["state"] != "NATIVE_DECIDED" or len(row["members"]) != 1 for row in imported["decisions"]):
        failures.append("a decision is not NATIVE_DECIDED with one member")
    keys = {key for values in imported["keys"].values() for key in values}
    if {m["observation"] for row in imported["decisions"] for m in row["members"]} != keys:
        failures.append("the decision members are not the observations of the import")
    decided = sorted(row["sequence"] for row in imported["decisions"] if row["kind"] == "IMPORT")
    if sorted(imported["obligations"]) != decided:
        failures.append("the delivery obligations are not the IMPORT decisions")
    if reimported["decisions_before"] != imported["decisions"]:
        failures.append("the restart changed the decisions before the re-import")
    if reimported.get("reimport_error"):
        failures.append(f"the re-import failed: {reimported['reimport_error']}")
    if imported["head_hash"] != reimported["head_hash"]:
        failures.append("the re-import moved the head")
    return failures


def observe(directory, expected, source):
    """Write observation.json and comparison.json of a run directory; return
    the comparison and the bridge check failures."""
    imported = load(directory / "import.json", 16 * 1024 * 1024)
    reimported = load(directory / "reimport.json", 64 * 1024 * 1024)
    observation = observation_of(imported, reimported, source)
    (directory / "observation.json").write_text(json.dumps(observation, indent=1, sort_keys=True) + "\n")
    try:
        comparison = corpus_compare.compare(expected, observation)
    except corpus_compare.FormatError as error:
        comparison = {"schema_version": 1, "kind": "native-corpus-comparison", "family": "poly", "result": "FAIL",
                      "format_error": str(error), "cases": [], "differences": []}
    (directory / "comparison.json").write_text(json.dumps(comparison, indent=1, sort_keys=True) + "\n")
    return comparison, bridge_checks(imported, reimported)


# ---------------------------------------------------------------------------
# Measured run.


def run(preparation_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(preparation_path.is_absolute(), "Preparation path must be absolute")
    preparation_ref, preparation = pinned_json(preparation_path, expected_sha, 16 * 1024 * 1024, deadline)
    require(preparation.get("kind") == "poly-corpus-preparation" and preparation.get("result") == "PASS"
            and preparation.get("cleanup_complete") is True, "A passing preparation is required")
    manifest_root = Path(preparation["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (preparation_path.parent, manifest_root, ROOT))
    state = capture_probe.source_state()
    with capture_probe.single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "poly-corpus-run", "result": "FAIL", "run_id": uuid.uuid4().hex,
                  "source": state, "preparation": preparation_ref,
                  "profile": "private local Bor fixture chain 15001 (POLY private local; not a public profile)"}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            expected_path = artifact(preparation["expected"][0]["copy"], 4 * 1024 * 1024, deadline=deadline)
            expected_bytes = read_bytes(expected_path, 4 * 1024 * 1024, deadline)
            require(expected_bytes == EXPECTED.read_bytes(), "The prepared expected file differs from the repository")
            expected = json.loads(expected_bytes)
            record["expected"] = preparation["expected"][0]["copy"]
            runtime = preparation["runtime_image"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
            for row in preparation["stub_bodies"]:
                artifact(row["copy"], 1024 * 1024, deadline=deadline)
            stub = Path(preparation["stub_bodies"][0]["copy"]["path"]).parent
            bor = preparation["bor"]
            binary = artifact(bor["probe"], 512 * 1024 * 1024, deadline=deadline)
            genesis = read_bytes(artifact(bor["genesis.json"], 4 * 1024 * 1024, deadline=deadline), 4 * 1024 * 1024,
                                 deadline)
            config = read_bytes(artifact(bor["chain-config.json"], 1024 * 1024, deadline=deadline), 1024 * 1024,
                                deadline)
            artifact(bor["helper.go"], 4 * 1024 * 1024, deadline=deadline)
            archive = artifact(bor["source_archive"], 512 * 1024 * 1024, deadline=deadline)
            with tarfile.open(archive) as stream:
                source_genesis = stream.extractfile("tests/bor/testdata/genesis.json").read()
            require(derived_inputs(source_genesis, expected) == (genesis, config),
                    "The prepared genesis items differ from the independent derivation")
            mounts = [(binary.parent, "/probe", True), (stub, "/stub", True), (output, "/out", False)]
            common = ["-genesis", "/probe/genesis.json", "-config", "/probe/chain-config.json", "-bodies", "/stub"]
            started_native = time.monotonic()
            native.command(runtime, ["/probe/probe", "generate", *common, "-keysource", "/probe/helper.go",
                                     "-out", "/out/chain"], mounts, cap=300, memory="2g")
            store = ["-blocks", "/out/chain/blocks.rlp", "-datadir", "/out/instance", "-commit", bor["source_commit"]]
            native.command(runtime, ["/probe/probe", "import", *common, *store, "-out", "/out/import.json"], mounts,
                           cap=300, memory="2g")
            native.command(runtime, ["/probe/probe", "reimport", *common, *store, "-accounts",
                                     ",".join(accounts_of(expected)), "-out", "/out/reimport.json"], mounts,
                           cap=300, memory="2g")
            record["native_seconds"] = time.monotonic() - started_native
            for name in ("chain/blocks.rlp", "chain/generated.json", "import.json", "reimport.json"):
                record.setdefault("outputs", {})[name] = reference(output / name, deadline=deadline)
            source = {"native_source": f"bor {bor['source_commit']}", "probe": bor["probe"],
                      "patch_sets": "E01-02.admission and E01-02.decision_bridge (test/harness/bor_admission_tree.py)",
                      "installed": reference(preparation_path, deadline=deadline), "tracer": TRACER,
                      "state_reads": STATE_READS, "import": record["outputs"]["import.json"],
                      "reimport": record["outputs"]["reimport.json"]}
            comparison, failures = observe(output, expected, source)
            record["observation"] = reference(output / "observation.json", deadline=deadline)
            record["comparison"] = reference(output / "comparison.json", deadline=deadline)
            record["cases"] = comparison["cases"]
            record["differences"] = len(comparison["differences"])
            record["bridge_failures"] = failures
            if comparison["result"] == "PASS" and not failures:
                record["result"] = "PASS"
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "run-record.json", record, native, started)
    return output, record


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    commands = parser.add_subparsers(dest="command", required=True)
    prep = commands.add_parser("prepare")
    prep.add_argument("--build-manifest", type=Path, required=True)
    prep.add_argument("--sha256", required=True)
    prep.add_argument("--output", type=Path, required=True)
    measured = commands.add_parser("run")
    measured.add_argument("preparation", type=Path)
    measured.add_argument("--sha256", required=True)
    measured.add_argument("--output", type=Path, required=True)
    offline = commands.add_parser("observe")
    offline.add_argument("directory", type=Path)
    derive = commands.add_parser("derive", help="write the corpus genesis items from a source genesis (development)")
    derive.add_argument("source_genesis", type=Path)
    derive.add_argument("output", type=Path)
    args = parser.parse_args(argv)
    if args.command == "prepare":
        output, record = prepare(args.build_manifest, args.sha256, args.output)
    elif args.command == "run":
        output, record = run(args.preparation, args.sha256, args.output)
    elif args.command == "derive":
        genesis, config = derived_inputs(args.source_genesis.read_bytes(), json.loads(EXPECTED.read_text()))
        args.output.mkdir(exist_ok=True)
        (args.output / "genesis.json").write_bytes(genesis)
        (args.output / "chain-config.json").write_bytes(config)
        return 0
    else:
        expected = json.loads(EXPECTED.read_text())
        previous = args.directory / "observation.json"
        # A retained run keeps its source; a development directory has none.
        source = load(previous, 64 * 1024 * 1024)["source"] if previous.exists() else {"development": "not measured"}
        comparison, failures = observe(args.directory, expected, source)
        print(json.dumps({"result": comparison["result"], "cases": comparison["cases"],
                          "differences": comparison["differences"][:20], "bridge_failures": failures}, indent=1))
        return 0 if comparison["result"] == "PASS" and not failures else 1
    print(json.dumps({"result": record["result"], "output": str(output), "error": record.get("error")}))
    return 0 if record["result"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
