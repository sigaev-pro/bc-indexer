#!/usr/bin/env python3
"""Prepare, run and judge the BSC block of E01-02.lifecycle_seams.

`prepare` applies the accepted BSC admission and decision patch sets
(test/harness/bsc_decision.py install) and then this block's patch
(`patches/08-core-lifecycle-seams.diff`) to the accepted BSC source tree,
installs the shared lifecycle package (`adapters/bsc/lifecycle`, at
`extractor/lifecycle`) and this block's package (`go/`, at
`extractor/bsc/lifecycle`), checks the format, vets the patched and installed
packages, runs the unit tests of both packages and compiles the probe test
binary with the accepted offline compiler. It writes a tool manifest that the
caller must pin. Preparation is not measured.

`run`, under the host lock (`pins.owned_attempt`), runs the probe
(`TestBSCLifecycleProbe`) once in the compiler image on the accepted private
BSC fixture of the ProfileManifest, then judges its report, trace and blocks
with the independent rules of this file (`judge`) and writes the qa matrix.

`judge RUN --output NEW` judges a retained run again from its recorded
outputs, without Docker, and writes `judge.json` into the new directory NEW.
"""

import argparse
import hashlib
import json
from pathlib import Path
import signal
import sys
import time
import uuid

FIXTURE = Path(__file__).resolve().parent
ROOT = FIXTURE.parents[3]
HARNESS = ROOT / "test/harness"
NATIVE = ROOT / "adapters/bsc/lifecycle/native"
sys.path.insert(0, str(HARNESS))
import bsc_admission as base  # noqa: E402
import bsc_decision  # noqa: E402
import bsc_source_inputs as probe14  # noqa: E402
import lifecycle as shared_lifecycle  # noqa: E402
import pins  # noqa: E402
from artifacts import (Rejected, artifact, copy_artifact, load, pinned_json, read_bytes, reference,  # noqa: E402
                       require, source_snapshot, sources_unchanged)

SHARED = ROOT / "adapters/bsc/lifecycle"
GO_DIR = NATIVE / "go"
PATCH = NATIVE / "patches/08-core-lifecycle-seams.diff"
PACKAGE = "./extractor/bsc/lifecycle/"
BINARY = "bsc-lifecycle.test"
PROBE_TEST = "TestBSCLifecycleProbe"
UNIT_TESTS = ("TestEvidenceDigests", "TestLifecycleIntent", "TestLoadConfig", "TestCheckProfile",
              "TestRequireNoOpenCapture")
VET = base.VET + ("./cmd/utils",)
HARNESS_FILES = bsc_decision.HARNESS + ("lifecycle.py",)
# The block files that decide the verdict, relative to the repository root.
BLOCK_FILES = ("test/harness/fixtures/lifecycle_bsc/bsc_lifecycle.py",
               "adapters/bsc/lifecycle/native/patches/08-core-lifecycle-seams.diff",
               "adapters/bsc/lifecycle/native/go/lifecycle.go", "adapters/bsc/lifecycle/native/go/recover.go",
               "adapters/bsc/lifecycle/native/go/node.go", "adapters/bsc/lifecycle/native/go/attestation.go",
               "adapters/bsc/lifecycle/native/go/unit_test.go", "adapters/bsc/lifecycle/native/go/probe_test.go")
PREPARATION_SECONDS = 2 * 3600
RUN_SECONDS = 1200
PROBE_CAP = 600
BINARY_LIMIT = 1024 * 1024 * 1024

# Independent copies of the README rules.
CANONICAL_TAG = b"extractor-bsc-lifecycle-canonical-v1\x00"
ATTESTATION_TAG = b"extractor-lifecycle-attestation-v1\x00"
BLOCK_ORDER = ("A1", "A2", "A3", "A4", "A5", "A6", "A7", "S3", "S4")
PARENTS = {"A1": "G", "A2": "A1", "A3": "A2", "A4": "A3", "A5": "A4", "A6": "A5", "A7": "A6", "S3": "A2", "S4": "S3"}
VALIDATOR = "03735c2ed70a56cd221e0024eb4bf90243c9d6e9"
DEFERRED = {"bsc.known-block": "D104: a stored block without a retained corpus stops with a source invariant error "
                               "(ErrCorpus) before the head moves; the targeted native replay is E05-02.reorg",
            "bsc.side-chain": "D104: a side branch on a pruned parent is refused at Begin (ErrImportProfile); "
                              "the targeted native replay is E05-02.reorg"}
# The evidence of each READ_ONLY row that this block owns or exercises.
READ_ONLY_EVIDENCE = {
    "bsc.attestation-read": "every decided import reads the attestation through ReadAttestation (AC-LIF-06-04, 06-05)",
    "bsc.execution-witness": "refused on a capture node (ErrExtractorWitness): probe step debug_executionWitness",
    "bsc.system-call-api": "the shipped node builds Parlia with the system-call API (eth.New); Start refuses an "
                           "engine without it (profile start_without_eth_api)",
    "bsc.freezer": "source review: GUARDED by admission patch 02; it moves canonical data only",
    "bsc.eth-call": "source review: a state copy, no write choke point; a write would fail closed (ErrBypass)",
    "bsc.prefetch": "source review: a throwaway state, no write choke point; a write would fail closed (ErrBypass)",
    "bsc.trace-state": "source review: an ephemeral trie database, no write choke point; a write would fail closed "
                       "(ErrBypass)",
}
OUTSIDE_GATE = ("bsc.snap-sync", "bsc.sealing", "bsc.manual-commands", "bsc.database-commands")

