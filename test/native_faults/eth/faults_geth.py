#!/usr/bin/env python3
"""Build, run and judge the Geth fault probe of E01-04.early_faults.

`prepare` starts from the accepted Geth decision-bridge preparation (pinned by
the SHA-256 of its preparation.json), installs the accepted tree exactly as
the accepted Geth lifecycle preparation did (the admission and decision-bridge
patch sets and packages, proven equal to the accepted install record, then the
lifecycle patch set and packages), applies the E01-04.early_faults patch set of
adapters/geth/faults/native, adds the probe as cmd/extractor-faults-geth and,
offline with the accepted compiler and module cache, runs go mod verify,
go vet and builds the probe with the native-commit stamp. `run` executes the
probe in the pinned runtime image under the host lock and judges its report.
`judge` judges a saved probe report. Public cells stay NOT_RUN.

The judgement turns the probe report into a native faults report: each case's
corpus observation is compared with test/native_corpus/expected/eth.json by
corpus_compare.py. Only its per-block `decisions` rows (one IMPORT per block)
are left out of the corpus result, as block 0 says: after a fault a recovered
obligation may close as a CANONICAL activation, and faults_judge.py rule 4
judges the delivery of each block. `decisions of other blocks` and the
re-import rows still count, and so does every other difference.
"""

import argparse
import io
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
NATIVE = ROOT / "adapters" / "geth" / "faults" / "native"
sys.path.insert(0, str(HARNESS))
sys.path.insert(0, str(HARNESS / "fixtures" / "admission_geth"))
sys.path.insert(0, str(HARNESS / "fixtures" / "lifecycle_geth"))
sys.path.insert(0, str(ROOT / "test" / "native_corpus"))
sys.path.insert(0, str(ROOT / "test" / "native_corpus" / "eth"))
sys.path.insert(0, str(ROOT / "test" / "native_faults"))
from artifacts import (artifact, check_deadline, copy_artifact, json_artifact, load, pinned_json,  # noqa: E402
                       read_bytes, reference, require)
import admission_geth  # noqa: E402
import capture_probe  # noqa: E402
from capture_probe import Interrupted, block_stops, single_writer, unblock_stops, write_until_done  # noqa: E402
import corpus_compare  # noqa: E402
import corpus_eth  # noqa: E402
import faults_judge  # noqa: E402
import lifecycle_geth  # noqa: E402
from native import Native, new_output  # noqa: E402

PROBE_PATH = "cmd/extractor-faults-geth/main.go"
FIXTURE_FILES = ("probe.go", "handoffs.go", "faults_geth.py", "README.md")
NATIVE_FILES = ("patches.json", "README.md")
GENESIS = HARNESS / "fixtures" / "genesis.json"
EXPECTED = ROOT / "test" / "native_corpus" / "expected" / "eth.json"
NATIVE_SOURCE = lifecycle_geth.NATIVE_SOURCE
STAMP = lifecycle_geth.STAMP
VET_PACKAGES = ("./ethdb/pebble", "./cmd/extractor-faults-geth")
PREPARATION_SECONDS = 3600
RUN_SECONDS = 1200
BINARY_LIMIT = 512 * 1024 * 1024
REPORT_LIMIT = 64 * 1024 * 1024
DECISION_FIELDS = ("decisions",)
STORAGE_MODEL = "pebble-v2-crashable-mem"
BLOCKS = 2

# ---------------------------------------------------------------- judgement


def corpus_result(expected, observation, source):
    """Return PASS or FAIL for one case's observation, with the differences that count."""
    if not isinstance(observation, dict):
        return "FAIL", ["no observation"]
    observation = dict(observation, source=source)
    try:
        comparison = corpus_compare.compare(expected, observation)
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
        result, rows = corpus_result(expected, case.get("observation"), source)
        final = dict(case.get("final") or {}, corpus=result)
        tag = f"{case.get('point')}/{case.get('variant')}"
        corpus[tag] = rows
        cases.append({key: case.get(key) for key in ("point", "variant", "fault", "storage", "after_restart")} |
                     {"final": final})
    return {"schema_version": 1, "kind": "native-faults-report", "family": "eth",
            "storage_model": probe.get("storage_model"), "source": source, "cases": cases,
            "handoffs": handoff_verdicts(probe)}, corpus


