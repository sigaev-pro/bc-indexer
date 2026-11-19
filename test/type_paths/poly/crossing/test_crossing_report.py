"""Native positive plus correlated corruptions; no new native execution."""

import base64
import copy
import gzip
import hashlib
import importlib.util
import json
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("crossing_report_judge", HERE / "crossing_judge.py")
j = importlib.util.module_from_spec(spec)
spec.loader.exec_module(j)
spec = importlib.util.spec_from_file_location("crossing_report_vectors", HERE.parent / "test_poly_judge.py")
vectors = importlib.util.module_from_spec(spec)
spec.loader.exec_module(vectors)


def edit_header(row, index, value):
    body = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(row["raw_block"])))
    body[0][index] = value
    row.update(raw_block=j.codec.rlp(body).hex(), hash="0x" + j.codec.keccak256(j.codec.rlp(body[0])).hex(),
               parent="0x" + body[0][0].hex(), root="0x" + body[0][3].hex(), number=j.integer(body[0][8]))


def ordered_pair(first, second):
    # RLP indexes 0 and 1 are 0x80 and 0x01: distinct first nibbles.
    leaves = [j.codec.rlp([b"\x30", first]), j.codec.rlp([b"\x31", second])]
    branch = [b""] * 17
    branch[8], branch[0] = [j.codec.keccak256(node) for node in leaves]
    node = j.codec.rlp(branch)
    root = j.codec.keccak256(node)
    proofs = [{"index": index, "key": key, "value": wire.hex(), "nodes": [node.hex(), leaves[index].hex()]}
              for index, (key, wire) in enumerate((("80", first), ("01", second)))]
    if root != j.codec.ordered_root([first, second]):
        raise ValueError("two-leaf fixture root differs")
    return root, proofs


def relink(case):
    first, second = case["crossing"]["blocks"]
    negative = case["before_activation"]
    genesis = j.codec.rlp_list(bytes.fromhex(case["genesis_raw"]))[0][1]
    genesis_hash = "0x" + j.codec.keccak256(genesis).hex()
    edit_header(first, 0, bytes.fromhex(genesis_hash[2:]))
    edit_header(second, 0, bytes.fromhex(first["hash"][2:]))
    edit_header(negative["block"], 0, bytes.fromhex(genesis_hash[2:]))
    negative["head_before"] = negative["head_hash"] = genesis_hash
    case["crossing"]["head_hash"] = second["hash"]
    for sequence, row in enumerate((first, second)):
        header = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(row["raw_block"])))[0]
        for receipt in row["receipts"]:
            receipt.update(blockHash=row["hash"], blockNumber=hex(row["number"]))
            for log in receipt["logs"]:
                log.update(blockHash=row["hash"], blockNumber=hex(row["number"]), blockTimestamp=hex(j.integer(header[11])))
        records = case["crossing"]["decisions"]
        prefix = f"extractor-decision-v1/bor.run-1/{sequence:016x}"
        intent, decided = json.loads(records[prefix + "/intent"]), json.loads(records[prefix + "/decided"])
        intent["old_head"] = {"number": row["number"] - 1, "hash": row["parent"][2:]}
        intent["new_head"] = {"number": row["number"], "hash": row["hash"][2:]}
        intent["members"][0].update(block=row["hash"][2:], child_root=row["root"][2:])
        intent["digest"] = j.prior.intent_digest(intent)
        decided["intent_digest"] = intent["digest"]
        decided["digest"] = j.prior.decided_digest(intent["digest"], "BATCH")
        records[prefix + "/intent"], records[prefix + "/decided"] = json.dumps(intent), json.dumps(decided)


