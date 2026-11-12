#!/usr/bin/env python3
"""Install the E01-02.admission and E01-02.decision_bridge Bor blocks into an
extracted Bor source tree.

The installer copies the shared extractor packages (admission, source_inputs,
state_provider, core/capture, decision) and the Bor glue to `extractor/` in
the tree, adds the new files of `adapters/bor/admission/native/tree/` and
`adapters/bor/decision/native/tree/`, and applies the anchored edits of
`adapters/bor/admission/native/patches.json`, then those of
`adapters/bor/decision/native/patches.json`. Every anchor must occur exactly
once and no new file may replace a native one, so a changed native tree is
refused instead of patched in the wrong place. The returned rows record the
digest of every file before and after.
"""

import hashlib
import json
from pathlib import Path, PurePosixPath

ROOT = Path(__file__).resolve().parents[2]
NATIVE = ROOT / "adapters/bor/admission/native"
DECISION = ROOT / "adapters/bor/decision/native"
PATCH_SETS = (NATIVE / "patches.json", DECISION / "patches.json")
MODES = ("before", "after", "replace")

# (source directory relative to ROOT, file names, directory inside the tree)
PACKAGES = (
    ("adapters/bor/admission", ("admission.go", "admission_test.go", "canonical.go", "canonical_test.go",
                                "vectors.json"), "extractor/admission"),
    ("adapters/bor/admission/native/obligation", ("obligation.go", "obligation_test.go"),
     "extractor/admission/native/obligation"),
    ("adapters/bor/source_inputs", ("inputs.go", "inputs_test.go"), "extractor/source_inputs"),
    # The state_provider tests need the E01-02.pins harness inputs (test/harness/pins.py runs them).
    ("adapters/bor/state_provider", ("provider.go", "family.go"), "extractor/state_provider"),
    ("adapters/bor/state_provider/native_overlay/internal/cli/server", ("zz_extractor_effective.go",),
     "internal/cli/server"),
    ("core/capture", ("capture.go", "capture_test.go"), "extractor/capture"),
    ("adapters/bor/admission/native/borimport", ("borimport.go", "safepoint.go", "borimport_test.go"),
     "extractor/borimport"),
    ("adapters/bor/decision", ("decision.go", "decision_test.go", "vectors.json"), "extractor/decision"),
    ("adapters/bor/decision/native/decider", ("decider.go", "decider_test.go"), "extractor/decision/native/decider"),
    ("adapters/bor/decision/native/bordecision", ("bordecision.go", "bordecision_test.go"), "extractor/bordecision"),
)


class TreeError(Exception):
    """The native tree or the patch set does not match."""


def sha(data):
    return hashlib.sha256(data).hexdigest()


def relative(value):
    path = PurePosixPath(value)
    if not value or path.is_absolute() or ".." in path.parts or "\\" in value:
        raise TreeError(f"unsafe tree path {value!r}")
    return path


def load_patches(path=NATIVE / "patches.json"):
    data = json.loads(path.read_text())
    if data.get("schema_version") != 1 or not isinstance(data.get("files"), dict) or \
            not isinstance(data.get("edits"), list):
        raise TreeError("malformed patch set")
    names = set()
    for edit in data["edits"]:
        if set(edit) != {"name", "file", "mode", "anchor", "text"} or edit["mode"] not in MODES or \
                not edit["anchor"] or edit["name"] in names:
            raise TreeError(f"malformed edit {edit.get('name')!r}")
        names.add(edit["name"])
        relative(edit["file"])
    return data


def apply_edit(text, edit):
    """Apply one anchored edit to text; the anchor must occur exactly once."""
    count = text.count(edit["anchor"])
    if count != 1:
        raise TreeError(f"edit {edit['name']}: anchor occurs {count} times in {edit['file']}")
    anchor, insert = edit["anchor"], edit["text"]
    replacement = {"before": insert + anchor, "after": anchor + insert, "replace": insert}[edit["mode"]]
    return text.replace(anchor, replacement, 1)


def add_file(source, tree_path, data, rows, kind):
    target = source / Path(*relative(tree_path).parts)
    if target.exists() or target.is_symlink():
        raise TreeError(f"{kind} {tree_path} already exists in the native tree")
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(data)
    rows.append({"kind": kind, "path": tree_path, "sha256": sha(data), "size_bytes": len(data)})


def install(source, patch_sets=PATCH_SETS, root=ROOT):
    """Install the Bor blocks into the tree at `source` and return the record.
    The patch sets apply in order; an edit name must be unique across them."""
    source = Path(source)
    sets = [(Path(path), load_patches(Path(path))) for path in patch_sets]
    names = [edit["name"] for _, patches in sets for edit in patches["edits"]]
    if len(names) != len(set(names)):
        raise TreeError("an edit name occurs in two patch sets")
    rows = []
    for directory, files, target in PACKAGES:
        for name in files:
            add_file(source, f"{target}/{name}", (root / directory / name).read_bytes(), rows, "package")
    for path, patches in sets:
        for tree_path, origin in sorted(patches["files"].items()):
            add_file(source, tree_path, (path.parent / Path(*relative(origin).parts)).read_bytes(), rows, "new")
    edited = {}
    for edit in (edit for _, patches in sets for edit in patches["edits"]):
        path = source / Path(*relative(edit["file"]).parts)
        if edit["file"] not in edited:
            if not path.is_file() or path.is_symlink():
                raise TreeError(f"edit {edit['name']}: {edit['file']} is not a regular native file")
            before = path.read_bytes()
            edited[edit["file"]] = {"before": before, "text": before.decode()}
        edited[edit["file"]]["text"] = apply_edit(edited[edit["file"]]["text"], edit)
    for name, row in sorted(edited.items()):
        after = row["text"].encode()
        (source / Path(*relative(name).parts)).write_bytes(after)
        rows.append({"kind": "edited", "path": name, "sha256_before": sha(row["before"]), "sha256": sha(after),
                     "size_bytes": len(after)})
    return {"patch_sets": [{"path": str(path.relative_to(root)) if path.is_relative_to(root) else str(path),
                            "sha256": sha(path.read_bytes())} for path, _ in sets],
            "edits": names, "files": rows}


if __name__ == "__main__":
    import sys
    print(json.dumps(install(Path(sys.argv[1])), indent=2))
