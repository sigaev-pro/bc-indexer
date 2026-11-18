"""Synthetic controls of the ETH judge; these are not native runtime evidence."""

import copy
import json
from pathlib import Path
import unittest

import eth_type_judge as judge


def rlp(value):
    if isinstance(value, list):
        data, base = b"".join(rlp(item) for item in value), 0xc0
    else:
        data, base = value, 0x80
        if len(data) == 1 and data[0] < 0x80:
            return data
    if len(data) < 56:
        return bytes([base + len(data)]) + data
    size = len(data).to_bytes((len(data).bit_length() + 7) // 8, "big")
    return bytes([base + 55 + len(size)]) + size + data


def imported_case(block_count=2):
    records = {}
    producer = "type-paths-eth.r0123456789abcdef"
    prefix = "extractor-decision-v1/" + producer + "/"
    views = []
    parent = "00" * 32
    for i in range(block_count):
        root = f"{i + 100:064x}"
        header = [b""] * 15
        header[0], header[3] = bytes.fromhex(parent), bytes.fromhex(root)
        header[8], header[11] = bytes([i + 1]), bytes([(i + 1) * 10])
        block_hash = judge.keccak256(rlp(header)).hex()
        intent = {"native_store_id": "e0105e71000000000000000000000001", "producer": producer,
                  "sequence": i, "kind": "IMPORT", "old_head": {"number": i, "hash": parent},
                  "new_head": {"number": i + 1, "hash": block_hash}, "members": [{
                      "observation": producer + f".{i:016x}", "block": block_hash,
                      "exec_ready_digest": "a" * 64, "child_root": root}], "evidence_digest": "b" * 64,
                  "events": True}
        intent["digest"] = judge.intent_digest(intent)
        records[prefix + f"{i:016x}/intent"] = json.dumps(intent)
        records[prefix + f"{i:016x}/decided"] = json.dumps({"sequence": i, "intent_digest": intent["digest"],
            "delivery": "BATCH", "digest": judge.decided_digest(intent["digest"], "BATCH")})
        views.append({"number": i + 1, "hash": "0x" + block_hash, "root": "0x" + root, "header_root": "0x" + root,
                      "body_equal": True, "root_equal": True, "gas_equal": True,
                      "replay_error": None, "raw_block": rlp([header, [], []]).hex(), "receipt_count": 0, "receipts": []})
        parent = block_hash
    records[prefix + "counter"] = json.dumps({"next": block_count})
    prefix = "extractor-admission-v1/" + producer + "/"
    return {"attach_error": None, "import_error": None, "head": block_count, "head_hash": "0x" + parent, "hooks_closed": True,
            "replay_transactions_equal": True, "replay_system_calls_equal": True,
            "admission_records": {prefix + "counter": json.dumps({"next": block_count}),
                                  prefix + "lowwater": json.dumps({"sequence": block_count}),
                                  prefix + "purged": json.dumps({"sequence": block_count})}, "decision_records": records,
            "transactions": [], "system_calls": [],
            "blocks": views}


def valid_observation():
    """An explicitly synthetic positive control using oracle inputs and arbitrary gas."""
    main = imported_case()
    state = {address: {"balance": "0", "nonce": 0, "code": "", "slot0": judge.ZERO}
             for address in (judge.SENDER, judge.AUTHORITY, judge.SUCCESS, judge.REVERT, judge.DELEGATE)}
    state[judge.SENDER]["balance"] = str(100 * 10**18)
    for index in range(10):
        kind, block = index % 5, index // 5 + 1
        status = int(block == 1)
        target = judge.SUCCESS if status else judge.REVERT
        before = copy.deepcopy(state)
        state[judge.SENDER]["nonce"] = index + 1
        blob_gas = 131072 if kind == 3 else 0
        gas_used, gas_price = 50000, 2_000_000_000
        state[judge.SENDER]["balance"] = str(int(state[judge.SENDER]["balance"]) - gas_used * gas_price - blob_gas - status)
        state[target]["balance"] = str(int(state[target]["balance"]) + status)
        if kind == 4:
            state[judge.AUTHORITY]["nonce"] = block
            state[judge.AUTHORITY]["code"] = "ef0100" + judge.DELEGATE[2:]
        wire = f"{kind:02x}c0" if kind else "c0"
        fields = {"type": hex(kind), "nonce": hex(index), "chainId": hex(31337), "gas": hex(150000),
                  "value": "0x1", "input": "0x", "to": target}
        if kind <= 1:
            fields["gasPrice"] = hex(2_000_000_000)
        else:
            fields.update(maxFeePerGas=hex(2_000_000_000), maxPriorityFeePerGas=hex(1_000_000_000))
        if kind:
            fields["accessList"] = [{"address": target, "storageKeys": [judge.ZERO, judge.ZERO]},
                                    {"address": target, "storageKeys": []}]
        if kind == 3:
            fields.update(blobVersionedHashes=["0x01" + "00" * 31], maxFeePerBlobGas=hex(1_000_000_000))
        if kind == 4:
            fields["authorizationList"] = [{"chainId": hex(31337), "nonce": hex(block - 1), "address": judge.DELEGATE}]
        row = {"block": block, "type": kind, "nonce": index, "from": judge.SENDER, "status": status,
               "execution_error": None, "fields": fields, "before": before, "after": copy.deepcopy(state),
               "gas_used": gas_used, "wire": wire}
        receipt = {"index": kind, "type": kind, "status": status, "gas_used": gas_used,
                   "effective_gas_price": str(gas_price), "wire": "c0", "transaction_wire": wire,
                   "receipt_equal": True, "blob_gas_used": blob_gas}
        if blob_gas:
            receipt["blob_gas_price"] = "1"
        main["transactions"].append(row)
        main["blocks"][block - 1]["receipts"].append(receipt)
        main["blocks"][block - 1]["receipt_count"] += 1
    main["system_calls"] = [{"block": block, "from": judge.SYSTEM, "to": target}
                            for block in (1, 2) for target in sorted(judge.SYSTEM_TARGETS)]
    forks = []
    for kind in range(1, 5):
        accepted = imported_case()
        accepted["transactions"] = [{"type": kind, "block": 2, "status": 1}]
        forks.append({"type": kind, "before_number": 1, "before_time": 10, "at_number": 2, "at_time": 20,
                      "signer_error": "transaction type not supported", "signer_unsupported": True,
                      "accepted": accepted,
                      "rejected": {"attach_error": None, "head": 0,
                                   "import_error": "data blobs present in block body" if kind == 3 else
                                   "could not apply tx: transaction type not supported"}})
    return {"kind": "native-type-paths-eth", "schema_version": 1, "native_commit": judge.NATIVE,
            "chain_id": 31337, "private_profile": "private-type-paths-osaka-v1", "main": main,
            "forks": forks, "unknown": {"wire": "7ec0", "unsupported": True, "error": "transaction type not supported"}}


class JudgeTests(unittest.TestCase):
    def setUp(self):
        self.observation = valid_observation()

    def failed(self, part):
        result = judge.judge(self.observation)
        self.assertEqual(result["result"], "FAIL", result)
        self.assertTrue(any(part in error for error in result["errors"]), result)
        self.assertFalse(result["public_profile_credit"])

    def test_synthetic_positive_is_private_only(self):
        result = judge.judge(self.observation)
        self.assertEqual(result["result"], "PASS", result)
        self.assertFalse(result["public_profile_credit"])

    def test_missing_observation_is_failure(self):
        self.observation = {}
        self.failed("Malformed")

    def test_wrong_native_and_private_identity(self):
        for key, value in (("native_commit", "0" * 40), ("chain_id", 1), ("private_profile", "ethereum-mainnet")):
            self.observation = valid_observation()
            self.observation[key] = value
            self.failed("wrong")

    def test_import_and_attachment_errors(self):
        for field in ("attach_error", "import_error"):
            self.observation = valid_observation()
            self.observation["main"][field] = "native failure"
            self.failed("failed or absent")

    def test_missing_null_error_is_failure(self):
        for field in ("attach_error", "import_error"):
            self.observation = valid_observation()
            del self.observation["main"][field]
            self.failed("failed or absent")

    def test_native_integrity_flags_must_be_true(self):
        for field in ("hooks_closed", "replay_transactions_equal", "replay_system_calls_equal"):
            self.observation = valid_observation()
            self.observation["main"][field] = None
            self.failed("main:")
        for field in ("body_equal", "root_equal", "gas_equal"):
            self.observation = valid_observation()
            self.observation["main"]["blocks"][0][field] = False
            self.failed(field)

    def test_missing_native_block_is_failure(self):
        self.observation["main"]["blocks"].pop()
        self.failed("block observations")

    def test_missing_type_is_failure(self):
        self.observation["main"]["transactions"].pop()
        self.failed("ten transaction")

    def test_success_cannot_replace_failed_execution(self):
        self.observation["main"]["transactions"][5]["status"] = 1
        self.failed("wrong sender/status")

    def test_duplicate_type_is_failure(self):
        self.observation["main"]["transactions"][1]["type"] = 0
        self.failed("wrong block/type/nonce")

    def test_source_fields_must_match_fixture(self):
        for field, value in (("gas", "0x1"), ("value", "0x0"), ("input", "0x01"), ("chainId", "0x1")):
            self.observation = valid_observation()
            self.observation["main"]["transactions"][0]["fields"][field] = value
            self.failed("source")

    def test_source_access_list_duplicates_preserved(self):
        self.observation["main"]["transactions"][1]["fields"]["accessList"][0]["storageKeys"].pop()
        self.failed("ordered access list")

    def test_blob_source_and_fee_boundaries(self):
        self.observation["main"]["transactions"][3]["fields"]["blobVersionedHashes"] = []
        self.failed("blob source")
        self.observation = valid_observation()
        self.observation["main"]["blocks"][0]["receipts"][3]["blob_gas_used"] = 0
        self.failed("fee/value accounting")

    def test_sender_nonce_boundary(self):
        self.observation["main"]["transactions"][5]["after"][judge.SENDER]["nonce"] -= 1
        self.failed("sender nonce")

    def test_failed_transaction_must_pay_fee(self):
        row = self.observation["main"]["transactions"][5]
        row["after"][judge.SENDER]["balance"] = row["before"][judge.SENDER]["balance"]
        self.failed("fee/value accounting")

    def test_revert_must_not_transfer_value(self):
        self.observation["main"]["transactions"][5]["after"][judge.REVERT]["balance"] = "1"
        self.failed("target value")

    def test_revert_must_not_keep_storage(self):
        self.observation["main"]["transactions"][5]["after"][judge.REVERT]["slot0"] = "0x" + "00" * 31 + "01"
        self.failed("reverted storage")

    def test_setcode_authorization_survives_evm_revert(self):
        self.observation["main"]["transactions"][9]["after"][judge.AUTHORITY]["nonce"] = 1
        self.failed("authorization state")

    def test_setcode_source_tuple_is_checked(self):
        self.observation["main"]["transactions"][9]["fields"]["authorizationList"][0]["nonce"] = "0x0"
        self.failed("authorization source")

    def test_source_bytes_preserved(self):
        self.observation["main"]["transactions"][0]["wire"] = "deadbeef"
        self.failed("source bytes")

    def test_receipt_reexecution_must_match(self):
        self.observation["main"]["blocks"][0]["receipts"][0]["receipt_equal"] = False
        self.failed("differing receipt")

    def test_missing_decision_is_failure(self):
        records = self.observation["main"]["decision_records"]
        del records[next(k for k in records if k.endswith("/decided"))]
        self.failed("unresolved capture intent")

    def test_each_fee_field_is_independent(self):
        for index, field, error in ((0, "gasPrice", "gas price"), (2, "maxFeePerGas", "fee caps"),
                                    (2, "maxPriorityFeePerGas", "fee caps"), (3, "maxFeePerBlobGas", "blob source")):
            self.observation = valid_observation()
            self.observation["main"]["transactions"][index]["fields"][field] = "0x0"
            self.failed(error)

    def test_native_digest_vectors(self):
        root = Path(__file__).resolve().parents[3]
        vectors = json.loads((root / "adapters/geth/decision/vectors.json").read_text())
        for name in ("import_marker", "finality_marker"):
            record = json.loads(vectors[name])
            self.assertEqual(judge.intent_digest(record), record["digest"])
            kind = name.split("_")[0]
            self.assertEqual(judge.decided_digest(record["digest"], "BATCH" if kind == "import" else "CHECKPOINT"),
                             vectors[kind + "_decided_digest"])

    def test_accepted_independent_codec_identity_and_vectors(self):
        root = Path(__file__).resolve().parents[3]
        source = (root / "test/harness/source_inputs_geth.py").read_text()
        local = Path(judge.__file__).read_text()
        self.assertEqual(source.split("KECCAK_ROUNDS =", 1)[1].split("\ndef capsule_digest(", 1)[0].strip(),
                         local.split("KECCAK_ROUNDS =", 1)[1].split("\ndef quantity(", 1)[0].strip())
        for data, expected in ((b"", "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"),
                               (b"abc", "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45"),
                               (b"a" * 200, "96ea54061def936c4be90b518992fdc6f12f535068a256229aca54267b4d084d"),
                               (b"\x5a" * 136, "ddc757d2caa82320e140f35833c18e8cc3b230b2b9a48def3d98461ffae81716")):
            self.assertEqual(judge.keccak256(data).hex(), expected)

    def rebind_records(self):
        case = self.observation["main"]
        records = case["decision_records"]
        for key in list(records):
            if not key.endswith("/intent"):
                continue
            record = json.loads(records[key])
            block = case["blocks"][record["sequence"]]
            record["old_head"] = {"number": block["number"] - 1, "hash": judge.parent_hash(block["raw_block"])}
            record["new_head"] = {"number": block["number"], "hash": block["hash"][2:]}
            record["members"][0].update(block=block["hash"][2:], child_root=block["root"][2:])
            record["digest"] = judge.intent_digest(record)
            records[key] = json.dumps(record)
            decided_key = key[:-6] + "decided"
            decided = json.loads(records[decided_key])
            decided.update(intent_digest=record["digest"], digest=judge.decided_digest(record["digest"], "BATCH"))
            records[decided_key] = json.dumps(decided)
        case["head_hash"] = case["blocks"][-1]["hash"]

    def test_correlated_view_hash_cannot_replace_native_header(self):
        self.observation["main"]["blocks"][0]["hash"] = "0x" + "ab" * 32
        self.rebind_records()
        self.failed("block hash differs from raw native header")

    def test_correlated_view_root_cannot_replace_native_header(self):
        block = self.observation["main"]["blocks"][0]
        block["root"] = block["header_root"] = "0x" + "ab" * 32
        self.rebind_records()
        self.failed("state root differs from raw native header")
        for field in ("root", "header_root"):
            self.observation = valid_observation()
            self.observation["main"]["blocks"][0][field] = "0x" + "ab" * 32
            self.failed("state root differs from raw native header")

    def change_header(self, index, position, value):
        block = self.observation["main"]["blocks"][index]
        header = [item for item, _ in judge.rlp_list(bytes.fromhex(block["raw_block"]))[0][0]]
        header[position] = value
        block["raw_block"] = rlp([header, [], []]).hex()
        block["hash"] = "0x" + judge.keccak256(rlp(header)).hex()
        self.rebind_records()

    def test_correlated_native_headers_must_form_one_chain(self):
        self.change_header(1, 0, b"\xab" * 32)
        self.failed("canonical chain is discontinuous")

    def test_native_final_head_binds_last_raw_header(self):
        self.observation["main"]["head_hash"] = "0x" + "ab" * 32
        self.failed("final head differs")

    def test_header_number_and_time_are_independently_checked(self):
        self.change_header(1, 8, b"\x03")
        self.failed("block number differs from raw native header")
        self.observation = valid_observation()
        self.change_header(1, 11, b"\x15")
        self.failed("timestamp differs")

    def test_raw_header_shapes_widths_and_lengths(self):
        header = [item for item, _ in judge.rlp_list(bytes.fromhex(self.observation["main"]["blocks"][0]["raw_block"]))[0][0]]
        cases = [(rlp([header, []]), "field count"), (rlp([header, [], [], [], []]), "field count"),
                 (rlp([header + header, [], []]), "header field count"),
                 (rlp([header + [[b""]], [], []]) + b"\x00", "not exactly one"),
                 (rlp([header[:14], [], []]), "not a header")]
        for position, value in ((0, b"x" * 31), (3, b"x" * 33), (8, b"\0\x01"), (11, b"x" * 9)):
            changed = list(header)
            changed[position] = value
            cases.append((rlp([changed, [], []]), "width" if position in (0, 3) else "integer"))
        for raw, message in cases:
            with self.assertRaisesRegex(ValueError, message):
                judge.native_header(raw.hex())
        for raw in (b"\x81\x01", b"\xb8\x01a", b"\xb9\x00\x40" + b"a" * 64, b"\x83ab", b"\xc2\x83a",
                    b"\xc4\xc1\x82ab"):
            with self.assertRaises((ValueError, IndexError)):
                judge.rlp_list(raw)

    def test_native_record_duplicate_fields_are_rejected(self):
        records = self.observation["main"]["decision_records"]
        key = next(k for k in records if k.endswith("/counter"))
        records[key] = '{"next":2,"next":2}'
        self.failed("Duplicate native record field")

    def test_malformed_native_digest_and_integer_encodings(self):
        for value in (None, "ABC", "a" * 63, "A" * 64):
            with self.assertRaises(ValueError):
                judge.digest_bytes(value)
        for value in (True, -1, 2**64, "0"):
            with self.assertRaises(ValueError):
                judge.uint64(value)
        for value in ("80e1a0" + "00" * 32, "e2e1a1" + "00" * 32, "e2e1a0" + "00" * 31):
            with self.assertRaises(ValueError):
                judge.parent_hash(value)

    def test_malformed_and_unrelated_native_records(self):
        for group in ("decision_records", "admission_records"):
            self.observation = valid_observation()
            records = self.observation["main"][group]
            for key in records:
                records[key] = "not JSON"
            self.failed("Malformed")
        self.observation = valid_observation()
        self.observation["main"]["admission_records"] = {"arbitrary": '{}'}
        self.failed("admission frontier")

    def test_native_records_bind_every_identity_and_digest(self):
        edits = [("intent", "native_store_id", "other"), ("intent", "producer", "other"),
                 ("intent", "sequence", 9), ("intent", "kind", "FINALITY"), ("intent", "events", False),
                 ("intent", "old_head", {"number": 0, "hash": "f" * 64}),
                 ("intent", "new_head", {"number": 1, "hash": "f" * 64}),
                 ("intent", "digest", "f" * 64), ("intent", "evidence_digest", "f" * 64),
                 ("decided", "sequence", 9), ("decided", "intent_digest", "f" * 64),
                 ("decided", "delivery", "EMPTY"), ("decided", "digest", "f" * 64),
                 ("intent", "extra", True), ("decided", "extra", True)]
        for suffix, field, value in edits:
            with self.subTest(suffix=suffix, field=field):
                self.observation = valid_observation()
                records = self.observation["main"]["decision_records"]
                key = next(k for k in records if k.endswith("/" + suffix))
                record = json.loads(records[key])
                record[field] = value
                if suffix == "intent" and field not in ("digest", "evidence_digest"):
                    record["digest"] = judge.intent_digest(record)
                    decided_key = key[:-6] + "decided"
                    decided = json.loads(records[decided_key])
                    decided.update(intent_digest=record["digest"], digest=judge.decided_digest(record["digest"], "BATCH"))
                    records[decided_key] = json.dumps(decided)
                records[key] = json.dumps(record)
                self.assertEqual(judge.judge(self.observation)["result"], "FAIL")

    def test_empty_capture_records(self):
        self.observation["main"]["decision_records"] = {}
        self.failed("missing or duplicate capture decisions")

    def test_extra_member_with_valid_digest_is_rejected(self):
        records = self.observation["main"]["decision_records"]
        key = next(k for k in records if k.endswith("/intent"))
        record = json.loads(records[key])
        record["members"].append(copy.deepcopy(record["members"][0]))
        record["digest"] = judge.intent_digest(record)
        records[key] = json.dumps(record)
        decided_key = key[:-6] + "decided"
        decided = json.loads(records[decided_key])
        decided.update(intent_digest=record["digest"], digest=judge.decided_digest(record["digest"], "BATCH"))
        records[decided_key] = json.dumps(decided)
        self.failed("wrong capture members")

    def test_consistently_substituted_producer_is_rejected(self):
        case = self.observation["main"]
        original = "type-paths-eth.r0123456789abcdef"
        case["decision_records"] = {k.replace(original, "other"): v.replace(original, "other")
                                    for k, v in case["decision_records"].items()}
        case["admission_records"] = {k.replace(original, "other"): v
                                     for k, v in case["admission_records"].items()}
        records = case["decision_records"]
        for key in list(records):
            if key.endswith("/intent"):
                record = json.loads(records[key])
                record["digest"] = judge.intent_digest(record)
                records[key] = json.dumps(record)
                decided_key = key[:-6] + "decided"
                decided = json.loads(records[decided_key])
                decided.update(intent_digest=record["digest"], digest=judge.decided_digest(record["digest"], "BATCH"))
                records[decided_key] = json.dumps(decided)
        self.failed("wrong capture producer")

    def test_member_bindings_even_with_recomputed_digests(self):
        for field, value in (("observation", "other.0000000000000000"), ("block", "f" * 64),
                             ("child_root", "f" * 64), ("extra", True)):
            self.observation = valid_observation()
            records = self.observation["main"]["decision_records"]
            key = next(k for k in records if k.endswith("/intent"))
            record = json.loads(records[key])
            record["members"][0][field] = value
            record["digest"] = judge.intent_digest(record)
            records[key] = json.dumps(record)
            decided = json.loads(records[key[:-6] + "decided"])
            decided.update(intent_digest=record["digest"], digest=judge.decided_digest(record["digest"], "BATCH"))
            records[key[:-6] + "decided"] = json.dumps(decided)
            self.assertEqual(judge.judge(self.observation)["result"], "FAIL")

    def test_frontier_and_extra_records(self):
        for group, suffix, value in (("decision_records", "counter", {"next": 3}),
                                    ("admission_records", "counter", {"next": 3}),
                                    ("admission_records", "lowwater", {"sequence": 1}),
                                    ("admission_records", "purged", {"sequence": 1})):
            self.observation = valid_observation()
            records = self.observation["main"][group]
            key = next(k for k in records if k.endswith("/" + suffix))
            records[key] = json.dumps(value)
            self.failed("frontier")

    def test_missing_admission_is_failure(self):
        self.observation["main"]["admission_records"] = {}
        self.failed("no admission")

    def test_each_protocol_system_call_required_once(self):
        for index in range(8):
            self.observation = valid_observation()
            self.observation["main"]["system_calls"].pop(index)
            self.failed("system-call presence")
        self.observation = valid_observation()
        self.observation["main"]["system_calls"].append(self.observation["main"]["system_calls"][0])
        self.failed("system-call presence")

    def test_all_four_fork_controls_required(self):
        self.observation["forks"].pop()
        self.failed("fork controls")

    def test_signer_only_is_not_native_import_proof(self):
        self.observation["forks"][0]["accepted"]["import_error"] = "failed"
        self.failed("native import failed")

    def test_pre_fork_import_cannot_advance_head(self):
        self.observation["forks"][0]["rejected"]["head"] = 1
        self.failed("import rejection/head")

    def test_unrelated_import_error_does_not_prove_fork_rejection(self):
        self.observation["forks"][0]["rejected"]["import_error"] = "database unavailable"
        self.failed("import rejection/head")

    def test_missing_successful_fork_transaction(self):
        self.observation["forks"][0]["accepted"]["transactions"] = []
        self.failed("successful native transaction")

    def test_unknown_decoder_rejection_must_be_native(self):
        self.observation["unknown"]["unsupported"] = False
        self.failed("unknown native envelope")

    def test_accepted_admission_preserves_import_tracer(self):
        root = Path(__file__).resolve().parents[3]
        source = (root / "adapters/geth/admission/native/geth.go").read_text()
        configure = source.split("func (o *observation) Configure(cfg *vm.Config) {", 1)[1].split("\n}", 1)[0]
        self.assertNotIn("Tracer", configure)
        self.assertIn("cfg.ExtractorGetHash = o.getHash", configure)
        self.assertIn("cfg.ExtractorSafePoint = o.a.opts.SafePoint", configure)
        self.assertIn("cfg.DisableParallelExecution = true", configure)


if __name__ == "__main__":
    unittest.main()
