#!/usr/bin/env python3
"""Prepare, run and mutate the BSC block of E01-02.decision_bridge in owned containers.

`prepare` applies the BSC admission patch set and the decision patch
(adapters/bsc/decision/native/patches) to the accepted BSC source tree with the
strict unified-diff applier of bsc_admission.py, installs the shared packages
(source_inputs, admission, decision, state_provider), the BSC admission package
(extractor/bsc) and the BSC decision package (extractor/bsc/decision), checks
the format, vets the patched and installed packages, compiles the probe test
binary with the accepted offline compiler, and writes a tool manifest that the
caller must pin.

`run` executes the probe binary against the accepted private BSC Parlia
fixture and checks the report and the event traces with its own rules: every
intent, NATIVE_DECIDED, rejected-intent, marker, admission DECIDED and
decision evidence digest is recomputed with the independent references in
decision.py and admission.py and the fixture bytes; the trace order shows the
child-root barrier before the intent and the intent before every native
write; and each crash point of AC-CAP-07-01, 07-02, 07-03 and 07-06 recovers
as the decision README requires. Private local evidence gives no
public-profile credit; the qa verdict is a separate record.

`mutants` runs single-check mutants of the BSC decision package, the BSC
admission seam and the patched native files from a table inside a fresh copy
of the accepted BSC tree.
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

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE))
from artifacts import (Rejected, artifact, check_deadline, copy_artifact, load, pinned_json, read_bytes, reference,
                       require, source_snapshot, sources_unchanged)
from native import Native
import admission
import bsc_admission as base
import bsc_source_inputs as probe14
import decision
import pins
import source_inputs

NATIVE = ROOT / "adapters" / "bsc" / "decision" / "native"
GO_DIR = NATIVE / "go"
PATCHES = NATIVE / "patches"
SHARED = base.SHARED + ((ROOT / "adapters/bsc/decision/decision.go", "extractor/decision/decision.go"),)
PACKAGE = "./extractor/bsc/decision/"
HARNESS = base.HARNESS + ("bsc_decision.py", "decision.py")
CHILD_TEST = "TestNativeBSCDecisionChild"
CRASH_MODES = ("first-handoff", "exec-ready-unsynced-drop", "exec-ready-synced", "barrier-synced", "intent-synced",
               "block-written", "head-written", "decided-unsynced-drop", "native-rejection")
TRACES = ("live",) + CRASH_MODES
# The AC case that each crash point proves.
AC_OF_MODE = {"first-handoff": "AC-CAP-07-01", "exec-ready-unsynced-drop": "AC-CAP-07-01",
              "exec-ready-synced": "AC-CAP-07-02", "barrier-synced": "AC-CAP-07-02", "intent-synced": "AC-CAP-07-03",
              "block-written": "AC-CAP-07-03", "head-written": "AC-CAP-07-03", "decided-unsynced-drop": "AC-CAP-07-03",
              "native-rejection": "AC-CAP-07-06"}
STORE_ID = "0123456789abcdef0123456789abcdef"
PRODUCER, RUN = "bsc-probe", "run-1"
OWNER = f"{PRODUCER}.{RUN}"
EVIDENCE_TAG = b"extractor-bsc-decision-evidence-v1\x00"
BAD_BLOCK_TAG = b"extractor-bsc-bad-block-v1\x00"
PREPARATION_SECONDS = 2 * 3600
RUN_SECONDS = 1800
MUTANT_SECONDS = 6 * 3600
TEST_CAP = 900


# ---- preparation ----

def patch_files():
    paths = sorted(PATCHES.glob("*.diff"))
    require(paths, "No decision patch files")
    return base.patch_files() + paths


def package_files():
    """The BSC admission and decision package files and their install paths."""
    rows = [(path, "extractor/bsc/decision/" + path.name) for path in sorted(GO_DIR.glob("*.go"))]
    require(rows, "No BSC decision package files")
    return base.bsc_files() + rows


def install(source, destination_root, deadline):
    """Patch the native tree and install the packages; never replace a native file."""
    require(not (source / "extractor").exists(), "extractor already exists in the native tree")
    patched = []
    for path in patch_files():
        expected = reference(path, 1024 * 1024, deadline)
        retained = destination_root / "patches" / path.name
        retained.parent.mkdir(parents=True, exist_ok=True)
        copy_artifact(path, retained, expected, 1024 * 1024, deadline)
        patched.append({"patch": expected, "files": base.apply_patch(source, retained.read_text())})
    installed = []
    for origin, relative in SHARED + tuple(package_files()):
        expected = reference(origin, 1024 * 1024, deadline)
        target = source / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        retained = destination_root / "installed" / relative
        retained.parent.mkdir(parents=True, exist_ok=True)
        copy_artifact(origin, retained, expected, 1024 * 1024, deadline)
        copy_artifact(origin, target, expected, 1024 * 1024, deadline)
        installed.append({"relative": relative, "source": expected,
                          "retained": reference(retained, 1024 * 1024, deadline)})
    return patched, installed


def build(runner, compiler, mounts, test_binary, patched_paths, check_format=True):
    """gofmt (optional), vet, and compile the probe binary; a failure raises Rejected."""
    env = pins.environment()
    if check_format:
        stdout = runner.command(compiler, ["/usr/local/go/bin/gofmt", "-l", "extractor", *patched_paths], mounts,
                                cap=300, memory="2g", cwd="/source", environment=env)
        require(not read_bytes(stdout, 1024 * 1024).strip(), "gofmt reports unformatted files")
        runner.command(compiler, ["/usr/local/go/bin/go", "vet", *base.VET], mounts, cap=base.BUILD_CAP, memory="6g",
                       cwd="/source", environment=env)
    runner.command(compiler, ["/usr/local/go/bin/go", "test", "-c", "-trimpath", "-o", "/work/generated/" + test_binary,
                              PACKAGE], mounts, cap=base.BUILD_CAP, memory="6g", cwd="/source", environment=env)


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
    proof = {"schema_version": 1, "kind": "bsc-decision-preparation", "result": "FAIL", "build_manifest": manifest_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": PREPARATION_SECONDS, "runtime": "NOT_RUN", "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS, deadline)
        root = output / "bsc"
        root.mkdir()
        closure, copied, source = probe14.extract_source(manifest, manifest_path, root, deadline)
        patched, installed = install(source, root, deadline)
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (root / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            copy_artifact(source / name, root / "metadata" / name, reference(source / name, deadline=deadline),
                          deadline=deadline)
        compiler = manifest["builds"]["bsc"]["compiler_image"]
        require(compiler == manifest["image_inputs"]["golang-build-image"]["reference"], "Unexpected compiler image")
        mounts = probe14.build_mounts(source, manifest_path.parent / "cache" / "bsc" / "mod", root)
        build(runner, compiler, mounts, "bsc-decision.test", sorted({row["path"] for item in patched
                                                                     for row in item["files"]}))
        sources_unchanged(origins, deadline)
        for row in installed:
            require(reference(Path(row["source"]["path"]), 1024 * 1024, deadline) == row["source"],
                    "Installed source changed during preparation")
        tests = probe14.test_names([Path(row["retained"]["path"]) for row in installed
                                    if row["relative"].startswith("extractor/bsc/decision/") and
                                    row["relative"].endswith("_test.go")])
        tool = {"schema_version": 1, "kind": "bsc-decision-tool", "build_manifest": manifest_ref,
                "native_source_commit": closure["source_commit"], "source_archive": copied, "compiler": compiler,
                "patched": patched, "installed": installed, "source_root": str(source),
                "tests": [name for name in tests if name != CHILD_TEST],
                "test_binary": reference(root / "generated/bsc-decision.test", deadline=deadline),
                "metadata": [reference(root / "metadata" / name, deadline=deadline) for name in ("go.mod", "go.sum")]}
        pins.save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "preparation.json", body)


# ---- independent checks ----

def sha(data):
    return hashlib.sha256(data).digest()


def head_bytes(head):
    return head["number"].to_bytes(8, "big") + bytes.fromhex(head["hash"])


def evidence_digest(kind, old_head, new_head, justified, finalized):
    """The decision evidence digest from the BSC decision README."""
    data = EVIDENCE_TAG + len(kind).to_bytes(8, "big") + kind.encode()
    for head in (old_head, new_head, justified, finalized):
        data += head_bytes(head)
    return sha(data).hex()


def bad_block_evidence(raw_block):
    """The rejection evidence of a native bad block: tag, block hash, SHA-256 of its RLP."""
    block_hash = probe14.keccak256(probe14.rlp_list(raw_block)[0])
    return sha(BAD_BLOCK_TAG + block_hash + sha(raw_block)).hex()


def segments(events):
    """Split a trace at its restart markers."""
    out, current = [], []
    for event in events:
        if event.get("event") == "restart":
            out.append(current)
            current = []
        else:
            current.append(event)
    return out + [current]


def puts(events, scope, record=None):
    return [event for event in events if event.get("event") == "put" and event.get("scope") == scope and
            (record is None or event.get("record") == record)]


def value(event):
    return json.loads(event["value"])


KEY_EVENTS = ("recover", "exec-ready", "bridge-open", "settle", "complete-intent", "replay-undecided", "replay",
              "replayed", "barrier", "evidence", "intent", "intent-resumed", "native-block", "native-head", "decided",
              "intent-rejected", "live")


def key_names(events, until_live=True):
    """The protocol events of a segment, up to its first "live"; a settle event names its outcome."""
    names = []
    for event in events:
        name = event.get("event")
        if name not in KEY_EVENTS:
            continue
        names.append(name + ":" + event["outcome"] if name == "settle" else name)
        if until_live and name == "live":
            break
    return names


# The protocol events of block 1 after the restart, per crash point.
REPLAYED = ["bridge-open", "replay-undecided", "replay", "replayed", "barrier", "evidence", "intent", "native-block",
            "native-head", "decided", "live"]
COMPLETED = ["bridge-open", "settle:complete", "complete-intent", "replay", "replayed", "barrier", "evidence",
             "intent-resumed", "native-block", "native-head", "decided", "live"]
EXPECTED_AFTER_RESTART = {
    "first-handoff": ["recover", "exec-ready"] + REPLAYED,
    "exec-ready-unsynced-drop": ["recover", "exec-ready"] + REPLAYED,
    "exec-ready-synced": REPLAYED,
    "barrier-synced": REPLAYED,
    "intent-synced": COMPLETED,
    "block-written": COMPLETED,
    "head-written": ["bridge-open", "settle:marker", "decided", "live"],
    "decided-unsynced-drop": ["bridge-open", "settle:marker", "decided", "live"],
    "native-rejection": ["bridge-open", "settle:native-rejection", "intent-rejected", "live"],
}
# Whether block 1 is stored natively when recovery completes its intent.
SETTLE_STORED = {"intent-synced": False, "block-written": True}
# The last protocol events of the child before its crash.
EXPECTED_CHILD_END = {
    "first-handoff": ["live"],
    "exec-ready-unsynced-drop": ["live"],
    "exec-ready-synced": ["live"],
    "barrier-synced": ["live", "exec-ready", "barrier"],
    "intent-synced": ["live", "exec-ready", "barrier", "evidence"],
    "block-written": ["live", "exec-ready", "barrier", "evidence", "intent", "native-block"],
    "head-written": ["live", "exec-ready", "barrier", "evidence", "intent", "native-block", "native-head"],
    "decided-unsynced-drop": ["live", "exec-ready", "barrier", "evidence", "intent", "native-block", "native-head"],
    "native-rejection": ["live", "exec-ready", "barrier", "evidence", "intent"],
}
# The state at the restart: the open observation of block 1, whether its
# child root is on disk, and the native head.
EXPECTED_RESTART = {
    "first-handoff": ("ADMITTED", False, 0), "exec-ready-unsynced-drop": ("ADMITTED", False, 0),
    "exec-ready-synced": ("EXEC_READY", False, 0), "barrier-synced": ("EXEC_READY", True, 0),
    "intent-synced": ("EXEC_READY", True, 0), "block-written": ("EXEC_READY", True, 0),
    "head-written": ("EXEC_READY", True, 1), "decided-unsynced-drop": ("EXEC_READY", True, 1),
    "native-rejection": ("EXEC_READY", True, 0),
}


class Fixture:
    """The fixture blocks and the expected decision evidence of each block."""

    def __init__(self, raw_blocks, genesis_hash, plato_block):
        require(len(raw_blocks) == 2, "Fixture must hold two blocks")
        self.raw = raw_blocks
        self.headers = [base.header_fields(probe14.rlp_list(raw)[0]) for raw in raw_blocks]
        require([h["number"] for h in self.headers] == [1, 2], "Fixture block numbers")
        self.genesis = {"number": 0, "hash": genesis_hash.hex()}
        require(self.headers[0]["parent"] == genesis_hash and self.headers[1]["parent"] == self.headers[0]["hash"],
                "Fixture blocks are not linked to the genesis")
        # Before Plato, Parlia gives the genesis as the finalized block, and
        # with no attestation in the snapshot the justified block is the
        # genesis too; the fixture has no attestation.
        require(plato_block > 2, "The fixture expectation needs both blocks before Plato")
        self.plato = plato_block

    def head(self, number):
        return self.genesis if number == 0 else {"number": number, "hash": self.headers[number - 1]["hash"].hex()}

    def check_intent(self, record, sequence, number):
        """Check an IMPORT intent of block `number` from every source fact; return its member."""
        require(record["sequence"] == sequence and record["native_store_id"] == STORE_ID and
                record["producer"] == OWNER, f"Intent {sequence} identity")
        require(record["kind"] == "IMPORT" and record["events"] is True, f"Intent {sequence} is not an IMPORT")
        require(record["old_head"] == self.head(number - 1) and record["new_head"] == self.head(number),
                f"Intent {sequence} heads")
        require(len(record["members"]) == 1, f"Intent {sequence} members")
        member = record["members"][0]
        header = self.headers[number - 1]
        require(member["block"] == header["hash"].hex() and member["child_root"] == header["root"].hex(),
                f"Intent {sequence} member block or child root")
        evidence = evidence_digest("IMPORT", self.head(number - 1), self.head(number), self.genesis, self.genesis)
        require(record["evidence_digest"] == evidence, f"Intent {sequence} evidence digest")
        require(record["digest"] == decision.intent_digest(record), f"Intent {sequence} digest")
        return member


def check_evidence_events(events, fixture):
    """Every evidence event names its block, heads, genesis finality and digest."""
    for event in events:
        if event.get("event") != "evidence":
            continue
        number = event["new_head"]["number"]
        require(event["kind"] == "IMPORT" and event["old_head"] == fixture.head(number - 1) and
                event["new_head"] == fixture.head(number), "Evidence heads")
        require(event["justified"] == fixture.genesis and event["finalized"] == fixture.genesis,
                "Finality result of a pre-Plato block is not the genesis")
        require(event["digest"] == evidence_digest("IMPORT", event["old_head"], event["new_head"], event["justified"],
                                                   event["finalized"]), "Evidence digest")


def check_barrier_order(events):
    """Each intent record follows the barrier and the evidence of its block; native writes follow its intent."""
    keys = []
    for event in events:
        name = event.get("event")
        if name == "put" and event.get("scope") == "decision" and event.get("record") == "intent":
            require(keys[-2:] == ["barrier", "evidence"], "An intent without the barrier and the evidence before it")
        if name in ("native-block", "native-head"):
            require(keys and keys[-1] in ("intent", "intent-resumed", "native-block"),
                    f"{name} before the intent of its decision")
        if name in KEY_EVENTS:
            keys.append(name)


def decisions_of(events):
    """The decision records that a trace put, by sequence and kind (the last value wins)."""
    out = {}
    for event in puts(events, "decision"):
        if event["record"] in ("intent", "decided", "rejected"):
            out.setdefault(int(event["owner"].split("/")[1], 16), {})[event["record"]] = value(event)
    return out


def check_decided(sequence, intent, decided):
    require(decided == {"sequence": sequence, "intent_digest": intent["digest"], "delivery": "BATCH",
                        "digest": decision.decided_digest(intent["digest"], "BATCH")}, f"NATIVE_DECIDED {sequence}")


def check_admission_decided(events, member, decision_digest):
    """The admission DECIDED record of the member binds its EXEC_READY digest and the decision digest."""
    rows = [value(e) for e in puts(events, "admission", "decided") if value(e)["observation"] == member["observation"]]
    require(rows, f"No admission DECIDED record of {member['observation']}")
    for row in rows:
        require(row["exec_ready_digest"] == member["exec_ready_digest"] and row["decision_digest"] == decision_digest
                and row["digest"] == admission.decided_digest(member["exec_ready_digest"], decision_digest),
                f"Admission DECIDED of {member['observation']}")


def exec_ready_records(events):
    return {value(e)["observation"]: value(e) for e in puts(events, "admission", "exec-ready")}


def check_reimport(sc, events):
    """After a restart, blocks in the chain open no observation and no decision."""
    reimport = sc["reimport"]
    require(reimport["error"] == "" and reimport["observation_counter"][0] == reimport["observation_counter"][1] and
            reimport["decision_counter"][0] == reimport["decision_counter"][1], f"{sc['name']}: re-import")
    require(reimport["head"] == sc["head_number"] and reimport["blocks"] == sc["head_number"],
            f"{sc['name']}: re-import head")
    names = [e.get("event") for e in events if e.get("event") != "sync"]
    require(names[:3] == ["start", "bridge-open", "live"], f"{sc['name']}: restart of the re-import")
    require(not set(names) & {"begin", "replay", "barrier", "intent", "intent-resumed", "decided", "put"},
            f"{sc['name']}: the re-import observed or decided a block")


def check_final(sc, fixture, decisions_seen, rejected):
    """The final state: LIVE, the head, no open observation, inputs or pins, the markers and obligations."""
    head = 0 if rejected else 2
    require(sc["mode"] == "LIVE" and sc["head_number"] == head and
            (head == 0 or sc["head_hash"] == fixture.head(head)["hash"]), f"{sc['name']}: final head")
    require(not sc["observations"] and not sc["capsules"] and not sc["pins"],
            f"{sc['name']}: open observations, capsules or pins after the decisions")
    markers = {int(key, 16): bytes.fromhex(data) for key, data in sc["markers"].items()}
    for sequence, data in markers.items():
        require(sequence in decisions_seen, f"Marker {sequence} has no intent")
        require(data == decision.marker_value(decisions_seen[sequence]["intent"]), f"Marker {sequence} bytes")
    obligations = [row["sequence"] for row in sc["obligations"]]
    for row in sc["obligations"]:
        intent = decisions_seen[row["sequence"]]["intent"]
        require(row["intent_digest"] == intent["digest"] and
                row["decided_digest"] == decision.decided_digest(intent["digest"], "BATCH"),
                f"Obligation {row['sequence']} digests")
    return sorted(markers), obligations


def evaluate_live(sc, events, fixture):
    live, after = segments(events)
    require(sc["mode_at_start"] == "RECOVERY_ONLY" and "RECOVERY_ONLY" in sc["extra"]["import_before_recovery"],
            "live: an import before recovery was not refused")
    require("guard" in sc["extra"]["begin_without_guard"], "live: a hook call without the guard was not refused")
    require(sc["import_errors"] == [""], "live: import error")
    check_barrier_order(live)
    check_evidence_events(live, fixture)
    require(key_names(live, until_live=False) == ["bridge-open", "live"] + ["exec-ready", "barrier", "evidence",
                                                                            "intent", "native-block", "native-head",
                                                                            "decided"] * 2, "live: protocol order")
    seen = decisions_of(live)
    require(sorted(seen) == [0, 1], "live: decisions")
    ready = exec_ready_records(live)
    for sequence in (0, 1):
        member = fixture.check_intent(seen[sequence]["intent"], sequence, sequence + 1)
        require(member["exec_ready_digest"] == ready[member["observation"]]["digest"], "live: member EXEC_READY")
        check_decided(sequence, seen[sequence]["intent"], seen[sequence]["decided"])
        check_admission_decided(live, member, seen[sequence]["decided"]["digest"])
    markers, obligations = check_final(sc, fixture, seen, False)
    applied = seen[0]["decided"]["digest"]
    require(sc["extra"]["applied_decision_0"] == applied, "live: the mirror applied another decision")
    require(markers == [1] and obligations == [1], "live: the applied decision kept its marker or obligation")
    require(sorted(sc["decision_records"]) == ["0000000000000001/decided", "0000000000000001/intent", "counter",
                                               "lowwater", "purged"] and
            sc["decision_records"]["lowwater"] == {"sequence": 1}, "live: records of the applied decision")
    check_reimport(sc, after)
    return {"decisions": 2, "obligations": obligations, "markers": markers}


def evaluate_crash(sc, events, fixture):
    mode = sc["name"]
    child, parent, after = segments(events)
    rejected = mode == "native-rejection"
    # The child: block 1 reached the crash point and nothing later.
    require(child[-1].get("event") == "crash" and child[-1].get("point") == mode, f"{mode}: crash marker")
    end = key_names(child, until_live=False)
    require(end == ["bridge-open"] + EXPECTED_CHILD_END[mode], f"{mode}: the child events {end}")
    if mode == "native-rejection":
        require([e.get("event") for e in child[-2:]] == ["native-bad-block", "crash"], f"{mode}: bad-block fixture")
    # The restart state.
    state, on_disk, head = EXPECTED_RESTART[mode]
    require(sc["mode_at_start"] == "RECOVERY_ONLY", f"{mode}: mode at the restart")
    require([(o["key"], o["state"]) for o in sc["observations_after_restart"]] == [(f"{OWNER}.{0:016x}", state)],
            f"{mode}: observation at the restart")
    require(sc["block1_root_on_disk_at_restart"] is on_disk and sc["head_at_restart"] == head,
            f"{mode}: child root or head at the restart")
    # The recovery.
    require(not sc.get("recover_error"), f"{mode}: recovery error")
    require(key_names(parent) == EXPECTED_AFTER_RESTART[mode], f"{mode}: recovery order {key_names(parent)}")
    if mode == "intent-synced":
        require("not open yet" in sc["extra"]["before_bridge"] and
                "still pending" in sc["extra"]["other_block_while_pending"],
                f"{mode}: the hook did not refuse before the intent was settled")
    if mode in SETTLE_STORED:
        settle = next(e for e in parent if e.get("event") == "settle")
        require(settle["stored"] is SETTLE_STORED[mode], f"{mode}: block 1 stored at the settle")
    check_barrier_order(child + parent)
    check_evidence_events(child + parent, fixture)
    # One decision for block 1: the intent of sequence 0 is put once (child or
    # parent); a resumed intent is the same record.
    intents = [value(e) for e in puts(child + parent, "decision", "intent")]
    require([i["sequence"] for i in intents][:1] == [0] and sum(i["sequence"] == 0 for i in intents) == 1,
            f"{mode}: block 1 has more than one intent")
    seen = decisions_of(child + parent)
    member = fixture.check_intent(seen[0]["intent"], 0, 1)
    for event in child + parent:
        if event.get("event") == "intent-resumed":
            require(event["sequence"] == 0 and event["digest"] == seen[0]["intent"]["digest"],
                    f"{mode}: resumed intent")
    # The EXEC_READY record of block 1 is the one the replay matches and the
    # member binds; it is written once, unless its write was lost.
    ready = [value(e) for e in puts(child + parent, "admission", "exec-ready") if value(e)["sequence"] == 0]
    require(ready and all(r == ready[0] for r in ready) and
            len(ready) == (2 if mode == "exec-ready-unsynced-drop" else 1), f"{mode}: EXEC_READY writes")
    require(member["exec_ready_digest"] == ready[-1]["digest"], f"{mode}: member EXEC_READY digest")
    for event in parent:
        if event.get("event") == "replayed":
            require(event["digest"] == ready[-1]["digest"] and event["consumed_digest"] == ready[-1]["consumed_digest"]
                    and event["exec_digest"] == ready[-1]["exec_digest"], f"{mode}: replay differs")
    if rejected:
        evidence = bad_block_evidence(fixture.raw[0])
        settle = next(e for e in parent if e.get("event") == "settle")
        require(settle["evidence"] == evidence, f"{mode}: bad-block evidence")
        want = {"sequence": 0, "intent_digest": seen[0]["intent"]["digest"], "evidence_digest": evidence,
                "digest": decision.rejected_digest(seen[0]["intent"]["digest"], evidence)}
        require(seen[0].get("rejected") == want and "decided" not in seen[0], f"{mode}: rejected intent record")
        rejection = next(e for e in parent if e.get("event") == "intent-rejected")
        require(rejection["digest"] == want["digest"], f"{mode}: rejection event")
        check_admission_decided(parent, member, want["digest"])
        require(not [e for e in parent if e.get("event") in ("native-block", "native-head", "decided")],
                f"{mode}: a native write or decision after the rejection")
        require(sc["import_errors"] == [] and sorted(sc["decision_records"]) == ["counter", "lowwater", "purged"] and
                sc["decision_records"]["lowwater"] == {"sequence": 1}, f"{mode}: records after the rejection")
    else:
        decided_puts = [value(e) for e in puts(child + parent, "decision", "decided") if value(e)["sequence"] == 0]
        require(len(decided_puts) == (2 if mode == "decided-unsynced-drop" else 1) and
                all(d == decided_puts[0] for d in decided_puts), f"{mode}: NATIVE_DECIDED writes of block 1")
        check_decided(0, seen[0]["intent"], seen[0]["decided"])
        check_admission_decided(parent, member, seen[0]["decided"]["digest"])
        require(sc["import_errors"] == [""], f"{mode}: live import after recovery")
        require(sorted(seen) == [0, 1], f"{mode}: decisions")
        member2 = fixture.check_intent(seen[1]["intent"], 1, 2)
        check_decided(1, seen[1]["intent"], seen[1]["decided"])
        check_admission_decided(parent, member2, seen[1]["decided"]["digest"])
    markers, obligations = check_final(sc, fixture, seen, rejected)
    want = [] if rejected else [0, 1]
    require(markers == want and obligations == want, f"{mode}: markers {markers} or obligations {obligations}")
    check_reimport(sc, after)
    return {"ac": AC_OF_MODE[mode], "after_restart": key_names(parent), "markers": markers, "obligations": obligations}


def evaluate(report, traces, raw_blocks, genesis_hash, genesis_file):
    """Check the report and every trace; return a summary per scenario."""
    config = json.loads(genesis_file)["config"]
    fixture = Fixture(raw_blocks, genesis_hash, config["platoBlock"])
    scenarios = {sc["name"]: sc for sc in report["scenarios"]}
    require(list(scenarios) == list(TRACES), f"Scenarios {list(scenarios)}")
    summary = {"live": evaluate_live(scenarios["live"], traces["live"], fixture)}
    for mode in CRASH_MODES:
        summary[mode] = evaluate_crash(scenarios[mode], traces[mode], fixture)
    return summary


def checked_evaluate(*args):
    """Evaluate, turning a malformed report into a rejection instead of an exception."""
    try:
        return evaluate(*args)
    except Rejected:
        raise
    except (KeyError, TypeError, ValueError, AttributeError, IndexError, StopIteration) as error:
        raise Rejected(f"Malformed probe report: {type(error).__name__}: {error}") from None


# ---- run ----

def probe_run(tool, fixture, genesis_hash, output, runner, deadline, binary=None):
    """Run the probe binary once and check it; return the family record."""
    root = output / "bsc"
    for name in ("inputs", "work", "temp"):
        (root / name).mkdir(parents=True)
    inputs = {name: copy_artifact(Path(ref["path"]), root / "inputs" / name, ref, 64 * 1024 * 1024, deadline)
              for name, ref in fixture["inputs"].items()}
    binary = binary or artifact(tool["test_binary"], 1024 * 1024 * 1024, deadline=deadline)
    mounts = [(binary.parent, "/tool", True), (root / "inputs", "/inputs", True), (root / "work", "/work", False),
              (root / "temp", "/scratch", False)]
    env = {"EXTRACTOR_DECISION_INPUTS": "/inputs", "EXTRACTOR_DECISION_WORK": "/work",
           "EXTRACTOR_DECISION_NATIVE_COMMIT": tool["native_source_commit"], "TMPDIR": "/scratch", "HOME": "/scratch"}
    started = time.monotonic()
    stdout = runner.command(tool["compiler"], ["/tool/" + binary.name, "-test.v", "-test.count=1", "-test.run", "^Test",
                                               "-test.skip", f"^{CHILD_TEST}$", "-test.timeout", f"{TEST_CAP - 60}s"],
                            mounts, cap=TEST_CAP, memory="2g", cwd="/scratch", environment=env)
    elapsed = round(time.monotonic() - started, 3)
    passed = pins.parse_go_test(read_bytes(stdout, 8 * 1024 * 1024, deadline).decode(), tool["tests"])
    traces = {name: probe14.read_jsonl(root / "work" / f"trace-{name}.jsonl", deadline) for name in TRACES}
    raw_blocks = probe14.split_rlp_list_items(read_bytes(root / "inputs/blocks.rlp", 1024 * 1024, deadline))
    genesis = read_bytes(root / "inputs/genesis.json", 4 * 1024 * 1024, deadline)
    report_path = root / "work/report.json"
    summary = checked_evaluate(load(report_path, 16 * 1024 * 1024, deadline), traces, raw_blocks,
                               bytes.fromhex(genesis_hash.removeprefix("0x")), genesis)
    return {"inputs": inputs, "passed": passed, "elapsed_seconds": elapsed,
            "report": reference(report_path, 16 * 1024 * 1024, deadline), "stdout": reference(stdout, deadline=deadline),
            "traces": {name: reference(root / "work" / f"trace-{name}.jsonl", deadline=deadline) for name in TRACES},
            "summary": summary}


def run(tool_path, tool_sha, manifest_path, manifest_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(tool_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "bsc-decision-tool", "Not a BSC decision tool")
    fixture, genesis_hash = probe14.load_inputs(manifest_path, manifest_sha, tool, deadline)
    proof = {"schema_version": 1, "kind": "bsc-decision-run", "result": "FAIL", "tool": tool_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": RUN_SECONDS, "public_profile_credit": False, "ac_verdict": None, "error": None,
             "fixture": {key: value for key, value in fixture.items() if key != "build_root"}}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS, deadline)
        proof["phases"] = {"execution_started": time.time()}
        proof["bsc"] = probe_run(tool, fixture, genesis_hash, output, runner, deadline)
        proof["phases"]["execution_finished"] = time.time()
        sources_unchanged(origins, deadline)
        artifact(tool["test_binary"], 1024 * 1024 * 1024, deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "run.json", body)


# ---- mutants ----

def load_table(path, source=None):
    """Mutant rows target a BSC decision file ("go/<name>"), a BSC admission file ("seam/<name>") or a patched
    native file ("native/<path>")."""
    table = json.loads(path.read_text())
    require(table.get("schema_version") == 1 and table.get("language") == "go-native-bsc-decision",
            "Unsupported mutant table")
    rows = table["mutants"]
    names = [row["name"] for row in rows]
    require(names and len(names) == len(set(names)), "Mutant names must be unique")
    for row in rows:
        require(set(row) <= {"name", "file", "original", "replacement", "equivalent"},
                f"Mutant {row['name']} has an unsupported shape")
        require(row["file"].startswith(("go/", "seam/", "native/")) and ".." not in Path(row["file"]).parts,
                f"Mutant {row['name']} file {row['file']}")
        require(row["original"] != row["replacement"], f"Mutant {row['name']} changes nothing")
        if row["file"].startswith("go/"):
            text = (GO_DIR / row["file"][3:]).read_text()
        elif row["file"].startswith("seam/"):
            text = (base.GO_DIR / row["file"][5:]).read_text()
        elif source is not None:
            text = (source / row["file"][7:]).read_text()
        else:
            continue
        require(text.count(row["original"]) == 1, f"Mutant {row['name']} original must occur exactly once")
    return table


def mutant_target(source, row):
    if row["file"].startswith("go/"):
        return source / ("extractor/bsc/decision/" + row["file"][3:])
    if row["file"].startswith("seam/"):
        return source / ("extractor/bsc/" + row["file"][5:])
    return source / row["file"][7:]


def mutants(tool_path, tool_sha, manifest_path, manifest_sha, table_path, output, only=()):
    started = time.monotonic()
    deadline = started + MUTANT_SECONDS
    require(tool_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "bsc-decision-tool", "Not a BSC decision tool")
    fixture, genesis_hash = probe14.load_inputs(manifest_path, manifest_sha, tool, deadline)
    table = load_table(table_path)
    selected = [row for row in table["mutants"] if not only or row["name"] in only]
    require(selected, "No mutant selected")
    manifest_ref = Path(tool["build_manifest"]["path"])
    _, manifest = pinned_json(manifest_ref, tool["build_manifest"]["sha256"], 64 * 1024 * 1024, deadline)
    proof = {"schema_version": 1, "kind": "bsc-decision-mutants", "result": "FAIL", "tool": tool_ref,
             "table": reference(table_path, deadline=deadline), "source_commit": pins.source_commit(),
             "run_id": uuid.uuid4().hex, "native_owner_nonce": None, "deadline_seconds": MUTANT_SECONDS,
             "mutants": [], "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS, deadline)
        root = output / "build"
        root.mkdir()
        _, _, source = probe14.extract_source(manifest, manifest_ref, root, deadline)
        install(source, root, deadline)
        load_table(table_path, source)
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath"):
            (root / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            shutil.copyfile(source / name, root / "metadata" / name)
        mounts = probe14.build_mounts(source, manifest_ref.parent / "cache" / "bsc" / "mod", root)
        for index, row in enumerate(selected):
            check_deadline(deadline)
            target = mutant_target(source, row)
            original = target.read_text()
            target.write_text(original.replace(row["original"], row["replacement"], 1))
            binary = root / "generated" / f"mutant-{index:03d}.test"

            def probe(probe_runner, directory):
                try:
                    probe_run(tool, fixture, genesis_hash, directory, probe_runner, deadline, binary=binary)
                finally:
                    binary.unlink(missing_ok=True)

            try:
                result = probe14.run_mutant(
                    index, row, output, runner, proof["mutants"], lambda path: Native(path, deadline),
                    lambda build_runner: build(build_runner, tool["compiler"], mounts, binary.name, [],
                                               check_format=False), probe)
            finally:
                target.write_text(original)
            require(result["cleanup_complete"], f"Mutant {row['name']} left resources behind")
        sources_unchanged(origins, deadline)
        bad, proof["counts"] = probe14.tally(proof["mutants"])
        require(not bad, f"Mutants not killed: {bad}")

    return pins.owned_attempt(output, started, deadline, proof, "mutants.json", body)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prep = commands.add_parser("prepare")
    prep.add_argument("--build-manifest", type=Path, required=True)
    prep.add_argument("--sha256", required=True)
    prep.add_argument("--output", type=Path, required=True)
    for name in ("run", "mutants"):
        command = commands.add_parser(name)
        command.add_argument("tool", type=Path)
        command.add_argument("--sha256", required=True)
        command.add_argument("--profile-manifest", nargs=2, metavar=("PATH", "SHA256"), required=True)
        command.add_argument("--output", type=Path, required=True)
        if name == "mutants":
            command.add_argument("--table", type=Path, default=NATIVE / "bsc_decision.mutants.json")
            command.add_argument("--only", nargs="*", default=())
    args = parser.parse_args()

    def interrupt(number, _frame):
        raise InterruptedError("Interrupted by " + signal.Signals(number).name)
    for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(number, interrupt)
    try:
        if args.command == "prepare":
            proof = prepare(args.build_manifest, args.sha256, args.output)
        else:
            manifest = (Path(args.profile_manifest[0]), args.profile_manifest[1])
            if args.command == "run":
                proof = run(args.tool, args.sha256, *manifest, args.output)
            else:
                proof = mutants(args.tool, args.sha256, *manifest, args.table.resolve(), args.output, set(args.only))
    except pins.FAILURES as error:
        print(json.dumps({"result": "REFUSED", "error": f"{type(error).__name__}: {str(error)[:400]}"}))
        return 2
    print(json.dumps({"result": proof["result"], "error": proof["error"]}))
    return 0 if proof["result"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
