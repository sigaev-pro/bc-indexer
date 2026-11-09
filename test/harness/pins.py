#!/usr/bin/env python3
"""Prepare and run the E01-02.pins native state-provider tests in owned containers.

`prepare` compiles the shared and family Go sources of
`adapters/<family>/state_provider` inside each accepted native module with the
accepted offline compiler, and writes a tool manifest that the caller must pin.
`run` executes the compiled native tests against the accepted private fixture
of each family under one 1,800-second budget and checks their report.
Private local evidence gives no public-profile credit.
"""

import argparse
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

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE))
from artifacts import (artifact, check_deadline, copy_artifact, load, pinned_json, read_bytes, reference, require,
                       source_snapshot, sources_unchanged)
from native import Native, new_output
import profile_manifest
from runner import LABEL, single_writer

FAMILIES = {"geth": "ETH", "bsc": "BSC", "bor": "POLY"}
SHARED = ("provider.go", "provider_test.go")
SOURCES = SHARED + ("family.go", "family_test.go")
OVERLAY = {"bor": ("internal/cli/server/zz_extractor_effective.go",)}
NATIVE_BINARY = {"geth": "geth", "bsc": "geth", "bor": None}
PACKAGE = "./extractor/state_provider/"
TEST_PATTERN = "^Test(Unit|Native)"
COMMON_TESTS = (
    "TestUnitBindIsDurableIdempotentAndExclusive", "TestUnitPinCommitsDirtyRootBeforeRecording",
    "TestUnitPinFailureLeavesNoRecord", "TestUnitReleaseAndDeletionGuards", "TestUnitMalformedLedgerFailsClosed",
    "TestUnitMisfiledLedgerRecordFailsClosed", "TestUnitLedgerKeysAreNotNativeStateKeys", "TestUnitStoreChecks",
    "TestUnitCheckProfileFields", "TestNativeVolatileTrieLost", "TestNativePinnedStatePruning",
    "TestNativeRewindDeletesPinnedHeader", "TestNativeStorePreflight", "TestNativeEffectiveProfile",
    "TestUnitLedgerPinOfAnotherStoreRejects", "TestUnitGuardedPrunerPaths", "TestUnitFreezeLimit")
FAMILY_TESTS = {"geth": (), "bsc": ("TestNativeLegacyPruneOffset",), "bor": ("TestNativeLegacyPruneOffset",)}
# Each family must report these profile cases; the Go test fixes accept or reject.
PROFILE_CASES = {
    "geth": {"baseline_flags", "allowed_native_args", "allowed_environment", "allowed_config",
             "wrong_hash_profile_default", "wrong_hash_profile_flag", "wrong_hash_profile_environment",
             "wrong_hash_profile_config", "environment_over_config_cannot_bypass", "flag_over_config_cannot_bypass",
             "wrong_gc_profile_archive_flag", "wrong_gc_profile_archive_environment",
             "wrong_gc_profile_archive_config"},
    "bor": {"baseline_config", "allowed_config", "allowed_native_args", "environment_is_not_a_config_source",
            "wrong_hash_profile_default", "wrong_hash_profile_config", "wrong_hash_profile_flag",
            "wrong_gc_profile_archive_flag", "wrong_gc_profile_archive_config", "wrong_syncmode_flag",
            "bor_pipelined_import", "bor_parallel_stateless", "bor_witness_sync", "bor_preconfirmation_relay",
            "bor_preconfirmation_rpc", "bor_blockstm_enable", "bor_blockstm_enforce", "bor_blockstm_config",
            "bor_sequencer"},
}
PROFILE_CASES["bsc"] = PROFILE_CASES["geth"] | {
    "forbidden_pruning_ancient_flag", "forbidden_pruning_block_history_flag",
    "forbidden_pruning_block_history_environment", "forbidden_pruning_block_history_config"}
ACCEPTED_PROFILE_CASES = {"baseline_flags", "baseline_config", "allowed_native_args", "allowed_environment",
                          "allowed_config", "environment_is_not_a_config_source"}
PREFLIGHT_REJECTIONS = {
    "foreign_identity": "native store identity mismatch", "unbound_start_on_bound_store": "native store identity mismatch",
    "bound_expectation_on_unbound_store": "native store identity mismatch",
    "foreign_genesis": "native store identity mismatch", "wrong_chain": "native store identity mismatch",
    "path_initialized_store": "unsupported state backend", "forbidden_pruning_leftover": "forbidden pruning profile",
    "wrong_datadir_empty": "native store identity mismatch"}
