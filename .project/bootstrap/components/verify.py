#!/usr/bin/env python3
"""Check the bootstrap inventory offline; this does not admit runtime artifacts."""

import argparse
import copy
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
from urllib.parse import urlparse


PROFILE_IDENTITIES = {
    "ethereum-mainnet": (1, "geth", "prysm"),
    "ethereum-sepolia": (11155111, "geth", "prysm"),
    "ethereum-hoodi": (560048, "geth", "prysm"),
    "bsc-mainnet": (56, "bsc", None),
    "bsc-testnet": (97, "bsc", None),
    "polygon-pos-mainnet": (137, "bor", "heimdall"),
    "polygon-pos-amoy": (80002, "bor", "heimdall"),
}
PROFILES = set(PROFILE_IDENTITIES)
COMMON_COMPONENTS = {"nats-server", "nats-go", "grpc-go", "protobuf-go", "rocksdb", "grocksdb"}
GO_CLIENT_INPUTS = {"geth", "bsc", "bor", "prysm", "heimdall", "nats-server"}
SHA256 = re.compile(r"[0-9a-f]{64}\Z")
COMMIT = re.compile(r"[0-9a-f]{40}\Z")
FLOATING = {"latest", "stable", "main", "master", "develop", "nightly", "edge"}
SOURCE_KINDS = {
    "github_releases", "git_module_tags", "official_downloads", "official_oci",
    "debian_stable_package",
}
HOSTS = {
    "api.github.com", "github.com", "raw.githubusercontent.com", "go.dev",
    "registry-1.docker.io", "deb.debian.org", "security.debian.org", "www.debian.org",
}
PENDING_OUTPUTS = {
    "patched-native-images", "supporting-runtime-images", "common-project-artifacts",
    "resolved-go-module-closures", "resolved-os-package-closures",
    "profile-bootstrap-manifests", "fixture-contract-bytecode", "updater-check-and-promotion",
}


