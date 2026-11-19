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

    def test_envelope_shape_and_authority_signature_have_direct_controls(self):
        typed = helpers.signed_wire(1, fields(1))
        with self.assertRaises(ValueError):
            j.signed_input(b"\x02" + typed[1:], 1, 1)
        extra = fields(4)
        extra.insert(-3, b"\x01")
        with self.assertRaises(ValueError):
            j.signed_input(helpers.signed_wire(4, extra), 4, 1)
        duplicate = fields(4)
        duplicate[9] += copy.deepcopy(duplicate[9])
        with self.assertRaises(ValueError):
            j.signed_input(helpers.signed_wire(4, duplicate), 4, 1)
        invalid_auth = fields(4)
        invalid_auth[9][0][-2] = num(j.integer(invalid_auth[9][0][-2]) + 1)
        with self.assertRaises(ValueError):
            j.signed_input(helpers.signed_wire(4, invalid_auth), 4, 1)

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
        with self.assertRaises(ValueError):
            j.raw_block(c.rlp([header, [txs[0]], [], []]).hex(), 16)
        short = copy.deepcopy(header)
        short[1] = bytes(31)
        with self.assertRaises(ValueError):
            j.raw_block(c.rlp([short, [txs[0]], []]).hex(), 16)
        for field, value in (("index", 1), ("key", "01"), ("value", "01"), ("nodes", [])):
            changed = copy.deepcopy(row)
            changed["transaction_proofs"][0][field] = value
            with self.subTest(proof=field), self.assertRaises(ValueError):
                j.block(changed, 16, 1, row["parent"])

    def test_incomplete_reports_never_gain_acceptance(self):
        for report in ({}, {"native_source": j.prior.NATIVE, "scope": "private-preactivation",
                            "public_profile_credit": False, "cases": []}):
            self.assertEqual(j.judge(report)["result"], "FAIL")

    def test_receipt_consensus_and_metadata_are_checked_separately(self):
        helpers.PolygonJudgeProofTests.setUpClass()
        row = copy.deepcopy(helpers.PolygonJudgeProofTests.native["blocks"][-1])
        header, _, _ = j.raw_block(row["raw_block"], 16)
        accounts, root = helpers.account_vector()
        sender_proof = accounts["account_proofs"][j.prior.SENDER]
        for address in j.prior.WATCHED - {j.prior.SENDER}:
            accounts["account_proofs"][address] = {
                "address": address, "nonce": "0x0", "balance": "0x0", "codeHash": "0x" + "00" * 32,
                "storageHash": "0x" + "00" * 32, "accountProof": sender_proof["accountProof"],
                "storageProof": [{"key": hex(i), "value": "0x0", "proof": []} for i in range(4)]}
            accounts["state_after"][address[2:]] = {"nonce": 0, "balance": "0", "code": "", "storage": {str(i): "00" * 32 for i in range(4)}}
        row.update(accounts)
        header[3] = root
        tx = helpers.signed_wire(0, fields(0))
        wire, expected_logs, bloom = j.transfer_receipt(j.integer(header[15]))
        key = c.rlp(b"")
        node = helpers.leaf(key, wire)
        header[5], header[6], header[10] = c.keccak256(node), bloom, num(21000)
        receipt = {"type": "0x0", "status": "0x1", "gasUsed": hex(21000), "cumulativeGasUsed": hex(21000),
                   "logs": [{"address": "0x" + address.hex(), "topics": ["0x" + topic.hex() for topic in topics], "data": "0x" + data.hex()} for address, topics, data in expected_logs], "logsBloom": "0x" + bloom.hex(), "transactionHash": "0x" + c.keccak256(tx).hex(),
                   "blockHash": row["hash"], "blockNumber": "0x1", "transactionIndex": "0x0"}
        row["receipts"] = [receipt]
        row["receipt_proofs"] = [{"index": 0, "key": key.hex(), "value": wire.hex(), "nodes": [node.hex()]}]
        j.stored(row, header, [tx], True)
        for field, value in (("status", "0x0"), ("gasUsed", "0x1"), ("cumulativeGasUsed", "0x1"),
                             ("type", "0x1"), ("logsBloom", "0x" + "01" * 256), ("logs", []),
                             ("transactionHash", "0x" + "01" * 32), ("blockHash", "0x" + "01" * 32),
                             ("blockNumber", "0x2"), ("transactionIndex", "0x1")):
            changed = copy.deepcopy(row)
            changed["receipts"][0][field] = value
            with self.subTest(receipt=field), self.assertRaises(ValueError):
                j.stored(changed, header, [tx], True)
        for index in (5, 6, 10):
            changed = copy.deepcopy(header)
            changed[index] = bytes(len(changed[index])) if index != 6 else bytes([1]) * 256
            with self.subTest(header=index), self.assertRaises(ValueError):
                j.stored(row, changed, [tx], True)
        changed = copy.deepcopy(row)
        changed["receipts"].append(copy.deepcopy(receipt))
        with self.assertRaises(ValueError):
            j.stored(changed, header, [tx], True)
        changed = copy.deepcopy(row)
        changed["account_proofs"][j.prior.SENDER]["accountProof"] = []
        with self.assertRaises(ValueError):
            j.stored(changed, header, [tx], True)
        # A correctly re-committed failed receipt must still fail the expected
        # successful transfer oracle, independently of the proof/root check.
        failed = c.rlp([b"", num(21000), bloom, expected_logs])
        changed, failed_header = copy.deepcopy(row), copy.deepcopy(header)
        failed_node = helpers.leaf(key, failed)
        failed_header[5] = c.keccak256(failed_node)
        changed["receipt_proofs"][0].update(value=failed.hex(), nodes=[failed_node.hex()])
        changed["receipts"][0]["status"] = "0x0"
        with self.assertRaises(ValueError):
            j.stored(changed, failed_header, [tx], True)


if __name__ == "__main__":
    unittest.main()
