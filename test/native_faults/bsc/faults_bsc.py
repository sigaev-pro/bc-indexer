#!/usr/bin/env python3
"""Prepare, run and judge the BSC fault probe of E01-04.early_faults.

`prepare BUILD_MANIFEST --sha256 SHA --output OUT` installs the accepted BSC
tree as the accepted BSC lifecycle preparation does (bsc_lifecycle.install:
the admission and decision patch sets, patch 08 and the lifecycle packages),
applies the E01-04.early_faults patch set of adapters/bsc/faults/native,
installs the probe as extractor/bsc/faults/faults_test.go and, with the
accepted offline compiler, checks the format, vets the patched and installed
packages and compiles the probe test binary. It writes a tool manifest that
the caller must pin.

`run TOOL --sha256 SHA --corpus-run RUN --corpus-sha256 SHA --output OUT`,
under the host lock, runs TestBSCFaults once in the compiler image on the
smoke corpus BSC blocks of the accepted E01-03.smoke_corpus run (its
genesis.json and blocks.rlp, pinned by the SHA-256 of its run.json), then
judges the probe report.

`judge REPORT` judges a saved probe report. The judgement builds each case's
corpus observation as bsc_corpus.observation_of does, compares it with
test/native_corpus/expected/bsc.json by corpus_compare.py without only its
per-block `decisions` rows (block 0, "Report format"), and adds the probe's
own errors. Public cells stay NOT_RUN.
"""

import argparse
import json
from pathlib import Path
import sys
import time
import uuid

FIXTURE = Path(__file__).resolve().parent
ROOT = FIXTURE.parents[2]
HARNESS = ROOT / "test" / "harness"
NATIVE = ROOT / "adapters" / "bsc" / "faults" / "native"
sys.path.insert(0, str(HARNESS))
sys.path.insert(0, str(HARNESS / "fixtures" / "admission_geth"))
sys.path.insert(0, str(HARNESS / "fixtures" / "lifecycle_bsc"))
sys.path.insert(0, str(ROOT / "test" / "native_corpus"))
sys.path.insert(0, str(ROOT / "test" / "native_corpus" / "bsc"))
sys.path.insert(0, str(ROOT / "test" / "native_faults"))
import admission_geth  # noqa: E402
from artifacts import (artifact, copy_artifact, load, pinned_json, read_bytes, reference, require,  # noqa: E402
                       source_snapshot, sources_unchanged)
import bsc_corpus  # noqa: E402
import bsc_lifecycle  # noqa: E402
import bsc_source_inputs as probe14  # noqa: E402
import corpus_compare  # noqa: E402
import faults_judge  # noqa: E402
import pins  # noqa: E402

PROBE = FIXTURE / "faults_test.go"
PROBE_TARGET = "extractor/bsc/faults/faults_test.go"
PACKAGE = "./extractor/bsc/faults/"
BINARY = "bsc-faults.test"
PROBE_TEST = "TestBSCFaults"
EXPECTED = ROOT / "test" / "native_corpus" / "expected" / "bsc.json"
HARNESS_FILES = bsc_lifecycle.HARNESS_FILES
BLOCK_FILES = ("test/native_faults/bsc/faults_bsc.py", "test/native_faults/bsc/faults_test.go",
               "adapters/bsc/faults/native/patches.json", "test/native_faults/faults_judge.py",
               "test/native_faults/faults.json")
PREPARATION_SECONDS = 2 * 3600
RUN_SECONDS = 1200
PROBE_CAP = 600
BINARY_LIMIT = 1024 * 1024 * 1024
STORAGE_MODEL = "pebble-v1-strict-mem"
DECISION_FIELDS = ("decisions",)
BLOCKS = 2
OBSERVATION_FIELDS = ("number", "hash", "state_root", "base_fee", "gas_limit", "gas_used", "body_tx_count",
                      "system_transactions", "system_nonce_after", "state_after_block")

# ---------------------------------------------------------------- judgement


def observation_of(export, source):
    """The corpus observation of one case, as bsc_corpus.observation_of builds it."""
    blocks = []
    for row in export["blocks"]:
        blocks.append({key: row[key] for key in OBSERVATION_FIELDS} |
                      {"transactions": [bsc_corpus.transaction_of(tx) for tx in row["transactions"]],
                       "state_sync": []})
    return {"schema_version": 1, "kind": "native-corpus-observation", "family": "bsc", "chain_id": 714,
            "source": source, "blocks": blocks, "decisions": export["decisions"], "reimport": export["reimport"]}


