#!/usr/bin/env python3
"""Build and run the BSC (Parlia) block of E01-03.smoke_corpus.

`prepare` applies the accepted BSC admission and decision patch sets to the
accepted BSC source tree and installs the accepted packages with
test/harness/bsc_decision.py, adds this directory's corpus probe
(`corpus_test.go`) to the BSC decision package, vets it and compiles the test
binary offline with the accepted compiler. It derives the corpus genesis: the
accepted private BSC genesis (test/harness/fixtures/bsc_admission/genesis.json)
with only the corpus accounts of `expected/bsc.json` added. Preparation is
not measured.

`run`, under the host lock (`pins.owned_attempt`), runs the probe three times
in the compiler image: generate the corpus blocks with native Parlia, import
them through the accepted decision-bridge import path, then restart, import
them again and export the native output. It writes the observation
(`observation.json`, README "Observation format" of test/native_corpus),
compares it with `expected/bsc.json` (`corpus_compare.compare`), checks the
bridge facts that the comparator does not see, and records a verdict per
case.

`observe DIR --output NEW` rebuilds the observation of a retained run from
its probe outputs and compares it again, without Docker; it writes only into
the new directory NEW.

The expected file is never read for a value: the driver takes from it only
the corpus accounts to add to the genesis and the account list to read.
"""

import argparse
import json
from pathlib import Path
import re
import sys
import time
import uuid

BSC_DIR = Path(__file__).resolve().parent
CORPUS = BSC_DIR.parent
ROOT = BSC_DIR.parents[2]
HARNESS = ROOT / "test/harness"
sys.path.insert(0, str(HARNESS))
sys.path.insert(0, str(CORPUS))
import admission  # noqa: E402
import bsc_admission  # noqa: E402
import bsc_decision  # noqa: E402
import bsc_source_inputs as probe14  # noqa: E402
import corpus_compare  # noqa: E402
import decision  # noqa: E402
import pins  # noqa: E402
import source_inputs  # noqa: E402
from artifacts import (Rejected, artifact, copy_artifact, load, pinned_json, read_bytes, reference,  # noqa: E402
                       require, source_snapshot, sources_unchanged)

EXPECTED = CORPUS / "expected/bsc.json"
FIXTURE_GENESIS = HARNESS / "fixtures/bsc_admission/genesis.json"
PROBE = BSC_DIR / "corpus_test.go"
PROBE_TARGET = "extractor/bsc/decision/corpus_test.go"
TEST = "TestBSCCorpus"
BINARY = "bsc-corpus.test"
STEPS = ("generate", "import", "reimport")
HARNESS_FILES = bsc_decision.HARNESS
CORPUS_FILES = ("bsc/bsc_corpus.py", "bsc/corpus_test.go", "corpus_compare.py", "expected/bsc.json")
PREPARATION_SECONDS = 2 * 3600
RUN_SECONDS = 900
STEP_CAP = 300
PRODUCER_RUN = "bsc-probe.run-1"
TRACER = "callTracer (eth/tracers/native) on a native re-execution of the block prefix"
STATE_READS = ("native state reads at the parent root and after each user transaction: a native re-execution of "
               "the block prefix on the parent state (TryUpdateBuildInSystemContract, then core.ApplyTransaction "
               "with the engine's author), checked against the stored receipts; block-end reads at the block root")


# ---------------------------------------------------------------------------
# Fixture.


def quantity(text):
    """A genesis quantity: 0x-prefixed hex or decimal."""
    return int(text, 16) if text.lower().startswith("0x") else int(text)


def corpus_genesis(fixture_genesis, expected):
    """The accepted private BSC genesis with the corpus accounts added; an
    account already in the genesis must have the same balance and no code."""
    document = json.loads(fixture_genesis)
    require(document.get("config", {}).get("chainId") == expected["chain_id"], "The genesis is not the corpus chain")
    alloc = document["alloc"]
    present = {address.lower().removeprefix("0x"): address for address in alloc}
    for row in expected["genesis_additions"]:
        account = {"balance": hex(int(row["balance"]))}
        if row["code"]:
            account["code"] = "0x" + row["code"]
        if row["address"] in present:
            existing = alloc[present[row["address"]]]
            require(not row["code"] and "code" not in existing and quantity(existing["balance"]) == int(row["balance"]),
                    f"The corpus account {row['address']} differs from the fixture")
            continue
        alloc["0x" + row["address"]] = account
    return (json.dumps(document, indent=1, sort_keys=True) + "\n").encode()


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


