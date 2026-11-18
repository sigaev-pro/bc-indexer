#!/usr/bin/env python3
"""Prepare, run and judge the Bor fault probe of E01-04.early_faults.

`prepare BUILD_MANIFEST --sha256 SHA --output OUT` installs the accepted Bor
tree as the accepted Bor lifecycle preparation does (lifecycle_tree.install:
the admission, decision and lifecycle Bor blocks), applies the
E01-04.early_faults patch set of adapters/bor/faults/native, copies the probe
(main.go) to cmd/extractor-faults-bor with the Heimdall stub in its
heimdallstub subdirectory and, with the accepted offline compiler, checks the
format, vets the patched packages and the probe and builds it. It writes a
tool manifest that the caller must pin.

`run TOOL --sha256 SHA --corpus-run RUN --corpus-sha256 SHA --output OUT`,
under the host lock, runs the probe once in the pinned runtime image on the
POLY smoke corpus blocks of the accepted E01-03.smoke_corpus run (its
blocks.rlp, and the genesis, chain config and Heimdall stub bodies of its
preparation; the run record is pinned by its SHA-256), then judges the probe
report.

`judge REPORT` judges a saved probe report. The judgement builds each case's
corpus observation as poly_corpus.observation_of does, compares it with
test/native_corpus/expected/poly.json by corpus_compare.py without only its
per-block `decisions` rows (block 0, "Report format"), and adds the probe's
own errors. Public cells stay NOT_RUN.
"""

import argparse
import json
from pathlib import Path
import re
import sys
import tarfile
import time
import uuid

FIXTURE = Path(__file__).resolve().parent
ROOT = FIXTURE.parents[2]
HARNESS = ROOT / "test" / "harness"
NATIVE = ROOT / "adapters" / "bor" / "faults" / "native"
sys.path.insert(0, str(HARNESS))
sys.path.insert(0, str(HARNESS / "fixtures" / "admission_geth"))
sys.path.insert(0, str(ROOT / "adapters" / "bor" / "lifecycle" / "native"))
sys.path.insert(0, str(ROOT / "test" / "native_corpus"))
sys.path.insert(0, str(ROOT / "test" / "native_corpus" / "poly"))
sys.path.insert(0, str(ROOT / "test" / "native_faults"))
import admission_geth  # noqa: E402
from artifacts import (artifact, copy_artifact, load, pinned_json, read_bytes, reference, require,  # noqa: E402
                       source_snapshot, sources_unchanged)
import corpus_compare  # noqa: E402
import faults_judge  # noqa: E402
import lifecycle_tree  # noqa: E402
import pins  # noqa: E402
import poly_corpus  # noqa: E402

PROBE = FIXTURE / "main.go"
HELP_PROBE = ROOT / "adapters/bor/lifecycle/native/probe/main.go"
HELP_TEXT = '"block whose milestone is written in BOOTSTRAP before a vote holds the guard across GoLive"'
PROBE_DIR = "cmd/extractor-faults-bor"
STUB = HARNESS / "fixtures" / "source_inputs_bor" / "heimdallstub" / "stub.go"
EXPECTED = ROOT / "test" / "native_corpus" / "expected" / "poly.json"
HARNESS_FILES = ("artifacts.py", "native.py", "owned.py", "pins.py", "bor_admission_tree.py", "admission.py",
                 "capture_probe.py")
BLOCK_FILES = ("test/native_faults/poly/faults_poly.py", "test/native_faults/poly/main.go",
               "test/native_faults/poly/handoffs.go", "adapters/bor/lifecycle/native/probe/main.go",
               "adapters/bor/faults/native/patches.json", "adapters/bor/lifecycle/native/lifecycle_tree.py",
               "test/native_corpus/poly/poly_corpus.py", "test/native_corpus/corpus_compare.py",
               "test/native_faults/faults_judge.py", "test/native_faults/faults.json")
