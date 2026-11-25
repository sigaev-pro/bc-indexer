#!/usr/bin/env python3
"""Path-scheme probes of the BSC fast-node capture profile (stream S0, D108).

`prepare` applies the admission, decision and lifecycle patch sets (01 to 08),
installs their packages, then applies the fast-node patch
(adapters/bsc/state_provider/native/patches/09-fast-node-path.diff) with the
strict applier, checks the format, vets the patched tree and compiles the test
binaries of the BSC decision and lifecycle packages with the accepted offline
compiler. It writes a tool manifest that the caller must pin.

`run` executes them on the accepted private BSC Parlia fixture with the path
state scheme and judges their reports with the independent rules of this file:

- the crash table of the decision bridge in path mode: after the barrier of an
  observed block the persistent state of the store is the state of that block
  (P1), and every crash point recovers as under the hash scheme (P2); the
  fixture's block 2 has the state root of its parent and passes the barrier
  (P5);
- the live boundary: a SIGKILL right after the boundary record, and the
  restart keeps H and raises no source invariant error (P4), on the shipped
  node (eth.New with the capture config).

The third path proof, the pin (P3), is TestNativePathPinSurvivesKill of the
pins package and runs in test/harness/pins.py. Private local evidence gives
no public-profile credit.

  python3 -I test/harness/fixtures/fastnode_bsc/fastnode_bsc.py prepare \\
    --build-manifest /abs/manifest.json --sha256 ACCEPTED_BUILD_SHA256 --output /abs/new-preparation
  python3 -I test/harness/fixtures/fastnode_bsc/fastnode_bsc.py run /abs/new-preparation/tool.json \\
    --sha256 TOOL_PIN --profile-manifest /abs/bsc.json PIN --output /abs/new-run
"""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import signal
import sys
import time
import uuid

FIXTURE = Path(__file__).resolve().parent
ROOT = FIXTURE.parents[3]
HARNESS = ROOT / "test/harness"
sys.path.insert(0, str(HARNESS))
sys.path.insert(0, str(HARNESS / "fixtures/lifecycle_bsc"))
import bsc_admission as base  # noqa: E402
import bsc_decision  # noqa: E402
import bsc_lifecycle  # noqa: E402
import bsc_source_inputs as probe14  # noqa: E402
import pins  # noqa: E402
from artifacts import (Rejected, artifact, check_deadline, copy_artifact, load, pinned_json, read_bytes,  # noqa: E402
                       reference, require, source_snapshot, sources_unchanged)

PATCH = ROOT / "adapters/bsc/state_provider/native/patches/09-fast-node-path.diff"
DECISION_PACKAGE = "./extractor/bsc/decision/"
LIFECYCLE_PACKAGE = "./extractor/bsc/lifecycle/"
BINARIES = {"decision_test_binary": DECISION_PACKAGE, "lifecycle_test_binary": LIFECYCLE_PACKAGE}
DECISION_TEST = "TestNativeBSCDecision"
GOLIVE_TEST = "TestFastNodeGoLiveCrash"
HARNESS_FILES = bsc_decision.HARNESS + ("lifecycle.py",)
PREPARATION_SECONDS = 2 * 3600
RUN_SECONDS = 1800
TEST_CAP = 900
BINARY_LIMIT = 1024 * 1024 * 1024
# The crash points before the barrier of block 1: its state was never committed.
BEFORE_BARRIER = ("first-handoff", "exec-ready-unsynced-drop", "exec-ready-synced")


# ---------------------------------------------------------------------------
# Preparation (not measured).


def install(source, destination_root, deadline):
    """Apply patches 01 to 08 with their packages, then the fast-node patch; never replace a native file."""
    patched, installed = bsc_lifecycle.install(source, destination_root, deadline)
    expected = reference(PATCH, 1024 * 1024, deadline)
    retained = destination_root / "patches" / PATCH.name
    copy_artifact(PATCH, retained, expected, 1024 * 1024, deadline)
    patched.append({"patch": expected, "files": base.apply_patch(source, retained.read_text())})
    return patched, installed


