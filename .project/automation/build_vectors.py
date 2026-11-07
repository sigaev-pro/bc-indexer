"""Synthetic materialization controls for the product's pre-start hash guard."""

import copy
from pathlib import Path

import build_contract as contract


IDENTITY = {"run_id": "synthetic-build-run", "operation_id": "synthetic-build-operation", "nonce": "a" * 64,
            "head_sha": "b" * 40, "base_sha": "c" * 40, "verifier_source_commit": "d" * 40}


def bindings(plan, builds):
    return [{**row, "native_binary_sha256": builds[row["native_component"]]["binary"]["sha256"],
             "consensus_binary_sha256": builds[row["consensus_component"]]["binary"]["sha256"] if row["consensus_component"] else None,
             "supporting_l1_binary_sha256": {key: builds[row["supporting_l1"][key]]["binary"]["sha256"]
                                             for key in ("execution_component", "consensus_component")} if row["supporting_l1"] else None}
            for row in plan["profiles"]]


def fixture(root, source):
    """These small bytes only test binding and structure, not native semantics."""
    root = Path(root)
    root.mkdir(parents=True, exist_ok=True)
    payload = root / "synthetic-only.bin"
    payload.write_bytes(b"SYNTHETIC PROTOCOL FIXTURE; NOT A NATIVE BUILD\n")
    ref = contract.reference(root, payload)
    registry, lock, registry_hash, lock_hash = contract.inputs(source)
    plan = contract.expected_plan(registry, lock, registry_hash, lock_hash)
    images = {name: {**plan["images"][name], **{key: copy.deepcopy(ref) for key in ("index", "manifest", "config", "inspection", "archive")}}
              for name in contract.IMAGES}
    modules = {name: {"source_commit": plan["sources"][name]["commit"],
                      **{key: copy.deepcopy(ref) for key in ("source_archive", "modules", "downloads", "verification")},
                      "artifacts": [{"module": "synthetic/dependency", "version": "v1.0.0", **{key: copy.deepcopy(ref) for key in ("zip", "mod", "info")}}]}
               for name in contract.MODULES}
    builds = {name: {"source_commit": plan["sources"][name]["commit"], "compiler_image": plan["images"]["golang-build-image"]["reference"],
                     "runtime_packages": [], "repeat_build": "NOT_RUN",
                     **{key: copy.deepcopy(ref) for key in ("binary", "build_info", "imports", "dynamic_links", "oci_layout", "oci_index", "oci_manifest", "oci_config", "oci_layer", "version_output")}}
              for name in contract.NATIVE}
    indexes = [{"url": row["url"], **{key: copy.deepcopy(ref) for key in ("inrelease", "release", "packages", "signature_log", "keyring", "verifier")}}
               for row in plan["package_indexes"]]
    os_closures = {name: {"image_reference": plan["images"][name]["reference"], "installed": copy.deepcopy(ref),
                          "signed_indexes": copy.deepcopy(indexes), "packages": [{"name": "synthetic-package", "version": "1.0-1", "architecture": "amd64",
                          "index_url": indexes[0]["url"], "archive": copy.deepcopy(ref)}]} for name in contract.OS_IMAGES}
    manifest = {"schema_version": 1, "task_id": contract.TASK, "contract": contract.CONTRACT, "evidence_kind": "synthetic-fixture",
                "identity": dict(IDENTITY), "plan": plan, "plan_sha256": contract.digest(plan), "image_inputs": images,
                "host_tools": {key: copy.deepcopy(ref) for key in ("docker", "git", "python", "github_cli")},
                "builds": builds, "module_closures": modules, "os_closures": os_closures, "profile_bindings": bindings(plan, builds),
                "updater": {"report": copy.deepcopy(ref), "candidate": copy.deepcopy(ref), "check_report": copy.deepcopy(ref),
                            "independent_report": copy.deepcopy(ref), "signature_proof": copy.deepcopy(ref),
                            "active_lock_before": lock_hash, "active_lock_after": lock_hash, "live_sources": True},
                "cleanup": copy.deepcopy(ref), "commands": [{"argv": ["synthetic-only"], "env": {}, "exit_code": 0,
                    "started_at": 1, "finished_at": 2, "stdout": copy.deepcopy(ref), "stderr": copy.deepcopy(ref)}], "runtime": "NOT_RUN"}
    return manifest


def mutations():
    return [("other-task", lambda m: m.update(task_id="E01-01.evidence")),
            ("boolean-version", lambda m: m.update(schema_version=True)),
            ("missing-native-build", lambda m: m["builds"].pop("bor")),
            ("missing-go-closure", lambda m: m["module_closures"].pop("nats-go")),
            ("missing-os-closure", lambda m: m["os_closures"].pop("golang-build-image")),
            ("wrong-source", lambda m: m["builds"]["bor"].update(source_commit="f" * 40)),
            ("wrong-compiler", lambda m: m["builds"]["bsc"].update(compiler_image="golang:latest")),
            ("wrong-binary-hash", lambda m: m["builds"]["geth"]["binary"].update(sha256="f" * 64)),
            ("wrong-binary-size", lambda m: m["builds"]["geth"]["binary"].update(size_bytes=True)),
            ("missing-materialized-binary", lambda m: m["builds"]["geth"]["binary"].update(path="missing.bin")),
            ("escaping-materialized-binary", lambda m: m["builds"]["geth"]["binary"].update(path="../outside.bin")),
            ("wrong-plan-hash", lambda m: m.update(plan_sha256="0" * 64)),
            ("wrong-lock", lambda m: m["plan"].update(lock_sha256="0" * 64)),
            ("profile-swap", lambda m: m["profile_bindings"][0].update(native_component="bsc")),
            ("missing-profile", lambda m: m["profile_bindings"].pop()),
            ("shared-polygon-l1", lambda m: m["profile_bindings"][-1]["supporting_l1"].update(dedicated_instance=False)),
            ("active-lock-mutation", lambda m: m["updater"].update(active_lock_after="0" * 64)),
            ("missing-live-sources", lambda m: m["updater"].update(live_sources=False)),
            ("failed-command", lambda m: m["commands"][0].update(exit_code=1)),
            ("boolean-exit", lambda m: m["commands"][0].update(exit_code=False)),
            ("unproved-runtime", lambda m: m.update(runtime="PASS")),
            ("unproved-repeat", lambda m: m["builds"]["prysm"].update(repeat_build="PASS")),
            ("unknown-manifest-field", lambda m: m.update(unknown="not accepted"))]
