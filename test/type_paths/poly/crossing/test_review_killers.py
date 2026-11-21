"""Independent reviewer killers for the thirteen open madhugiri conjuncts."""
import copy
import json
import unittest

import test_crossing_madhugiri as tm

j, c, PRIOR, b = tm.j, tm.c, tm.PRIOR, tm.Builder


def slot_view(view, address, index, value):
    view = dict(view); inner = dict(view[address]); storage = dict(inner["storage"])
    storage["0x" + f"{index:064x}"] = f"{value:064x}"
    inner["storage"] = storage; view[address] = inner
    return view


def target_view(view, storage_values):
    view = dict(view); inner = dict(view[PRIOR.TARGET]); storage = dict(inner["storage"])
    for i, v in enumerate(storage_values):
        storage["0x" + f"{i:064x}"] = f"{v:064x}"
    inner["storage"] = storage; view[PRIOR.TARGET] = inner
    return view


class ReviewerKillers(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        b.prepare()
        cls.report = tm.build_madhugiri_report()

    def setUp(self):
        original = j.MADHUGIRI_BASE_FEES
        j.MADHUGIRI_BASE_FEES = (b.base1, b.base2)
        self.addCleanup(setattr, j, "MADHUGIRI_BASE_FEES", original)

    def reject(self, change, index=6):
        report = copy.deepcopy(self.report)
        change(report["cases"][index])
        self.assertEqual(j.judge(report)["result"], "FAIL")

    def calls(self, row):
        return row["crossing"]["blocks"][1]["calls"]

    def test_inner_progress_before_conjunct(self):
        def change(row):
            r = self.calls(row)
            r[3]["before"] = slot_view(r[3]["before"], PRIOR.RECEIVER, 0, 3)
        self.reject(change)

    def test_outer_outcome_state_conjunct(self):
        def change(row):
            r = self.calls(row)
            r[2]["after"] = slot_view(r[2]["after"], PRIOR.REVERT, 1, 1)
            r[4]["before"] = slot_view(r[4]["before"], PRIOR.REVERT, 1, 1)
        self.reject(change)

    def test_inner_revert_state_conjunct(self):
        def change(row):
            r = self.calls(row)
            r[5]["before"] = slot_view(r[5]["before"], PRIOR.REVERT, 1, 1)
        self.reject(change)

    def test_inner_success_state_before_conjunct(self):
        def change(row):
            r = self.calls(row)
            r[3]["before"] = slot_view(r[3]["before"], PRIOR.TARGET, 0, 5)
        self.reject(change)

    def test_inner_success_state_after_conjunct(self):
        def change(row):
            r = self.calls(row)
            values = (2, tm.RECEIVER_ADDRESS, len(tm.DATA), tm.PADDED)
            r[3]["after"] = target_view(r[3]["after"], values)
            r[2]["after"] = target_view(r[2]["after"], values)
            r[4]["before"] = target_view(r[4]["before"], values)
        self.reject(change)

    def test_last_action_state_conjunct(self):
        def change(row):
            r = self.calls(row)
            r[4]["after"] = slot_view(r[4]["after"], PRIOR.REVERT, 1, 1)
            r[5]["after"] = slot_view(r[5]["after"], PRIOR.REVERT, 1, 1)
            r[5]["before"] = slot_view(r[5]["before"], PRIOR.REVERT, 1, 1)
        self.reject(change)

    def test_carrier_items_conjunct(self):
        def change(row):
            items = [list(item) for item in b.items]
            items[0][3] = (5).to_bytes(32, "big")
            wire = b"\x7f" + c.rlp(items)
            second = b.assemble(b.header2, [b.legacy_wire, wire], [b.oracle2, b.sync_oracle], [0, 127],
                                bytes(x | y for x, y in zip(b.oracle2["bloom"], b.sync_oracle["bloom"])),
                                b.final_state())
            b.extend(second, (1, 2))
            tagged = "0x" + c.keccak256(wire).hex()
            old_system = "0x" + c.keccak256(b.original).hex()
            original = row["crossing"]["blocks"][1]
            second["transactions"] = [dict(r, hash=tagged, raw=wire.hex()) if r["type"] == 127 else r
                                      for r in original["transactions"]]
            second["calls"] = [dict(call, transaction=tagged) if call["transaction"] == old_system else call
                               for call in original["calls"]]
            receipt = second["receipts"][1]
            receipt["transactionHash"] = tagged
            for log in receipt["logs"]:
                log["transactionHash"] = tagged
            row["crossing"]["blocks"][1] = second
            tm.relink(row, second)
        self.reject(change)

    def candidate_block(self, txs):
        cand = b.assemble(b.header1, txs, [], [], bytes(256), b.genesis_state())
        fields = PRIOR.plain_rlp(c.rlp_list(bytes.fromhex(cand["raw_block"])))[0]
        fields[3] = b.genesis[3]
        return {**tm.seal(fields, txs), "transaction_proofs": cand["transaction_proofs"],
                "bor_receipt": {"present": False, "logs": []}, "state_sync": [],
                "transactions": [], "calls": []}

    def swap_candidate(self, row, candidate):
        negative = row["before_activation"]
        negative["block"] = candidate
        negative["head_hash"] = candidate["hash"]
        rec = tm.Records.intent(0, "IMPORT", {"number": 0, "hash": b.genesis_hash[2:]},
                                {"number": 1, "hash": candidate["hash"][2:]}, [(0, candidate)])
        rec["extractor-decision-v1/bor.run-1/counter"] = json.dumps({"next": 1})
        negative["decisions"] = rec
        tm.relink(row, row["crossing"]["blocks"][1])

    def test_observation_body_count_conjunct(self):
        def change(row):
            self.swap_candidate(row, self.candidate_block([b.original, b.legacy_wire]))
        self.reject(change)

    def test_observation_body_tag_conjunct(self):
        def change(row):
            self.swap_candidate(row, self.candidate_block([bytes([126]) + b.original[1:]]))
        self.reject(change)

    def test_observation_items_conjunct(self):
        def change(row):
            items = [list(item) for item in b.items]
            items[0][3] = (5).to_bytes(32, "big")
            self.swap_candidate(row, self.candidate_block([b"\x7f" + c.rlp(items)]))
        self.reject(change)

    def edited_candidate(self, field, value):
        cand = self.candidate_block([b.original])
        tm.reports.edit_header(cand, field, value)
        return cand

    def test_observation_commitments_gas_conjunct(self):
        self.reject(lambda row: self.swap_candidate(row, self.edited_candidate(10, b"\x01")))

    def test_observation_commitments_bloom_conjunct(self):
        self.reject(lambda row: self.swap_candidate(row, self.edited_candidate(6, b"\x01" + bytes(255))))

    def test_observation_commitments_root_conjunct(self):
        self.reject(lambda row: self.swap_candidate(row, self.edited_candidate(5, c.keccak256(b""))))

    def test_observation_commitments_state_root_conjunct(self):
        self.reject(lambda row: self.swap_candidate(row, self.edited_candidate(3, bytes(32))))

    def test_live_boundaries_after_conjunct(self):
        # The system live row's after-state, moved together with every frame
        # state that cites it, isolates the boundary conjunct.
        replacement = b.traced_view(b.final_state(receiver_slot=3))
        def change(row):
            block = tm.Builder if False else row["crossing"]["blocks"][1]
            old = block["transactions"][1]["after"]
            block["transactions"][1]["after"] = replacement
            for call in block["calls"]:
                for field in ("before", "after"):
                    if call[field] == old:
                        call[field] = replacement
        self.reject(change)

    def test_carrier_envelope_tag_conjunct(self):
        # A 0x7e tag byte over the identical payload, with the live row,
        # receipt oracles and frame correlation all following the new wire,
        # isolates the envelope conjunct.
        def change(row):
            evil = b"\x7e" + b.original[1:]
            sync = dict(b.sync_oracle, wire=b"\x7e" + b.sync_oracle["wire"][1:])
            receipt = dict(tm.local_sync_receipt(21000 + 7))
            receipt["type"] = "0x7e"
            block = b.assemble(b.header2, [b.legacy_wire, evil], [b.oracle2, sync], [0, 126],
                               bytes(x | y for x, y in zip(b.oracle2["bloom"], b.sync_oracle["bloom"])),
                               b.final_state())
            b.extend(block, (1, 2))
            block["state_sync"] = row["crossing"]["blocks"][1]["state_sync"]
            block["bor_receipt"] = row["crossing"]["blocks"][1]["bor_receipt"]
            block["transactions"] = [
                b.live_row(b.legacy_wire, 0, PRIOR.SENDER, b.traced_view(b.state1()), b.traced_view(b.mid_state())),
                b.live_row(evil, 126, PRIOR.SYSTEM, b.traced_view(b.mid_state()),
                           b.traced_view(b.final_state()), receipt)]
            new_hash = "0x" + c.keccak256(evil).hex()
            block["calls"] = [dict(call) for call in row["crossing"]["blocks"][1]["calls"]]
            for call in block["calls"]:
                if call["depth"] > 0 or call["from"].lower() == PRIOR.SYSTEM:
                    call["transaction"] = new_hash
            row["crossing"]["blocks"][1] = block
        self.reject(change)


if __name__ == "__main__":
    unittest.main()