STOP_SIGNALS = (signal.SIGTERM, signal.SIGINT, signal.SIGHUP)
PREPARATION_SECONDS = 3600
RUN_SECONDS = 1800
TEST_CAP = 900
REPORT_LIMIT = 4 * 1024 * 1024


def save(path, value):
    with path.open("x") as stream:
        stream.write(json.dumps(value, sort_keys=True, indent=2) + "\n")
        stream.flush()
        os.fsync(stream.fileno())


def save_final(path, value, lock):
    """Write the final record, then release the lock even if the write fails."""
    try:
        save(path, value)
    finally:
        lock.close()


def source_commit():
    """Return HEAD of the checkout that holds this driver; the tracked adapter
    and harness files must equal it, so evidence names the exact executed source."""
    def git(*args):
        result = subprocess.run(["git", "-C", str(ROOT), *args], capture_output=True, text=True, timeout=60)
        require(result.returncode == 0, "git failed: " + " ".join(args))
        return result.stdout
    head = git("rev-parse", "HEAD").strip()
    require(not git("status", "--porcelain", "--untracked-files=all", "--", "adapters", "test/harness"),
            "Adapters or harness differ from the committed source")
    return head


def admit_docker():
    """Take the host-wide single-writer lock and require Docker to hold no harness container."""
    lock = single_writer()
    try:
        require(not foreign_harness_containers(), "Docker holds harness containers of another run")
    except BaseException:
        lock.close()
        raise
    return lock


def canonical_digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def adapter_files(family):
    """Return (source path, path inside the native tree) for every adapter file."""
    base = ROOT / "adapters" / family / "state_provider"
    rows = [(base / name, "extractor/state_provider/" + name) for name in SOURCES]
    rows += [(base / "native_overlay" / name, name) for name in OVERLAY.get(family, ())]
    return rows


def check_shared_sources():
    """The shared files must be byte-identical in all three family packages."""
    for name in SHARED:
        digests = {family: hashlib.sha256((ROOT / "adapters" / family / "state_provider" / name).read_bytes()).hexdigest()
                   for family in FAMILIES}
        require(len(set(digests.values())) == 1, f"Shared source {name} differs between families: {digests}")


def install_adapter(family, source, destination_root, deadline):
    """Copy adapter files into an extracted native tree; an overlay never replaces a native file."""
    rows = []
    for origin, relative in adapter_files(family):
        target = source / relative
        require(not target.exists() and not target.is_symlink(), f"Adapter path already exists in the native tree: {relative}")
        target.parent.mkdir(parents=True, exist_ok=True)
        expected = reference(origin, 1024 * 1024, deadline)
        retained = destination_root / relative
        retained.parent.mkdir(parents=True, exist_ok=True)
        copy_artifact(origin, retained, expected, 1024 * 1024, deadline)
        copy_artifact(origin, target, expected, 1024 * 1024, deadline)
        rows.append({"relative": relative, "source": expected, "retained": reference(retained, 1024 * 1024, deadline)})
    return rows


def environment(build_cache="/work/build-cache"):
    return {"CGO_ENABLED": "1", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64", "GOCACHE": build_cache,
            "GOMODCACHE": "/modules", "GOPATH": "/work/gopath", "GOTMPDIR": "/work/build-temp",
            "TMPDIR": "/work/build-temp", "GOFLAGS": "-mod=readonly -modfile=/work/metadata/go.mod",
            "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOWORK": "off"}


