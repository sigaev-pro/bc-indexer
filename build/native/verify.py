#!/usr/bin/env python3
"""Validate materialized build evidence before any native process starts."""

from pathlib import Path
import math
import re
import struct
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
from core import (CONTRACT, TASK, NATIVE, MODULES, IMAGES, OS_IMAGES, closed, decode,
                  encode, hex_string, json_sha, load_inputs, make_plan, need, ref, regular, sha)


def same_keys(value, keys, label):
    need(type(value) is dict and set(value) == set(keys), label + " coverage differs")


def identity(value):
    closed(value, "run_id operation_id nonce head_sha base_sha verifier_source_commit", "Build identity")
    for key in ("run_id", "operation_id"):
        need(isinstance(value[key], str) and re.fullmatch(r"[A-Za-z0-9._-]{1,160}", value[key]), "Invalid run or operation ID")
    need(hex_string(value["nonce"]), "Invalid identity nonce")
    for key in ("head_sha", "base_sha", "verifier_source_commit"):
        need(hex_string(value[key], 40), "Invalid commit identity")


def commands(rows, root):
    need(type(rows) is list and rows, "Build commands are missing")
    for row in rows:
        closed(row, "argv env exit_code started_at finished_at stdout stderr", "Command")
        need(type(row["argv"]) is list and row["argv"] and all(isinstance(a, str) for a in row["argv"]), "Invalid command arguments")
        need(type(row["env"]) is dict and all(isinstance(k, str) and isinstance(v, str) for k, v in row["env"].items()), "Invalid command environment")
        need(type(row["exit_code"]) is int and row["exit_code"] == 0, "Build command did not succeed")
        need(all(type(row[k]) in (int, float) and math.isfinite(row[k]) for k in ("started_at", "finished_at"))
             and row["started_at"] <= row["finished_at"], "Invalid command timestamps")
        for key in ("stdout", "stderr"):
            ref(root, row[key])


