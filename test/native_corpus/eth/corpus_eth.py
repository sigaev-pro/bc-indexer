#!/usr/bin/env python3
"""Build and run the ETH (Geth) native corpus probe of E01-03.smoke_corpus.

`prepare` starts from the accepted Geth decision-bridge preparation of
E01-02.decision_bridge (pinned by the SHA-256 of its preparation.json). It
extracts the same Geth source archive, applies the same two product patch
sets and installs the same extractor packages from the copies that
preparation retains, proves that every installed file equals the accepted
install, adds the corpus probe (`probe.go`) as cmd/extractor-corpus-eth, and
compiles it offline with the accepted compiler. `run` executes the probe in
the pinned runtime image, adds the `source` of the observation, checks the
probe's native self-checks and compares the observation with
`expected/eth.json` using `corpus_compare.py`. Preparation is not part of the
measured run. Private local evidence gives no AC verdict and no
public-profile credit; the public cells stay NOT_RUN.
"""

import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import time
import uuid

ETH = Path(__file__).resolve().parent
CORPUS = ETH.parent
ROOT = CORPUS.parents[1]
HARNESS = ROOT / "test" / "harness"
GETH_FIXTURE = HARNESS / "fixtures" / "admission_geth"
sys.path.insert(0, str(HARNESS))
sys.path.insert(0, str(GETH_FIXTURE))
from artifacts import (artifact, check_deadline, copy_artifact, json_artifact, load, pinned_json,  # noqa: E402
                       read_bytes, reference, require)
import admission_geth  # noqa: E402
import capture_probe  # noqa: E402
from capture_probe import Interrupted, block_stops, single_writer, unblock_stops, write_until_done  # noqa: E402
from native import Native, new_output  # noqa: E402

PROBE_PATH = "cmd/extractor-corpus-eth/main.go"
ETH_FILES = ("probe.go", "corpus_eth.py", "README.md")
CORPUS_FILES = ("corpus_compare.py", "ORACLE.md", "README.md")
NATIVE_SOURCE = "1df30b18d9ed35ee9d28ffd4648442c2204554ad"
PREPARATION_SECONDS = 2400
RUN_SECONDS = 900
BINARY_LIMIT = 512 * 1024 * 1024
OBSERVATION_LIMIT = 16 * 1024 * 1024
TOOL = ("cmd/extractor-corpus-eth (test/native_corpus/eth/probe.go): Geth core.GenerateChainWithGenesis on the "
        "private fixture; core.BlockChain.InsertChain on a store attached with AttachDecisions, Options{} and "
        "DecisionOptions{} (the shipped node configuration), then a restart and the same import; blocks and receipts "
        "from rawdb; the native callTracer per transaction; state before and after each transaction by a native "
        "re-execution of the block on its parent state (core.ApplyTransactionWithEVM, then the ethash Finalize), whose "
        "receipts and root must equal the imported ones; state_after_block at the block root")


# ---------------------------------------------------------------- native checks


def native_failures(observation, block_count):
    """The probe's native self-checks; each failure is a reason the run cannot be judged PASS."""
    failures = []
    native = observation.get("native")
    if not isinstance(native, dict):
        return ["observation has no native section"]
    for name in ("live_import", "restart_import"):
        row = native.get(name) or {}
        if row.get("attach_error") is not None:
            failures.append(f"{name}: AttachDecisions failed: {row['attach_error']}")
        if row.get("insert_error") is not None:
            failures.append(f"{name}: InsertChain failed: {row['insert_error']}")
        if (row.get("head") or {}).get("number") != block_count:
            failures.append(f"{name}: head is not block {block_count}")
    live = native.get("live_import") or {}
    restart = native.get("restart_import") or {}
    if live.get("admission_records_before") or live.get("decision_records_before"):
        failures.append("live_import: the store held extractor records before the first import")
    for kind in ("admission", "decision"):
        # One observation and one decision per corpus block, read from the counter records.
        if counted(live.get(f"{kind}_records_after")) != block_count:
            failures.append(f"live_import: the {kind} counters do not count {block_count} sequences")
        # The restart reopened the store that the live import left.
        if restart.get(f"{kind}_records_before") != live.get(f"{kind}_records_after"):
            failures.append(f"restart_import: the {kind} records at the restart differ from the live import")
    if restart.get("decision_records_after") != restart.get("decision_records_before"):
        failures.append("restart_import: the re-import changed the decision records")
    if native.get("undecided_intents") != []:
        failures.append("live_import: an intent has no NATIVE_DECIDED record")
    sizes = {}
    for block in observation.get("blocks") or []:
        if isinstance(block, dict):
            sizes[block.get("number")] = block.get("body_tx_count")
    checks = native.get("reexecution")
    if not isinstance(checks, list) or [row.get("number") for row in checks] != list(range(1, block_count + 1)):
        failures.append("reexecution: not one check per block")
        checks = []
    for row in checks:
        if row.get("root_equal") is not True:
            failures.append(f"reexecution: block {row.get('number')} root differs from the imported root")
        if row.get("cumulative_gas_used") != row.get("header_gas_used"):
            failures.append(f"reexecution: block {row.get('number')} gas differs from the header")
        receipts = row.get("receipts") or []
        if not isinstance(sizes.get(row.get("number")), int) or \
                [receipt.get("index") for receipt in receipts] != list(range(sizes[row.get("number")])):
            failures.append(f"reexecution: block {row.get('number')} does not check one receipt per transaction")
        for receipt in receipts:
            if receipt.get("receipt_equal") is not True:
                failures.append(f"reexecution: block {row.get('number')} receipt {receipt.get('index')} differs")
    return failures