def prepare_family(family, manifest, manifest_path, output, runner, deadline):
    closure = manifest["module_closures"][family]
    root = output / family
    root.mkdir()
    archive = artifact(closure["source_archive"], 256 * 1024 * 1024, manifest_path.parent, deadline)
    copied = copy_artifact(archive, root / "source.tar", closure["source_archive"], 256 * 1024 * 1024, deadline)
    source = root / "source"
    source.mkdir()
    data = read_bytes(root / "source.tar", closure["source_archive"]["size_bytes"], deadline)
    with tarfile.open(fileobj=io.BytesIO(data)) as stream:
        for member in stream:
            check_deadline(deadline)
            stream.extract(member, source, filter="data")
    del data
    for row in closure["artifacts"]:
        for field in ("info", "mod", "zip"):
            artifact(row[field], 1024 * 1024 * 1024, manifest_path.parent, deadline)
    adapters = install_adapter(family, source, root / "adapter", deadline)
    for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
        (root / directory).mkdir()
    for name in ("go.mod", "go.sum"):
        copy_artifact(source / name, root / "metadata" / name, reference(source / name, deadline=deadline), deadline=deadline)
    compiler = manifest["builds"][family]["compiler_image"]
    require(compiler == manifest["image_inputs"]["golang-build-image"]["reference"], "Unexpected compiler image")
    mounts = [(source, "/source", True), (manifest_path.parent / "cache" / family / "mod", "/modules", True),
              (root, "/work", True)]
    mounts += [(root / name, "/work/" + name, False) for name in ("metadata", "generated", "build-cache", "build-temp", "gopath")]
    first = len(runner.commands)
    runner.command(compiler, ["/usr/local/go/bin/go", "vet", PACKAGE], mounts, cap=1500, memory="6g", cwd="/source",
                   environment=environment())
    runner.command(compiler, ["/usr/local/go/bin/go", "test", "-c", "-trimpath", "-o", "/work/generated/pins.test", PACKAGE],
                   mounts, cap=1500, memory="6g", cwd="/source", environment=environment())
    binary = manifest["builds"][family]["binary"] if NATIVE_BINARY[family] else None
    return {"source_commit": closure["source_commit"], "source_archive": copied,
            "module_closure_sha256": canonical_digest(closure), "compiler": compiler, "adapter": adapters,
            "test_binary": reference(root / "generated/pins.test", deadline=deadline),
            "native_binary": binary, "commands": runner.commands[first:],
            "metadata": [reference(root / "metadata" / name, deadline=deadline) for name in ("go.mod", "go.sum")]}


FAILURES = (OSError, ValueError, KeyError, TypeError, InterruptedError, subprocess.SubprocessError)


def owned_attempt(output, started, deadline, proof, record_name, body):
    """Create a new output under the single-writer lock, run body, and always write the record.

    A refusal before the output exists leaves no record. After that, every
    failure or stop signal still records FAIL with the commands and cleanup.
    """
    lock = admit_docker()
    try:
        output = new_output(output)
    except BaseException:
        lock.close()
        raise
    runner, unexpected = None, None
    try:
        runner = Native(output, deadline)
        proof["native_owner_nonce"] = runner.nonce
        body(output, runner)
        require(source_commit() == proof["source_commit"], "Source changed during the attempt")
        proof["result"] = "PASS"
    except Exception as error:
        proof["result"] = "FAIL"
        proof["error"] = f"{type(error).__name__}: {str(error)[:400]}"
        if not isinstance(error, FAILURES):
            unexpected = error
    finally:
        # Stop signals stay blocked from here until the record is written and
        # the lock is released; a pending signal is delivered afterward.
        blocked = signal.pthread_sigmask(signal.SIG_BLOCK, STOP_SIGNALS)
        try:
            proof.update(commands=runner.commands if runner else [],
                         elapsed_seconds=round(time.monotonic() - started, 3),
                         cleanup_complete=runner.cleanup_complete if runner else True)
            if proof["cleanup_complete"] is not True:
                proof["result"] = "FAIL"
            save_final(output / record_name, proof, lock)
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, blocked)
    if unexpected is not None:
        raise unexpected
    return proof


def prepare(manifest_path, expected_sha, output, previous=None, previous_sha=None, correction=None):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    require(manifest_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    manifest_ref = reference(manifest_path, 64 * 1024 * 1024, deadline)
    require(manifest_ref["sha256"] == expected_sha, "BuildManifest digest mismatch")
    manifest = load(manifest_path, 64 * 1024 * 1024, deadline)
    check_shared_sources()
    commit = source_commit()
    previous_ref, attempt = None, 1
    if previous is not None:
        previous_ref, old = pinned_json(previous, previous_sha, deadline=deadline)
        require(old.get("kind") == "pins-preparation" and old.get("result") == "FAIL" and
                old.get("build_manifest") == manifest_ref and correction, "Invalid preparation retry")
        attempt = old["attempt_index"] + 1
    else:
        require(correction is None, "Correction needs an earlier failed preparation")
    require(not output.is_relative_to(manifest_path.parent), "Preparation cannot write into accepted build storage")
    proof = {"schema_version": 1, "kind": "pins-preparation", "result": "FAIL", "build_manifest": manifest_ref,
             "source_commit": commit,
             "run_id": uuid.uuid4().hex, "native_owner_nonce": None, "attempt_index": attempt,
             "previous_attempt": previous_ref, "correction": correction, "deadline_seconds": PREPARATION_SECONDS,
             "runtime": "NOT_RUN", "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, ("pins.py", "artifacts.py", "native.py", "owned.py"),
                                                            deadline)
        tool = {"schema_version": 1, "kind": "pins-native-tool", "build_manifest": manifest_ref, "families": {}}
        for family in FAMILIES:
            tool["families"][family] = prepare_family(family, manifest, manifest_path, output, runner, deadline)
        sources_unchanged(origins, deadline)
        for family in FAMILIES:
            for row in tool["families"][family]["adapter"]:
                require(reference(Path(row["source"]["path"]), 1024 * 1024, deadline) == row["source"],
                        "Adapter source changed during preparation")
        save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)

    return owned_attempt(output, started, deadline, proof, "preparation.json", body)


