#!/usr/bin/env python3
"""Build and run the measured probe of the Bor block of E01-02.lifecycle_seams.

`prepare` installs the accepted admission and decision blocks and this block
into the accepted Bor source tree (lifecycle_tree.py), copies the probe and
the Heimdall stub into it, runs the in-tree Go tests of the lifecycle
packages, and builds the probe offline with the accepted compiler. It derives
the fixture genesis: the accepted Bor test genesis with test key 1 funded and
`stateSyncConfirmationDelay` {"0": 0} (README, Fixture).

`run`, under the host lock, runs the probe in the pinned runtime image, one
process per step (README, Measured run): generate, the first-start scenario,
a restart, a crash before a lifecycle marker, the restart that settles it, a
lost head state root, the restart with native startup repair, the manual
commands, the `bor server` profile check, the admin_importChain caller, and
the summed coverage with `lifecycle.Gaps`. It then judges the outputs and
writes the AC matrix of the POLY slot.

`observe DIR --output NEW` judges a retained run directory again without
Docker and writes only into the new directory NEW. `vectors` prints the
evidence digests of the Go test vectors from the README formula.
"""

import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import struct
import sys
import tarfile
import time
import uuid

NATIVE = Path(__file__).resolve().parent
ROOT = NATIVE.parents[3]
HARNESS = ROOT / "test/harness"
sys.path.insert(0, str(HARNESS))
sys.path.insert(0, str(NATIVE))
import admission  # noqa: E402
import capture_probe  # noqa: E402
import lifecycle  # noqa: E402  (test/harness/lifecycle.py, the independent map checker)
import lifecycle_tree  # noqa: E402
from artifacts import (artifact, check_deadline, copy_artifact, load, pinned_json, read_bytes,  # noqa: E402
                       reference, require)
from capture_probe import Interrupted, block_stops, unblock_stops, write_until_done  # noqa: E402
from native import Native, new_output  # noqa: E402

STUB_SOURCES = HARNESS / "fixtures/source_inputs_bor/heimdallstub"
STUB_BODIES = ROOT / "test/native_corpus/poly/stub"
BODY_FILES = ("status.json", "span-latest.json", "span-0.json", "span-1.json", "clerk-corpus-from-1.json",
              "clerk-empty.json")
PROBE_DIR = "cmd/extractor-bor-lifecycle-probe"
PREPARATION_SECONDS = 3600
RUN_SECONDS = 1200
STATE_SYNC_DELAY = {"0": 0}
SENDER = "7e5f4552091a69125d5dfcb7b8c2659029395bdf"
SENDER_BALANCE = 10 ** 20
STORE_ID = "0123456789abcdef0123456789abcdef"
HEIMDALL_HOST = "heimdall.stub.invalid"
GO_TESTS = ("./extractor/lifecycle", "./extractor/borlifecycle/...", "./extractor/bordecision",
            "./extractor/decision/...", "./extractor/admission/...", "./eth/downloader/whitelist")
TAG_EVIDENCE = "extractor-bor-lifecycle-evidence-v1"


def go_environment():
    return {"CGO_ENABLED": "1", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64",
            "GOCACHE": "/work/build-cache", "GOMODCACHE": "/modules", "GOPATH": "/work/gopath",
            "GOFLAGS": "-mod=readonly", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
            "GOTMPDIR": "/work/gotmp", "TMPDIR": "/work/gotmp", "GOWORK": "off"}


# ---------------------------------------------------------------------------
# Fixture and digests.


def derived_inputs(source_genesis):
    """The fixture genesis and chain-config items in the shared canonical
    JSON form: the accepted test genesis with test key 1 funded and the state
    sync delay 0."""
    document = json.loads(source_genesis)
    config = document.get("config")
    require(isinstance(config, dict) and isinstance(config.get("bor"), dict), "The genesis has no Bor config")
    require(config.get("chainId") == 15001, "The source genesis is not the private Bor chain 15001")
    require("stateSyncConfirmationDelay" not in config["bor"], "The source genesis already sets the delay")
    config["bor"]["stateSyncConfirmationDelay"] = dict(STATE_SYNC_DELAY)
    alloc = document["alloc"]
    require(SENDER not in {address.lower().removeprefix("0x") for address in alloc}, "The sender is already funded")
    alloc["0x" + SENDER] = {"balance": hex(SENDER_BALANCE)}
    return admission.canonical(json.dumps(document).encode()), admission.canonical(json.dumps(config).encode())


def digest(tag, *values):
    """SHA-256 of tag, one zero byte, then each value as its 8-byte big-endian
    length and its bytes (README, Digests)."""
    h = hashlib.sha256(tag.encode() + b"\0")
    for value in values:
        data = value.encode()
        h.update(struct.pack(">Q", len(data)) + data)
    return h.hexdigest()


