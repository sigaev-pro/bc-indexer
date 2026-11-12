"""Portable controls of the ETH corpus driver; no Docker or Go toolchain needed."""

import copy
import hashlib
import json
from pathlib import Path
import re
import sys
import tempfile
import unittest
import unittest.mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import corpus_eth  # noqa: E402

EXPECTED = json.loads((corpus_eth.CORPUS / "expected" / "eth.json").read_text())
PROBE = (corpus_eth.ETH / "probe.go").read_text()


def good_probe():
    """A probe output whose native self-checks all hold."""
    records = {"extractor-decision-v1/geth-corpus.r/counter": "{\"next\":2}"}
    imports = {"attach_error": None, "insert_error": None, "head": {"number": 2, "hash": "0x01"},
               "admission_records_before": {}, "decision_records_before": {}, "decision_records_after": records}
    restart = dict(imports, admission_records_before={"a": "1"}, decision_records_before=records)
    checks = [{"number": 1, "root_equal": True, "cumulative_gas_used": 228538, "header_gas_used": 228538,
               "receipts": [{"index": n, "receipt_equal": True} for n in range(4)]},
              {"number": 2, "root_equal": True, "cumulative_gas_used": 0, "header_gas_used": 0, "receipts": []}]
    return {"schema_version": 1, "kind": "native-corpus-observation", "family": "eth",
            "native": {"live_import": imports, "restart_import": restart, "reexecution": checks}}


class NativeChecks(unittest.TestCase):
    def test_good_probe_passes(self):
        self.assertEqual(corpus_eth.native_failures(good_probe(), 2), [])

    def test_each_defect_fails(self):
        defects = {
            "AttachDecisions failed": lambda n: n["live_import"].update(attach_error="boom"),
            "restart_import: InsertChain failed": lambda n: n["restart_import"].update(insert_error="boom"),
            "head is not block 2": lambda n: n["restart_import"].update(head={"number": 1}),
            "held extractor records": lambda n: n["live_import"].update(admission_records_before={"k": "v"}),
            "held extractor records ": lambda n: n["live_import"].update(decision_records_before={"k": "v"}),
            "no decision record": lambda n: n["live_import"].update(decision_records_after={}),
            "root differs": lambda n: n["reexecution"][0].update(root_equal=False),
            "gas differs": lambda n: n["reexecution"][1].update(cumulative_gas_used=1),
            "receipt 2 differs": lambda n: n["reexecution"][0]["receipts"][2].update(receipt_equal=False),
            "not one check per block": lambda n: n["reexecution"].pop(),
        }
        for message, change in defects.items():
            probe = good_probe()
            change(probe["native"])
            failures = corpus_eth.native_failures(probe, 2)
            self.assertTrue(any(message.strip() in row for row in failures), (message, failures))

    def test_missing_native_section(self):
        self.assertEqual(corpus_eth.native_failures({}, 2), ["observation has no native section"])

    def test_truthy_strings_are_not_true(self):
        probe = good_probe()
        probe["native"]["reexecution"][0]["root_equal"] = "true"
        self.assertTrue(corpus_eth.native_failures(probe, 2))
        probe = good_probe()
        del probe["native"]["reexecution"][0]["receipts"][1]["receipt_equal"]
        self.assertEqual(corpus_eth.native_failures(probe, 2), ["reexecution: block 1 receipt 1 differs"])


class Source(unittest.TestCase):
    def test_source_is_added_once(self):
        observation = corpus_eth.with_source(good_probe(), {"native": "x"})
        self.assertEqual(observation["source"], {"native": "x"})
        with self.assertRaisesRegex(Exception, "must not write the source"):
            corpus_eth.with_source(observation, {"native": "y"})


