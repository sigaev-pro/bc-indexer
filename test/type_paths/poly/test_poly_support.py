"""Source reuse and refusal controls; none of these runs a native client."""

import copy
import importlib.util
from pathlib import Path
import unittest
import tempfile
import time
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("type_paths_poly_native", HERE / "poly_native.py")
native = importlib.util.module_from_spec(spec)
spec.loader.exec_module(native)


class PolygonSupportTests(unittest.TestCase):
    def test_source_retention_creates_owned_directories_and_verifies_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            origins, copies = native.keep_code(output, time.monotonic() + 30)
            self.assertEqual(len(origins), len(copies))
            self.assertGreater(len(copies), len(native.FILES))
            for origin, retained in zip(origins, copies):
                self.assertEqual(Path(origin["path"]).read_bytes(), Path(retained["path"]).read_bytes())
                self.assertTrue(Path(retained["path"]).is_relative_to(output))
            with self.assertRaises(FileExistsError):
                native.keep_code(output, time.monotonic() + 30)

    def test_accepted_support_is_pinned_and_seams_leave_native_helpers_intact(self):
        source = (native.CORPUS / "main.go").read_bytes()
        generated = native.support.render_support(source)
        restored = generated.replace(b"func acceptedCorpusMain()", b"func main()")
        restored = restored.replace(b"func acceptedCorpusTransactions(", b"func corpusTransactions(")
        restored = restored.replace(b"\n\tcfg.VMTrace = typePathTracer", b"")
        self.assertEqual(restored, source)
        self.assertIn(b"txs, err := corpusTransactions(config, sender)", generated)
        with self.assertRaisesRegex(ValueError, "source identity"):
            native.support.render_support(source.replace(b"func generate", b"func alteredGenerate", 1))

    def tool(self):
        return {"kind": "poly-type-paths-diagnostic-tool", "native_source": native.profile.SOURCE_COMMIT,
                "source_archive_sha256": native.profile.SOURCE_ARCHIVE_SHA256,
                "runtime": "docker.io/library/debian@sha256:" + "a" * 64,
                "fixture": {name: native.reference(HERE / name) for name in native.FILES},
                "binary": {"path": "/tmp/poly-prep/bor/bin/probe"},
                "inputs": [{"path": "/tmp/poly-prep/bor/inputs/" + name}
                           for name in ("genesis.json", "chain-config.json", "helper.go")],
                "bodies": [{"path": "/tmp/poly-prep/bor/bodies/" + name} for name in native.poly_corpus.BODY_FILES]}

    def refuse(self, tool, message):
        with patch.object(native, "pinned_json", return_value=({}, tool)), \
                patch.object(native, "artifact", return_value=Path("/tmp/poly-prep/bor/bin/probe")), \
                patch.object(native.pins, "owned_attempt") as owned:
            with self.assertRaisesRegex(ValueError, message):
                native.run(Path("/tmp/poly-prep/tool.json"), "ignored", Path("/tmp/poly-output"))
            owned.assert_not_called()

    def test_refuses_wrong_native_source_before_output(self):
        for field in ("kind", "native_source", "source_archive_sha256"):
            with self.subTest(field=field):
                tool = self.tool()
                tool[field] = "other"
                self.refuse(tool, "Wrong native tool")

    def test_refuses_changed_or_missing_prepared_fixture_before_output(self):
        tool = self.tool()
        tool["fixture"]["probe.go"]["sha256"] = "0" * 64
        self.refuse(tool, "Prepared fixture source mismatch")
        tool = self.tool()
        del tool["fixture"]["poly_profile.py"]
        self.refuse(tool, "Prepared fixture source mismatch")

    def test_refuses_mutable_runtime_before_output(self):
        tool = self.tool()
        tool["runtime"] = "debian:latest"
        self.refuse(tool, "Unpinned runtime")

    def test_refuses_missing_duplicate_or_displaced_input(self):
        for field in ("inputs", "bodies"):
            for change in ("missing", "duplicate", "displaced"):
                with self.subTest(field=field, change=change):
                    tool = self.tool()
                    if change == "missing":
                        tool[field].pop()
                    elif change == "duplicate":
                        tool[field].append(copy.deepcopy(tool[field][0]))
                    else:
                        tool[field][0]["path"] = "/tmp/other/" + Path(tool[field][0]["path"]).name
                    self.refuse(tool, "Incomplete or duplicate|escape owned")


if __name__ == "__main__":
    unittest.main()