PREPARATION_SECONDS = 2 * 3600
RUN_SECONDS = 1800
BUILD_CAP = 2400
PROBE_CAP = 1200
BINARY_LIMIT = 512 * 1024 * 1024
STORAGE_MODEL = "pebble-v1-strict-mem"
DECISION_FIELDS = ("decisions",)
BLOCKS = 4
BLOCK_FIELDS = ("number", "hash", "state_root", "base_fee", "gas_limit", "gas_used", "body_tx_count",
                "state_after_block")

# ---------------------------------------------------------------- judgement


def observation_of(case, source):
    """The corpus observation of one case, as poly_corpus.observation_of builds it."""
    export = case["export"]
    syncs = {}
    for event in case["state_sync"]:
        syncs.setdefault(event["block"], []).append(event)
    blocks = []
    for row in export["blocks"]:
        state_sync = []
        for event in syncs.get(row["number"], []):
            require(row["state_sync_receipt"], f"Block {row['number']} has state sync events and no receipt")
            state_sync.append({"id": event["id"], "contract": event["contract"], "data": event["data"],
                               "logs": row["state_sync_receipt_logs"],
                               "state_receiver_last_state_id": int(row["state_receiver_last_state_id"])})
        blocks.append({key: row[key] for key in BLOCK_FIELDS} |
                      {"transactions": [poly_corpus.transaction_of(tx) for tx in row["transactions"]],
                       "system_transactions": [], "state_sync": state_sync})
    return {"schema_version": 1, "kind": "native-corpus-observation", "family": "poly", "chain_id": 15001,
            "source": source, "blocks": blocks, "decisions": export["decisions"], "reimport": export["reimport"]}


def corpus_result(expected, case, source):
    """Return PASS or FAIL for one case's export, with the differences that count."""
    if not isinstance(case.get("export"), dict):
        return "FAIL", ["no export"]
    try:
        comparison = corpus_compare.compare(expected, observation_of(case, source))
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
        result, rows = corpus_result(expected, case, source)
        final = dict(case.get("final") or {}, corpus=result)
        corpus[f"{case.get('point')}/{case.get('variant')}"] = rows
        cases.append({key: case.get(key) for key in ("point", "variant", "fault", "storage", "after_restart")} |
                     {"final": final})
    return {"schema_version": 1, "kind": "native-faults-report", "family": "poly",
            "storage_model": probe.get("storage_model"), "source": source, "cases": cases,
            "handoffs": handoff_verdicts(probe)}, corpus