def validate(registry, lock, registry_hash, root):
    errors = []

    def require(condition, message):
        if not condition:
            errors.append(message)

    def sha(value, label):
        require(isinstance(value, str) and bool(SHA256.fullmatch(value)), label + ": invalid SHA-256")

    def exact(value, label):
        require(isinstance(value, str) and bool(value) and any(c.isdigit() for c in value)
                and value.lower() not in FLOATING and not re.search(r"[\s*^<>=]", value),
                label + ": a resolved version is required")

    def url(value, label):
        parsed = urlparse(value)
        require(parsed.scheme == "https" and parsed.hostname in HOSTS
                and parsed.username is None and parsed.password is None,
                label + ": an official HTTPS source without credentials is required")

    require(registry["schema_version"] == 1 and lock["schema_version"] == 1, "unsupported schema")
    require(registry["task_id"] == "BOOT-06", "wrong task")
    require(set(registry["profile_ids"]) == PROFILES and len(registry["profile_ids"]) == 7,
            "registry must cover the seven accepted profiles exactly")
    require(registry["policy"]["candidate_only"] is True, "registry must remain a candidate")
    require(registry["policy"]["stage_limit"] == "BOOT", "wrong launch stage")
    require(registry["policy"]["allow_floating_pins"] is False, "floating pins are forbidden")
    require(registry["policy"]["stable_sources_only"] is True, "stable source policy is required")
    require(lock["registry_sha256"] == registry_hash, "registry digest mismatch")
    require(lock["status"] == "METADATA_VERIFIED_CANDIDATE", "wrong inventory status")
    require(lock["release_ready"] is False and lock["active_lock_changed"] is False,
            "metadata inventory cannot claim promotion")

    entries = registry["components"]
    ids = [entry["id"] for entry in entries]
    component_profiles = {entry["id"]: set(entry["profiles"]) for entry in entries}
    require(len(ids) == len(set(ids)), "duplicate component IDs")
    require(set(ids) == set(lock["components"]), "registry and lock component sets differ")
    platforms = set(lock["candidate_policy"]["build_platforms"])
    require(bool(platforms) and platforms <= {"linux/amd64", "linux/arm64"}, "invalid candidate platforms")

    for entry in entries:
        ident = entry["id"]
        item = lock["components"].get(ident)
        if item is None:
            continue
        require(bool(entry["profiles"]) and set(entry["profiles"]) <= PROFILES, ident + ": invalid profile scope")
        source = entry["source"]
        require(source["kind"] in SOURCE_KINDS, ident + ": unknown source kind")
        url(source["url"], ident)
        require(bool(entry["update_rule"]) and bool(entry["verification"]), ident + ": missing update/check rule")
        require(item["compatibility"] == "NOT_RUN", ident + ": unproved compatibility claim")
        require(bool(item["evidence_type"]), ident + ": missing evidence type")
        kind = entry["pin_kind"]
        if ident in GO_CLIENT_INPUTS:
            paths = [dep["path"] for dep in item.get("dependency_inputs", [])]
            require(kind == "git_source", ident + ": a native/support Go client needs a source pin")
            require(len(paths) == 2 and set(paths) == {"go.mod", "go.sum"},
                    ident + ": exactly one go.mod and one go.sum input are required")
        if kind == "git_source":
            exact(item["version"], ident)
            require(bool(COMMIT.fullmatch(item["commit"])), ident + ": invalid source commit")
            require(item["source_repository"] == source["repository"], ident + ": wrong source repository")
            url(item["release_url"], ident)
            require(item["release_url"].startswith("https://github.com/" + source["repository"] + "/"),
                    ident + ": release belongs to another repository")
            prefix = "https://raw.githubusercontent.com/" + source["repository"] + "/" + item["commit"] + "/"
            for dep in item.get("dependency_inputs", []):
                sha(dep["sha256"], ident + " dependency")
                require(dep["url"] == prefix + dep["path"], ident + ": crossed or mutable dependency input")
                require(dep["evidence_type"] == "SOURCE_BYTES_HASHED", ident + ": missing dependency evidence")
            if source["channel"] == "upstream-bundle":
                owner = item["owner_component"]
                require(owner in {"bor", "heimdall"} and ident.startswith(owner + "-"),
                        ident + ": upstream fork owner is not explicit")
        elif kind == "oci_image":
            exact(item["version"], ident)
            require(item["image"] == source["repository"], ident + ": wrong image repository")
            require(item["digest"].startswith("sha256:"), ident + ": missing image digest type")
            sha(item["digest"].removeprefix("sha256:"), ident)
            require(item["pull_reference"] == item["image"] + "@" + item["digest"],
                    ident + ": pull reference is not immutable")
            require(set(item["platform_manifests"]) == platforms, ident + ": missing platform descriptors")
            for value in item["platform_manifests"].values():
                require(value.startswith("sha256:"), ident + ": missing platform digest type")
                sha(value.removeprefix("sha256:"), ident + " platform")
            require(item["evidence_type"] == "OCI_INDEX_BYTES_HASHED", ident + ": missing OCI evidence")
            url(item["discovery_url"], ident)
        elif kind == "distribution_package":
            require(set(item["version_by_platform"]) == platforms, ident + ": package platforms differ")
            require(item["package"] == source["package"], ident + ": wrong package")
            require({a["platform"] for a in item["artifacts"]} == platforms, ident + ": package artifact missing")
            for artifact in item["artifacts"]:
                exact(artifact["version"], ident)
                require(item["version_by_platform"][artifact["platform"]] == artifact["version"],
                        ident + ": package version mismatch")
            require(item["signed_index_verification"] == "PENDING_E01", ident + ": unproved signature verification")
        elif kind == "artifact_set":
            exact(item["version"], ident)
            require(bool(item["artifacts"]), ident + ": empty artifact set")
        else:
            errors.append(ident + ": unknown pin kind")
        for artifact in item.get("artifacts", []):
            sha(artifact["sha256"], ident + " artifact")
            url(artifact["url"], ident + " artifact")
            require(artifact["payload_verified"] is False, ident + ": metadata cannot claim downloaded payload checks")

    require(lock["components"]["nats-chart"]["chart_metadata"]["app_version"]
            == lock["components"]["nats-server"]["version"].removeprefix("v"), "NATS chart/server mismatch")
    chart = lock["components"]["nats-chart"]["chart_metadata"]
    sha(chart["sha256"], "chart metadata")
    require(chart["version"] == lock["components"]["nats-chart"]["version"].removeprefix("nats-"),
            "NATS chart version mismatch")
    for name in ("reloader", "natsBox", "promExporter"):
        require(lock["candidate_policy"]["nats_chart_overrides"][name]["enabled"] is False,
                name + ": this candidate has no admitted sidecar")

    constraints = {c["id"]: c for c in lock["constraints"]}
    require(len(constraints) == len(lock["constraints"]), "duplicate constraints")
    for constraint in constraints.values():
        require(constraint["status"] == "OPEN", "unproved compatibility constraint closure")
        require(bool(constraint["required_evidence"]), "constraint needs an acceptance condition")
        for source in constraint["sources"]:
            url(source, constraint["id"])
    rows = lock["profiles"]
    require({row["id"] for row in rows} == PROFILES and len(rows) == 7, "profile coverage is incomplete")
    for profile in rows:
        ident = profile["id"]
        require(profile["build"] == "NOT_RUN" and profile["runtime"] == "NOT_RUN", ident + ": unproved profile result")
        expected_identity = PROFILE_IDENTITIES.get(ident)
        if expected_identity is None:
            errors.append(ident + ": unknown profile identity")
            continue
        chain_id, native, consensus = expected_identity
        require(type(profile["chain_id"]) is int and profile["chain_id"] == chain_id,
                ident + ": chain ID does not match the accepted profile")
        require(profile["native_component"] == native and profile["consensus_component"] == consensus,
                ident + ": native/support clients do not match the accepted profile")
        require(profile["native_component"] in ids and profile["consensus_component"] in [None] + ids,
                ident + ": unknown native/support component")
        common = profile["common_components"]
        require(set(common) == COMMON_COMPONENTS and len(common) == len(COMMON_COMPONENTS),
                ident + ": common components are incomplete, duplicated, or unknown")
        referenced = [profile["native_component"], profile["consensus_component"], *common]
        for component in referenced:
            if component is not None:
                require(component in lock["components"] and ident in component_profiles.get(component, set()),
                        ident + ": component does not apply to this profile: " + component)
        require(bool(profile["constraints"]) and set(profile["constraints"]) <= set(constraints),
                ident + ": missing constraints")
        if ident.startswith("polygon-"):
            l1 = profile["supporting_l1"]
            expected = "ethereum-mainnet" if ident == "polygon-pos-mainnet" else "ethereum-sepolia"
            require(l1["profile"] == expected and l1["dedicated_instance"] is True
                    and l1["share_with_other_profiles"] is False, ident + ": invalid L1 ownership")
            _, execution, consensus = PROFILE_IDENTITIES[expected]
            require(l1["execution_component"] == execution and l1["consensus_component"] == consensus,
                    ident + ": invalid L1 execution/consensus clients")
            for component in (l1["execution_component"], l1["consensus_component"]):
                scope = component_profiles.get(component, set())
                require(component in lock["components"] and {ident, expected} <= scope,
                        ident + ": supporting component does not apply to both the Polygon and L1 profiles: " + component)

    expected = {item["id"] for item in registry["generated_outputs"]}
    require(expected == PENDING_OUTPUTS, "required generated outputs are missing")
    pending = lock["pending_outputs"]
    require({item["id"] for item in pending} == expected and len(pending) == len(expected), "pending output sets differ")
    for item in pending:
        require(item["status"] == "PENDING_IMPLEMENTATION" and item["runtime"] == "NOT_RUN",
                item["id"] + ": unproved generated output")
    task_text = (root / ".project/docs/TASK_DECOMPOSITION.md").read_text()
    task_ids = set(re.findall(r"E\d+-\d+\.[a-z][a-z0-9_]*", task_text))
    for item in registry["generated_outputs"]:
        require(set(item["owner_tasks"]) <= task_ids, item["id"] + ": unknown task owner")
        require(set(item["source_components"]) <= set(ids), item["id"] + ": unknown component")

    for item in lock["package_indexes"]:
        url(item["url"], "package index")
        sha(item["sha256"], "package index")
        require(item["signature_verification"] == "PENDING_E01", "unproved package-index signature")
    indexes = {item["url"] for item in lock["package_indexes"]}
    expected_indexes = set()
    for platform in platforms:
        architecture = platform.split("/")[1]
        expected_indexes.add("https://deb.debian.org/debian/dists/trixie/main/binary-" + architecture + "/Packages.gz")
        expected_indexes.add("https://security.debian.org/debian-security/dists/trixie-security/main/binary-" + architecture + "/Packages.xz")
        expected_indexes.add("https://deb.debian.org/debian/dists/trixie-updates/main/binary-" + architecture + "/Packages.xz")
    require(indexes == expected_indexes and len(lock["package_indexes"]) == len(indexes),
            "stable/security/update indexes are incomplete or duplicated")
    for artifact in lock["acquired_bootstrap_artifacts"]:
        sha(artifact["sha256"], artifact["id"])
        sha(artifact["genesis_sha256"], artifact["id"] + " genesis")
        url(artifact["url"], artifact["id"])
        require(artifact["profile"] in PROFILES and artifact["component"] in ids, "unknown bootstrap owner")
        require(artifact["native_init"] == "NOT_RUN" and artifact["effective_fork_config"] == "NOT_RUN",
                artifact["id"] + ": archive inspection is not native admission")

    require(bool(COMMIT.fullmatch(lock["base_commit"])), "invalid baseline commit")
    for entry in lock["baseline"]["files"]:
        sha(entry["sha256"], entry["path"])
        require(not Path(entry["path"]).is_absolute() and ".." not in Path(entry["path"]).parts,
                "baseline path must stay within the repository")
        result = subprocess.run(["git", "show", lock["base_commit"] + ":" + entry["path"]],
                                cwd=root, capture_output=True, check=False)
        require(result.returncode == 0 and hashlib.sha256(result.stdout).hexdigest() == entry["sha256"],
                entry["path"] + ": baseline bytes do not match the pinned commit")
    original = root / ".project/evm-transaction-extractor-tz-v2.md"
    require(hashlib.sha256(original.read_bytes()).hexdigest()
            == "ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634",
            "immutable original specification changed")
    return errors


