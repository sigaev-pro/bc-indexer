"""Portable checks for the capture-enabled BSC client image procedure. No native runtime claim."""

import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

import bsc_capture_build as build
from artifacts import Rejected, reference
import runner

RUN = subprocess.run
COMMIT = "54afaff4da31ac9e49ba646a4f65104edac5a2f3"
GOLANG = "docker.io/library/golang@sha256:" + "1" * 64
DEBIAN = "docker.io/library/debian@sha256:" + "2" * 64
IMAGE_ID, BASE_ID = "sha256:" + "3" * 64, "sha256:" + "4" * 64
BASE_LAYER, EXTRA_LAYER = "sha256:" + "5" * 64, "sha256:" + "6" * 64
LAST_LAYER = "sha256:" + "7" * 64
EPOCH = 1793671467  # 2026-11-03T02:04:27Z
TAG = "bc-indexer/bsc-capture:1.8.2-capture"
SELECTED = {"repository": "bnb-chain/bsc", "version": "v1.8.2", "commit": COMMIT, "go_version": "go1.27.3",
            "compiler_image": GOLANG, "runtime_image": DEBIAN}
VERSION = (f"Geth\nVersion: 1.8.2\nGit Commit: {COMMIT}\nArchitecture: amd64\nGo Version: go1.27.3\n"
           "Operating System: linux\nGOPATH=\nGOROOT=\n")
CONFIG = ('[Eth]\nNetworkId = 97\nSyncMode = "full"\nNoPruning = false\nStateScheme = "hash"\n'
          'PruneAncientData = false\n\n[Eth.Miner]\nSyncMode = "snap"\nStateScheme = "path"\n\n[Eth.Miner.Mev]\n'
          'Enabled = false\n')
START_LOG = "\n".join("INFO [11-25|03:10:32.206] " + line for line in build.START_MARKERS) + "\n"
REFUSALS = {"snap-sync": (1, "Fatal: Failed to register the Ethereum service: sync mode snap (bsc.snap-sync)"),
            "foreign-store": (1, "Fatal: native store identity mismatch: store is bound to x"),
            "pruned-history": (1, "Fatal: Failed to register the Ethereum service: native profile violates capture "
                                    "constraints: history.blocks 600000 deletes block data"),
            "path-scheme": (1, "Fatal: Failed to register the Ethereum service: native profile violates capture "
                                  "constraints: state scheme \"\", want \"hash\"; history.blocks 600000 deletes block data")}
FAST = ["--syncmode=full", "--history.transactions=600000", "--history.blocks=600000", "--history.logs=600000",
        "--tries-verify-mode=local"]
SMOKE_MARKERS = {"snap-sync": "bsc.snap-sync", "foreign-store": "native store identity mismatch",
                 "pruned-history": "history.blocks 600000 deletes block data", "path-scheme": 'state scheme "", want "hash"'}
LIBRARIES = ("\tlinux-vdso.so.1 (0x00007ffc1a1b1000)\n\tlibc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x00007f0b5e000000)\n"
             "\t/lib64/ld-linux-x86-64.so.2 (0x00007f0b5e400000)\n")
MISSING = LIBRARIES + "\tlibrocksdb.so.10 => not found\n"
LDD = ["-c", "ldd /usr/local/bin/geth || true"]
FIXTURE = Path(tempfile.gettempdir()).resolve() / "bsc-capture-variant-fixture"
NO_INPUT = {"patches": [], "packages": [], "include": [], "library": [], "link": [], "runtime_libraries": []}


def done(argv, code=0, out=b"", err=b""):
    return subprocess.CompletedProcess(argv, code, out, err)


def relative_reference(path, base):
    """A descriptor of a file below base, relative to base, as the accepted
    manifest holds its artifacts."""
    row = reference(path)
    return {"path": path.relative_to(base).as_posix(), "sha256": row["sha256"], "size_bytes": row["size_bytes"]}


def provenance_rows(variant=None):
    """The provenance of a base build, or of a variant with its applied overlay."""
    patched = [{"patch": {"path": "/p/" + name, "sha256": f"{index:064x}", "size_bytes": 10}, "files": []}
               for index, name in enumerate(build.PATCHES)]
    installed = [{"relative": "extractor/bsc/lifecycle/lifecycle.go", "source": {"sha256": "a" * 64, "size_bytes": 3}}]
    if variant is not None and "rows" in variant:
        patched.append(variant.pop("rows"))
    return build.image_facts(SELECTED, patched, installed, "b" * 40, "go version go1.27.3 linux/amd64",
                             {"sha256": "c" * 64}, {"sha256": "d" * 64}, {"sha256": "e" * 64}, EPOCH, variant)


def described_variant():
    """A complete variant, in the shape variant_of describes, and the row of its applied patch."""
    variant = {"schema_version": 1, "kind": "bsc-capture-image-variant", "name": "collector",
               "patches": [{"path": str(FIXTURE / "09-collector.diff"), "name": "09-collector.diff",
                            "sha256": "1" * 64, "size_bytes": 4}],
               "packages": [{"path": "extractor/collector", "source": {"path": str(FIXTURE / "package"), "sha256": "2" * 64,
                                                                        "files": 2, "size_bytes": 33}}],
               "cgo": {"include": [{"path": str(FIXTURE / "include"), "sha256": "3" * 64, "files": 1, "size_bytes": 9}],
                       "library": [{"path": str(FIXTURE / "lib"), "sha256": "4" * 64, "files": 1, "size_bytes": 16}],
                       "link": ["-lrocksdb"]},
               "runtime_libraries": [{"path": str(FIXTURE / "lib" / "librocksdb.so.10.4.2"), "sha256": "6" * 64,
                                      "size_bytes": 16}],
               "rows": {"patch": {"path": str(FIXTURE / "09-collector.diff"), "sha256": "1" * 64, "size_bytes": 4},
                        "files": [{"path": "extractor/collector/doc.go", "new": True, "before_sha256": None,
                                   "after_sha256": "9" * 64}]}}
    return variant


class TagTest(unittest.TestCase):
    def test_tag_of_an_upstream_version(self):
        self.assertEqual(build.tag_for("v1.8.2"), TAG)

    def test_tag_of_a_variant(self):
        self.assertEqual(build.tag_for("v1.8.2", described_variant()), TAG + "-collector")

    def test_other_versions_are_refused(self):
        for version in ("1.8.2", "v1.8", "v1.8.2-x", "v01.8.2", "main", "", None, 2):
            with self.subTest(version=version), self.assertRaises(Rejected):
                build.tag_for(version)