def handoff_verdicts(probe):
    """Derive handoff verdicts from native facts; never accept producer verdicts."""
    runs = probe.get("handoff_runs") or {}
    verdicts = []
    for name in ("geth.pass-at-golive", "geth.sethead-interrupted", "geth.mid-reorg-long-branch"):
        row = runs.get(name)
        if not isinstance(row, dict):
            continue
        good = False
        try:
            if name == "geth.pass-at-golive":
                boundary, block = row["boundary"], row["block1"]
                good = (row["held"] is True and row["phase_during_pass"] == "BOOTSTRAP" and
                        row["boundary_during_pass"] is None and row["import_error"] is None and
                        row["golive_error"] is None and row["block2_error"] is None and
                        row["observations_before_block2"] == 0 and row["decisions_before_block2"] == 0 and
                        boundary["number"] == 1 and boundary["hash"] == block["hash"] and
                        boundary["root"] == block["root"] and row["head"] == 2 and
                        (row["returned_during_pass"] is False or isinstance(row["during_error"], str)))
            else:
                records = row["records_before"]
                pending = [(key, json.loads(value)) for key, value in records.items()
                           if key.endswith("/intent") and key[:-7] + "/decided" not in records and
                           key[:-7] + "/rejected" not in records]
                good = len(pending) == 1
                intent = pending[0][1] if good else {}
                target = row["target"]
                evidence = "completed_inside_rewind" if name == "geth.sethead-interrupted" else \
                    "completed_inside_reorg"
                good = (good and intent["kind"] == "CANONICAL" and
                        intent["old_head"]["hash"] == row["old_head"] and
                        intent["new_head"]["hash"] == target["hash"] and
                        row["hit"] == name and row["step_error"] is None and row["restart_error"] is None and
                        row["phase"] == "LIVE" and row["head"] == target and row["state_error"] is None and
                        len(row["settled"]) == 1 and row["settled"][0]["sequence"] == intent["sequence"] and
                        row["settled"][0]["evidence"] == evidence)
                if name == "geth.sethead-interrupted":
                    intermediate = row["intermediate"]
                    good = (good and type(row["head_write_attempts"]) is int and
                            row["head_write_attempts"] == 2 and
                            type(intermediate["number"]) is int and
                            target["number"] < intermediate["number"] < intent["old_head"]["number"] and
                            row["head_before_restart"] == intermediate["hash"] and
                            row["head_before_restart"] not in (row["old_head"], target["hash"]))
                if name == "geth.mid-reorg-long-branch":
                    good = good and row["old_branch_length"] > 1 and \
                        row["head_before_restart"] not in (row["old_head"], target["hash"])
        except (KeyError, TypeError, ValueError, IndexError):
            good = False
        verdicts.append({"id": name, "verdict": "PASS" if good else "FAIL",
                         "evidence": f"handoff_runs.{name}"})
    return verdicts


def reexecution_errors(tag, observation):
    """The native re-execution checks of test/native_corpus/eth/corpus_eth.py, per case."""
    errors = []
    sizes = {block.get("number"): block.get("body_tx_count") for block in observation.get("blocks") or []
             if isinstance(block, dict)}
    checks = (observation.get("native") or {}).get("reexecution")
    if not isinstance(checks, list) or [row.get("number") for row in checks] != list(range(1, BLOCKS + 1)):
        return [f"{tag}: re-execution does not check one row per block"]
    for row in checks:
        number = row.get("number")
        if row.get("root_equal") is not True:
            errors.append(f"{tag}: native re-execution of block {number} root differs")
        if row.get("cumulative_gas_used") != row.get("header_gas_used"):
            errors.append(f"{tag}: native re-execution of block {number} gas differs from the header")
        receipts = row.get("receipts") or []
        if not isinstance(sizes.get(number), int) or \
                [receipt.get("index") for receipt in receipts] != list(range(sizes[number])):
            errors.append(f"{tag}: native re-execution of block {number} does not check one receipt per transaction")
        for receipt in receipts:
            if receipt.get("receipt_equal") is not True:
                errors.append(f"{tag}: native re-execution of block {number} receipt {receipt.get('index')} differs")
    return errors


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
        imported = case.get("import") or {}
        if imported.get("error"):
            errors.append(f"{tag}: import error {imported['error']}")
        observation = case.get("observation")
        if not isinstance(observation, dict):
            continue  # the corpus result fails it
        native = observation.get("native") or {}
        if native.get("undecided_intents") != []:
            errors.append(f"{tag}: undecided intents {native.get('undecided_intents')}")
        if native.get("reimport_error"):
            errors.append(f"{tag}: re-import error {native['reimport_error']}")
        errors.extend(reexecution_errors(tag, observation))
    return errors


