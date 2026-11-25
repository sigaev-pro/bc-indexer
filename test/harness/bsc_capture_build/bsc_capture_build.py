#!/usr/bin/env python3
"""Build and smoke-test the capture-enabled BSC client image.

`build` extracts the accepted BSC source of the accepted BuildManifest and
applies the extractor patches 01 to 08 through the existing install path of
the lifecycle block (test/harness/fixtures/lifecycle_bsc/bsc_lifecycle.py
install: the admission patches 01 to 06, the decision patch 07 and the
lifecycle patch 08, with the strict diff applier of bsc_admission.py). It
compiles ./cmd/geth with the accepted offline compiler in the pinned compiler
image, packages the binary on the pinned runtime image as
bc-indexer/bsc-capture:<version>-capture, and writes provenance.json next to
the output. The image labels hold the same facts. `version` and `dumpconfig`
then run in the new image through the owned container boundary.

`smoke` starts the image offline on a throwaway home with a minimal capture
config (EXTRACTOR_BSC_CONFIG) and the Chapel genesis, stops it after a short
time, and runs the refusal controls that show the extractor hooks in the
start path. The commands are in README.md next to this file.

The candidate lock keeps `patched-native-images` PENDING_IMPLEMENTATION:
this is the first capture-enabled client image, not the accepted output of
E01-01.builds, and it gives no public-profile or AC credit.
"""

import argparse
import datetime
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import time
import uuid

HERE = Path(__file__).resolve().parent
HARNESS = HERE.parent
ROOT = HERE.parents[2]
sys.path.insert(0, str(HARNESS))
sys.path.insert(0, str(HARNESS / "fixtures" / "lifecycle_bsc"))
import bsc_lifecycle  # noqa: E402
import pins  # noqa: E402
import bsc_source_inputs as probe14  # noqa: E402
from artifacts import (check_deadline, copy_artifact, load, read_bytes, reference, require,  # noqa: E402
                       source_snapshot, sources_unchanged)

FAMILY = "bsc"
REPOSITORY = "bc-indexer/bsc-capture"
SUFFIX = "capture"
DOCKERFILE = HERE / "Dockerfile"
LOCK_PATH = ROOT / ".project" / "bootstrap" / "components" / "candidate-lock.json"
PATCHES = ("01-core-import-seam.diff", "02-rawdb-freezer.diff", "03-eth-recover-pruning.diff",
           "04-geth-prune-state.diff", "05-vm-safe-point.diff", "06-parlia-snapshots.diff",
           "07-core-decision-step.diff", "08-core-lifecycle-seams.diff")
GO = "/usr/local/go/bin/go"
TARGET = "./cmd/geth"
BINARY = "geth"
# The capture profile on the command line: adapters/README.md, "Interface",
# and adapters/bsc/lifecycle/native/README.md, "Node wiring". BSC selects
# snap sync by default, and the capture profile refuses every sync mode but
# full, so the flag is part of the profile.
PROFILE = ("--state.scheme", "hash", "--gcmode", "full", "--syncmode", "full")
# No peer, no listener and no IPC: the smoke start never reaches a network.
OFFLINE = ("--maxpeers", "0", "--nodiscover", "--port", "0", "--ipcdisable")
# The effective config of the profile flags in the [Eth] section of dumpconfig.
CONFIG_VALUES = {"StateScheme": "hash", "SyncMode": "full", "NoPruning": False, "PruneAncientData": False}
CHAPEL_CHAIN_ID = 97
# The lines of a start that reached the networking stack and stopped cleanly.
START_MARKERS = ("Starting BSC on Chapel testnet", "Initialised chain configuration", "Enabled full-sync",
                 "Started P2P networking", "Got interrupt, shutting down", "Blockchain stopped")
READY_MARKER = "Started P2P networking"
LABEL, OCI = "io.extractor.", "org.opencontainers.image."
HARNESS_FILES = bsc_lifecycle.HARNESS_FILES
OWN_FILES = (HERE / "bsc_capture_build.py", DOCKERFILE)
BUILD_SECONDS, SMOKE_SECONDS, IMAGE_SECONDS = 3600, 900, 900
BINARY_LIMIT, LOG_LIMIT = 1024 * 1024 * 1024, 8 * 1024 * 1024
SMOKE_STORE = "0123456789abcdef0123456789abcdef"
FOREIGN_STORE = "fedcba9876543210fedcba9876543210"
STOP_SIGNALS = (signal.SIGTERM, signal.SIGINT, signal.SIGHUP)