def handoff_verdicts(probe):
    """Derive every handoff from required native facts, never supplied verdicts."""
    runs = probe.get("handoff_runs")
    if not isinstance(runs, dict):
        return []
    checks = {"bor.probe-flag-help": HELP_TEXT in HELP_PROBE.read_text()}
    for hid, row in runs.items():
        if not isinstance(row, dict):
            checks[hid] = False
            continue
        try:
            valid = row["id"] == hid and row["phase_at_start"] == "BOOTSTRAP" and not row.get("child_error")
            if hid == "bor.pass-at-golive":
                valid = valid and all(row[k] is True for k in ("native_head_write_held", "golive_attempted"))
                valid = valid and row["boundary_during_import"] is None
                valid = valid and all(row[k] is None for k in ("import_error", "golive_error", "block2_error"))
                valid = valid and row["boundary"] == dict(row["block1"], number=1)
                valid = valid and row["observations_before_block2"] == 0 and row["decided_before_block2"] == []
                valid = valid and row["decided"] == [2] and row["head"] == 2
            elif hid == "bor.below-h-rewind":
                valid = valid and "rewind to 0 is below the live boundary 1" in row["rewind_error"]
                valid = valid and row["boundary_before"] == dict(row["block1"], number=1)
                valid = valid and row["boundary_after"] == row["boundary_before"]
                valid = valid and row["head_before"] == row["head_after"] and row["head"] == 2
                valid = valid and row["decisions_before"] == row["decisions_after"]
            elif hid in ("bor.bootstrap-vote-pass", "bor.golive-order"):
                valid = valid and row["vote_locked"] is True and row["guard_matches_vote"] is True
                valid = valid and row["order"] == ["chain-locked", "guard-contended", "vote-release", "guard-acquired"]
                valid = valid and row["boundary_held"] is None and row["boundary_before_vote_release"] is None
                valid = valid and row["decisions_held"] == {} and row["decisions_before_golive"] == {}
                before = row["coverage_before"].get("bor.milestone-lock", {"Passes": 0, "Intents": 0, "Refusals": 0})
                held = row["coverage_held"]["bor.milestone-lock"]
                valid = valid and held == dict(before, Passes=before["Passes"] + 1)
                valid = valid and row["coverage_released"]["bor.milestone-lock"] == held
                valid = valid and row["golive_error"] is None and row["phase"] == "LIVE"
                valid = valid and row["boundary"] == dict(row["block1"], number=1)
            elif hid == "bor.power-loss-model":
                valid = valid and row["root_present_before_fixture"] is True and row["fixture_deletion_synced"] is True
                valid = valid and row["sentinel_before_reset"] is True and row["sentinel_after_reset"] is False
                valid = valid and row["reset_to_synced"] is True and row["fixture_root_after_reset"] is False
                valid = valid and row["restart_error"] is None and row["phase_after"] == "LIVE"
                valid = valid and row["head_before"] == 4 and row["head_after"] == 3
                valid = valid and row["decided_before"] == [1, 2, 3, 4] and row["undecided"] == []
                valid = valid and row["roots_distinct"] is True and row["predecessor_state_before"] is True
                valid = valid and row["predecessor_state_after"] is True and row["predecessor"]["number"] == 3
                valid = valid and row["boundary_before"]["number"] == 0
                valid = valid and row["boundary_after"] == row["boundary_before"]
                repaired = row["recovery"]["repaired"]
                valid = valid and repaired["number"] == 3 and repaired["hash"] == row["predecessor"]["hash"]
            else:
                valid = False
        except (KeyError, TypeError, AttributeError):
            valid = False
        checks[hid] = valid
    return [{"id": hid, "verdict": "PASS" if passed else "FAIL", "evidence": f"handoff_runs.{hid}"}
            for hid, passed in sorted(checks.items())]


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
        for key in ("child_error", "restart_error", "golive_error"):
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
        if export.get("export_error"):
            errors.append(f"{tag}: export error {export['export_error']}")
        # The export re-executes every block and stops on a receipt that
        # differs from the stored one (exportChain), so a written export
        # holds one checked row per block.
        if [row.get("number") for row in export.get("blocks") or []] != list(range(1, BLOCKS + 1)):
            errors.append(f"{tag}: the export does not hold one row per block")
    return errors


def judge(probe, expected=None, source=None):
    """Return (verdict, corpus differences, faults report) for one probe report."""
    expected = expected or json.loads(EXPECTED.read_text())
    source = source or {"native_build": "unrecorded", "tool": "faults_poly.py judge"}
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


def extract(archive, source):
    with tarfile.open(archive) as stream:
        for member in stream:
            stream.extract(member, source, filter="data")


