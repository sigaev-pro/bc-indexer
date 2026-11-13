#!/usr/bin/env python3
"""Build and run the Geth probe of E01-02.lifecycle_seams.

`prepare` starts from the accepted Geth decision-bridge preparation of
E01-02.decision_bridge (pinned by the SHA-256 of its preparation.json). It
installs the accepted tree exactly as that preparation did (the admission and
decision-bridge patch sets and the extractor packages, from its retained
copies) and proves it equal to the accepted install record, then applies the
lifecycle patch set of adapters/geth/lifecycle/native, installs the shared
lifecycle package and the Geth lifecycle wiring, adds the probe as
cmd/extractor-lifecycle-geth, and, offline with the accepted compiler and
module cache, runs go mod verify, go vet, the Go tests of the extractor
packages and builds the probe with the native-commit stamp. `run` executes
the probe in the pinned runtime image and judges its report (`judge`): the
native self-checks and the qa matrix of the private local ETH slot.
Preparation is not part of the measured run. Public cells stay NOT_RUN.
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
HARNESS = FIXTURE.parents[1]
ROOT = HARNESS.parents[1]
NATIVE = ROOT / "adapters" / "geth" / "lifecycle" / "native"
SHARED = ROOT / "adapters" / "geth" / "lifecycle"
sys.path.insert(0, str(HARNESS))
sys.path.insert(0, str(HARNESS / "fixtures" / "admission_geth"))
sys.path.insert(0, str(ROOT / "test" / "native_corpus" / "eth"))
from artifacts import (artifact, check_deadline, copy_artifact, json_artifact, load, pinned_json,  # noqa: E402
                       read_bytes, reference, require)
import admission_geth  # noqa: E402
import capture_probe  # noqa: E402
from capture_probe import Interrupted, block_stops, single_writer, unblock_stops, write_until_done  # noqa: E402
import corpus_eth  # noqa: E402
from native import Native, new_output  # noqa: E402

PROBE_PATH = "cmd/extractor-lifecycle-geth/main.go"
FIXTURE_FILES = ("probe.go", "genesis.json", "lifecycle_geth.py", "README.md")
NATIVE_FILES = ("patches.json", "core_lifecycle.go.txt", "lifecycle.go", "lifecycle_test.go", "README.md")
SHARED_FILES = ("lifecycle.go", "finality.go", "callers.json", "lifecycle_test.go", "vectors.json")
NATIVE_SOURCE = "1df30b18d9ed35ee9d28ffd4648442c2204554ad"
STAMP = "github.com/ethereum/go-ethereum/extractor/admission/native.BinaryCommit"
VET_PACKAGES = ("./extractor/lifecycle", "./extractor/admission/native", "./core", "./eth", "./eth/catalyst",
                "./eth/syncer", "./cmd/geth", "./cmd/extractor-lifecycle-geth")
TEST_PACKAGES = ("./extractor/lifecycle", "./extractor/admission/native", "./extractor/decision",
                 "./extractor/admission")
PREPARATION_SECONDS = 3600
RUN_SECONDS = 900
BINARY_LIMIT = 512 * 1024 * 1024
REPORT_LIMIT = 16 * 1024 * 1024

# ---------------------------------------------------------------- expectations

# The decisions each native-caller step must add or settle: (kind, old head,
# new head, member blocks). Fixture names: A1..A6 extend genesis; B3 B4, C3
# C4, S3 S4, T3 T4 and G3 fork from A2; H5 H6 fork from A4.


def F(text):
    """A FINALITY decision: "FIELD caller old>new" ("-" is no reference)."""
    return ("FINALITY", text)


EXPECTED_STEPS = {
    "bootstrap newPayload A1": [], "bootstrap newPayload A2": [], "bootstrap newPayload A3": [],
    "bootstrap newPayload S3": [], "bootstrap newPayload T3": [], "bootstrap forkchoice A3": [],
    "newPayload A4": [("SIDE", "A3", "A3", ["A4"])],
    "forkchoice head A4": [("CANONICAL", "A3", "A4", ["A4"])],
    "forkchoice safe A2 finalized A1": [F("FINALIZED geth.set-finalized ->A1"), F("SAFE geth.set-safe ->A2")],
    "AC-LIF-05-02 forkchoice zero safe and finalized": [],
    "forkchoice unchanged safe A2 finalized A1": [],
    "AC-LIF-05-03 forkchoice finalized A2 then unknown safe": [F("FINALIZED geth.set-finalized A1>A2")],
    "newPayload B3": [("SIDE", "A4", "A4", ["B3"])],
    "newPayload B4": [("SIDE", "A4", "A4", ["B4"])],
    "forkchoice reorg to B4 safe B3": [("CANONICAL", "A4", "B4", ["B3", "B4"]), F("SAFE geth.set-safe A2>B3")],
    "forkchoice back to A4 without corpus": [],
    "newPayload C3": [("SIDE", "B4", "B4", ["C3"])],
    "newPayload C4": [("SIDE", "B4", "B4", ["C4"])],
    "InsertChain known C3 C4": [("CANONICAL", "B4", "C3", ["C3"]), ("CANONICAL", "C3", "C4", ["C4"])],
    "InsertReceiptChain (snap sync)": [],
    "syncer Sync (sync target)": [],
    "NewSimulatedBeacon (developer beacon)": [],
    "Reset": [],
    "forkchoice safe C4 finalized A2": [F("SAFE geth.set-safe B3>C4")],
    # The concurrent callers include SetCanonical of the current head, which
    # rewrites the head batch: ten CANONICAL decisions from C4 to C4.
    "after deadlock smoke": [("CANONICAL", "C4", "C4", [])] * 10,
    "restart": [F("SAFE geth.startup-reconcile C4>A2")],
    "InsertChain T4 on pruned T3 (side chain)": [("CANONICAL", "C4", "T3", ["T3"]), ("IMPORT", "T3", "T4", ["T4"])],
    "InsertBlockWithoutSetHead S4 on pruned S3 (recover ancestors)": [("SIDE", "T4", "T4", ["S3"]),
                                                                        ("SIDE", "T4", "T4", ["S4"])],
    "forkchoice head S4": [("CANONICAL", "T4", "S4", ["S3", "S4"])],
    "forkchoice safe S4 finalized A2": [F("SAFE geth.set-safe A2>S4")],
    "SetHead 3": [("CANONICAL", "S4", "S3", []), F("SAFE geth.set-head S4>-"), F("SAFE geth.set-head ->A2")],
    "newPayload G3 (retained side block)": [("SIDE", "S3", "S3", ["G3"])],
}
# Steps that must fail, with a fragment of the error.
EXPECTED_ERRORS = {
    "AC-LIF-05-03 forkchoice finalized A2 then unknown safe": "Invalid forkchoice state",
    "forkchoice back to A4 without corpus": "no retained EXEC_READY observation",
    "InsertReceiptChain (snap sync)": "refused in the capture profile",
    "syncer Sync (sync target)": "refused in the capture profile",
    "NewSimulatedBeacon (developer beacon)": "refused in the capture profile",
    "Reset": "chain reset is refused",
}
# The native references after a step: head, finalized, safe (fixture names).
EXPECTED_REFS = {
    "bootstrap forkchoice A3": ("A3", "", ""),
    "forkchoice head A4": ("A4", "", ""),
    "forkchoice safe A2 finalized A1": ("A4", "A1", "A2"),
    "AC-LIF-05-02 forkchoice zero safe and finalized": ("A4", "A1", "A2"),
    "forkchoice unchanged safe A2 finalized A1": ("A4", "A1", "A2"),
    "AC-LIF-05-03 forkchoice finalized A2 then unknown safe": ("A4", "A2", "A2"),
    "forkchoice reorg to B4 safe B3": ("B4", "A2", "B3"),
    "forkchoice back to A4 without corpus": ("B4", "A2", "B3"),
    "InsertChain known C3 C4": ("C4", "A2", "B3"),
    "forkchoice safe C4 finalized A2": ("C4", "A2", "C4"),
    "restart": ("C4", "A2", "A2"),
    "InsertChain T4 on pruned T3 (side chain)": ("T4", "A2", "A2"),
    "forkchoice head S4": ("S4", "A2", "A2"),
    "forkchoice safe S4 finalized A2": ("S4", "A2", "S4"),
    "SetHead 3": ("S3", "A2", "A2"),
}
SHARED_STEPS = ("newPayload A4", "forkchoice head A4", "forkchoice safe A2 finalized A1",
                "AC-LIF-05-02 forkchoice zero safe and finalized", "forkchoice unchanged safe A2 finalized A1",
                "AC-LIF-05-03 forkchoice finalized A2 then unknown safe", "newPayload B3", "newPayload B4",
                "forkchoice reorg to B4 safe B3", "forkchoice back to A4 without corpus")
# The crash scenarios: the child stops at its crash point (exit code 3); the
# restart settles the crashed intent from native evidence (the recovery
# trace names it) and leaves the native head shown.
EXPECTED_CRASHES = {
    "finality": ("finality_not_durable", "A3", 0, ""),
    # The rewind ran with finalized A3 and safe A5: the restart completes it
    # and then decides the safe reset to A3 (one more decision).
    "rewind": ("native_head_is_new_head", "A4", 1, "A3"),
    "canonical": ("native_head_is_old_head", "A4", 0, "A3"),
    # A crash between the head batch of H5 and the final head batch of H6:
    # the restart completes the saved transition from its old head A4.
    "reorg": ("completed_inside_reorg", "H6", 0, "A3"),
}
OUTSIDE_REFUSALS = ("geth.manual-commands", "geth.dev-sealing", "geth.sync-target", "geth.snap-sync")
AC_ROWS = ("AC-CAP-01-01", "AC-LIF-05-02", "AC-LIF-05-03", "AC-LIF-06-04", "AC-LIF-06-05")
PUBLIC_SLOTS = ("ethereum-mainnet", "ethereum-sepolia", "ethereum-hoodi")


# ---------------------------------------------------------------- judging


def decision_shape(row):
    """The comparable shape of one decision row of the probe: a FINALITY row
    by its field, caller and references, any other by kind, heads and members."""
    if row.get("kind") == "FINALITY":
        return ("FINALITY", row.get("finality"))
    return (row.get("kind"), row.get("old_hash"), row.get("new_hash"), list(row.get("members") or []))


def step_failures(steps, expected_names, prefix):
    """Every difference between the recorded steps and the expected steps."""
    failures = []
    names = [st.get("name") for st in steps]
    if names != list(expected_names):
        return [f"{prefix}: steps {names} are not the expected steps"]
    for st in steps:
        name = st["name"]
        rows = st.get("decisions") or []
        if any(row.get("state") != "NATIVE_DECIDED" for row in rows):
            failures.append(f"{prefix}: {name}: a decision is not NATIVE_DECIDED")
        if [decision_shape(row) for row in rows] != [_shape(e) for e in EXPECTED_STEPS[name]]:
            failures.append(f"{prefix}: {name}: decisions {[decision_shape(r) for r in rows]} differ")
        fragment = EXPECTED_ERRORS.get(name)
        error = st.get("error")
        if fragment is None and error is not None:
            failures.append(f"{prefix}: {name}: unexpected error {error}")
        if fragment is not None and (not isinstance(error, str) or fragment not in error):
            failures.append(f"{prefix}: {name}: expected an error with {fragment!r}, got {error!r}")
        want = EXPECTED_REFS.get(name)
        refs = st.get("refs") or {}
        if want is not None and (refs.get("head_hash"), refs.get("finalized"), refs.get("safe")) != want:
            failures.append(f"{prefix}: {name}: references {refs.get('head_hash'), refs.get('finalized'), refs.get('safe')}"
                            f" are not {want}")
    return failures


def _shape(expected):
    if expected[0] == "FINALITY":
        return tuple(expected)
    kind, old, new, members = expected
    return (kind, old, new, list(members))


def bootstrap_failures(row, prefix):
    failures = []
    if row.get("phase_at_open") != "BOOTSTRAP":
        failures.append(f"{prefix}: the first start is not BOOTSTRAP")
    if row.get("admission_records_before_live") != 0 or row.get("decision_records_before_live") != 0:
        failures.append(f"{prefix}: the initial native sync left admission or decision records")
    if row.get("live_service_error") is not None or row.get("golive_error") is not None:
        failures.append(f"{prefix}: going live failed")
    if row.get("phase_after") != "LIVE" or row.get("boundary_present") is not True:
        failures.append(f"{prefix}: the gate is not LIVE with a boundary")
    if (row.get("boundary") or {}).get("hash") != "A3" or row.get("boundary_root_is_head_root") is not True or \
            row.get("boundary_root_durable") is not True:
        failures.append(f"{prefix}: the boundary is not A3 with its durable state root")
    return failures


def crash_failures(rows):
    failures = []
    by_name = {row.get("scenario"): row for row in rows if isinstance(row, dict)}
    if sorted(by_name) != sorted(EXPECTED_CRASHES):
        return ["crashes: not one report per scenario"]
    for name, (evidence, head, decided, safe) in EXPECTED_CRASHES.items():
        row = by_name[name]
        if row.get("exit_code") != 3:
            failures.append(f"crashes: {name}: the child did not stop at its crash point")
        if row.get("attach_error") is not None:
            failures.append(f"crashes: {name}: the restart failed: {row['attach_error']}")
        settled = [e for e in row.get("settled") or [] if e.get("event") == "settling"]
        if [e.get("evidence") for e in settled] != [evidence]:
            failures.append(f"crashes: {name}: the restart did not settle one intent as {evidence}")
        if any(d.get("state") == "INTENT" for d in row.get("decisions") or []):
            failures.append(f"crashes: {name}: an intent stays unresolved")
        if (row.get("refs") or {}).get("head_hash") != head:
            failures.append(f"crashes: {name}: the native head is not {head}")
        if sum(e.get("event") == "lifecycle_decided" for e in row.get("settled") or []) != decided:
            failures.append(f"crashes: {name}: the restart did not make {decided} reconciliation decision(s)")
        if (row.get("refs") or {}).get("safe") != safe:
            failures.append(f"crashes: {name}: safe is not {safe!r} after the restart")
    return failures


def native_failures(report):
    """The probe's native self-checks; each failure is a reason the run cannot be judged PASS."""
    failures = []
    if not isinstance(report, dict) or report.get("kind") != "lifecycle-geth-probe":
        return ["the report is not a lifecycle probe report"]
    if report.get("native_identity") != NATIVE_SOURCE:
        failures.append("the binary does not carry the native-commit stamp of the source")
    profile = report.get("profile") or {}
    for key in ("flags_dev", "flags_synctarget", "flags_snap", "eth_new_snap_sync"):
        if not isinstance(profile.get(key), str) or "capture profile refuses" not in profile[key]:
            failures.append(f"profile: {key} is not refused by the capture profile check")
    if profile.get("flags_full") is not None:
        failures.append("profile: full sync is refused")
    if not isinstance(profile.get("eth_new_wrong_commit"), str) or \
            "native commit identity" not in profile["eth_new_wrong_commit"]:
        failures.append("profile: a config with another native commit is not refused")
    native = report.get("native_caller") or {}
    failures += bootstrap_failures(native.get("bootstrap") or {}, "native")
    steps = native.get("steps") or []
    failures += step_failures(steps, list(EXPECTED_STEPS), "native")
    if (native.get("restart_boundary") or {}).get("hash") != "A3":
        failures.append("native: the restart did not keep the boundary H")
    if native.get("state_after_restart") != {"A1": False, "A2": True, "A3": True, "S3": False, "T3": False}:
        failures.append("native: the state of the BOOTSTRAP blocks after the restart is not as the scenario needs")
    deadlock = native.get("deadlock") or {}
    if deadlock.get("completed") is not True or deadlock.get("errors") != []:
        failures.append("native: the concurrent callers did not all return (deadlock check)")
    if native.get("exit_errors_first_open") != [] or native.get("exit_errors_restart") != []:
        failures.append("native: a gate pass closed out of order")
    outside = native.get("outside_gate_refusals") or {}
    for caller in OUTSIDE_REFUSALS:
        if not isinstance(outside.get(caller), str):
            failures.append(f"native: no refusal evidence for {caller}")
    harness = report.get("harness") or {}
    failures += bootstrap_failures(harness.get("bootstrap") or {}, "harness")
    harness_names = list(EXPECTED_STEPS)[:6] + list(SHARED_STEPS)
    failures += step_failures(harness.get("steps") or [], harness_names, "harness")
    if report.get("gaps") != []:
        failures.append(f"coverage: required callers without evidence: {report.get('gaps')}")
    failures += crash_failures(report.get("crashes") or [])
    return failures


