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
the output. The image labels hold the same facts. `version`, `dumpconfig`
and `ldd` then run in the new image through the owned container boundary.

A variant adds an overlay on top of patches 01 to 08 (extra packages and
patches, for the in-node collector) and can link a C library whose header
and library directories are build arguments (for RocksDB). The variant
itself is later work; the procedure only carries, records and applies it.

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
import hashlib
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
import bsc_admission  # noqa: E402
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
# A variant with shared libraries adds them to the image; ldconfig makes them visible.
LIBRARIES = "COPY libs/ /usr/local/lib/\nRUN ldconfig\n"
# The header and library directories of a variant are read-only mounts of the
# compiler container at these paths, which the cgo build arguments name.
CGO_MOUNTS = {"include": "/cgo/include", "library": "/cgo/library"}
BUILD_SECONDS, SMOKE_SECONDS, IMAGE_SECONDS = 3600, 900, 900
BINARY_LIMIT, LOG_LIMIT, OVERLAY_LIMIT = 1024 * 1024 * 1024, 8 * 1024 * 1024, 16 * 1024 * 1024
SMOKE_STORE = "0123456789abcdef0123456789abcdef"
FOREIGN_STORE = "fedcba9876543210fedcba9876543210"
STOP_SIGNALS = (signal.SIGTERM, signal.SIGINT, signal.SIGHUP)


def tag_for(version, variant=None):
    """The image tag of an upstream BSC version, with the name of a variant at its end."""
    require(isinstance(version, str) and re.fullmatch(r"v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)", version) is not None,
            "Unsupported BSC version: " + str(version))
    return f"{REPOSITORY}:{version[1:]}-{SUFFIX}" + ("-" + variant["name"] if variant else "")


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


def tree_reference(path, deadline=None):
    """A reference of a directory: SHA-256 over the sorted relative paths and the
    file digests under it."""
    hasher, files, size = hashlib.sha256(), 0, 0
    for item in sorted(part for part in path.rglob("*") if part.is_file()):
        row = reference(item, BINARY_LIMIT, deadline)
        hasher.update(f"{item.relative_to(path).as_posix()} {row['sha256']}\n".encode())
        files, size = files + 1, size + row["size_bytes"]
    require(files, f"Empty directory: {path}")
    return {"path": str(path), "sha256": hasher.hexdigest(), "files": files, "size_bytes": size}


def variant_of(name, patches, packages, include, library, link, runtime_libraries, deadline=None):
    """The variant of a build, or None for the base build.

    An overlay holds extra packages (SOURCE=TARGET: a file or a directory of the
    repository, installed at a new path of the source tree) and extra patches,
    applied after patches 01 to 08. A C library holds header and library
    directories, linker flags, and the shared libraries the runtime image
    needs when the binary links them dynamically.
    """
    given = [*patches, *packages, *include, *library, *link, *runtime_libraries]
    require(name or not given, "A variant needs --variant NAME")
    require(not name or given, "--variant NAME needs an overlay or a C library input")
    if not name:
        return None
    require(re.fullmatch(r"[a-z0-9][a-z0-9-]{0,31}", name) is not None, "Invalid variant name: " + name)

    def resolve(item, label):
        path = Path(item)
        path = path if path.is_absolute() else ROOT / path
        require(path.exists() and not path.is_symlink(), f"Missing {label}: {item}")
        return path

    patch_rows, seen = [], set(PATCHES)
    for item in patches:
        path = resolve(item, "extra patch")
        require(path.is_file() and path.suffix == ".diff" and path.name not in seen,
                f"Extra patch is not a new .diff file: {item}")
        seen.add(path.name)
        row = reference(path, OVERLAY_LIMIT, deadline)
        patch_rows.append({"path": row["path"], "name": path.name, "sha256": row["sha256"],
                           "size_bytes": row["size_bytes"]})
    package_rows, targets = [], []
    for item in packages:
        source, separator, target = item.partition("=")
        require(separator and target and not target.startswith("/") and "\\" not in target and
                all(part not in ("", ".", "..") for part in target.split("/")), f"Invalid overlay target: {item}")
        require(target not in targets and not any(target.startswith(other + "/") or other.startswith(target + "/")
                                                  for other in targets), f"Overlay targets overlap: {item}")
        targets.append(target)
        path = resolve(source, "overlay package")
        row = reference(path, OVERLAY_LIMIT, deadline) if path.is_file() else tree_reference(path, deadline)
        package_rows.append({"path": target, "source": row})
    trees = {}
    for kind, items in (("include", include), ("library", library)):
        trees[kind] = []
        for item in items:
            path = Path(item)
            require(path.is_absolute() and path.is_dir(), f"--cgo-{kind} needs an absolute directory: {item}")
            trees[kind].append(tree_reference(path, deadline))
    for flag in link:
        require(re.fullmatch(r"-[^\s]+", flag) is not None, f"Invalid linker flag: {flag}")
    shared = [reference(resolve(item, "runtime library"), BINARY_LIMIT, deadline) for item in runtime_libraries]
    return {"schema_version": 1, "kind": "bsc-capture-image-variant", "name": name, "patches": patch_rows,
            "packages": package_rows, "cgo": {"include": trees["include"], "library": trees["library"], "link": list(link)},
            "runtime_libraries": shared}