def corpus_result(expected, export, source):
    """Return PASS or FAIL for one case's export, with the differences that count."""
    if not isinstance(export, dict):
        return "FAIL", ["no export"]
    try:
        comparison = corpus_compare.compare(expected, observation_of(export, source))
    except (corpus_compare.FormatError, ValueError, KeyError, TypeError) as error:
        return "FAIL", [f"format: {error}"]
    rows = [row for row in comparison["differences"] if row["field"] not in DECISION_FIELDS]
    return ("FAIL" if rows else "PASS"), [f"{row['place']} {row['field']}" for row in rows]


def to_faults_report(probe, expected, source):
    """Build the native faults report of faults_judge.py from a probe report."""
    cases, corpus = [], {}
    for case in probe.get("cases", []):
        if not isinstance(case, dict):
            continue
        result, rows = corpus_result(expected, case.get("export"), source)
        final = dict(case.get("final") or {}, corpus=result)
        corpus[f"{case.get('point')}/{case.get('variant')}"] = rows
        cases.append({key: case.get(key) for key in ("point", "variant", "fault", "storage", "after_restart")} |
                     {"final": final})
    return {"schema_version": 1, "kind": "native-faults-report", "family": "bsc",
            "storage_model": probe.get("storage_model"), "source": source, "cases": cases,
            "handoffs": probe.get("handoffs", [])}, corpus


def native_errors(probe):
    """Errors of the probe's own steps that the faults report does not carry."""
    errors = []
    if probe.get("storage_model") != STORAGE_MODEL:
        errors.append(f"probe: storage_model is not {STORAGE_MODEL}")
    for index, case in enumerate(probe.get("cases", [])):
        if not isinstance(case, dict):
            errors.append(f"case {index}: malformed")
            continue
        tag = f"{case.get('point')}/{case.get('variant')}"
        for key in ("restart_error", "golive_error"):
            if case.get(key):
                errors.append(f"{tag}: {key} {case[key]}")
        if (case.get("import") or {}).get("error"):
            errors.append(f"{tag}: import error {case['import']['error']}")
        export = case.get("export")
        if not isinstance(export, dict):
            continue  # the corpus result fails it
        if export.get("undecided_intents") != []:
            errors.append(f"{tag}: undecided intents {export.get('undecided_intents')}")
        if export.get("reimport_error"):
            errors.append(f"{tag}: re-import error {export['reimport_error']}")
        # The native re-execution of the BSC corpus probe stops the test on a
        # receipt that differs (exportBlock), so a written export holds one
        # checked row per block.
        if [row.get("number") for row in export.get("blocks") or []] != list(range(1, BLOCKS + 1)):
            errors.append(f"{tag}: the export does not hold one row per block")
    return errors


def judge(probe, expected=None, source=None):
    """Return (verdict, corpus differences, faults report) for one probe report."""
    expected = expected or json.loads(EXPECTED.read_text())
    source = source or {"native_build": "unrecorded", "tool": "faults_bsc.py judge"}
    report, corpus = to_faults_report(probe, expected, source)
    verdict = faults_judge.judge(report, json.loads(faults_judge.CATALOG.read_text()))
    errors = native_errors(probe)
    if errors and verdict.get("result") != "FAIL":
        verdict = dict(verdict, result="FAIL")
    verdict["native_errors"] = errors
    return verdict, corpus, report


# ---------------------------------------------------------------- preparation


def block_snapshot(output, deadline=None):
    """Copy the block files that decide the verdict into the record; return their references."""
    origins = []
    for name in BLOCK_FILES:
        origin = ROOT / name
        origins.append(reference(origin, 4 * 1024 * 1024, deadline))
        target = output / "block-code" / name
        target.parent.mkdir(parents=True, exist_ok=True)
        copy_artifact(origin, target, origins[-1], 4 * 1024 * 1024, deadline)
    return origins