def step_named(steps, name):
    rows = [st for st in steps if st.get("name") == name]
    return rows[0] if len(rows) == 1 else {}


def qa_matrix(report, failures):
    """The qa verdict per AC row on the private local ETH slot, and the public cells."""
    native = report.get("native_caller") or {}
    harness = report.get("harness") or {}
    paths = [native.get("steps") or [], harness.get("steps") or []]
    ac = report.get("ac_cap_01_01") or {}
    reached = all(any(row[1] for row in (ac.get(side) or {}).get("decisions") or []) for side in ("native", "harness"))
    cap = "PASS" if not failures and ac.get("native") == ac.get("harness") and reached and report.get("gaps") == [] \
        else "FAIL"

    def zero_noop(steps):
        st = step_named(steps, "AC-LIF-05-02 forkchoice zero safe and finalized")
        again = step_named(steps, "forkchoice unchanged safe A2 finalized A1")
        before = step_named(steps, "forkchoice safe A2 finalized A1")
        refs = lambda row: ((row.get("refs") or {}).get("finalized"), (row.get("refs") or {}).get("safe"),
                            (row.get("refs") or {}).get("stored_finalized"))
        return st.get("error") is None and st.get("decisions") == [] and again.get("decisions") == [] and \
            refs(st) == refs(before) == ("A1", "A2", "A1")

    def setter_then_error(steps):
        st = step_named(steps, "AC-LIF-05-03 forkchoice finalized A2 then unknown safe")
        rows = st.get("decisions") or []
        refs = st.get("refs") or {}
        return isinstance(st.get("error"), str) and len(rows) == 1 and rows[0].get("kind") == "FINALITY" and \
            rows[0].get("state") == "NATIVE_DECIDED" and refs.get("finalized") == "A2" and \
            refs.get("stored_finalized") == "A2" and refs.get("safe") == "A2"

    verdicts = {
        "AC-CAP-01-01": cap,
        "AC-LIF-05-02": "PASS" if not failures and all(zero_noop(p) for p in paths) else "FAIL",
        "AC-LIF-05-03": "PASS" if not failures and all(setter_then_error(p) for p in paths) else "FAIL",
        # Parlia vote attestations are BSC only; ETH has no attestation snapshot to read.
        "AC-LIF-06-04": "NOT_APPLICABLE",
        "AC-LIF-06-05": "NOT_APPLICABLE",
    }
    applicable = [v for v in verdicts.values() if v != "NOT_APPLICABLE"]
    return {"slot": "private local ETH (chain 31337, lifecycle_geth/genesis.json)", "verdicts": verdicts,
            "family": "PASS" if all(v == "PASS" for v in applicable) else "FAIL",
            "public": {slot: {row: "NOT_RUN" for row in AC_ROWS} for slot in PUBLIC_SLOTS}}