# The shared scenario that both paths run: step name, error class, phase,
# head, and the decisions it makes (kind, old head, new head, members).
SHARED_STEPS = (
    ("start (BOOTSTRAP)", "", "BOOTSTRAP", "G", ()),
    ("import A1 A2 A3 (BOOTSTRAP)", "", "BOOTSTRAP", "A3", ()),
    ("import S3 (BOOTSTRAP, side block)", "", "BOOTSTRAP", "A3", ()),
    ("setHead 2 (BOOTSTRAP)", "", "BOOTSTRAP", "A2", ()),
    ("GoLive (H)", "", "LIVE", "A2", ()),
    ("fixture: S3 stored above the head, as a snap-sync rollback leaves it", "", "LIVE", "A2", ()),
    ("InsertChain S3 (known block)", "ErrCorpus", "LIVE", "A2", ()),
    ("import A3 A4 A5", "", "LIVE", "A5", (("IMPORT", "A2", "A3", ("A3",)), ("IMPORT", "A3", "A4", ("A4",)),
                                          ("IMPORT", "A4", "A5", ("A5",)))),
    ("setHead 4", "", "LIVE", "A4", (("CANONICAL", "A5", "A4", ()),)),
    ("import A5 again", "", "LIVE", "A5", (("IMPORT", "A4", "A5", ("A5",)),)),
    ("restart 1 (shutdown, RECOVERY, live at H)", "", "LIVE", "A5", (("CANONICAL", "A5", "A5", ()),)),
    ("import S4 (side branch, pruned parent S3)", "ErrImportProfile", "LIVE", "A5", ()),
    ("import A6 A7", "", "LIVE", "A7", (("IMPORT", "A5", "A6", ("A6",)), ("IMPORT", "A6", "A7", ("A7",)))),
    ("restart 2 (A7 state root deleted: startup repair and reconciliation)", "", "LIVE", "A6",
     (("CANONICAL", "A7", "A7", ()), ("CANONICAL", "A7", "A6", ()))),
    ("InsertChain A7 (stored, its state deleted)", "", "LIVE", "A7", (("IMPORT", "A6", "A7", ("A7",)),)),
)
# The caller of each CANONICAL decision of the shared scenario, by (old, new).
CANONICAL_CALLERS = {("A5", "A4"): "bsc.set-head", ("A5", "A5"): "bsc.shutdown", ("A7", "A7"): "bsc.shutdown",
                     ("A7", "A6"): "bsc.startup-reconcile"}
# The rest of the caller map on the shipped node: step, error class, text.
NATIVE_EXTRAS = (
    ("InsertBlockWithoutSetHead (Engine API)", "ErrRefused", "bsc.engine-api"),
    ("SetCanonical (Engine API)", "ErrRefused", "bsc.engine-api"),
    ("InsertReceiptChain (snap sync)", "ErrRefused", "bsc.snap-sync"),
    ("InsertHeaderChain (header sync)", "ErrRefused", "bsc.snap-sync"),
    ("WriteBlockAndSetHead (miner)", "ErrRefused", "bsc.sealing"),
    ("StartMining", "ErrRefused", "bsc.sealing"),
    ("ResetWithGenesisBlock", "other", "chain reset is refused on a capture store"),
    ("debug_executionWitness", "ErrExtractorWitness", "execution without a state write is refused"),
)


# ---------------------------------------------------------------------------
# Preparation (not measured).


def install(source, root, deadline):
    """Apply the accepted admission and decision installs, then patch 08 and
    the two lifecycle packages; never replace a native file."""
    patched, installed = bsc_decision.install(source, root, deadline)
    expected = reference(PATCH, 1024 * 1024, deadline)
    retained = root / "patches" / PATCH.name
    copy_artifact(PATCH, retained, expected, 1024 * 1024, deadline)
    patched.append({"patch": expected, "files": base.apply_patch(source, retained.read_text())})
    rows = [(SHARED / name, "extractor/lifecycle/" + name) for name in shared_lifecycle.PACKAGE_FILES]
    go = sorted(GO_DIR.glob("*.go"))
    require([path.name for path in go] == sorted(Path(name).name for name in BLOCK_FILES if "/go/" in name),
            "The block package files differ from the declared list")
    rows += [(path, "extractor/bsc/lifecycle/" + path.name) for path in go]
    for origin, relative in rows:
        target = source / relative
        require(not target.exists(), f"{relative} already exists in the native tree")
        ref = reference(origin, 1024 * 1024, deadline)
        target.parent.mkdir(parents=True, exist_ok=True)
        copy_artifact(origin, target, ref, 1024 * 1024, deadline)
        kept = root / "installed" / relative
        kept.parent.mkdir(parents=True, exist_ok=True)
        copy_artifact(origin, kept, ref, 1024 * 1024, deadline)
        installed.append({"relative": relative, "source": ref, "retained": reference(kept, 1024 * 1024, deadline)})
    return patched, installed


