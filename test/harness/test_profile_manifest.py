"""Portable controls for test-only local ProfileManifests; no native process starts."""

import hashlib
import json
from pathlib import Path
import tempfile
import time
import unittest

import profile_manifest

FAMILIES = {
    "geth-import": ("ETH", "ethereum-mainnet", "fixture-preparation", None, None, None, 31337),
    "bsc-parlia-import": ("BSC", "bsc-mainnet", "bsc-fixture-preparation", "bsc-native-import", "bsc-parlia", "bsc-native-observer", 714),
    "bor-import": ("POLY", "polygon-pos-mainnet", "bor-fixture-preparation", "bor-native-import", "bor-private", "bor-native-observer", 15001),
}


def write(path, value):
    data = value if isinstance(value, bytes) else (json.dumps(value, sort_keys=True) + "\n").encode()
    path.write_bytes(data)
    return {"path": str(path), "sha256": hashlib.sha256(data).hexdigest(), "size_bytes": len(data)}


def family_tree(root, slice_name, hooks=None):
    """Write a synthetic accepted preparation and return its matching manifest.

    Hooks change one prepared document before it is written, so every later
    reference is computed from the changed bytes."""
    def hook(name, value):
        (hooks or {}).get(name, lambda v: None)(value)
        return value
    family, profile, preparation_kind, request_kind, fixture_kind, tool_kind, chain_id = FAMILIES[slice_name]
    build = write(root / "build.json", {"synthetic": True})
    inputs = {name: write(root / f"{name}.bin", name.encode()) for name in ("genesis", "blocks", "full_chain")}
    expected = write(root / "expected.json", {"independent": True})
    identity = {"kind": "private-local", "chain_id": chain_id, "genesis_hash": "0x" + "1" * 64, "config_sha256": "2" * 64}
    fixture_value = {"schema_version": 1, "identity": identity, **inputs, "expected": expected}
    if fixture_kind: fixture_value["kind"] = fixture_kind
    fixture = write(root / "fixture.json", hook("fixture", fixture_value))
    request_value = {"schema_version": 1, "build_manifest": build, "profile_association": profile, "fixture": fixture, "deadline_seconds": 300}
    if request_kind: request_value["kind"] = request_kind
    request = write(root / "request.json", hook("request", request_value))
    tool = write(root / "tool.json", hook("tool", {"kind": tool_kind, "build_manifest": build})) if tool_kind else None
    proof = {"schema_version": 1, "kind": preparation_kind, "result": "PASS", "request": request, "fixture": fixture}
    if tool: proof["observer_tool"] = tool
    preparation = write(root / "preparation.json", hook("preparation", proof))
    return {"schema_version": 1, "kind": "test-only-profile-manifest", "profile": profile, "family": family, "fixture_kind": "native-seam",
            "fixture": {"fixture_id": "private-fixture", "fixture_version": "1", "fork_variant": "private-genesis"},
            "identity": {"kind": "test-only", "chain_id": str(chain_id), "genesis_hash": identity["genesis_hash"],
                         "native_config_sha256": identity["config_sha256"]},
            "build_manifest": build, "preparation": preparation,
            "runner": {"slice": slice_name, "request": request, "observer_tool": tool},
            "inputs": [inputs["genesis"], inputs["blocks"], inputs["full_chain"]], "oracle": expected,
            "state_format": "prepared-genesis-plus-rlp-blocks", "supporting_services": [], "public_correspondence": None}