def build(runner, compiler, mounts, patched_paths):
    """gofmt, vet, and compile the two test binaries."""
    env = pins.environment()
    stdout = runner.command(compiler, ["/usr/local/go/bin/gofmt", "-l", "extractor", *patched_paths], mounts, cap=300,
                            memory="2g", cwd="/source", environment=env)
    require(not read_bytes(stdout, 1024 * 1024).strip(), "gofmt reports unformatted files")
    vet = runner.command(compiler, ["/usr/local/go/bin/go", "vet", *bsc_lifecycle.VET, "./extractor/..."], mounts,
                         cap=base.BUILD_CAP, memory="6g", cwd="/source", environment=env)
    binaries = {}
    for name, package in BINARIES.items():
        output = "/work/generated/" + name + ".test"
        runner.command(compiler, ["/usr/local/go/bin/go", "test", "-c", "-trimpath", "-o", output, package], mounts,
                       cap=base.BUILD_CAP, memory="6g", cwd="/source", environment=env)
        binaries[name] = output.rsplit("/", 1)[1]
    return {"vet": reference(vet), "names": binaries}


def prepare(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    require(manifest_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    manifest_ref = reference(manifest_path, 64 * 1024 * 1024, deadline)
    require(manifest_ref["sha256"] == expected_sha, "BuildManifest digest mismatch")
    manifest = load(manifest_path, 64 * 1024 * 1024, deadline)
    pins.check_shared_sources()
    require(not output.is_relative_to(manifest_path.parent), "Preparation cannot write into accepted build storage")
    proof = {"schema_version": 1, "kind": "fastnode-bsc-preparation", "result": "FAIL", "build_manifest": manifest_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": PREPARATION_SECONDS, "runtime": "NOT_RUN", "public_profile_credit": False,
             "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS_FILES, deadline)
        proof["driver"] = reference(Path(__file__).resolve(), 1024 * 1024, deadline)
        proof["patch"] = reference(PATCH, 4 * 1024 * 1024, deadline)
        root = output / "bsc"
        root.mkdir()
        closure, copied, source = probe14.extract_source(manifest, manifest_path, root, deadline)
        patched, installed = install(source, root / "installed", deadline)
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (root / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            shutil.copyfile(source / name, root / "metadata" / name)
        compiler = manifest["builds"]["bsc"]["compiler_image"]
        require(compiler == manifest["image_inputs"]["golang-build-image"]["reference"], "Unexpected compiler image")
        mounts = probe14.build_mounts(source, manifest_path.parent / "cache" / "bsc" / "mod", root)
        proof["checks"] = build(runner, compiler, mounts, sorted({row["path"] for item in patched for row in item["files"]}))
        sources_unchanged(origins, deadline)
        for row in installed:
            require(reference(Path(row["source"]["path"]), 1024 * 1024, deadline) == row["source"],
                    "Installed source changed during preparation")
        require(reference(PATCH, deadline=deadline) == proof["patch"], "The fast-node patch changed during preparation")
        require(reference(Path(__file__).resolve(), deadline=deadline) == proof["driver"], "The driver changed")
        keystore = root / "generated/validator-keystore.json"
        copy_artifact(source / base.KEYSTORE, keystore, reference(source / base.KEYSTORE, deadline=deadline), deadline)
        tool = {"schema_version": 1, "kind": "fastnode-bsc-tool", "build_manifest": manifest_ref,
                "native_source_commit": closure["source_commit"], "source_archive": copied, "compiler": compiler,
                "patched": patched, "installed": installed, "validator_keystore": reference(keystore, deadline=deadline),
                "callers": reference(bsc_lifecycle.SHARED / "callers.json", deadline=deadline),
                "metadata": [reference(root / "metadata" / name, deadline=deadline) for name in ("go.mod", "go.sum")]}
        for name, binary in proof["checks"]["names"].items():
            tool[name] = reference(root / "generated" / binary, BINARY_LIMIT, deadline)
        pins.save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "preparation.json", body)


# ---------------------------------------------------------------------------
# Judge (independent rules).


class Checks:
    """Named checks; a failed check is kept with its detail."""

    def __init__(self):
        self.rows = []

    def check(self, name, ok, detail=None):
        self.rows.append({"check": name, "ok": bool(ok), "detail": None if ok else detail})
        return bool(ok)

    def failures(self):
        return [row for row in self.rows if not row["ok"]]


# The persistent layer above the head at the restart of each crash point of
# the decision, before the native startup rolls it back: block 1 is the state
# being decided, the head is still genesis. The child root is then readable
# at the restart only when the head batch made block 1 the head.
PATH_HEAD_ABOVE = ("barrier-synced", "intent-synced", "block-written", "native-rejection")
PATH_HEAD_MOVED = ("head-written", "decided-unsynced-drop")
PATH_RESTART = {mode: (state, mode in PATH_HEAD_MOVED, head)
                for mode, (state, _, head) in bsc_decision.EXPECTED_RESTART.items()}


class PathRestartRules:
    """The restart table of the decision crash judge in path mode."""

    def __enter__(self):
        self.saved = bsc_decision.EXPECTED_RESTART
        bsc_decision.EXPECTED_RESTART = PATH_RESTART
        return self

    def __exit__(self, *_):
        bsc_decision.EXPECTED_RESTART = self.saved


def ids_of(extra):
    """The state id of block 1 and the persistent state id of the stopped home, then
    the persistent state id after the native startup, as a triple of text."""
    before = extra.get("state_ids_before_restart")
    if not isinstance(before, str) or before.count("/") != 1:
        return "", "", ""
    block, persistent = before.split("/")
    return block, persistent, str(extra.get("persistent_state_id_after_restart") or "")


def judge_decision(c, summary, report, traces):
    """The path rules over the judged crash table: P1 after every barrier, the
    rollback of a persistent layer above the head, and P5."""
    scenarios = {sc["name"]: sc for sc in report["scenarios"]}
    live = scenarios["live"]["extra"]
    c.check("decision: the stores are of the path scheme", live.get("state_scheme") == "path", live)
    c.check("decision: block 2 has the state root of block 1 and was decided through the barrier (P5)",
            live.get("block2_has_the_root_of_block1") == "true" and summary["live"].get("decisions") == 2,
            (live.get("block2_has_the_root_of_block1"), summary["live"].get("decisions")))
    c.check("decision: after the decisions the persistent state is the head state (P1)",
            live.get("head_state_id") not in (None, "none") and live.get("head_state_id") == live.get("persistent_state_id"),
            live)
    barrier = [event for event in traces["barrier-synced"] if event.get("event") == "barrier-check"]
    c.check("decision: the barrier of block 1 returned with its state as the persistent state (P1)",
            len(barrier) == 1 and barrier[0].get("block1_state_id") not in (None, "none") and
            barrier[0].get("block1_state_id") == barrier[0].get("persistent_state_id"), barrier)
    for mode in bsc_decision.CRASH_MODES:
        block, persistent, after = ids_of(scenarios[mode]["extra"])
        if mode in BEFORE_BARRIER:
            # Nothing of block 1 was committed: the persistent state did not move.
            ok = block == "none" and persistent not in ("", "none") and after == persistent
        else:
            # The barrier made the state of block 1 persistent.
            ok = block not in ("", "none") and block == persistent
            if mode in PATH_HEAD_ABOVE:
                # The startup rolls the persistent layer back to the state of
                # the head, which is below the block's state.
                ok = ok and after not in ("", "none") and int(after) < int(block)
            else:
                # The head is block 1: its state stays the persistent state.
                ok = ok and after == block
        c.check(f"decision: {mode} persistent state before and after the restart", ok,
                (block, persistent, after))


def judge_golive(c, report, text):
    """The path rules of the live boundary probe (P4) and of the decided blocks
    (P1), and the node log of the run: no native repair of the head state and
    no source invariant error."""
    child, stopped, restart = report.get("child", {}), report.get("stopped", {}), report.get("restart", {})
    c.check("golive: the node log holds the live boundary", "Extractor lifecycle is live" in text)
    c.check("golive: no native repair of the head state", "Head state missing, repairing" not in text,
            [line for line in text.splitlines() if "Head state missing" in line][:3])
    c.check("golive: no source invariant error", "source invariant" not in text,
            [line for line in text.splitlines() if "source invariant" in line][:3])
    c.check("golive: the child runs the path scheme and reaches the boundary at A3 in LIVE",
            child.get("scheme") == "path" and child.get("boundary") == 3 and child.get("head") == "A3" and
            child.get("phase") == "LIVE", child)
    ids = child.get("head_state_id")
    c.check("golive: the state of the boundary is the persistent state before the kill (P4)",
            ids not in (None, "none") and ids == child.get("persistent_state_id"), child)
    c.check("golive: the stopped home keeps the head A3 and its persistent state",
            stopped.get("head") == "A3" and stopped.get("head_state_id") == ids and
            stopped.get("persistent_state_id") == ids, stopped)
    c.check("golive: the restart is live at the stored boundary without an error (P4)",
            not restart.get("restart_error") and restart.get("phase") == "LIVE" and restart.get("head") == "A3" and
            restart.get("boundary") == 3 and restart.get("view_head") == "A3", restart)
    decided = restart.get("decided") or []
    c.check("golive: two blocks decided after the restart",
            len(decided) == 2 and [row.get("block") for row in decided] == ["A4", "A5"], decided)
    for row in decided:
        c.check(f"golive: {row.get('block')} decided with the head state persistent (P1)",
                not row.get("import_error") and row.get("head_is_block") is True and row.get("new_decisions") == 1 and
                row.get("head_state_id") not in (None, "none") and row.get("head_state_id") == row.get("persistent_state_id"),
                row)


def judge_part(c, part, function, *args):
    """Run one part of the judge; a malformed report is a failed check, never an exception."""
    try:
        function(c, *args)
    except (KeyError, TypeError, ValueError, AttributeError, IndexError) as error:
        c.check(f"{part}: well-formed report", False, f"{type(error).__name__}: {error}")


# ---------------------------------------------------------------------------
# Measured run.


def decision_probe(tool, fixture, genesis_hash, output, runner, deadline):
    """Run the decision probe once on the path scheme; return the judged record."""
    root = output / "decision"
    inputs = pins.prepare_inputs("bsc", fixture, root, deadline)
    binary = artifact(tool["decision_test_binary"], BINARY_LIMIT, deadline=deadline)
    mounts = [(binary.parent, "/tool", True), (root / "inputs", "/inputs", True), (root / "work", "/work", False),
              (root / "temp", "/scratch", False)]
    env = {"EXTRACTOR_DECISION_INPUTS": "/inputs", "EXTRACTOR_DECISION_WORK": "/work",
           "EXTRACTOR_DECISION_NATIVE_COMMIT": tool["native_source_commit"],
           "EXTRACTOR_DECISION_STATE_SCHEME": "path", "TMPDIR": "/scratch", "HOME": "/scratch"}
    started = time.monotonic()
    stdout = runner.command(tool["compiler"], ["/tool/" + binary.name, "-test.v", "-test.count=1", "-test.run",
                                               f"^{DECISION_TEST}$", "-test.skip", f"^{bsc_decision.CHILD_TEST}$",
                                               "-test.timeout", f"{TEST_CAP - 60}s"],
                            mounts, cap=TEST_CAP, memory="2g", cwd="/scratch", environment=env)
    elapsed = round(time.monotonic() - started, 3)
    passed = pins.parse_go_test(read_bytes(stdout, 8 * 1024 * 1024, deadline).decode(), [DECISION_TEST])
    traces = {name: probe14.read_jsonl(root / "work" / f"trace-{name}.jsonl", deadline) for name in bsc_decision.TRACES}
    raw_blocks = probe14.split_rlp_list_items(read_bytes(root / "inputs/blocks.rlp", 1024 * 1024, deadline))
    genesis = read_bytes(root / "inputs/genesis.json", 4 * 1024 * 1024, deadline)
    report_path = root / "work/report.json"
    report = load(report_path, 16 * 1024 * 1024, deadline)
    with PathRestartRules():
        summary = bsc_decision.checked_evaluate(report, traces, raw_blocks, bytes.fromhex(genesis_hash.removeprefix("0x")),
                                                genesis)
    return {"inputs": inputs, "passed": passed, "elapsed_seconds": elapsed, "state_scheme": "path",
            "report": reference(report_path, 16 * 1024 * 1024, deadline), "stdout": reference(stdout, deadline=deadline),
            "traces": {name: reference(root / "work" / f"trace-{name}.jsonl", deadline=deadline)
                       for name in bsc_decision.TRACES}, "summary": summary}, report, traces


def golive_probe(tool, fixture, output, runner, deadline):
    """Run the live boundary probe once on the path scheme; return its report path."""
    root = output / "golive"
    inputs = pins.prepare_inputs("bsc", fixture, root, deadline)
    inputs["validator-keystore.json"] = copy_artifact(Path(tool["validator_keystore"]["path"]),
                                                      root / "inputs/validator-keystore.json", tool["validator_keystore"],
                                                      1024 * 1024, deadline)
    binary = artifact(tool["lifecycle_test_binary"], BINARY_LIMIT, deadline=deadline)
    mounts = [(binary.parent, "/tool", True), (root / "inputs", "/inputs", True), (root / "work", "/work", False),
              (root / "temp", "/scratch", False)]
    env = {"EXTRACTOR_LIFECYCLE_INPUTS": "/inputs", "EXTRACTOR_LIFECYCLE_WORK": "/work",
           "EXTRACTOR_LIFECYCLE_NATIVE_COMMIT": tool["native_source_commit"], "EXTRACTOR_LIFECYCLE_STATE_SCHEME": "path",
           "TMPDIR": "/scratch", "HOME": "/scratch"}
    stdout = runner.command(tool["compiler"], ["/tool/" + binary.name, "-test.v", "-test.count=1", "-test.run",
                                               f"^{GOLIVE_TEST}$", "-test.timeout", f"{TEST_CAP - 60}s"],
                            mounts, cap=TEST_CAP, memory="4g", cwd="/scratch", environment=env)
    pins.parse_go_test(read_bytes(stdout, 16 * 1024 * 1024, deadline).decode(), [GOLIVE_TEST])
    report_path = root / "work/golive-report.json"
    return {"inputs": inputs, "stdout": reference(stdout, deadline=deadline),
            "report": reference(report_path, deadline=deadline)}, load(report_path, 16 * 1024 * 1024, deadline), \
        read_bytes(stdout, 16 * 1024 * 1024, deadline).decode()


def run(tool_path, tool_sha, manifest_path, manifest_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(tool_path.is_absolute() and output.is_absolute() and manifest_path.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "fastnode-bsc-tool", "Not a fast-node tool")
    fixture, genesis_hash = probe14.load_inputs(manifest_path, manifest_sha, tool, deadline)
    proof = {"schema_version": 1, "kind": "fastnode-bsc-run", "result": "FAIL", "tool": tool_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": RUN_SECONDS, "public_profile_credit": False,
             "profile": "private local BSC Parlia fixture chain 714 in the path state scheme (not a public profile)",
             "fixture": {key: value for key, value in fixture.items() if key != "build_root"}, "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS_FILES, deadline)
        proof["phases"] = {"execution_started": time.time()}
        proof["decision"], report, traces = decision_probe(tool, fixture, genesis_hash, output, runner, deadline)
        proof["golive"], golive, node_log = golive_probe(tool, fixture, output, runner, deadline)
        proof["phases"]["execution_finished"] = time.time()
        sources_unchanged(origins, deadline)
        for name in BINARIES:
            artifact(tool[name], BINARY_LIMIT, deadline=deadline)
        c = Checks()
        judge_part(c, "decision", judge_decision, proof["decision"]["summary"], report, traces)
        judge_part(c, "golive", judge_golive, golive, node_log)
        proof["checks"], proof["failures"] = len(c.rows), c.failures()
        require(not proof["failures"], f"{len(proof['failures'])} checks failed: "
                                       f"{[row['check'] for row in proof['failures'][:5]]}")

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
    run_parser.add_argument("--profile-manifest", nargs=2, metavar=("PATH", "SHA256"), required=True)
    run_parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)

    def interrupt(number, _frame):
        raise InterruptedError("Interrupted by " + signal.Signals(number).name)
    for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(number, interrupt)
    try:
        if args.command == "prepare":
            proof = prepare(args.build_manifest, args.sha256, args.output)
        else:
            proof = run(args.tool, args.sha256, Path(args.profile_manifest[0]), args.profile_manifest[1], args.output)
    except (Rejected, *pins.FAILURES) as error:
        print(json.dumps({"result": "REFUSED", "error": f"{type(error).__name__}: {str(error)[:400]}"}))
        return 2
    print(json.dumps({key: proof.get(key) for key in ("result", "error", "failures")}, default=str)[:2000])
    return 0 if proof.get("result") == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
