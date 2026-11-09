#!/usr/bin/env python3
"""Build and run the E01-02.owned_capture native probe for Geth, BSC and Bor.

`prepare` builds one probe binary per family inside that family's accepted
source tree, offline, with a test-only VM safe point. `run` executes the
prepared binaries in the pinned runtime image and compares their observations
with the hand-written oracle. Preparation is not part of the measured run.
"""

import argparse
import contextlib
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tarfile
import time
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parent))
from artifacts import (Rejected, artifact, check_deadline, copy_artifact, json_artifact, load, read_bytes,
                       reference, require)
from native import Native, new_output

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = Path(__file__).with_name("fixtures") / "capture"
FAMILIES = ("geth", "bsc", "bor")
LOCK = Path("/tmp/extractor-harness-runner.lock")
ANCHOR = "\t\t// Do tracing before potential memory expansion\n"
SAFE_POINT_CALL = ("\t\tif extractorSafePoint != nil {\n"
                   "\t\t\textractorSafePoint(op, callContext)\n"
                   "\t\t}\n")
PROBE_FILES = ("probe.go", "insert.go", "insert_bor.go")
CAPTURE_FILES = ("go.mod", "capture.go", "capture_test.go")
FIXTURE_FILES = ("genesis.json", "expected.json", "ORACLE.md", "safepoint.go.txt", *PROBE_FILES)
PREPARATION_SECONDS = 1800
RUN_SECONDS = 900
STOP_SIGNALS = (signal.SIGTERM, signal.SIGINT, signal.SIGHUP)


class Interrupted(BaseException):
    """A stop signal arrived; the record is still written."""


def stop(signum, frame):
    raise Interrupted(f"signal {signum}")