class ProfileManifestTests(unittest.TestCase):
    def deadline(self):
        return time.monotonic() + 30

    def test_each_family_binds_to_its_own_preparation(self):
        for slice_name in FAMILIES:
            with tempfile.TemporaryDirectory() as temp:
                manifest = family_tree(Path(temp).resolve(), slice_name)
                digest = profile_manifest.verify(manifest, self.deadline())
                self.assertEqual(digest, profile_manifest.evidence.digest(manifest))

    def test_manifest_field_changes_reject(self):
        cases = {
            "public identity": lambda m: m["identity"].update(kind="public"),
            "public correspondence": lambda m: m.update(public_correspondence={"path": "/x", "sha256": "0" * 64, "size_bytes": 1}),
            "local-e2e kind": lambda m: m.update(fixture_kind="local-e2e"),
            "other family": lambda m: m.update(family="BSC"),
            "other profile family": lambda m: m.update(profile="bsc-testnet"),
            "other profile in family": lambda m: m.update(profile="ethereum-sepolia"),
            "chain id": lambda m: m["identity"].update(chain_id="1"),
            "genesis hash": lambda m: m["identity"].update(genesis_hash="0x" + "3" * 64),
            "config digest": lambda m: m["identity"].update(native_config_sha256="4" * 64),
            "missing input": lambda m: m["inputs"].pop(),
            "swapped oracle": lambda m: m.update(oracle=m["inputs"][0]),
            "stale input hash": lambda m: m["inputs"][0].update(sha256="5" * 64),
            "supporting service": lambda m: m["supporting_services"].append("heimdall"),
            "unknown field": lambda m: m.update(extra=True),
            "other slice": lambda m: m["runner"].update(slice="bsc-parlia-import"),
            "observer tool on geth": lambda m: m["runner"].update(observer_tool=m["oracle"]),
        }
        for name, mutate in cases.items():
            with tempfile.TemporaryDirectory() as temp:
                manifest = family_tree(Path(temp).resolve(), "geth-import")
                mutate(manifest)
                with self.subTest(case=name), self.assertRaises(ValueError):
                    profile_manifest.verify(manifest, self.deadline())

    def test_prepared_document_changes_reject_for_their_own_reason(self):
        other = {"path": "/other", "sha256": "0" * 64, "size_bytes": 1}
        cases = {
            "failed preparation": ("preparation", lambda v: v.update(result="FAIL"), "accepted passing proof"),
            "wrong preparation kind": ("preparation", lambda v: v.update(kind="bor-fixture-preparation"), "accepted passing proof"),
            "incomplete cleanup": ("preparation", lambda v: v.update(cleanup_complete=False), "accepted passing proof"),
            "preparation names other request": ("preparation", lambda v: v.update(request=other), "Runner request"),
            "preparation names other fixture": ("preparation", lambda v: v.update(fixture=other), "bind different fixtures"),
            "preparation names other tool": ("preparation", lambda v: v.update(observer_tool=other), "Observer tool differs"),
            "other request kind": ("request", lambda v: v.update(kind="bor-native-import"), "Request kind or profile"),
            "other profile association": ("request", lambda v: v.update(profile_association="bsc-testnet"), "Request kind or profile"),
            "other request build": ("request", lambda v: v.update(build_manifest=other), "different builds"),
            "public fixture identity": ("fixture", lambda v: v["identity"].update(kind="public"), "private local identity"),
            "missing fixture identity": ("fixture", lambda v: v.pop("identity"), "private local identity"),
            "string chain id": ("fixture", lambda v: v["identity"].update(chain_id="714"), "Test-only identity"),
            "other fixture kind": ("fixture", lambda v: v.update(kind="bor-private"), "Fixture kind"),
            "other fixture input": ("fixture", lambda v: v.update(blocks=v["genesis"]), "Manifest inputs"),
            "other tool kind": ("tool", lambda v: v.update(kind="bor-native-observer"), "Observer tool kind"),
            "other tool build": ("tool", lambda v: v.update(build_manifest=other), "Observer tool kind"),
        }
        for name, (document, mutate, reason) in cases.items():
            with tempfile.TemporaryDirectory() as temp:
                root = Path(temp).resolve()
                manifest = family_tree(root, "bsc-parlia-import", {document: mutate})
                with self.subTest(case=name), self.assertRaisesRegex(ValueError, reason):
                    profile_manifest.verify(manifest, self.deadline())
        with tempfile.TemporaryDirectory() as temp:
            manifest = family_tree(Path(temp).resolve(), "bsc-parlia-import")
            manifest["runner"]["observer_tool"] = None
            with self.assertRaisesRegex(ValueError, "Observer tool differs"):
                profile_manifest.verify(manifest, self.deadline())

    def test_cli_requires_external_pin_and_never_grants_public_credit(self):
        import subprocess, sys
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            ref = write(root / "manifest.json", family_tree(root, "bor-import"))
            script = str(Path(profile_manifest.__file__))
            good = subprocess.run([sys.executable, "-I", script, ref["path"], "--sha256", ref["sha256"]], capture_output=True, text=True)
            self.assertEqual(good.returncode, 0, good.stdout)
            self.assertEqual(json.loads(good.stdout)["public_profile_credit"], False)
            bad = subprocess.run([sys.executable, "-I", script, ref["path"], "--sha256", "0" * 64], capture_output=True, text=True)
            self.assertEqual(bad.returncode, 2)
            self.assertFalse(json.loads(bad.stdout)["valid"])


if __name__ == "__main__": unittest.main()
