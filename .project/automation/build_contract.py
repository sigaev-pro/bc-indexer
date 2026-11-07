"""Closed task-owned build coverage and materialized artifact invariants."""

import hashlib
import json
import math
from pathlib import Path, PurePosixPath
import re


TASK = "E01-01.builds"
CONTRACT = "build-artifacts-v1"
NATIVE = ("geth", "bsc", "bor", "prysm", "heimdall")
MODULES = (*NATIVE, "nats-server", "grpc-go", "nats-go", "protobuf-go", "grocksdb")
IMAGES = ("golang-build-image", "debian-runtime-image", "nats-runtime-image")
OS_IMAGES = ("golang-build-image", "debian-runtime-image")
PLATFORMS = ("linux/amd64", "linux/arm64")
HEX = re.compile(r"[a-f0-9]{64}\Z")
COMMIT = re.compile(r"[a-f0-9]{40}\Z")
DEFAULT_LIMITS = {"cpus": 4, "memory_bytes": 6 * 1024 ** 3, "pids": 256, "command_seconds": 3600}
OUTPUTS = ("build/native/plan.py", "build/native/verify.py", "build/native/build-manifest.schema.json",
           "build/native/README.md", "scripts/refresh-versions", "scripts/versioning/README.md", "test/builds/test_build_contract.py")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def canonical(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False) + "\n").encode()


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate JSON field")
        result[key] = value
    return result


def parse_json(data):
    def nonfinite(value):
        raise ValueError("nonfinite JSON number")
    return json.loads(data, object_pairs_hook=unique_object, parse_constant=nonfinite)


def file_hash(path):
    result = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(block)
    return result.hexdigest()


def inputs(source):
    source = Path(source)
    registry_path = source / ".project/bootstrap/components/registry.json"
    lock_path = source / ".project/bootstrap/components/candidate-lock.json"
    registry, lock = parse_json(registry_path.read_bytes()), parse_json(lock_path.read_bytes())
    registry_hash, lock_hash = file_hash(registry_path), file_hash(lock_path)
    require(lock["registry_sha256"] == registry_hash, "candidate lock refers to another component registry")
    identifiers = [row["id"] for row in registry["components"]]
    require(len(identifiers) == 41 and len(set(identifiers)) == 41
            and set(identifiers) == set(lock["components"]), "component coverage differs from the accepted inventory")
    require(len(registry["profile_ids"]) == 7 and len(set(registry["profile_ids"])) == 7
            and set(registry["profile_ids"]) == {row["id"] for row in lock["profiles"]},
            "profile coverage differs from the seven accepted profiles")
    return registry, lock, registry_hash, lock_hash


def limits(value):
    require(type(value) is dict and set(value) == set(DEFAULT_LIMITS)
            and all(type(item) is int for item in value.values()), "build limits must be closed integer fields")
    require(1 <= value["cpus"] <= 4 and 512 * 1024 ** 2 <= value["memory_bytes"] <= 6 * 1024 ** 3
            and 32 <= value["pids"] <= 256 and 30 <= value["command_seconds"] <= 3600,
            "build resource or timeout limits exceed the trusted bound")
    return value