def counted(records):
    """The sum of the "next" values of the counter records in a record set (key to JSON text)."""
    total = 0
    for key, value in (records if isinstance(records, dict) else {}).items():
        if key.endswith("/counter"):
            total += json.loads(value)["next"]
    return total


def verdict(report, returncode, failures):
    """PASS only when the comparison is PASS, the comparator exited 0 and no native self-check failed."""
    return "PASS" if report.get("result") == "PASS" and returncode == 0 and not failures else "FAIL"


def with_source(observation, source):
    """The observation with its `source`; the probe output never carries one."""
    require("source" not in observation, "The probe must not write the source")
    out = dict(observation)
    out["source"] = source
    return out


# ---------------------------------------------------------------- preparation


def verified_copies(accepted, deadline):
    """The retained copies of the accepted preparation, each checked against its record; map name to directory."""
    rows = list(accepted["native"]) + list(accepted["decision_native"])
    for package_rows in accepted["shared"].values():
        rows += list(package_rows)
    for row in rows:
        artifact(row["copy"], 4 * 1024 * 1024, deadline=deadline)
    first = Path(accepted["native"][0]["copy"]["path"]).parent.parent
    copies = {"native": first / "native", "decision_native": first / "decision-native"}
    for package in accepted["shared"]:
        copies[package] = first / "shared" / package.split("/")[-1]
    return copies, rows


def repository_equal(rows):
    """For each retained copy, whether the repository file of the same origin name is byte-equal now."""
    out = {}
    for row in rows:
        origin = Path(row["origin"]["path"])
        parts = origin.parts
        # The origin was a frozen worktree; its path from "adapters" on names the repository file.
        relative = Path(*parts[len(parts) - 1 - parts[::-1].index("adapters"):])
        current = ROOT / relative
        out[relative.as_posix()] = current.is_file() and \
            hashlib.sha256(current.read_bytes()).hexdigest() == row["copy"]["sha256"]
    return out


def code_equal(equal):
    """Whether every retained code file equals the repository; README text may differ."""
    return all(same for name, same in equal.items() if not name.endswith(".md"))


def install(source, copies):
    """Apply both patch sets and install the packages exactly as the accepted preparation did."""
    record = admission_geth.apply_patches(source, copies["native"])
    record["decision"] = admission_geth.apply_patches(source, copies["decision_native"])
    rows = []
    for package, (_, names) in admission_geth.PACKAGES.items():
        directory = copies["native"] if package.endswith("/native") else copies[package]
        (source / package).mkdir(parents=True, exist_ok=True)
        for name in names:
            rows.append({"relative": f"{package}/{name}",
                         "copy": copy_artifact(directory / name, source / package / name,
                                               reference(directory / name, 4 * 1024 * 1024))})
    for name in admission_geth.DECISION_GO:
        relative = f"extractor/admission/native/{name}"
        rows.append({"relative": relative, "copy": copy_artifact(copies["decision_native"] / name, source / relative,
                                                                 reference(copies["decision_native"] / name,
                                                                           4 * 1024 * 1024))})
    record["packages"] = rows
    return record


