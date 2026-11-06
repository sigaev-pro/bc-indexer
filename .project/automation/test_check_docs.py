"""Keep local worktrees and untracked files outside document acceptance."""

from pathlib import Path
import subprocess
import tempfile
import unittest

import check_docs


class TrackedDocumentTests(unittest.TestCase):
    def test_untracked_worktree_documents_do_not_enter_validation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "--quiet", str(root)], check=True)
            (root / "tracked.md").write_text("# Tracked\n\n[Section](#tracked)\n")
            subprocess.run(["git", "add", "tracked.md"], cwd=root, check=True)
            ignored = root / ".project/worktrees/other"
            ignored.mkdir(parents=True)
            (ignored / "broken.md").write_text("[Missing](missing.md)\n")
            paths = check_docs.tracked_paths(root)
            result = check_docs.check_markdown(paths, root)
            self.assertEqual(result, {"documents": 1, "local_links": 1})

    def test_existing_untracked_link_target_cannot_hide_a_missing_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            document = root / "tracked.md"
            document.write_text("[Missing from Git](untracked.md)\n")
            (root / "untracked.md").write_text("# Local only\n")
            with self.assertRaisesRegex(ValueError, "untracked or missing link"):
                check_docs.check_markdown([document], root)

    def test_missing_anchor_blocks_document_acceptance(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            document = root / "tracked.md"
            document.write_text("# Real\n\n[Wrong](#missing)\n")
            with self.assertRaisesRegex(ValueError, "missing anchor"):
                check_docs.check_markdown([document], root)


if __name__ == "__main__":
    unittest.main()
