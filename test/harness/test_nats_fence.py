"""Independent native-field controls; synthetic rows are not runtime evidence."""

import copy
import unittest

import nats_fence


def frontier():
    binding = {"account": "$G", "group": "S-frozen", "traffic_account": "$G",
               "system_account": False, "voters": {
        "fence-1": "W3vLCXAo", "fence-2": "zyhNdjE2", "fence-3": "3wiq6uFt"}}
    samples = {}
    for name, identifier in binding["voters"].items():
        samples[name] = {"$G": {"S-frozen": {
            "id": identifier, "state": "LEADER" if name == "fence-1" else "FOLLOWER",
            "size": 3, "quorum_needed": 2, "committed": 7, "applied": 7,
            "leader": "W3vLCXAo", "ever_had_leader": True, "term": 2,
            "pterm": 2, "pindex": 7, "system_account": False, "traffic_account": "$G",
            "ipq_proposal_len": 0, "ipq_entry_len": 0, "ipq_resp_len": 0,
            "ipq_apply_len": 0, "wal": {"messages": 2, "first_seq": 6, "last_seq": 7},
            "peers": {peer: {"name": peer_name, "known": True}
                      for peer_name, peer in binding["voters"].items() if peer != identifier},
        }}}
    return binding, samples


class NatsFrontierTests(unittest.TestCase):
    def test_voter_ids_must_match_the_declared_native_server_names(self):
        binding, samples = frontier()
        binding["voters"]["renamed"] = binding["voters"].pop("fence-3")
        samples["renamed"] = samples.pop("fence-3")
        for raw in samples.values():
            peers = raw["$G"]["S-frozen"]["peers"]
            if "3wiq6uFt" in peers: peers["3wiq6uFt"]["name"] = "renamed"
        with self.assertRaisesRegex(ValueError, "native server name"):
            nats_fence.validate_frontier(binding, samples, copy.deepcopy(samples))

    def test_all_voters_and_both_brackets_are_required(self):
        binding, samples = frontier()
        result = nats_fence.validate_frontier(binding, samples, copy.deepcopy(samples))
        self.assertEqual(result, {"leader": "W3vLCXAo", "term": 2, "last_log_term": 2, "index": 7})
        # WAL retention count is intentionally not the Raft applied index.
        for mutate in (
            lambda value: value.pop("fence-3"),
            lambda value: value.update({"foreign": value["fence-3"]}),
            lambda value: value["fence-3"].clear(),
            lambda value: value["fence-3"]["$G"].update({"S-extra": {}}),
        ):
            broken = copy.deepcopy(samples)
            mutate(broken)
            with self.subTest(mutate=mutate), self.assertRaises(ValueError):
                nats_fence.validate_frontier(binding, samples, broken)

    def test_empty_queues_cannot_hide_unapplied_or_uncommitted_work(self):
        binding, samples = frontier()
        for field, value in (("applied", 6), ("committed", 6), ("pindex", 8),
                             ("ipq_proposal_len", 1), ("ipq_entry_len", 1),
                             ("ipq_apply_len", 1), ("ipq_resp_len", 1)):
            broken = copy.deepcopy(samples)
            broken["fence-2"]["$G"]["S-frozen"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                nats_fence.validate_frontier(binding, broken, samples)

    def test_identity_health_and_type_conflicts_reject(self):
        binding, samples = frontier()
        changes = {"id": "FOREIGN1", "size": 2, "quorum_needed": 1,
                   "observer": True, "paused": True, "overrun": True,
                   "catching_up": True, "wal_error": "disk failure",
                   "system_account": True, "traffic_account": "OTHER",
                   "term": True, "applied": 7.0, "state": "CANDIDATE",
                   "leader": "FOREIGN1", "ever_had_leader": False}
        for field, value in changes.items():
            broken = copy.deepcopy(samples)
            broken["fence-2"]["$G"]["S-frozen"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                nats_fence.validate_frontier(binding, samples, broken)
        for mutate in (
            lambda row: row["peers"].pop("3wiq6uFt"),
            lambda row: row["peers"]["3wiq6uFt"].update(known=False),
            lambda row: row["peers"]["3wiq6uFt"].update(name="wrong"),
        ):
            broken = copy.deepcopy(samples)
            mutate(broken["fence-2"]["$G"]["S-frozen"])
            with self.assertRaises(ValueError):
                nats_fence.validate_frontier(binding, samples, broken)

    def test_changed_term_leader_or_frontier_invalidates_whole_collection(self):
        binding, samples = frontier()
        for change in ("term", "leader", "index", "pterm"):
            later = copy.deepcopy(samples)
            for name, by_account in later.items():
                row = by_account["$G"]["S-frozen"]
                if change == "term": row["term"] = 3
                elif change == "pterm": row["pterm"] = 1
                elif change == "leader":
                    row["leader"] = "zyhNdjE2"
                    row["state"] = "LEADER" if name == "fence-2" else "FOLLOWER"
                else: row.update(pindex=8, committed=8, applied=8)
            with self.subTest(change=change), self.assertRaises(ValueError):
                nats_fence.validate_frontier(binding, samples, later)


if __name__ == "__main__":
    unittest.main()