def tag_for(version):
    """The image tag of an upstream BSC version."""
    require(isinstance(version, str) and re.fullmatch(r"v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)", version) is not None,
            "Unsupported BSC version: " + str(version))
    return f"{REPOSITORY}:{version[1:]}-{SUFFIX}"


def lock_pins(lock_path, manifest, deadline=None):
    """The BSC, Go and image pins of the candidate lock, bound to the accepted manifest."""
    lock_ref = reference(lock_path, 4 * 1024 * 1024, deadline)
    lock = load(lock_path, 4 * 1024 * 1024, deadline)
    component, images = lock["components"]["bsc"], lock["components"]
    require(component["commit"] == manifest["builds"][FAMILY]["source_commit"],
            "The lock names another BSC commit than the accepted build")
    for name in ("golang-build-image", "debian-runtime-image"):
        require(images[name]["pull_reference"] == manifest["image_inputs"][name]["reference"],
                f"The lock image {name} differs from the accepted build")
    require(manifest["builds"][FAMILY]["compiler_image"] == images["golang-build-image"]["pull_reference"],
            "Unexpected compiler image")
    return lock_ref, {"repository": component["source_repository"], "version": component["version"],
                      "commit": component["commit"], "go_version": images["go"]["version"],
                      "compiler_image": images["golang-build-image"]["pull_reference"],
                      "runtime_image": images["debian-runtime-image"]["pull_reference"]}


def source_date_epoch(manifest_path, manifest, deadline=None):
    """The commit time of the upstream source, in seconds. The accepted build
    stored it as the creation time of its vanilla OCI configuration."""
    descriptor = manifest["builds"][FAMILY].get("oci_config")
    require(descriptor is not None, "The accepted build has no vanilla OCI configuration")
    path = Path(descriptor["path"])
    path = path if path.is_absolute() else manifest_path.parent / path
    created = json.loads(read_bytes(path, 1024 * 1024, deadline))["created"]
    require(isinstance(created, str) and created.endswith("Z"), "Malformed creation time: " + str(created))
    try:
        moment = datetime.datetime.fromisoformat(created[:-1] + "+00:00")
    except ValueError:
        raise ValueError("Malformed creation time: " + created) from None
    return int(moment.timestamp())


def image_facts(selected, patches, installed, harness_commit, go_output, binary, manifest_ref, lock_ref, epoch):
    """The provenance shared by provenance.json and the image labels."""
    return {"schema_version": 1, "kind": "bsc-capture-image-provenance", "platform": "linux/amd64",
            "upstream": {key: selected[key] for key in ("repository", "version", "commit")},
            "patches": [{"name": Path(row["patch"]["path"]).name, "sha256": row["patch"]["sha256"],
                         "size_bytes": row["patch"]["size_bytes"], "files": row["files"]} for row in patches],
            "installed": [{"path": row["relative"], "sha256": row["source"]["sha256"],
                           "size_bytes": row["source"]["size_bytes"]} for row in installed],
            "go_version_output": go_output, "go_version": selected["go_version"],
            "compiler_image": selected["compiler_image"], "runtime_image": selected["runtime_image"],
            "build_manifest_sha256": manifest_ref["sha256"], "candidate_lock_sha256": lock_ref["sha256"],
            "harness_commit": harness_commit, "source_date_epoch": epoch, "binary_sha256": binary["sha256"]}