class LockTest(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp()).resolve()
        self.lock = {"components": {"bsc": {"source_repository": SELECTED["repository"], "version": "v1.8.2",
                                            "commit": COMMIT},
                                    "go": {"version": "go1.27.3"},
                                    "golang-build-image": {"pull_reference": GOLANG},
                                    "debian-runtime-image": {"pull_reference": DEBIAN}}}
        self.plan = {"sources": {"bsc": {"version": "v1.8.2"}}, "compiler": {"version": "go1.27.3"}}
        self.manifest = {"builds": {"bsc": {"source_commit": COMMIT, "compiler_image": GOLANG}},
                         "image_inputs": {"golang-build-image": {"reference": GOLANG},
                                          "debian-runtime-image": {"reference": DEBIAN}},
                         "plan": self.plan}

    def pins(self):
        path = self.root / "candidate-lock.json"
        path.write_text(json.dumps(self.lock))
        self.plan["lock_sha256"] = reference(path)["sha256"]
        return build.lock_pins(path, self.manifest)

    def test_bound_pins(self):
        lock_ref, selected = self.pins()
        self.assertEqual(selected, SELECTED)
        self.assertEqual(lock_ref["sha256"], hashlib.sha256((self.root / "candidate-lock.json").read_bytes()).hexdigest())

    def test_each_difference_is_refused(self):
        changes = [lambda: self.lock["components"]["bsc"].update(commit="0" * 40),
                   lambda: self.lock["components"]["debian-runtime-image"].update(pull_reference="debian@sha256:" + "9" * 64),
                   lambda: self.lock["components"]["golang-build-image"].update(pull_reference=GOLANG[:-1] + "0"),
                   lambda: self.manifest["builds"]["bsc"].update(compiler_image="golang:latest"),
                   lambda: self.manifest["image_inputs"]["debian-runtime-image"].update(reference="debian:13"),
                   lambda: self.lock["components"]["go"].update(version="go1.27.2"),
                   lambda: self.lock["components"]["bsc"].update(version="v1.8.1"),
                   lambda: self.plan["sources"]["bsc"].update(version="v1.8.1"),
                   lambda: self.manifest.pop("plan")]
        for index, change in enumerate(changes):
            original = json.dumps(self.lock), json.dumps(self.manifest)
            change()
            with self.subTest(change=index), self.assertRaises(Rejected):
                self.pins()
            self.lock, self.manifest = json.loads(original[0]), json.loads(original[1])
            self.plan = self.manifest["plan"]

    def test_the_plan_binds_the_lock_by_digest(self):
        path = self.root / "candidate-lock.json"
        path.write_text(json.dumps(self.lock))
        self.plan["lock_sha256"] = "0" * 64
        with self.assertRaisesRegex(Rejected, "candidate lock differs"):
            build.lock_pins(path, self.manifest)


class LabelsTest(unittest.TestCase):
    EXPECTED = {"source-commit": COMMIT, "source-version": "v1.8.2", "go-version": "go1.27.3",
                "compiler-image": GOLANG, "base-image": DEBIAN, "binary-sha256": "c" * 64,
                "lock-sha256": "e" * 64, "harness-commit": "b" * 40,
                "patches": ";".join(f"{name}={index:064x}" for index, name in enumerate(build.PATCHES))}

    def test_labels_hold_the_facts_and_no_local_path(self):
        rows = provenance_rows()
        labels = build.image_labels(rows)
        self.assertEqual({key: labels["io.extractor." + key] for key in self.EXPECTED}, self.EXPECTED)
        self.assertEqual(labels["org.opencontainers.image.version"], "1.8.2-capture")
        self.assertEqual(labels["org.opencontainers.image.revision"], COMMIT)
        self.assertEqual(labels["org.opencontainers.image.source"], "https://github.com/bnb-chain/bsc")
        self.assertEqual(labels["org.opencontainers.image.base.name"], "docker.io/library/debian")
        self.assertEqual(labels["org.opencontainers.image.base.digest"], "sha256:" + "2" * 64)
        self.assertEqual([row["name"] for row in rows["patches"]], list(build.PATCHES))
        self.assertIsNone(rows["variant"])
        self.assertNotIn("/p/", json.dumps(labels))
        self.assertEqual(len(labels), 16)

    def test_variant_labels(self):
        labels = build.image_labels(provenance_rows(described_variant()))
        extra = {key: value for key, value in labels.items() if key not in build.image_labels(provenance_rows())}
        self.assertEqual(extra, {"io.extractor.variant": "collector",
                                 "io.extractor.overlay-patches": "09-collector.diff=" + "1" * 64,
                                 "io.extractor.overlay-packages": "extractor/collector=" + "2" * 64,
                                 "io.extractor.runtime-libraries": "librocksdb.so.10.4.2=" + "6" * 64,
                                 "io.extractor.cgo-include-sha256": "3" * 64,
                                 "io.extractor.cgo-library-sha256": "4" * 64,
                                 "io.extractor.cgo-link": "-lrocksdb"})
        self.assertNotIn(str(FIXTURE), json.dumps(labels))
        self.assertEqual(labels["org.opencontainers.image.version"], "1.8.2-capture-collector")
        self.assertEqual(labels["io.extractor.patches"], self.EXPECTED["patches"])  # patches 01 to 08 keep their label
        self.assertEqual(len(labels), 16 + 7)

    def test_labels_of_a_variant_without_an_overlay_or_shared_library(self):
        variant = described_variant()
        variant.pop("rows")
        variant.update(patches=[], packages=[], runtime_libraries=[])
        variant["cgo"]["link"] = []
        labels = build.image_labels(provenance_rows(variant))
        self.assertEqual([key for key in labels if "overlay" in key or "runtime" in key or "link" in key], [])
        self.assertEqual(labels["io.extractor.cgo-include-sha256"], "3" * 64)
        self.assertEqual(labels["io.extractor.cgo-library-sha256"], "4" * 64)
        self.assertEqual(labels["io.extractor.variant"], "collector")
        self.assertEqual(len(labels), 16 + 3)

    def test_changed_labels_are_reported(self):
        rows = provenance_rows()
        self.assertIsNone(build.check_labels(build.image_labels(rows), rows))
        labels = {**build.image_labels(rows), "io.extractor.source-commit": "0" * 40}
        del labels["org.opencontainers.image.revision"]
        with self.assertRaisesRegex(Rejected, "source-commit.*revision"):
            build.check_labels(labels, rows)


class VersionAndConfigTest(unittest.TestCase):
    def test_version_output(self):
        fields = build.version_fields(VERSION)
        self.assertEqual(fields["Version"], "1.8.2")
        self.assertNotIn("Geth", fields)
        self.assertNotIn("GOPATH", fields)
        self.assertIsNone(build.check_version(fields, SELECTED))
        for wrong in ({"Version": "1.8.1"}, {"Git Commit": "0" * 40}, {"Go Version": "go1.27.2"},
                      {"Architecture": "arm64"}, {"Operating System": "darwin"}):
            with self.subTest(wrong=wrong), self.assertRaises(Rejected):
                build.check_version({**fields, **wrong}, SELECTED)
        with self.assertRaises(Rejected):
            build.version_fields("Version: 1\nVersion: 2\n")

    def test_effective_config_reads_only_the_eth_section(self):
        self.assertEqual(build.config_values(CONFIG, "Eth", build.CONFIG_VALUES), build.CONFIG_VALUES)
        self.assertIsNone(build.check_effective_config(CONFIG))
        for text in (CONFIG.replace('SyncMode = "full"', 'SyncMode = "snap"'),
                     CONFIG.replace('StateScheme = "hash"\n', ""),
                     CONFIG.replace("NoPruning = false", "NoPruning = true"),
                     CONFIG.replace("PruneAncientData = false", "PruneAncientData = true"), "",
                     CONFIG.replace("StateScheme", "GCMode"),
                     CONFIG.replace("[Eth]", "[Node]")):
            with self.subTest(text=text[-80:]), self.assertRaises(Rejected):
                build.check_effective_config(text)
        with self.assertRaises(Rejected):
            build.config_values('[Eth]\nStateScheme = "hash"\nStateScheme = "path"\n', "Eth", build.CONFIG_VALUES)

    def test_shared_libraries_of_the_binary(self):
        self.assertIsNone(build.check_libraries(build.dynamic_libraries(LIBRARIES)))
        self.assertEqual(build.dynamic_libraries(LIBRARIES)["libc.so.6"], "/lib/x86_64-linux-gnu/libc.so.6")
        self.assertNotIn("linux-vdso.so.1", build.dynamic_libraries(LIBRARIES))  # no arrow on that line
        with self.assertRaisesRegex(Rejected, "librocksdb.so.10"):
            build.check_libraries(build.dynamic_libraries(MISSING))
        with self.assertRaisesRegex(Rejected, "lacks"):
            build.check_libraries(build.dynamic_libraries("statically linked\n"))
        with self.assertRaises(Rejected):
            build.dynamic_libraries("\tlibc.so.6 => /lib/libc.so.6 (0x1)\n\tlibc.so.6 => /lib/libc.so.6 (0x1)\n")