def parse_go_test(text, expected):
    """Every expected test passes exactly once, nothing fails or skips, and the binary reports PASS."""
    passed, failed = [], []
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("--- PASS: "):
            passed.append(stripped.split()[2])
        elif stripped.startswith(("--- FAIL: ", "--- SKIP: ")):
            failed.append(stripped)
    lines = [line.strip() for line in text.splitlines() if line.strip()]
    require(not failed, f"Go tests failed or skipped: {failed[:5]}")
    require(sorted(passed) == sorted(expected), f"Go tests passed {sorted(passed)}, expected {sorted(expected)}")
    require(lines and lines[-1] == "PASS", "Go test binary did not report PASS")
    return sorted(passed)


def read_report(path, deadline):
    rows = []
    for line in read_bytes(path, REPORT_LIMIT, deadline).decode().splitlines():
        row = json.loads(line)
        require(type(row) is dict and set(row) == {"family", "test", "name", "value"}, "Malformed report row")
        rows.append(row)
    return rows


def strings(value):
    """Every string inside a JSON value, so a reason can be found in nested detail."""
    if isinstance(value, str):
        return [value]
    if isinstance(value, dict):
        return [item for child in value.values() for item in strings(child)]
    if isinstance(value, list):
        return [item for child in value for item in strings(child)]
    return []


def evaluate_report(family, rows, blocks):
    """Independently check the retained observations against the accepted fixture block roots."""
    require(all(row["family"] == family for row in rows), "Report rows name another family")
    named = {}
    for row in rows:
        key = (row["test"], row["name"])
        require(key not in named, f"Duplicate report row {key}")
        named[key] = row["value"]
    root1, root2 = blocks[0]["state_root"], blocks[1]["state_root"]
    pinned = named[("TestNativeVolatileTrieLost", "pinned")]
    control = named[("TestNativeVolatileTrieLost", "control")]
    require(pinned["child"]["block1_root_on_disk_before"] is False and pinned["child"]["block1_root_on_disk_after"] is True
            and pinned["block1_root_on_disk"] is True and pinned["walk_error"] == "<nil>" and pinned["walk"]["accounts"] > 0
            and pinned["preflight_pins"] == ["obs-volatile"] and pinned["recovered_root"] == root1
            and pinned["recovered_head"] == (2 if root2 == root1 else 1), "Pinned volatile-trie observation failed")
    require(control["child"]["block1_root_on_disk_after"] is False and control["block1_root_on_disk"] is False
            and control["walk_error"] != "<nil>" and control["recovered_head"] == 0 and control["preflight_pins"] == [],
            "Unpinned control did not lose the volatile state")
    pruning = named[("TestNativePinnedStatePruning", "pruning")]
    require(pruning["block1_root"] == root1 and pruning["head_root"] == root2 and
            pruning["refused"].startswith("live pins forbid this native deletion") and
            pruning["pinned_side_walk"]["codes"] > 0 and pruning["pinned_side_walk"]["slots"] > 0 and
            pruning["unguarded_side_deleted"] is True and
            pruning["unguarded_preflight"].startswith("pinned native data is unavailable") and
            pruning["released_side_deleted"] is True and pruning["head_walk"]["accounts"] > 0,
            "Pruning observation failed")
    if family in ("bsc", "bor"):
        require(named[("TestNativeLegacyPruneOffset", "legacy_offset")].startswith("forbidden pruning profile"),
                "Legacy prune offset was not rejected")
    rewind = named[("TestNativeRewindDeletesPinnedHeader", "rewind")]
    require(rewind["refused"].startswith("live pins forbid this native deletion: rewind to 0") and
            rewind["unguarded_verify"].startswith("pinned native data is unavailable"), "Rewind observation failed")
    preflight = named[("TestNativeStorePreflight", "preflight")]
    require(preflight["unbound_fresh_store"] == "<nil>" and preflight["bound_store"] == "<nil>", "Compatible homes rejected")
    for case, prefix in PREFLIGHT_REJECTIONS.items():
        require(preflight[case].startswith(prefix), f"Preflight case {case} returned {preflight[case]}")
    require(preflight["native_hash_override_on_path_store"].startswith("incompatible state scheme"),
            "Native parser converted a path store")
    profile = named[("TestNativeEffectiveProfile", "profile")]
    require(set(profile["cases"]) == PROFILE_CASES[family], "Profile cases differ from the declared set")
    for case, result in profile["results"].items():
        accepted = case in ACCEPTED_PROFILE_CASES
        require(result["accepted"] is accepted and (result["reason"] == "") is accepted,
                f"Profile case {case} has the wrong outcome")
        require(accepted or result["reason"] in " ".join(strings(result["detail"])),
                f"Profile case {case} was rejected for another reason")
    return {"recovered_head": pinned["recovered_head"], "pinned_walk": pinned["walk"], "pruning_refused": pruning["refused"],
            "native_default_scheme_for_empty_store": preflight["native_default_scheme_for_empty_store"],
            "profile_cases": len(profile["cases"])}