class Install(unittest.TestCase):
    def record(self):
        patch = {"name": "p", "file": "core/a.go", "sha256_before": "00", "sha256_after": "11"}
        row = {"relative": "extractor/a.go", "copy": {"sha256": "22"}}
        return {"patches": [patch], "files": [], "packages": [row, {"relative": "cmd/x/probe.go", "copy": {"sha256": "33"}}],
                "decision": {"patches": [dict(patch, name="q")], "files": [{"relative": "core/b.go", "copy": {"sha256": "22"}}]}}

    def test_equal_installs(self):
        accepted = self.record()
        installed = copy.deepcopy(accepted)
        # The accepted admission probe is not part of the corpus install.
        installed["packages"] = installed["packages"][:1]
        self.assertEqual(corpus_eth.install_differences(accepted, installed), [])

    def test_each_part_differs(self):
        for key, change in {"patches": lambda r: r["decision"]["patches"][0].update(sha256_after="ff"),
                            "files": lambda r: r["decision"]["files"][0]["copy"].update(sha256="ff"),
                            "packages": lambda r: r["packages"][0]["copy"].update(sha256="ff")}.items():
            installed = self.record()
            change(installed)
            self.assertEqual(corpus_eth.install_differences(self.record(), installed), [key])

    def test_repository_equal(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        path = corpus_eth.ROOT / "adapters" / "geth" / "decision" / "decision.go"
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        origin = str(root / "measured" / "adapters" / "geth" / "decision" / "decision.go")
        rows = [{"origin": {"path": origin}, "copy": {"sha256": digest}},
                {"origin": {"path": origin.replace("decision.go", "README.md")}, "copy": {"sha256": "00"}}]
        self.assertEqual(corpus_eth.repository_equal(rows), {"adapters/geth/decision/decision.go": True,
                                                             "adapters/geth/decision/README.md": False})


class ProbeInputs(unittest.TestCase):
    """The probe's literal inputs are the transactions and accounts of ORACLE.md and the expected file."""

    def literal(self, name):
        return re.search(name + r'\s+= "([0-9a-f]+)"', PROBE).group(1)

    def test_contract_code(self):
        additions = {row["address"]: row["code"] for row in EXPECTED["genesis_additions"]}
        self.assertEqual(self.literal("childCode"), additions["3000000000000000000000000000000000000002"])
        self.assertEqual(self.literal("parentCode"), additions["3000000000000000000000000000000000000001"])

    def test_addresses(self):
        for name, address in (("coinbase", EXPECTED["coinbase"]), ("parent", "3000000000000000000000000000000000000001"),
                              ("child", "3000000000000000000000000000000000000002"),
                              ("recipient", "4000000000000000000000000000000000000001"),
                              ("sender", "7e5f4552091a69125d5dfcb7b8c2659029395bdf")):
            self.assertRegex(PROBE, rf'{name}\s+= common.HexToAddress\("0x{address}"\)')

    def test_transactions(self):
        rows = [tx["transaction"] for tx in EXPECTED["blocks"][0]["transactions"]]
        self.assertEqual([row["nonce"] for row in rows], [0, 1, 2, 3])
        self.assertIn("sign(&types.LegacyTx{Nonce: 0, To: &recipient, Value: ether, Gas: 21_000, GasPrice: price})",
                      PROBE)
        self.assertIn("Value: big.NewInt(gwei),\n\t\t\tGas: 21_000, GasFeeCap: big.NewInt(2_200_000_000), "
                      "GasTipCap: big.NewInt(1_500_000_000)})", PROBE)
        self.assertIn("sign(&types.LegacyTx{Nonce: 2, To: &parent, Value: big.NewInt(5), Gas: 200_000, GasPrice: price})",
                      PROBE)
        self.assertIn("sign(&types.LegacyTx{Nonce: 3, To: &child, Value: big.NewInt(7), Gas: 100_000, GasPrice: price})",
                      PROBE)
        self.assertEqual([(r["type"], r["value"], r["gas"]) for r in rows],
                         [(0, "1000000000000000000", 21000), (2, "1000000000", 21000), (0, "5", 200000),
                          (0, "7", 100000)])
        self.assertEqual({rows[n]["gas_price"] for n in (0, 2, 3)}, {"2000000000"})
        self.assertIn("price := big.NewInt(2 * gwei)", PROBE)

    def test_shipped_attach_options(self):
        self.assertIn("extractorgeth.AttachDecisions(chain, db, cfg, extractorgeth.Options{}, "
                      "extractorgeth.DecisionOptions{})", PROBE)


class CommandLine(unittest.TestCase):
    def test_usage(self):
        with self.assertRaises(SystemExit) as raised, unittest.mock.patch("sys.stderr"):
            corpus_eth.main([])
        self.assertEqual(raised.exception.code, 2)

    def test_relative_preparation_is_refused(self):
        with unittest.mock.patch("sys.stdout") as stdout:
            self.assertEqual(corpus_eth.main(["run", "relative.json", "--sha256", "0" * 64, "--output", "/tmp/x"]), 2)
        self.assertIn("valid", "".join(call.args[0] for call in stdout.write.call_args_list))



if __name__ == "__main__":
    unittest.main()