def prepare(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    require(manifest_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    manifest_ref = reference(manifest_path, 64 * 1024 * 1024, deadline)
    require(manifest_ref["sha256"] == expected_sha, "BuildManifest digest mismatch")
    manifest = load(manifest_path, 64 * 1024 * 1024, deadline)
    require(not output.is_relative_to(manifest_path.parent), "Preparation cannot write into accepted build storage")
    proof = {"schema_version": 1, "kind": "bsc-faults-preparation", "result": "FAIL", "build_manifest": manifest_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": PREPARATION_SECONDS, "runtime": "NOT_RUN", "public_profile_credit": False,
             "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS_FILES, deadline)
        proof["block_sources"] = block_snapshot(output, deadline)
        root = output / "bsc"
        root.mkdir()
        closure, copied, source = probe14.extract_source(manifest, manifest_path, root, deadline)
        patched, installed = bsc_lifecycle.install(source, root, deadline)
        faults = admission_geth.apply_patches(source, NATIVE)
        target = source / PROBE_TARGET
        require(not target.exists(), f"{PROBE_TARGET} already exists in the native tree")
        target.parent.mkdir(parents=True)
        probe_ref = copy_artifact(PROBE, target, reference(PROBE, 4 * 1024 * 1024, deadline), 4 * 1024 * 1024,
                                  deadline)
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (root / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            copy_artifact(source / name, root / "metadata" / name, reference(source / name, deadline=deadline),
                          deadline=deadline)
        compiler = manifest["builds"]["bsc"]["compiler_image"]
        require(compiler == manifest["image_inputs"]["golang-build-image"]["reference"], "Unexpected compiler image")
        mounts = probe14.build_mounts(source, manifest_path.parent / "cache" / "bsc" / "mod", root)
        env = pins.environment()
        stdout = runner.command(compiler, ["/usr/local/go/bin/gofmt", "-l", "extractor/bsc/faults", "ethdb/pebble"],
                                mounts, cap=300, memory="2g", cwd="/source", environment=env)
        require(not read_bytes(stdout, 1024 * 1024).strip(), "gofmt reports unformatted files")
        vet = runner.command(compiler, ["/usr/local/go/bin/go", "vet", "./ethdb/pebble/", PACKAGE], mounts,
                             cap=bsc_lifecycle.base.BUILD_CAP, memory="6g", cwd="/source", environment=env)
        runner.command(compiler, ["/usr/local/go/bin/go", "test", "-c", "-trimpath", "-o", "/work/generated/" + BINARY,
                                  PACKAGE], mounts, cap=bsc_lifecycle.base.BUILD_CAP, memory="6g", cwd="/source",
                       environment=env)
        sources_unchanged(origins, deadline)
        sources_unchanged(proof["block_sources"], deadline)
        proof["checks"] = {"vet": reference(vet)}
        tool = {"schema_version": 1, "kind": "bsc-faults-tool", "build_manifest": manifest_ref,
                "native_source_commit": closure["source_commit"], "source_archive": copied, "compiler": compiler,
                "patched": patched, "installed": installed, "faults_install": faults, "probe": probe_ref,
                "test_binary": reference(root / "generated" / BINARY, BINARY_LIMIT, deadline)}
        pins.save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "preparation.json", body)


# ---------------------------------------------------------------- measured run


def run(tool_path, tool_sha, corpus_path, corpus_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(tool_path.is_absolute() and corpus_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "bsc-faults-tool", "Not a BSC faults tool")
    corpus_ref, corpus = pinned_json(corpus_path, corpus_sha, 64 * 1024 * 1024, deadline=deadline)
    require(corpus.get("kind") == "bsc-corpus-run" and corpus.get("result") == "PASS",
            "A passing BSC smoke corpus run is required")
    expected = json.loads(EXPECTED.read_text())
    proof = {"schema_version": 1, "kind": "bsc-faults-run", "result": "FAIL", "tool": tool_ref,
             "corpus_run": corpus_ref, "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex,
             "native_owner_nonce": None, "deadline_seconds": RUN_SECONDS, "public_profile_credit": False,
             "profile": "private local BSC Parlia fixture chain 714 (BSC private local; not a public profile)",
             "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS_FILES, deadline)
        proof["block_sources"] = block_snapshot(output, deadline)
        root = output / "bsc"
        for name in ("inputs", "work", "scratch"):
            (root / name).mkdir(parents=True)
        proof["inputs"] = {
            "genesis.json": copy_artifact(Path(corpus["inputs"]["genesis.json"]["path"]), root / "inputs/genesis.json",
                                          corpus["inputs"]["genesis.json"], 4 * 1024 * 1024, deadline),
            "blocks.rlp": copy_artifact(Path(corpus["outputs"]["blocks.rlp"]["path"]), root / "inputs/blocks.rlp",
                                        corpus["outputs"]["blocks.rlp"], 4 * 1024 * 1024, deadline)}
        binary = artifact(tool["test_binary"], BINARY_LIMIT, deadline=deadline)
        mounts = [(binary.parent, "/tool", True), (root / "inputs", "/inputs", True), (root / "work", "/work", False),
                  (root / "scratch", "/scratch", False)]
        env = {"EXTRACTOR_FAULTS_INPUTS": "/inputs", "EXTRACTOR_FAULTS_WORK": "/work/cases",
               "EXTRACTOR_FAULTS_REPORT": "/work/report.json",
               "EXTRACTOR_FAULTS_NATIVE_COMMIT": tool["native_source_commit"],
               "EXTRACTOR_FAULTS_ACCOUNTS": ",".join(bsc_corpus.accounts_of(expected)),
               "TMPDIR": "/scratch", "HOME": "/scratch"}
        proof["phases"] = {"execution_started": time.time()}
        stdout = runner.command(tool["compiler"], ["/tool/" + binary.name, "-test.v", "-test.count=1", "-test.run",
                                                   f"^{PROBE_TEST}$", "-test.timeout", f"{PROBE_CAP - 60}s"],
                                mounts, cap=PROBE_CAP, memory="4g", cwd="/scratch", environment=env)
        proof["phases"]["execution_finished"] = time.time()
        pins.parse_go_test(read_bytes(stdout, 16 * 1024 * 1024, deadline).decode(), [PROBE_TEST])
        proof["stdout"] = reference(stdout, deadline=deadline)
        work = root / "work"
        proof["report"] = reference(work / "report.json", 64 * 1024 * 1024, deadline)
        probe = load(work / "report.json", 64 * 1024 * 1024)
        source = {"native_build": tool["test_binary"]["sha256"], "native_commit": tool["native_source_commit"],
                  "tool": "test/native_faults/bsc/faults_bsc.py", "tool_manifest": tool_ref["sha256"]}
        verdict, corpus_rows, report = judge(probe, expected, source)
        (work / "faults-report.json").write_text(json.dumps(report, indent=1, sort_keys=True) + "\n")
        proof["faults_report"] = reference(work / "faults-report.json", 64 * 1024 * 1024, deadline)
        proof["verdict"], proof["corpus_differences"] = verdict, corpus_rows
        proof["seconds_in_probe"] = probe.get("elapsed_seconds")
        sources_unchanged(origins, deadline)
        sources_unchanged(proof["block_sources"], deadline)
        artifact(tool["test_binary"], BINARY_LIMIT, deadline=deadline)
        require(verdict["result"] in ("PASS", "INCOMPLETE"),
                f"the fault verdict is {verdict['result']}: {(verdict['failures'] + verdict['native_errors'])[:5]}")

    return pins.owned_attempt(output, started, deadline, proof, "run.json", body)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    first = commands.add_parser("prepare")
    first.add_argument("manifest", type=Path)
    first.add_argument("--sha256", required=True)
    first.add_argument("--output", type=Path, required=True)
    second = commands.add_parser("run")
    second.add_argument("tool", type=Path)
    second.add_argument("--sha256", required=True)
    second.add_argument("--corpus-run", type=Path, required=True)
    second.add_argument("--corpus-sha256", required=True)
    second.add_argument("--output", type=Path, required=True)
    third = commands.add_parser("judge")
    third.add_argument("report", type=Path)
    args = parser.parse_args(argv)
    try:
        if args.command == "judge":
            verdict, corpus, _ = judge(json.loads(args.report.read_text()))
            print(json.dumps({"verdict": verdict, "corpus_differences": corpus}, indent=1, sort_keys=True))
            return 0 if verdict["result"] == "PASS" else 1
        if args.command == "prepare":
            record = prepare(args.manifest, args.sha256, args.output)
        else:
            record = run(args.tool, args.sha256, args.corpus_run, args.corpus_sha256, args.output)
    except Exception as error:  # a refusal before any record exists
        print(json.dumps({"valid": False, "error": f"{type(error).__name__}: {error}"}))
        return 2
    print(json.dumps({"result": record.get("result"), "verdict": (record.get("verdict") or {}).get("result"),
                      "error": record.get("error")}))
    return 0 if record.get("result") == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