def apply_variant(source, root, variant, deadline=None):
    """Install the overlay of a variant on top of patches 01 to 08. Its packages
    never replace a native or an installed file. Its patches follow, with the
    strict diff applier of the harness, so they can change what the packages
    installed. Returns the overlay patch rows."""
    kept = root / "overlay"
    for row in variant["packages"]:
        origin, target = Path(row["source"]["path"]), source / row["path"]
        require(not target.exists() and not target.is_symlink(), f"Overlay target already exists in the native tree: {row['path']}")
        items = [(origin, target)] if origin.is_file() else [
            (item, target / item.relative_to(origin)) for item in sorted(origin.rglob("*")) if item.is_file()]
        for item, destination in items:
            expected = reference(item, OVERLAY_LIMIT, deadline)
            retained = kept / "packages" / destination.relative_to(source)
            for path in (destination, retained):
                path.parent.mkdir(parents=True, exist_ok=True)
                copy_artifact(item, path, expected, OVERLAY_LIMIT, deadline)
    rows = []
    for row in variant["patches"]:
        origin, retained = Path(row["path"]), kept / "patches" / row["name"]
        retained.parent.mkdir(parents=True, exist_ok=True)
        expected = {key: row[key] for key in ("path", "sha256", "size_bytes")}
        copy_artifact(origin, retained, expected, OVERLAY_LIMIT, deadline)
        rows.append({"patch": expected, "files": bsc_admission.apply_patch(source, retained.read_text())})
    return rows


def variant_mounts(variant):
    """Read-only mounts of the header and library directories of a variant."""
    if variant is None:
        return []
    return [(Path(row["path"]), f"{CGO_MOUNTS[kind]}/{index}", True)
            for kind in ("include", "library") for index, row in enumerate(variant["cgo"][kind])]


def variant_environment(variant):
    """The cgo build arguments of a variant: the mounted header and library
    directories and the linker flags."""
    if variant is None:
        return {}
    environment = {}
    if variant["cgo"]["include"]:
        environment["CGO_CFLAGS"] = " ".join(f"-I{CGO_MOUNTS['include']}/{index}"
                                             for index in range(len(variant["cgo"]["include"])))
    if variant["cgo"]["library"] or variant["cgo"]["link"]:
        environment["CGO_LDFLAGS"] = " ".join([f"-L{CGO_MOUNTS['library']}/{index}"
                                               for index in range(len(variant["cgo"]["library"]))]
                                              + variant["cgo"]["link"])
    return environment


def dockerfile(variant):
    """The Dockerfile of a build. A variant with shared libraries adds them to
    /usr/local/lib of the image. The effective file is written into the context,
    so the record holds what the image was built from."""
    text = DOCKERFILE.read_text()
    if variant and variant["runtime_libraries"]:
        anchor = "COPY --chmod=0755 geth /usr/local/bin/geth\n"
        require(text.count(anchor) == 1, "Dockerfile lost its binary line")
        text = text.replace(anchor, anchor + LIBRARIES)
    return text


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


def image_facts(selected, patched, installed, harness_commit, go_output, binary, manifest_ref, lock_ref, epoch,
                variant=None):
    """The provenance shared by provenance.json and the image labels."""
    def patch_row(row):
        return {"name": Path(row["patch"]["path"]).name, "sha256": row["patch"]["sha256"],
                "size_bytes": row["patch"]["size_bytes"], "files": row["files"]}
    base = [row for row in patched if Path(row["patch"]["path"]).name in PATCHES]
    return {"schema_version": 1, "kind": "bsc-capture-image-provenance", "platform": "linux/amd64",
            "upstream": {key: selected[key] for key in ("repository", "version", "commit")},
            "patches": [patch_row(row) for row in base],
            "installed": [{"path": row["relative"], "sha256": row["source"]["sha256"],
                           "size_bytes": row["source"]["size_bytes"]} for row in installed],
            "go_version_output": go_output, "go_version": selected["go_version"],
            "compiler_image": selected["compiler_image"], "runtime_image": selected["runtime_image"],
            "build_manifest_sha256": manifest_ref["sha256"], "candidate_lock_sha256": lock_ref["sha256"],
            "harness_commit": harness_commit, "source_date_epoch": epoch, "binary_sha256": binary["sha256"],
            "variant": None if variant is None else
            {**variant, "patches": [patch_row(row) for row in patched if row not in base]}}


