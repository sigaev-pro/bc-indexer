"""Check which tracked test files the bootstrap CI runs."""

from pathlib import Path
import unittest

import ci


class DiscoveryTests(unittest.TestCase):
    def test_selects_project_harness_corpus_faults_and_top_level_test_modules(self):
        root = Path("/repository")
        paths = [root / name for name in (
            ".project/automation/test_ci.py", "test/harness/test_runner.py", "test/test_mutate.py",
            "test/builds/test_build_contract.py", "test/evidence/test_evidence.py", "test/mutate.py",
            "test/harness/fixtures/test_data.json", "adapters/test_adapter.py",
            "test/native_corpus/test_native_corpus.py", "test/native_corpus/helper.py",
            "test/native_faults/test_native_faults.py", "test/native_faults/faults_judge.py")]
        self.assertEqual([str(path.relative_to(root)) for path in ci.test_modules(paths, root)],
                         [".project/automation/test_ci.py", "test/harness/test_runner.py", "test/test_mutate.py",
                          "test/native_corpus/test_native_corpus.py",
                          "test/native_faults/test_native_faults.py"])


if __name__ == "__main__":
    unittest.main()
