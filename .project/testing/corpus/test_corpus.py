"""Negative checks for corpus definition and static allocation, not product tests."""

import copy
import importlib.util
from pathlib import Path
import unittest

SPEC = importlib.util.spec_from_file_location("boot08_corpus_verify", Path(__file__).with_name("verify.py"))
VERIFY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VERIFY)


class CorpusTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.catalog = VERIFY.read_json(VERIFY.HERE / "catalog.json")
        cls.sources = VERIFY.read_json(VERIFY.HERE / "candidate-sources.json")
        cls.metadata = VERIFY.validate_definition(cls.catalog, cls.sources)
        cls.cells = VERIFY.expand(cls.catalog, cls.sources, cls.metadata)
        cls.by_id = {cell["cell_id"]: cell for cell in cls.cells}

    def definition_rejected(self, change, source=False):
        catalog, sources = copy.deepcopy(self.catalog), copy.deepcopy(self.sources)
        change(sources if source else catalog)
        with self.assertRaises(ValueError):
            VERIFY.validate_definition(catalog, sources)

    def allocation_run(self, suite="quick", profile="ethereum-mainnet", recipe=None):
        cells = [cell["cell_id"] for cell in self.cells
                 if cell["profile"] == profile and cell["suite"] == suite
                 and (recipe is None or cell["recipe"] == recipe)]
        return {
            "run_id": "unit-fixture-only", "suite": suite, "profiles": [profile],
            "duration_mode": "standard", "total_seconds": 1800,
            "phase_seconds": {"startup": 60, "scenario": 900, "shutdown_drain": 300,
                              "stream_fence": 240, "reader_compare": 300},
            "resources": {"cpu_count": 4, "ram_bytes": 8 * 1024**3, "disk_bytes": 100 * 1024**3,
                          "queue_bytes": 16 * 1024**2, "parallelism": 1},
            "cells": cells, "preparation_in_measured_run": False, "runtime": "NOT_RUN",
            "signing": {profile: "none" if profile.endswith("mainnet") else "testnet_only"},
            "quotas": {"seed_count": 8, "actions_per_seed": 256, "soak_seconds": 600, "soak_actions": 1000},
            "backlog": {"pending_batches": 32, "pending_bytes": 64 * 1024**2,
                        "plan_exact_bytes_ref": "unit-test-reference-not-runtime-evidence"},
        }

    def reject_run(self, run):
        with self.assertRaises(ValueError):
            VERIFY.validate_run(run, self.catalog, self.by_id)

    def test_complete_definition_is_not_runtime_acceptance(self):
        self.assertEqual(len(self.metadata), 217)
        self.assertEqual({cell["runtime"] for cell in self.cells}, {"NOT_RUN"})
        self.assertEqual({cell["resolution"] for cell in self.cells}, {"PENDING_E01_E06"})
        self.assertEqual(set(VERIFY.PROFILE_IDS), set(self.catalog["profiles"]))

    def test_empty_or_missing_oracle_contract_rejected(self):
        self.definition_rejected(lambda value: value["oracles"].update({"native-and-contract": {}}))
        self.definition_rejected(lambda value: value["oracles"].pop("native-and-contract"))

    def test_oracle_fields_are_required_and_typed(self):
        for field in ["owner", "inputs", "proof", "forbidden"]:
            with self.subTest(missing=field):
                self.definition_rejected(lambda value: value["oracles"]["native-and-contract"].pop(field))
        for field, replacement in [("owner", 1), ("inputs", "native bytes"), ("inputs", []),
                                   ("proof", []), ("proof", " \t "), ("forbidden", {}),
                                   ("forbidden", []), ("forbidden", [""])]:
            with self.subTest(field=field, replacement=replacement):
                self.definition_rejected(
                    lambda value: value["oracles"]["native-and-contract"].update({field: replacement}))

    def test_oracle_owner_must_exist_in_task_registry(self):
        self.definition_rejected(
            lambda value: value["oracles"]["native-and-contract"].update(owner="E99-99.fake"))

    def test_oracle_provenance_is_typed_and_closed(self):
        for replacement in [{}, {"provenance": "normalizer_output", "description": "Expected records"},
                            {"provenance": "native_block_transaction_bytes", "description": ""},
                            {"provenance": [], "description": "Original native bytes"},
                            {"provenance": "accepted_contract", "description": "Wrong oracle category"}]:
            with self.subTest(replacement=replacement):
                self.definition_rejected(
                    lambda value: value["oracles"]["native-and-contract"]["inputs"].__setitem__(0, replacement))
        self.definition_rejected(lambda value: value["oracles"]["native-and-contract"]["inputs"].pop())
        self.definition_rejected(lambda value: value["oracles"]["native-and-contract"]["inputs"].append(
            copy.deepcopy(value["oracles"]["native-and-contract"]["inputs"][0])))

    def test_missing_resolution_contract_rejected(self):
        self.definition_rejected(lambda value: value.pop("resolution"))
        self.definition_rejected(lambda value: value.update(resolution={}))

    def test_resolution_fields_are_required_and_typed(self):
        for field in VERIFY.RESOLUTION_ARTIFACTS | {"owner", "rule", "profile_owners", "artifact_owners"}:
            with self.subTest(missing=field):
                self.definition_rejected(lambda value: value["resolution"].pop(field))
        for field in VERIFY.RESOLUTION_ARTIFACTS | {"rule"}:
            for replacement in [" ", [], None]:
                with self.subTest(field=field, replacement=replacement):
                    self.definition_rejected(lambda value: value["resolution"].update({field: replacement}))

    def test_resolution_owner_mappings_use_real_tasks(self):
        self.definition_rejected(lambda value: value["resolution"].update(owner="E99-99.fake"))
        self.definition_rejected(lambda value: value["resolution"]["profile_owners"].pop("ethereum-hoodi"))
        for replacement in [[], ["E99-99.fake"], ["E01-01.harness"], "E06-13.init"]:
            with self.subTest(profile_owners=replacement):
                self.definition_rejected(lambda value: value["resolution"]["profile_owners"].update(
                    {"ethereum-hoodi": replacement}))
        self.definition_rejected(lambda value: value["resolution"]["artifact_owners"].update(compiler=[]))
        self.definition_rejected(lambda value: value["resolution"]["artifact_owners"].update(
            compiler=["E99-99.fake"]))

    def test_gate_assignments_are_required_typed_and_complete(self):
        self.definition_rejected(lambda value: value.pop("gates"))
        for replacement in [[], {}, ["E01-01.harness"], [{"owner": "E01-01.harness"}]]:
            with self.subTest(replacement=replacement):
                self.definition_rejected(lambda value: value["gates"].update(E01=replacement))
        self.definition_rejected(lambda value: value["gates"]["E01"][0].update(responsibility=" "))
        self.definition_rejected(lambda value: value["gates"]["E01"].pop())

    def test_gate_owner_must_exist_and_match_stage(self):
        self.definition_rejected(lambda value: value["gates"]["E01"][0].update(owner="E99-99.fake"))
        self.definition_rejected(lambda value: value["gates"]["E01"][0].update(owner="E07-02.e2e"))
        self.definition_rejected(lambda value: value["gates"]["E01"].append(
            copy.deepcopy(value["gates"]["E01"][0])))

    def test_task_registry_preserves_digits_in_task_names(self):
        self.assertEqual(len(VERIFY.product_tasks()), 68)
        self.assertIn("E07-02.e2e", VERIFY.product_tasks())
        self.assertNotIn("E07-02.e", VERIFY.product_tasks())
        self.assertEqual(self.metadata["AC-DEV-02-01"]["owner"], "E07-02.e2e")

    def test_document_projection_preserves_existing_schema_v3_doc_identity(self):
        documents = [cell for cell in self.cells if cell["mode"] == "document"]
        self.assertEqual(len(documents), 51)
        for cell in documents:
            projection = cell["case_registry_projection"]
            self.assertEqual(cell["profile"], "COMMON")
            self.assertEqual(projection["profile"], "DOC")
            self.assertEqual(projection["profile_manifest_digest"], "NOT_APPLICABLE")
            self.assertEqual(projection["fork_variant"], "NOT_APPLICABLE")
            self.assertEqual(projection["fixture_kind"], "document")
            self.assertEqual(projection["task_id"], cell["owner"])
            self.assertEqual(projection["ac_id"], cell["ac"])

    def test_runtime_projection_has_no_fabricated_digest_or_new_fixture_enum(self):
        for cell in self.cells:
            if cell["mode"] == "document":
                continue
            projection = cell["case_registry_projection"]
            self.assertEqual(projection["profile"], cell["profile"])
            self.assertIsNone(projection["profile_manifest_digest"])
            self.assertIsNone(projection["fork_variant"])
            if cell["mode"] in {"synthetic", "harness"}:
                self.assertIsNone(projection["fixture_kind"])
            else:
                self.assertEqual(projection["fixture_kind"], cell["mode"])

    def test_common_cannot_become_runtime_profile(self):
        with self.assertRaises(ValueError):
            VERIFY.case_registry_projection("AC-DEV-02-01", "E07-02.e2e", "COMMON", "functional",
                                            "local-e2e", "baseline", "example", "current")

    def test_missing_ac_rejected(self):
        self.definition_rejected(lambda value: value["cases"].pop())

    def test_missing_variant_rejected(self):
        self.definition_rejected(lambda value: value["cases"][0]["variants"].pop())

    def test_missing_required_recipe_rejected(self):
        self.definition_rejected(lambda value: value["scenarios"].pop())

    def test_missing_profile_assignment_rejected(self):
        def mutate(value):
            case = next(item for item in value["cases"] if len(item["profiles"]) == 7)
            case["profiles"].remove("ethereum-hoodi")
        self.definition_rejected(mutate)

    def test_relabeling_synthetic_as_native_rejected(self):
        def mutate(value):
            scenario = next(item for item in value["scenarios"] if item["id"] == "large-record")
            scenario["modes"] = ["local-e2e"]
        self.definition_rejected(mutate)

    def test_renamed_variant_rejected(self):
        self.definition_rejected(lambda value: value["cases"][0]["variants"][0].update(id="renamed"))

    def test_cross_profile_scope_rejected(self):
        def mutate(value):
            case = next(item for item in value["cases"] if item["profiles"] == ["COMMON"])
            case["variants"][0]["profiles"] = ["ethereum-mainnet"]
        self.definition_rejected(mutate)

    def test_runtime_claim_rejected(self):
        self.definition_rejected(lambda value: value.update(runtime="PASS"))

    def test_stale_ac_binding_rejected(self):
        self.definition_rejected(lambda value: value.update(ac_rows_sha256="0" * 64))

    def test_unreviewed_source_change_rejected(self):
        self.definition_rejected(lambda value: value["profiles"]["bsc-testnet"].update(chain_id=56), source=True)

    def test_false_runtime_hash_rejected(self):
        self.definition_rejected(lambda value: value["runtime_outputs"].update(genesis_hashes="0" * 64), source=True)

    def test_duplicate_json_field_rejected(self):
        with self.assertRaises(ValueError):
            VERIFY.unique_object([("profile", "a"), ("profile", "b")])

    def test_every_existing_ac_has_its_original_owner(self):
        for ac, metadata in self.metadata.items():
            values = [cell for cell in self.cells if cell["ac"] == ac]
            self.assertTrue(values)
            self.assertEqual({cell["owner"] for cell in values}, {metadata["owner"]})

    def test_fork_clocks_and_genesis_are_not_interchangeable(self):
        groups = VERIFY.activation_groups("ethereum-mainnet", self.sources)
        self.assertIn("total_difficulty:58750000000000000000000", {group["id"] for group in groups})
        genesis = [cell for cell in self.cells if cell["recipe"] == "fork-boundary"
                   and cell["dimension"].endswith(":0")]
        self.assertTrue(genesis)
        self.assertNotIn("before", {cell["variant"] for cell in genesis})

    def test_hoodi_cancellation_and_sepolia_activation_remain_separate(self):
        hoodi = self.sources["profiles"]["ethereum-hoodi"]["forks"]
        sepolia = self.sources["profiles"]["ethereum-sepolia"]["forks"]
        self.assertIsNone(next(fork["value"] for fork in hoodi if fork["field"] == "amsterdamTime"))
        self.assertEqual(next(fork["value"] for fork in sepolia if fork["field"] == "amsterdamTime"), "1791294816")
        self.assertIsNone(self.sources["consensus_forks"]["ethereum-hoodi"]["epochs"]["Gloas"])
        self.assertEqual(self.sources["consensus_forks"]["ethereum-sepolia"]["epochs"]["Gloas"], "353024")

    def test_native_seam_does_not_close_current_e2e(self):
        key = ("ethereum-mainnet", "call-tree", "call")
        cells = [cell for cell in self.cells
                 if (cell["profile"], cell["recipe"], cell["variant"]) == key]
        self.assertIn("native-seam", {cell["mode"] for cell in cells})
        self.assertIn("local-e2e", {cell["mode"] for cell in cells})
        self.assertEqual({cell["dimension"] for cell in cells if cell["mode"] == "local-e2e"}, {"current"})

    def test_large_synthetic_records_are_not_native_evidence(self):
        values = [cell for cell in self.cells if cell["recipe"] == "large-record"
                  or cell["variant"] == "synthetic_over_2gib"]
        self.assertTrue(values)
        self.assertEqual({cell["mode"] for cell in values}, {"synthetic"})

    def test_cas20_all_routes_and_methods_expand(self):
        expected = {(route, method["selector_name"]) for route, methods in
                    self.sources["cas20"]["routes"].items() for method in methods}
        for profile in ["bsc-mainnet", "bsc-testnet"]:
            actual = {tuple(cell["dimension"].split(":")[1:]) for cell in self.cells
                      if cell["recipe"] == "cas20" and cell["profile"] == profile}
            self.assertEqual(actual, expected)
        self.assertEqual(len(expected), 159)

    def test_all_size_boundaries_are_explicit(self):
        actual = {cell["dimension"] for cell in self.cells if cell["recipe"] == "wire-boundaries"}
        self.assertEqual(actual, set(self.catalog["size_boundaries"]))
        self.assertNotIn("each-size-boundary", actual)

    def test_pairwise_generator_covers_every_pair(self):
        factors = self.catalog["pairwise"]["factors"]
        rows = VERIFY.pairwise_rows(factors)
        VERIFY.validate_pairwise(rows, factors, [])
        self.assertEqual(rows, VERIFY.pairwise_rows(factors))
        with self.assertRaises(ValueError):
            VERIFY.validate_pairwise([], factors, [])

    def test_pairwise_exclusion_requires_source_review(self):
        with self.assertRaises(ValueError):
            VERIFY.validate_pairwise([], self.catalog["pairwise"]["factors"], [{
                "pair": ["branch", "stable", "delivery", "online"],
                "source_proof_ref": "", "review_ref": "",
            }])

    def test_standard_run_allocation_valid_without_granting_ready(self):
        run = self.allocation_run()
        plan = {"schema_version": 1, "catalog_sha256": VERIFY.digest(self.catalog), "purpose": "acceptance",
                "profiles": run["profiles"], "suites": ["quick"], "runs": [run], "runtime": "NOT_RUN"}
        result = VERIFY.validate_campaign(plan, self.catalog, self.cells)
        self.assertEqual(result["allocation"], "COMPLETE")
        self.assertFalse(result["ready"])
        self.assertEqual(result["runtime"], "NOT_RUN")

    def test_fresh_campaign_digest_cannot_bypass_invalid_oracle_or_resolution(self):
        run = self.allocation_run()
        for mutate in [
            lambda value: value["oracles"].update({"native-and-contract": {}}),
            lambda value: value["oracles"]["native-and-contract"].update(owner="E99-99.fake"),
            lambda value: value.pop("resolution"),
            lambda value: value["gates"].update(E01=[]),
        ]:
            catalog = copy.deepcopy(self.catalog)
            mutate(catalog)
            plan = {"schema_version": 1, "catalog_sha256": VERIFY.digest(catalog), "purpose": "acceptance",
                    "profiles": run["profiles"], "suites": ["quick"], "runs": [run], "runtime": "NOT_RUN"}
            with self.assertRaises(ValueError):
                VERIFY.validate_campaign(plan, catalog, self.cells)

    def test_valid_prose_edit_needs_review_and_invalidates_old_campaign_digest(self):
        catalog = copy.deepcopy(self.catalog)
        catalog["oracles"]["native-and-contract"]["proof"] += " Preserve the reviewed provenance."
        VERIFY.validate_definition(catalog, self.sources)
        run = self.allocation_run()
        plan = {"schema_version": 1, "catalog_sha256": VERIFY.digest(self.catalog), "purpose": "acceptance",
                "profiles": run["profiles"], "suites": ["quick"], "runs": [run], "runtime": "NOT_RUN"}
        with self.assertRaisesRegex(ValueError, "stale campaign definition"):
            VERIFY.validate_campaign(plan, catalog, self.cells)
        plan["catalog_sha256"] = VERIFY.digest(catalog)
        result = VERIFY.validate_campaign(plan, catalog, self.cells)
        self.assertEqual(result["allocation"], "COMPLETE")
        self.assertFalse(result["ready"])

    def test_missing_cell_blocks_acceptance_but_stays_visible_in_diagnostic(self):
        run = self.allocation_run()
        missing = run["cells"].pop()
        plan = {"schema_version": 1, "catalog_sha256": VERIFY.digest(self.catalog), "purpose": "acceptance",
                "profiles": run["profiles"], "suites": ["quick"], "runs": [run], "runtime": "NOT_RUN"}
        with self.assertRaises(ValueError):
            VERIFY.validate_campaign(plan, self.catalog, self.cells)
        plan["purpose"] = "diagnostic"
        result = VERIFY.validate_campaign(plan, self.catalog, self.cells)
        self.assertEqual(result["allocation"], "PARTIAL")
        self.assertIn(missing, result["missing_cells"])

    def test_standard_deadline_cannot_be_extended(self):
        run = self.allocation_run()
        run["total_seconds"] = 1801
        self.reject_run(run)

    def test_comparison_time_is_inside_run(self):
        run = self.allocation_run()
        run["phase_seconds"]["reader_compare"] += 1
        self.reject_run(run)

    def test_preparation_cannot_supply_actual_actions(self):
        run = self.allocation_run()
        run["preparation_in_measured_run"] = True
        self.reject_run(run)

    def test_public_mainnet_signing_rejected(self):
        run = self.allocation_run("public-smoke")
        run["signing"]["ethereum-mainnet"] = "testnet_only"
        self.reject_run(run)

    def test_public_testnet_contract_requires_signer(self):
        run = self.allocation_run("public-smoke", "ethereum-sepolia", "public-owned-contract")
        VERIFY.validate_run(run, self.catalog, self.by_id)
        run["signing"]["ethereum-sepolia"] = "none"
        self.reject_run(run)

    def test_soak_requires_duration_and_actions(self):
        run = self.allocation_run("soak")
        VERIFY.validate_run(run, self.catalog, self.by_id)
        for key, value in [("seed_count", 7), ("actions_per_seed", 255),
                           ("soak_seconds", 599), ("soak_actions", 999)]:
            changed = copy.deepcopy(run)
            changed["quotas"][key] = value
            self.reject_run(changed)

    def test_extended_soak_has_its_own_floor(self):
        run = self.allocation_run("soak")
        run.update(duration_mode="extended", total_seconds=5000)
        run["phase_seconds"]["scenario"] = 4000
        run["quotas"].update(soak_seconds=3600, soak_actions=10000)
        VERIFY.validate_run(run, self.catalog, self.by_id)
        run["quotas"]["soak_actions"] = 9999
        self.reject_run(run)

    def test_backlog_must_exceed_real_queue_capacity(self):
        run = self.allocation_run("soak")
        run["resources"]["queue_bytes"] = 64 * 1024**2
        self.reject_run(run)
        run["backlog"]["pending_bytes"] = 128 * 1024**2
        VERIFY.validate_run(run, self.catalog, self.by_id)

    def test_unknown_cell_cannot_hide_a_required_variant(self):
        run = self.allocation_run()
        run["cells"][0] += "/unknown"
        self.reject_run(run)

    def test_profile_and_suite_relabeling_rejected(self):
        run = self.allocation_run()
        run["profiles"] = ["ethereum-sepolia"]
        self.reject_run(run)


if __name__ == "__main__":
    unittest.main()