def evidence_digest(e):
    return digest(TAG_EVIDENCE, e["caller"], e["kind"], e["operation"], str(e["old_head"]["number"]),
                  e["old_head"]["hash"], str(e["new_head"]["number"]), e["new_head"]["hash"], e["key"], e["old"],
                  e["new"])


VECTORS = (
    {"caller": "bor.set-head", "kind": "CANONICAL", "operation": "set-head",
     "old_head": {"number": 7, "hash": "11" * 32}, "new_head": {"number": 6, "hash": "00" * 32},
     "key": "", "old": "", "new": ""},
    {"caller": "bor.milestone", "kind": "FINALITY", "operation": "last-finality",
     "old_head": {"number": 4, "hash": "11" * 32}, "new_head": {"number": 4, "hash": "11" * 32},
     "key": "6c617374", "old": "", "new": "ab"},
)


# ---------------------------------------------------------------------------
# Judgment (from the probe outputs only).

# The native head after each scenario step, by generated block name.
SCENARIO_HEADS = (
    ("import-A1-A5", "A5"), ("side-B3", "A5"), ("side-B4", "A5"), ("set-canonical-A2", "A2"), ("known-A3-refused", "A2"), ("known-B3", "B3"),
    ("known-B4", "B4"), ("side-C4", "B4"), ("set-canonical-C4", "C4"), ("set-canonical-stateless-D4", "D4"),
    ("stateless-E4", "D4"), ("side-chain-E5", "E5"), ("side-F4", "E5"), ("side-G4", "E5"),
    ("milestone-reorg-F4", "F4"), ("checkpoint-F4", "F4"), ("milestone-F4", "F4"), ("future-milestone-16", "F4"),
    ("milestone-vote-12", "F4"), ("milestone-remove-vote", "F4"), ("milestone-purge-after-3", "F4"),
    ("refuse-engine-api", "F4"), ("refuse-snap-sync", "F4"), ("refuse-seal-write", "F4"),
    ("refuse-stateless", "F4"), ("deadlock-check", "H8"), ("set-head-7", "H7"),
)

# The open decisions after the scenario: kind, old head, new head, members.
# FINALITY decisions have no public events and are cleaned up once decided.
SCENARIO_DECISIONS = (
    ("IMPORT", "?", "A1", ["A1"]), ("IMPORT", "A1", "A2", ["A2"]), ("IMPORT", "A2", "A3", ["A3"]),
    ("IMPORT", "A3", "A4", ["A4"]), ("IMPORT", "A4", "A5", ["A5"]), ("CANONICAL", "A5", "A2", []),
    ("CANONICAL", "A2", "B3", ["B3"]), ("CANONICAL", "B3", "B4", ["B4"]), ("CANONICAL", "B4", "C4", ["C4"]),
    ("CANONICAL", "C4", "D4", ["D4"]), ("CANONICAL", "D4", "E5", ["E4", "E5"]), ("SIDE", "E5", "E5", ["G4"]),
    ("CANONICAL", "E5", "B3", []), ("IMPORT", "B3", "F4", ["F4"]), ("CANONICAL", "F4", "F4", []),
    ("IMPORT", "F4", "H5", ["H5"]), ("IMPORT", "H5", "H6", ["H6"]), ("IMPORT", "H6", "H7", ["H7"]),
    ("IMPORT", "H7", "H8", ["H8"]), ("CANONICAL", "H8", "H7", []),
)

# The callers each profile argument set must refuse ("" is a profile value
# that no caller row names).
PROFILE_REFUSALS = {
    "capture-profile": [], "snap-sync": ["bor.snap-sync"], "parallel-evm": ["bor.parallel"],
    "pipelined-import": ["bor.pipelined"], "stateless-import": ["bor.stateless"], "witness-sync": ["bor.stateless"],
    "preconfs": ["bor.preconf"], "accept-preconf-tx": ["bor.preconf"], "sequencer": ["bor.preconf"],
    "mining": ["bor.seal-write"], "dev": ["bor.seal-write"], "dev-fake-author": ["bor.seal-write"],
}

COVERAGE_FILES = ("scenario", "restart", "settle", "resolved", "imported", "advance", "completed", "repair",
                  "native-caller", "stateless", "refuse", "bootstrap", "bootstrap-restart")
OUTSIDE_FILES = ("profile", "manual")


def rows(decisions):
    return [(d["kind"], d["old_head"], d["new_head"], d["members"]) for d in decisions if d["state"] == "NATIVE_DECIDED"]


def summed_coverage(outputs):
    """The gate coverage of every node process with the refusals that
    happened outside the gate (profile check, manual commands)."""
    total = {}

    def add(caller, field, count=1):
        reach = total.setdefault(caller, {"Passes": 0, "Intents": 0, "Refusals": 0})
        reach[field] += count

    for name in COVERAGE_FILES:
        for caller, reach in sorted(outputs[name]["coverage"].items()):
            for field in ("Passes", "Intents", "Refusals"):
                add(caller, field, reach[field])
    for row in outputs["profile"]["sets"]:
        for refusal in row.get("refusals") or []:
            if refusal["caller"]:
                add(refusal["caller"], "Refusals")
    for row in outputs["manual"]["commands"]:
        if row["refused"]:
            add(row["caller"], "Refusals")
    return total


