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

sys.path.insert(0, str(Path(__file__).resolve().parent))
import capture_probe
from artifacts import (Rejected, artifact, check_deadline, copy_artifact, json_artifact, load, pinned_json,
                       read_bytes, reference, require)
from capture_probe import Interrupted, block_stops, unblock_stops, write_until_done
from native import Native, new_output
import source_inputs

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = Path(__file__).with_name("fixtures") / "bor_source_inputs"
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
            "generated": reference(chains / "a/generated.json", deadline=deadline)}


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
    for index, (step, row) in enumerate(zip(plan["attempts"], attempts)):
        status, result = row["status"], row.get("result")
        if step["crash_after"] >= 0:
            if status.get("signal") != "killed" or not row.get("crash_result"):
                fail(f"attempt {index} was not lost at its crash point")
        elif status.get("exit_code") != 0 or status.get("signal"):
            fail(f"attempt {index} exited with {status}")
        if result is None and row.get("crash_result") is None:
            fail(f"attempt {index} left no result")
    if failures:
        return failures
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
        delayed = expected_requests(oracle, context["timestamps"], plan["attempts"][1]["state_sync_delay"])
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


def build_context(oracle, generated, bodies, genesis, blocks, commit):
    timestamps = {row["number"]: row["timestamp"] for row in generated["blocks"]}
    requests = expected_requests(oracle, timestamps)
    items = (("genesis", genesis), ("blocks", blocks), ("bor-source-commit", commit.encode()))
    return {"oracle": oracle, "generated": generated, "timestamps": timestamps, "requests": requests,
            "plans": {plan["name"]: plan for plan in oracle["scenarios"]},
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
            context = build_context(oracle, generated, bodies, genesis, blocks, bor["source_commit"])
            record["expected"] = {"requests": context["requests"], **context["expected"]}
            plans = output / "plans"
            plans.mkdir()
            record["phases"]["execution_started"] = time.time()
            for plan in oracle["scenarios"]:
                name = plan["name"]
                plan_path = plans / f"{name}.json"
                plan_path.write_text(json.dumps(plan, indent=2) + "\n")
                scenario_started = time.monotonic()
                native.command(preparation["runtime_image"],
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
            if len(record["scenarios"]) == len(oracle["scenarios"]) and \
                    all(row["result"] == "PASS" for row in record["scenarios"].values()):
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