class CrossingNativeReport(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        vector = json.loads((HERE / "native_vector.json").read_text())
        raw = gzip.decompress(base64.b64decode(vector["data"]))
        digest = hashlib.sha256(raw).hexdigest()
        if digest != "d859fe7374dc5812455cec772f9f618a2926b16d84dcfe23a51480cc5f8bf3f6" or digest != vector["sha256"]:
            raise ValueError("crossing native vector identity differs")
        cls.native = json.loads(raw)

    def reject(self, change):
        value = copy.deepcopy(self.native)
        change(value)
        self.assertEqual(j.judge(value)["result"], "FAIL")

    def test_actual_private_crossings(self):
        self.assertEqual(j.judge(self.native), {"result": "PASS", "private_activation_crossings": [1, 2, 4], "reserved127": "NOT_EVALUATED", "public_profile_credit": False})

    def test_identity_and_matrix(self):
        for field, value in (("native_source", "00" * 20), ("scope", "public"), ("public_profile_credit", True), ("error", "failed"), ("cases", [])):
            self.reject(lambda r: r.update({field: value}))
        for field, value in (("name", "other"), ("type", 2), ("type", True), ("activation_block", 3), ("activation_block", 2.0)):
            self.reject(lambda r: r["cases"][0].update({field: value}))

    def test_configuration_gate_and_numeric_domains(self):
        for index, (_, gate, _, _, _) in enumerate(j.CASES):
            for value in (0, 8, 2.0, True):
                self.reject(lambda r: r["cases"][index]["config"].update({gate: value}))
        self.reject(lambda r: r["cases"][0]["config"].update(chainId=1))
        self.reject(lambda r: r["cases"][0]["config"].update(homesteadBlock=False))
        self.reject(lambda r: r["cases"][0]["config"]["bor"].update(jaipurBlock=8.0))

    def test_numeric_durable_domains_and_empty_initial_namespace(self):
        for view in ("before_activation", "crossing"):
            for value in (True, 1.0, 3.0):
                self.reject(lambda r: r["cases"][0][view]["admission"].update({"extractor-admission-v1/bor.run-1/counter": json.dumps({"next": value})}))
        for value in (2.0, True):
            self.reject(lambda r: r["cases"][0]["crossing"]["decisions"].update({"extractor-decision-v1/bor.run-1/counter": json.dumps({"next": value})}))
        for value in (None, [], {"extractor-decision-v1/bor.run-1/counter": '{"next":0}'}):
            self.reject(lambda r: r["cases"][0]["before_activation"].update(decisions=value))
        for observed in ([True], [1.0], [1, True, 2], [1, 1, 2.0]):
            view = "before_activation" if len(observed) == 1 else "crossing"
            self.reject(lambda r: r["cases"][0][view].update(observed=observed))

    def test_boundaries_bind_error_head_admission_and_observer(self):
        for view in ("before_activation", "crossing"):
            for field, value in (("head_hash", "0x" + "00" * 32), ("observer_errors", []), ("keys", {}), ("observed", []), ("admission", {})):
                self.reject(lambda r: r["cases"][0][view].update({field: value}))
        for field, value in (("accepted", True), ("error", "transaction type not supported"), ("head_before", "0x" + "00" * 32)):
            self.reject(lambda r: r["cases"][0]["before_activation"].update({field: value}))
        self.reject(lambda r: r["cases"][0]["before_activation"]["block"].update(receipts=[]))

    def test_receipt_metadata_and_malformed_proofs(self):
        for field, value in (("type", "0x7f"), ("status", "0x0"), ("gasUsed", "0x1"), ("cumulativeGasUsed", "0x1"),
                             ("effectiveGasPrice", "0x1"), ("logs", []), ("logsBloom", "00" * 256),
                             ("transactionHash", "0x" + "00" * 32), ("blockHash", "0x" + "00" * 32), ("blockNumber", "0x1"), ("transactionIndex", "0x1")):
            self.reject(lambda r: r["cases"][0]["crossing"]["blocks"][1]["receipts"][0].update({field: value}))
        for field in ("account_proofs", "state_after", "receipts", "receipt_proofs", "transaction_proofs"):
            for value in (None, False, [], {}):
                self.reject(lambda r: r["cases"][0]["crossing"]["blocks"][0].update({field: value}))
        for field, value in (("removed", True), ("blockTimestamp", "0x1"), ("logIndex", "0x8"), ("transactionIndex", "0x1"),
                             ("blockNumber", "0x1"), ("blockHash", "0x" + "00" * 32), ("transactionHash", "0x" + "00" * 32)):
            self.reject(lambda r: r["cases"][0]["crossing"]["blocks"][1]["receipts"][0]["logs"][0].update({field: value}))

    def test_block_components_and_proof_fields_directly(self):
        row = copy.deepcopy(self.native["cases"][0]["crossing"]["blocks"][0])
        for field, value in (("number", 2), ("number", True), ("hash", "0x" + "ab" * 32), ("parent", "0x" + "ab" * 32), ("root", "0x" + "ab" * 32), ("transaction_proofs", [])):
            changed = copy.deepcopy(row)
            changed[field] = value
            with self.subTest(field=field), self.assertRaises((ValueError, TypeError, KeyError)):
                j.h.block(changed, 15, 1, row["parent"])
        for field, value in (("index", True), ("index", 1), ("key", "01"), ("value", "01"), ("nodes", [])):
            changed = copy.deepcopy(row)
            changed["transaction_proofs"][0][field] = value
            with self.subTest(proof=field), self.assertRaises((ValueError, TypeError)):
                j.h.block(changed, 15, 1, row["parent"])

    def test_legacy_absolute_state_directly(self):
        row = self.native["cases"][0]["crossing"]["blocks"][0]
        state = j.prior.snapshot(row["state_after"], False)
        after = 10**19 - 21000 * 5_000_000_000 - 7
        j.legacy_state(state, after)
        for address in j.prior.WATCHED:
            for field, value in (("balance", 123), ("nonce", 9), ("code", b"bad"), ("storage", (1, 0, 0, 0))):
                changed = copy.deepcopy(state)
                changed[address][field] = value
                with self.subTest(address=address, field=field), self.assertRaises(ValueError):
                    j.legacy_state(changed, after)

    def test_correlated_header_transition_edits(self):
        for index, field, value in ((1, 9, 10_000_000), (1, 15, 999999999), (2, 15, 1), (0, 11, 1)):
            def change(r):
                row = r["cases"][index]
                edit_header(row["crossing"]["blocks"][1], field, vectors.num(value))
                relink(row)
            self.reject(change)

    def test_genesis_number_parent_and_body_are_independent(self):
        for field in ("number", "parent", "transactions"):
            def change(report):
                case = report["cases"][0]
                body = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(case["genesis_raw"])))
                if field == "number":
                    body[0][8] = b"\x01"
                elif field == "parent":
                    body[0][0] = bytes([1]) * 32
                else:
                    wire = bytes.fromhex(case["crossing"]["blocks"][0]["transaction_proofs"][0]["value"])
                    body[1] = [j.prior.plain_rlp(j.codec.rlp_list(wire))]
                    body[0][4] = j.codec.ordered_root([wire])
                case["genesis_raw"] = j.codec.rlp(body).hex()
                relink(case)
            with self.subTest(field=field):
                self.reject(change)

    def test_correlated_initial_gas_limit(self):
        def change(report):
            case = report["cases"][0]
            body = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(case["genesis_raw"])))
            body[0][9] = vectors.num(9_000_000)
            case["genesis_raw"] = j.codec.rlp(body).hex()
            edit_header(case["crossing"]["blocks"][0], 9, vectors.num(9_000_000))
            edit_header(case["before_activation"]["block"], 9, vectors.num(9_000_000))
            relink(case)
        self.reject(change)
        for index in range(3):
            def change(r):
                row = r["cases"][index]
                edit_header(row["before_activation"]["block"], 9, vectors.num(1))
                relink(row)
            self.reject(change)

    def test_first_timestamp_and_base_fee_correlated_with_candidate(self):
        for case_index, field, value in ((0, 11, 1), (2, 15, 1)):
            def change(report):
                case = report["cases"][case_index]
                first, second = case["crossing"]["blocks"]
                if field == 11:
                    header = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(first["raw_block"])))[0]
                    chosen = j.integer(header[11]) + 1
                    edit_header(second, 11, vectors.num(chosen + 1))
                else:
                    chosen = value
                edit_header(first, field, vectors.num(chosen))
                edit_header(case["before_activation"]["block"], field, vectors.num(chosen))
                relink(case)
            self.reject(change)

    def test_extra_authenticated_transaction_is_not_ignored(self):
        for negative in (False, True):
            def change(report):
                case = report["cases"][0]
                row = case["before_activation"]["block"] if negative else case["crossing"]["blocks"][0]
                wire = bytes.fromhex(row["transaction_proofs"][0]["value"])
                root, proofs = ordered_pair(wire, wire)
                body = j.prior.plain_rlp(j.codec.rlp_list(bytes.fromhex(row["raw_block"])))
                body[1].append(copy.deepcopy(body[1][0]))
                row["raw_block"] = j.codec.rlp(body).hex()
                row["transaction_proofs"] = proofs
                edit_header(row, 4, root)
                relink(case)
            with self.subTest(negative=negative):
                self.reject(change)


if __name__ == "__main__":
    unittest.main()