def gaps(callers_map, coverage):
    """lifecycle.Gaps of the Bor callers, independently of the Go code."""
    out = []
    for caller in callers_map["callers"]:
        if caller["client"] != "bor":
            continue
        reach = coverage.get(caller["id"], {"Passes": 0, "Intents": 0, "Refusals": 0})
        if (caller["route"] == "OBSERVATION" and reach["Passes"] == 0
                or caller["route"] == "DECISION" and reach["Intents"] == 0
                or caller["route"] == "REFUSE" and reach["Refusals"] == 0):
            out.append(caller["id"])
    return sorted(out)


def judge(outputs, callers_map):
    """Every check of the measured run; each failure is named."""
    failures = []

    def check(ok, message):
        if not ok:
            failures.append(message)

    sc = outputs["scenario"]
    check(not sc.get("error") and not sc.get("decisions_error"), "the scenario reported an error")
    check(sc.get("bootstrap_at_start") is True and sc["reconcile"]["phase"] == "BOOTSTRAP"
          and sc["phase_after_start"] == "LIVE", "the first start did not go from BOOTSTRAP to LIVE")
    boundary = sc["reconcile"].get("boundary") or {}
    check(boundary.get("head", {}).get("number") == 0, "H of the first start is not the genesis")
    steps = [(s["name"], s["head_name"]) for s in sc["steps"]]
    check(steps == list(SCENARIO_HEADS), f"scenario steps or heads differ: {steps}")
    for s in sc["steps"]:
        check(s["error"] == "", f"step {s['name']} failed: {s['error']}")
    check(rows(sc["decisions"]) == [(k, o, n, m) for k, o, n, m in SCENARIO_DECISIONS],
          "the scenario decisions differ from the expected transitions")
    check(all(d["state"] == "NATIVE_DECIDED" for d in sc["decisions"]), "a scenario decision is not decided")
    for name, expect in (("restart", "H7"), ("settle", "H6"), ("resolved", "H5"), ("imported", "H6"),
                         ("advance", "H8"), ("completed", "H5"), ("repair", None)):
        out = outputs[name]
        check(not out.get("error"), f"{name} reported an error")
        check(out["reconcile"]["phase"] == "RECOVERY" and out["phase_after_start"] == "LIVE",
              f"{name} did not go from RECOVERY to LIVE")
        check(out["reconcile"].get("boundary") == boundary, f"{name} did not keep H")
        check(expect is None or out["native_head"]["name"] == expect,
              f"{name} ended at {out['native_head']['name']}, not {expect}")
        check(all(d["state"] == "NATIVE_DECIDED" for d in out["decisions"]), f"{name} left a decision unresolved")
    check(outputs["restart"]["reconcile"]["settled"] == [] and outputs["restart"]["reconcile"]["repaired"] is None,
          "the clean restart settled or repaired something")
    check(rows(outputs["restart"]["decisions"]) == rows(sc["decisions"]), "the clean restart changed the decisions")
    for name, line in (("crash", "crash point: marker of bor.set-head"),
                       ("crash-resolve", "crash point: resolve of bor.set-head"),
                       ("crash-import", "crash point: import marker of H6"),
                       ("crash-mid", "crash point: write of bor.set-head")):
        check(outputs[name]["exit"] == 3 and line in outputs[name]["stderr_tail"], f"the {name} child did not stop at "
              f"its crash point")
    settle = outputs["settle"]
    check([(r["caller"], r["outcome"]) for r in settle["reconcile"]["settled"]] == [("bor.set-head", "NATIVE_DECIDED")],
          "the restart did not settle the set-head intent as decided")
    check(rows(settle["decisions"])[-1:] == [("CANONICAL", "H7", "H6", [])],
          "the settled decision is not the set-head from H7 to H6")
    for name, last in (("resolved", ("CANONICAL", "H6", "H5", [])), ("imported", ("IMPORT", "H5", "H6", ["H6"]))):
        out = outputs[name]
        check(out["reconcile"]["settled"] == [] and out["reconcile"]["repaired"] is None
              and len((out.get("decision_recovery") or {}).get("resolved") or []) == 1,
              f"{name}: the decision recovery did not resolve the marked intent")
        check(rows(out["decisions"])[-1:] == [last], f"{name}: the resolved decision is not {last}")
    advance, completed = outputs["advance"], outputs["completed"]
    check([(s["name"], s["error"]) for s in advance["steps"]] == [("import-H7", ""), ("import-H8", "")]
          and rows(advance["decisions"])[-2:] == [("IMPORT", "H6", "H7", ["H7"]), ("IMPORT", "H7", "H8", ["H8"])],
          "the advance did not import H7 and H8")
    start = completed["native_head_at_start"]
    check(start["name"] in ("H6", "H7"), f"the mid-rewind crash left the head at {start['name']}, not between H8 and H5")
    check([(r["caller"], r["outcome"], r["completed"]) for r in completed["reconcile"]["settled"]]
          == [("bor.set-head", "NATIVE_DECIDED", True)]
          and rows(completed["decisions"])[-1:] == [("CANONICAL", "H8", "H5", [])],
          "the interrupted rewind was not completed and decided")
    for name in COVERAGE_FILES:
        check(outputs[name].get("evidence_records") == 0, f"{name} left lifecycle evidence records")
    fault = outputs["fault"]
    check(fault["number"] == 5, "the fault did not hit the head H5")
    repair = outputs["repair"]
    repaired = repair["reconcile"].get("repaired")
    check(repaired is not None and repaired["hash"] == repair["native_head_at_start"]["hash"]
          and repair["native_head_at_start"]["number"] < 5,
          "the startup reconcile did not decide the native repair")
    check(rows(repair["decisions"])[-1:] == [("CANONICAL", "H5", repair["native_head_at_start"]["name"], [])],
          "the repair decision is not from H5 to the repaired head")
    check(repair["coverage"].get("bor.startup-reconcile", {}).get("Intents") == 1,
          "bor.startup-reconcile made no intent")
    stateless, refuse = outputs["stateless"], outputs["refuse"]
    check(not stateless.get("error") and stateless["native_head"]["name"] == "D5"
          and all(s["error"] == "" for s in stateless["steps"]), "the third store did not import A1 to D5")
    check(outputs["fault-stateless"]["block"] == "D4", "the stateless fault did not hit D4")
    check(not refuse.get("error") and refuse["phase_after_start"] == "LIVE"
          and [(s["name"], s["error"]) for s in refuse["steps"]] == [("refuse-set-head-4", "")]
          and refuse["native_head"]["name"] == "D5" and rows(refuse["decisions"]) == rows(stateless["decisions"]),
          "the rewind to a target without state was not refused before any intent")
    check(refuse["decisions"] == stateless["decisions"]
          and all(d["state"] == "NATIVE_DECIDED" for d in refuse["decisions"] + stateless["decisions"])
          and refuse["coverage"].get("bor.set-head", {}).get("Intents", 0) == 0,
          "the refused rewind left an intent or changed a decision")
    boot, again = outputs["bootstrap"], outputs["bootstrap-restart"]
    crash_boot = outputs["crash-bootstrap"]
    check(crash_boot["exit"] == 3 and "crash point: during BOOTSTRAP" in crash_boot["stderr_tail"],
          "the BOOTSTRAP child did not stop at its crash point")
    boundary4 = boot.get("reconcile", {}).get("boundary") or {}
    check(not boot.get("error") and boot.get("bootstrap_at_start") is True
          and boot["reconcile"]["phase"] == "BOOTSTRAP" and boot["phase_after_start"] == "LIVE"
          and all(s["error"] == "" for s in boot["steps"])
          and [s["name"] for s in boot["steps"]][-4:] == ["golive", "import-B3", "import-B4", "import-A6"],
          "BOOTSTRAP did not sync natively and then go live")
    before = boot.get("coverage_before_golive", {}).get("bor.milestone", {})
    check(before.get("Passes", 0) >= 1 and before.get("Intents", 1) == 0 and boot.get("golive_waited_for_vote") is True,
          "the whitelist callers did not run through the gate in BOOTSTRAP, or GoLive did not wait for the vote")
    check(boot.get("head_at_golive", {}).get("name") == "A5"
          and boundary4.get("head", {}).get("hash") == boot["head_at_golive"]["hash"],
          "H is not the native head after the initial sync (A5)")
    check(rows(boot["decisions"]) == [("SIDE", "A5", "A5", ["B3"]), ("SIDE", "A5", "A5", ["B4"]),
                                      ("IMPORT", "A5", "A6", ["A6"])],
          "the initial sync was captured, or the imports after H were not")
    check(all(d["state"] == "NATIVE_DECIDED" for d in boot["decisions"] + again["decisions"]),
          "a BOOTSTRAP-store decision is not decided")
    check(not again.get("error") and again.get("bootstrap_at_start") is False
          and again["reconcile"]["phase"] == "RECOVERY" and again["phase_after_start"] == "LIVE"
          and again["reconcile"].get("boundary") == boundary4 and again["native_head"]["name"] == "A6"
          and rows(again["decisions"]) == rows(boot["decisions"]),
          "the restart after BOOTSTRAP did not keep H")
    manual = outputs["manual"]
    check([r["refused"] for r in manual["commands"]] == [True, True, True, True] and manual["chaindata_kept"],
          "a manual command was not refused, or the store is gone")
    check([(r["caller"], (MANUAL_REFUSED if r["caller"] == "bor.manual-commands" else RETENTION_REFUSED) in r["output"])
           for r in manual["commands"]] == [("bor.manual-commands", True), ("bor.prune-commands", True),
                                            ("bor.manual-commands", True), ("bor.manual-commands", True)],
          "a manual command was not refused by the capture-store or retention guard")
    profile = outputs["profile"]
    got = {r["name"]: sorted(x["caller"] for x in (r.get("refusals") or [])) for r in profile["sets"]}
    check(got == {k: sorted(v) for k, v in PROFILE_REFUSALS.items()}, f"profile refusals differ: {got}")
    check(not any(r.get("config_error") for r in profile["sets"]), "a profile argument set did not parse")
    check(profile["server"]["refused"] and not profile["server"]["chaindata_created"],
          "bor server with --extractor.capture --mine was not refused before eth.New")
    check(profile.get("plain_server", {}).get("refused") is True,
          "bor server without --extractor.capture was not refused on the capture store")
    native = outputs["native-caller"]
    check(not native.get("error"), "the admin_importChain node reported an error")
    check(native["after_import"]["head"] == sc["after_import"]["head"]
          and rows(native["after_import"]["decisions"]) == rows(sc["after_import"]["decisions"]),
          "admin_importChain and the harness import differ")
    check(native["steps"] == [{"name": "admin-import-A1-A5", "head": native["native_head"]["hash"],
                               "head_name": "A5", "head_number": 5, "error": ""}],
          "admin_importChain did not import A1 to A5")
    coverage = summed_coverage(outputs)
    python_gaps = gaps(callers_map, coverage)
    check(python_gaps == [], f"lifecycle gaps: {python_gaps}")
    check(outputs["gaps"]["gaps"] == python_gaps, "the Go and Python gaps differ")
    return failures, coverage