def foreign_harness_containers():
    result = subprocess.run(["docker", "ps", "-a", "-q", "--filter", "label=" + LABEL], capture_output=True, text=True,
                            timeout=30)
    require(result.returncode == 0, "Docker cannot list harness containers")
    return result.stdout.split()


def run_family(family, entry, fixture, output, runner, deadline):
    root = output / family
    (root / "inputs").mkdir(parents=True)
    (root / "work").mkdir()
    (root / "temp").mkdir()
    inputs = {name: copy_artifact(Path(ref["path"]), root / "inputs" / name, ref, 64 * 1024 * 1024, deadline)
              for name, ref in fixture["inputs"].items()}
    binary = artifact(entry["test_binary"], 1024 * 1024 * 1024, deadline=deadline)
    mounts = [(binary.parent, "/tool", True), (root / "inputs", "/inputs", True), (root / "work", "/work", False),
              (root / "temp", "/scratch", False)]
    env = {"EXTRACTOR_PINS_INPUTS": "/inputs", "EXTRACTOR_PINS_WORK": "/work",
           "EXTRACTOR_PINS_REPORT": "/work/report.jsonl", "TMPDIR": "/scratch", "HOME": "/scratch"}
    if entry["native_binary"] is not None:
        native = artifact(entry["native_binary"], 1024 * 1024 * 1024, fixture["build_root"], deadline)
        mounts.append((native.parent, "/native", True))
        env["EXTRACTOR_NATIVE_BINARY"] = "/native/" + native.name
    started = time.monotonic()
    stdout = runner.command(entry["compiler"], ["/tool/pins.test", "-test.v", "-test.count=1", "-test.run", TEST_PATTERN,
                                                "-test.timeout", f"{TEST_CAP - 60}s"],
                            mounts, cap=TEST_CAP, memory="2g", cwd="/scratch", environment=env)
    elapsed = round(time.monotonic() - started, 3)
    passed = parse_go_test(read_bytes(stdout, 8 * 1024 * 1024, deadline).decode(), COMMON_TESTS + FAMILY_TESTS[family])
    report = root / "work/report.jsonl"
    summary = evaluate_report(family, read_report(report, deadline), fixture["blocks"])
    return {"inputs": inputs, "passed": passed, "elapsed_seconds": elapsed, "report": reference(report, deadline=deadline),
            "stdout": reference(stdout, deadline=deadline), "summary": summary}