class StartTest(unittest.TestCase):
    def test_clean_start(self):
        self.assertIsNone(build.check_start(START_LOG, 0))
        for marker in build.START_MARKERS:
            with self.subTest(marker=marker), self.assertRaises(Rejected):
                build.check_start(START_LOG.replace(marker, "x"), 0)
        with self.assertRaisesRegex(Rejected, "exited with 1"):
            build.check_start(START_LOG, 1)

    def test_refusal(self):
        for name, (code, log) in REFUSALS.items():
            self.assertIsNone(build.check_refusal(log, code, SMOKE_MARKERS[name], name))
        with self.assertRaisesRegex(Rejected, "snap-sync: the start passed"):
            build.check_refusal(REFUSALS["snap-sync"][1], 0, "bsc.snap-sync", "snap-sync")
        with self.assertRaisesRegex(Rejected, "path-scheme: the refusal does not name"):
            build.check_refusal("Fatal: other error", 1, 'state scheme "", want "hash"', "path-scheme")

    def test_start_scripts(self):
        script = build.start_script(["--chapel", "--datadir", "/home"], "start", 20)
        self.assertIn("geth --chapel --datadir /home > /work/start.out 2>&1 &", script)
        self.assertIn('until grep -q "' + build.READY_MARKER + '" /work/start.out', script)
        self.assertIn("\nsleep 20\n", script)
        self.assertIn('kill -INT "$pid" 2>/dev/null\nwait "$pid"\necho $? > /work/start.exit', script)
        self.assertEqual(build.start_script(["--chapel"], "control"),
                         "geth --chapel > /work/control.out 2>&1; echo $? > /work/control.exit")

    def test_profile_and_commands(self):
        self.assertEqual(build.PROFILE, ("--state.scheme", "hash", "--gcmode", "full", "--syncmode", "full"))
        self.assertEqual(build.FAST_NODE, ("--syncmode=full", "--history.transactions=600000", "--history.blocks=600000",
                                           "--history.logs=600000", "--tries-verify-mode=local"))
        self.assertEqual(build.OFFLINE, ("--maxpeers", "0", "--nodiscover", "--port", "0", "--ipcdisable"))
        arguments = build.build_arguments(COMMIT)
        self.assertEqual(arguments[:4], [build.GO, "build", "-trimpath", "-buildvcs=false"])
        self.assertEqual(arguments[4:6], ["-ldflags", "-X github.com/ethereum/go-ethereum/internal/version.gitCommit=" + COMMIT])
        self.assertEqual(arguments[-3:], ["-o", "/work/generated/geth", "./cmd/geth"])
        self.assertEqual(build.capture_config("a" * 32, COMMIT),
                         {"store_id": "a" * 32, "producer_id": "bsc-capture-smoke", "run_id": "run-1",
                          "native_commit": COMMIT, "genesis_file": "/config/genesis.json"})

    def test_patch_set(self):
        rows = [{"patch": {"path": "/adapters/" + name}} for name in build.PATCHES]
        build.check_patch_set(rows)
        for wrong in (rows[:-1], rows[::-1], rows + [{"patch": {"path": "/x/09-other.diff"}}], []):
            with self.assertRaises(Rejected):
                build.check_patch_set(wrong)


class EpochTest(unittest.TestCase):
    def test_source_time_of_the_accepted_vanilla_configuration(self):
        root = Path(tempfile.mkdtemp()).resolve()
        (root / "oci").mkdir()
        config = root / "oci" / "config.json"
        config.write_text(json.dumps({"created": "2026-11-03T02:04:27Z"}))
        manifest = {"builds": {"bsc": {"oci_config": relative_reference(config, root)}}}
        self.assertEqual(build.source_date_epoch(root / "manifest.json", manifest), EPOCH)
        # A created time of another shape is refused, with the bytes still bound to the manifest.
        for created in ("2026-11-03T02:04:27", "yesterday", 17, None):
            config.write_text(json.dumps({"created": created}))
            manifest["builds"]["bsc"]["oci_config"] = relative_reference(config, root)
            with self.subTest(created=created), self.assertRaises(Rejected):
                build.source_date_epoch(root / "manifest.json", manifest)
        config.write_text(json.dumps({"created": "2026-11-03T02:04:27Z"}))
        for descriptor in ({**relative_reference(config, root), "sha256": "0" * 64},
                           {**relative_reference(config, root), "size_bytes": 1},
                           {**relative_reference(config, root), "path": "../" + root.name + "/oci/config.json"},
                           {**reference(config)}):
            with self.subTest(descriptor=descriptor["path"]), self.assertRaises(Rejected):
                build.source_date_epoch(root / "manifest.json", {"builds": {"bsc": {"oci_config": descriptor}}})
        with self.assertRaises(Rejected):
            build.source_date_epoch(root / "manifest.json", {"builds": {"bsc": {}}})


def overlay(root):
    """A complete variant in a temporary directory: an overlay and a C library."""
    patch = root / "09-collector.diff"
    patch.write_text("--- /dev/null\n+++ b/extractor/collector/doc.go\n@@ -0,0 +1 @@\n"
                     "+// Package collector is a test overlay.\n")
    package = root / "package"
    (package / "nested").mkdir(parents=True)
    (package / "collector.go").write_text("package collector\n")
    (package / "nested" / "reader.go").write_text("package nested\n")
    header, library = root / "include", root / "lib"
    header.mkdir()
    library.mkdir()
    (header / "cprobe.h").write_text("int cprobe(void);\n")
    (library / "libcprobe.so").write_bytes(b"\x7fELF" + bytes(12))
    return build.variant_of("collector", [str(patch)], [f"{package}=extractor/collector"], [str(header)],
                            [str(library)], ["-lcprobe"], [str(library / "libcprobe.so")])


