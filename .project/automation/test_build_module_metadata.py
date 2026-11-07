"""Synthetic checksum evidence and an offline Go relative-replacement control."""

import copy
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import build_contract as contract
import build_execution as execution
import build_materials as materials


MOD = b"module example.org/native\n\ngo 1.20\n"
OLD = "example.org/old v0.9.0/go.mod h1:" + "C" * 43 + "=\n"
SELECTED = "example.org/dep v1.2.3 h1:" + "A" * 43 + "=\nexample.org/dep v1.2.3/go.mod h1:" + "B" * 43 + "=\n"
EXTRA = "example.org/graph v0.7.0/go.mod h1:" + "D" * 43 + "=\n"


class ModuleMetadataTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.source = self.root / "sources/geth/tree"
        self.source.mkdir(parents=True)
        self.archive = self.source.parent / "source.tar"
        with tarfile.open(self.archive, "w") as archive:
            for name, data in {"go.mod": MOD, "go.sum": OLD.encode(), "main.go": b"package main\n"}.items():
                entry = tarfile.TarInfo(name)
                entry.size = len(data)
                archive.addfile(entry, io.BytesIO(data))
                (self.source / name).write_bytes(data)
        self.runner = execution.Runner.__new__(execution.Runner)
        self.runner.root = self.root
        self.runner.commands = []
        self.metadata = self.source.parent / "metadata"
        self.container = "synthetic-geth-container"

    def command(self, tail, data, env=None):
        number = len(self.runner.commands)
        log = self.root / "logs" / str(number)
        log.parent.mkdir(exist_ok=True)
        log.write_bytes(data)
        error = log.with_suffix(".stderr")
        error.write_bytes(b"")
        arguments = [part for key, value in sorted((env or {}).items()) for part in ("--env", key + "=" + value)]
        row = {"argv": ["docker", "container", "exec", *arguments, self.container, *tail], "env": {}, "exit_code": 0,
               "started_at": number + 1, "finished_at": number + 1,
               "stdout": contract.reference(self.root, log), "stderr": contract.reference(self.root, error)}
        self.runner.commands.append(row)
        return row["stdout"]

    def fixture(self):
        self.runner.prepare_module_metadata("geth", self.archive)
        env = execution.go_environment("geth", "linux/amd64", 1)
        downloads = [{"Path": "example.org/dep", "Version": "v1.2.3", "Sum": "h1:" + "A" * 43 + "=",
                      "GoModSum": "h1:" + "B" * 43 + "="}]
        graph = [{"Path": "example.org/native", "Main": True, "Dir": "/source",
                  "GoMod": "/proof/sources/geth/metadata/go.mod"}, {"Path": "example.org/dep", "Version": "v1.2.3"}]
        row = {"source_archive": contract.reference(self.root, self.archive)}
        for field, tail, values in (("downloads", ["mod", "download", "-json", "all"], downloads),
                                    ("modules", ["list", "-m", "-json", "all"], graph),
                                    ("verification", ["mod", "verify"], None)):
            data = b"".join(contract.canonical(item) for item in values) if values is not None else b"all modules verified\n"
            row[field] = self.command(["/usr/local/go/bin/go", *tail], data, env)
        (self.metadata / "go.sum").write_text(OLD + SELECTED + EXTRA)
        for name in ("go.mod", "go.sum"):
            self.command(["cat", "/proof/sources/geth/metadata/" + name], (self.metadata / name).read_bytes())
        return row

    def validate(self, row):
        materials.module_metadata("geth", row, self.root, self.runner.commands)

    def rewrite_capture(self, name, data):
        (self.metadata / name).write_bytes(data)
        command = next(row for row in self.runner.commands if row["argv"][-2:] == ["cat", "/proof/sources/geth/metadata/" + name])
        path = self.root / command["stdout"]["path"]
        path.write_bytes(data)
        command["stdout"] = contract.reference(self.root, path)

    def test_each_native_graph_has_an_explicit_isolated_modfile(self):
        for identifier in contract.MODULES:
            env = execution.go_environment(identifier, "linux/amd64", 1)
            self.assertEqual(env["GOFLAGS"], "-mod=readonly -modfile=/proof/sources/" + identifier + "/metadata/go.mod")
            self.assertEqual(env["GOSUMDB"], "sum.golang.org")
            self.assertEqual(env["GOPRIVATE"], "")
            self.assertEqual(env["GONOSUMDB"], "")

    def test_copies_leave_source_and_archive_unchanged(self):
        archive_hash = contract.file_hash(self.archive)
        row = self.fixture()
        self.validate(row)
        self.assertEqual(contract.file_hash(self.archive), archive_hash)
        self.assertEqual((self.source / "go.mod").read_bytes(), MOD)
        self.assertEqual((self.source / "go.sum").read_text(), OLD)
        self.assertEqual((self.source / "main.go").read_bytes(), b"package main\n")
        self.assertEqual((self.metadata / "go.mod").read_bytes(), MOD)
        with self.assertRaises(FileExistsError):
            self.runner.prepare_module_metadata("geth", self.archive)

    def test_source_container_mount_remains_read_only(self):
        self.runner.plan = {"limits": dict(contract.DEFAULT_LIMITS), "platform": "linux/amd64"}
        self.runner.spec, self.runner.inventory = {}, self.root / "inventory.json"
        with patch.object(self.runner, "remaining", return_value=60), patch.object(self.runner, "run"), \
                patch.object(execution.build_resources, "create", return_value="synthetic-container") as create:
            self.runner.container("go-geth", "synthetic-image", self.source)
        options = create.call_args.args[4]
        self.assertIn("type=bind,src=" + str(self.source) + ",dst=/source,readonly", options)
        self.assertEqual(options[options.index("--workdir") + 1], "/source")

    def test_archived_metadata_rejects_links_duplicates_and_oversized_inputs(self):
        for defect in ("link", "duplicate", "oversized", "absent-sum"):
            path = self.root / (defect + ".tar")
            with tarfile.open(path, "w") as archive:
                entry = tarfile.TarInfo("go.mod")
                data = MOD
                if defect == "link":
                    entry.type, entry.linkname = tarfile.SYMTYPE, "../elsewhere/go.mod"
                    archive.addfile(entry)
                else:
                    if defect == "oversized":
                        data += b" " * (1024 * 1024)
                    entry.size = len(data)
                    archive.addfile(entry, io.BytesIO(data))
                    if defect == "duplicate":
                        archive.addfile(entry, io.BytesIO(data))
            if defect == "absent-sum":
                self.assertEqual(materials.archived_module_metadata(path), {"go.mod": MOD, "go.sum": b""})
            else:
                with self.subTest(defect=defect), self.assertRaises(ValueError):
                    materials.archived_module_metadata(path)

    def test_changed_module_file_and_removed_or_conflicting_sums_fail(self):
        row = self.fixture()
        for name, data in (("go.mod", MOD + b"require example.org/other v2.0.0\n"),
                           ("go.sum", (SELECTED + EXTRA).encode()),
                           ("go.sum", (OLD.replace("C" * 43, "E" * 43) + SELECTED + EXTRA).encode()),
                           ("go.sum", (OLD + SELECTED + EXTRA + OLD.replace("C" * 43, "E" * 43)).encode())):
            with self.subTest(name=name, data=data):
                self.rewrite_capture(name, data)
                with self.assertRaises(ValueError):
                    self.validate(row)
                self.rewrite_capture("go.mod", MOD)
                self.rewrite_capture("go.sum", (OLD + SELECTED + EXTRA).encode())

    def test_selected_hashes_are_required_and_only_extra_mod_sums_are_allowed(self):
        row = self.fixture()
        for sums in (OLD + EXTRA, OLD + SELECTED.replace("A" * 43, "E" * 43) + EXTRA,
                     OLD + SELECTED + EXTRA.replace("/go.mod", "")):
            self.rewrite_capture("go.sum", sums.encode())
            with self.subTest(sums=sums), self.assertRaises(ValueError):
                self.validate(row)
        self.rewrite_capture("go.sum", (OLD + SELECTED + EXTRA).encode())
        self.validate(row)

    def test_capture_must_be_unique_successful_and_component_bound(self):
        row = self.fixture()
        original = copy.deepcopy(self.runner.commands)
        mutations = [lambda commands: commands.pop(), lambda commands: commands.append(copy.deepcopy(commands[-1])),
                     lambda commands: commands[-1].update(exit_code=1),
                     lambda commands: commands[-1]["argv"].__setitem__(-1, "/proof/sources/bor/metadata/go.sum"),
                     lambda commands: commands[-1]["argv"].__setitem__(-3, "another-container")]
        for mutate in mutations:
            self.runner.commands = copy.deepcopy(original)
            mutate(self.runner.commands)
            with self.subTest(mutation=mutate), self.assertRaises(ValueError):
                self.validate(row)

    def test_acquisition_authentication_and_all_go_phases_keep_the_same_modfile(self):
        row = self.fixture()
        original = copy.deepcopy(self.runner.commands)
        for before, after in (("GOSUMDB=sum.golang.org", "GOSUMDB=off"),
                              ("GONOSUMDB=", "GONOSUMDB=*"), ("GOPRIVATE=", "GOPRIVATE=*"),
                              ("GOFLAGS=-mod=readonly -modfile=/proof/sources/geth/metadata/go.mod", "GOFLAGS=-mod=readonly")):
            self.runner.commands = copy.deepcopy(original)
            args = self.runner.commands[0]["argv"]
            args[args.index(before)] = after
            with self.subTest(change=after), self.assertRaises(ValueError):
                self.validate(row)
        self.runner.commands = copy.deepcopy(original)
        env = execution.go_environment("geth", "linux/amd64", 1)
        env.update(GOPROXY="off", GOSUMDB="off")
        self.command(["/usr/local/go/bin/go", "build", "./cmd/geth"], b"", env)
        self.validate(row)
        self.runner.commands[-1]["argv"][self.runner.commands[-1]["argv"].index("GOFLAGS=" + env["GOFLAGS"])] = "GOFLAGS=-mod=mod"
        with self.assertRaises(ValueError):
            self.validate(row)

    def test_post_build_metadata_and_capture_mutation_are_rejected(self):
        row = self.fixture()
        for path in (self.metadata / "go.mod", self.metadata / "go.sum", self.root / self.runner.commands[-1]["stdout"]["path"]):
            data = path.read_bytes()
            path.write_bytes(data + b"\n")
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.validate(row)
            path.write_bytes(data)
        path = self.metadata / "go.sum"
        data = path.read_bytes()
        path.unlink()
        with self.assertRaises(ValueError):
            self.validate(row)
        path.symlink_to(self.root / self.runner.commands[-1]["stdout"]["path"])
        with self.assertRaises(ValueError):
            self.validate(row)
        path.unlink()
        path.write_bytes(data)
        self.validate(row)

    @unittest.skipUnless(shutil.which("go"), "host Go is needed for the offline flag-semantics control")
    def test_real_offline_go_keeps_relative_replacements_at_the_source_root(self):
        local = self.source / "local"
        local.mkdir()
        (local / "go.mod").write_text("module example.org/local\n\ngo 1.20\n")
        module = MOD + b"\nrequire example.org/local v0.0.0\nreplace example.org/local => ./local\n"
        (self.source / "go.mod").write_bytes(module)
        self.metadata.mkdir()
        alternate = self.metadata / "go.mod"
        alternate.write_bytes(module)
        (self.metadata / "go.sum").write_bytes(b"")
        env = {**os.environ, "GOTOOLCHAIN": "local", "GOWORK": "off", "GOENV": "off", "GOPROXY": "off", "GOSUMDB": "off",
               "GOFLAGS": "-mod=readonly -modfile=" + str(alternate), "GOPATH": str(self.root / "gopath"),
               "GOMODCACHE": str(self.root / "modcache"), "GOCACHE": str(self.root / "buildcache")}
        result = subprocess.run([shutil.which("go"), "list", "-m", "-json", "all"], cwd=self.source,
                                env=env, text=True, capture_output=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        graph = materials.json_stream(result.stdout)
        main = next(row for row in graph if row.get("Main"))
        replacement = next(row["Replace"] for row in graph if "Replace" in row)
        self.assertEqual(main["Dir"], str(self.source))
        self.assertEqual(main["GoMod"], str(alternate))
        self.assertEqual(replacement["Dir"], str(local))
        self.assertEqual(replacement["GoMod"], str(local / "go.mod"))
        self.assertEqual((self.source / "go.mod").read_bytes(), module)
        self.assertEqual((self.source / "go.sum").read_text(), OLD)


if __name__ == "__main__":
    unittest.main()