def judge(probe, expected=None, source=None):
    """Return (verdict, corpus differences, native errors) for one probe report."""
    expected = expected or json.loads(EXPECTED.read_text())
    source = source or {"native_build": "unrecorded", "tool": "faults_geth.py judge"}
    report, corpus = to_faults_report(probe, expected, source)
    verdict = faults_judge.judge(report, json.loads(faults_judge.CATALOG.read_text()))
    errors = native_errors(probe)
    if errors and verdict.get("result") != "FAIL":
        verdict = dict(verdict, result="FAIL")
    verdict["native_errors"] = errors
    return verdict, corpus, report


# ---------------------------------------------------------------- preparation


def prepare(accepted_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    accepted_ref, accepted = pinned_json(accepted_path, expected_sha, 16 * 1024 * 1024, deadline)
    require(accepted.get("kind") == "admission-geth-preparation" and accepted.get("result") == "PASS" and
            accepted.get("cleanup_complete") is True, "A passing Geth decision-bridge preparation is required")
    require(accepted.get("source_commit") == NATIVE_SOURCE, "The accepted preparation names another Geth commit")
    manifest_root = Path(accepted["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (accepted_path.parent, manifest_root, ROOT))
    state = capture_probe.source_state()
    with single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "faults-geth-preparation", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": state, "accepted_preparation": accepted_ref,
                  "public_profile_credit": False}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            record["fixture_code"] = capture_probe.copy_sources(FIXTURE_FILES, FIXTURE, output / "fixture-code",
                                                                deadline)
            record["faults_native"] = capture_probe.copy_sources(NATIVE_FILES, NATIVE, output / "faults-native",
                                                                 deadline)
            record["lifecycle_native"] = capture_probe.copy_sources(lifecycle_geth.NATIVE_FILES, lifecycle_geth.NATIVE,
                                                                    output / "lifecycle-native", deadline)
            record["lifecycle_shared"] = capture_probe.copy_sources(lifecycle_geth.SHARED_FILES, lifecycle_geth.SHARED,
                                                                    output / "lifecycle-shared", deadline)
            copies, rows = corpus_eth.verified_copies(accepted, deadline)
            copies["lifecycle_native"] = output / "lifecycle-native"
            copies["lifecycle"] = output / "lifecycle-shared"
            record["repository_equal"] = corpus_eth.repository_equal(rows)
            require(corpus_eth.code_equal(record["repository_equal"]),
                    "The repository Geth adapter code differs from the accepted preparation")
            _, manifest = json_artifact(accepted["build_manifest"], 64 * 1024 * 1024, deadline=deadline)
            compiler = accepted["compiler_image"]
            require(manifest["image_inputs"]["golang-build-image"]["reference"] == compiler, "Compiler mismatch")
            require(re.fullmatch(r"docker.io/library/golang@sha256:[0-9a-f]{64}", compiler), "Unpinned compiler")
            record["compiler_image"] = compiler
            record["source_commit"] = accepted["source_commit"]
            directory = output / "geth"
            directory.mkdir()
            archive = artifact(accepted["source_archive"], BINARY_LIMIT, deadline=deadline)
            record["source_archive"] = copy_artifact(archive, directory / "source.tar", accepted["source_archive"],
                                                     BINARY_LIMIT, deadline)
            source = directory / "source"
            source.mkdir()
            data = read_bytes(directory / "source.tar", accepted["source_archive"]["size_bytes"], deadline)
            with tarfile.open(fileobj=io.BytesIO(data)) as stream:
                for member in stream:
                    check_deadline(deadline)
                    stream.extract(member, source, filter="data")
            del data
            record["install"] = corpus_eth.install(source, copies)
            differences = corpus_eth.install_differences(accepted["install"], record["install"])
            require(not differences, f"The install differs from the accepted install: {differences}")
            record["install_equals_accepted"] = True
            record["lifecycle_install"] = lifecycle_geth.install_lifecycle(source, copies)
            record["faults_install"] = admission_geth.apply_patches(source, output / "faults-native")
            probe = source / PROBE_PATH
            probe.parent.mkdir(parents=True)
            record["probe_source"] = copy_artifact(FIXTURE / "probe.go", probe, reference(FIXTURE / "probe.go"))
            record["handoff_source"] = copy_artifact(FIXTURE / "handoffs.go", probe.parent / "handoffs.go",
                                                      reference(FIXTURE / "handoffs.go"))
            for name in ("bin", "build-cache", "gopath", "gotmp"):
                (directory / name).mkdir()
            mounts = [(directory, "/work", True), (manifest_root / "cache" / "geth" / "mod", "/modules", True)]
            mounts += [(directory / name, f"/work/{name}", False) for name in ("bin", "build-cache", "gopath", "gotmp")]
            go = "/usr/local/go/bin/go"
            env = admission_geth.go_environment()
            native.command(compiler, [go, "mod", "verify"], mounts, cap=300, memory="4g", cwd="/work/source",
                           environment=env)
            record["vet"] = reference(native.command(compiler, [go, "vet", *VET_PACKAGES], mounts, cap=1800,
                                                     memory="6g", cwd="/work/source", environment=env))
            native.command(compiler, [go, "build", "-p", "2", "-trimpath", "-ldflags", f"-X {STAMP}={NATIVE_SOURCE}",
                                      "-o", "/work/bin/faults", "./cmd/extractor-faults-geth"], mounts,
                           cap=1800, memory="6g", cwd="/work/source", environment=env)
            record["binary"] = reference(directory / "bin/faults", BINARY_LIMIT, deadline)
            record["genesis"] = copy_artifact(GENESIS, directory / "bin/genesis.json", reference(GENESIS),
                                              deadline=deadline)
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
    preparation_ref, preparation = pinned_json(preparation_path, expected_sha, 16 * 1024 * 1024, deadline)
    require(preparation.get("kind") == "faults-geth-preparation" and preparation.get("result") == "PASS" and
            preparation.get("cleanup_complete") is True, "A passing Geth faults preparation is required")
    _, accepted = json_artifact(preparation["accepted_preparation"], 16 * 1024 * 1024, deadline=deadline)
    manifest_root = Path(accepted["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (preparation_path.parent, manifest_root, ROOT))
    state = capture_probe.source_state()
    with single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "faults-geth-run", "result": "FAIL", "run_id": uuid.uuid4().hex,
                  "source": state, "preparation": preparation_ref, "public_profile_credit": False, "phases": {}}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            _, manifest = json_artifact(accepted["build_manifest"], 64 * 1024 * 1024, deadline=deadline)
            runtime = manifest["image_inputs"]["debian-runtime-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
            binary = artifact(preparation["binary"], BINARY_LIMIT, deadline=deadline)
            artifact(preparation["genesis"], 1024 * 1024, deadline=deadline)
            probe_dir = output / "probe"
            probe_dir.mkdir()
            record["phases"]["execution_started"] = time.time()
            begun = time.monotonic()
            native.command(runtime, ["/probe/faults", "run", "/probe/genesis.json", "/out/work", "/out/report.json"],
                           [(binary.parent, "/probe", True), (probe_dir, "/out", False)], cap=900, memory="3g")
            record["execution_seconds"] = time.monotonic() - begun
            record["phases"]["execution_finished"] = time.time()
            record["report"] = reference(probe_dir / "report.json", REPORT_LIMIT, deadline)
            probe = load(probe_dir / "report.json", REPORT_LIMIT, deadline)
            source = {"native_build": preparation["binary"]["sha256"], "native_commit": NATIVE_SOURCE,
                      "tool": "test/native_faults/eth/faults_geth.py", "preparation": preparation_ref["sha256"]}
            verdict, corpus, report = judge(probe, source=source)
            (probe_dir / "faults-report.json").write_text(json.dumps(report, indent=1, sort_keys=True) + "\n")
            record["faults_report"] = reference(probe_dir / "faults-report.json", REPORT_LIMIT, deadline)
            record["verdict"] = verdict
            record["corpus_differences"] = corpus
            record["result"] = verdict["result"]
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "run-record.json", record, native, started)
    return output, record


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    first = commands.add_parser("prepare")
    first.add_argument("--accepted-preparation", type=Path, required=True)
    first.add_argument("--sha256", required=True)
    first.add_argument("--output", type=Path, required=True)
    second = commands.add_parser("run")
    second.add_argument("preparation", type=Path)
    second.add_argument("--sha256", required=True)
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
            path, record = prepare(args.accepted_preparation, args.sha256, args.output)
        else:
            path, record = run(args.preparation, args.sha256, args.output)
    except (Interrupted, Exception) as error:
        print(json.dumps({"valid": False, "error": f"{type(error).__name__}: {error}"}))
        return 2
    print(json.dumps({"result": record["result"], "output": str(path), "error": record.get("error")}))
    return 0 if record["result"] in ("PASS", "INCOMPLETE") else 1


if __name__ == "__main__":
    raise SystemExit(main())