def build(runner, compiler, mounts, patched_paths):
    """gofmt, vet, the unit tests of both lifecycle packages, and the probe binary."""
    env = pins.environment()
    stdout = runner.command(compiler, ["/usr/local/go/bin/gofmt", "-l", "extractor", *patched_paths], mounts, cap=300,
                            memory="2g", cwd="/source", environment=env)
    require(not read_bytes(stdout, 1024 * 1024).strip(), "gofmt reports unformatted files")
    vet = runner.command(compiler, ["/usr/local/go/bin/go", "vet", *VET], mounts, cap=base.BUILD_CAP, memory="6g",
                         cwd="/source", environment=env)
    units = runner.command(compiler, ["/usr/local/go/bin/go", "test", "-count=1", "-v", "-run",
                                      "^(" + "|".join(UNIT_TESTS) + ")$", PACKAGE], mounts, cap=base.BUILD_CAP,
                           memory="6g", cwd="/source", environment=env)
    lines = read_bytes(units, 8 * 1024 * 1024).decode().rstrip().splitlines()
    require(lines and lines[-1].startswith("ok ") and PACKAGE.strip("./") in lines[-1],
            "go test did not report ok for the block package")
    pins.parse_go_test("\n".join(lines[:-1]), list(UNIT_TESTS))
    shared = runner.command(compiler, ["/usr/local/go/bin/go", "test", "-count=1", "./extractor/lifecycle/"], mounts,
                            cap=base.BUILD_CAP, memory="6g", cwd="/source", environment=env)
    require(b"\nok " in b"\n" + read_bytes(shared, 8 * 1024 * 1024), "The shared lifecycle tests did not pass")
    runner.command(compiler, ["/usr/local/go/bin/go", "test", "-c", "-trimpath", "-o", "/work/generated/" + BINARY,
                              PACKAGE], mounts, cap=base.BUILD_CAP, memory="6g", cwd="/source", environment=env)
    return {"vet": reference(vet), "unit_tests": reference(units), "shared_tests": reference(shared)}


