#!/usr/bin/env python3
"""Install the E01-02.lifecycle_seams Bor block into an extracted Bor source
tree.

`install` first installs the accepted admission and decision blocks with
test/harness/bor_admission_tree.py, unchanged. `install_lifecycle` then adds
this block on top: the shared `lifecycle` package at `extractor/lifecycle`,
the Bor glue (`extractor/borlifecycle`, its `profile` and `wire` packages),
the new files of `tree/`, and the anchored edits of `patches.json`, which apply
to the tree as the earlier patch sets left it (an anchor may sit in text an
earlier set inserted, or in an installed `extractor/` package). Every anchor
must occur exactly once, an edit name must be unique across all patch sets,
and no new file may replace an existing one, so a changed tree is refused
instead of patched in the wrong place. The returned record lists the digest
of every file before and after.
"""

import hashlib
import json
from pathlib import Path
import sys

NATIVE = Path(__file__).resolve().parent
ROOT = NATIVE.parents[3]
sys.path.insert(0, str(ROOT / "test/harness"))
import bor_admission_tree  # noqa: E402
from bor_admission_tree import TreeError, add_file, apply_edit, load_patches, relative  # noqa: E402

PATCHES = NATIVE / "patches.json"

# (source directory relative to ROOT, file names, directory inside the tree)
PACKAGES = (
    ("adapters/bor/lifecycle", ("lifecycle.go", "finality.go", "callers.json", "lifecycle_test.go", "vectors.json"),
     "extractor/lifecycle"),
    ("adapters/bor/lifecycle/native/borlifecycle", ("borlifecycle.go", "borlifecycle_test.go"),
     "extractor/borlifecycle"),
    ("adapters/bor/lifecycle/native/borlifecycle/profile", ("profile.go", "profile_test.go"),
     "extractor/borlifecycle/profile"),
    ("adapters/bor/lifecycle/native/borlifecycle/wire", ("wire.go",), "extractor/borlifecycle/wire"),
)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def install_lifecycle(source, patches_path=PATCHES, earlier=(), root=ROOT):
    """Install this block into a tree that already holds the admission and
    decision blocks. `earlier` lists the edit names of the earlier patch
    sets, which no edit name here may repeat."""
    source = Path(source)
    patches = load_patches(Path(patches_path))
    names = [edit["name"] for edit in patches["edits"]]
    if set(names) & set(earlier):
        raise TreeError("an edit name occurs in two patch sets")
    rows = []
    for directory, files, target in PACKAGES:
        for name in files:
            add_file(source, f"{target}/{name}", (root / directory / name).read_bytes(), rows, "package")
    for tree_path, origin in sorted(patches["files"].items()):
        add_file(source, tree_path, (Path(patches_path).parent / Path(*relative(origin).parts)).read_bytes(), rows,
                 "new")
    edited = {}
    for edit in patches["edits"]:
        path = source / Path(*relative(edit["file"]).parts)
        if edit["file"] not in edited:
            if not path.is_file() or path.is_symlink():
                raise TreeError(f"edit {edit['name']}: {edit['file']} is not a regular file")
            before = path.read_bytes()
            edited[edit["file"]] = {"before": before, "text": before.decode()}
        edited[edit["file"]]["text"] = apply_edit(edited[edit["file"]]["text"], edit)
    for name, row in sorted(edited.items()):
        after = row["text"].encode()
        (source / Path(*relative(name).parts)).write_bytes(after)
        rows.append({"kind": "edited", "path": name, "sha256_before": sha(row["before"]), "sha256": sha(after),
                     "size_bytes": len(after)})
    path = Path(patches_path)
    return {"patch_set": {"path": str(path.relative_to(root)) if path.is_relative_to(root) else str(path),
                          "sha256": sha(path.read_bytes())}, "edits": names, "files": rows}


def install(source):
    """Install the admission, decision and lifecycle Bor blocks into the
    tree at `source` and return both records."""
    base = bor_admission_tree.install(Path(source))
    return {"admission_decision": base, "lifecycle": install_lifecycle(source, earlier=base["edits"])}


if __name__ == "__main__":
    print(json.dumps(install(Path(sys.argv[1])), indent=2))