AC_ROWS = (
    ("AC-CAP-01-01", "ALL", "POLY private local: every Bor caller of the map reached as its route requires (Gaps "
     "empty); the same blocks through the harness (InsertChain) and the native admin_importChain caller reach the "
     "bridge with the same native result"),
    ("AC-LIF-05-02", "ETH", "ETH forkchoice zero-hash rule: not a POLY case; Bor has no Engine API forkchoice "
     "(bor.engine-api is refused)"),
    ("AC-LIF-05-03", "ETH", "ETH forkchoice setter-then-error rule: not a POLY case"),
    ("AC-LIF-06-04", "BSC", "BSC Parlia attestation genesis fallback: not a POLY case"),
    ("AC-LIF-06-05", "BSC", "BSC Parlia attestation source error: not a POLY case"),
)


def ac_matrix(result):
    """The qa AC matrix of the POLY slot. Public cells stay NOT_RUN (D96)."""
    matrix = []
    for ac, scope, note in AC_ROWS:
        local = ("PASS" if result == "PASS" else "FAIL") if scope == "ALL" else "NOT_APPLICABLE"
        matrix.append({"ac": ac, "scope": scope, "poly_private_local": local, "polygon_mainnet": "NOT_RUN",
                       "amoy": "NOT_RUN", "note": note})
    return matrix


