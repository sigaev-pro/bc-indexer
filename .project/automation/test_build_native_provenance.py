"""Synthetic native provenance controls; no compiler or container is executed."""

import copy
import hashlib
import io
import json
from pathlib import Path
import struct
import tarfile
import tempfile
import unittest

import build_contract as contract
import build_execution as execution
import build_materials as materials


COMMIT = "a" * 40


class NativeProvenanceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()

    def artifact(self, path, data):
        path = self.root / path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return contract.reference(self.root, path)

    def command(self, argv, data=b""):
        number = len(self.commands)
        row = {"argv": argv, "env": {}, "exit_code": 0, "started_at": number + 1, "finished_at": number + 1,
               "stdout": self.artifact(f"logs/{number}.stdout", data), "stderr": self.artifact(f"logs/{number}.stderr", b"")}
        self.commands.append(row)
        return row["stdout"]

    def inside(self, owner, argv, data=b"", environment=None):
        args = [part for key, value in sorted((environment or {}).items()) for part in ("--env", key + "=" + value)]
        return self.command(["docker", "container", "exec", *args, owner, *argv], data)

    def fixture(self, identifier):
        self.identifier, self.commands = identifier, []
        self.module = "github.com/ethereum/go-ethereum" if identifier == "bor" else "example.org/" + identifier
        mod = ("module " + self.module + "\n").encode()
        archive = self.root / "source.tar"
        with tarfile.open(archive, "w", format=tarfile.PAX_FORMAT, pax_headers={"comment": COMMIT}) as stream:
            for name, data in {"go.mod": mod, "go.sum": b"", "cmd/cli/main.go": b"package main\nfunc main() {}\n"}.items():
                entry = tarfile.TarInfo(name)
                entry.size, entry.mtime = len(data), 1
                stream.addfile(entry, io.BytesIO(data))
        pin = {"commit": COMMIT, "version": "v1.2.3", "dependency_inputs": [
            {"path": "go.mod", "sha256": hashlib.sha256(mod).hexdigest()},
            {"path": "go.sum", "sha256": hashlib.sha256(b"").hexdigest()}]}
        self.plan = {"sources": {identifier: pin}, "compiler": {"version": "go1.27.3"}, "platform": "linux/amd64",
                     "limits": {"cpus": 2}, "lock_sha256": "b" * 64,
                     "images": {"debian-runtime-image": {"reference": "debian@sha256:" + "c" * 64}}}
        self.closure = {"source_commit": COMMIT, "source_archive": contract.reference(self.root, archive),
                        "local_modules": self.artifact("local-modules.json", b"{}\n")}
        env = execution.go_environment(identifier, "linux/amd64", 1)
        self.owner = identifier + "-compiler"
        graph = [{"Path": self.module, "Main": True, "Dir": "/source",
                  "GoMod": "/proof/sources/" + identifier + "/metadata/go.mod"}]
        if identifier == "heimdall":
            graph += [{"Path": "github.com/cosmos/cosmos-sdk", "Version": "v0.1.0"},
                      {"Path": "github.com/cometbft/cometbft", "Version": "v0.3.0"}]
        self.closure["downloads"] = self.inside(self.owner, ["/usr/local/go/bin/go", "mod", "download", "-json", "all"], b"", env)
        self.closure["modules"] = self.inside(self.owner, ["/usr/local/go/bin/go", "list", "-m", "-json", "all"], b"".join(contract.canonical(row) for row in graph), env)
        self.closure["verification"] = self.inside(self.owner, ["/usr/local/go/bin/go", "mod", "verify"], b"all modules verified\n", env)
        for name, data in (("go.mod", mod), ("go.sum", b"")):
            relative = "sources/" + identifier + "/metadata/" + name
            self.artifact(relative, data)
            self.inside(self.owner, ["cat", "/proof/" + relative], data)
        self.arguments = execution.build_arguments(identifier, pin, 1, graph, 2)
        imported = [self.arguments[0], "list", "-deps", "-json", *self.arguments[2:self.arguments.index("-o")], self.arguments[-1]]
        offline = {**env, "GOPROXY": "off", "GOSUMDB": "off"}
        if identifier == "prysm":
            offline["CGO_CFLAGS"] = "-O2 -D__BLST_PORTABLE__"
        rows = [{"ImportPath": self.module + "/internal/value", "Module": graph[0]}]
        if identifier == "bor":
            rows.append({"ImportPath": "command-line-arguments", "Name": "main", "Dir": "/source/cmd/cli",
                         "GoFiles": ["main.go"], "Match": ["./cmd/cli/main.go"]})
        self.build = {"source_commit": COMMIT, "imports": self.inside(self.owner, imported, b"".join(contract.canonical(row) for row in rows), offline)}
        self.inside(self.owner, self.arguments, environment=offline)
        header = bytearray(64)
        header[:6] = b"\x7fELF\x02\x01"
        struct.pack_into("<H", header, 18, 62)
        relative = "binaries/" + identifier + "/" + execution.NAMES[identifier]
        self.build["binary"] = self.artifact(relative, bytes(header))
        self.info = "/proof/" + relative + ": go1.27.3\n"
        self.info += "\tpath\t" + ("command-line-arguments" if identifier == "bor" else self.module + "/" + self.arguments[-1][2:]) + "\n"
        self.info += ("\tdep\t" if identifier == "bor" else "\tmod\t") + self.module + "\t(devel)\t\n"
        self.info += "\tbuild\tGOOS=linux\n\tbuild\tGOARCH=amd64\n\tbuild\tCGO_ENABLED=1\n\tbuild\t-trimpath=true\n"
        self.build["build_info"] = self.inside(self.owner, ["/usr/local/go/bin/go", "version", "-m", "/proof/" + relative], self.info.encode(), env)
        runtime = identifier + "-runtime"
        runtime_binary = "/usr/local/bin/" + execution.NAMES[identifier]
        self.command(["docker", "container", "cp", str(self.root / relative), runtime + ":" + runtime_binary])
        version = COMMIT + " " + pin["version"]
        if identifier == "heimdall":
            version = json.dumps({"commit": COMMIT, "version": "1.2.3", "name": "heimdall", "server_name": "heimdalld"})
        self.build["version_output"] = self.inside(runtime, [runtime_binary, *materials.version_arguments(identifier)], version.encode())
        layer = self.root / "layer.tar"
        with tarfile.open(layer, "w") as stream:
            entry = tarfile.TarInfo(runtime_binary.lstrip("/")); entry.size = len(header)
            stream.addfile(entry, io.BytesIO(header))
        self.build["oci_layer"] = contract.reference(self.root, layer)
        config = {"architecture": "amd64", "os": "linux", "rootfs": {"type": "layers", "diff_ids": ["sha256:" + self.build["oci_layer"]["sha256"]]},
                  "config": {"Entrypoint": [runtime_binary], "Labels": {"io.extractor.source-commit": COMMIT,
                  "io.extractor.lock-sha256": self.plan["lock_sha256"], "io.extractor.base-image": self.plan["images"]["debian-runtime-image"]["reference"]}}}
        self.build["oci_config"] = self.artifact("config.json", contract.canonical(config))
        def descriptor(field, media):
            ref = self.build[field]
            return {"mediaType": media, "digest": "sha256:" + ref["sha256"], "size": ref["size_bytes"]}
        manifest = {"schemaVersion": 2, "config": descriptor("oci_config", "application/vnd.oci.image.config.v1+json"),
                    "layers": [descriptor("oci_layer", "application/vnd.oci.image.layer.v1.tar")]}
        self.build["oci_manifest"] = self.artifact("manifest.json", contract.canonical(manifest))
        index = {"schemaVersion": 2, "manifests": [{**descriptor("oci_manifest", "application/vnd.oci.image.manifest.v1+json"),
                  "platform": {"os": "linux", "architecture": "amd64"}}]}
        self.build["oci_index"] = self.artifact("index.json", contract.canonical(index))
        self.build["oci_layout"] = self.artifact("oci-layout", b'{"imageLayoutVersion":"1.0.0"}')

    def validate(self):
        materials.native_build(self.identifier, self.build, self.closure, self.root, self.plan, self.commands)

    def replace_output(self, field, value):
        original = self.build[field]
        command = next(row for row in self.commands if row["stdout"] == original)
        ref = self.artifact(original["path"], value.encode())
        self.build[field] = command["stdout"] = ref

    def test_all_native_clients_bind_missing_flags_to_real_command_and_version_records(self):
        for identifier in contract.NATIVE:
            with self.subTest(identifier=identifier):
                self.fixture(identifier)
                with self.assertRaises(ValueError):
                    materials.build_info(self.info, "go1.27.3", self.module, COMMIT, "linux/amd64")
                self.validate()
                flags = self.arguments[self.arguments.index("-ldflags") + 1]
                self.replace_output("build_info", self.info + "\tbuild\t-ldflags=" + json.dumps(flags) + "\n")
                self.validate()

    def test_serialized_or_other_client_context_cannot_admit_a_file_target(self):
        self.fixture("bor")
        for proof in ({}, {"identifier": "bor", "commit": COMMIT}, object()):
            with self.subTest(proof=proof), self.assertRaises(ValueError):
                materials.build_info(self.info, "go1.27.3", self.module, COMMIT, "linux/amd64", proof=proof)
        self.fixture("geth")
        altered = self.info.replace(self.module + "/cmd/geth", "command-line-arguments").replace("\tmod\t", "\tdep\t")
        self.replace_output("build_info", altered)
        with self.assertRaises(ValueError):
            self.validate()

    def test_bor_file_target_metadata_cannot_hide_ambiguous_or_foreign_roots(self):
        self.fixture("bor")
        original = self.info
        root = "\tdep\t" + self.module + "\t(devel)\t\n"
        changes = [original.replace("command-line-arguments", self.module + "/cmd/cli"),
                   original.replace(root, ""), original + root,
                   original.replace(root, root.replace(self.module, "example.org/foreign")),
                   original.replace(root, root.replace("(devel)", "v1.2.3")),
                   original + "\tdep\texample.org/foreign\t(devel)\n",
                   original.replace(root, root + "\t=>\texample.org/foreign\tv1.0.0\n"),
                   original + "\tmod\t" + self.module + "\t(devel)\n",
                   original + "\tdep\tgithub.com/nats-io/nats.go\tv1.0.0\n"]
        for value in changes:
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.replace_output("build_info", value)
                self.validate()

    def test_native_proof_rejects_changed_recipe_source_and_artifact_bindings(self):
        self.fixture("geth")
        saved = copy.deepcopy((self.build, self.closure, self.commands, self.plan))
        changes = [lambda: self.build.update(source_commit="b" * 40),
                   lambda: self.closure.update(source_commit="b" * 40),
                   lambda: self.build.pop("version_output"),
                   lambda: self.build.update(version_output=self.build["build_info"]),
                   lambda: self.build.update(build_info=self.build["version_output"]),
                   lambda: self.build.update(binary=self.artifact("other-binary", b"other")),
                   lambda: self.commands[6]["argv"].__setitem__(-4, "-X other.Commit=" + COMMIT),
                   lambda: self.commands[5]["argv"].__setitem__(-2, "-X other.Commit=" + COMMIT),
                   lambda: self.commands[7]["argv"].__setitem__(-1, "/proof/binaries/other/geth"),
                   lambda: self.commands[9]["argv"].__setitem__(-2, "/usr/local/bin/other"),
                   lambda: self.commands.pop(8),
                   lambda: self.commands.__setitem__(slice(8, 10), list(reversed(self.commands[8:10])))]
        for change in changes:
            self.build, self.closure, self.commands, self.plan = copy.deepcopy(saved)
            with self.subTest(change=change), self.assertRaises(ValueError):
                change()
                self.validate()

    def test_correct_descriptors_do_not_hide_wrong_native_version_or_build_settings(self):
        self.fixture("geth")
        self.replace_output("version_output", "wrong commit " + "b" * 40)
        with self.assertRaisesRegex(ValueError, "exact injected commit"):
            self.validate()
        self.replace_output("version_output", COMMIT)
        for value in (self.info.replace("CGO_ENABLED=1", "CGO_ENABLED=0"),
                      self.info.replace("GOARCH=amd64", "GOARCH=arm64"), self.info.replace("go1.27.3", "go1.27.2"),
                      self.info.replace("-trimpath=true", "-trimpath=false"),
                      self.info + "\tbuild\t-ldflags=-X other.Commit=" + COMMIT + "\n",
                      self.info + "\tpath\tother/ambiguous\n"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.replace_output("build_info", value)
                self.validate()
        self.replace_output("build_info", self.info)
        self.validate()


if __name__ == "__main__":
    unittest.main()