def prepare(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + PREPARATION_SECONDS
    require(manifest_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    manifest_ref = reference(manifest_path, 64 * 1024 * 1024, deadline)
    require(manifest_ref["sha256"] == expected_sha, "BuildManifest digest mismatch")
    manifest = load(manifest_path, 64 * 1024 * 1024, deadline)
    shared_lifecycle.check_identical(ROOT)
    require(not output.is_relative_to(manifest_path.parent), "Preparation cannot write into accepted build storage")
    proof = {"schema_version": 1, "kind": "bsc-lifecycle-preparation", "result": "FAIL", "build_manifest": manifest_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": PREPARATION_SECONDS, "runtime": "NOT_RUN", "public_profile_credit": False,
             "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS_FILES, deadline)
        proof["block_sources"] = block_snapshot(output, deadline)
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
        proof["checks"] = build(runner, compiler, mounts, sorted({row["path"] for item in patched
                                                                  for row in item["files"]}))
        sources_unchanged(origins, deadline)
        sources_unchanged(proof["block_sources"], deadline)
        for row in installed:
            require(reference(Path(row["source"]["path"]), 1024 * 1024, deadline) == row["source"],
                    "Installed source changed during preparation")
        keystore = root / "generated/validator-keystore.json"
        copy_artifact(source / base.KEYSTORE, keystore, reference(source / base.KEYSTORE, deadline=deadline),
                      deadline=deadline)
        tool = {"schema_version": 1, "kind": "bsc-lifecycle-tool", "build_manifest": manifest_ref,
                "native_source_commit": closure["source_commit"], "source_archive": copied, "compiler": compiler,
                "patched": patched, "installed": installed, "source_root": str(source),
                "test_binary": reference(root / "generated" / BINARY, BINARY_LIMIT, deadline),
                "validator_keystore": reference(keystore, deadline=deadline),
                "callers": reference(SHARED / "callers.json", deadline=deadline),
                "metadata": [reference(root / "metadata" / name, deadline=deadline) for name in ("go.mod", "go.sum")]}
        pins.save(output / "tool.json", tool)
        proof["tool"] = reference(output / "tool.json", deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "preparation.json", body)


def block_snapshot(output, deadline=None):
    """Copy the block files that decide the verdict into the record; return their references."""
    origins = []
    for name in BLOCK_FILES:
        source = ROOT / name
        origins.append(reference(source, 4 * 1024 * 1024, deadline))
        target = output / "block-code" / name
        target.parent.mkdir(parents=True, exist_ok=True)
        copy_artifact(source, target, origins[-1], 4 * 1024 * 1024, deadline)
    return origins


# ---------------------------------------------------------------------------
# Judge (independent rules).


def sha(data):
    return hashlib.sha256(data).digest()


def str_bytes(value):
    data = value.encode()
    return len(data).to_bytes(8, "big") + data


def canonical_evidence(caller, old_head, new_head):
    """The CANONICAL evidence digest of the BSC lifecycle README."""
    return sha(CANONICAL_TAG + str_bytes(caller) + bsc_decision.head_bytes(old_head) +
               bsc_decision.head_bytes(new_head)).hex()


def attestation_digest(status, justified, finalized):
    """The attestation digest of the shared lifecycle README."""
    return sha(ATTESTATION_TAG + str_bytes(status) + bsc_decision.head_bytes(justified) +
               bsc_decision.head_bytes(finalized)).hex()


def decode_blocks(raw):
    """Header fields of each block in blocks.rlp: hash, parent, number, time, difficulty, coinbase."""
    out = []
    for item in probe14.split_rlp_list_items(raw):
        header_raw = probe14.rlp_list(item)[0]
        fields = [probe14.rlp_bytes(field) for field in probe14.rlp_list(header_raw)]
        out.append({"hash": probe14.keccak256(header_raw).hex(), "parent": fields[0].hex(), "coinbase": fields[2].hex(),
                    "root": fields[3].hex(), "difficulty": int.from_bytes(fields[7], "big"),
                    "number": int.from_bytes(fields[8], "big"), "time": int.from_bytes(fields[11], "big")})
    return out


class Checks:
    """Named checks; a failed check is kept with its detail."""

    def __init__(self):
        self.rows = []

    def check(self, name, ok, detail=None):
        self.rows.append({"check": name, "ok": bool(ok), "detail": None if ok else detail})
        return bool(ok)

    def failures(self):
        return [row for row in self.rows if not row["ok"]]


def judge_blocks(c, report, raw_blocks, genesis_hash):
    blocks = decode_blocks(raw_blocks)
    c.check("blocks.rlp holds the nine probe blocks", len(blocks) == len(BLOCK_ORDER), len(blocks))
    c.check("block order", report.get("block_order") == list(BLOCK_ORDER), report.get("block_order"))
    c.check("genesis is the ProfileManifest genesis", report.get("genesis") == genesis_hash, report.get("genesis"))
    names = {genesis_hash: "G"}
    decoded = dict(zip(BLOCK_ORDER, blocks))
    for name, header in decoded.items():
        names[header["hash"]] = name
    roots = set()
    for name, header in decoded.items():
        row = report["blocks"].get(name, {})
        c.check(f"{name} hash", row.get("hash") == header["hash"], (row.get("hash"), header["hash"]))
        c.check(f"{name} parent", names.get(header["parent"]) == PARENTS[name], names.get(header["parent"]))
        c.check(f"{name} number", header["number"] == int(name[1]), header["number"])
        c.check(f"{name} sealed in turn by the validator",
                header["difficulty"] == 2 and header["coinbase"] == VALIDATOR,
                (header["difficulty"], header["coinbase"]))
        roots.add(header["root"])
        parent = decoded.get(PARENTS[name])
        if parent is not None:
            offset = 4 if name == "S3" else 3
            c.check(f"{name} time", header["time"] == parent["time"] + offset, (header["time"], parent["time"]))
    c.check("every state root differs", len(roots) == len(BLOCK_ORDER), len(roots))
    return names


def canonical_of(head):
    """The canonical names from genesis to head along the probe parents."""
    chain = []
    while head != "G":
        chain.append(head)
        head = PARENTS[head]
    return ["G"] + chain[::-1]


def judge_path(c, label, steps, genesis_head, intents):
    """The shared scenario of one path; returns the IMPORT decisions."""
    imports = []
    c.check(f"{label}: shared step names", [s["name"] for s in steps[:len(SHARED_STEPS)]] ==
            [row[0] for row in SHARED_STEPS], [s["name"] for s in steps[:len(SHARED_STEPS)]])
    sequence = 0
    for step, (name, klass, phase, head, made) in zip(steps, SHARED_STEPS):
        where = f"{label}: {name}"
        c.check(f"{where}: error class", step["class"] == klass, (step["class"], step["error"]))
        c.check(f"{where}: phase", step["phase"] == phase, step["phase"])
        c.check(f"{where}: head", step["head"] == head, step["head"])
        c.check(f"{where}: canonical chain", step["canonical"] == canonical_of(head), step["canonical"])
        c.check(f"{where}: finalized", step["finalized"] == ("zero" if name == "start (BOOTSTRAP)" else "G"),
                step["finalized"])
        c.check(f"{where}: no open observation", step["open_observations"] == [], step["open_observations"])
        if phase == "LIVE":
            c.check(f"{where}: view", step.get("view") == {"head": head, "finalized": "G"}, step.get("view"))
        else:
            c.check(f"{where}: no view before H", step.get("view") is None, step.get("view"))
        got = [(d["kind"], d["old"], d["new"], tuple(d["members"])) for d in step["new_decisions"]]
        c.check(f"{where}: decisions", got == list(made), got)
        for d in step["new_decisions"]:
            c.check(f"{where}: decision {d['sequence']} in order", d["sequence"] == sequence, d["sequence"])
            sequence = d["sequence"] + 1
            c.check(f"{where}: decision {d['sequence']} decided with its marker",
                    d["state"] == "NATIVE_DECIDED" and d["marker"] is True, (d["state"], d["marker"]))
            if d["kind"] == "IMPORT":
                imports.append(d)
                want = bsc_decision.evidence_digest("IMPORT", d["old_head"], d["new_head"], genesis_head, genesis_head)
                c.check(f"{where}: IMPORT {d['new']} evidence binds the genesis fallback (AC-LIF-06-04)",
                        d["evidence_digest"] == want, d["evidence_digest"])
            elif d["kind"] == "CANONICAL":
                caller = CANONICAL_CALLERS.get((d["old"], d["new"]))
                c.check(f"{where}: CANONICAL {d['old']}->{d['new']} evidence of {caller}",
                        caller is not None and
                        d["evidence_digest"] == canonical_evidence(caller, d["old_head"], d["new_head"]),
                        d["evidence_digest"])
                if intents is not None:
                    c.check(f"{where}: CANONICAL {d['old']}->{d['new']} intent of {caller}",
                            intents.get(d["evidence_digest"]) == caller, intents.get(d["evidence_digest"]))
    extra = {step["name"]: step.get("extra") or {} for step in steps}
    c.check(f"{label}: S3 is stored above the head with its state before the known-block step",
            extra.get(SHARED_STEPS[5][0], {}).get("s3_known_with_state") is True, extra.get(SHARED_STEPS[5][0]))
    s4 = extra.get(SHARED_STEPS[11][0], {})
    c.check(f"{label}: S4's parent S3 is stored without state (pruned parent)",
            s4.get("s3_stored") is True and s4.get("s3_has_state") is False, s4)
    return imports


def judge_attestations(c, trace, scope, imports, genesis_head):
    events = [e for e in trace if e.get("scope") == scope and e.get("event") == "attestation"]
    for e in events:
        ok = (e["status"] == "GENESIS_FALLBACK" and e["justified"] == genesis_head and e["finalized"] == genesis_head
              and e["digest"] == attestation_digest("GENESIS_FALLBACK", genesis_head, genesis_head))
        c.check(f"{scope}: attestation of block {e['number']} is GENESIS_FALLBACK with the real genesis (AC-LIF-06-04)",
                ok, e)
    c.check(f"{scope}: one attestation read per decided import",
            sorted(e["number"] for e in events) == sorted(d["new_head"]["number"] for d in imports),
            (sorted(e["number"] for e in events), sorted(d["new_head"]["number"] for d in imports)))
    c.check(f"{scope}: the post-Plato block 7 is read", 7 in [e["number"] for e in events],
            [e["number"] for e in events])
    return len(events)


def judge_profile(c, report):
    p = report["profile"]
    c.check("profile: full sync passes", p.get("full_sync") is None, p.get("full_sync"))
    for key, marker in (("snap_sync", "bsc.snap-sync"), ("vote_enable", "bsc.sealing"), ("mev_builder", "bsc.sealing"),
                        ("path_scheme", "state scheme"), ("eth_new_snap_sync", "bsc.snap-sync"),
                        ("start_without_eth_api", "ethAPI")):
        c.check(f"profile: {key} refused", isinstance(p.get(key), str) and marker in p[key], p.get(key))


def judge_coverage(c, report, callers):
    routes = {row["id"]: row["route"] for row in callers["callers"] if row["client"] == "bsc"}
    c.check("the probe's caller map is the repository map", report.get("routes") == routes, report.get("routes"))
    coverage = report["coverage"]
    gaps = []
    for caller, route in sorted(routes.items()):
        reach = coverage.get(caller, {})
        if route == "OBSERVATION" and not reach.get("passes"):
            gaps.append(caller)
        if route == "DECISION" and not reach.get("intents"):
            gaps.append(caller)
        if route == "REFUSE" and not reach.get("refusals"):
            gaps.append(caller)
    c.check("Gaps recomputed from the coverage", gaps == report.get("gaps"), (gaps, report.get("gaps")))
    c.check("the gaps are exactly the D104 rows", gaps == sorted(DEFERRED), gaps)
    known = coverage.get("bsc.known-block", {})
    c.check("the known-block passes ran and stopped without an intent",
            known.get("passes", 0) >= 2 and known.get("intents") == 0, known)
    for caller in OUTSIDE_GATE:
        c.check(f"refusal of {caller} outside the gate", isinstance(report["outside_gate_refusals"].get(caller), str),
                report["outside_gate_refusals"].get(caller))
    return routes, gaps


def judge_extras(c, report):
    steps = report["native_caller"]["steps"][report["native_caller"]["shared_steps"]:]
    c.check("native: the rest of the caller map", [s["name"] for s in steps] == [row[0] for row in NATIVE_EXTRAS],
            [s["name"] for s in steps])
    for step, (name, klass, text) in zip(steps, NATIVE_EXTRAS):
        c.check(f"native: {name} refused", step["class"] == klass and text in (step["error"] or ""),
                (step["class"], step["error"]))
        c.check(f"native: {name} leaves the head", step["head"] == "A7" and step["new_decisions"] == [],
                (step["head"], step["new_decisions"]))
    for path in ("native_caller", "harness"):
        c.check(f"{path}: every pass exit accepted", report[path]["exit_errors"] == [], report[path]["exit_errors"])
    d = report["deadlock"]
    c.check("deadlock smoke finished without errors", d.get("finished") is True and d.get("errors") == [], d)
    after = report["harness"]["steps"][report["harness"]["shared_steps"]:]
    ok = (len(after) == 1 and after[0]["head"] == "A7" and len(after[0]["new_decisions"]) == 10 and
          all((x["kind"], x["old"], x["new"], x["state"]) == ("CANONICAL", "A7", "A7", "NATIVE_DECIDED")
              for x in after[0]["new_decisions"]))
    c.check("deadlock smoke: ten SetHead decisions to the head", ok, after)


def judge_attestation_fault(c, report, trace, genesis_head):
    fault = report["attestation_fault"]
    steps = fault["steps"]
    want = (("start (BOOTSTRAP)", "", "G", (), ()),
            ("import A1 A2 (BOOTSTRAP)", "", "A2", (), ()),
            ("GoLive", "", "A2", (), ()),
            ("import A3 with the snapshot lookup error", "ErrSource", "A2", (), ("EXEC_READY A3",)),
            ("setHead 1 while A3's pin is live", "ErrLivePins", "A2", (), ("EXEC_READY A3",)),
            ("restart: recovery replays A3 without the fault", "", "A3",
             (("CANONICAL", "A2", "A2"), ("IMPORT", "A2", "A3")), ()))
    c.check("attestation fault: steps", [s["name"] for s in steps] == [w[0] for w in want], [s["name"] for s in steps])
    for step, (name, klass, head, made, open_) in zip(steps, want):
        c.check(f"attestation fault: {name}", step["class"] == klass and step["head"] == head and
                [(d["kind"], d["old"], d["new"]) for d in step["new_decisions"]] == list(made) and
                [o["state"] + " " + o["block"] for o in step["open_observations"]] == list(open_),
                (step["class"], step["head"], step["new_decisions"], step["open_observations"]))
    refused = [e for e in trace if e.get("scope") == "attestation" and e.get("event") == "attestation-refused"]
    c.check("attestation fault: one refused read, a source error (AC-LIF-06-05)",
            len(refused) == 1 and refused[0]["number"] == 3 and
            "finality evidence source error" in refused[0]["error"], refused)
    rejected = [e for e in trace if e.get("scope") == "attestation" and e.get("event") == "lifecycle-rejected"]
    intents = [e for e in trace if e.get("scope") == "attestation" and e.get("event") == "lifecycle-intent"
               and e.get("caller") == "bsc.set-head"]
    c.check("attestation fault: the refused rewind's intent is rejected at run time (head unchanged)",
            len(rejected) == 1 and len(intents) == 1 and rejected[0]["sequence"] == intents[0]["sequence"] and
            "live pins" in rejected[0]["cause"] and intents[0]["old_head"]["number"] == 2 and
            intents[0]["new_head"]["number"] == 1, (rejected, intents))
    for d in steps[5]["new_decisions"] if len(steps) > 5 else []:
        if d["kind"] == "IMPORT":
            want_digest = bsc_decision.evidence_digest("IMPORT", d["old_head"], d["new_head"], genesis_head,
                                                       genesis_head)
            c.check("attestation fault: the recovered import binds the genesis fallback",
                    d["evidence_digest"] == want_digest, d["evidence_digest"])
    read = [e for e in trace if e.get("scope") == "attestation" and e.get("event") == "attestation"]
    c.check("attestation fault: the replay reads the attestation again",
            [e["number"] for e in read] == [3] and read[0]["status"] == "GENESIS_FALLBACK", read)
    guard = fault.get("database_guard", {})
    c.check("database guard: refused while A3's pin is live",
            "live pins forbid" in str(guard.get("pinned")), guard.get("pinned"))
    c.check("database guard: refused while a decision is not released",
            "open extractor obligations" in str(guard.get("decided")), guard.get("decided"))
    c.check("database guard: a store without decisions passes",
            guard.get("fresh") is None and guard.get("fresh_import_error") is None, guard)


def judge_events(c, report):
    e = report["events"]
    errors = ("reference_error", "bootstrap_error", "golive_error", "decided_error")
    c.check("events: no import error", all(e.get(k) is None for k in errors), e)
    for name in ("A1", "A2"):
        c.check(f"events: BOOTSTRAP {name} as native", e["bootstrap"].get(name) == e["reference"].get(name),
                (e["bootstrap"].get(name), e["reference"].get(name)))
    for name in ("A3", "A4", "A5", "A6", "A7"):
        got = e["decided"].get(name)
        c.check(f"events: decided {name} sends the native events (D101)", got == e["reference"].get(name),
                (got, e["reference"].get(name)))
        c.check(f"events: decided {name} sends the verified-block event",
                got is not None and f"verified {name}" in got and f"block {name}" in got, got)
    made = [(d["kind"], d["old"], d["new"], d["state"]) for d in e["decisions"]]
    c.check("events: one IMPORT decision per decided block",
            made == [("IMPORT", f"A{i - 1}", f"A{i}", "NATIVE_DECIDED") for i in range(3, 8)], made)


def judge_crashes(c, report):
    rows = {row["mode"]: row for row in report["crashes"]}
    want = {"set-head-intended": ("A5", "rejected", "native-head-is-old-head"),
            "set-head-rewound": ("A4", "completed", "native-head-is-new-head")}
    c.check("crash modes", sorted(rows) == sorted(want), sorted(rows))
    for mode, (head, outcome, evidence) in want.items():
        row = rows.get(mode, {})
        c.check(f"crash {mode}: child exited at its point", row.get("child_exit") == 3, row.get("child_exit"))
        c.check(f"crash {mode}: restart live at {head}",
                row.get("restart_error") is None and row.get("phase") == "LIVE" and row.get("head") == head and
                row.get("view_head") == head and row.get("head_after_crash") == head, row)
        settle = [(s.get("outcome"), s.get("evidence")) for s in row.get("settle", [])]
        c.check(f"crash {mode}: the lifecycle intent is {outcome}", settle == [(outcome, evidence)], settle)


def lifecycle_intents(trace, scope):
    """Evidence digest -> caller of each lifecycle intent of scope."""
    return {e["evidence"]: e["caller"] for e in trace
            if e.get("scope") == scope and e.get("event") == "lifecycle-intent"}


def row_verdicts(routes, coverage, callers):
    """The per-row verdicts of AC-CAP-01-01 on the BSC private local slot."""
    notes = {row["id"]: row for row in callers["callers"] if row["client"] == "bsc"}
    out = {}
    for caller, route in sorted(routes.items()):
        reach = coverage.get(caller, {})
        if caller in DEFERRED:
            out[caller] = {"verdict": "DEFERRED", "reason": DEFERRED[caller]}
        elif route == "OBSERVATION":
            out[caller] = {"verdict": "PASS" if reach.get("passes") else "FAIL", "evidence": reach}
        elif route == "DECISION":
            out[caller] = {"verdict": "PASS" if reach.get("intents") else "FAIL", "evidence": reach}
        elif route == "REFUSE":
            out[caller] = {"verdict": "PASS" if reach.get("refusals") else "FAIL", "evidence": reach}
        elif notes[caller]["owner"] != "E01-02.lifecycle_seams" and notes[caller]["status"] == "PLANNED":
            out[caller] = {"verdict": "NOT_APPLICABLE", "reason": "owner " + notes[caller]["owner"]}
        else:
            out[caller] = {"verdict": "PASS", "reason": READ_ONLY_EVIDENCE[caller]}
    return out


def judge(report, trace, raw_blocks, genesis_hash, callers):
    """Judge one probe report; return the summary with the checks and the qa matrix."""
    c = Checks()
    genesis_head = {"number": 0, "hash": genesis_hash}

    def part(name, function, *args, default=None):
        """Run one part of the judge; a malformed report is a failed check, never a crash."""
        try:
            return function(*args)
        except (KeyError, TypeError, AttributeError, IndexError, ValueError) as error:
            c.check(f"{name}: well-formed report", False, f"{type(error).__name__}: {error}")
            return default

    def paths():
        native = judge_path(c, "native", report["native_caller"]["steps"], genesis_head, None)
        harness = judge_path(c, "harness", report["harness"]["steps"], genesis_head,
                             lifecycle_intents(trace, "harness"))
        c.check("AC-CAP-01-01: both paths reach the bridge with the same native result",
                report["ac_cap_01_01"]["native"] == report["ac_cap_01_01"]["harness"] and
                len(report["ac_cap_01_01"]["native"]) == len(SHARED_STEPS), None)
        return native, harness

    part("blocks", judge_blocks, c, report, raw_blocks, genesis_hash)
    part("profile", judge_profile, c, report)
    native_imports, harness_imports = part("paths", paths, default=([], []))
    reads = part("attestations", judge_attestations, c, trace, "harness", harness_imports, genesis_head, default=0)
    routes, gaps = part("coverage", judge_coverage, c, report, callers, default=({}, []))
    part("extras", judge_extras, c, report)
    part("attestation fault", judge_attestation_fault, c, report, trace, genesis_head)
    part("events", judge_events, c, report)
    part("crashes", judge_crashes, c, report)
    failures = c.failures()
    rows = part("rows", row_verdicts, routes, report.get("coverage", {}), callers, default={})
    clean = not failures
    matrix = {
        "AC-CAP-01-01": {"bsc-private-local": "FAIL" if clean else "FAIL_CHECKS",
                         "detail": "every row passes except the two rows deferred under D104" if clean else
                         "the probe checks failed", "rows": rows, "gaps": gaps},
        "AC-LIF-05-02": {"bsc-private-local": "NOT_APPLICABLE",
                         "detail": "ACCEPTANCE_CASES makes AC-LIF-05-02 ETH-only"},
        "AC-LIF-05-03": {"bsc-private-local": "NOT_APPLICABLE",
                         "detail": "ACCEPTANCE_CASES makes AC-LIF-05-03 ETH-only"},
        "AC-LIF-06-04": {"bsc-private-local": "PASS" if clean else "FAIL",
                         "detail": f"{reads} attestation reads of decided imports (blocks 3 to 7, Plato at 7): "
                                   "GENESIS_FALLBACK with the real genesis hash, bound by each IMPORT evidence digest"},
        "AC-LIF-06-05": {"bsc-private-local": "PASS" if clean else "FAIL",
                         "detail": "a native snapshot lookup error is a source error before any write; the block "
                                   "stays EXEC_READY and the restart decides it"},
    }
    for case in matrix.values():
        case["bsc-mainnet"] = case["bsc-testnet"] = "NOT_RUN"
    return {"checks": len(c.rows), "failures": failures, "imports": {"native": len(native_imports),
                                                                     "harness": len(harness_imports)},
            "qa_matrix": matrix, "result": "PASS" if clean else "FAIL"}


def judge_files(work, genesis_hash, callers_path):
    report = load(work / "report.json", 64 * 1024 * 1024)
    trace = [json.loads(line) for line in read_bytes(work / "trace.jsonl", 64 * 1024 * 1024).decode().splitlines()]
    callers = json.loads(read_bytes(callers_path, 4 * 1024 * 1024))
    return judge(report, trace, read_bytes(work / "blocks.rlp", 1024 * 1024), genesis_hash, callers)


# ---------------------------------------------------------------------------
# Measured run.


def run(tool_path, tool_sha, manifest_path, manifest_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(tool_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    tool_ref, tool = pinned_json(tool_path, tool_sha, deadline=deadline)
    require(tool.get("kind") == "bsc-lifecycle-tool", "Not a BSC lifecycle tool")
    fixture, genesis_hash = probe14.load_inputs(manifest_path, manifest_sha, tool, deadline)
    genesis_hash = genesis_hash.removeprefix("0x")
    proof = {"schema_version": 1, "kind": "bsc-lifecycle-run", "result": "FAIL", "tool": tool_ref,
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": RUN_SECONDS, "public_profile_credit": False,
             "profile": "private local BSC Parlia fixture chain 714 (BSC private local; not a public profile)",
             "fixture": {key: value for key, value in fixture.items() if key != "build_root"},
             "genesis_hash": genesis_hash, "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS_FILES, deadline)
        proof["block_sources"] = block_snapshot(output, deadline)
        callers = artifact(tool["callers"], 4 * 1024 * 1024, deadline=deadline)
        require(read_bytes(callers, 4 * 1024 * 1024) == (SHARED / "callers.json").read_bytes(),
                "The prepared caller map differs from the repository")
        root = output / "bsc"
        for name in ("inputs", "work", "scratch"):
            (root / name).mkdir(parents=True)
        proof["inputs"] = {
            "genesis.json": copy_artifact(Path(fixture["inputs"]["genesis.json"]["path"]), root / "inputs/genesis.json",
                                          fixture["inputs"]["genesis.json"], 4 * 1024 * 1024, deadline),
            "validator-keystore.json": copy_artifact(Path(tool["validator_keystore"]["path"]),
                                                     root / "inputs/validator-keystore.json",
                                                     tool["validator_keystore"], 1024 * 1024, deadline)}
        binary = artifact(tool["test_binary"], BINARY_LIMIT, deadline=deadline)
        mounts = [(binary.parent, "/tool", True), (root / "inputs", "/inputs", True), (root / "work", "/work", False),
                  (root / "scratch", "/scratch", False)]
        env = {"EXTRACTOR_LIFECYCLE_INPUTS": "/inputs", "EXTRACTOR_LIFECYCLE_WORK": "/work",
               "EXTRACTOR_LIFECYCLE_NATIVE_COMMIT": tool["native_source_commit"], "TMPDIR": "/scratch",
               "HOME": "/scratch"}
        proof["phases"] = {"execution_started": time.time()}
        stdout = runner.command(tool["compiler"], ["/tool/" + binary.name, "-test.v", "-test.count=1", "-test.run",
                                                   f"^{PROBE_TEST}$", "-test.timeout", f"{PROBE_CAP - 60}s"],
                                mounts, cap=PROBE_CAP, memory="4g", cwd="/scratch", environment=env)
        proof["phases"]["execution_finished"] = time.time()
        pins.parse_go_test(read_bytes(stdout, 16 * 1024 * 1024, deadline).decode(), [PROBE_TEST])
        proof["stdout"] = reference(stdout, deadline=deadline)
        work = root / "work"
        proof["outputs"] = {name: reference(work / name, 64 * 1024 * 1024, deadline)
                            for name in ("report.json", "trace.jsonl", "blocks.rlp")}
        summary = judge_files(work, genesis_hash, callers)
        proof["checks"], proof["failures"] = summary["checks"], summary["failures"]
        proof["qa_matrix"] = summary["qa_matrix"]
        proof["seconds_in_probe"] = load(work / "report.json", 64 * 1024 * 1024)["seconds"]
        sources_unchanged(origins, deadline)
        sources_unchanged(proof["block_sources"], deadline)
        artifact(tool["test_binary"], BINARY_LIMIT, deadline=deadline)
        require(summary["result"] == "PASS", f"{len(summary['failures'])} probe checks failed: "
                                             f"{[row['check'] for row in summary['failures'][:5]]}")

    return pins.owned_attempt(output, started, deadline, proof, "run.json", body)


def judge_retained(run_directory, output):
    """Judge a retained run again in a new directory, from its recorded outputs."""
    require(run_directory.is_absolute() and output.is_absolute(), "Paths must be absolute")
    require(not output.exists(), "The judge output already exists")
    record = load(run_directory / "run.json", 16 * 1024 * 1024)
    require(record.get("kind") == "bsc-lifecycle-run", "Not a BSC lifecycle run record")
    work = run_directory / "bsc" / "work"
    for name in ("report.json", "trace.jsonl", "blocks.rlp"):
        require(artifact(record["outputs"][name], 64 * 1024 * 1024) == work / name,
                f"The retained {name} is not the recorded one")
    tool = load(Path(record["tool"]["path"]), 16 * 1024 * 1024)
    callers = SHARED / "callers.json"
    require(reference(callers) == tool["callers"], "The repository caller map is not the one the run used")
    summary = judge_files(work, record["genesis_hash"], callers)
    output.mkdir()
    result = {"schema_version": 1, "kind": "bsc-lifecycle-judge", "result": summary["result"],
              "run": reference(run_directory / "run.json"), "outputs": record["outputs"],
              "callers": reference(callers), "checks": summary["checks"], "failures": summary["failures"],
              "qa_matrix": summary["qa_matrix"],
              "matrix_equal_to_run": summary["qa_matrix"] == record.get("qa_matrix")}
    with open(output / "judge.json", "x") as handle:
        handle.write(json.dumps(result, indent=1, sort_keys=True) + "\n")
    return result


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
    judge_parser = commands.add_parser("judge")
    judge_parser.add_argument("directory", type=Path, help="a retained run directory (with run.json)")
    judge_parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)

    def interrupt(number, _frame):
        raise InterruptedError("Interrupted by " + signal.Signals(number).name)
    for number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(number, interrupt)
    try:
        if args.command == "prepare":
            proof = prepare(args.build_manifest, args.sha256, args.output)
        elif args.command == "run":
            proof = run(args.tool, args.sha256, Path(args.profile_manifest[0]), args.profile_manifest[1], args.output)
        else:
            proof = judge_retained(args.directory, args.output)
    except (Rejected, *pins.FAILURES) as error:
        print(json.dumps({"result": "REFUSED", "error": f"{type(error).__name__}: {str(error)[:400]}"}))
        return 2
    print(json.dumps({key: proof.get(key) for key in ("result", "error", "failures")}, default=str)[:2000])
    return 0 if proof.get("result") == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