def write_new(path, value):
    """Write JSON to a new file; an existing file (a retained record) is never replaced."""
    with open(path, "x") as handle:
        handle.write(json.dumps(value, indent=1, sort_keys=True) + "\n")


OUTPUT_FILES = {"scenario": "scenario.json", "restart": "restart.json", "crash": "crash.json", "settle": "settle.json",
                "crash-resolve": "crash-resolve.json", "resolved": "resolved.json", "crash-import": "crash-import.json",
                "imported": "imported.json", "advance": "advance.json", "crash-mid": "crash-mid.json",
                "completed": "completed.json", "fault": "fault.json", "repair": "repair.json", "manual": "manual.json",
                "profile": "profile.json", "native-caller": "native-caller.json", "stateless": "stateless.json",
                "fault-stateless": "fault-stateless.json", "refuse": "refuse.json",
                "crash-bootstrap": "crash-bootstrap.json", "bootstrap": "bootstrap.json",
                "bootstrap-restart": "bootstrap-restart.json", "gaps": "gaps.json"}

MANUAL_REFUSED = "manual chaindata command refused on a capture store"
RETENTION_REFUSED = "live pins forbid this native deletion"


def observe(directory, callers_map, output):
    """Judge the probe outputs in directory; write judgment.json as a new
    file in output and return the result, failures and AC matrix."""
    outputs = {name: load(directory / file, 16 * 1024 * 1024) for name, file in OUTPUT_FILES.items()}
    failures, coverage = judge(outputs, callers_map)
    result = "PASS" if not failures else "FAIL"
    judgment = {"schema_version": 1, "kind": "bor-lifecycle-judgment", "result": result, "failures": failures,
                "coverage": coverage, "ac_matrix": ac_matrix(result)}
    write_new(output / "judgment.json", judgment)
    return judgment


# ---------------------------------------------------------------------------
# Preparation (not measured).


