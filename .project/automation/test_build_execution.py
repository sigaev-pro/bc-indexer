"""Fixed executor controls use local disposable files, never native builds."""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

import build_contract as contract
import build_execution as execution
import build_os


class ExecutionTests(unittest.TestCase):
    def runner(self, root):
        runner = execution.Runner.__new__(execution.Runner)
        runner.root = Path(root).resolve()
        (runner.root / "logs").mkdir()
        runner.plan = {"limits": dict(contract.DEFAULT_LIMITS)}
        runner.deadline = time.time() + 30
        runner.monotonic_deadline = time.monotonic() + 30
        runner.request = {"stop_file": str(runner.root / "stop")}
        runner.commands, runner.cleaning = [], False
        return runner

    def test_original_deadline_and_stop_are_not_reset(self):
        with tempfile.TemporaryDirectory() as root:
            runner = self.runner(root)
            self.assertLessEqual(runner.remaining(60), 30)
            runner.monotonic_deadline = time.monotonic() - 1
            with self.assertRaises(ValueError):
                runner.remaining()
            runner.monotonic_deadline = time.monotonic() + 30
            Path(runner.request["stop_file"]).touch()
            with self.assertRaises(ValueError):
                runner.remaining()

    def test_failed_commands_and_timeout_history_survive(self):
        with tempfile.TemporaryDirectory() as root:
            runner = self.runner(root)
            with self.assertRaises(ValueError):
                runner.run([sys.executable, "-I", "-c", "raise SystemExit(7)"])
            history = json.loads((runner.root / "command-history.json").read_text())
            self.assertEqual(history[0]["exit_code"], 7)
            with self.assertRaises(subprocess.TimeoutExpired):
                runner.run([sys.executable, "-I", "-c", "import time; time.sleep(2)"], timeout=0.05)
            history = json.loads((runner.root / "command-history.json").read_text())
            self.assertEqual(len(history), 2)
            self.assertEqual(history[0]["exit_code"], 7)
            self.assertIsNone(history[1]["exit_code"])
            self.assertIn("TimeoutExpired", history[1]["error"])
            self.assertIn("stdout", history[1])

    def test_fixed_commands_bind_native_metadata_and_owner_replacements(self):
        pin = {"commit": "a" * 40, "version": "v1.2.3"}
        graph = [{"Path": "github.com/cosmos/cosmos-sdk", "Version": "v0.1.0",
                  "Replace": {"Path": "synthetic/cosmos", "Version": "v0.2.0-owner"}},
                 {"Path": "github.com/cometbft/cometbft", "Version": "v0.3.0"}]
        for name in contract.NATIVE:
            args = execution.build_arguments(name, pin, 946684800, graph, 2)
            self.assertEqual(args[:2], ["/usr/local/go/bin/go", "build"])
            self.assertEqual(args[-1], execution.TARGETS[name])
            self.assertIn(pin["commit"], args[args.index("-ldflags") + 1])
            self.assertNotIn("-a", args)
            self.assertIn("-buildvcs=false", args)
            if name in {"geth", "bsc"}:
                self.assertIn("urfave_cli_no_docs", args)
            if name == "bor":
                self.assertIn("-pgo=prod.pprof", args)
            if name == "prysm":
                self.assertIn("runtime/version.gitCommit=" + pin["commit"], " ".join(args))
            if name == "heimdall":
                self.assertIn("version.Version=v0.2.0-owner", " ".join(args))
        with self.assertRaises(ValueError):
            execution.build_arguments("heimdall", pin, 946684800, [], 2)

    def test_native_caches_and_explicit_toolchain_stay_separate(self):
        values = [execution.go_environment(name, "linux/amd64", 1) for name in contract.MODULES]
        self.assertEqual(len({row["GOMODCACHE"] for row in values}), 10)
        for value in values:
            self.assertEqual(value["GOTOOLCHAIN"], "local")
            self.assertEqual(value["GOWORK"], "off")
            self.assertEqual(value["GOENV"], "off")
            self.assertEqual(value["GOFLAGS"], "-mod=readonly")

    def test_official_sources_reject_credentials_and_host_substitution(self):
        execution.official_url("https://deb.debian.org/debian/dists/trixie/InRelease")
        for value in ("http://deb.debian.org/file", "https://user:secret@deb.debian.org/file", "https://deb.debian.org.example/file"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                execution.official_url(value)

    def test_apt_download_plan_cannot_escape_signed_inventory(self):
        resolver = build_os.Packages.__new__(build_os.Packages)
        resolver.by_filename = {}
        with tempfile.TemporaryDirectory() as root:
            for line in ("'https://example.org/payload' file.deb 8 SHA256:abc", "'file:/proof/mirror/repo-0/pool/a.deb' ../bad 8 SHA256:abc",
                         "'file:/proof/mirror/repo-0/pool/a.deb' a.deb 8 SHA256:abc"):
                with self.subTest(line=line), self.assertRaises(ValueError):
                    resolver.download_plan(line, root)


if __name__ == "__main__":
    unittest.main()
