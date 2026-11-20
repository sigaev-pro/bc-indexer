"""Direct diagnostic boundary controls; all native commands are mocked."""
from contextlib import ExitStack
import copy
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("bsc_full_import_driver_controls", HERE / "native.py")
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)


def tool():
    return {"kind": "bsc-full-type-import-tool", "native_source_commit": driver.COMMIT,
            "inputs": {name: str(path) for name, path in driver.INPUTS.items()}, "binary": {}, "compiler": "pinned-compiler"}


class DriverControls(unittest.TestCase):
    def test_tool_guards_precede_native(self):
        for field in ("kind", "native_source_commit", "inputs"):
            candidate = tool(); candidate[field] = "wrong"
            with self.subTest(field=field), patch.object(driver, "pinned_json", return_value=({}, candidate)), \
                    patch.object(driver, "reference", side_effect=lambda path: str(path)), \
                    patch.object(driver, "artifact", return_value=Path("/binary")), \
                    patch.object(driver, "record", return_value={}), \
                    patch.object(driver.pins, "owned_attempt") as execute:
                with self.assertRaises(driver.Rejected):
                    driver.run(Path("/tool"), "unused", Path("/output"), diagnostic=True)
                execute.assert_not_called()

    def test_relative_path_rejected_before_read(self):
        for input_path, output_path in ((Path("tool"), Path("/out")), (Path("/tool"), Path("out"))):
            with self.subTest(input=input_path, output=output_path), patch.object(driver, "pinned_json") as read:
                with self.assertRaises(driver.Rejected):
                    driver.run(input_path, "unused", output_path, diagnostic=True)
                read.assert_not_called()

    def exercise(self, diagnostic, judgement="FAIL", command_error=False, source_error=False):
        with tempfile.TemporaryDirectory() as directory, ExitStack() as stack:
            root = Path(directory)
            runner = Mock()
            runner.command.return_value = root / "stdout"
            if command_error:
                runner.command.side_effect = driver.Rejected("native command failed")
            checked = {"result": judgement, "errors": ["oracle incomplete"] if judgement != "PASS" else []}
            def owned(output, started, deadline, proof, name, body):
                body(output, runner)
                return dict(proof, result="PASS")
            patches = [patch.object(driver, "pinned_json", return_value=({}, tool())),
                       patch.object(driver, "reference", side_effect=lambda path, *args, **kw: str(path)),
                       patch.object(driver, "artifact", return_value=root / "binary"),
                       patch.object(driver, "record", return_value={"result": "FAIL", "ac_verdict": None}),
                       patch.object(driver, "keep_code", return_value=(["source"], ["copy"])),
                       patch.object(driver, "read_bytes", return_value=b"native output"),
                       patch.object(driver, "load", return_value={}),
                       patch.object(driver.judge, "check", return_value=checked),
                       patch.object(driver.pins, "parse_go_test", return_value={"result": "PASS"}),
                       patch.object(driver.pins, "save"), patch.object(driver.pins, "owned_attempt", side_effect=owned)]
            for context in patches:
                stack.enter_context(context)
            unchanged = stack.enter_context(patch.object(driver, "sources_unchanged"))
            if source_error:
                unchanged.side_effect = driver.Rejected("source changed")
            result = driver.run(root / "tool", "unused", root, diagnostic=diagnostic)
            unchanged.assert_called_once()
            self.assertEqual(runner.command.call_args.kwargs["cap"], 750)
            self.assertEqual(runner.command.call_args.kwargs["memory"], "3g")
            self.assertIsNone(result["ac_verdict"])
            self.assertEqual(result["diagnostic_only"], diagnostic)
            return result

    def test_explicit_diagnostic_retains_failed_judgement_without_acceptance(self):
        self.assertEqual(self.exercise(True)["result"], "PASS")

    def test_ordinary_run_requires_judge_pass(self):
        with self.assertRaisesRegex(driver.Rejected, "Independent judge failed"):
            self.exercise(False)
        self.assertEqual(self.exercise(False, "PASS")["result"], "PASS")

    def test_diagnostic_does_not_bypass_execution_or_source_integrity(self):
        for option in ("command_error", "source_error"):
            with self.subTest(option=option), self.assertRaises(driver.Rejected):
                self.exercise(True, **{option: True})


if __name__ == "__main__":
    unittest.main()