def negative_checks(registry, lock, digest, root):
    mutations = {
        "floating_version": lambda r, l: l["components"]["geth"].update(version="latest"),
        "mutable_commit": lambda r, l: l["components"]["geth"].update(commit="main"),
        "missing_component": lambda r, l: l["components"].pop("prysm"),
        "duplicate_component": lambda r, l: r["components"].append(copy.deepcopy(r["components"][0])),
        "missing_profile": lambda r, l: l["profiles"].pop(),
        "false_runtime_pass": lambda r, l: l["profiles"][0].update(runtime="PASS"),
        "stale_registry_digest": lambda r, l: l.update(registry_sha256="0" * 64),
        "bad_artifact_hash": lambda r, l: l["components"]["go"]["artifacts"][0].update(sha256="broken"),
        "mutable_image_pull": lambda r, l: l["components"]["nats-runtime-image"].update(pull_reference="nats:latest"),
        "wrong_nats_version": lambda r, l: l["components"]["nats-chart"]["chart_metadata"].update(app_version="0.0.0"),
        "unlocked_sidecar": lambda r, l: l["candidate_policy"]["nats_chart_overrides"]["natsBox"].update(enabled=True),
        "crossed_native_graph": lambda r, l: l["components"]["bor"].update(dependency_inputs=l["components"]["heimdall"]["dependency_inputs"]),
        "wrong_polygon_l1": lambda r, l: l["profiles"][-1]["supporting_l1"].update(profile="ethereum-mainnet"),
        "missing_generated_output": lambda r, l: l["pending_outputs"].pop(),
        "unofficial_artifact": lambda r, l: l["components"]["go"]["artifacts"][0].update(url="https://example.com/go.tar.gz"),
        "false_release_ready": lambda r, l: l.update(release_ready=True),
        "duplicate_package_index": lambda r, l: l["package_indexes"].__setitem__(-1, l["package_indexes"][0]),
        "wrong_chain_id": lambda r, l: l["profiles"][0].update(chain_id=97),
        "wrong_native_client": lambda r, l: l["profiles"][0].update(native_component="bsc"),
        "wrong_consensus_client": lambda r, l: l["profiles"][0].update(consensus_component="heimdall"),
        "unexpected_bsc_consensus_client": lambda r, l: l["profiles"][3].update(consensus_component="prysm"),
        "wrong_polygon_l1_execution": lambda r, l: l["profiles"][-1]["supporting_l1"].update(execution_component="bsc"),
        "wrong_polygon_l1_consensus": lambda r, l: l["profiles"][-1]["supporting_l1"].update(consensus_component="heimdall"),
        "unknown_common_component": lambda r, l: l["profiles"][0]["common_components"].append("unknown"),
        "missing_common_component": lambda r, l: l["profiles"][0]["common_components"].pop(),
        "duplicate_common_component": lambda r, l: l["profiles"][0]["common_components"].append("grpc-go"),
        "inapplicable_common_component": lambda r, l: next(e for e in r["components"] if e["id"] == "nats-server")["profiles"].remove("ethereum-mainnet"),
        "inapplicable_native_component": lambda r, l: next(e for e in r["components"] if e["id"] == "geth")["profiles"].remove("ethereum-mainnet"),
        "inapplicable_polygon_support": lambda r, l: next(e for e in r["components"] if e["id"] == "prysm")["profiles"].remove("polygon-pos-amoy"),
        "inapplicable_l1_network": lambda r, l: next(e for e in r["components"] if e["id"] == "prysm")["profiles"].remove("ethereum-sepolia"),
    }
    for ident in sorted(GO_CLIENT_INPUTS):
        mutations[ident + "_empty_dependency_inputs"] = (
            lambda r, l, ident=ident: l["components"][ident].update(dependency_inputs=[]))
        for path in ("go.mod", "go.sum"):
            mutations[ident + "_missing_" + path] = (
                lambda r, l, ident=ident, path=path: l["components"][ident].update(
                    dependency_inputs=[dep for dep in l["components"][ident]["dependency_inputs"] if dep["path"] != path]))
        mutations[ident + "_duplicate_dependency_input"] = (
            lambda r, l, ident=ident: l["components"][ident]["dependency_inputs"].append(
                copy.deepcopy(l["components"][ident]["dependency_inputs"][0])))
    missed = []
    for name, mutate in mutations.items():
        changed_registry, changed_lock = copy.deepcopy(registry), copy.deepcopy(lock)
        mutate(changed_registry, changed_lock)
        if not validate(changed_registry, changed_lock, digest, root):
            missed.append(name)
    return {"cases": len(mutations), "rejected": len(mutations) - len(missed), "missed": missed}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--self-test", action="store_true", help="Reject unsafe inventory mutations in memory.")
    args = parser.parse_args()
    directory = Path(__file__).resolve().parent
    root = directory.parents[2]
    try:
        raw = (directory / "registry.json").read_bytes()
        registry = json.loads(raw)
        lock = json.loads((directory / "candidate-lock.json").read_bytes())
        digest = hashlib.sha256(raw).hexdigest()
        errors = validate(registry, lock, digest, root)
        negative = negative_checks(registry, lock, digest, root) if args.self_test and not errors else None
        if negative and negative["missed"]:
            errors.append("unsafe mutations were accepted")
        report = {"task": "BOOT-06", "structure": "FAIL" if errors else "PASS",
                  "components": len(registry["components"]), "profiles": len(lock["profiles"]),
                  "constraints": len(lock["constraints"]), "pending_outputs": len(lock["pending_outputs"]),
                  "runtime": "NOT_RUN", "release_ready": False, "errors": errors}
        if negative:
            report["negative_checks"] = negative
        print(json.dumps(report, indent=2))
        return 1 if errors else 0
    except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
        print(json.dumps({"task": "BOOT-06", "structure": "FAIL", "error": str(error), "runtime": "NOT_RUN"}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