def expected_plan(registry, lock, registry_hash, lock_hash, platform="linux/amd64", resource_limits=None):
    require(platform in PLATFORMS, "unsupported build platform")
    selected_limits = dict(limits(resource_limits or DEFAULT_LIMITS))
    components = lock["components"]
    required_sources = {}
    for identifier in MODULES:
        selected = components[identifier]
        require(COMMIT.fullmatch(selected["commit"]) is not None, "source is not pinned to a commit")
        required_sources[identifier] = {"repository": selected["source_repository"], "commit": selected["commit"], "version": selected["version"],
                                        "dependency_inputs": selected["dependency_inputs"]}
    package_ids = sorted(row["id"] for row in registry["components"] if row["pin_kind"] == "distribution_package")
    return {"schema_version": 1, "task_id": TASK, "contract": CONTRACT, "platform": platform,
            "registry_sha256": registry_hash, "lock_sha256": lock_hash,
            "candidate_platforms": list(PLATFORMS), "unexecuted_platforms": [value for value in PLATFORMS if value != platform],
            "native_components": list(NATIVE), "module_components": list(MODULES),
            "sources": required_sources, "patches": [], "limits": selected_limits,
            "compiler": components["go"],
            "images": {key: {"reference": components[key]["pull_reference"],
                              "platform_digest": components[key]["platform_manifests"][platform]} for key in IMAGES},
            "packages": {key: {"package": components[key]["package"],
                                "version": components[key]["version_by_platform"][platform],
                                "artifacts": [row for row in components[key]["artifacts"] if row["platform"] == platform]}
                         for key in package_ids},
            "package_indexes": [row for row in lock["package_indexes"] if "/binary-" + platform.split("/")[1] + "/" in row["url"]],
            "profiles": [{"id": row["id"], "chain_id": row["chain_id"], "native_component": row["native_component"],
                          "consensus_component": row["consensus_component"],
                          "supporting_l1": row.get("supporting_l1")} for row in lock["profiles"]],
            "later_outputs": sorted(row["id"] for row in registry["generated_outputs"] if TASK not in row["owner_tasks"]),
            "runtime": "NOT_RUN", "promotion": "FORBIDDEN_UNTIL_REQUIRED_GATES"}


def validate_plan(plan, source, input_snapshot=None):
    registry, lock, registry_hash, lock_hash = inputs(source) if input_snapshot is None else input_snapshot
    require(type(plan) is dict and type(plan.get("schema_version")) is int, "build plan must have an integer version")
    expected = expected_plan(registry, lock, registry_hash, lock_hash, plan.get("platform"), plan.get("limits"))
    require(canonical(plan) == canonical(expected), "build plan differs from the exact trusted source/profile/resource contract")
    return expected


def safe_file(root, name):
    require(isinstance(name, str) and name and "\\" not in name, "artifact path must be a nonempty POSIX path")
    relative = PurePosixPath(name)
    require(not relative.is_absolute() and all(part not in {".", "..", ""} for part in name.split("/")),
            "artifact path escapes its root")
    root = Path(root).resolve()
    path = root
    for part in relative.parts:
        path /= part
        require(not path.is_symlink(), "artifact path contains a symlink")
    require(path.is_file() and path.resolve().is_relative_to(root), "artifact file is missing or outside its root")
    return path


def reference(root, path):
    root, path = Path(root).absolute(), Path(path).absolute()
    require(path.is_relative_to(root), "artifact output is outside its execution root")
    relative = path.relative_to(root).as_posix()
    path = safe_file(root, relative)
    return {"path": relative, "sha256": file_hash(path), "size_bytes": path.stat().st_size}


def reference_descriptor(value):
    require(type(value) is dict and set(value) == {"path", "sha256", "size_bytes"}
            and isinstance(value["sha256"], str) and HEX.fullmatch(value["sha256"])
            and type(value["size_bytes"]) is int and value["size_bytes"] >= 0, "invalid artifact reference")
    name = value["path"]
    require(isinstance(name, str) and name and not name.startswith("/") and "\\" not in name
            and all(part not in {"", ".", ".."} for part in name.split("/")), "unsafe artifact reference path")
    return value


def verify_reference(root, value):
    reference_descriptor(value)
    path = safe_file(root, value["path"])
    require(path.stat().st_size == value["size_bytes"] and file_hash(path) == value["sha256"],
            "materialized artifact bytes differ from their reference")
    return path


