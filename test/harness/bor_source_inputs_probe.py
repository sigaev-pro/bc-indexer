#!/usr/bin/env python3
"""Build and run the E01-02.inputs Bor probe and judge AC-CAP-04-01 to AC-CAP-04-03.

`prepare` builds the probe inside the accepted Bor source tree, offline, with
the shared source_inputs package copied in, runs that package's unit tests in
the Bor module graph, and seals the private fixture chain against the
deterministic Heimdall stub. `run` executes each scenario plan of the oracle in
the pinned runtime image, one container per scenario, and compares the result
with values computed here from the stub files and the README formulas, not
from the probe output. Preparation is not part of the measured run.
"""

import argparse
from datetime import datetime, timezone
import io
import json
from pathlib import Path
import re
import sys
import tarfile
import time
import uuid
import zlib

sys.path.insert(0, str(Path(__file__).resolve().parent))
import capture_probe
from artifacts import (Rejected, artifact, check_deadline, copy_artifact, json_artifact, load, pinned_json,
                       read_bytes, reference, require)
from capture_probe import Interrupted, block_stops, unblock_stops, write_until_done
from native import Native, new_output
import source_inputs

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = Path(__file__).with_name("fixtures") / "source_inputs_bor"
PROBE_FILES = ("main.go",)
STUB_FILES = ("stub.go", "stub_test.go")
CORE_FILES = ("inputs.go", "inputs_test.go")
BODY_FILES = ("span-0.json", "span-1.json", "span-latest.json", "status.json", "clerk-a-from-1.json", "clerk-b-from-1.json",
              "clerk-empty.json")
FIXTURE_FILES = ("expected.json", "ORACLE.md", *PROBE_FILES)
PACKAGE = "github.com/ethereum/go-ethereum/cmd/extractor-bor-inputs-probe"
PROBE_DIR = "cmd/extractor-bor-inputs-probe"
PREPARATION_SECONDS = 3600
RUN_SECONDS = 1200
MARKER_KIND = "test-only-stand-in-exec-ready"
LOG_PREFIX = "extractor-source-inputs-v1/"
CHECKPOINT_INTERVAL = 1024
MARKER_PREFIX = "extractor-test-only-stand-in-exec-ready/"


