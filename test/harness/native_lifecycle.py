#!/usr/bin/env python3
"""Assemble the test module of E05-02.reorg (test/native_lifecycle) and check its layout.

`assemble DIR` extends the assembled finality module (source_inputs,
admission, decision, lifecycle, finality) with the ETH scenario model of
`test/native_lifecycle/eth` at `extractor/native_lifecycle_eth`, and binds
the repository-local modules it composes — adapters/lifecycle_registry for
the real activation and revision registry, test/lifecycle_validator for the
E02-03 oracle (adapters/recovery_gc and protocol/semantic are already bound
by the finality assembly) — through local replaces. The Go tests of the ETH
block run only there, because the model imports the extractor packages at
their native paths. `check` verifies that the source files of the block are
present and that the assembled layout carries them unchanged.
"""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
import finality  # noqa: E402

ROOT = finality.ROOT
BLOCK = ROOT / "test" / "native_lifecycle" / "eth"
BLOCK_FILES = ("lifecycle_eth.go", "store.go", "lifecycle_eth_test.go")

# The repository-local modules the ETH block composes beyond the finality
# assembly (which binds adapters/recovery_gc and protocol/semantic). The
# replaces of a dependency module are ignored, so the assembled main module
# binds every module of the graph itself.
LOCAL_MODULES = {
    "github.com/sigaev-pro/bc-indexer/adapters/lifecycle_registry": "adapters/lifecycle_registry",
    "github.com/sigaev-pro/bc-indexer/protocol/balance": "protocol/balance",
    "github.com/sigaev-pro/bc-indexer/protocol/block": "protocol/block",
    "github.com/sigaev-pro/bc-indexer/protocol/profiles": "protocol/profiles",
    "github.com/sigaev-pro/bc-indexer/protocol/proto": "protocol/proto",
    "github.com/sigaev-pro/bc-indexer/test/lifecycle_validator": "test/lifecycle_validator",
}


def source_files(root=ROOT):
    """Return the ETH block files with their digests; raise ValueError if one is missing."""
    block = root / "test" / "native_lifecycle" / "eth"
    files = {}
    for name in BLOCK_FILES:
        path = block / name
        if not path.is_file():
            raise ValueError(f"missing ETH block file: {path.relative_to(root)}")
        files[name] = hashlib.sha256(path.read_bytes()).hexdigest()
    return files


def assemble(target, root=ROOT):
    """Extend the assembled finality module with the ETH block and return its directories."""
    target = Path(target)
    source_files(root)
    # finality.assemble refuses an existing target before it writes.
    packages = finality.assemble(target, root)
    (target / "extractor/native_lifecycle_eth").mkdir(parents=True)
    for name in BLOCK_FILES:
        shutil.copyfile(root / "test" / "native_lifecycle" / "eth" / name,
                        target / "extractor/native_lifecycle_eth" / name)
    module = ["", "// The repository-local modules the native lifecycle ETH block composes",
              "// beyond the finality assembly. The replaces of a dependency module",
              "// are ignored, so this main module binds every module of the graph",
              "// itself.",
              "require (",
              "\tgithub.com/sigaev-pro/bc-indexer/adapters/lifecycle_registry v0.0.0",
              "\tgithub.com/sigaev-pro/bc-indexer/test/lifecycle_validator v0.0.0",
              ")",
              "",
              "require (",
              "\tgithub.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0 // indirect",
              "\tgithub.com/sigaev-pro/bc-indexer/protocol/block v0.0.0 // indirect",
              "\tgithub.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0 // indirect",
              "\tgithub.com/sigaev-pro/bc-indexer/protocol/proto v0.0.0 // indirect",
              ")",
              ""]
    for module_path, relative in sorted(LOCAL_MODULES.items()):
        local = (root / relative).resolve()
        module.append(f"replace {module_path} => {local}")
    (target / "go.mod").write_text((target / "go.mod").read_text() + "\n".join(module) + "\n")
    return sorted(packages + ["extractor/native_lifecycle_eth"])


def check(root=ROOT):
    """Return the ETH block file digests; raise ValueError if the layout is incomplete."""
    files = source_files(root)
    readme = root / "test" / "native_lifecycle" / "README.md"
    if not readme.is_file():
        raise ValueError("missing test/native_lifecycle/README.md")
    return files


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=("check", "assemble"))
    parser.add_argument("target", nargs="?", help="new directory for assemble")
    args = parser.parse_args(argv)
    try:
        if args.command == "check":
            output = check()
        else:
            if not args.target:
                raise ValueError("assemble needs a target directory")
            output = assemble(args.target)
    except ValueError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    print(json.dumps(output, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