def extract(data, source, deadline):
    with tarfile.open(fileobj=io.BytesIO(data)) as stream:
        for member in stream:
            check_deadline(deadline)
            stream.extract(member, source, filter="data")


def prepare(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    require(manifest_path.is_absolute(), "BuildManifest path must be absolute")
    manifest_ref, manifest = pinned_json(manifest_path, expected_sha, 64 * 1024 * 1024, deadline)
    require(manifest.get("evidence_kind") == "actual-build" and manifest.get("task_id") == "E01-01.builds",
            "An accepted actual build is required")
    root = manifest_path.parent
    capture_probe.refuse_inside(output, (root, ROOT))
    state = capture_probe.source_state()
    with capture_probe.single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "bor-lifecycle-preparation", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": state, "build_manifest": manifest_ref}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            record["fixture"] = capture_probe.copy_sources(("lifecycle_bor.py", "lifecycle_tree.py", "patches.json"),
                                                           NATIVE, output / "fixture", deadline)
            record["probe_code"] = capture_probe.copy_sources(("main.go",), NATIVE / "probe", output / "probe",
                                                              deadline)
            record["callers"] = capture_probe.copy_sources(("callers.json",), NATIVE.parent, output / "callers",
                                                           deadline)
            record["stub_bodies"] = capture_probe.copy_sources(BODY_FILES, STUB_BODIES, output / "stub", deadline)
            record["harness_code"] = capture_probe.copy_sources(
                ("bor_admission_tree.py", "lifecycle.py", "admission.py", "capture_probe.py", "native.py", "owned.py",
                 "artifacts.py"), HARNESS, output / "harness-code", deadline)
            compiler = manifest["image_inputs"]["golang-build-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/golang@sha256:[0-9a-f]{64}", compiler), "Unpinned compiler")
            runtime = manifest["image_inputs"]["debian-runtime-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
            record["compiler_image"], record["runtime_image"] = compiler, runtime
            record["bor"] = prepare_bor(manifest, root, output, native, compiler, deadline)
            record["result"] = "PASS"
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "preparation.json", record, native, started)
    return output, record


def prepare_bor(manifest, root, output, native, compiler, deadline):
    closure = manifest["module_closures"]["bor"]
    require(closure["source_commit"] == manifest["builds"]["bor"]["source_commit"], "Source closure mismatch")
    require(manifest["builds"]["bor"]["compiler_image"] == compiler, "Bor compiler mismatch")
    callers_map = json.loads((NATIVE.parent / "callers.json").read_text())
    require(callers_map["sources"]["bor"] == closure["source_commit"], "The caller map pins another Bor source")
    directory = output / "bor"
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
    extract(read_bytes(directory / "source.tar", closure["source_archive"]["size_bytes"], deadline), source, deadline)
    row["installed"] = lifecycle_tree.install(source)
    probe = source / PROBE_DIR
    probe.parent.mkdir(parents=True, exist_ok=True)
    row["probe_sources"] = capture_probe.copy_sources(("main.go",), NATIVE / "probe", probe, deadline)
    row["stub_sources"] = capture_probe.copy_sources(("stub.go",), STUB_SOURCES, probe / "heimdallstub", deadline)
    for name in ("bin", "build-cache", "gopath", "gotmp"):
        (directory / name).mkdir()
    mounts = [(directory, "/work", True), (root / "cache" / "bor" / "mod", "/modules", True)]
    for name in ("bin", "build-cache", "gopath", "gotmp"):
        mounts.append((directory / name, f"/work/{name}", False))

    def go(argv, cap):
        return native.command(compiler, ["/usr/local/go/bin/go", *argv], mounts, cap=cap, memory="6g",
                              cwd="/work/source", environment=go_environment())

    go(["mod", "verify"], 300)
    go(["vet", "./extractor/borlifecycle/...", "./extractor/bordecision", "./eth/downloader/whitelist",
        f"./{PROBE_DIR}/..."], 1200)
    row["go_tests"] = {"packages": list(GO_TESTS),
                       "stdout": reference(go(["test", "-p", "2", "-count=1", *GO_TESTS], 1500), deadline=deadline)}
    go(["build", "-p", "2", "-trimpath", "-o", "/work/bin/probe", f"./{PROBE_DIR}"], 1800)
    bin_directory = directory / "bin"
    source_genesis = (source / "tests/bor/testdata/genesis.json").read_bytes()
    genesis, config = derived_inputs(source_genesis)
    (bin_directory / "genesis.json").write_bytes(genesis)
    (bin_directory / "chain-config.json").write_bytes(config)
    copy_artifact(source / "tests/bor/helper.go", bin_directory / "helper.go",
                  reference(source / "tests/bor/helper.go", 4 * 1024 * 1024, deadline), deadline=deadline)
    for name in ("probe", "genesis.json", "chain-config.json", "helper.go"):
        row[name] = reference(bin_directory / name, deadline=deadline)
    row["source_genesis"] = {"sha256": hashlib.sha256(source_genesis).hexdigest(), "size_bytes": len(source_genesis)}
    return row


