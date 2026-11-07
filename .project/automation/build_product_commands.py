"""Run candidate protocol commands with retained controls and fixed deadlines."""

import copy
import hashlib
import json
import os
from pathlib import Path
import resource
import subprocess
import sys
import time

import build_contract as contract
import build_update_vectors
import build_updates
import build_vectors


class Commands:
    def __init__(self, candidate, source, directory, deadline):
        self.candidate, self.source, self.directory = (Path(value).resolve() for value in (candidate, source, directory))
        self.directory.mkdir(parents=True, exist_ok=False)
        self.deadline = deadline
        self.outcomes = []
        self.registry, self.lock, self.registry_hash, self.lock_hash = contract.inputs(self.source)
        self.registry_path, self.lock_path = self.directory / "registry.json", self.directory / "active-lock.json"
        self.registry_path.write_bytes((self.source / ".project/bootstrap/components/registry.json").read_bytes())
        self.lock_path.write_bytes((self.source / ".project/bootstrap/components/candidate-lock.json").read_bytes())
        self.input_state = self.fingerprint_inputs()

    def fingerprint_inputs(self):
        result = {}
        for path in (self.registry_path, self.lock_path):
            stat = path.stat()
            result[path.name] = {"sha256": contract.file_hash(path), "size": stat.st_size, "mtime_ns": stat.st_mtime_ns,
                                 "ctime_ns": stat.st_ctime_ns, "inode": stat.st_ino, "mode": stat.st_mode}
        return result

    def call(self, identifier, command, request=None, expected=0, timeout=60):
        contract.require(identifier not in {row["id"] for row in self.outcomes}, "candidate control ID was reused")
        remaining = min(timeout, self.deadline - time.time())
        contract.require(remaining > 0, "candidate control exceeded the original launch deadline")
        stdout_path, stderr_path = (self.directory / (identifier + suffix) for suffix in (".stdout", ".stderr"))
        def bounds():
            resource.setrlimit(resource.RLIMIT_FSIZE, (256 * 1024 * 1024, 256 * 1024 * 1024))
        payload = b"" if request is None else contract.canonical(request)
        with stdout_path.open("xb") as stdout, stderr_path.open("xb") as stderr:
            result = subprocess.run(command, input=payload, stdout=stdout, stderr=stderr, cwd=self.candidate,
                                    timeout=remaining, preexec_fn=bounds)
        contract.require(stdout_path.stat().st_size <= 8 * 1024 * 1024 and stderr_path.stat().st_size <= 8 * 1024 * 1024,
                         "candidate protocol output exceeded its bound")
        row = {"id": identifier, "expected_exit": expected, "actual_exit": result.returncode,
               "command": command, "stdin_sha256": hashlib.sha256(payload).hexdigest(),
               "stdout_sha256": contract.file_hash(stdout_path), "stderr_sha256": contract.file_hash(stderr_path)}
        self.outcomes.append(row)
        (self.directory / "outcomes.json").write_bytes(contract.canonical(self.outcomes))
        contract.require(self.fingerprint_inputs() == self.input_state, "candidate updater mutated an active input, even if its bytes were restored")
        contract.require(result.returncode == expected, "candidate disagreed with independent control: " + identifier)
        return stdout_path.read_bytes()

    def plan_request(self, platform="linux/amd64", limits=None):
        return {"contract": contract.CONTRACT, "kind": "BuildPlan", "registry_path": str(self.registry_path),
                "lock_path": str(self.lock_path), "platform": platform, "limits": limits or dict(contract.DEFAULT_LIMITS)}

    def plans(self):
        command = [sys.executable, "-I", str(self.candidate / "build/native/plan.py")]
        result = None
        for platform in contract.PLATFORMS:
            request = self.plan_request(platform)
            plan = contract.parse_json(self.call("plan-" + platform.split("/")[1], command, request, timeout=10))
            contract.validate_plan(plan, self.source)
            contract.require(plan["platform"] == platform and plan["limits"] == request["limits"], "candidate plan changed the requested platform or resource limit")
            if platform == "linux/amd64":
                result = plan
        request = self.plan_request(limits=dict(contract.DEFAULT_LIMITS, cpus=1, memory_bytes=512 * 1024 ** 2, pids=32, command_seconds=30))
        reduced = contract.parse_json(self.call("plan-bounded-resources", command, request, timeout=10))
        contract.require(reduced == contract.expected_plan(self.registry, self.lock, self.registry_hash, self.lock_hash,
                         "linux/amd64", request["limits"]), "candidate plan ignored configured finite resource limits")
        for identifier, mutation in (("unknown-platform", lambda r: r.update(platform="linux/386")),
                                     ("boolean-cpu", lambda r: r["limits"].update(cpus=True)),
                                     ("unbounded-cpu", lambda r: r["limits"].update(cpus=64)),
                                     ("unknown-field", lambda r: r.update(command=["true"]))):
            request = self.plan_request()
            mutation(request)
            self.call("plan-reject-" + identifier, command, request, expected=2, timeout=10)
        return result

    def preflight(self, identifier, manifest, root, profile="ethereum-mainnet", synthetic=False, expected=0, alter=None):
        root = Path(root)
        path = root / (identifier + ".json")
        path.write_bytes(contract.canonical(manifest))
        request = {"contract": contract.CONTRACT, "kind": "VerifyManifest", "synthetic": synthetic, "root": str(root.resolve()),
                   "manifest_path": path.name, "manifest_sha256": contract.file_hash(path), "profile_id": profile,
                   "registry_path": str(self.registry_path), "lock_path": str(self.lock_path), "identity": copy.deepcopy(manifest["identity"])}
        if alter:
            alter(request)
        value = contract.parse_json(self.call(identifier, [sys.executable, "-I", str(self.candidate / "build/native/verify.py")],
                                             request, expected=expected, timeout=10))
        contract.require(type(value) is dict and set(value) == {"valid", "reason"} and value["valid"] is (expected == 0)
                         and isinstance(value["reason"], str) and value["reason"], "preflight returned a malformed validity report")

    def preflight_controls(self):
        root = self.directory / "synthetic-materials"
        manifest = build_vectors.fixture(root, self.source)
        contract.validate_manifest(manifest, root, self.source, allow_synthetic=True)
        for profile in manifest["plan"]["profiles"]:
            self.preflight("synthetic-accept-" + profile["id"], manifest, root, profile["id"], synthetic=True)
        for identifier, mutation in build_vectors.mutations():
            value = copy.deepcopy(manifest)
            mutation(value)
            self.preflight("synthetic-reject-" + identifier, value, root, synthetic=True, expected=2)
        for key in ("head_sha", "base_sha", "verifier_source_commit"):
            self.preflight("synthetic-reject-identity-" + key, manifest, root, synthetic=True, expected=2,
                           alter=lambda request, field=key: request["identity"].update({field: "f" * 40}))
        self.preflight("synthetic-reject-profile", manifest, root, profile="unknown-profile", synthetic=True, expected=2)
        self.preflight("synthetic-reject-manifest-digest", manifest, root, synthetic=True, expected=2,
                       alter=lambda request: request.update(manifest_sha256="0" * 64))
        self.preflight("synthetic-cannot-be-actual", manifest, root, synthetic=False, expected=2)
        link = root / "linked.bin"
        link.symlink_to(root / "synthetic-only.bin")
        value = copy.deepcopy(manifest)
        value["builds"]["geth"]["binary"]["path"] = "linked.bin"
        self.preflight("synthetic-reject-symlink", value, root, synthetic=True, expected=2)

    def updater(self, identifier, mode, transport=None, expected=0, output_override=None, hash_override=None):
        directory = self.directory / identifier
        directory.mkdir()
        report_path, output = directory / "report.json", directory / "candidate.json"
        if output_override is not None:
            output = Path(output_override)
        command = [sys.executable, "-I", str(self.candidate / "scripts/refresh-versions"), mode,
                   "--registry", str(self.registry_path), "--active-lock", str(self.lock_path),
                   "--expected-lock-sha256", hash_override or self.lock_hash, "--report", str(report_path)]
        if mode == "update":
            command.extend(["--output", str(output)])
        if transport:
            command.extend(["--fixture-transport", str(transport)])
        self.call(identifier, command, expected=expected, timeout=60 if transport else 600)
        if expected == 2:
            if output_override is None:
                contract.require(not output.exists(), "rejected updater invocation wrote a candidate")
            return None
        report = contract.parse_json(contract.safe_file(directory, "report.json").read_bytes())
        validated = build_updates.validate_report(report, directory, self.registry, self.lock, self.registry_hash,
                                                   self.lock_hash, mode, transport is not None, time.time())
        contract.require(validated["complete"] is (expected == 0), "updater exited successfully with an incomplete report, or hid a source failure")
        for row in report["external_locks"]:
            contract.require(row["sha256"] == contract.file_hash(self.source / row["path"]), "updater changed an external toolchain or agent lock")
        if expected == 0 and mode == "update":
            contract.require(contract.verify_reference(directory, report["candidate"]) == output.resolve(), "updater wrote another candidate path")
        else:
            contract.require(not output.exists(), "check or failed source discovery wrote a candidate lock")
        return {"report": report, "path": report_path, "directory": directory, "validated": validated, "candidate": output}

    def updater_controls(self):
        base = build_update_vectors.fixture(self.directory / "synthetic-update-sources", self.source)
        self.updater("synthetic-update-check", "check", base["transport"])
        self.updater("synthetic-update-create", "update", base["transport"])
        for name, status, payload in (("unavailable", 503, b"synthetic unavailable source\n"),
                                      ("malformed", 200, b"not JSON\n"),
                                      ("unstable-only", 200, contract.canonical([{"tag_name": "v999.0.0-rc.1", "draft": False, "prerelease": True}]))):
            fixture = build_update_vectors.fixture(self.directory / ("synthetic-update-" + name + "-sources"), self.source)
            transport = contract.parse_json(fixture["transport"].read_bytes())
            row = transport["observations"][0]
            path = contract.verify_reference(fixture["root"], row["body"])
            path.write_bytes(payload)
            row.update(status=status, body=contract.reference(fixture["root"], path))
            fixture["transport"].write_bytes(contract.canonical(transport))
            self.updater("synthetic-update-reject-" + name, "update", fixture["transport"], expected=1)
        self.updater("synthetic-update-reject-active-target", "update", base["transport"], expected=2, output_override=self.lock_path)
        self.updater("synthetic-update-reject-stale-lock", "update", base["transport"], expected=2, hash_override="0" * 64)
