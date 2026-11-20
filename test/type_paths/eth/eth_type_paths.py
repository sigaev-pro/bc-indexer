#!/usr/bin/env python3
"""Prepare and execute the private ETH type-path probe with retained evidence."""

import argparse
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
ROOT = ETH.parents[2]
HARNESS = ROOT / "test/harness"
sys.path[:0] = [str(HARNESS), str(ROOT / "test/native_corpus/eth"), str(HARNESS / "fixtures/admission_geth")]
from artifacts import artifact, check_deadline, copy_artifact, json_artifact, load, pinned_json, read_bytes, reference, require
import admission_geth
import capture_probe
import corpus_eth
from capture_probe import Interrupted, block_stops, single_writer, unblock_stops, write_until_done
from native import Native, new_output

NATIVE = "1df30b18d9ed35ee9d28ffd4648442c2204554ad"
PROBE = "cmd/extractor-type-paths-eth/main.go"
FILES = ("probe.go", "eth_type_paths.py", "eth_type_judge.py", "ORACLE.md", "README.md")
PREPARATION_SECONDS = 2400
RUN_SECONDS = 900
LIMIT = 512 * 1024 * 1024


def prepare(accepted_path, sha256, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    accepted_ref, accepted = pinned_json(accepted_path, sha256, 16 * 1024 * 1024, deadline)
    require(accepted.get("kind") == "admission-geth-preparation" and accepted.get("result") == "PASS" and
            accepted.get("cleanup_complete") is True and accepted.get("decision_native"),
            "A passing Geth decision-bridge preparation is required")
    require(accepted.get("source_commit") == NATIVE, "Wrong native source")
    manifest_root = Path(accepted["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (accepted_path.parent, manifest_root, ROOT))
    source_state = capture_probe.source_state()
    require(source_state.get("clean") is True, "Preparation requires a clean committed source")
    with single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "native-type-paths-eth-preparation", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": source_state, "accepted_preparation": accepted_ref,
                  "public_profile_credit": False}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            record["code"] = capture_probe.copy_sources(FILES, ETH, output / "eth-code", deadline)
            record["preparation_helper"] = copy_artifact(Path(corpus_eth.__file__), output / "corpus_eth.py",
                                                       reference(Path(corpus_eth.__file__)), deadline=deadline)
            copies, rows = corpus_eth.verified_copies(accepted, deadline)
            record["repository_equal"] = corpus_eth.repository_equal(rows)
            require(corpus_eth.code_equal(record["repository_equal"]), "Accepted adapter files differ from repository")
            _, manifest = json_artifact(accepted["build_manifest"], 64 * 1024 * 1024, deadline=deadline)
            compiler = accepted["compiler_image"]
            require(manifest["image_inputs"]["golang-build-image"]["reference"] == compiler and
                    re.fullmatch(r"docker.io/library/golang@sha256:[0-9a-f]{64}", compiler), "Unpinned compiler")
            record["compiler_image"], record["source_commit"] = compiler, NATIVE
            directory = output / "geth"
            directory.mkdir()
            archive = artifact(accepted["source_archive"], LIMIT, deadline=deadline)
            record["source_archive"] = copy_artifact(archive, directory / "source.tar", accepted["source_archive"], LIMIT, deadline)
            source = directory / "source"
            source.mkdir()
            data = read_bytes(directory / "source.tar", accepted["source_archive"]["size_bytes"], deadline)
            with tarfile.open(fileobj=io.BytesIO(data)) as archive:
                for member in archive:
                    check_deadline(deadline)
                    archive.extract(member, source, filter="data")
            del data
            record["install"] = corpus_eth.install(source, copies)
            differences = corpus_eth.install_differences(accepted["install"], record["install"])
            require(not differences, f"Accepted install differs: {differences}")
            record["install_equals_accepted"] = True
            probe = source / PROBE
            probe.parent.mkdir(parents=True)
            record["probe_source"] = copy_artifact(ETH / "probe.go", probe, reference(ETH / "probe.go"), deadline=deadline)
            for name in ("bin", "build-cache", "gopath", "gotmp"):
                (directory / name).mkdir()
            mounts = [(directory, "/work", True), (manifest_root / "cache/geth/mod", "/modules", True)]
            mounts += [(directory / name, f"/work/{name}", False) for name in ("bin", "build-cache", "gopath", "gotmp")]
            go = "/usr/local/go/bin/go"
            environment = admission_geth.go_environment()
            native.command(compiler, [go, "mod", "verify"], mounts, cap=300, memory="4g", cwd="/work/source", environment=environment)
            native.command(compiler, [go, "vet", "./cmd/extractor-type-paths-eth"], mounts, cap=1500, memory="6g",
                           cwd="/work/source", environment=environment)
            native.command(compiler, [go, "build", "-p", "2", "-trimpath", "-o", "/work/bin/typepaths",
                                      "./cmd/extractor-type-paths-eth"], mounts, cap=1800, memory="6g",
                           cwd="/work/source", environment=environment)
            record["binary"] = reference(directory / "bin/typepaths", LIMIT, deadline)
            record["result"] = "PASS"
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "preparation.json", record, native, started)
    return record


def run(preparation_path, sha256, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    prep_ref, prep = pinned_json(preparation_path, sha256, 16 * 1024 * 1024, deadline)
    require(prep.get("kind") == "native-type-paths-eth-preparation" and prep.get("result") == "PASS" and
            prep.get("cleanup_complete") is True, "A passing type-path preparation is required")
    _, accepted = json_artifact(prep["accepted_preparation"], 16 * 1024 * 1024, deadline=deadline)
    _, manifest = json_artifact(accepted["build_manifest"], 64 * 1024 * 1024, deadline=deadline)
    capture_probe.refuse_inside(output, (preparation_path.parent, Path(accepted["build_manifest"]["path"]).parent, ROOT))
    source_state = capture_probe.source_state()
    require(source_state.get("clean") is True, "Run requires clean committed source")
    with single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "native-type-paths-eth-run", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": source_state, "preparation": prep_ref,
                  "public_profile_credit": False, "ac_verdict": None}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            binary = artifact(prep["binary"], LIMIT, deadline=deadline)
            retained = {Path(row["copy"]["path"]).name: artifact(row["copy"], 4 * 1024 * 1024, deadline=deadline)
                        for row in prep["code"]}
            require(set(retained) == set(FILES), "Incomplete preparation code set")
            require(all((ETH / name).read_bytes() == path.read_bytes() for name, path in retained.items()),
                    "Prepared sources differ from repository")
            runtime = manifest["image_inputs"]["debian-runtime-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime")
            probe = output / "probe"
            probe.mkdir()
            begun = time.monotonic()
            native.command(runtime, ["/bin/typepaths", "run", "/out/work", "/out/observation.json"],
                           [(binary.parent, "/bin", True), (probe, "/out", False)], cap=600, memory="2g")
            record["execution_seconds"] = time.monotonic() - begun
            record["observation"] = reference(probe / "observation.json", 16 * 1024 * 1024, deadline)
            compared = subprocess.run([sys.executable, "-I", str(retained["eth_type_judge.py"]), str(probe / "observation.json")],
                                      capture_output=True, text=True, timeout=max(1, deadline - time.monotonic() - 40))
            record["judge_command"] = compared.args
            record["judge_exit"], record["judge_stdout"], record["judge_stderr"] = compared.returncode, compared.stdout, compared.stderr
            record["judgement"] = json.loads(compared.stdout)
            require(compared.returncode == 0 and record["judgement"]["result"] == "PASS", "Native type-path judge failed")
            record["result"] = "PASS"
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "run-record.json", record, native, started)
    return record


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    prepare_parser = sub.add_parser("prepare")
    prepare_parser.add_argument("--accepted-preparation", type=Path, required=True)
    run_parser = sub.add_parser("run")
    run_parser.add_argument("preparation", type=Path)
    for command in (prepare_parser, run_parser):
        command.add_argument("--sha256", required=True)
        command.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        record = (prepare(args.accepted_preparation, args.sha256, args.output) if args.command == "prepare" else
                  run(args.preparation, args.sha256, args.output))
    except (Interrupted, Exception) as error:
        print(json.dumps({"result": "FAIL", "error": f"{type(error).__name__}: {error}"}))
        return 2
    print(json.dumps({"result": record["result"], "output": str(args.output), "error": record.get("error")}))
    return int(record["result"] != "PASS")


if __name__ == "__main__":
    raise SystemExit(main())