# ---------------------------------------------------------------------------
# Measured run.


def run(preparation_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(preparation_path.is_absolute(), "Preparation path must be absolute")
    preparation_ref, preparation = pinned_json(preparation_path, expected_sha, 16 * 1024 * 1024, deadline)
    require(preparation.get("kind") == "bor-lifecycle-preparation" and preparation.get("result") == "PASS"
            and preparation.get("cleanup_complete") is True, "A passing preparation is required")
    manifest_root = Path(preparation["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (preparation_path.parent, manifest_root, ROOT))
    state = capture_probe.source_state()
    with capture_probe.single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "bor-lifecycle-run", "result": "FAIL", "run_id": uuid.uuid4().hex,
                  "source": state, "preparation": preparation_ref,
                  "profile": "private local Bor fixture chain 15001 (POLY private local; not a public profile)"}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            callers_path = artifact(preparation["callers"][0]["copy"], 1024 * 1024, deadline=deadline)
            callers_bytes = read_bytes(callers_path, 1024 * 1024, deadline)
            require(callers_bytes == (NATIVE.parent / "callers.json").read_bytes(),
                    "The prepared caller map differs from the repository")
            callers_map = json.loads(callers_bytes)
            lifecycle.check_map(callers_map)
            runtime = preparation["runtime_image"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
            for row in preparation["stub_bodies"]:
                artifact(row["copy"], 1024 * 1024, deadline=deadline)
            stub = Path(preparation["stub_bodies"][0]["copy"]["path"]).parent
            bor = preparation["bor"]
            binary = artifact(bor["probe"], 512 * 1024 * 1024, deadline=deadline)
            genesis = read_bytes(artifact(bor["genesis.json"], 4 * 1024 * 1024, deadline=deadline), 4 * 1024 * 1024,
                                 deadline)
            config = read_bytes(artifact(bor["chain-config.json"], 1024 * 1024, deadline=deadline), 1024 * 1024,
                                deadline)
            artifact(bor["helper.go"], 4 * 1024 * 1024, deadline=deadline)
            archive = artifact(bor["source_archive"], 512 * 1024 * 1024, deadline=deadline)
            with tarfile.open(archive) as stream:
                source_genesis = stream.extractfile("tests/bor/testdata/genesis.json").read()
            require(derived_inputs(source_genesis) == (genesis, config),
                    "The prepared genesis items differ from the independent derivation")
            write_new(output / "capture.json", {"store_id": STORE_ID, "producer": "bor", "run": "run-1",
                                                "genesis": "/probe/genesis.json",
                                                "chain_config": "/probe/chain-config.json",
                                                "commit": bor["source_commit"], "normalization": "test-only-probe-v1",
                                                "heimdall_host": HEIMDALL_HOST})
            mounts = [(binary.parent, "/probe", True), (stub, "/stub", True), (output, "/out", False)]
            common = ["-genesis", "/probe/genesis.json", "-config", "/probe/chain-config.json", "-bodies", "/stub",
                      "-blocks", "/out/chain/blocks.rlp", "-commit", bor["source_commit"]]

            def probe(*argv, cap=300):
                native.command(runtime, ["/probe/probe", *argv], mounts, cap=cap, memory="2g")

            started_native = time.monotonic()
            probe("generate", "-genesis", "/probe/genesis.json", "-config", "/probe/chain-config.json", "-keysource",
                  "/probe/helper.go", "-bodies", "/stub", "-out", "/out/chain")
            store = [*common, "-datadir", "/out/instance"]
            probe("scenario", *store, "-out", "/out/scenario.json")
            probe("restart", *store, "-out", "/out/restart.json")
            probe("crash", "-record", "/out/crash.json", "--", *store, "-sethead", "6", "-crash", "bor.set-head",
                  "-out", "/out/crash-child.json")
            probe("restart", *store, "-out", "/out/settle.json")
            probe("crash", "-record", "/out/crash-resolve.json", "--", *store, "-sethead", "5", "-crash",
                  "bor.set-head", "-crash-step", "resolve", "-out", "/out/crash-resolve-child.json")
            probe("restart", *store, "-out", "/out/resolved.json")
            probe("crash", "-record", "/out/crash-import.json", "--", *store, "-import", "H6", "-crash-import", "H6",
                  "-out", "/out/crash-import-child.json")
            probe("restart", *store, "-out", "/out/imported.json")
            probe("restart", *store, "-import", "H7,H8", "-out", "/out/advance.json")
            probe("crash", "-record", "/out/crash-mid.json", "--", *store, "-sethead", "5", "-crash", "bor.set-head",
                  "-crash-step", "write", "-crash-count", "4", "-out", "/out/crash-mid-child.json")
            probe("restart", *store, "-out", "/out/completed.json")
            probe("fault", "-datadir", "/out/instance", "-out", "/out/fault.json")
            probe("restart", *store, "-out", "/out/repair.json")
            probe("manual", "-datadir", "/out/instance", "-out", "/out/manual.json")
            probe("profile", "-datadir", "/out/server", "-capture", "/out/capture.json", "-capture-store",
                  "/out/instance", "-out", "/out/profile.json")
            probe("native-caller", *common, "-datadir", "/out/instance2", "-out", "/out/native-caller.json")
            probe("restart", *common, "-datadir", "/out/instance3", "-import", "A1,A2,B3,D4,D5", "-out",
                  "/out/stateless.json")
            probe("fault", "-datadir", "/out/instance3", "-blocks", "/out/chain/blocks.rlp", "-block", "D4", "-out",
                  "/out/fault-stateless.json")
            probe("restart", *common, "-datadir", "/out/instance3", "-refuse-sethead", "4", "-out", "/out/refuse.json")
            probe("crash", "-record", "/out/crash-bootstrap.json", "--", *common, "-datadir", "/out/instance4",
                  "-bootstrap", "-import", "A1,A2,A3", "-crash-bootstrap", "-out", "/out/crash-bootstrap-child.json")
            probe("restart", *common, "-datadir", "/out/instance4", "-bootstrap", "-import", "A1,A2,A3,A4,A5",
                  "-golive", "-whitelist-during-golive", "A2", "-import-live", "B3,B4,A6", "-out",
                  "/out/bootstrap.json")
            probe("restart", *common, "-datadir", "/out/instance4", "-out", "/out/bootstrap-restart.json")
            outputs = {name: load(output / file, 16 * 1024 * 1024) for name, file in OUTPUT_FILES.items()
                       if name != "gaps"}
            write_new(output / "coverage-sum.json", summed_coverage(outputs))
            probe("gaps", "-coverage", "/out/coverage-sum.json", "-out", "/out/gaps.json")
            record["native_seconds"] = time.monotonic() - started_native
            record["outputs"] = {file: reference(output / file, deadline=deadline)
                                 for file in (*OUTPUT_FILES.values(), "coverage-sum.json", "chain/blocks.rlp",
                                              "chain/generated.json")}
            judgment = observe(output, callers_map, output)
            record["judgment"] = reference(output / "judgment.json", deadline=deadline)
            record["result"], record["failures"] = judgment["result"], judgment["failures"]
            record["ac_matrix"] = judgment["ac_matrix"]
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "run-record.json", record, native, started)
    return output, record


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    commands = parser.add_subparsers(dest="command", required=True)
    prep = commands.add_parser("prepare")
    prep.add_argument("--build-manifest", type=Path, required=True)
    prep.add_argument("--sha256", required=True)
    prep.add_argument("--output", type=Path, required=True)
    measured = commands.add_parser("run")
    measured.add_argument("preparation", type=Path)
    measured.add_argument("--sha256", required=True)
    measured.add_argument("--output", type=Path, required=True)
    offline = commands.add_parser("observe", help="judge a retained run again; writes to a new directory")
    offline.add_argument("directory", type=Path)
    offline.add_argument("--output", type=Path, required=True)
    commands.add_parser("vectors", help="print the evidence digests of the Go test vectors")
    derive = commands.add_parser("derive", help="write the fixture genesis items from a source genesis (development)")
    derive.add_argument("source_genesis", type=Path)
    derive.add_argument("output", type=Path)
    args = parser.parse_args(argv)
    if args.command == "prepare":
        output, record = prepare(args.build_manifest, args.sha256, args.output)
    elif args.command == "run":
        output, record = run(args.preparation, args.sha256, args.output)
    elif args.command == "vectors":
        print(json.dumps([evidence_digest(e) for e in VECTORS]))
        return 0
    elif args.command == "derive":
        genesis, config = derived_inputs(args.source_genesis.read_bytes())
        args.output.mkdir(exist_ok=True)
        (args.output / "genesis.json").write_bytes(genesis)
        (args.output / "chain-config.json").write_bytes(config)
        return 0
    else:
        record_path = args.directory / "run-record.json"
        if record_path.exists():
            # The caller map the run used, checked against its recorded digest.
            preparation = load(Path(load(record_path, 16 * 1024 * 1024)["preparation"]["path"]), 16 * 1024 * 1024)
            callers_map = json.loads(artifact(preparation["callers"][0]["copy"], 1024 * 1024).read_text())
        else:
            callers_map = json.loads((NATIVE.parent / "callers.json").read_text())
        output = new_output(args.output)
        judgment = observe(args.directory, callers_map, output)
        print(json.dumps({"result": judgment["result"], "failures": judgment["failures"]}, indent=1))
        return 0 if judgment["result"] == "PASS" else 1
    print(json.dumps({"result": record["result"], "output": str(output), "error": record.get("error")}))
    return 0 if record["result"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