class VariantTest(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp()).resolve()

    def test_the_base_build_has_no_variant(self):
        self.assertIsNone(build.variant_of(None, *NO_INPUT.values()))
        self.assertEqual(build.variant_mounts(None), [])
        self.assertEqual(build.variant_environment(None), {})
        self.assertEqual(build.dockerfile(None), build.DOCKERFILE.read_text())
        with self.assertRaisesRegex(Rejected, "needs an overlay"):
            build.variant_of("collector", *NO_INPUT.values())
        with self.assertRaisesRegex(Rejected, "needs --variant"):
            build.variant_of(None, ["x.diff"], *list(NO_INPUT.values())[1:])

    def test_variant_parts(self):
        variant = overlay(self.root)
        patch = reference(self.root / "09-collector.diff")
        package = build.tree_reference(self.root / "package")
        self.assertEqual(variant["patches"], [{"path": str(self.root / "09-collector.diff"), "name": "09-collector.diff",
                                               "sha256": patch["sha256"], "size_bytes": patch["size_bytes"]}])
        self.assertEqual(variant["packages"], [{"path": "extractor/collector", "source": package}])
        self.assertEqual((package["files"], package["size_bytes"]), (2, 33))
        self.assertEqual([row["sha256"] for row in variant["cgo"]["include"]],
                         [build.tree_reference(self.root / "include")["sha256"]])
        self.assertEqual(variant["cgo"]["library"], [build.tree_reference(self.root / "lib")])
        self.assertEqual(variant["cgo"]["link"], ["-lcprobe"])
        self.assertEqual(variant["runtime_libraries"], [reference(self.root / "lib" / "libcprobe.so")])
        self.assertEqual(build.variant_mounts(variant),
                         [(self.root / "include", "/cgo/include/0", True), (self.root / "lib", "/cgo/library/0", True)])
        # Go's own C flags come first: a set CGO_CFLAGS or CGO_LDFLAGS replaces them.
        self.assertEqual(build.variant_environment(variant),
                         {"CGO_CFLAGS": "-O2 -g -I/cgo/include/0", "CGO_LDFLAGS": "-O2 -g -L/cgo/library/0 -lcprobe"})

    def test_a_file_package_and_several_directories(self):
        single = self.root / "single.go"
        single.write_text("package single\n")
        (self.root / "include").mkdir()
        (self.root / "include" / "a.h").write_text("int a(void);\n")
        second = self.root / "second"
        second.mkdir()
        (second / "b.h").write_text("int b(void);\n")
        variant = build.variant_of("single", [], [f"{single}=extractor/single/single.go"], [str(self.root / "include"),
                            str(second)], [], [], [])
        self.assertEqual(variant["packages"], [{"path": "extractor/single/single.go", "source": reference(single)}])
        self.assertEqual(build.variant_environment(variant),
                         {"CGO_CFLAGS": "-O2 -g -I/cgo/include/0 -I/cgo/include/1"})
        self.assertNotIn("CGO_LDFLAGS", build.variant_environment(variant))
        self.assertEqual(build.variant_mounts(variant)[-1], (second, "/cgo/include/1", True))

    def test_invalid_variants_are_refused(self):
        patch = self.root / "x.diff"
        patch.write_text("")
        (self.root / "dir").mkdir()
        (self.root / "01-core-import-seam.diff").write_text("")
        for name, value in (("patches", ["missing.diff"]), ("patches", [str(self.root / "01-core-import-seam.diff")]),
                            ("patches", [str(self.root / "dir")]), ("packages", ["missing=extractor/x"]),
                            ("packages", [f"{patch}=../x"]), ("packages", [f"{patch}=/abs"]),
                            ("packages", [f"{patch}=a/./b"]), ("packages", [f"{patch}=a/", f"{self.root / 'dir'}=a/b"]),
                            ("packages", [f"{self.root / 'dir'}=a/b", f"{self.root / 'dir'}=a"]),
                            ("packages", ["no-separator"]), ("include", ["relative"]), ("include", [str(patch)]),
                            ("link", ["lcprobe"]), ("link", ["-l probe"]), ("runtime_libraries", ["missing.so"])):
            with self.subTest(name=name, value=value), self.assertRaises(Rejected):
                build.variant_of("x", **{**NO_INPUT, name: value})
        (self.root / "dir" / "empty").mkdir()
        with self.assertRaisesRegex(Rejected, "Empty directory"):
            build.variant_of("x", **{**NO_INPUT, "library": [str(self.root / "dir" / "empty")]})
        for name in ("Upper", "with_underscore", "x" * 33, ""):
            with self.subTest(name=name), self.assertRaises(Rejected):
                build.variant_of(name, **{**NO_INPUT, "patches": [str(patch)]})

    def test_overlay_is_applied_on_top(self):
        variant = overlay(self.root)
        source, record = self.root / "tree", self.root / "record"
        (source / "cmd").mkdir(parents=True)
        (source / "cmd" / "main.go").write_text("package main\n\nfunc main() {}\n")
        (source / "extractor").mkdir()
        edit = self.root / "10-edit.diff"
        edit.write_text("--- a/cmd/main.go\n+++ b/cmd/main.go\n@@ -1,3 +1,4 @@\n package main\n \n+var _ = 1\n"
                        " func main() {}\n")
        variant["patches"].append({**reference(edit), "name": edit.name})
        record.mkdir()
        rows = build.apply_variant(source, record, variant)
        self.assertEqual((source / "extractor/collector/doc.go").read_text(), "// Package collector is a test overlay.\n")
        self.assertEqual((source / "extractor/collector/nested/reader.go").read_text(), "package nested\n")
        self.assertEqual((source / "cmd/main.go").read_text(), "package main\n\nvar _ = 1\nfunc main() {}\n")
        self.assertEqual([Path(row["patch"]["path"]).name for row in rows], ["09-collector.diff", "10-edit.diff"])
        self.assertEqual(rows[0]["files"], [{"path": "extractor/collector/doc.go", "new": True, "before_sha256": None,
                                             "after_sha256": hashlib.sha256(b"// Package collector is a test overlay.\n").hexdigest()}])
        self.assertEqual((rows[1]["files"][0]["new"], rows[1]["files"][0]["before_sha256"] is not None), (False, True))
        for retained in ("overlay/packages/extractor/collector/nested/reader.go", "overlay/patches/10-edit.diff"):
            self.assertTrue((record / retained).is_file(), retained)
        with self.assertRaisesRegex(Rejected, "already exists"):
            build.apply_variant(source, record, variant)

    def test_gofmt_receives_go_source_only(self):
        def row(*paths):
            return {"patch": {"path": "/p/x.diff"}, "files": [{"path": path} for path in paths]}
        self.assertEqual(build.gofmt_paths([row("b.go", "a.go"), row("go.mod", "go.sum", "c.go")]),
                         ["a.go", "b.go", "c.go"])
        self.assertEqual(build.gofmt_paths([]), [])
        for path in ("README.md", "Makefile", "go.mod.bak", "sub/go.sum.txt", "x.go/mod.go.bak"):
            with self.subTest(path=path), self.assertRaisesRegex(Rejected, "only"):
                build.gofmt_paths([row("a.go", path)])

    def test_an_overlay_patch_never_finds_a_missing_or_changed_context(self):
        variant = overlay(self.root)
        source, record = self.root / "tree", self.root / "record"
        source.mkdir()
        record.mkdir()
        (source / "extractor").mkdir()
        (source / "extractor" / "collector").mkdir()
        (source / "extractor" / "collector" / "doc.go").write_text("package collector\n")
        variant["packages"] = []
        with self.assertRaises(Rejected):
            build.apply_variant(source, record, variant)  # the patch creates an existing file

    def test_dockerfile_of_a_variant_with_shared_libraries(self):
        text = build.DOCKERFILE.read_text()
        variant = overlay(self.root)
        effective = build.dockerfile(variant)
        self.assertEqual(effective, text.replace("COPY --chmod=0755 geth /usr/local/bin/geth\n",
                                                 "COPY --chmod=0755 geth /usr/local/bin/geth\n" + build.LIBRARIES))
        self.assertLess(effective.index("geth /usr/local/bin/geth"), effective.index("RUN ldconfig"))
        self.assertLess(effective.index("RUN ldconfig"), effective.index("ENTRYPOINT"))
        variant["runtime_libraries"] = []
        self.assertEqual(build.dockerfile(variant), text)