def image_labels(facts):
    """The provenance as image labels: digests only, no local path."""
    upstream = facts["upstream"]
    name, _, digest = facts["runtime_image"].partition("@")
    return {LABEL + "source-commit": upstream["commit"], LABEL + "source-version": upstream["version"],
            LABEL + "patches": ";".join(row["name"] + "=" + row["sha256"] for row in facts["patches"]),
            LABEL + "lock-sha256": facts["candidate_lock_sha256"],
            LABEL + "build-manifest-sha256": facts["build_manifest_sha256"],
            LABEL + "harness-commit": facts["harness_commit"], LABEL + "go-version": facts["go_version"],
            LABEL + "binary-sha256": facts["binary_sha256"], LABEL + "compiler-image": facts["compiler_image"],
            LABEL + "base-image": facts["runtime_image"], OCI + "title": "bsc-capture",
            OCI + "version": upstream["version"][1:] + "-" + SUFFIX,
            OCI + "source": "https://github.com/" + upstream["repository"], OCI + "revision": upstream["commit"],
            OCI + "base.name": name, OCI + "base.digest": digest}


def version_fields(text):
    """The valued lines of `geth version` as a dict; the title and the empty
    GOPATH= and GOROOT= lines hold no colon and are skipped."""
    fields = {}
    for line in text.strip().splitlines():
        name, separator, value = line.partition(":")
        if separator:
            require(name not in fields, "Repeated version line: " + line)
            fields[name] = value.strip()
    return fields


def check_version(fields, selected):
    """The version output names the pinned upstream commit, Go version and platform."""
    expected = {"Version": selected["version"][1:], "Git Commit": selected["commit"], "Architecture": "amd64",
                "Go Version": selected["go_version"], "Operating System": "linux"}
    wrong = {key: [fields.get(key), value] for key, value in expected.items() if fields.get(key) != value}
    require(not wrong, "Version output differs from the pins: " + json.dumps(wrong, sort_keys=True))


def config_values(text, section, keys):
    """The values of keys in one TOML section of a dumpconfig output."""
    values, current = {}, None
    for line in text.splitlines():
        line = line.strip()
        if line.startswith("[") and line.endswith("]"):
            current = line[1:-1]
            continue
        name, separator, value = line.partition(" = ")
        if current == section and separator and name in keys:
            require(name not in values, "Repeated config key: " + name)
            values[name] = json.loads(value)
    return values


def check_effective_config(text):
    """The effective config of the profile flags shows the hash state scheme, full
    garbage collection, no block-data pruning and full sync."""
    values = config_values(text, "Eth", CONFIG_VALUES)
    require(values == CONFIG_VALUES, "Effective config differs from the capture profile: " + json.dumps(values))


def check_start(log, exit_code):
    """A smoke start reached the networking stack and stopped cleanly."""
    tail = log[-600:]
    require(exit_code == 0, f"The node exited with {exit_code}: {tail}")
    missing = [marker for marker in START_MARKERS if marker not in log]
    require(not missing, "The node log lacks " + ", ".join(missing) + ": " + tail)


def check_refusal(log, exit_code, marker):
    """A control start was refused, and the refusal names the extractor rule."""
    require(exit_code != 0, f"The start passed (exit 0): {log[-600:]}")
    require(marker in log, f"The refusal does not name {marker!r}: {log[-600:]}")


def build_arguments(commit):
    return [GO, "build", "-trimpath", "-buildvcs=false", "-ldflags",
            "-X github.com/ethereum/go-ethereum/internal/version.gitCommit=" + commit, "-o",
            "/work/generated/" + BINARY, TARGET]


def start_script(arguments, output, seconds=None):
    """A shell script that runs geth with its output in /work/<output>.out. With
    seconds, it waits for the ready marker, lets the node run that time, stops it
    with SIGINT and leaves its exit code in /work/<output>.exit; without, it
    leaves the exit code at once."""
    command = " ".join([BINARY, *arguments]) + f" > /work/{output}.out 2>&1"
    if seconds is None:
        return command + f"; echo $? > /work/{output}.exit"
    return "\n".join([command + " &", "pid=$!", "waited=0",
                      f'until grep -q "{READY_MARKER}" /work/{output}.out || ! kill -0 "$pid" 2>/dev/null || '
                      '[ "$waited" -ge 90 ]; do sleep 1; waited=$((waited + 1)); done',
                      f"sleep {seconds}", 'kill -INT "$pid" 2>/dev/null', 'wait "$pid"',
                      f'echo $? > /work/{output}.exit'])