def go_environment(directory):
    return {"CGO_ENABLED": "1", "GOENV": "off", "GOOS": "linux", "GOARCH": "amd64",
            "GOCACHE": "/work/build-cache", "GOMODCACHE": "/modules", "GOPATH": "/work/gopath",
            "GOFLAGS": "-mod=readonly", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
            "GOTMPDIR": "/work/gotmp", "GOWORK": "off"}


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
        record = {"schema_version": 1, "kind": "bor-source-inputs-probe-preparation", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": state, "build_manifest": manifest_ref}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            record["fixture"] = capture_probe.copy_sources(FIXTURE_FILES, FIXTURE, output / "fixture", deadline)
            record["stub_bodies"] = capture_probe.copy_sources(BODY_FILES, FIXTURE / "stub", output / "stub", deadline)
            record["harness_code"] = capture_probe.copy_sources(
                ("bor_source_inputs_probe.py", "capture_probe.py", "source_inputs.py", "native.py", "owned.py",
                 "artifacts.py"), Path(__file__).parent, output / "harness-code", deadline)
            compiler = manifest["image_inputs"]["golang-build-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/golang@sha256:[0-9a-f]{64}", compiler), "Unpinned compiler")
            runtime = manifest["image_inputs"]["debian-runtime-image"]["reference"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
            record["compiler_image"], record["runtime_image"] = compiler, runtime
            record["bor"] = prepare_bor(manifest, root, output, native, compiler, deadline)
            record["chain"] = generate_chain(output, native, runtime, deadline)
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
    data = read_bytes(directory / "source.tar", closure["source_archive"]["size_bytes"], deadline)
    with tarfile.open(fileobj=io.BytesIO(data)) as stream:
        for member in stream:
            check_deadline(deadline)
            stream.extract(member, source, filter="data")
    del data
    probe = source / PROBE_DIR
    probe.parent.mkdir(exist_ok=True)
    row["probe_sources"] = capture_probe.copy_sources(PROBE_FILES, FIXTURE, probe, deadline)
    row["stub_sources"] = capture_probe.copy_sources(STUB_FILES, FIXTURE / "heimdallstub", probe / "heimdallstub",
                                                     deadline)
    row["core_sources"] = capture_probe.copy_sources(CORE_FILES, ROOT / "adapters/bor/source_inputs",
                                                     probe / "sourceinputs", deadline)
    for name in ("bin", "build-cache", "gopath", "gotmp"):
        (directory / name).mkdir()
    mounts = [(directory, "/work", True), (root / "cache" / "bor" / "mod", "/modules", True)]
    for name in ("bin", "build-cache", "gopath", "gotmp"):
        mounts.append((directory / name, f"/work/{name}", False))
    environment = go_environment(directory)
    native.command(compiler, ["/usr/local/go/bin/go", "mod", "verify"], mounts, cap=300, memory="4g",
                   cwd="/work/source", environment=environment)
    native.command(compiler, ["/usr/local/go/bin/go", "vet", f"./{PROBE_DIR}/..."], mounts, cap=900,
                   memory="4g", cwd="/work/source", environment=environment)
    # The shared package and the stub tests, compiled in the Bor module graph with the accepted compiler.
    stdout = native.command(compiler, ["/usr/local/go/bin/go", "test", "-race", "-count=1", "-cover", "-v",
                                       f"./{PROBE_DIR}/sourceinputs", f"./{PROBE_DIR}/heimdallstub"], mounts,
                            cap=900, memory="4g", cwd="/work/source", environment=environment)
    row["unit_tests"] = reference(stdout)
    output_text = b"\n" + stdout.read_bytes()
    for package in ("sourceinputs", "heimdallstub"):
        require(f"\nok  \t{PACKAGE}/{package}".encode() in output_text,
                f"{package} tests did not pass in the Bor module graph")
    native.command(compiler, ["/usr/local/go/bin/go", "build", "-p", "2", "-trimpath", "-o", "/work/bin/probe",
                              f"./{PROBE_DIR}"], mounts, cap=1200, memory="4g", cwd="/work/source",
                   environment=environment)
    bin_directory = directory / "bin"
    for name, origin in (("genesis.json", source / "tests/bor/testdata/genesis.json"),
                         ("helper.go", source / "tests/bor/helper.go")):
        copy_artifact(origin, bin_directory / name, reference(origin, 4 * 1024 * 1024, deadline), deadline=deadline)
    row["binary"] = reference(bin_directory / "probe", deadline=deadline)
    row["genesis"] = reference(bin_directory / "genesis.json", deadline=deadline)
    row["key_source"] = reference(bin_directory / "helper.go", deadline=deadline)
    return row


def generate_chain(output, native, runtime, deadline):
    chains = output / "chains"
    chains.mkdir()
    native.command(runtime, ["/probe/probe", "generate", "-genesis", "/probe/genesis.json", "-keysource",
                             "/probe/helper.go", "-bodies", "/stub", "-out", "/chains/a"],
                   [(output / "bor/bin", "/probe", True), (output / "stub", "/stub", True), (chains, "/chains", False)],
                   cap=600, memory="2g")
    return {"blocks": reference(chains / "a/blocks.rlp", deadline=deadline),
            "generated": reference(chains / "a/generated.json", deadline=deadline),
            "parent_header": reference(chains / "a/parent-header.rlp", deadline=deadline)}


# ---------------------------------------------------------------------------
# Independent expected values.


def to_time(timestamp, delay):
    """The state sync `to_time` query value Bor derives from a header time (bor.go CommitStates)."""
    moment = datetime.fromtimestamp(timestamp - delay, tz=timezone.utc)
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


def expected_requests(oracle, timestamps, delay=0):
    """The counted request sequence for the fixture chain, from the oracle template."""
    rows = []
    for row in oracle["requests"]:
        request = row["request"]
        if "block" in row:
            request = request.replace("{to_time}", to_time(timestamps[row["block"]], delay))
        rows.append({"request": request, "body": row["body"]})
    return rows


def expected_digests(items, requests, bodies):
    """Capsule and consumed-manifest digests from the README formulas (source_inputs.py)."""
    capsule = source_inputs.capsule_digest(items)
    chain = source_inputs.chain_start(capsule)
    entries = []
    for sequence, row in enumerate(requests):
        request, response = row["request"].encode(), bodies[row["body"]]
        entry = source_inputs.entry_digest(sequence, request, response)
        chain = source_inputs.chain_next(chain, entry)
        entries.append({"request_sha256": source_inputs.sha(request).hex(),
                        "response_sha256": source_inputs.sha(response).hex()})
    return {"capsule": capsule.hex(), "entries": entries,
            "consumed": source_inputs.consumed_digest(capsule, len(requests), chain).hex()}


def log_keys(dump, observation):
    prefix = LOG_PREFIX + observation + "/"
    return {row["key"]: row["value_sha256"] for row in dump if row["key"].startswith(prefix)}


def entry_sequences(keys, observation):
    pattern = re.compile(re.escape(LOG_PREFIX + observation) + r"/log/([0-9a-f]{16})$")
    return sorted(int(match.group(1), 16) for key in keys if (match := pattern.match(key)))


def check_rows(rows, want, origins, fail, label):
    """Compare counted calls with the expected requests, response digests and origins."""
    if len(rows) != len(origins):
        fail(f"{label}: {len(rows)} counted calls, expected {len(origins)}")
        return
    for index, (row, origin) in enumerate(zip(rows, origins)):
        if row.get("sequence") != index or row.get("request") != want[index]["request"]:
            fail(f"{label}: call {index} request differs from the expected sequence")
        elif row.get("request_sha256") != want[index]["request_sha256"]:
            fail(f"{label}: call {index} request digest differs")
        elif row.get("error"):
            fail(f"{label}: call {index} failed: {row['error']}")
        elif row.get("response_sha256") != want[index]["response_sha256"]:
            fail(f"{label}: call {index} returned bytes that differ from the saved response")
        elif row.get("origin") != origin:
            fail(f"{label}: call {index} origin {row.get('origin')}, expected {origin}")


def resumed(point, total):
    """Origins of a retry after a loss at `point`: the saved prefix replays, the tail is fetched."""
    return ["replay"] * (point + 1) + ["fetch"] * (total - point - 1)


def completed(attempt, generated, fail, label):
    if attempt.get("import_error") or attempt.get("inserted") != len(generated["blocks"]):
        fail(f"{label}: native import did not complete: {attempt.get('import_error')}")
    last = generated["blocks"][-1]
    if attempt.get("head_hash") != last["hash"] or attempt.get("head_root") != last["state_root"]:
        fail(f"{label}: imported head differs from the sealed chain")


def check_capsule(result, step, context, fail, label):
    """A logged attempt sealed the independent capsule and ran Bor with its chain config,
    except under the AC-CAP-04-02 fault, which runs with another delay on purpose."""
    capsule = result.get("capsule") or {}
    if capsule.get("items") != context["capsule_items"] or capsule.get("digest") != context["expected"]["capsule"]:
        fail(f"{label}: the sealed capsule differs from the independent items")
    if result.get("native_config_matches_capsule") is not (step["fault_state_sync_delay"] < 0):
        fail(f"{label}: native Bor did not run with the expected chain config")


def compare(name, observed, context):
    """Return the differences between one scenario result and the oracle."""
    failures = []

    def fail(message):
        failures.append(f"{name}: {message}")

    oracle, generated = context["oracle"], context["generated"]
    plan = context["plans"][name]
    observation = plan["observation"]
    attempts = observed.get("attempts") or []
    ran = {"name": plan["name"], "observation": observation, "attempts": plan["attempts"]}
    if observed.get("plan") != ran or len(attempts) != len(plan["attempts"]):
        return failures + [f"{name}: the scenario did not run the oracle plan"]
    full = context["expected"]
    requests = context["requests"]
    want = [{**entry, "request": row["request"]} for entry, row in zip(full["entries"], requests)]
    refusals = plan.get("refusals") or {}
    for index, (step, row) in enumerate(zip(plan["attempts"], attempts)):
        status, result = row["status"], row.get("result")
        if step["crash_after"] >= 0:
            if status.get("signal") != "killed" or not row.get("crash_result"):
                fail(f"attempt {index} was not lost at its crash point")
        elif str(index) in refusals:
            if status.get("exit_code") != 1 or status.get("signal"):
                fail(f"attempt {index} exited with {status}, expected a refusal")
        elif status.get("exit_code") != 0 or status.get("signal"):
            fail(f"attempt {index} exited with {status}")
        if result is None and row.get("crash_result") is None:
            fail(f"attempt {index} left no result")
    if failures:
        return failures
    for index, (step, row) in enumerate(zip(plan["attempts"], attempts)):
        if step["log"] and str(index) not in refusals:
            check_capsule(row.get("result") or row["crash_result"], step, context, fail, f"attempt {index}")
    kind = plan["kind"]
    if kind == "replay-after-loss":
        lost, retry = attempts[0], attempts[1]
        point = plan["attempts"][0]["crash_after"]
        crash = lost["crash_result"]
        check_rows(crash["counted_calls"], want,
                   ["fetch"] * (point + 1), fail, "lost attempt")
        if entry_sequences(log_keys(lost["store_after"], observation), observation) != list(range(point + 1)):
            fail("the lost attempt did not leave exactly the saved prefix durable")
        if retry["result"].get("frontier_at_open") != point + 1:
            fail("the retry did not open at the durable frontier")
        check_rows(retry["result"]["counted_calls"], want,
                   resumed(point, len(want)), fail, "retry")
        changed = context["body_sha256"][plan["changed_body"]]
        if any(r.get("response_sha256") == changed for r in retry["result"]["counted_calls"]):
            fail("the changed external response reached Bor")
        completed(retry["result"], generated, fail, "retry")
        if (retry["result"].get("consumed") or {}).get("digest") != full["consumed"]:
            fail("the final consumed-manifest digest differs from the independent value")
    elif kind == "prefix-conflict":
        lost, conflict, reopened = attempts[0], attempts[1], attempts[2]
        point = plan["attempts"][0]["crash_after"]
        offset = plan["conflict_offset"]
        delayed = expected_requests(oracle, context["timestamps"], plan["attempts"][1]["fault_state_sync_delay"])
        if delayed[offset]["request"] == requests[offset]["request"] or offset > point:
            fail("the oracle conflict offset does not hold a different request inside the saved prefix")
        rows = conflict["result"]["counted_calls"]
        check_rows(rows[:offset], want,
                   ["replay"] * offset, fail, "conflicting retry")
        if len(rows) != offset + 1 or rows[offset].get("request") != delayed[offset]["request"] or \
                "dependency log prefix conflict" not in (rows[offset].get("error") or ""):
            fail("the different request at the saved offset was not refused with a prefix conflict")
        if conflict["result"].get("prefix_conflict") is not True or not conflict["result"].get("import_error"):
            fail("native import did not fail with the prefix conflict")
        if conflict["result"].get("consumed") or conflict["result"].get("stand_in_marker"):
            fail("a refused retry closed the log or wrote a marker")
        if conflict["store_after"] != conflict["store_before"] or conflict["store_before"] != lost["store_after"]:
            fail("the refused retry changed the store")
        if reopened["result"].get("frontier_at_open") != point + 1:
            fail("the reopened log did not keep the saved frontier")
        check_rows(reopened["result"]["counted_calls"], want,
                   resumed(point, len(want)), fail, "reopened retry")
        completed(reopened["result"], generated, fail, "reopened retry")
    elif kind == "tail-append":
        lost, retry = attempts[0], attempts[1]
        point = plan["attempts"][0]["crash_after"]
        prefix = log_keys(lost["store_after"], observation)
        if entry_sequences(prefix, observation) != list(range(point + 1)):
            fail("the lost attempt did not leave exactly the saved prefix durable")
        result = retry["result"]
        if result.get("frontier_at_open") != point + 1:
            fail("the retry did not open at the durable frontier")
        check_rows(result["counted_calls"], want,
                   resumed(point, len(want)), fail, "retry")
        after = log_keys(retry["store_after"], observation)
        frontier = LOG_PREFIX + observation + "/frontier"
        for key, value in prefix.items():
            if key != frontier and after.get(key) != value:
                fail(f"the saved prefix key {key} changed")
        if entry_sequences(after, observation) != list(range(len(want))):
            fail("the tail was not appended beyond the durable frontier")
        completed(result, generated, fail, "retry")
        consumed = result.get("consumed") or {}
        marker = result.get("stand_in_marker") or {}
        if consumed.get("digest") != full["consumed"] or consumed.get("count") != len(want) or \
                consumed.get("capsule_digest") != full["capsule"]:
            fail("the final consumed manifest differs from the independent value")
        if marker != {"kind": MARKER_KIND, "observation": observation, "consumed_manifest_digest": full["consumed"]}:
            fail("the stand-in marker does not bind the final consumed-manifest digest")
        if not any(row["key"] == MARKER_PREFIX + observation for row in retry["store_after"]):
            fail("the stand-in marker is not in the store")
    elif kind == "capsule-controls":
        for index, row in enumerate(attempts):
            result = row["result"]
            if str(index) in refusals:
                if refusals[str(index)] not in (result.get("error") or ""):
                    fail(f"attempt {index} was not refused with {refusals[str(index)]!r}: {result.get('error')}")
                if result.get("capsule") or result.get("counted_calls") or result.get("inserted") or \
                        result.get("consumed") or result.get("stand_in_marker"):
                    fail(f"attempt {index} sealed, fetched or imported before its refusal")
                if row["store_after"] != row["store_before"]:
                    fail(f"attempt {index} changed the store")
            else:
                completed(result, generated, fail, f"attempt {index}")
                origins = ["fetch"] * len(want) if result.get("frontier_at_open") == 0 else ["replay"] * len(want)
                check_rows(result["counted_calls"], want, origins, fail, f"attempt {index}")
                if result.get("stand_in_marker") != {"kind": MARKER_KIND, "observation": observation,
                                                     "consumed_manifest_digest": full["consumed"]}:
                    fail(f"attempt {index} did not bind the independent consumed digest")
        if attempts[0]["store_before"] != [] or str(0) not in refusals:
            fail("the first control did not refuse an item on an empty store")
        if [step["tamper"] for step in plan["attempts"] if step["tamper"]] != plan["tampered"]:
            fail("the controls do not tamper every derived capsule item")
    elif kind == "control-changed-response":
        result = attempts[0]["result"]
        if not result.get("import_error") or result.get("inserted") != plan["expected_inserted"]:
            fail("without the log, the changed response did not make native import fail")
    elif kind == "control-delay":
        result = attempts[0]["result"]
        delayed = expected_requests(oracle, context["timestamps"], plan["attempts"][0]["state_sync_delay"])
        if [row.get("request") for row in result["counted_calls"]] != [row["request"] for row in delayed]:
            fail("without the log, Bor did not build the delayed request sequence")
        completed(result, generated, fail, "control")
    else:
        fail(f"unknown scenario kind {kind}")
    return failures


def checked_compare(name, observed, context):
    try:
        return compare(name, observed, context)
    except (AttributeError, IndexError, KeyError, TypeError, ValueError) as error:
        return [f"{name}: malformed observation: {type(error).__name__}: {error}"]


def rlp_item(data, offset):
    """Return (item, next offset) of one RLP item: bytes, or a list of items."""
    require(offset < len(data), "Truncated RLP")
    first = data[offset]
    if first < 0x80:
        return data[offset:offset + 1], offset + 1
    if first < 0xb8:
        start, length = offset + 1, first - 0x80
    elif first < 0xc0:
        size = first - 0xb7
        start, length = offset + 1 + size, int.from_bytes(data[offset + 1:offset + 1 + size], "big")
    elif first < 0xf8:
        start, length = offset + 1, first - 0xc0
    else:
        size = first - 0xf7
        start, length = offset + 1 + size, int.from_bytes(data[offset + 1:offset + 1 + size], "big")
    end = start + length
    require(end <= len(data), "Truncated RLP")
    if first < 0xc0:
        return data[start:end], end
    items, position = [], start
    while position < end:
        item, position = rlp_item(data, position)
        items.append(item)
    require(position == end, "Malformed RLP list")
    return items, end


def rlp_encode(value):
    """RLP encoding of bytes or a list of values; for a canonical item it returns the original bytes."""
    if isinstance(value, list):
        payload, short, long = b"".join(rlp_encode(item) for item in value), 0xc0, 0xf7
    elif len(value) == 1 and value[0] < 0x80:
        return bytes(value)
    else:
        payload, short, long = bytes(value), 0x80, 0xb7
    if len(payload) < 56:
        return bytes([short + len(payload)]) + payload
    size = len(payload).to_bytes((len(payload).bit_length() + 7) // 8, "big")
    return bytes([long + len(size)]) + size + payload


KECCAK_ROUND_CONSTANTS = (
    0x0000000000000001, 0x0000000000008082, 0x800000000000808A, 0x8000000080008000, 0x000000000000808B,
    0x0000000080000001, 0x8000000080008081, 0x8000000000008009, 0x000000000000008A, 0x0000000000000088,
    0x0000000080008009, 0x000000008000000A, 0x000000008000808B, 0x800000000000008B, 0x8000000000008089,
    0x8000000000008003, 0x8000000000008002, 0x8000000000000080, 0x000000000000800A, 0x800000008000000A,
    0x8000000080008081, 0x8000000000008080, 0x0000000080000001, 0x8000000080008008)
# KECCAK_ROTATIONS[x][y] is the rho offset of lane (x, y).
KECCAK_ROTATIONS = ((0, 36, 3, 41, 18), (1, 44, 10, 45, 2), (62, 6, 43, 15, 61), (28, 55, 25, 21, 56),
                    (27, 20, 39, 8, 14))
LANE = (1 << 64) - 1


def rotate(value, count):
    return ((value << count) | (value >> (64 - count))) & LANE


def keccak_f(lanes):
    """The Keccak-f[1600] permutation; lanes[x][y] are 64-bit integers."""
    for constant in KECCAK_ROUND_CONSTANTS:
        parity = [lanes[x][0] ^ lanes[x][1] ^ lanes[x][2] ^ lanes[x][3] ^ lanes[x][4] for x in range(5)]
        mix = [parity[(x - 1) % 5] ^ rotate(parity[(x + 1) % 5], 1) for x in range(5)]
        moved = [[0] * 5 for _ in range(5)]
        for x in range(5):
            for y in range(5):
                moved[y][(2 * x + 3 * y) % 5] = rotate(lanes[x][y] ^ mix[x], KECCAK_ROTATIONS[x][y])
        lanes = [[moved[x][y] ^ (~moved[(x + 1) % 5][y] & moved[(x + 2) % 5][y]) for y in range(5)]
                 for x in range(5)]
        lanes[0][0] ^= constant
    return lanes


def keccak256(data):
    """Ethereum Keccak-256 (original Keccak padding, not SHA3-256); hashlib has no Keccak."""
    rate = 136
    padded = bytearray(data) + b"\x01"
    padded += bytes(-len(padded) % rate)
    padded[-1] |= 0x80
    lanes = [[0] * 5 for _ in range(5)]
    for start in range(0, len(padded), rate):
        for index in range(rate // 8):
            offset = start + 8 * index
            lanes[index % 5][index // 5] ^= int.from_bytes(padded[offset:offset + 8], "little")
        lanes = keccak_f(lanes)
    return b"".join(lanes[index % 5][index // 5].to_bytes(8, "little") for index in range(4))


def decoded_header(data):
    """The fields of one encoded header; the encoding must be canonical."""
    # Re-encoding to the same bytes also rules out trailing bytes.
    header, _ = rlp_item(data, 0)
    require(isinstance(header, list) and len(header) > 12 and rlp_encode(header) == data,
            "Malformed header")
    return header


def sealed_headers(blocks):
    """Number, time, state root, parent hash and hash of each block in a blocks.rlp stream, decoded here."""
    rows, offset = [], 0
    while offset < len(blocks):
        block, offset = rlp_item(blocks, offset)
        require(isinstance(block, list) and block and isinstance(block[0], list) and len(block[0]) > 11,
                "Malformed block")
        header = block[0]
        rows.append({"number": int.from_bytes(header[8], "big"), "timestamp": int.from_bytes(header[11], "big"),
                     "state_root": "0x" + header[3].hex(), "parent_hash": "0x" + header[0].hex(),
                     "hash": "0x" + keccak256(rlp_encode(header)).hex()})
    require(rows, "No sealed blocks")
    return rows


def chain_config(genesis, delay):
    """The chain-config item: the genesis config with the state sync delay, as canonical JSON."""
    config = json.loads(genesis)["config"]
    require(isinstance(config, dict) and isinstance(config.get("bor"), dict), "The genesis has no Bor config")
    require("stateSyncConfirmationDelay" not in config["bor"], "The fixture genesis already sets the delay")
    config["bor"]["stateSyncConfirmationDelay"] = {"0": delay}
    return json.dumps(config, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


def fork_id(config, genesis_hash, head):
    """The EIP-2124 fork ID item at block `head` (core/forkid NewID). Bor's chain config has block forks only."""
    forks = sorted({value for key, value in config.items()
                    if key.endswith("Block") and type(value) is int and value > 0})
    checksum, following = zlib.crc32(genesis_hash), 0
    for fork in forks:
        if fork > head:
            following = fork
            break
        checksum = zlib.crc32(fork.to_bytes(8, "big"), checksum)
    return json.dumps({"hash": checksum.to_bytes(4, "big").hex(), "next": str(following)}, sort_keys=True,
                      separators=(",", ":")).encode()


def capsule_items(oracle, genesis, blocks, headers, parent, commit):
    """The expected capsule items, in the oracle order, from the fixture inputs and the sealed chain."""
    fields = decoded_header(parent)
    document = json.loads(genesis)
    parent_hash = keccak256(parent)
    require(int.from_bytes(fields[8], "big") == 0 and "0x" + parent_hash.hex() == headers[0]["parent_hash"],
            "The parent header is not the parent of the first sealed block")
    extra = document.get("extraData") or ""
    require(int.from_bytes(fields[11], "big") == int(document["timestamp"], 16) and
            int.from_bytes(fields[9], "big") == int(document["gasLimit"], 16) and
            fields[12] == bytes.fromhex(extra.removeprefix("0x")), "The parent header differs from the genesis file")
    config = chain_config(genesis, oracle["state_sync_delay"])
    items = (("genesis", genesis), ("blocks", blocks), ("parent-header", parent), ("parent-hash", parent_hash),
             ("parent-root", fields[3]), ("ancestor-headers", rlp_encode([fields])),
             ("chain-config", config), ("fork-id", fork_id(json.loads(config), parent_hash, headers[-1]["number"])),
             ("native-commit", commit.encode()), ("normalization-profile", oracle["normalization_profile"].encode()))
    require([name for name, _ in items] == oracle["capsule_items"], "The oracle capsule items differ")
    return items


def build_context(oracle, generated, bodies, genesis, blocks, commit, parent):
    # Header times, state roots and hashes come from the sealed blocks, decoded and
    # hashed here; the generation record must agree with them.
    headers = sealed_headers(blocks)
    fields = ("number", "timestamp", "state_root", "hash")
    require([{key: row[key] for key in fields} for row in generated["blocks"]] ==
            [{key: row[key] for key in fields} for row in headers], "The generation record differs from the sealed blocks")
    require(all(row["parent_hash"] == previous["hash"] for previous, row in zip(headers, headers[1:])),
            "The sealed blocks are not one chain")
    timestamps = {row["number"]: row["timestamp"] for row in headers}
    requests = expected_requests(oracle, timestamps)
    items = capsule_items(oracle, genesis, blocks, headers, parent, commit)
    refs = [{"name": name, "length": len(data), "sha256": source_inputs.sha(data).hex()} for name, data in items]
    return {"oracle": oracle, "generated": generated, "timestamps": timestamps, "requests": requests,
            "plans": {plan["name"]: plan for plan in oracle["scenarios"]}, "capsule_items": refs,
            "expected": expected_digests(items, requests, bodies),
            "body_sha256": {name: source_inputs.sha(data).hex() for name, data in bodies.items()}}


def load_scenario(directory, deadline):
    scenario = load(directory / "scenario.json", 64 * 1024 * 1024, deadline)
    for index, row in enumerate(scenario.get("attempts") or []):
        for key, name in (("result", f"attempt-{index}.json"), ("crash_result", f"attempt-{index}.json.crash.json")):
            path = directory / name
            row[key] = load(path, 16 * 1024 * 1024, deadline) if path.exists() else None
    return scenario


def run(preparation_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + RUN_SECONDS
    require(preparation_path.is_absolute(), "Preparation path must be absolute")
    preparation_ref, preparation = pinned_json(preparation_path, expected_sha, 16 * 1024 * 1024, deadline)
    require(preparation.get("kind") == "bor-source-inputs-probe-preparation" and preparation.get("result") == "PASS"
            and preparation.get("cleanup_complete") is True, "A passing preparation is required")
    manifest_root = Path(preparation["build_manifest"]["path"]).parent
    capture_probe.refuse_inside(output, (preparation_path.parent, manifest_root, ROOT))
    state = capture_probe.source_state()
    with capture_probe.single_writer():
        block_stops()
        output = new_output(output)
        native = None
        record = {"schema_version": 1, "kind": "bor-source-inputs-probe-run", "result": "FAIL",
                  "run_id": uuid.uuid4().hex, "source": state, "preparation": preparation_ref,
                  "profile": "private local Bor fixture chain 15001 (not a public profile)",
                  "scenarios": {}, "phases": {"admission_started": time.time()}}
        try:
            unblock_stops()
            native = Native(output, deadline)
            record["native_owner_nonce"] = native.nonce
            oracles = [row["copy"] for row in preparation["fixture"] if row["copy"]["path"].endswith("/expected.json")]
            require(len(oracles) == 1, "The preparation must retain exactly one oracle")
            _, oracle = json_artifact(oracles[0], 1024 * 1024, deadline=deadline)
            record["oracle"] = oracles[0]
            runtime = preparation["runtime_image"]
            require(re.fullmatch(r"docker.io/library/debian@sha256:[0-9a-f]{64}", runtime), "Unpinned runtime image")
            bodies = {}
            for row in preparation["stub_bodies"]:
                path = artifact(row["copy"], 1024 * 1024, deadline=deadline)
                bodies[path.name] = read_bytes(path, 1024 * 1024, deadline)
            bor = preparation["bor"]
            binary = artifact(bor["binary"], 512 * 1024 * 1024, deadline=deadline)
            genesis = read_bytes(artifact(bor["genesis"], 1024 * 1024, deadline=deadline), 1024 * 1024, deadline)
            artifact(bor["key_source"], 4 * 1024 * 1024, deadline=deadline)
            blocks_path = artifact(preparation["chain"]["blocks"], 64 * 1024 * 1024, deadline=deadline)
            blocks = read_bytes(blocks_path, 64 * 1024 * 1024, deadline)
            _, generated = json_artifact(preparation["chain"]["generated"], 16 * 1024 * 1024, deadline=deadline)
            parent = read_bytes(artifact(preparation["chain"]["parent_header"], 1024 * 1024, deadline=deadline),
                                1024 * 1024, deadline)
            context = build_context(oracle, generated, bodies, genesis, blocks, bor["source_commit"], parent)
            record["expected"] = {"requests": context["requests"], "capsule_items": context["capsule_items"],
                                  **context["expected"]}
            record["phases"]["admission_finished"] = time.time()
            plans = output / "plans"
            plans.mkdir()
            record["phases"]["execution_started"] = time.time()
            for plan in oracle["scenarios"]:
                name = plan["name"]
                plan_path = plans / f"{name}.json"
                plan_path.write_text(json.dumps(plan, indent=2) + "\n")
                scenario_started = time.monotonic()
                native.command(runtime,
                               ["/probe/probe", "scenario", "-plan", f"/plans/{name}.json", "-genesis",
                                "/probe/genesis.json", "-chains", "/chains", "-bodies", "/stub", "-commit",
                                bor["source_commit"], "-out", f"/out/{name}"],
                               [(binary.parent, "/probe", True), (blocks_path.parent.parent, "/chains", True),
                                (Path(preparation["stub_bodies"][0]["copy"]["path"]).parent, "/stub", True),
                                (plans, "/plans", True), (output, "/out", False)],
                               cap=600, memory="2g")
                observed = load_scenario(output / name, deadline)
                failures = checked_compare(name, observed, context)
                record["scenarios"][name] = {"ac": plan["ac"], "scenario": reference(output / name / "scenario.json"),
                                             "failures": failures,
                                             "elapsed_seconds": time.monotonic() - scenario_started,
                                             "result": "PASS" if not failures else "FAIL"}
            record["phases"]["execution_finished"] = time.time()
            # An oracle without scenarios proves nothing, so it cannot pass.
            if record["scenarios"] and all(row["result"] == "PASS" for row in record["scenarios"].values()):
                record["result"] = "PASS"
        except (Interrupted, Exception) as error:
            record["error"] = f"{type(error).__name__}: {error}"
        finally:
            write_until_done(output / "run-record.json", record, native, started)
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
    except (Interrupted, Exception) as error:
        print(json.dumps({"valid": False, "error": f"{type(error).__name__}: {error}"}))
        return 2
    print(json.dumps({"result": record["result"], "output": str(path), "error": record.get("error")}))
    return 0 if record["result"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
