"""Synthetic protocol controls; these tests do not compile native clients."""

import copy
from pathlib import Path
import tempfile
import unittest

import build_contract as contract


SOURCE = Path(__file__).resolve().parents[2]


class PlanTests(unittest.TestCase):
    def setUp(self):
        self.registry, self.lock, self.registry_hash, self.lock_hash = contract.inputs(SOURCE)
        self.plan = contract.expected_plan(self.registry, self.lock, self.registry_hash, self.lock_hash)

    def test_complete_owned_coverage_and_later_work(self):
        self.assertEqual(contract.validate_plan(self.plan, SOURCE), self.plan)
        self.assertEqual(len(self.plan["sources"]), 10)
        self.assertEqual(len(self.plan["profiles"]), 7)
        self.assertEqual(len(self.plan["packages"]), 13)
        self.assertEqual(len(self.plan["package_indexes"]), 3)
        self.assertIn("common-project-artifacts", self.plan["later_outputs"])
        self.assertNotIn("resolved-os-package-closures", self.plan["later_outputs"])
        bsc = next(row for row in self.plan["profiles"] if row["id"] == "bsc-testnet")
        self.assertIsNone(bsc["consensus_component"])
        polygon = next(row for row in self.plan["profiles"] if row["id"] == "polygon-pos-amoy")
        self.assertEqual(polygon["supporting_l1"]["profile"], "ethereum-sepolia")
        self.assertTrue(polygon["supporting_l1"]["dedicated_instance"])

    def test_resource_choices_are_bounded_and_explicit(self):
        plan = contract.expected_plan(self.registry, self.lock, self.registry_hash, self.lock_hash, "linux/arm64",
                                      dict(contract.DEFAULT_LIMITS, cpus=1, command_seconds=60))
        self.assertEqual(contract.validate_plan(plan, SOURCE), plan)
        for field, value in (("cpus", True), ("cpus", 5), ("pids", 0), ("command_seconds", 3601),
                             ("memory_bytes", 8 * 1024 ** 3)):
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                contract.expected_plan(self.registry, self.lock, self.registry_hash, self.lock_hash,
                                       resource_limits=dict(contract.DEFAULT_LIMITS, **{field: value}))

    def test_plan_cannot_change_source_lock_profile_or_scope(self):
        mutations = [lambda p: p.update(task_id="E01-01.evidence"), lambda p: p.update(schema_version=True),
                     lambda p: p.update(lock_sha256="0" * 64), lambda p: p["sources"]["bor"].update(commit="0" * 40),
                     lambda p: p["images"]["golang-build-image"].update(reference="golang:latest"),
                     lambda p: p["native_components"].pop(), lambda p: p["profiles"].pop(),
                     lambda p: p["profiles"][0].update(native_component="bsc"),
                     lambda p: p["profiles"][-1]["supporting_l1"].update(share_with_other_profiles=True),
                     lambda p: p["package_indexes"].pop(), lambda p: p.update(patches=["unknown.patch"]),
                     lambda p: p.update(arbitrary_command=["true"])]
        for mutation in mutations:
            value = copy.deepcopy(self.plan)
            mutation(value)
            with self.subTest(value=value), self.assertRaises(ValueError):
                contract.validate_plan(value, SOURCE)

    def test_inventory_never_silently_shrinks(self):
        value = copy.deepcopy(self.registry)
        value["components"].pop()
        self.assertNotEqual({row["id"] for row in value["components"]}, set(self.lock["components"]))

    def test_json_rejects_duplicate_and_nonfinite_fields(self):
        for data in ('{"a":1,"a":2}', '{"a":NaN}', '{"a":Infinity}'):
            with self.subTest(data=data), self.assertRaises(ValueError):
                contract.parse_json(data)

    def test_references_require_actual_exact_safe_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / "synthetic.bin"
            path.write_bytes(b"explicitly synthetic fixture\n")
            ref = contract.reference(root, path)
            self.assertEqual(contract.verify_reference(root, ref), path.resolve())
            for field, value in (("sha256", "0" * 64), ("size_bytes", True), ("size_bytes", 0),
                                 ("path", "../synthetic.bin"), ("path", "/synthetic.bin"),
                                 ("path", "./synthetic.bin"), ("path", "missing.bin")):
                with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                    contract.verify_reference(root, dict(ref, **{field: value}))
            (root / "link.bin").symlink_to(path)
            with self.assertRaises(ValueError):
                contract.verify_reference(root, dict(ref, path="link.bin"))
            path.write_bytes(b"different bytes\n")
            with self.assertRaises(ValueError):
                contract.verify_reference(root, ref)

    def test_untrusted_command_result_cannot_claim_success(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            log = root / "synthetic.log"
            log.write_text("synthetic output\n")
            row = {"argv": ["synthetic-only"], "env": {}, "exit_code": 0, "started_at": 1, "finished_at": 2,
                   "stdout": contract.reference(root, log), "stderr": contract.reference(root, log)}
            contract.command_record(row, root)
            for field, value in (("exit_code", False), ("exit_code", 1), ("started_at", True),
                                 ("finished_at", float("nan")), ("finished_at", 0)):
                with self.subTest(field=field), self.assertRaises(ValueError):
                    contract.command_record(dict(row, **{field: value}), root)


if __name__ == "__main__":
    unittest.main()
