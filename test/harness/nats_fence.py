"""Check exact native Raft observations; this does not establish process recovery."""

import hashlib
import re

from artifacts import closed, require


def integer(value, label, minimum=0):
    require(type(value) is int and minimum <= value < 2**64, "Invalid " + label)
    return value


def validate_frontier(binding, before, after):
    """Validate raw filtered Raftz brackets for one declared three-voter group.

    The caller must authenticate configuration, process generations, response
    bytes and collection order separately. Synthetic rows cannot prove recovery.
    """
    closed(binding, {"account", "group", "traffic_account", "system_account", "voters"}, "Raft binding")
    for key in ("account", "group", "traffic_account"):
        require(isinstance(binding[key], str) and binding[key] and len(binding[key]) <= 256,
                "Invalid Raft " + key)
    require(type(binding["system_account"]) is bool, "Invalid Raft account kind")
    voters = binding["voters"]
    require(isinstance(voters, dict) and len(voters) == 3, "Exactly three voting identities required")
    for name, identifier in voters.items():
        require(isinstance(name, str) and re.fullmatch(r"[A-Za-z0-9_-]{1,64}", name) and
                isinstance(identifier, str) and re.fullmatch(r"[A-Za-z0-9]{8}", identifier),
                "Invalid voting identity")
        # Pinned server/events.go getHashSize and server/accounts.go alphabet.
        alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
        native_id = "".join(alphabet[byte % 62] for byte in hashlib.sha256(name.encode()).digest()[:8])
        require(identifier == native_id, "Voter ID does not match native server name")
    require(len(set(voters.values())) == 3, "Repeated voting identity")
    common = None
    for samples in (before, after):
        require(isinstance(samples, dict) and set(samples) == set(voters), "Missing or extra voter observations")
        leaders = []
        for name, identifier in voters.items():
            raw = samples[name]
            closed(raw, {binding["account"]}, "Raft account observations")
            closed(raw[binding["account"]], {binding["group"]}, "Raft group observations")
            row = raw[binding["account"]][binding["group"]]
            require(isinstance(row, dict), "Invalid native Raft row")
            require(row.get("id") == identifier and integer(row.get("size"), "group size") == 3 and
                    integer(row.get("quorum_needed"), "quorum") == 2, "Raft identity or membership changed")
            require(row.get("system_account") is binding["system_account"] and
                    row.get("traffic_account") == binding["traffic_account"], "Raft transport account changed")
            for flag in ("observer", "paused", "overrun", "catching_up"):
                require(row.get(flag, False) is False, "Unsafe Raft " + flag)
            require(row.get("wal_error") is None and isinstance(row.get("wal"), dict), "Raft WAL error or missing state")
            require(row.get("ever_had_leader") is True, "Raft has no recovered leader")
            peers = row.get("peers")
            require(isinstance(peers, dict) and set(peers) == set(voters.values()) - {identifier},
                    "Raft peer membership changed")
            for peer_name, peer in voters.items():
                if peer != identifier:
                    require(isinstance(peers[peer], dict) and peers[peer].get("known") is True and
                            peers[peer].get("name") == peer_name, "Unknown or substituted Raft peer")
            leader = row.get("leader")
            require(leader in voters.values() and row.get("state") ==
                    ("LEADER" if identifier == leader else "FOLLOWER"), "Raft role or leader mismatch")
            if row["state"] == "LEADER": leaders.append(identifier)
            term = integer(row.get("term"), "Raft term", 1)
            log_term = integer(row.get("pterm"), "last log term")
            require(log_term <= term, "Last log term exceeds current term")
            index = integer(row.get("pindex"), "last log index")
            require(integer(row.get("committed"), "commit index") == index and
                    integer(row.get("applied"), "applied index") == index, "Raft work remains unapplied")
            for queue in ("ipq_proposal_len", "ipq_entry_len", "ipq_resp_len", "ipq_apply_len"):
                require(integer(row.get(queue), queue) == 0, "Raft queue is not empty")
            current = {"leader": leader, "term": term, "last_log_term": log_term, "index": index}
            if common is None: common = current
            require(current == common, "Raft frontier changed during collection")
        require(leaders == [common["leader"]], "Exactly one recovered leader required")
    return common