class FakeNative:
    """Stands in for Docker at the Native boundary; writes what each command would write."""

    instances = []
    version, config, control, binary, libraries = VERSION, CONFIG, {}, b"geth", LIBRARIES
    during_build = None  # a test's hook, run while the compiler works

    def __init__(self, output, deadline):
        self.output, self.nonce, self.commands, self.owned, self.deadline = output, "n", [], [], deadline
        self.cleanup_complete = True
        FakeNative.instances.append(self)
        (output / "logs").mkdir()

    def command(self, image, argv, mounts, **kwargs):
        targets = {target: source for source, target, _ in mounts}
        self.owned.append({"image": image, "argv": argv, "mounts": list(mounts),
                           "environment": kwargs.get("environment"), "entrypoint": kwargs.get("entrypoint")})
        self.commands.append({"owned": len(self.owned)})  # one record per owned command, as in native.py
        stdout = self.output / "logs" / f"{len(self.commands):03d}.stdout"
        if argv[:2] == [build.GO, "build"]:
            if FakeNative.during_build is not None:
                FakeNative.during_build()
            (targets["/work/generated"] / "geth").write_bytes(self.binary)
        elif argv == [build.GO, "version"]:
            stdout.write_text("go version go1.27.3 linux/amd64\n")
        elif argv == ["version"]:
            stdout.write_text(self.version)
        elif argv[-1] == "dumpconfig":
            stdout.write_text(self.config)
        elif argv[-1] == "dumpgenesis":
            stdout.write_text(json.dumps({"config": {"chainId": 97}, "alloc": {}}))
        elif argv[1].startswith("ldd "):
            stdout.write_text(self.libraries)
        elif argv[0] == "-c":
            name = argv[-1].split("/work/")[1].split(".out")[0]
            code, text = self.control[name]
            (targets["/work"] / (name + ".exit")).write_text(f"{code}\n")
            (targets["/work"] / (name + ".out")).write_text(text)
        stdout.touch(exist_ok=True)
        return stdout


class FakeDocker:
    """Answers the Docker CLI of the host: inspect, version, build and remove."""

    def __init__(self):
        self.calls, self.labels, self.removed, self.shared = [], build.image_labels(provenance_rows()), [], False

    def answer(self, argv, **kwargs):
        if argv[0] != "docker":
            return RUN(argv, **kwargs)
        self.calls.append(argv)

        def image(identifier):
            # The binary is one layer; shared libraries and ldconfig add two.
            layers = [BASE_LAYER] + ([EXTRA_LAYER] if identifier == IMAGE_ID else []) + \
                ([EXTRA_LAYER, LAST_LAYER] if identifier == IMAGE_ID and self.shared else [])
            return json.dumps([{"Architecture": "amd64", "Os": "linux", "Id": identifier, "Config": {
                "Labels": self.labels, "Entrypoint": ["/usr/local/bin/geth"]}, "RootFS": {"Layers": layers}}]).encode()
        if argv[1] == "ps":
            return done(argv, 0, b"")
        if argv[:3] == ["docker", "image", "inspect"] and "--format" in argv:
            return done(argv, 1, b"", b"Error: No such image")
        if argv[:3] == ["docker", "image", "inspect"] and argv[3] == DEBIAN:
            return done(argv, 0, json.dumps([{"Architecture": "amd64", "Os": "linux", "Id": BASE_ID, "Config": {},
                                              "RepoDigests": ["debian@" + DEBIAN.partition("@")[2]],
                                              "RootFS": {"Layers": [BASE_LAYER]}}]).encode())
        if argv[:2] == ["docker", "version"]:
            return done(argv, 0, b"29.8.2\n")
        if argv[1] == "build":
            position = argv.index("--label")
            self.labels = {}
            while argv[position] == "--label":
                self.labels[argv[position + 1].partition("=")[0]] = argv[position + 1].partition("=")[2]
                position += 2
            self.shared = (Path(argv[-1]) / "libs").is_dir()
            Path(argv[argv.index("--iidfile") + 1]).write_text(IMAGE_ID + "\n")
            return done(argv, 0, b"", b"")
        if argv[:3] == ["docker", "image", "inspect"]:
            return done(argv, 0, image(IMAGE_ID))
        if argv[:3] == ["docker", "image", "rm"]:
            self.removed.append(argv[-1])
            return done(argv, 0, b"")
        raise AssertionError("unexpected Docker command: " + " ".join(argv))

    def __call__(self, argv, **kwargs):
        return FakeDocker.answer(self, argv, **kwargs)


class ProcedureFixture(unittest.TestCase):
    """Temporary roots and fakes; it defines no tests of its own."""

    def setUp(self):
        FakeNative.instances = []
        FakeNative.libraries = LIBRARIES
        self.root = Path(tempfile.mkdtemp()).resolve()
        self.output, self.accepted = self.root / "out", self.root / "accepted"
        (self.accepted / "cache" / "bsc" / "mod").mkdir(parents=True)
        (self.accepted / "oci").mkdir()
        config = self.accepted / "oci" / "config.json"
        config.write_text(json.dumps({"created": "2026-11-03T02:04:27Z"}))
        lock = self.root / "candidate-lock.json"
        lock.write_text(json.dumps({"components": {
            "bsc": {"source_repository": SELECTED["repository"], "version": "v1.8.2", "commit": COMMIT},
            "go": {"version": "go1.27.3"}, "golang-build-image": {"pull_reference": GOLANG},
            "debian-runtime-image": {"pull_reference": DEBIAN}}}))
        manifest = self.accepted / "manifest.json"
        manifest.write_text(json.dumps({
            "evidence_kind": "actual-build", "task_id": "E01-01.builds",
            "builds": {"bsc": {"source_commit": COMMIT, "compiler_image": GOLANG,
                               "oci_config": relative_reference(config, self.accepted)}},
            "image_inputs": {"golang-build-image": {"reference": GOLANG},
                             "debian-runtime-image": {"reference": DEBIAN}},
            "plan": {"lock_sha256": reference(lock)["sha256"], "compiler": {"version": "go1.27.3"},
                     "sources": {"bsc": {"version": "v1.8.2", "commit": COMMIT}}}}))
        self.sha = reference(manifest)["sha256"]
        self.closure = COMMIT
        self.docker = FakeDocker()
        patches = [mock.patch.object(build, "LOCK_PATH", lock),
                   mock.patch.object(build.pins, "source_commit", return_value="b" * 40),
                   mock.patch.object(build.pins, "Native", FakeNative),
                   mock.patch.object(runner, "LOCK", self.root / "runner.lock"),
                   mock.patch.object(build.subprocess, "run", self.docker),
                   mock.patch.object(build.probe14, "extract_source", self.extract),
                   mock.patch.object(build.bsc_lifecycle, "install", self.install)]
        for patch in patches:
            patch.start()
            self.addCleanup(patch.stop)

    def extract(self, manifest, manifest_path, root, deadline):
        source = root / "source"
        source.mkdir()
        (source / "go.mod").write_text("module github.com/ethereum/go-ethereum\n")
        (source / "go.sum").write_text("")
        return {"source_commit": self.closure}, reference(source / "go.mod"), source

    def rewrite_manifest(self, change, bind=True):
        """Change the fixture manifest and return its new pin. With bind, the plan
        keeps naming the lock on disk."""
        manifest = json.loads((self.accepted / "manifest.json").read_text())
        change(manifest)
        if bind:
            manifest["plan"]["lock_sha256"] = reference(self.root / "candidate-lock.json")["sha256"]
        (self.accepted / "manifest.json").write_text(json.dumps(manifest))
        return reference(self.accepted / "manifest.json")["sha256"]

    def fails(self, arrange, message, removed=None):
        """Run a build where `arrange` fed a wrong answer; it must FAIL with `message`."""
        arrange()
        proof = build.build(self.accepted / "manifest.json", self.sha, self.output)
        self.assertEqual((proof["result"], proof["cleanup_complete"]), ("FAIL", True))
        self.assertIn(message, proof["error"])
        if removed is not None:
            self.assertEqual(self.docker.removed, removed)
        return proof

    def install(self, source, root, deadline):
        directory = root / "patches"
        directory.mkdir()
        patched = []
        for index, name in enumerate(build.PATCHES):
            path = directory / name
            path.write_text(name + "\n")
            patched.append({"patch": reference(path), "files": [
                {"path": f"core/file{index}.go", "new": index % 2 == 0, "before_sha256": None,
                 "after_sha256": f"{index:064x}"}]})
        installed = [{"relative": "extractor/bsc/lifecycle/lifecycle.go", "source": reference(source / "go.mod"),
                      "retained": reference(source / "go.mod")}]
        return patched, installed

    @property
    def native(self):
        """The owned container commands of the last run: image, argv, mounts, environment, entry point."""
        return FakeNative.instances[-1].owned