def image_labels(facts):
    """The provenance as image labels: digests only, no local path."""
    upstream = facts["upstream"]
    name, _, digest = facts["runtime_image"].partition("@")
    variant, cgo = facts["variant"], facts["variant"]["cgo"] if facts["variant"] else None
    labels = {LABEL + "source-commit": upstream["commit"], LABEL + "source-version": upstream["version"],
              LABEL + "patches": ";".join(row["name"] + "=" + row["sha256"] for row in facts["patches"]),
              LABEL + "lock-sha256": facts["candidate_lock_sha256"],
              LABEL + "build-manifest-sha256": facts["build_manifest_sha256"],
              LABEL + "harness-commit": facts["harness_commit"], LABEL + "go-version": facts["go_version"],
              LABEL + "binary-sha256": facts["binary_sha256"], LABEL + "compiler-image": facts["compiler_image"],
              LABEL + "base-image": facts["runtime_image"], OCI + "title": "bsc-capture",
              OCI + "version": upstream["version"][1:] + "-" + SUFFIX + ("-" + variant["name"] if variant else ""),
              OCI + "source": "https://github.com/" + upstream["repository"], OCI + "revision": upstream["commit"],
              OCI + "base.name": name, OCI + "base.digest": digest}
    if variant:
        labels[LABEL + "variant"] = variant["name"]
        if variant["patches"]:
            labels[LABEL + "overlay-patches"] = ";".join(row["name"] + "=" + row["sha256"] for row in variant["patches"])
        if variant["packages"]:
            labels[LABEL + "overlay-packages"] = ";".join(row["path"] + "=" + row["source"]["sha256"]
                                                           for row in variant["packages"])
        if variant["runtime_libraries"]:
            labels[LABEL + "runtime-libraries"] = ";".join(Path(row["path"]).name + "=" + row["sha256"]
                                                           for row in variant["runtime_libraries"])
        for key in ("include", "library"):
            if cgo[key]:
                labels[LABEL + "cgo-" + key + "-sha256"] = ",".join(row["sha256"] for row in cgo[key])
        if cgo["link"]:
            labels[LABEL + "cgo-link"] = " ".join(cgo["link"])
    return labels


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


def dynamic_libraries(text):
    """The libraries of an `ldd` output: the library name and where the loader
    finds it ("not found" for a missing one)."""
    rows = {}
    for line in text.splitlines():
        name, separator, rest = line.strip().partition(" => ")
        if not separator:
            continue  # the loader and the vDSO have no path
        require(name not in rows, "Repeated library line: " + line)
        rows[name] = rest.partition(" (")[0].strip()
    return rows


def check_libraries(rows):
    """The runtime image holds every shared library of the binary."""
    missing = sorted(name for name, path in rows.items() if path == "not found")
    require(rows and not missing, "Runtime image lacks shared libraries of the binary: " + ", ".join(missing))


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