def host(runner, name, argv, timeout, check=True):
    """Run one Docker CLI command on the host; its record sits with the owned commands."""
    check_deadline(runner.deadline)
    prefix = runner.output / "logs" / f"host-{len(runner.commands):03d}-{name}"
    started, code, reason, out, err = time.monotonic(), None, None, b"", b""
    try:
        done = subprocess.run(argv, capture_output=True, timeout=timeout, stdin=subprocess.DEVNULL)
        code, out, err = done.returncode, done.stdout, done.stderr
    except subprocess.TimeoutExpired as error:
        reason, out, err = "TIMEOUT", error.stdout or b"", error.stderr or b""
        runner.cleanup_complete = False  # The command may still hold Docker resources.
    prefix.with_suffix(".request.json").write_text(
        json.dumps({"argv": argv, "timeout": timeout}, sort_keys=True) + "\n")
    prefix.with_suffix(".stdout").write_bytes(out)
    prefix.with_suffix(".stderr").write_bytes(err)
    prefix.with_suffix(".result.json").write_text(json.dumps(
        {"exit_code": code, "reason": reason, "elapsed_seconds": round(time.monotonic() - started, 3)},
        sort_keys=True) + "\n")
    runner.commands.append({key: reference(prefix.with_suffix("." + suffix), LOG_LIMIT, runner.deadline)
                            for key, suffix in (("request", "request.json"), ("result", "result.json"),
                                                ("stdout", "stdout"), ("stderr", "stderr"))})
    if check:
        require(code == 0, f"Docker {name} failed (exit {code}, {reason}); inspect {prefix.name}.stderr")
    return code, out


def inspected(raw):
    """The one image of a `docker image inspect` output."""
    rows = json.loads(raw)
    require(isinstance(rows, list) and len(rows) == 1 and isinstance(rows[0], dict),
            "Docker did not describe exactly one image")
    return rows[0]


def retained(output, sources, deadline):
    """Copy files that decide the result next to the harness sources; return their references."""
    directory, origins = output / "block-code", []
    directory.mkdir()
    for source in sources:
        origin = reference(source, 1024 * 1024, deadline)
        copy_artifact(source, directory / source.name, origin, 1024 * 1024, deadline)
        origins.append(origin)
    return origins


def check_patch_set(patched):
    names = [Path(row["patch"]["path"]).name for row in patched]
    require(names == list(PATCHES), f"Applied patch set differs from 01 to 08: {names}")


def check_labels(labels, facts):
    """Every expected label of the provenance is on the image."""
    wrong = {key: [labels.get(key), value] for key, value in image_labels(facts).items()
             if labels.get(key) != value}
    require(not wrong, "Image labels differ from the provenance: " + json.dumps(wrong, sort_keys=True))