@contextlib.contextmanager
def single_writer():
    """Hold the host-wide harness lock and refuse to start beside foreign harness containers."""
    descriptor = os.open(LOCK, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    handle = os.fdopen(descriptor, "r+")
    try:
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Rejected("Another local runner holds the single-writer lock") from None
        listed = subprocess.run(["docker", "ps", "-aq", "--filter", "label=extractor.harness"],
                                capture_output=True, text=True, timeout=60)
        require(listed.returncode == 0, "Docker cannot list harness containers")
        require(not listed.stdout.strip(), "Foreign harness containers exist; refusing to start")
        handlers = {number: signal.signal(number, stop) for number in STOP_SIGNALS}
        try:
            yield
        finally:
            for number, handler in handlers.items():
                signal.signal(number, handler)
    finally:
        handle.close()


def source_state():
    head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
    dirty = subprocess.run(["git", "status", "--porcelain=v1", "--untracked-files=no"], cwd=ROOT,
                           capture_output=True, text=True, check=True).stdout
    return {"commit": head, "clean": not dirty}


def go_directive(go_mod):
    for line in go_mod.splitlines():
        if line.startswith("go "):
            return line.split()[1]
    raise Rejected("Client go.mod has no go directive")


def patch_interpreter(source):
    """Insert the test-only safe point once, before the opcode hook block."""
    path = source / "core/vm/interpreter.go"
    before = path.read_text()
    require(before.count(ANCHOR) == 1, "Interpreter anchor must occur exactly once")
    require("extractorSafePoint" not in before, "Interpreter already has a safe point")
    after = before.replace(ANCHOR, SAFE_POINT_CALL + ANCHOR)
    path.write_text(after)
    hook = source / "core/vm/extractor_capture_probe.go"
    hook.write_bytes((FIXTURE / "safepoint.go.txt").read_bytes())
    return {"file": "core/vm/interpreter.go", "sha256_before": hashlib.sha256(before.encode()).hexdigest(),
            "sha256_after": hashlib.sha256(after.encode()).hexdigest(),
            "inserted": SAFE_POINT_CALL, "hook_file": reference(hook)}


def copy_sources(names, origin, target, deadline):
    target.mkdir()
    rows = []
    for name in names:
        source_ref = reference(origin / name, 4 * 1024 * 1024, deadline)
        rows.append({"origin": source_ref, "copy": copy_artifact(origin / name, target / name, source_ref,
                                                                 4 * 1024 * 1024, deadline)})
    return rows


def prepare(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    require(manifest_path.is_absolute(), "BuildManifest path must be absolute")
    manifest_ref = reference(manifest_path, 64 * 1024 * 1024, deadline)
    require(manifest_ref["sha256"] == expected_sha, "BuildManifest digest mismatch")
    _, manifest = json_artifact(manifest_ref, deadline=deadline)
    require(manifest.get("evidence_kind") == "actual-build" and manifest.get("task_id") == "E01-01.builds",
            "An accepted actual build is required")
    root = manifest_path.parent
    require(not output.is_relative_to(root) and not output.is_relative_to(ROOT / "core"),
            "Preparation cannot write inside accepted inputs")
    with single_writer():
        output = new_output(output)
        native = Native(output, deadline)
        record = {"schema_version": 1, "kind": "owned-capture-probe-preparation", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": source_state(), "build_manifest": manifest_ref,
                  "native_owner_nonce": native.nonce, "families": {}}
        try:
            record["fixture"] = copy_sources(FIXTURE_FILES, FIXTURE, output / "fixture", deadline)
            record["harness_code"] = copy_sources(("capture_probe.py", "native.py", "owned.py", "artifacts.py"),
                                                  Path(__file__).parent, output / "harness-code", deadline)
            compiler = manifest["image_inputs"]["golang-build-image"]["reference"]
            require(compiler.startswith("docker.io/library/golang@sha256:"), "Unpinned compiler")
            record["compiler_image"] = compiler
            for family in FAMILIES:
                record["families"][family] = prepare_family(family, manifest, root, output, native, compiler, deadline)
            unit = output / "unit"
            unit.mkdir()
            for directory in ("build-cache", "gopath"):
                (unit / directory).mkdir()
            # Unit tests with the race detector use the accepted compiler and only the module itself.
            stdout = native.command(compiler, ["/usr/local/go/bin/go", "test", "-race", "-count=1", "-cover", "-v", "."],
                                    [(output / "geth/capture", "/capture", True), (unit / "build-cache", "/cache", False),
                                     (unit / "gopath", "/gopath", False)], cap=300, memory="2g", cwd="/capture",
                                    environment={"CGO_ENABLED": "1", "GOCACHE": "/cache", "GOPATH": "/gopath",
                                                 "GOFLAGS": "-mod=readonly", "GOPROXY": "off", "GOSUMDB": "off",
                                                 "GOTOOLCHAIN": "local", "GOWORK": "off", "GOENV": "off"})
            record["unit_tests"] = reference(stdout)
            require(b"\nok  \tgithub.com/sigaev-pro/bc-indexer/core/capture" in b"\n" + stdout.read_bytes(),
                    "Unit tests did not pass")
            record["result"] = "PASS"
        except Interrupted as error:
            record["error"] = str(error)
        except (Rejected, OSError, KeyError, ValueError, TypeError, subprocess.SubprocessError) as error:
            record["error"] = str(error)
        finally:
            record.update(commands=native.commands, cleanup_complete=native.cleanup_complete,
                          elapsed_seconds=time.monotonic() - started)
            (output / "preparation.json").write_text(json.dumps(record, sort_keys=True, indent=2) + "\n")
    return output, record


def prepare_family(family, manifest, root, output, native, compiler, deadline):
    closure = manifest["module_closures"][family]
    require(closure["source_commit"] == manifest["builds"][family]["source_commit"], "Source closure mismatch")
    require(manifest["builds"][family]["compiler_image"] == compiler, "Family compiler mismatch")
    directory = output / family
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
    data = read_bytes(directory / "source.tar", closure["source_archive"]["size_bytes"], deadline)
    with tarfile.open(fileobj=io.BytesIO(data)) as stream:
        for member in stream:
            check_deadline(deadline)
            stream.extract(member, source, filter="data")
    del data
    row["patch"] = patch_interpreter(source)
    probe = source / "cmd/extractor-capture-probe"
    probe.mkdir()
    row["probe_sources"] = copy_sources(PROBE_FILES, FIXTURE, probe, deadline)
    row["capture_sources"] = copy_sources(CAPTURE_FILES, ROOT / "core/capture", directory / "capture", deadline)
    version = go_directive((source / "go.mod").read_text())
    (directory / "go.work").write_text(f"go {version}\n\nuse (\n\t./source\n\t./capture\n)\n")
    row["go_work"] = reference(directory / "go.work")
    for name in ("bin", "build-cache", "gopath"):
        (directory / name).mkdir()
    mounts = [(directory, "/work", True), (root / "cache" / family / "mod", "/modules", True),
              (directory / "bin", "/work/bin", False), (directory / "build-cache", "/work/build-cache", False),
              (directory / "gopath", "/work/gopath", False)]
    environment = {"CGO_ENABLED": "0", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64",
                   "GOCACHE": "/work/build-cache", "GOMODCACHE": "/modules", "GOPATH": "/work/gopath",
                   "GOFLAGS": "-mod=readonly", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
                   "GOWORK": "off"}
    native.command(compiler, ["/usr/local/go/bin/go", "mod", "verify"], mounts, cap=300, memory="4g",
                   cwd="/work/source", environment=environment)
    build = ["/usr/local/go/bin/go", "build", "-p", "2", "-trimpath"]
    if family == "bor":
        build += ["-tags", "extractor_bor"]
    build += ["-o", "/work/bin/probe", "./cmd/extractor-capture-probe"]
    native.command(compiler, build, mounts, cap=900, memory="4g", cwd="/work/source",
                   environment={**environment, "GOWORK": "/work/go.work"})
    copy_artifact(FIXTURE / "genesis.json", directory / "bin/genesis.json", reference(FIXTURE / "genesis.json"),
                  deadline=deadline)
    row["binary"] = reference(directory / "bin/probe", deadline=deadline)
    row["genesis"] = reference(directory / "bin/genesis.json", deadline=deadline)
    return row


def expected_field(field, tag, length, sha, chunks):
    return field == {"tag": tag, "length": length, "sha256": sha, "chunks": chunks}


def compare(observation, oracle, family):
    """Return the list of differences between one family's observations and the oracle."""
    failures = []
    if observation.get("family") != family:
        failures.append("family mismatch")
    generated = observation.get("generated_head_hash")
    if not generated or observation.get("baseline_head_hash") != generated:
        failures.append("baseline import head differs from the generated head")
    rows = {row.get("name"): row for row in observation.get("scenarios", [])}
    if set(rows) != set(oracle["scenarios"]) or len(rows) != len(observation.get("scenarios", [])):
        return failures + ["scenario set differs from the oracle"]
    for name, want in oracle["scenarios"].items():
        row = rows[name]

        def fail(message):
            failures.append(f"{name}: {message}")

        if row.get("errors"):
            fail("probe errors: " + "; ".join(row["errors"]))
        if row.get("head_number") != oracle["head_number"] or row.get("head_hash") != generated:
            fail("imported head differs from the generated head")
        if row.get("chunk_bytes") != want["chunk_bytes"] or ("buffer_bytes" in want and
                                                            row.get("buffer_bytes") != want["buffer_bytes"]):
            fail("capture bounds differ from the oracle")
        if not isinstance(row.get("peak_held"), int) or not 0 < row["peak_held"] <= row.get("buffer_bytes", 0):
            fail("peak held bytes exceed the budget")
        if row.get("notes") != want["notes"]:
            fail("callback note count differs")
        if "waits" in want and row.get("waits") != want["waits"]:
            fail("unexpected safe-point waits")
        if "min_waits" in want and not row.get("waits", 0) >= want["min_waits"]:
            fail("the producer never waited")
        if row.get("log_sha256") != oracle["log_sha256"]:
            fail("receipt logs differ from the callee output")
        storage = {key.lower(): value for key, value in (row.get("storage_slot0") or {}).items()}
        if storage != oracle["storage_slot0"]:
            fail("caller storage differs from the callee output")
        fields = row.get("fields") or []
        if "call_chunks" in want:
            calls = oracle["call_inputs"]
            if len(fields) != len(calls) or not all(
                    expected_field(field, call["tag"], call["length"], call["sha256"], chunks)
                    for field, call, chunks in zip(fields, calls, want["call_chunks"])):
                fail("captured CALL inputs differ from the input bytes")
            if row.get("callback_wait_rejected") is not True:
                fail("a wait inside the opcode callback was not rejected")
        else:
            if len(fields) != want["block_fields"] or not all(
                    field.get("tag") == 3 and field.get("length", 0) > 0 and
                    field.get("chunks") == -(-field["length"] // want["chunk_bytes"]) for field in fields):
                fail("block fields differ from the oracle")
        pause = row.get("pause")
        if want["pause"] is None:
            if pause is not None:
                fail("unexpected pause record")
            continue
        if not isinstance(pause, dict) or pause.get("observed") is not True or pause.get("kind") != want["pause"]["kind"] or \
                pause.get("head_number") != want["pause"]["head_number"]:
            fail("the producer did not pause at the expected safe point")
        elif pause.get("frames_after_hold") != pause.get("frames_at_pause") or \
                pause.get("import_returned_during_hold") is not False or \
                pause.get("callback_open_during_pause") is not False or \
                not pause.get("hold_milliseconds", 0) >= oracle["min_hold_milliseconds"]:
            fail("the paused producer moved, held a callback open or released too early")
    return failures


def run(preparation_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(preparation_path.is_absolute(), "Preparation path must be absolute")
    preparation_ref = reference(preparation_path, 16 * 1024 * 1024, deadline)
    require(preparation_ref["sha256"] == expected_sha, "Preparation digest mismatch")
    preparation = load(preparation_path, deadline=deadline)
    require(preparation.get("kind") == "owned-capture-probe-preparation" and preparation.get("result") == "PASS" and
            preparation.get("cleanup_complete") is True, "A passing preparation is required")
    require(not output.is_relative_to(preparation_path.parent), "Run output cannot be inside the preparation")
    with single_writer():
        output = new_output(output)
        native = Native(output, deadline)
        record = {"schema_version": 1, "kind": "owned-capture-probe-run", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": source_state(), "preparation": preparation_ref,
                  "native_owner_nonce": native.nonce, "families": {}, "phases": {"admission_started": time.time()}}
        try:
            oracle_ref = next(row["copy"] for row in preparation["fixture"] if row["copy"]["path"].endswith("/expected.json"))
            oracle_path = artifact(oracle_ref, 1024 * 1024, deadline=deadline)
            oracle = load(oracle_path, deadline=deadline)
            record["oracle"] = oracle_ref
            runtime = load(Path(preparation["build_manifest"]["path"]), deadline=deadline)["image_inputs"]["debian-runtime-image"]["reference"]
            require(runtime.startswith("docker.io/library/debian@sha256:"), "Unpinned runtime image")
            binaries = {}
            for family in FAMILIES:
                row = preparation["families"][family]
                binaries[family] = (artifact(row["binary"], 512 * 1024 * 1024, deadline=deadline),
                                    artifact(row["genesis"], 1024 * 1024, deadline=deadline))
            record["phases"]["execution_started"] = time.time()
            for family in FAMILIES:
                directory = output / family
                directory.mkdir()
                binary, _ = binaries[family]
                family_started = time.monotonic()
                native.command(runtime, ["/probe/probe", family, "/probe/genesis.json", "/out/probe.json"],
                               [(binary.parent, "/probe", True), (directory, "/out", False)], cap=300, memory="1g")
                observation_ref = reference(directory / "probe.json", 16 * 1024 * 1024, deadline)
                failures = compare(load(directory / "probe.json", deadline=deadline), oracle, family)
                record["families"][family] = {"observation": observation_ref, "failures": failures,
                                              "elapsed_seconds": time.monotonic() - family_started,
                                              "result": "PASS" if not failures else "FAIL"}
            record["phases"]["execution_finished"] = time.time()
            if all(row["result"] == "PASS" for row in record["families"].values()) and \
                    len(record["families"]) == len(FAMILIES):
                record["result"] = "PASS"
        except Interrupted as error:
            record["error"] = str(error)
        except (Rejected, OSError, KeyError, ValueError, TypeError, StopIteration, subprocess.SubprocessError) as error:
            record["error"] = str(error)
        finally:
            if not native.cleanup_complete:
                record["result"] = "FAIL"
            record.update(commands=native.commands, cleanup_complete=native.cleanup_complete,
                          elapsed_seconds=time.monotonic() - started)
            (output / "run-record.json").write_text(json.dumps(record, sort_keys=True, indent=2) + "\n")
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
    except (Rejected, OSError, KeyError, ValueError, TypeError) as error:
        print(json.dumps({"valid": False, "error": str(error)}))
        return 2
    print(json.dumps({"result": record["result"], "output": str(path), "error": record.get("error")}))
    return 0 if record["result"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