def judge(report):
    """The native failures, the qa matrix and the run result of one probe report.

    Any native failure fails every applicable row, so the result is the family verdict."""
    failures = native_failures(report)
    matrix = qa_matrix(report, failures)
    return failures, matrix, matrix["family"]


# ---------------------------------------------------------------- preparation


def install_lifecycle(source, copies):
    """Apply the lifecycle patch set and install the lifecycle packages from the retained copies."""
    record = admission_geth.apply_patches(source, copies["lifecycle_native"])
    rows = []
    (source / "extractor" / "lifecycle").mkdir(parents=True)
    for name in SHARED_FILES:
        rows.append({"relative": f"extractor/lifecycle/{name}",
                     "copy": copy_artifact(copies["lifecycle"] / name, source / "extractor" / "lifecycle" / name,
                                           reference(copies["lifecycle"] / name, 4 * 1024 * 1024))})
    for name in ("lifecycle.go", "lifecycle_test.go"):
        relative = f"extractor/admission/native/{name}"
        rows.append({"relative": relative, "copy": copy_artifact(copies["lifecycle_native"] / name, source / relative,
                                                                 reference(copies["lifecycle_native"] / name,
                                                                           4 * 1024 * 1024))})
    record["packages"] = rows
    return record


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
        record = {"schema_version": 1, "kind": "lifecycle-geth-preparation", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": state, "accepted_preparation": accepted_ref,
                  "public_profile_credit": False}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            record["fixture_code"] = capture_probe.copy_sources(FIXTURE_FILES, FIXTURE, output / "fixture-code",
                                                                deadline)
            record["lifecycle_native"] = capture_probe.copy_sources(NATIVE_FILES, NATIVE, output / "lifecycle-native",
                                                                    deadline)
            record["lifecycle_shared"] = capture_probe.copy_sources(SHARED_FILES, SHARED, output / "lifecycle-shared",
                                                                    deadline)
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
            record["lifecycle_install"] = install_lifecycle(source, copies)
            probe = source / PROBE_PATH
            probe.parent.mkdir(parents=True)
            record["probe_source"] = copy_artifact(FIXTURE / "probe.go", probe, reference(FIXTURE / "probe.go"))
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
            record["unit_tests"] = reference(native.command(compiler, [go, "test", "-count=1", "-p", "2", *TEST_PACKAGES],
                                                            mounts, cap=1800, memory="6g", cwd="/work/source",
                                                            environment=env))
            native.command(compiler, [go, "build", "-p", "2", "-trimpath", "-ldflags", f"-X {STAMP}={NATIVE_SOURCE}",
                                      "-o", "/work/bin/lifecycle", "./cmd/extractor-lifecycle-geth"], mounts,
                           cap=1800, memory="6g", cwd="/work/source", environment=env)
            record["binary"] = reference(directory / "bin/lifecycle", BINARY_LIMIT, deadline)
            record["genesis"] = copy_artifact(FIXTURE / "genesis.json", directory / "bin/genesis.json",
                                              reference(FIXTURE / "genesis.json"), deadline=deadline)
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
    require(preparation.get("kind") == "lifecycle-geth-preparation" and preparation.get("result") == "PASS" and
            preparation.get("cleanup_complete") is True, "A passing Geth lifecycle preparation is required")
    _, accepted = json_artifact(preparation["accepted_preparation"], 16 * 1024 * 1024, deadline=deadline)
    manifest_root = Path(accepted["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (preparation_path.parent, manifest_root, ROOT))
    state = capture_probe.source_state()
    with single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "lifecycle-geth-run", "result": "FAIL", "run_id": uuid.uuid4().hex,
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
            native.command(runtime, ["/probe/lifecycle", "run", "/probe/genesis.json", "/out/work", "/out/report.json"],
                           [(binary.parent, "/probe", True), (probe_dir, "/out", False)], cap=600, memory="3g")
            record["execution_seconds"] = time.monotonic() - begun
            record["phases"]["execution_finished"] = time.time()
            record["report"] = reference(probe_dir / "report.json", REPORT_LIMIT, deadline)
            report = load(probe_dir / "report.json", REPORT_LIMIT, deadline)
            failures, matrix, result = judge(report)
            record["native_failures"] = failures
            record["qa"] = matrix
            record["gaps"] = report.get("gaps")
            record["result"] = result
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
            failures, matrix, result = judge(json.loads(args.report.read_text()))
            print(json.dumps({"result": result, "native_failures": failures, "qa": matrix}, indent=1))
            return 0 if result == "PASS" else 1
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
