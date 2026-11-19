"""Synthetic component controls; native evidence is separately retained."""

import copy
import json
import importlib.util
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


j = load("activation_strict_judge", HERE / "activation_judge.py")
helpers = load("activation_test_vectors", HERE.parent / "test_poly_judge.py")
c, num = j.codec, helpers.num


def fields(kind):
    to = j.octets(j.prior.SUCCESS)
    if kind == 0:
        return [b"", num(5_000_000_000), num(21000), to, b"\x07", b"", b"", b"", b""]
    if kind == 1:
        return [num(15001), b"\x01", num(5_000_000_000), num(21000), to, b"\x07", b"", [], b"", b"", b""]
    value = [num(15001), b"\x01", num(2_000_000_000), num(5_000_000_000), num(100000 if kind == 4 else 21000), to, b"\x07", b"", []]
    if kind == 4:
        auth = [num(15001), to, b""]
        auth += helpers.sign_fixture(c.keccak256(b"\x05" + c.rlp(auth)), 2)
        value.append([auth])
    return value + [b"", b"", b""]


class ActivationJudgeInputs(unittest.TestCase):
    def test_actual_record_bindings_include_head_and_observation_not_just_counts(self):
        helpers.PolygonJudgeProofTests.setUpClass()
        native = copy.deepcopy(helpers.PolygonJudgeProofTests.native)
        views = native["blocks"][:2]
        decisions = {key: value for key, value in native["decisions"].items()
                     if "/0000000000000002/" not in key and "/0000000000000003/" not in key}
        decisions["extractor-decision-v1/bor.run-1/counter"] = json.dumps({"next": 2})
        prefix = "extractor-admission-v1/bor.run-1/"
        admission = {prefix + "counter": json.dumps({"next": 2}),
                     prefix + "lowwater": json.dumps({"sequence": 2}), prefix + "purged": json.dumps({"sequence": 2})}
        row = {"decisions": decisions, "admission": admission, "head_hash": views[-1]["hash"],
               "observed": [1, 2], "keys": {"1": ["bor.run-1.0000000000000000"], "2": ["bor.run-1.0000000000000001"]}, "observer_errors": []}
        j.records(row, views, views[-1]["hash"], 2)
        for field, value in (("head_hash", views[0]["hash"]), ("observed", [1]), ("keys", {}), ("observer_errors", ["capture failed"]),
                             ("decisions", {}), ("admission", {})):
            changed = copy.deepcopy(row)
            changed[field] = value
            with self.subTest(field=field), self.assertRaises((ValueError, KeyError)):
                j.records(changed, views, views[-1]["hash"], 2)

    def test_historical_profile_has_only_scheduled_coherent_forks(self):
        baseline = {name: 0 for name in ("homesteadBlock", "eip150Block", "eip155Block", "eip158Block", "byzantiumBlock", "constantinopleBlock", "petersburgBlock", "istanbulBlock", "muirGlacierBlock")}
        baseline.update(chainId=15001, eip150Hash="0x" + "00" * 32)
        engine = {"hampiBlock": None, "sprint": {"0": 4}, "period": {"0": 1}, "producerDelay": {"0": 6}, "backupMultiplier": {"0": 1}, "stateSyncConfirmationDelay": {"0": 0}, "coinbase": {"0": "0x" + "00" * 20}, "burntContract": {"0": "0x" + "00" * 20}, "stateReceiverContract": "0x0000000000000000000000000000000000001001", "validatorContract": "0x0000000000000000000000000000000000001000"}
        for _, _, eth_count, bor_count in j.CASES:
            config = copy.deepcopy(baseline)
            config.update({name: 0 if i < eth_count else 8 for i, name in enumerate(j.profile.ETHEREUM_SCHEDULE)})
            config["bor"] = copy.deepcopy(engine)
            config["bor"].update({name: 0 if i < bor_count else 8 for i, name in enumerate(j.profile.BOR_SCHEDULE)})
            j.historical_config(config, eth_count, bor_count)
            for nested, field in ((False, "berlinBlock"), (False, "chainId"), (False, "osakaTime"), (False, "blobSchedule"), (True, "hampiBlock"), (True, "madhugiriBlock")):
                changed = copy.deepcopy(config)
                target = changed["bor"] if nested else changed
                target[field] = 999
                with self.subTest(eth=eth_count, field=field), self.assertRaises(ValueError):
                    j.historical_config(changed, eth_count, bor_count)

    def test_valid_signed_native_domains(self):
        for kind in (0, 1, 2, 4):
            with self.subTest(kind=kind):
                j.signed_input(helpers.signed_wire(kind, fields(kind)), kind, int(kind != 0))

    def test_resigned_bad_inputs_do_not_hide_behind_signature_failure(self):
        for kind in (0, 1, 2, 4):
            original = fields(kind)
            # Independently sign each changed value so the fixture predicate,
            # rather than invalid ECDSA, must reject it.
            indices = {0: (0, 1, 2, 3, 4, 5), 1: (0, 1, 2, 3, 4, 5, 6, 7),
                       2: tuple(range(9)), 4: tuple(range(9))}[kind]
            for index in indices:
                changed = copy.deepcopy(original)
                changed[index] = [b"\x01"] if isinstance(changed[index], list) else b"\x01"
                if changed[index] == original[index]:
                    changed[index] = b"\x02"
                with self.subTest(kind=kind, field=index), self.assertRaises((ValueError, TypeError)):
                    j.signed_input(helpers.signed_wire(kind, changed), kind, int(kind != 0))

    def test_authority_domain_is_checked_after_valid_resigning(self):
        for index, replacement in ((0, b"\x01"), (1, bytes(20)), (2, b"\x01")):
            changed = fields(4)
            auth = changed[9][0]
            auth[index] = replacement
            auth[-3:] = helpers.sign_fixture(c.keccak256(b"\x05" + c.rlp(auth[:3])), 2)
            with self.subTest(field=index), self.assertRaises(ValueError):
                j.signed_input(helpers.signed_wire(4, changed), 4, 1)

    def test_wrong_key_and_parity_are_not_valid_fixture_inputs(self):
        for kind in (0, 1, 2, 4):
            wire = helpers.signed_wire(kind, fields(kind))
            decoded = j.prior.plain_rlp(c.rlp_list(wire if kind == 0 else wire[1:]))
            for index in (-3, -2, -1):
                changed = copy.deepcopy(decoded)
                changed[index] = num(j.integer(changed[index]) ^ 1)
                corrupted = (bytes([kind]) if kind else b"") + c.rlp(changed)
                with self.subTest(kind=kind, signature=index), self.assertRaises(ValueError):
                    j.signed_input(corrupted, kind, int(kind != 0))

    def test_historical_width_body_and_reported_chain_are_authenticated(self):
        row = helpers.block_vector()
        header, txs, digest = j.raw_block(row["raw_block"], 16)
        self.assertEqual(digest, row["hash"])
        j.block(row, 16, 1, row["parent"])
        for field, value in (("hash", "0x" + "12" * 32), ("root", "0x" + "12" * 32),
                             ("parent", "0x" + "12" * 32), ("number", 2),
                             ("transaction_proofs", [])):
            changed = copy.deepcopy(row)
            changed[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                j.block(changed, 16, 1, row["parent"])
        with self.assertRaises(ValueError):
            j.raw_block(row["raw_block"], 15)
        malformed = c.rlp([header, [], []]).hex()
        with self.assertRaises(ValueError):
            j.raw_block(malformed, 16)

    def test_incomplete_reports_never_gain_acceptance(self):
        for report in ({}, {"native_source": j.prior.NATIVE, "scope": "private-preactivation",
                            "public_profile_credit": False, "cases": []}):
            self.assertEqual(j.judge(report)["result"], "FAIL")


if __name__ == "__main__":
    unittest.main()