class BuildTest(ProcedureFixture):
    def test_build_passes_and_records_the_provenance(self):
        proof = build.build(self.accepted / "manifest.json", self.sha, self.output)
        self.assertEqual((proof["result"], proof["error"], proof["cleanup_complete"]), ("PASS", None, True))
        self.assertEqual((proof["image"], proof["previous_image_id"], proof["variant"]), (TAG, None, None))
        provenance = json.loads((self.output / "provenance.json").read_text())
        self.assertEqual(provenance["image_id"], IMAGE_ID)
        self.assertEqual(provenance["runtime_image_id"], BASE_ID)
        self.assertEqual(provenance["docker_server_version"], "29.8.2")
        self.assertEqual(provenance["source_date_epoch"], EPOCH)
        self.assertEqual(provenance["upstream"], {"repository": "bnb-chain/bsc", "version": "v1.8.2", "commit": COMMIT})
        self.assertEqual([row["name"] for row in provenance["patches"]], list(build.PATCHES))
        self.assertEqual(provenance["installed"][0]["path"], "extractor/bsc/lifecycle/lifecycle.go")
        self.assertEqual(provenance["effective_config"], build.CONFIG_VALUES)
        self.assertEqual(provenance["version_output"]["Git Commit"], COMMIT)
        self.assertIsNone(provenance["variant"])
        self.assertEqual(provenance["build_environment"], {})
        self.assertEqual(provenance["dynamic_libraries"], build.dynamic_libraries(LIBRARIES))
        binary = self.output / "bsc" / "generated" / "geth"
        self.assertEqual(provenance["binary_sha256"], hashlib.sha256(binary.read_bytes()).hexdigest())
        self.assertEqual(proof["provenance"], reference(self.output / "provenance.json"))
        context = self.output / "image" / "geth"
        self.assertEqual(oct(context.stat().st_mode & 0o777), "0o755")
        self.assertEqual(int(context.stat().st_mtime), EPOCH)
        self.assertFalse((self.output / "image" / "libs").exists())
        self.assertEqual((self.output / "image" / "Dockerfile").read_text(), build.DOCKERFILE.read_text())
        self.assertEqual((self.output / "image-id.txt").read_text().strip(), IMAGE_ID)
        command = next(argv for argv in self.docker.calls if argv[1] == "build")
        self.assertEqual(self.docker.labels, build.image_labels({**provenance}))
        self.assertEqual(command[command.index("--tag") + 1], TAG)
        self.assertEqual(command[command.index("--iidfile") + 1], str(self.output / "image-id.txt"))
        self.assertEqual(command[-1], str(self.output / "image"))
        build_args = [command[index + 1] for index, part in enumerate(command) if part == "--build-arg"]
        self.assertEqual(build_args, ["BASE_IMAGE=" + DEBIAN, "SOURCE_DATE_EPOCH=" + str(EPOCH)])
        self.assertEqual(command[2:7], ["--platform", "linux/amd64", "--pull=false", "--network", "none"])
        self.assertEqual(self.docker.removed, [])
        native = self.native
        self.assertEqual([row["argv"] for row in native[:4]], [
            ["/usr/local/go/bin/gofmt", "-l", "extractor", *sorted(f"core/file{index}.go" for index in range(8))],
            [build.GO, "version"], build.build_arguments(COMMIT), [build.GO, "version", "-m", "/work/generated/geth"]])
        self.assertEqual([(row["image"], row["argv"]) for row in native[4:]],
                         [(TAG, LDD), (TAG, ["version"]),
                          (TAG, ["--chapel", *build.PROFILE, "--datadir", "/work/dumpconfig", "dumpconfig"])])
        self.assertTrue(all(row["image"] == GOLANG for row in native[:4]))
        self.assertEqual([row["entrypoint"] for row in native], [None] * 4 + ["/bin/sh", None, None])
        self.assertEqual([row["environment"] for row in native[:4]], [build.pins.environment()] * 4)
        record = json.loads((self.output / "build.json").read_text())
        self.assertEqual((record["result"], record["source_commit"], record["cleanup_complete"]), ("PASS", "b" * 40, True))
        self.assertEqual(len(record["commands"]), 7 + 5)  # seven owned commands and five host records
        self.assertEqual(set(record["commands"][0]), {"request", "result", "stdout", "stderr"})
        self.assertEqual(json.loads(Path(record["commands"][0]["request"]["path"]).read_text())["argv"][:4],
                         ["docker", "image", "inspect", DEBIAN])

    def test_variant_build_passes_and_records_the_overlay(self):
        variant = overlay(self.root)
        proof = build.build(self.accepted / "manifest.json", self.sha, self.output, variant)
        self.assertEqual((proof["result"], proof["error"]), ("PASS", None))
        tag = TAG + "-collector"
        self.assertEqual((proof["image"], proof["variant"]), (tag, "collector"))
        provenance = json.loads((self.output / "provenance.json").read_text())
        self.assertEqual(provenance["variant"]["name"], "collector")
        self.assertEqual(provenance["variant"]["patches"][0]["files"][0]["path"], "extractor/collector/doc.go")
        self.assertEqual(provenance["installed"][-1], {"path": "extractor/collector", "sha256": provenance["installed"][-1]["sha256"],
                                                        "size_bytes": 33})
        self.assertEqual(provenance["installed"][-1]["sha256"], build.tree_reference(self.root / "package")["sha256"])
        self.assertEqual(provenance["build_environment"],
                         {"CGO_CFLAGS": "-O2 -g -I/cgo/include/0", "CGO_LDFLAGS": "-O2 -g -L/cgo/library/0 -lcprobe"})
        self.assertEqual((self.output / "image" / "libs" / "libcprobe.so").read_bytes(), b"\x7fELF" + bytes(12))
        self.assertEqual(oct((self.output / "image" / "libs" / "libcprobe.so").stat().st_mode & 0o777), "0o755")
        self.assertIn(build.LIBRARIES, (self.output / "image" / "Dockerfile").read_text())
        self.assertEqual(provenance["image_layers"], [BASE_LAYER, EXTRA_LAYER, EXTRA_LAYER, LAST_LAYER])
        command = next(argv for argv in self.docker.calls if argv[1] == "build")
        self.assertEqual(self.docker.labels, build.image_labels({**provenance}))
        self.assertEqual(self.docker.labels["io.extractor.variant"], "collector")
        self.assertEqual(command[command.index("--tag") + 1], tag)
        compile_command = self.native[2]
        self.assertEqual({key: compile_command["environment"][key] for key in ("CGO_CFLAGS", "CGO_LDFLAGS")},
                         provenance["build_environment"])
        self.assertEqual(compile_command["environment"]["GOPROXY"], "off")
        self.assertIn((self.root / "include", "/cgo/include/0", True), compile_command["mounts"])
        self.assertIn((self.root / "lib", "/cgo/library/0", True), compile_command["mounts"])
        self.assertEqual(len(compile_command["mounts"]), 8 + 2)
        self.assertIn("extractor/collector/doc.go", self.native[0]["argv"])
        self.assertEqual((self.output / "bsc" / "source" / "extractor" / "collector" / "doc.go").read_text(),
                         "// Package collector is a test overlay.\n")
        self.assertEqual(self.docker.removed, [])

    def test_a_missing_shared_library_removes_the_tag(self):
        variant = overlay(self.root)
        FakeNative.libraries = MISSING
        proof = build.build(self.accepted / "manifest.json", self.sha, self.output, variant)
        self.assertEqual((proof["result"], self.docker.removed), ("FAIL", [TAG + "-collector"]))
        self.assertIn("librocksdb.so.10", proof["error"])
        self.assertFalse((self.output / "provenance.json").exists())
        self.assertEqual(len(self.native), 5)  # the node was not started on an image that cannot load it

    def test_library_directories_and_link_flags_alone_keep_the_default_flags(self):
        library = self.root / "rocksdb" / "lib"
        library.mkdir(parents=True)
        (library / "librocksdb.a").write_bytes(b"!<arch>\n")
        variant = build.variant_of("rocksdb", [], [], [], [str(library)], ["-lrocksdb", "-Wl,--no-as-needed"], [])
        self.assertEqual(build.variant_environment(variant),
                         {"CGO_LDFLAGS": "-O2 -g -L/cgo/library/0 -lrocksdb -Wl,--no-as-needed"})
        self.assertEqual(build.variant_mounts(variant), [(library, "/cgo/library/0", True)])
        self.assertNotIn("CGO_CFLAGS", build.variant_environment(variant))

    def test_the_command_line_accepts_linker_flags(self):
        header = self.root / "rocksdb" / "include"
        header.mkdir(parents=True)
        (header / "rocksdb.h").write_text("#include <stdint.h>\n")
        base = ["build", "--build-manifest", str(self.accepted / "manifest.json"), "--sha256", self.sha]
        output = self.root / "link"
        self.assertEqual(build.main([*base, "--output", str(output), "--variant", "rocksdb", "--cgo-link", "-lrocksdb",
                                     "--cgo-include", str(header)]), 0)
        provenance = json.loads((output / "provenance.json").read_text())
        self.assertEqual(provenance["variant"]["cgo"]["link"], ["-lrocksdb"])
        self.assertEqual(provenance["build_environment"],
                         {"CGO_CFLAGS": "-O2 -g -I/cgo/include/0", "CGO_LDFLAGS": "-O2 -g -lrocksdb"})
        self.assertEqual(provenance["image"], TAG + "-rocksdb")
        self.assertEqual(build.command_line(["--cgo-link", "-la", "--cgo-link=-lb", "x", "--cgo-link"]),
                         ["--cgo-link=-la", "--cgo-link=-lb", "x", "--cgo-link"])
        with mock.patch("builtins.print"):
            self.assertEqual(build.main([*base, "--output", str(self.root / "none"), "--cgo-link", "-lrocksdb"]), 2)
        self.assertFalse((self.root / "none").exists())

    def test_a_variant_may_patch_the_module_files(self):
        module = self.root / "go.mod.diff"
        module.write_text("--- a/go.mod\n+++ b/go.mod\n@@ -1 +1,3 @@\n module github.com/ethereum/go-ethereum\n"
                          "+\n+require github.com/sigaev-pro/bc-indexer/adapters/ingestclient v0.0.0\n")
        source = self.root / "zz_probe.diff"
        source.write_text('--- /dev/null\n+++ b/cmd/geth/zz_probe.go\n@@ -0,0 +1,3 @@\n+package main\n+\n'
                          '+import _ "github.com/ethereum/go-ethereum/extractor/bsc/probe"\n')
        variant = build.variant_of("probe", [str(module), str(source)], [], [], [], [], [])
        proof = build.build(self.accepted / "manifest.json", self.sha, self.output, variant)
        self.assertEqual((proof["result"], proof["error"]), ("PASS", None))
        self.assertEqual(self.native[0]["argv"], ["/usr/local/go/bin/gofmt", "-l", "extractor", "cmd/geth/zz_probe.go",
                                                   *sorted(f"core/file{index}.go" for index in range(8))])
        self.assertEqual((self.output / "bsc" / "metadata" / "go.mod").read_text(),
                         "module github.com/ethereum/go-ethereum\n\n"
                         "require github.com/sigaev-pro/bc-indexer/adapters/ingestclient v0.0.0\n")
        provenance = json.loads((self.output / "provenance.json").read_text())
        self.assertEqual([row["files"][0]["path"] for row in provenance["variant"]["patches"]],
                         ["go.mod", "cmd/geth/zz_probe.go"])
        self.assertEqual(provenance["build_environment"], {})

    def test_a_patch_on_any_other_non_go_file_is_refused(self):
        notes = self.root / "notes.diff"
        notes.write_text("--- /dev/null\n+++ b/docs/notes.md\n@@ -0,0 +1 @@\n+# notes\n")
        variant = build.variant_of("notes", [str(notes)], [], [], [], [], [])
        proof = build.build(self.accepted / "manifest.json", self.sha, self.output, variant)
        self.assertEqual(proof["result"], "FAIL")
        self.assertIn("A patch may change Go source, go.mod and go.sum only: docs/notes.md", proof["error"])
        self.assertEqual([argv for argv in self.docker.calls if argv[1] == "build"], [])
        self.assertEqual(self.native, [])

    def test_a_changed_variant_input_fails_before_an_image_exists(self):
        variant = overlay(self.root)
        original = build.probe14.build_mounts

        def changing(*arguments):
            (self.root / "lib" / "libcprobe.so").write_bytes(b"changed")
            return original(*arguments)
        with mock.patch.object(build.probe14, "build_mounts", changing):
            proof = build.build(self.accepted / "manifest.json", self.sha, self.output, variant)
        self.assertEqual((proof["result"], self.docker.removed), ("FAIL", []))
        self.assertIn("Variant input changed", proof["error"])

    def test_a_failed_check_removes_the_tag_and_keeps_the_fail(self):
        with mock.patch.object(FakeNative, "version", VERSION.replace(COMMIT, "0" * 40)):
            proof = build.build(self.accepted / "manifest.json", self.sha, self.output)
        self.assertEqual(proof["result"], "FAIL")
        self.assertIn("Version output differs", proof["error"])
        self.assertEqual(self.docker.removed, [TAG])
        self.assertFalse((self.output / "provenance.json").exists())
        self.assertEqual(json.loads((self.output / "build.json").read_text())["result"], "FAIL")

    def test_a_failed_image_check_removes_the_tag(self):
        def empty(docker, argv, **kwargs):
            if argv[:3] == ["docker", "image", "inspect"] and argv[3] == TAG:
                return done(argv, 0, b"[]")
            return FakeDocker.answer(docker, argv, **kwargs)
        with mock.patch.object(FakeDocker, "__call__", empty):
            proof = build.build(self.accepted / "manifest.json", self.sha, self.output)
        self.assertEqual((proof["result"], self.docker.removed), ("FAIL", [TAG]))
        self.assertIn("exactly one image", proof["error"])

    def test_the_lock_is_the_one_the_plan_names(self):
        sha = self.rewrite_manifest(lambda manifest: manifest["plan"].update(lock_sha256="0" * 64), bind=False)
        with self.assertRaisesRegex(Rejected, "candidate lock differs from the lock of the accepted build plan"):
            build.build(self.accepted / "manifest.json", sha, self.output)
        self.assertFalse(self.output.exists())

    def test_the_vanilla_configuration_is_bound_to_the_manifest(self):
        sha = self.rewrite_manifest(lambda manifest: None)
        FakeNative.during_build = lambda: (self.accepted / "oci" / "config.json").write_text(
            json.dumps({"created": "2026-11-04T00:00:00Z"}))
        proof = self.fails(lambda: None, "Artifact bytes changed")
        self.assertEqual([argv for argv in self.docker.calls if argv[1] == "build"], [])
        self.assertIsNone(proof["previous_image_id"])
        self.assertTrue(FakeNative.instances[-1].owned)  # the compile ran first

    def test_the_manifest_is_read_once(self):
        original = build.pinned_json
        reads = []
        def counted(path, *arguments, **keywords):
            reads.append(path)
            return original(path, *arguments, **keywords)
        with mock.patch.object(build, "pinned_json", counted):
            build.build(self.accepted / "manifest.json", self.sha, self.output)
        self.assertEqual(reads, [self.accepted / "manifest.json"])

    def test_an_output_inside_the_repository_is_refused_before_anything_runs(self):
        inside = build.ROOT / "not-created-by-a-test"
        for output in (inside, build.ROOT / "bsc-capture-output" / ".." / "not-created-by-a-test",
                       build.ROOT / "test" / "harness" / "out"):
            with self.subTest(output=output), self.assertRaisesRegex(Rejected, "cannot be inside the repository"):
                build.build(self.accepted / "manifest.json", self.sha, output)
            with self.assertRaisesRegex(Rejected, "cannot be inside the repository"):
                build.smoke(TAG, output, 20)
        self.assertFalse(inside.exists())
        self.assertFalse((build.ROOT / "bsc-capture-output").exists())
        self.assertFalse((build.ROOT / "test" / "harness" / "out").exists())
        with self.assertRaisesRegex(Rejected, "must be absolute"):
            build.build(self.accepted / "manifest.json", self.sha, Path("relative/out"))
        self.assertEqual(self.docker.calls, [])
        self.assertEqual(FakeNative.instances, [])

    def test_refusals_leave_no_output(self):
        with self.assertRaises(Rejected):
            build.build(self.accepted / "manifest.json", "0" * 64, self.output)
        with self.assertRaises(Rejected):
            build.build(self.accepted / "manifest.json", self.sha, self.accepted / "new")
        self.assertFalse((self.accepted / "new").exists())
        self.assertFalse(self.output.exists())
        self.assertEqual(self.docker.calls, [])