def prepare(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    require(manifest_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    manifest_ref, manifest = pinned_json(manifest_path, expected_sha, 64 * 1024 * 1024, deadline)
    require(manifest.get("evidence_kind") == "actual-build" and manifest.get("task_id") == "E01-01.builds",
            "An accepted actual build is required")
    manifest_root = manifest_path.parent
    require(not output.is_relative_to(manifest_root), "Preparation cannot write into accepted build storage")
    proof = {"schema_version": 1, "kind": "bor-faults-preparation", "result": "FAIL", "build_manifest": manifest_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": PREPARATION_SECONDS, "runtime": "NOT_RUN", "public_profile_credit": False,
             "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS_FILES, deadline)
        proof["block_sources"] = block_snapshot(output, deadline)
        closure = manifest["module_closures"]["bor"]
        require(closure["source_commit"] == manifest["builds"]["bor"]["source_commit"], "Source closure mismatch")
        compiler = manifest["image_inputs"]["golang-build-image"]["reference"]
        require(re.fullmatch(r"docker.io/library/golang@sha256:[0-9a-f]{64}", compiler), "Unpinned compiler")
        require(manifest["builds"]["bor"]["compiler_image"] == compiler, "Bor compiler mismatch")
        runtime = manifest["image_inputs"]["debian-runtime-image"]["reference"]
        require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
        root = output / "bor"
        root.mkdir()
        archive = artifact(closure["source_archive"], 512 * 1024 * 1024, manifest_root, deadline)
        copied = copy_artifact(archive, root / "source.tar", closure["source_archive"], 512 * 1024 * 1024, deadline)
        for module in closure["artifacts"]:
            for field in ("info", "mod", "zip"):
                artifact(module[field], 1024 * 1024 * 1024, manifest_root, deadline)
        source = root / "source"
        source.mkdir()
        extract(root / "source.tar", source)
        installed = lifecycle_tree.install(source)
        faults = admission_geth.apply_patches(source, NATIVE)
        target = source / PROBE_DIR
        require(not target.exists(), f"{PROBE_DIR} already exists in the native tree")
        (target / "heimdallstub").mkdir(parents=True)
        probe_ref = copy_artifact(PROBE, target / "main.go", reference(PROBE, 4 * 1024 * 1024, deadline),
                                  4 * 1024 * 1024, deadline)
        handoff = FIXTURE / "handoffs.go"
        handoff_ref = copy_artifact(handoff, target / handoff.name, reference(handoff, deadline=deadline),
                                    deadline=deadline)
        stub_ref = copy_artifact(STUB, target / "heimdallstub" / "stub.go", reference(STUB, 4 * 1024 * 1024, deadline),
                                 4 * 1024 * 1024, deadline)
        for name in ("bin", "build-cache", "gopath", "gotmp"):
            (root / name).mkdir()
        mounts = [(root, "/work", True), (manifest_root / "cache" / "bor" / "mod", "/modules", True)]
        mounts += [(root / name, f"/work/{name}", False) for name in ("bin", "build-cache", "gopath", "gotmp")]
        env = poly_corpus.go_environment()

        def go(argv, cap):
            return runner.command(compiler, argv, mounts, cap=cap, memory="6g", cwd="/work/source", environment=env)

        stdout = go(["/usr/local/go/bin/gofmt", "-l", PROBE_DIR, "ethdb/pebble", "node"], 300)
        require(not read_bytes(stdout, 1024 * 1024).strip(), "gofmt reports unformatted files")
        vet = go(["/usr/local/go/bin/go", "vet", "./ethdb/pebble/", "./node/", f"./{PROBE_DIR}/..."], BUILD_CAP)
        go(["/usr/local/go/bin/go", "build", "-p", "2", "-trimpath", "-o", "/work/bin/probe", f"./{PROBE_DIR}"],
           BUILD_CAP)
        sources_unchanged(origins, deadline)
        sources_unchanged(proof["block_sources"], deadline)
        proof["checks"] = {"vet": reference(vet)}
        tool = {"schema_version": 1, "kind": "bor-faults-tool", "build_manifest": manifest_ref,
                "native_source_commit": closure["source_commit"], "source_archive": copied, "compiler": compiler,
                "runtime": runtime, "installed": installed, "faults_install": faults, "probe": probe_ref,
                "heimdall_stub": stub_ref, "handoffs": handoff_ref,
                "binary": reference(root / "bin" / "probe", BINARY_LIMIT, deadline)}
        pins.save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "preparation.json", body)


# ---------------------------------------------------------------- measured run


def run(tool_path, tool_sha, corpus_path, corpus_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(tool_path.is_absolute() and corpus_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "bor-faults-tool", "Not a Bor faults tool")
    corpus_ref, corpus = pinned_json(corpus_path, corpus_sha, 64 * 1024 * 1024, deadline=deadline)
    require(corpus.get("kind") == "poly-corpus-run" and corpus.get("result") == "PASS",
            "A passing POLY smoke corpus run is required")
    corpus_prep_ref, corpus_prep = pinned_json(Path(corpus["preparation"]["path"]), corpus["preparation"]["sha256"],
                                               16 * 1024 * 1024, deadline)
    require(corpus_prep.get("kind") == "poly-corpus-preparation" and corpus_prep.get("result") == "PASS",
            "The corpus run's preparation did not pass")
    require(corpus_prep["bor"]["source_commit"] == tool["native_source_commit"],
            "The corpus blocks come from another Bor source")
    expected = json.loads(EXPECTED.read_text())
    proof = {"schema_version": 1, "kind": "bor-faults-run", "result": "FAIL", "tool": tool_ref,
             "corpus_run": corpus_ref, "corpus_preparation": corpus_prep_ref, "source_commit": pins.source_commit(),
             "run_id": uuid.uuid4().hex, "native_owner_nonce": None, "deadline_seconds": RUN_SECONDS,
             "public_profile_credit": False,
             "profile": "private local Bor fixture chain 15001 (POLY private local; not a public profile)",
             "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS_FILES, deadline)
        proof["block_sources"] = block_snapshot(output, deadline)
        root = output / "bor"
        for name in ("inputs/stub", "work"):
            (root / name).mkdir(parents=True)
        inputs = {}
        for name in ("genesis.json", "chain-config.json"):
            row = corpus_prep["bor"][name]
            inputs[name] = copy_artifact(Path(row["path"]), root / "inputs" / name, row, 4 * 1024 * 1024, deadline)
        row = corpus["outputs"]["chain/blocks.rlp"]
        inputs["blocks.rlp"] = copy_artifact(Path(row["path"]), root / "inputs/blocks.rlp", row, 4 * 1024 * 1024,
                                             deadline)
        for body_row in corpus_prep["stub_bodies"]:
            row = body_row["copy"]
            name = Path(row["path"]).name
            inputs[f"stub/{name}"] = copy_artifact(Path(row["path"]), root / "inputs/stub" / name, row, 1024 * 1024,
                                                   deadline)
        proof["inputs"] = inputs
        binary = artifact(tool["binary"], BINARY_LIMIT, deadline=deadline)
        mounts = [(binary.parent, "/tool", True), (root / "inputs", "/inputs", True), (root / "work", "/work", False)]
        argv = ["/tool/" + binary.name, "run", "-genesis", "/inputs/genesis.json", "-config",
                "/inputs/chain-config.json", "-bodies", "/inputs/stub", "-blocks", "/inputs/blocks.rlp",
                "-commit", tool["native_source_commit"], "-accounts", ",".join(poly_corpus.accounts_of(expected)),
                "-work", "/work/cases", "-report", "/work/report.json"]
        proof["phases"] = {"execution_started": time.time()}
        stdout = runner.command(tool["runtime"], argv, mounts, cap=PROBE_CAP, memory="4g", cwd="/work")
        proof["phases"]["execution_finished"] = time.time()
        proof["stdout"] = reference(stdout, deadline=deadline)
        work = root / "work"
        proof["report"] = reference(work / "report.json", 64 * 1024 * 1024, deadline)
        probe = load(work / "report.json", 64 * 1024 * 1024)
        source = {"native_build": tool["binary"]["sha256"], "native_commit": tool["native_source_commit"],
                  "tool": "test/native_faults/poly/faults_poly.py", "tool_manifest": tool_ref["sha256"]}
        verdict, corpus_rows, report = judge(probe, expected, source)
        (work / "faults-report.json").write_text(json.dumps(report, indent=1, sort_keys=True) + "\n")
        proof["faults_report"] = reference(work / "faults-report.json", 64 * 1024 * 1024, deadline)
        proof["verdict"], proof["corpus_differences"] = verdict, corpus_rows
        proof["seconds_in_probe"] = probe.get("elapsed_seconds")
        sources_unchanged(origins, deadline)
        sources_unchanged(proof["block_sources"], deadline)
        artifact(tool["binary"], BINARY_LIMIT, deadline=deadline)
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
