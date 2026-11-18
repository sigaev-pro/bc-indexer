"""Synthetic controls of the ETH judge; these are not native runtime evidence."""

import copy
from pathlib import Path
import unittest

import eth_type_judge as judge


def imported_case(block_count=2):
    records = {}
    for i in range(block_count):
        records[f"decision/{i}/intent"] = "{}"
        records[f"decision/{i}/decided"] = "{}"
    return {"attach_error": None, "import_error": None, "head": block_count, "hooks_closed": True,
            "replay_transactions_equal": True, "replay_system_calls_equal": True,
            "admission_records": {"admission/counter": '{"next":2}'}, "decision_records": records,
            "transactions": [], "system_calls": [],
            "blocks": [{"number": n, "body_equal": True, "root_equal": True, "gas_equal": True,
                        "replay_error": None, "raw_block": "c0", "receipt_count": 0, "receipts": []}
                       for n in range(1, block_count + 1)]}


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
        del self.observation["main"]["decision_records"]["decision/0/decided"]
        self.failed("unresolved capture intent")

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