def prepare(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    require(manifest_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    manifest_ref = reference(manifest_path, 64 * 1024 * 1024, deadline)
    require(manifest_ref["sha256"] == expected_sha, "BuildManifest digest mismatch")
    manifest = load(manifest_path, 64 * 1024 * 1024, deadline)
    source_inputs.check_identical(ROOT)
    admission.check_identical(ROOT)
    decision.check_identical(ROOT)
    require(not output.is_relative_to(manifest_path.parent), "Preparation cannot write into accepted build storage")
    proof = {"schema_version": 1, "kind": "bsc-corpus-preparation", "result": "FAIL", "build_manifest": manifest_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": PREPARATION_SECONDS, "runtime": "NOT_RUN", "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS_FILES, deadline)
        root = output / "bsc"
        root.mkdir()
        closure, copied, source = probe14.extract_source(manifest, manifest_path, root, deadline)
        patched, installed = bsc_decision.install(source, root, deadline)
        probe_ref = reference(PROBE, 1024 * 1024, deadline)
        require(not (source / PROBE_TARGET).exists(), "The corpus probe is already in the native tree")
        copy_artifact(PROBE, source / PROBE_TARGET, probe_ref, 1024 * 1024, deadline)
        retained = root / "installed" / PROBE_TARGET
        retained.parent.mkdir(parents=True, exist_ok=True)
        copy_artifact(PROBE, retained, probe_ref, 1024 * 1024, deadline)
        installed.append({"relative": PROBE_TARGET, "source": probe_ref,
                          "retained": reference(retained, 1024 * 1024, deadline)})
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (root / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            copy_artifact(source / name, root / "metadata" / name, reference(source / name, deadline=deadline),
                          deadline=deadline)
        compiler = manifest["builds"]["bsc"]["compiler_image"]
        require(compiler == manifest["image_inputs"]["golang-build-image"]["reference"], "Unexpected compiler image")
        mounts = probe14.build_mounts(source, manifest_path.parent / "cache" / "bsc" / "mod", root)
        bsc_decision.build(runner, compiler, mounts, BINARY, sorted({row["path"] for item in patched
                                                                     for row in item["files"]}))
        sources_unchanged(origins, deadline)
        for row in installed:
            require(reference(Path(row["source"]["path"]), 1024 * 1024, deadline) == row["source"],
                    "Installed source changed during preparation")
        keystore = root / "generated/validator-keystore.json"
        copy_artifact(source / bsc_admission.KEYSTORE, keystore,
                      reference(source / bsc_admission.KEYSTORE, deadline=deadline), deadline=deadline)
        expected_bytes = read_bytes(EXPECTED, 4 * 1024 * 1024, deadline)
        expected_copy = root / "generated/expected-bsc.json"
        expected_copy.write_bytes(expected_bytes)
        genesis = root / "generated/genesis.json"
        genesis.write_bytes(corpus_genesis(read_bytes(FIXTURE_GENESIS, 4 * 1024 * 1024, deadline),
                                           json.loads(expected_bytes)))
        tool = {"schema_version": 1, "kind": "bsc-corpus-tool", "build_manifest": manifest_ref,
                "native_source_commit": closure["source_commit"], "source_archive": copied, "compiler": compiler,
                "patched": patched, "installed": installed, "source_root": str(source),
                "test_binary": reference(root / "generated" / BINARY, deadline=deadline),
                "validator_keystore": reference(keystore, deadline=deadline),
                "fixture_genesis": reference(FIXTURE_GENESIS, deadline=deadline),
                "genesis": reference(genesis, deadline=deadline),
                "expected": reference(expected_copy, deadline=deadline),
                "metadata": [reference(root / "metadata" / name, deadline=deadline) for name in ("go.mod", "go.sum")]}
        pins.save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "preparation.json", body)


# ---------------------------------------------------------------------------
# Observation.


def hex_digits(value):
    """0x-prefixed or bare hex from the native tracer, as lowercase hex without 0x."""
    require(isinstance(value, str), f"Expected a hex string, got {value!r}")
    text = value.lower().removeprefix("0x")
    require(re.fullmatch(r"[0-9a-f]*", text) is not None, f"Bad hex {value!r}")
    return text


def frame_of(trace):
    """A native callTracer frame in the observation format."""
    value = trace.get("value")
    return {"type": trace["type"], "from": hex_digits(trace["from"]), "to": hex_digits(trace["to"]),
            "value": str(int(value, 16)) if value else "0", "input": hex_digits(trace.get("input", "0x")),
            "output": hex_digits(trace.get("output", "0x")), "error": trace.get("error"),
            "calls": [frame_of(call) for call in trace.get("calls", [])]}


def transaction_of(row):
    tx = {key: row[key] for key in ("index", "hash", "type", "from", "nonce", "to", "value", "gas", "data", "receipt",
                                    "state_before", "state_after")}
    tx["frame"] = frame_of(row["call_tracer"])
    for key in ("gas_price", "max_fee_per_gas", "max_priority_fee_per_gas"):
        if key in row:
            tx[key] = row[key]
    return tx


def decisions_of(bridge):
    """The decided decisions of the bridge records: sequence -> (intent, decided)."""
    records = bridge["decision_records"]
    out = {}
    for key, value in records.items():
        match = re.fullmatch(r"([0-9a-f]{16})/(intent|decided)", key)
        if match:
            out.setdefault(int(match.group(1), 16), {})[match.group(2)] = value
    return {sequence: rows for sequence, rows in out.items() if "decided" in rows}


def observation_of(imported, reimported, source):
    """Build the observation from the import and re-import outputs of the probe."""
    require(not imported["error"] and not reimported["recover_error"] and not reimported["reimport_error"],
            "A probe step reported an error")
    by_hash = {row["hash"]: row["number"] for row in imported["blocks"]}
    blocks = []
    for row in reimported["blocks"]:
        blocks.append({key: row[key] for key in ("number", "hash", "state_root", "base_fee", "gas_limit", "gas_used",
                                                 "body_tx_count", "system_transactions", "system_nonce_after",
                                                 "state_after_block")}
                      | {"transactions": [transaction_of(tx) for tx in row["transactions"]], "state_sync": []})
    decisions = []
    for sequence, rows in sorted(decisions_of(imported["bridge"]).items()):
        for member in rows["intent"]["members"]:
            decisions.append({"block": by_hash.get(member["block"], -1), "kind": rows["intent"]["kind"]})
    before, after = reimported["before"]["counters"], reimported["after"]["counters"]
    return {"schema_version": 1, "kind": "native-corpus-observation", "family": "bsc", "chain_id": 714,
            "source": source, "blocks": blocks, "decisions": decisions,
            "reimport": {"new_observations": after[0] - before[0], "new_decisions": after[1] - before[1]}}


def bridge_checks(imported, reimported):
    """Facts of the decision bridge that the comparator does not see; each
    failure is named in the run record and fails the run."""
    failures = []
    bridge = imported["bridge"]
    blocks = imported["blocks"]
    decided = decisions_of(bridge)
    members = [member for rows in decided.values() for member in rows["intent"]["members"]]
    if bridge["mode"] != "LIVE" or bridge["head_number"] != len(blocks) or \
            bridge["head_hash"] != blocks[-1]["hash"]:
        failures.append("the import did not end LIVE at the last corpus block")
    if bridge["counters"] != [len(blocks), len(blocks)]:
        failures.append(f"the import opened {bridge['counters']} observations and decisions, not one per block")
    if bridge["open_observations"]:
        failures.append("an observation stayed open after the import")
    if any(rows["intent"]["kind"] != "IMPORT" or len(rows["intent"]["members"]) != 1 for rows in decided.values()):
        failures.append("a decision is not an IMPORT with one member")
    if sorted(member["block"] for member in members) != sorted(row["hash"] for row in blocks):
        failures.append("the decision members are not the corpus blocks, one each")
    if len({member["observation"] for member in members}) != len(blocks):
        failures.append("the decision members are not one observation per block")
    if any(rows["decided"]["intent_digest"] != rows["intent"]["digest"] for rows in decided.values()):
        failures.append("a NATIVE_DECIDED record does not bind its intent")
    obligations = sorted(row["sequence"] for row in bridge["obligations"])
    if obligations != sorted(decided):
        failures.append("the delivery obligations are not the IMPORT decisions")
    if any(row["intent_digest"] != decided.get(row["sequence"], {}).get("intent", {}).get("digest")
           for row in bridge["obligations"]):
        failures.append("an obligation does not bind its intent")
    before, after = reimported["before"], reimported["after"]
    if before["decision_records"] != bridge["decision_records"]:
        failures.append("the restart changed the decision records before the re-import")
    if after["decision_records"] != before["decision_records"]:
        failures.append("the re-import changed the decision records")
    if after["obligations"] != bridge["obligations"] or before["obligations"] != bridge["obligations"]:
        failures.append("the restart or the re-import changed the delivery obligations")
    if after["counters"] != before["counters"]:
        failures.append("the re-import opened an observation or a decision")
    if reimported["recover_error"] or reimported["reimport_error"]:
        failures.append("the restart recovery or the re-import failed")
    if after["head_hash"] != bridge["head_hash"] or after["mode"] != "LIVE":
        failures.append("the re-import moved the head or left LIVE")
    return failures


def write_new(path, value):
    """Write JSON to a new file; an existing file (a retained record) is never replaced."""
    with open(path, "x") as handle:
        handle.write(json.dumps(value, indent=1, sort_keys=True) + "\n")


def observe(directory, expected, source, output):
    """Build the observation from the probe outputs in directory, compare it
    with expected, and write observation.json and comparison.json as new
    files in output. Return the comparison and the bridge check failures."""
    imported = load(directory / "import.json", 16 * 1024 * 1024)
    reimported = load(directory / "reimport.json", 64 * 1024 * 1024)
    observation = observation_of(imported, reimported, source)
    try:
        comparison = corpus_compare.compare(expected, observation)
    except corpus_compare.FormatError as error:
        comparison = {"schema_version": 1, "kind": "native-corpus-comparison", "family": "bsc", "result": "FAIL",
                      "format_error": str(error), "cases": [], "differences": []}
    write_new(output / "observation.json", observation)
    write_new(output / "comparison.json", comparison)
    return comparison, bridge_checks(imported, reimported)


def verdict(comparison, failures):
    """The run result and the per-case verdicts. A bridge failure is a
    whole-observation difference, so it fails every applicable case."""
    cases = []
    for row in comparison["cases"]:
        if failures and row["verdict"] == "PASS":
            row = {**row, "verdict": "FAIL", "bridge_failures": len(failures)}
        cases.append(row)
    passed = comparison["result"] == "PASS" and not failures and bool(cases)
    return ("PASS" if passed else "FAIL"), cases


# ---------------------------------------------------------------------------
# Measured run.


def run(tool_path, tool_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(tool_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "bsc-corpus-tool", "Not a BSC corpus tool")
    proof = {"schema_version": 1, "kind": "bsc-corpus-run", "result": "FAIL", "tool": tool_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": RUN_SECONDS, "public_profile_credit": False,
             "profile": "private local BSC Parlia fixture chain 714 (BSC private local; not a public profile)",
             "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS_FILES, deadline)
        expected_bytes = read_bytes(artifact(tool["expected"], 4 * 1024 * 1024, deadline=deadline), 4 * 1024 * 1024,
                                    deadline)
        require(expected_bytes == EXPECTED.read_bytes(), "The prepared expected file differs from the repository")
        expected = json.loads(expected_bytes)
        genesis = read_bytes(artifact(tool["genesis"], 4 * 1024 * 1024, deadline=deadline), 4 * 1024 * 1024, deadline)
        require(genesis == corpus_genesis(FIXTURE_GENESIS.read_bytes(), expected),
                "The prepared genesis differs from the independent derivation")
        root = output / "bsc"
        for name in ("inputs", "work", "scratch"):
            (root / name).mkdir(parents=True)
        proof["inputs"] = {
            "genesis.json": copy_artifact(Path(tool["genesis"]["path"]), root / "inputs/genesis.json", tool["genesis"],
                                          4 * 1024 * 1024, deadline),
            "validator-keystore.json": copy_artifact(Path(tool["validator_keystore"]["path"]),
                                                     root / "inputs/validator-keystore.json",
                                                     tool["validator_keystore"], 1024 * 1024, deadline)}
        binary = artifact(tool["test_binary"], 1024 * 1024 * 1024, deadline=deadline)
        mounts = [(binary.parent, "/tool", True), (root / "inputs", "/inputs", True), (root / "work", "/work", False),
                  (root / "scratch", "/scratch", False)]
        proof["phases"] = {"execution_started": time.time()}
        proof["steps"] = {}
        for step in STEPS:
            env = {"EXTRACTOR_CORPUS_STEP": step, "EXTRACTOR_CORPUS_INPUTS": "/inputs", "EXTRACTOR_CORPUS_WORK": "/work",
                   "EXTRACTOR_CORPUS_ACCOUNTS": ",".join(accounts_of(expected)),
                   "EXTRACTOR_DECISION_NATIVE_COMMIT": tool["native_source_commit"], "TMPDIR": "/scratch",
                   "HOME": "/scratch"}
            stdout = runner.command(tool["compiler"], ["/tool/" + binary.name, "-test.v", "-test.count=1", "-test.run",
                                                       f"^{TEST}$", "-test.timeout", f"{STEP_CAP - 60}s"],
                                    mounts, cap=STEP_CAP, memory="2g", cwd="/scratch", environment=env)
            pins.parse_go_test(read_bytes(stdout, 8 * 1024 * 1024, deadline).decode(), [TEST])
            proof["steps"][step] = reference(stdout, deadline=deadline)
        proof["phases"]["execution_finished"] = time.time()
        work = root / "work"
        proof["outputs"] = {name: reference(work / name, 64 * 1024 * 1024, deadline)
                            for name in ("blocks.rlp", "generated.json", "import.json", "reimport.json",
                                         "trace-import.jsonl", "trace-reimport.jsonl")}
        source = {"native_source": f"bsc {tool['native_source_commit']}", "test_binary": tool["test_binary"],
                  "patch_sets": "E01-02.admission and E01-02.decision_bridge (test/harness/bsc_decision.py install)",
                  "tool": tool_ref, "tracer": TRACER, "state_reads": STATE_READS,
                  "import": proof["outputs"]["import.json"], "reimport": proof["outputs"]["reimport.json"]}
        comparison, failures = observe(work, expected, source, root)
        proof["observation"] = reference(root / "observation.json", deadline=deadline)
        proof["comparison"] = reference(root / "comparison.json", deadline=deadline)
        proof["verdict"], proof["cases"] = verdict(comparison, failures)
        proof["differences"] = len(comparison["differences"])
        proof["bridge_failures"] = failures
        sources_unchanged(origins, deadline)
        artifact(tool["test_binary"], 1024 * 1024 * 1024, deadline=deadline)
        require(proof["verdict"] == "PASS", f"Corpus verdict {proof['verdict']}: {proof['differences']} differences, "
                                            f"bridge failures {failures}")

    return pins.owned_attempt(output, started, deadline, proof, "run.json", body)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    prep = commands.add_parser("prepare")
    prep.add_argument("--build-manifest", type=Path, required=True)
    prep.add_argument("--sha256", required=True)
    prep.add_argument("--output", type=Path, required=True)
    run_parser = commands.add_parser("run")
    run_parser.add_argument("tool", type=Path)
    run_parser.add_argument("--sha256", required=True)
    run_parser.add_argument("--output", type=Path, required=True)
    observe_parser = commands.add_parser("observe")
    observe_parser.add_argument("directory", type=Path, help="the bsc/ directory of a retained run")
    observe_parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "prepare":
            proof = prepare(args.build_manifest, args.sha256, args.output)
        elif args.command == "run":
            proof = run(args.tool, args.sha256, args.output)
        else:
            args.output.mkdir()
            directory = args.directory
            observation = load(directory / "observation.json", 64 * 1024 * 1024)
            comparison, failures = observe(directory / "work", json.loads(EXPECTED.read_text()),
                                           observation["source"], args.output)
            result, cases = verdict(comparison, failures)
            proof = {"result": result, "cases": cases, "bridge_failures": failures,
                     "differences": len(comparison["differences"])}
    except Rejected as error:
        print(json.dumps({"result": "FAIL", "error": str(error)}))
        return 1
    print(json.dumps({key: proof.get(key) for key in ("result", "cases", "bridge_failures", "differences", "error")}))
    return 0 if proof.get("result") == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