def load_fixture(path, sha, deadline):
    """Verify one pinned test-only ProfileManifest and return its family inputs and prepared blocks."""
    manifest = profile_manifest.load_pinned(path, sha, deadline)
    digest = profile_manifest.verify(manifest, deadline)
    family = {value: key for key, value in FAMILIES.items()}[manifest["family"]]
    inputs = {Path(ref["path"]).name: ref for ref in manifest["inputs"]}
    require({"genesis.json", "blocks.rlp"} <= set(inputs), "ProfileManifest lacks genesis or blocks")
    request = load(Path(manifest["runner"]["request"]["path"]), 1024 * 1024, deadline)
    fixture_path = Path(request["fixture"]["path"])
    require(reference(fixture_path, 1024 * 1024, deadline) == request["fixture"], "Fixture changed")
    fixture = load(fixture_path, 1024 * 1024, deadline)
    blocks = sorted(fixture["prepared_blocks"], key=lambda row: row["number"])
    require([row["number"] for row in blocks] == [1, 2], "Unexpected prepared blocks")
    return family, {"manifest": reference(path, deadline=deadline), "manifest_digest": digest, "profile": manifest["profile"],
                    "build_manifest": manifest["build_manifest"], "build_root": Path(manifest["build_manifest"]["path"]).parent,
                    "inputs": {name: inputs[name] for name in ("genesis.json", "blocks.rlp")}, "blocks": blocks}


def run(tool_path, tool_sha, manifests, output, previous=None, previous_sha=None, correction=None):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(tool_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "pins-native-tool" and set(tool["families"]) == set(FAMILIES), "Not a pins tool")
    fixtures = {}
    for path, sha in manifests:
        family, fixture = load_fixture(path, sha, deadline)
        require(family not in fixtures, f"Two ProfileManifests for {family}")
        require(fixture["build_manifest"] == tool["build_manifest"], "Fixture and tool use different builds")
        fixtures[family] = fixture
    require(set(fixtures) == set(FAMILIES), "One ProfileManifest per family is required")
    commit = source_commit()
    previous_ref, attempt = None, 1
    if previous is not None:
        previous_ref, old = pinned_json(previous, previous_sha, deadline=deadline)
        require(old.get("kind") == "pins-run" and old.get("result") == "FAIL" and correction and
                old.get("tool") == tool_ref, "Invalid run retry")
        attempt = old["attempt_index"] + 1
    else:
        require(correction is None, "Correction needs an earlier failed run")
    proof = {"schema_version": 1, "kind": "pins-run", "result": "FAIL", "tool": tool_ref, "source_commit": commit,
             "run_id": uuid.uuid4().hex, "native_owner_nonce": None, "attempt_index": attempt,
             "previous_attempt": previous_ref, "correction": correction, "deadline_seconds": RUN_SECONDS,
             "public_profile_credit": False, "families": {}, "error": None,
             "fixtures": {family: {key: value for key, value in fixture.items() if key != "build_root"}
                          for family, fixture in fixtures.items()}}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, ("pins.py", "artifacts.py", "native.py", "owned.py"),
                                                            deadline)
        for family in FAMILIES:
            proof["families"][family] = run_family(family, tool["families"][family], fixtures[family], output, runner,
                                                   deadline)
        sources_unchanged(origins, deadline)
        for family in FAMILIES:
            artifact(tool["families"][family]["test_binary"], 1024 * 1024 * 1024, deadline=deadline)

    return owned_attempt(output, started, deadline, proof, "run.json", body)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prep = commands.add_parser("prepare")
    prep.add_argument("--build-manifest", type=Path, required=True)
    prep.add_argument("--sha256", required=True)
    prep.add_argument("--output", type=Path, required=True)
    prep.add_argument("--previous-attempt", type=Path)
    prep.add_argument("--previous-sha256")
    prep.add_argument("--correction")
    measured = commands.add_parser("run")
    measured.add_argument("tool", type=Path)
    measured.add_argument("--sha256", required=True)
    measured.add_argument("--profile-manifest", nargs=2, action="append", metavar=("PATH", "SHA256"), required=True)
    measured.add_argument("--output", type=Path, required=True)
    measured.add_argument("--previous-run", type=Path)
    measured.add_argument("--previous-sha256")
    measured.add_argument("--correction")
    args = parser.parse_args()

    def interrupt(number, _frame):
        raise InterruptedError("Interrupted by " + signal.Signals(number).name)
    for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(number, interrupt)
    try:
        if args.command == "prepare":
            proof = prepare(args.build_manifest, args.sha256, args.output, args.previous_attempt, args.previous_sha256,
                            args.correction)
        else:
            proof = run(args.tool, args.sha256, [(Path(path), sha) for path, sha in args.profile_manifest], args.output,
                        args.previous_run, args.previous_sha256, args.correction)
    except FAILURES as error:
        # Refused before this command created its output: no record exists.
        print(json.dumps({"result": "REFUSED", "error": f"{type(error).__name__}: {str(error)[:400]}"}))
        return 2
    print(json.dumps({"result": proof["result"], "error": proof["error"]}))
    return 0 if proof["result"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