def build(manifest_path, expected_sha, output, variant=None):
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
    tag, commit = tag_for(selected["version"], variant), selected["commit"]
    proof = {"schema_version": 1, "kind": "bsc-capture-image-build", "result": "FAIL", "build_manifest": manifest_ref,
             "candidate_lock": lock_ref, "source_commit": pins.source_commit(), "run_id": uuid.uuid4().hex,
             "native_owner_nonce": None, "deadline_seconds": BUILD_SECONDS, "public_profile_credit": False,
             "image": tag, "variant": None if variant is None else variant["name"], "error": None}

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
        if variant is not None:
            patched += apply_variant(source, root, variant, deadline)
            installed += [{"relative": row["path"], "source": row["source"]} for row in variant["packages"]]
        for directory in ("metadata", "generated", "build-cache", "build-temp", "gopath", "work"):
            (root / directory).mkdir()
        for name in ("go.mod", "go.sum"):
            copy_artifact(source / name, root / "metadata" / name, reference(source / name, deadline=deadline),
                          deadline=deadline)
        compiler = selected["compiler_image"]
        environment = {**pins.environment(), **variant_environment(variant)}
        mounts = probe14.build_mounts(source, manifest_path.parent / "cache" / FAMILY / "mod", root) + \
            variant_mounts(variant)
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
        if variant is not None:
            for row in [*variant["patches"], *variant["runtime_libraries"]]:
                require(reference(Path(row["path"]), BINARY_LIMIT, deadline)["sha256"] == row["sha256"],
                        "Variant input changed during the build: " + row["path"])
        epoch = source_date_epoch(manifest_path, manifest, deadline)
        facts = image_facts(selected, patched, installed, proof["source_commit"], go_output, binary, manifest_ref,
                            lock_ref, epoch, variant)
        # The context holds the binary and the shared libraries of a variant. Their
        # time is the source time, so the layers and the image creation time do
        # not depend on the wall clock.
        context, identity = output / "image", output / "image-id.txt"
        context.mkdir()
        copy_artifact(root / "generated" / BINARY, context / BINARY, binary, BINARY_LIMIT, deadline)
        os.chmod(context / BINARY, 0o755)
        os.utime(context / BINARY, (epoch, epoch))
        shared = variant["runtime_libraries"] if variant else []
        if shared:
            (context / "libs").mkdir()
            for row in shared:
                library = context / "libs" / Path(row["path"]).name
                copy_artifact(Path(row["path"]), library, row, BINARY_LIMIT, deadline)
                os.chmod(library, 0o755)
                os.utime(library, (epoch, epoch))
        (context / "Dockerfile").write_text(dockerfile(variant))
        # The binary is one layer; shared libraries and ldconfig add two.
        added = 1 + (2 if shared else 0)
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
            require(layers[:len(base_layers)] == base_layers and len(layers) == len(base_layers) + added,
                    "Image is not the pinned runtime image with the layers of this build")
            # The checks run in the new image, in owned offline containers.
            version = runner.command(tag, ["version"], [], cap=120, memory="512m")
            fields = version_fields(read_bytes(version, 64 * 1024, deadline).decode())
            check_version(fields, selected)
            config = runner.command(tag, ["--chapel", *PROFILE, "--datadir", "/work/dumpconfig", "dumpconfig"],
                                    [(root / "work", "/work", False)], cap=120, memory="1g")
            text = read_bytes(config, 4 * 1024 * 1024, deadline).decode()
            check_effective_config(text)
            # Every shared library of the binary must be in the image, or the node
            # cannot start there.
            loader = runner.command(tag, ["-c", f"ldd /usr/local/bin/{BINARY} || true"], [], cap=120, memory="512m",
                                    entrypoint="/bin/sh")
            libraries = dynamic_libraries(read_bytes(loader, 64 * 1024, deadline).decode())
            check_libraries(libraries)
        except BaseException:
            # A failed record never leaves its tag on an image.
            if identity.exists():
                host(runner, "remove-image", ["docker", "image", "rm", "-f", tag], 120, check=False)
            raise
        provenance = {**facts, "image": tag, "image_id": image_id, "image_layers": layers,
                      "runtime_image_id": base["Id"], "docker_server_version": docker_version,
                      "go_build_info": reference(info, deadline=deadline), "source_archive": copied,
                      "build_command": arguments, "build_environment": variant_environment(variant),
                      "version_output": fields, "effective_config": config_values(text, "Eth", CONFIG_VALUES),
                      "dynamic_libraries": libraries}
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
        proof["variant"] = labels.get(LABEL + "variant")
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
    builder.add_argument("--variant", help="name of a variant; the image tag ends with it")
    builder.add_argument("--extra-patch", action="append", default=[], metavar="PATH",
                         help="unified diff applied after patches 01 to 08 (repeatable)")
    builder.add_argument("--extra-package", action="append", default=[], metavar="SOURCE=TARGET",
                         help="file or directory installed at a new path of the source tree (repeatable)")
    builder.add_argument("--cgo-include", action="append", default=[], metavar="DIR",
                         help="absolute directory with C headers, passed to cgo with -I (repeatable)")
    builder.add_argument("--cgo-library", action="append", default=[], metavar="DIR",
                         help="absolute directory with libraries, passed to cgo with -L (repeatable)")
    builder.add_argument("--cgo-link", action="append", default=[], metavar="FLAG",
                         help="linker flag for cgo, such as -lrocksdb (repeatable)")
    builder.add_argument("--runtime-library", action="append", default=[], metavar="FILE",
                         help="shared library the image needs in /usr/local/lib (repeatable)")
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
            proof = build(args.build_manifest, args.sha256, args.output,
                          variant_of(args.variant, args.extra_patch, args.extra_package, args.cgo_include,
                                      args.cgo_library, args.cgo_link, args.runtime_library))
        else:
            proof = smoke(args.image, args.output, args.seconds)
    except (ValueError, OSError, KeyError, TypeError, InterruptedError, subprocess.SubprocessError) as error:
        print(json.dumps({"result": "REFUSED", "error": f"{type(error).__name__}: {str(error)[:400]}"}))
        return 2
    print(json.dumps({key: proof.get(key) for key in ("result", "error")}))
    return 0 if proof.get("result") == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