def command_record(row, root, reference_check=None):
    verify = reference_check or verify_reference
    require(type(row) is dict and set(row) == {"argv", "env", "exit_code", "started_at", "finished_at", "stdout", "stderr"}
            and type(row["argv"]) is list and row["argv"] and all(isinstance(arg, str) for arg in row["argv"])
            and type(row["env"]) is dict and all(isinstance(key, str) and isinstance(value, str) for key, value in row["env"].items())
            and type(row["exit_code"]) is int and row["exit_code"] == 0,
            "build execution lacks a successful explicit command")
    require(all(type(row[key]) in (int, float) and math.isfinite(row[key]) for key in ("started_at", "finished_at"))
            and row["started_at"] <= row["finished_at"], "build execution times are invalid")
    verify(root, row["stdout"])
    verify(root, row["stderr"])


def validate_manifest(manifest, root, source, allow_synthetic=False, input_snapshot=None, reference_check=None):
    verify = reference_check or verify_reference
    required = {"schema_version", "task_id", "contract", "evidence_kind", "plan", "plan_sha256", "builds", "identity", "image_inputs", "host_tools",
                "module_closures", "os_closures", "profile_bindings", "updater", "cleanup", "commands", "runtime"}
    require(type(manifest) is dict and set(manifest) == required and type(manifest["schema_version"]) is int
            and manifest["schema_version"] == 1 and manifest["task_id"] == TASK and manifest["contract"] == CONTRACT,
            "invalid build manifest identity or fields")
    require(manifest["evidence_kind"] == "actual-build" or allow_synthetic and manifest["evidence_kind"] == "synthetic-fixture",
            "synthetic or unknown evidence cannot accept a build")
    plan = validate_plan(manifest["plan"], source, input_snapshot)
    require(manifest["plan_sha256"] == digest(plan) and manifest["runtime"] == "NOT_RUN",
            "build manifest plan or runtime scope differs")
    require(type(manifest["host_tools"]) is dict and set(manifest["host_tools"]) == {"docker", "git", "python", "github_cli"}, "actual build host tool evidence is missing")
    for ref in manifest["host_tools"].values():
        verify(root, ref)
    identity = manifest["identity"]
    require(type(identity) is dict and set(identity) == {"run_id", "operation_id", "nonce", "head_sha", "base_sha", "verifier_source_commit"}
            and all(isinstance(identity[key], str) and re.fullmatch(r"[A-Za-z0-9._-]{1,160}", identity[key]) for key in ("run_id", "operation_id"))
            and HEX.fullmatch(identity["nonce"])
            and all(COMMIT.fullmatch(identity[key]) for key in ("head_sha", "base_sha", "verifier_source_commit")), "build execution identity is invalid")
    require(type(manifest["image_inputs"]) is dict and set(manifest["image_inputs"]) == set(IMAGES), "image input coverage differs")
    for identifier, row in manifest["image_inputs"].items():
        require(type(row) is dict and set(row) == {"reference", "platform_digest", "index", "manifest", "config", "inspection", "archive"}
                and row["reference"] == plan["images"][identifier]["reference"]
                and row["platform_digest"] == plan["images"][identifier]["platform_digest"], "acquired image identity differs")
        for key in ("index", "manifest", "config", "inspection", "archive"):
            verify(root, row[key])
    require(type(manifest["builds"]) is dict and set(manifest["builds"]) == set(NATIVE)
            and type(manifest["module_closures"]) is dict and set(manifest["module_closures"]) == set(MODULES)
            and type(manifest["os_closures"]) is dict and set(manifest["os_closures"]) == set(OS_IMAGES),
            "build manifest omits a required client or dependency closure")
    for identifier, row in manifest["module_closures"].items():
        require(type(row) is dict and set(row) == {"source_commit", "source_archive", "modules", "local_modules", "downloads", "verification", "artifacts"}
                and row["source_commit"] == plan["sources"][identifier]["commit"], "module closure source differs")
        for key in ("source_archive", "modules", "local_modules", "downloads", "verification"):
            verify(root, row[key])
        require(type(row["artifacts"]) is list and row["artifacts"], "module closure has no retained cache artifacts")
        for item in row["artifacts"]:
            require(type(item) is dict and set(item) == {"module", "version", "zip", "mod", "info"}, "invalid module artifact entry")
            for key in ("zip", "mod", "info"):
                verify(root, item[key])
    for identifier, row in manifest["builds"].items():
        require(type(row) is dict and set(row) == {"source_commit", "compiler_image", "binary", "repeat_build", "build_info", "imports", "dynamic_links",
                                                  "runtime_packages", "oci_layout", "oci_index", "oci_manifest", "oci_config", "oci_layer", "version_output"}
                and row["source_commit"] == plan["sources"][identifier]["commit"]
                and row["compiler_image"] == plan["images"]["golang-build-image"]["reference"],
                "native build source or compiler differs")
        for key in set(row) - {"source_commit", "compiler_image", "runtime_packages", "repeat_build"}:
            verify(root, row[key])
        require(row["repeat_build"] == "NOT_RUN", "this adapter does not claim a repeated bit-identical build")
        require(type(row["runtime_packages"]) is list and all(type(item) is dict and set(item) == {"name", "version", "architecture"}
                and all(isinstance(value, str) and value for value in item.values()) for item in row["runtime_packages"]), "runtime library package closure is invalid")
    for identifier, row in manifest["os_closures"].items():
        require(type(row) is dict and set(row) == {"image_reference", "installed", "signed_indexes", "packages"}
                and row["image_reference"] == plan["images"][identifier]["reference"]
                and type(row["signed_indexes"]) is list and len(row["signed_indexes"]) == 3
                and type(row["packages"]) is list and row["packages"], "OS closure or signed index coverage is incomplete")
        verify(root, row["installed"])
        for index in row["signed_indexes"]:
            require(set(index) == {"url", "inrelease", "release", "packages", "signature_log", "keyring", "verifier"}
                    and index["url"] in {item["url"] for item in plan["package_indexes"]}, "OS index identity differs")
            for key in ("inrelease", "release", "packages", "signature_log", "keyring", "verifier"):
                verify(root, index[key])
        require(len({row["url"] for row in row["signed_indexes"]}) == 3, "OS closure duplicates an index")
        package_keys = set()
        for package in row["packages"]:
            require(set(package) == {"name", "version", "architecture", "index_url", "archive"}
                    and package["index_url"] in {item["url"] for item in plan["package_indexes"]}
                    and package["architecture"] in {plan["platform"].split("/")[1], "all"}, "OS package identity differs")
            key = (package["name"], package["version"], package["architecture"])
            require(key not in package_keys, "OS closure duplicates a package")
            package_keys.add(key)
            verify(root, package["archive"])
    expected_bindings = [{**row, "native_binary_sha256": manifest["builds"][row["native_component"]]["binary"]["sha256"],
                          "consensus_binary_sha256": (manifest["builds"][row["consensus_component"]]["binary"]["sha256"]
                                                       if row["consensus_component"] else None),
                          "supporting_l1_binary_sha256": ({key: manifest["builds"][row["supporting_l1"][key]]["binary"]["sha256"]
                                                          for key in ("execution_component", "consensus_component")}
                                                         if row["supporting_l1"] else None)}
                         for row in plan["profiles"]]
    require(canonical(manifest["profile_bindings"]) == canonical(expected_bindings), "profile artifacts differ from their own native/support bindings")
    update = manifest["updater"]
    require(type(update) is dict and set(update) == {"report", "candidate", "check_report", "independent_report", "signature_proof", "active_lock_before", "active_lock_after", "live_sources"}
            and update["active_lock_before"] == update["active_lock_after"] == plan["lock_sha256"]
            and update["live_sources"] is True, "updater changed the active lock or lacks live source evidence")
    for key in ("report", "candidate", "check_report", "independent_report", "signature_proof"):
        verify(root, update[key])
    verify(root, manifest["cleanup"])
    require(type(manifest["commands"]) is list and manifest["commands"], "build commands are missing")
    for row in manifest["commands"]:
        command_record(row, root, verify)
    return manifest
