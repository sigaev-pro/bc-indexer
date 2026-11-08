"""Independent controls for the fixed Bor file-target import graph."""

import copy
import io
from pathlib import Path
import tarfile
import tempfile
import unittest

import build_materials as materials


MODULE = "github.com/ethereum/go-ethereum"
COMMIT = "a" * 40
ARGUMENTS = ["/usr/local/go/bin/go", "list", "-deps", "-json", "-trimpath", "-buildvcs=false",
             "-mod=readonly", "-p", "4", "-pgo=prod.pprof", "-ldflags",
             "-X " + MODULE + "/params.GitCommit=" + COMMIT, "./cmd/cli/main.go"]


class FileTargetTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.archive = self.root / "source.tar"
        self.write_archive()
        self.rows = [{"ImportPath": "fmt", "Standard": True},
                     {"ImportPath": MODULE + "/internal/cli", "Module": {"Path": MODULE, "Main": True, "Dir": "/source"}},
                     {"ImportPath": "example.org/dependency", "Module": {"Path": "example.org/dependency", "Dir": "/proof/cache/bor/mod/dependency"}},
                     {"ImportPath": "command-line-arguments", "Name": "main", "Dir": "/source/cmd/cli",
                      "GoFiles": ["main.go"], "Match": ["./cmd/cli/main.go"]}]

    def write_archive(self, *, module=MODULE, commit=COMMIT, target="regular"):
        with tarfile.open(self.archive, "w", format=tarfile.PAX_FORMAT, pax_headers={"comment": commit}) as archive:
            for name, data in {"go.mod": ("module " + module + "\n").encode(),
                               "cmd/cli/main.go": b"package main\nfunc main() {}\n"}.items():
                if name.endswith("main.go") and target == "missing":
                    continue
                entry = tarfile.TarInfo(name)
                if name.endswith("main.go") and target == "symlink":
                    entry.type, entry.linkname = tarfile.SYMTYPE, "../../outside.go"
                    archive.addfile(entry)
                else:
                    entry.size = len(data)
                    archive.addfile(entry, io.BytesIO(data))
                    if name.endswith("main.go") and target == "duplicate":
                        archive.addfile(entry, io.BytesIO(data))
            if target == "ancestor-link":
                entry = tarfile.TarInfo("cmd/cli")
                entry.type, entry.linkname = tarfile.SYMTYPE, "../../outside"
                archive.addfile(entry)

    def validate(self, rows=None, *, identifier="bor", module=MODULE, arguments=None, commit=COMMIT, archive=True):
        return materials.imported_packages(self.rows if rows is None else rows, module, identifier,
                                           archive=self.archive if archive else None,
                                           arguments=ARGUMENTS if arguments is None else arguments,
                                           source_commit=commit)

    def test_exact_file_target_keeps_a_separate_selected_module_proof(self):
        self.validate()
        with self.assertRaisesRegex(ValueError, "selected native root"):
            self.validate([self.rows[-1]])
        with self.assertRaises(ValueError):
            self.validate(self.rows[:-1])

    def test_file_target_identity_is_exact_and_not_dependency_only(self):
        for change in ({"ImportPath": "other-command"}, {"Name": "library"}, {"Dir": "/source/other"},
                       {"Dir": "/source/cmd/../cmd/cli"}, {"GoFiles": ["other.go"]},
                       {"GoFiles": ["main.go", "extra.go"]}, {"GoFiles": ["/source/cmd/cli/main.go"]},
                       {"GoFiles": ["../main.go"]}, {"Match": ["./cmd/cli"]},
                       {"Match": ["./cmd/cli/main.go", "./other.go"]}, {"DepOnly": True},
                       {"Standard": True}, {"Incomplete": True}, {"Error": {"Err": "failed"}},
                       {"DepsErrors": [{"Err": "failed"}]}, {"Module": {"Path": MODULE, "Main": True, "Dir": "/source"}}):
            rows = copy.deepcopy(self.rows)
            rows[-1].update(change)
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.validate(rows)
        for field in ("CgoFiles", "CompiledGoFiles", "CFiles", "CXXFiles", "MFiles", "HFiles", "FFiles",
                      "SFiles", "SwigFiles", "SwigCXXFiles", "SysoFiles", "EmbedFiles", "TestGoFiles", "XTestGoFiles"):
            rows = copy.deepcopy(self.rows)
            rows[-1][field] = ["unexpected"]
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.validate(rows)

    def test_file_target_requires_bor_and_its_archived_source_identity(self):
        for identifier in ("geth", "bsc", "prysm", "heimdall"):
            with self.subTest(identifier=identifier), self.assertRaises(ValueError):
                self.validate(identifier=identifier)
        for kwargs in ({"module": "other/native"}, {"commit": "b" * 40}, {"commit": None}, {"archive": False}):
            with self.subTest(kwargs=kwargs), self.assertRaises(ValueError):
                self.validate(**kwargs)
        for kwargs in ({"module": "other/native"}, {"commit": "b" * 40}, {"target": "missing"},
                       {"target": "symlink"}, {"target": "duplicate"}, {"target": "ancestor-link"}):
            self.write_archive(**kwargs)
            with self.subTest(archive=kwargs), self.assertRaises(ValueError):
                self.validate()

    def test_file_target_rejects_changed_native_command(self):
        for index, value in ((0, "/other/go"), (1, "build"), (2, "-m"), (-1, "./cmd/cli"),
                             (-1, "./cmd/cli/other.go"), (-1, "/source/cmd/cli/main.go"),
                             (-2, "-X other/native.Commit=" + COMMIT),
                             (-2, "-X " + MODULE + "/params.GitCommit=" + "b" * 40)):
            arguments = list(ARGUMENTS)
            arguments[index] = value
            with self.subTest(index=index, value=value), self.assertRaises(ValueError):
                self.validate(arguments=arguments)
        for arguments in ([], ARGUMENTS + ["./other.go"], [x for x in ARGUMENTS if x != "-pgo=prod.pprof"],
                          ARGUMENTS[:-1] + ["-modfile=/outside/go.mod", ARGUMENTS[-1]]):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                self.validate(arguments=arguments)

    def test_file_target_does_not_own_other_packages_or_relax_isolation(self):
        changes = [lambda r: r.append(copy.deepcopy(r[-1])),
                   lambda r: r.append({"ImportPath": "unowned/package"}),
                   lambda r: r[2].pop("Module"),
                   lambda r: r[2]["Module"].update(Dir="/proof/cache/geth/mod/dependency"),
                   lambda r: r[2].update(ImportPath="github.com/nats-io/nats.go"),
                   lambda r: r[2]["Module"].update(Path="github.com/nats-io/nats.go"),
                   lambda r: r[1]["Module"].update(Dir="/other/source")]
        for change in changes:
            rows = copy.deepcopy(self.rows)
            change(rows)
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.validate(rows)


if __name__ == "__main__":
    unittest.main()