def install_differences(accepted, installed):
    """Every difference between this install and the accepted install record (patches, files, packages)."""
    def view(record):
        patches = [(row["name"], row["file"], row["sha256_before"], row["sha256_after"])
                   for row in record["patches"] + record["decision"]["patches"]]
        files = sorted((row["relative"], row["copy"]["sha256"])
                       for row in record["files"] + record["decision"]["files"])
        packages = sorted((row["relative"], row["copy"]["sha256"]) for row in record["packages"]
                          if not row["relative"].startswith("cmd/"))
        return {"patches": patches, "files": files, "packages": packages}
    want, got = view(accepted), view(installed)
    return [key for key in ("patches", "files", "packages") if want[key] != got[key]]


def prepare(accepted_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    accepted_ref, accepted = pinned_json(accepted_path, expected_sha, 16 * 1024 * 1024, deadline)
    require(accepted.get("kind") == "admission-geth-preparation" and accepted.get("result") == "PASS" and
            accepted.get("cleanup_complete") is True, "A passing Geth decision-bridge preparation is required")
    require(accepted.get("source_commit") == NATIVE_SOURCE, "The accepted preparation names another Geth commit")
    require(accepted.get("decision_native"), "The accepted preparation has no decision-bridge patch set")
    manifest_root = Path(accepted["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (accepted_path.parent, manifest_root, ROOT))
    state = capture_probe.source_state()
    with single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "native-corpus-eth-preparation", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": state, "accepted_preparation": accepted_ref,
                  "public_profile_credit": False}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            record["eth_code"] = capture_probe.copy_sources(ETH_FILES, ETH, output / "eth-code", deadline)
            record["corpus_code"] = capture_probe.copy_sources(CORPUS_FILES, CORPUS, output / "corpus-code", deadline)
            record["expected"] = capture_probe.copy_sources(("eth.json",), CORPUS / "expected", output / "expected",
                                                            deadline)
            record["fixture"] = capture_probe.copy_sources(("genesis.json",), HARNESS / "fixtures", output / "fixture",
                                                           deadline)
            _, expected = json_artifact(record["expected"][0]["copy"], deadline=deadline)
            require(expected["fixture"]["native_source"] == f"go-ethereum {NATIVE_SOURCE}",
                    "The oracle names another Geth source")
            copies, rows = verified_copies(accepted, deadline)
            record["repository_equal"] = repository_equal(rows)
            require(code_equal(record["repository_equal"]),
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
            record["install"] = install(source, copies)
            differences = install_differences(accepted["install"], record["install"])
            require(not differences, f"The install differs from the accepted install: {differences}")
            record["install_equals_accepted"] = True
            probe = source / PROBE_PATH
            probe.parent.mkdir(parents=True)
            record["probe_source"] = copy_artifact(ETH / "probe.go", probe, reference(ETH / "probe.go"))
            for name in ("bin", "build-cache", "gopath", "gotmp"):
                (directory / name).mkdir()
            mounts = [(directory, "/work", True), (manifest_root / "cache" / "geth" / "mod", "/modules", True)]
            mounts += [(directory / name, f"/work/{name}", False) for name in ("bin", "build-cache", "gopath", "gotmp")]
            go = "/usr/local/go/bin/go"
            env = admission_geth.go_environment()
            native.command(compiler, [go, "mod", "verify"], mounts, cap=300, memory="4g", cwd="/work/source",
                           environment=env)
            record["vet"] = reference(native.command(compiler, [go, "vet", "./cmd/extractor-corpus-eth"], mounts,
                                                     cap=1500, memory="6g", cwd="/work/source", environment=env))
            native.command(compiler, [go, "build", "-p", "2", "-trimpath", "-o", "/work/bin/corpus",
                                      "./cmd/extractor-corpus-eth"], mounts, cap=1800, memory="6g",
                           cwd="/work/source", environment=env)
            record["binary"] = reference(directory / "bin/corpus", BINARY_LIMIT, deadline)
            record["genesis"] = copy_artifact(HARNESS / "fixtures" / "genesis.json", directory / "bin/genesis.json",
                                              reference(HARNESS / "fixtures" / "genesis.json"), deadline=deadline)
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
    require(preparation.get("kind") == "native-corpus-eth-preparation" and preparation.get("result") == "PASS" and
            preparation.get("cleanup_complete") is True, "A passing ETH corpus preparation is required")
    _, accepted = json_artifact(preparation["accepted_preparation"], 16 * 1024 * 1024, deadline=deadline)
    manifest_root = Path(accepted["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (preparation_path.parent, manifest_root, ROOT))
    state = capture_probe.source_state()
    with single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "native-corpus-eth-run", "result": "FAIL", "run_id": uuid.uuid4().hex,
                  "source": state, "preparation": preparation_ref, "public_profile_credit": False,
                  "ac_verdict": None, "phases": {}}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            _, manifest = json_artifact(accepted["build_manifest"], 64 * 1024 * 1024, deadline=deadline)
            runtime = manifest["image_inputs"]["debian-runtime-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
            binary = artifact(preparation["binary"], BINARY_LIMIT, deadline=deadline)
            artifact(preparation["genesis"], 1024 * 1024, deadline=deadline)
            expected_path = artifact(preparation["expected"][0]["copy"], 4 * 1024 * 1024, deadline=deadline)
            compare_path = [artifact(row["copy"], 1024 * 1024, deadline=deadline)
                            for row in preparation["corpus_code"] if row["copy"]["path"].endswith("/corpus_compare.py")]
            require(len(compare_path) == 1, "The preparation must retain the comparator")
            record["expected_equals_repository"] = \
                (CORPUS / "expected" / "eth.json").read_bytes() == expected_path.read_bytes()
            require(record["expected_equals_repository"], "The retained oracle differs from expected/eth.json")
            probe_dir = output / "probe"
            probe_dir.mkdir()
            record["phases"]["execution_started"] = time.time()
            begun = time.monotonic()
            native.command(runtime, ["/probe/corpus", "run", "/probe/genesis.json", "/out/work", "/out/probe.json",
                                     preparation["source_commit"]],
                           [(binary.parent, "/probe", True), (probe_dir, "/out", False)], cap=600, memory="2g")
            record["execution_seconds"] = time.monotonic() - begun
            record["phases"]["execution_finished"] = time.time()
            record["probe_output"] = reference(probe_dir / "probe.json", OBSERVATION_LIMIT, deadline)
            probe = load(probe_dir / "probe.json", OBSERVATION_LIMIT, deadline)
            _, expected = json_artifact(preparation["expected"][0]["copy"], deadline=deadline)
            record["native_failures"] = native_failures(probe, len(expected["blocks"]))
            source = {"native": f"go-ethereum {preparation['source_commit']}",
                      "accepted_build": {"preparation": preparation["accepted_preparation"],
                                         "patch_sets": [row["copy"] for row in accepted["native"] + accepted["decision_native"]
                                                        if row["copy"]["path"].endswith("/patches.json")]},
                      "corpus_preparation": preparation_ref, "probe_binary": preparation["binary"],
                      "probe_source": preparation["probe_source"], "tool": TOOL,
                      "run_id": record["run_id"]}
            observation = with_source(probe, source)
            observation_path = output / "observation.json"
            observation_path.write_text(json.dumps(observation, indent=1, sort_keys=True) + "\n")
            record["observation"] = reference(observation_path, OBSERVATION_LIMIT, deadline)
            report_path = output / "comparison.json"
            compared = subprocess.run([sys.executable, "-I", str(compare_path[0]), str(expected_path),
                                       str(observation_path), "--report", str(report_path)],
                                      capture_output=True, text=True,
                                      timeout=max(1, deadline - time.monotonic() - 40))
            check_deadline(deadline)
            record["comparison"] = reference(report_path, OBSERVATION_LIMIT, deadline)
            report = load(report_path, OBSERVATION_LIMIT, deadline)
            record["comparison_exit"] = compared.returncode
            record["cases"] = {row["case"]: row["verdict"] for row in report["cases"]}
            record["differences"] = len(report["differences"])
            record["comparison_result"] = report["result"]
            record["result"] = verdict(report, compared.returncode, record["native_failures"])
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "run-record.json", record, native, started)
    return output, record


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    first = commands.add_parser("prepare")
    first.add_argument("--accepted-preparation", type=Path, required=True)
    first.add_argument("--sha256", required=True)
    first.add_argument("--output", type=Path, required=True)
    second = commands.add_parser("run")
    second.add_argument("preparation", type=Path)
    second.add_argument("--sha256", required=True)
    second.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "prepare":
            path, record = prepare(args.accepted_preparation, args.sha256, args.output)
        else:
            path, record = run(args.preparation, args.sha256, args.output)
    except (Interrupted, Exception) as error:
        print(json.dumps({"valid": False, "error": f"{type(error).__name__}: {error}"}))
        return 2
    print(json.dumps({"result": record["result"], "output": str(path), "error": record.get("error")}))
    return 0 if record["result"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