class SmokeTest(ProcedureFixture):
    def setUp(self):
        super().setUp()
        FakeNative.control = {"start": (0, START_LOG), **REFUSALS}
        FakeNative.version = VERSION

    def test_smoke_passes(self):
        proof = build.smoke(TAG, self.output, 20)
        self.assertEqual((proof["result"], proof["error"]), ("PASS", None))
        self.assertEqual(proof["start"]["exit_code"], 0)
        self.assertEqual({name: row["exit_code"] for name, row in proof["controls"].items()},
                         {"snap-sync": 1, "foreign-store": 1, "pruned-history": 1, "path-scheme": 1})
        self.assertEqual(proof["config"]["store_id"], build.SMOKE_STORE)
        self.assertEqual(proof["config"]["native_commit"], COMMIT)
        self.assertIsNone(proof["variant"])
        self.assertTrue((self.output / "smoke.json").is_file())
        foreign = json.loads((self.output / "smoke" / "config" / "foreign.json").read_text())
        self.assertEqual(foreign["store_id"], build.FOREIGN_STORE)
        self.assertEqual(json.loads(Path(proof["genesis"]["path"]).read_text())["config"]["chainId"], 97)
        home = ["--chapel", "--datadir", "/home"]
        path_home = ["--chapel", "--datadir", "/home-path"]
        capture = {"EXTRACTOR_BSC_CONFIG": "/config/capture.json"}
        self.assertEqual([row["argv"] for row in self.native], [
            ["--chapel", "dumpgenesis"], [*home, "--state.scheme", "hash", "init", "/config/genesis.json"],
            ["-c", build.start_script([*home, *build.PROFILE, *build.OFFLINE], "start", 20)],
            [*path_home, "init", "/config/genesis.json"],
            ["-c", build.start_script([*home, "--state.scheme", "hash", "--gcmode", "full", *build.OFFLINE], "snap-sync")],
            ["-c", build.start_script([*home, *build.PROFILE, *build.OFFLINE], "foreign-store")],
            ["-c", build.start_script([*home, "--state.scheme", "hash", "--gcmode", "full", *build.FAST_NODE, *build.OFFLINE],
                                      "pruned-history")],
            ["-c", build.start_script([*path_home, "--gcmode", "full", *build.FAST_NODE, *build.OFFLINE], "path-scheme")]])
        self.assertNotIn("--state.scheme", self.native[-1]["argv"][-1].split("> /work/")[0])
        self.assertEqual([row["environment"] for row in self.native],
                         [None, None, capture, None, capture, {"EXTRACTOR_BSC_CONFIG": "/config/foreign.json"},
                          capture, capture])
        self.assertEqual([row["entrypoint"] for row in self.native],
                         [None, None, "/bin/sh", None, "/bin/sh", "/bin/sh", "/bin/sh", "/bin/sh"])
        self.assertIn((self.output / "smoke" / "home-path", "/home-path", False), self.native[-1]["mounts"])
        self.assertEqual(len(self.native[-1]["mounts"]), 4)
        self.assertTrue(all(row["image"] == TAG for row in self.native))
        scripts = [row["argv"][1] for row in self.native if row["entrypoint"] == "/bin/sh"]
        self.assertIn("--syncmode full", scripts[0])
        self.assertNotIn("--syncmode", scripts[1])
        self.assertIn("--maxpeers 0", scripts[0])

    def test_smoke_names_the_variant_of_the_image(self):
        self.docker.labels = {**self.docker.labels, "io.extractor.variant": "collector"}
        proof = build.smoke(TAG + "-collector", self.output, 20)
        self.assertEqual((proof["result"], proof["variant"]), ("PASS", "collector"))

    def test_a_control_that_passes_fails_the_smoke(self):
        controls = dict(FakeNative.control)
        for name in ("snap-sync", "foreign-store", "pruned-history", "path-scheme"):
            FakeNative.instances = []
            FakeNative.control = {**controls, name: (0, START_LOG)}
            self.output = self.root / ("pass-" + name)
            with self.subTest(name=name):
                proof = build.smoke(TAG, self.output, 20)
                self.assertEqual(proof["result"], "FAIL")
                self.assertIn("passed", proof["error"])
                self.assertIn(name, proof["error"])

    def test_a_control_refused_by_another_rule_fails_the_smoke(self):
        FakeNative.control = {**FakeNative.control,
                              "foreign-store": (1, "Fatal: genesis does not build the home's genesis block")}
        proof = build.smoke(TAG, self.output, 20)
        self.assertEqual(proof["result"], "FAIL")
        self.assertIn("does not name", proof["error"])

    def test_an_image_without_the_labels_is_refused(self):
        for labels in ({}, {**build.image_labels(provenance_rows()), "io.extractor.source-commit": "short"},
                       {**build.image_labels(provenance_rows()),
                        "io.extractor.patches": "01-core-import-seam.diff=" + "0" * 64}):
            with self.subTest(labels=list(labels)[:2]):
                self.docker.labels = labels
                proof = build.smoke(TAG, self.output, 20)
                self.assertEqual(proof["result"], "FAIL")
                self.assertIn("not a capture build", proof["error"])
                FakeNative.instances[-1].cleanup_complete = True
                self.output = self.root / ("out-" + str(len(FakeNative.instances)))
        self.assertFalse(self.native)

    def test_arguments_are_checked(self):
        for image, seconds in (("latest", 20), ("a/b:tag", 5), ("a/b:tag", 121), ("a/b:tag", "20"), ("A/B:tag", 20)):
            with self.subTest(image=image, seconds=seconds), self.assertRaises(Rejected):
                build.smoke(image, self.root / "none", seconds)
        self.assertFalse((self.root / "none").exists())


if __name__ == "__main__":
    unittest.main()