def build(manifest_path, expected_sha, output):
    started = time.monotonic()
    deadline = started + BUILD_SECONDS
    require(manifest_path.is_absolute() and output.is_absolute(), "Paths must be absolute")
    manifest_ref = reference(manifest_path, 64 * 1024 * 1024, deadline)
    require(manifest_ref["sha256"] == expected_sha, "BuildManifest digest mismatch")
    manifest = load(manifest_path, 64 * 1024 * 1024, deadline)
    require(manifest.get("evidence_kind") == "actual-build" and manifest.get("task_id") == "E01-01.builds",
            "An accepted actual build is required")
    lock_ref, selected = lock_pins(LOCK_PATH, manifest, deadline)
    require(not output.is_relative_to(manifest_path.parent), "The build cannot write into accepted build storage")
    tag, commit = tag_for(selected["version"]), selected["commit"]
    proof = {"schema_version": 1, "kind": "bsc-capture-image-build", "result": "FAIL", "build_manifest": manifest_ref,
             "candidate_lock": lock_ref, "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex,
             "native_owner_nonce": None, "deadline_seconds": BUILD_SECONDS, "public_profile_credit": False,
             "image": tag, "error": None}

    def body(output, runner):
        origins, proof["harness_sources"] = source_snapshot(output, HARNESS_FILES, deadline)
        drivers = retained(output, [*OWN_FILES, bsc_lifecycle.FIXTURE / "bsc_lifecycle.py"], deadline)
        runtime, digest = selected["runtime_image"], "@" + selected["runtime_image"].partition("@")[2]
        # The pinned images are already in Docker; nothing is pulled.
        code, raw = host(runner, "runtime-inspect", ["docker", "image", "inspect", runtime], 60)
        require(code == 0, "The pinned runtime image is not present in Docker")
        base = inspected(raw)
        require(base["Architecture"] == "amd64" and base["Os"] == "linux" and
                any(row.endswith(digest) for row in base["RepoDigests"]), "Runtime image differs from its pin")
        _, raw = host(runner, "docker-version", ["docker", "version", "--format", "{{.Server.Version}}"], 60)
        docker_version = raw.decode().strip()
        code, raw = host(runner, "previous-image", ["docker", "image", "inspect", "--format", "{{.Id}}", tag], 60,
                         check=False)
        proof["previous_image_id"] = raw.decode().strip() if code == 0 else None
        root = output / FAMILY
        root.mkdir()
        closure, copied, source = probe14.extract_source(manifest, manifest_path, root, deadline)
        require(closure["source_commit"] == commit, "Source closure names another commit")
        # The existing install path applies patches 01 to 08 and installs the packages.
        patched, installed = bsc_lifecycle.install(source, root, deadline)
        check_patch_set(patched)
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath", "work"):
            (root / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            copy_artifact(source / name, root / "metadata" / name, reference(source / name, deadline=deadline),
                          deadline=deadline)
        compiler, environment = selected["compiler_image"], pins.environment()
        mounts = probe14.build_mounts(source, manifest_path.parent / "cache" / FAMILY / "mod", root)
        formatted = runner.command(compiler, ["/usr/local/go/bin/gofmt", "-l", "extractor",
                                               *sorted({row["path"] for item in patched for row in item["files"]})],
                                   mounts, cap=300, memory="2g", cwd="/source", environment=environment)
        require(not read_bytes(formatted, LOG_LIMIT, deadline).strip(), "gofmt reports unformatted files")
        version = runner.command(compiler, [GO, "version"], mounts, cap=120, memory="512m", cwd="/source",
                                 environment=environment)
        go_output = read_bytes(version, 4096, deadline).decode().strip()
        require(go_output == "go version " + selected["go_version"] + " linux/amd64",
                "Compiler image does not run the pinned Go: " + go_output)
        arguments = build_arguments(commit)
        runner.command(compiler, arguments, mounts, cap=3000, memory="6g", cwd="/source", environment=environment)
        binary = reference(root / "generated" / BINARY, BINARY_LIMIT, deadline)
        info = runner.command(compiler, [GO, "version", "-m", "/work/generated/" + BINARY], mounts, cap=120,
                              memory="512m", cwd="/source", environment=environment)
        read_bytes(info, LOG_LIMIT, deadline)
        sources_unchanged(origins, deadline)
        sources_unchanged(drivers, deadline)
        epoch = source_date_epoch(manifest_path, manifest, deadline)
        facts = image_facts(selected, patched, installed, proof["source_commit"], go_output, binary, manifest_ref,
                            lock_ref, epoch)
        # The context holds the binary only. Its time is the source time, so the
        # layer and the image creation time do not depend on the wall clock.
        context, identity = output / "image", output / "image-id.txt"
        context.mkdir()
        copy_artifact(root / "generated" / BINARY, context / BINARY, binary, BINARY_LIMIT, deadline)
        os.chmod(context / BINARY, 0o755)
        os.utime(context / BINARY, (epoch, epoch))
        shutil.copyfile(DOCKERFILE, context / "Dockerfile")
        argv = ["docker", "build", "--platform", "linux/amd64", "--pull=false", "--network", "none",
                "--build-arg", "BASE_IMAGE=" + runtime, "--build-arg", "SOURCE_DATE_EPOCH=" + str(epoch),
                *(part for key, value in image_labels(facts).items() for part in ("--label", key + "=" + value)),
                "--iidfile", str(identity), "--tag", tag, str(context)]
        try:
            host(runner, "build", argv, IMAGE_SECONDS)
            _, raw = host(runner, "image-inspect", ["docker", "image", "inspect", tag], 60)
            image, image_id = inspected(raw), identity.read_text().strip()
            require(image["Id"] == image_id and image["Architecture"] == "amd64" and image["Os"] == "linux",
                    "Built image identity differs from its record")
            check_labels(image["Config"].get("Labels", {}), facts)
            require(image["Config"]["Entrypoint"] == ["/usr/local/bin/" + BINARY], "Image entrypoint differs")
            layers, base_layers = image["RootFS"]["Layers"], base["RootFS"]["Layers"]
            require(layers[:-1] == base_layers and len(layers) == len(base_layers) + 1,
                    "Image is not the pinned runtime image with one layer")
            # The checks run in the new image, in owned offline containers.
            version = runner.command(tag, ["version"], [], cap=120, memory="512m")
            fields = version_fields(read_bytes(version, 64 * 1024, deadline).decode())
            check_version(fields, selected)
            config = runner.command(tag, ["--chapel", *PROFILE, "--datadir", "/work/dumpconfig", "dumpconfig"],
                                    [(root / "work", "/work", False)], cap=120, memory="1g")
            text = read_bytes(config, 4 * 1024 * 1024, deadline).decode()
            check_effective_config(text)
        except BaseException:
            # A failed record never leaves its tag on an image.
            if identity.exists():
                host(runner, "remove-image", ["docker", "image", "rm", "-f", tag], 120, check=False)
            raise
        provenance = {**facts, "image": tag, "image_id": image_id, "image_layers": layers,
                      "runtime_image_id": base["Id"], "docker_server_version": docker_version,
                      "go_build_info": reference(info, deadline=deadline), "source_archive": copied,
                      "build_command": arguments, "version_output": fields,
                      "effective_config": config_values(text, "Eth", CONFIG_VALUES)}
        pins.save(output / "provenance.json", provenance)
        proof["provenance"] = reference(output / "provenance.json", deadline=deadline)

    return pins.owned_attempt(output, started, deadline, proof, "build.json", body)


def capture_config(store_id, commit):
    """A minimal valid capture config (EXTRACTOR_BSC_CONFIG) of the smoke."""
    return {"store_id": store_id, "producer_id": "bsc-capture-smoke", "run_id": "run-1",
            "native_commit": commit, "genesis_file": "/config/genesis.json"}


def smoke(image, output, seconds):
    started = time.monotonic()
    deadline = started + SMOKE_SECONDS
    require(output.is_absolute(), "Output must be absolute")
    require(re.fullmatch(r"[a-z0-9][a-z0-9._/-]*:[A-Za-z0-9_][A-Za-z0-9._-]*", image) is not None,
            "Invalid image reference")
    require(type(seconds) is int and 10 <= seconds <= 120, "Seconds must be between 10 and 120")
    proof = {"schema_version": 1, "kind": "bsc-capture-image-smoke", "result": "FAIL",
             "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex, "native_owner_nonce": None,
             "deadline_seconds": SMOKE_SECONDS, "public_profile_credit": False, "image": image,
             "seconds": seconds, "error": None}

    def body(output, runner):
        origins = retained(output, OWN_FILES[:1], deadline)
        code, raw = host(runner, "image-inspect", ["docker", "image", "inspect", image], 60)
        require(code == 0, "The image is not present in Docker")
        labels = inspected(raw)["Config"].get("Labels", {})
        commit, names = labels.get(LABEL + "source-commit"), (labels.get(LABEL + "patches") or "").split(";")
        require(commit is not None and re.fullmatch(r"[0-9a-f]{40}", commit) is not None and
                [part.partition("=")[0] for part in names] == list(PATCHES) and
                labels.get(LABEL + "binary-sha256") is not None,
                "The image is not a capture build made by this procedure")
        root = output / "smoke"
        for name in ("config", "home", "work"):
            (root / name).mkdir(parents=True)
        # The genesis of the Chapel preset comes from the image itself: it is
        # the input of the init and the genesis file of the capture config.
        genesis = runner.command(image, ["--chapel", "dumpgenesis"], [], cap=120, memory="512m")
        data = read_bytes(genesis, 16 * 1024 * 1024, deadline)
        require(json.loads(data)["config"]["chainId"] == CHAPEL_CHAIN_ID, "The image does not dump the Chapel genesis")
        (root / "config" / "genesis.json").write_bytes(data)
        config = capture_config(SMOKE_STORE, commit)
        for name, value in (("capture.json", config), ("foreign.json", {**config, "store_id": FOREIGN_STORE})):
            (root / "config" / name).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
        mounts = [(root / "config", "/config", True), (root / "home", "/home", False), (root / "work", "/work", False)]
        home = ["--chapel", "--datadir", "/home"]
        # The native init of the deployment contract, with the explicit hash scheme.
        runner.command(image, [*home, "--state.scheme", "hash", "init", "/config/genesis.json"], mounts, cap=300,
                       memory="1g")
        # The image entry point is geth; the scripts run through a shell that
        # replaces it, so the command is only the shell's own arguments.
        runner.command(image, ["-c", start_script([*home, *PROFILE, *OFFLINE], "start", seconds)], mounts,
                       cap=seconds + 240, memory="2g", environment={"EXTRACTOR_BSC_CONFIG": "/config/capture.json"},
                       entrypoint="/bin/sh")
        # A control with the default sync mode (snap) and one with another store ID.
        controls = {"snap-sync": (["--state.scheme", "hash", "--gcmode", "full", *OFFLINE], "capture.json", "bsc.snap-sync"),
                    "foreign-store": ([*PROFILE, *OFFLINE], "foreign.json", "native store identity mismatch")}
        for name, (flags, config_name, _) in controls.items():
            runner.command(image, ["-c", start_script([*home, *flags], name)], mounts, cap=240, memory="2g",
                           environment={"EXTRACTOR_BSC_CONFIG": "/config/" + config_name}, entrypoint="/bin/sh")
        runs = {name: (int(read_bytes(root / "work" / (name + ".exit"), 64, deadline).decode().strip()),
                       read_bytes(root / "work" / (name + ".out"), LOG_LIMIT, deadline).decode(errors="replace"))
                for name in ("start", *controls)}
        check_start(runs["start"][1], runs["start"][0])
        for name, (_, _, marker) in controls.items():
            check_refusal(runs[name][1], runs[name][0], marker)
        proof["start"] = {"exit_code": runs["start"][0], "output": reference(root / "work" / "start.out", deadline=deadline)}
        proof["controls"] = {name: {"exit_code": runs[name][0], "marker": marker,
                                    "output": reference(root / "work" / (name + ".out"), deadline=deadline)}
                             for name, (_, _, marker) in controls.items()}
        proof.update(config=config, genesis=reference(root / "config" / "genesis.json", deadline=deadline), labels=labels)
        sources_unchanged(origins, deadline)

    return pins.owned_attempt(output, started, deadline, proof, "smoke.json", body)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    builder = commands.add_parser("build", help="compile the patched client and package the image")
    builder.add_argument("--build-manifest", type=Path, required=True)
    builder.add_argument("--sha256", required=True)
    builder.add_argument("--output", type=Path, required=True, help="new absolute directory owned by this build")
    checker = commands.add_parser("smoke", help="start the image offline and run the refusal controls")
    checker.add_argument("--image", required=True)
    checker.add_argument("--output", type=Path, required=True, help="new absolute directory owned by this smoke")
    checker.add_argument("--seconds", type=int, default=20, help="time the node runs after its start")
    args = parser.parse_args(argv)

    def interrupt(number, _frame):
        raise InterruptedError("Interrupted by " + signal.Signals(number).name)
    for number in STOP_SIGNALS:
        signal.signal(number, interrupt)
    try:
        if args.command == "build":
            proof = build(args.build_manifest, args.sha256, args.output)
        else:
            proof = smoke(args.image, args.output, args.seconds)
    except (ValueError, OSError, KeyError, TypeError, InterruptedError, subprocess.SubprocessError) as error:
        print(json.dumps({"result": "REFUSED", "error": f"{type(error).__name__}: {str(error)[:400]}"}))
        return 2
    print(json.dumps({key: proof.get(key) for key in ("result", "error")}))
    return 0 if proof.get("result") == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