def validate(value, root, snapshot, synthetic):
    closed(value, "schema_version task_id contract evidence_kind plan plan_sha256 builds identity image_inputs host_tools module_closures os_closures profile_bindings updater cleanup commands runtime", "Build manifest")
    need(type(value["schema_version"]) is int and value["schema_version"] == 1 and value["task_id"] == TASK and value["contract"] == CONTRACT, "Unsupported build manifest")
    need(value["evidence_kind"] == "actual-build" or synthetic and value["evidence_kind"] == "synthetic-fixture", "Synthetic or unknown evidence is not an actual build")
    identity(value["identity"])
    plan = value["plan"]
    need(type(plan) is dict and type(plan.get("schema_version")) is int, "Invalid plan version")
    expected = make_plan(snapshot, plan.get("platform"), plan.get("limits"))
    need(encode(plan) == encode(expected) and value["plan_sha256"] == json_sha(expected), "Manifest plan differs from selected inputs")
    need(value["runtime"] == "NOT_RUN", "Build evidence cannot claim native runtime acceptance")
    same_keys(value["host_tools"], ["docker", "git", "python", "github_cli"], "Host tool")
    for item in value["host_tools"].values():
        ref(root, item)
    same_keys(value["image_inputs"], IMAGES, "Image input")
    for name, item in value["image_inputs"].items():
        closed(item, "reference platform_digest index manifest config inspection archive", "Image input")
        need(item["reference"] == plan["images"][name]["reference"] and item["platform_digest"] == plan["images"][name]["platform_digest"], "Image source or platform differs")
        for key in ("index", "manifest", "config", "inspection", "archive"):
            ref(root, item[key])
    same_keys(value["module_closures"], MODULES, "Module closure")
    for name, item in value["module_closures"].items():
        closed(item, "source_commit source_archive modules local_modules downloads verification artifacts", "Module closure")
        need(item["source_commit"] == plan["sources"][name]["commit"], "Module closure source differs")
        for key in ("source_archive", "modules", "local_modules", "downloads", "verification"):
            ref(root, item[key])
        need(type(item["artifacts"]) is list and item["artifacts"], "Module cache artifacts are missing")
        for artifact in item["artifacts"]:
            closed(artifact, "module version zip mod info", "Module cache entry")
            need(all(isinstance(artifact[k], str) and artifact[k] for k in ("module", "version")), "Invalid module identity")
            for key in ("zip", "mod", "info"):
                ref(root, artifact[key])
    same_keys(value["builds"], NATIVE, "Native build")
    for name, item in value["builds"].items():
        closed(item, "source_commit compiler_image binary repeat_build build_info imports dynamic_links runtime_packages oci_layout oci_index oci_manifest oci_config oci_layer version_output", "Native build")
        need(item["source_commit"] == plan["sources"][name]["commit"] and item["compiler_image"] == plan["images"]["golang-build-image"]["reference"], "Native source or compiler differs")
        need(item["repeat_build"] == "NOT_RUN", "Repeat build has not been established")
        for key in ("binary", "build_info", "imports", "dynamic_links", "oci_layout", "oci_index", "oci_manifest", "oci_config", "oci_layer", "version_output"):
            ref(root, item[key])
        need(type(item["runtime_packages"]) is list, "Invalid runtime packages")
        for package in item["runtime_packages"]:
            closed(package, "name version architecture", "Runtime package")
            need(all(isinstance(v, str) and v for v in package.values()), "Invalid runtime package identity")
        if value["evidence_kind"] == "actual-build":
            with ref(root, item["binary"]).open("rb") as stream:
                header = stream.read(20)
            need(len(header) == 20 and header[:6] == b"\x7fELF\x02\x01", "Native binary is not a little-endian 64-bit ELF")
            need(struct.unpack_from("<H", header, 18)[0] == {"linux/amd64": 62, "linux/arm64": 183}[plan["platform"]], "Native binary architecture differs")
    same_keys(value["os_closures"], OS_IMAGES, "OS closure")
    urls = {p["url"] for p in plan["package_indexes"]}
    for name, item in value["os_closures"].items():
        closed(item, "image_reference installed signed_indexes packages", "OS closure")
        need(item["image_reference"] == plan["images"][name]["reference"], "OS image differs")
        ref(root, item["installed"])
        need(type(item["signed_indexes"]) is list and len(item["signed_indexes"]) == 3, "OS signed index coverage differs")
        seen = set()
        for index in item["signed_indexes"]:
            closed(index, "url inrelease release packages signature_log keyring verifier", "Signed index")
            need(index["url"] in urls and index["url"] not in seen, "Unknown or duplicate OS index")
            seen.add(index["url"])
            for key in ("inrelease", "release", "packages", "signature_log", "keyring", "verifier"):
                ref(root, index[key])
        need(type(item["packages"]) is list and item["packages"], "OS package closure is empty")
        seen = set()
        for package in item["packages"]:
            closed(package, "name version architecture index_url archive", "OS package")
            need(package["index_url"] in urls and package["architecture"] in ("all", plan["platform"].split("/")[1]), "OS package platform or index differs")
            key = (package["name"], package["version"], package["architecture"])
            need(key not in seen, "Duplicate OS package")
            seen.add(key)
            ref(root, package["archive"])
    bindings = []
    for profile in plan["profiles"]:
        def binary(component):
            return value["builds"][component]["binary"]["sha256"]
        l1 = profile["supporting_l1"]
        bindings.append(profile | dict(native_binary_sha256=binary(profile["native_component"]),
                        consensus_binary_sha256=binary(profile["consensus_component"]) if profile["consensus_component"] else None,
                        supporting_l1_binary_sha256={k: binary(l1[k]) for k in ("execution_component", "consensus_component")} if l1 else None))
    need(encode(value["profile_bindings"]) == encode(bindings), "Profile binary bindings differ")
    update = value["updater"]
    closed(update, "report candidate check_report independent_report signature_proof active_lock_before active_lock_after live_sources", "Updater evidence")
    need(update["active_lock_before"] == update["active_lock_after"] == plan["lock_sha256"] and update["live_sources"] is True, "Updater changed active inputs or lacks live observations")
    for key in ("report", "candidate", "check_report", "independent_report", "signature_proof"):
        ref(root, update[key])
    ref(root, value["cleanup"])
    commands(value["commands"], root)


def main():
    try:
        request = decode(sys.stdin.buffer.read())
        closed(request, "contract kind synthetic root manifest_path manifest_sha256 profile_id registry_path lock_path identity", "Preflight request")
        need(request["contract"] == CONTRACT and request["kind"] == "VerifyManifest" and type(request["synthetic"]) is bool, "Invalid preflight request")
        for key in ("root", "registry_path", "lock_path"):
            need(isinstance(request[key], str) and Path(request[key]).is_absolute(), "Preflight paths must be absolute")
        path = regular(request["root"], request["manifest_path"])
        need(hex_string(request["manifest_sha256"]) and sha(path) == request["manifest_sha256"], "Manifest bytes changed")
        value = decode(path.read_bytes())
        identity(request["identity"])
        need(value["identity"] == request["identity"], "Requested build identity differs")
        validate(value, request["root"], load_inputs(request["registry_path"], request["lock_path"]), request["synthetic"])
        need(request["profile_id"] in {p["id"] for p in value["profile_bindings"]}, "Requested profile is absent")
        result, status = dict(valid=True, reason="All materialized references and build bindings passed preflight."), 0
    except (ValueError, TypeError, KeyError, OSError, OverflowError, AttributeError) as error:
        result, status = dict(valid=False, reason="Build preflight rejected: " + str(error)), 2
    sys.stdout.buffer.write(encode(result))
    return status


if __name__ == "__main__":
    sys.exit(main())
