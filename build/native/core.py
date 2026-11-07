"""Product-owned primitives for closed build inputs and artifact references."""

import hashlib
import json
import math
from pathlib import Path
import re
import stat

TASK = "E01-01.builds"
CONTRACT = "build-artifacts-v1"
PLATFORMS = ["linux/amd64", "linux/arm64"]
NATIVE = ["geth", "bsc", "bor", "prysm", "heimdall"]
MODULES = NATIVE + ["nats-server", "grpc-go", "nats-go", "protobuf-go", "grocksdb"]
IMAGES = ["golang-build-image", "debian-runtime-image", "nats-runtime-image"]
OS_IMAGES = IMAGES[:2]
DEFAULT_LIMITS = dict(cpus=4, memory_bytes=6 * 1024**3, pids=256, command_seconds=3600)


def need(ok, message):
    if not ok:
        raise ValueError(message)


def closed(value, keys, label):
    need(type(value) is dict and set(value) == set(keys.split()), label + " has missing or unknown fields")
    return value


def hex_string(value, length=64):
    return isinstance(value, str) and re.fullmatch("[0-9a-f]{%d}" % length, value) is not None


def encode(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False) + "\n").encode()


def decode(data):
    def pairs(items):
        result = {}
        for key, value in items:
            need(key not in result, "Duplicate JSON field: " + key)
            result[key] = value
        return result
    def number(text):
        value = float(text)
        need(math.isfinite(value), "Nonfinite JSON number")
        return value
    def invalid(text):
        raise ValueError("Nonfinite JSON number: " + text)
    return json.loads(data, object_pairs_hook=pairs, parse_float=number, parse_constant=invalid)


def sha(path):
    value = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def json_sha(value):
    return hashlib.sha256(encode(value)).hexdigest()


def relative_path(name):
    need(isinstance(name, str) and name and not name.startswith("/") and "\\" not in name
         and all(part not in ("", ".", "..") for part in name.split("/")), "Unsafe relative artifact path")
    return name


def regular(root, name):
    relative_path(name)
    root = Path(root).absolute()
    path = root
    # Check root ancestors as well: resolving first would conceal links.
    for ancestor in [root, *root.parents]:
        need(not ancestor.is_symlink(), "Artifact root contains a symlink")
    for part in name.split("/"):
        path = path / part
        need(not path.is_symlink(), "Artifact path contains a symlink")
    need(stat.S_ISREG(path.stat().st_mode), "Artifact is not a regular file")
    return path


def ref(root, descriptor):
    closed(descriptor, "path sha256 size_bytes", "Artifact reference")
    need(hex_string(descriptor["sha256"]) and type(descriptor["size_bytes"]) is int
         and descriptor["size_bytes"] >= 0, "Invalid artifact size or hash")
    path = regular(root, descriptor["path"])
    need(path.stat().st_size == descriptor["size_bytes"] and sha(path) == descriptor["sha256"], "Artifact bytes changed")
    return path


def describe(root, path):
    path = Path(path).absolute()
    name = path.relative_to(Path(root).absolute()).as_posix()
    path = regular(root, name)
    return dict(path=name, sha256=sha(path), size_bytes=path.stat().st_size)


def load_inputs(registry_path, lock_path):
    registry = decode(Path(registry_path).read_bytes())
    lock = decode(Path(lock_path).read_bytes())
    rh, lh = sha(registry_path), sha(lock_path)
    need(lock["registry_sha256"] == rh, "Lock does not bind the selected registry")
    ids = [row["id"] for row in registry["components"]]
    profiles = [row["id"] for row in lock["profiles"]]
    need(len(ids) == len(set(ids)) == 41 and set(ids) == set(lock["components"]), "Expected 41 independent component pins")
    need(len(profiles) == len(set(profiles)) == 7 and len(registry["profile_ids"]) == 7
         and set(profiles) == set(registry["profile_ids"]), "Expected seven distinct profile bindings")
    return registry, lock, rh, lh


def make_plan(snapshot, platform, limits):
    registry, lock, rh, lh = snapshot
    need(platform in PLATFORMS, "Unsupported platform")
    closed(limits, "cpus memory_bytes pids command_seconds", "Resource limits")
    bounds = dict(cpus=(1, 4), memory_bytes=(512 * 1024**2, 6 * 1024**3), pids=(32, 256), command_seconds=(30, 3600))
    for key, (low, high) in bounds.items():
        need(type(limits[key]) is int and low <= limits[key] <= high, "Invalid resource limit: " + key)
    pins = lock["components"]
    sources = {}
    for identifier in MODULES:
        pin = pins[identifier]
        need(hex_string(pin["commit"], 40), "Source requires an immutable commit")
        sources[identifier] = dict(repository=pin["source_repository"], commit=pin["commit"], version=pin["version"], dependency_inputs=pin["dependency_inputs"])
    images = {}
    for identifier in IMAGES:
        pin = pins[identifier]
        need(re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", pin["pull_reference"]) is not None, "Image reference is not immutable")
        need(set(pin["platform_manifests"]) == set(PLATFORMS), "Image lacks exact platform pins")
        images[identifier] = dict(reference=pin["pull_reference"], platform_digest=pin["platform_manifests"][platform])
    packages = {}
    for row in registry["components"]:
        if row["pin_kind"] == "distribution_package":
            pin = pins[row["id"]]
            packages[row["id"]] = dict(package=pin["package"], version=pin["version_by_platform"][platform], artifacts=[a for a in pin["artifacts"] if a["platform"] == platform])
    profiles = [{key: p[key] for key in ("id", "chain_id", "native_component", "consensus_component")} | {"supporting_l1": p.get("supporting_l1")} for p in lock["profiles"]]
    return dict(schema_version=1, task_id=TASK, contract=CONTRACT, platform=platform, registry_sha256=rh, lock_sha256=lh,
                candidate_platforms=PLATFORMS, unexecuted_platforms=[p for p in PLATFORMS if p != platform], native_components=NATIVE,
                module_components=MODULES, sources=sources, patches=[], limits=limits, compiler=pins["go"], images=images,
                packages=packages, package_indexes=[p for p in lock["package_indexes"] if "/binary-" + platform.split("/")[1] + "/" in p["url"]],
                profiles=profiles, later_outputs=sorted(p["id"] for p in registry["generated_outputs"] if TASK not in p["owner_tasks"]),
                runtime="NOT_RUN", promotion="FORBIDDEN_UNTIL_REQUIRED_GATES")
