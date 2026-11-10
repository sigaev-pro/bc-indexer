"""Portable checks of the E01-02.inputs Bor probe judge: no Docker or native client."""

import copy
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import bor_source_inputs_probe as probe
import source_inputs

FIXTURE = Path(__file__).with_name("fixtures") / "bor_source_inputs"
COMMIT = "3a018e38467594508ca31ebeaf804837fb92834a"
BASE_TIME = 1794317135


def context():
    oracle = probe.load(FIXTURE / "expected.json")
    bodies = {path.name: path.read_bytes() for path in (FIXTURE / "stub").iterdir()}
    times = [BASE_TIME, BASE_TIME + 1, BASE_TIME + 2, BASE_TIME + 8, BASE_TIME + 9, BASE_TIME + 10, BASE_TIME + 11,
             BASE_TIME + 17, BASE_TIME + 18]
    generated = {"blocks": [{"number": n, "hash": f"0x{n:064x}", "state_root": f"0x{n + 100:064x}", "timestamp": t}
                            for n, t in enumerate(times, 1)]}
    return probe.build_context(oracle, generated, bodies, b"genesis-bytes", b"blocks-bytes", COMMIT)


def call_rows(ctx, origins, requests=None):
    requests = requests or ctx["requests"]
    rows = []
    for index, origin in enumerate(origins):
        request = requests[index]["request"]
        rows.append({"sequence": index, "request": request,
                     "request_sha256": source_inputs.sha(request.encode()).hex(),
                     "response_sha256": ctx["expected"]["entries"][index]["response_sha256"],
                     "response_length": 1, "origin": origin})
    return rows


def dump(observation, count, closed=False, marker=None):
    prefix = probe.LOG_PREFIX + observation
    rows = [{"key": prefix + "/capsule", "value_sha256": "c" * 64, "value_length": 1}]
    for sequence in range(count):
        rows.append({"key": f"{prefix}/log/{sequence:016x}", "value_sha256": f"{sequence:064x}", "value_length": 1})
        rows.append({"key": f"{prefix}/log/{sequence:016x}/0000000000000000", "value_sha256": f"{sequence + 50:064x}",
                     "value_length": 1})
    if count:
        rows.append({"key": prefix + "/frontier", "value_sha256": f"{count + 90:064x}", "value_length": 1})
    if closed:
        rows.append({"key": prefix + "/closed", "value_sha256": "d" * 64, "value_length": 1})
    if marker:
        rows.append({"key": probe.MARKER_PREFIX + observation, "value_sha256": "e" * 64, "value_length": 1})
    return rows


def result(ctx, plan, step, rows, complete=True, frontier=0, consumed=True, marker=True):
    last = ctx["generated"]["blocks"][-1]
    value = {"observation": plan["observation"], "frontier_at_open": frontier, "counted_calls": rows,
             "inserted": len(ctx["generated"]["blocks"]) if complete else 3,
             "head_hash": last["hash"] if complete else "0x0", "head_root": last["state_root"] if complete else "0x0",
             "prefix_conflict": False, "crashed": False, "crash_after": step["crash_after"]}
    if not complete:
        value["import_error"] = "invalid merkle root"
    if complete and consumed and step["log"]:
        value["consumed"] = {"observation": plan["observation"], "capsule_digest": ctx["expected"]["capsule"],
                             "count": len(ctx["requests"]), "chain": "f" * 64, "digest": ctx["expected"]["consumed"]}
        if marker:
            value["stand_in_marker"] = {"kind": probe.MARKER_KIND, "observation": plan["observation"],
                                        "consumed_manifest_digest": ctx["expected"]["consumed"]}
    return value


def attempt(status, before, after, value=None, crash=None):
    return {"argv": [], "status": dict(status), "store_before": before, "store_after": after, "result": value,
            "crash_result": crash}


OK = {"exit_code": 0, "signal": ""}
KILLED = {"exit_code": -1, "signal": "killed"}


def passing(ctx, name):
    """A scenario observation that matches the oracle."""
    plan = ctx["plans"][name]
    obs, steps, total = plan["observation"], plan["attempts"], len(ctx["requests"])
    ran = {"name": name, "observation": obs, "attempts": steps}
    kind = plan["kind"]
    if kind in ("replay-after-loss", "tail-append"):
        point = steps[0]["crash_after"]
        crash = {"counted_calls": call_rows(ctx, ["fetch"] * (point + 1)), "crashed": True}
        retry = call_rows(ctx, ["replay"] * (point + 1) + ["fetch"] * (total - point - 1))
        attempts = [attempt(KILLED, [], dump(obs, point + 1), None, crash),
                    attempt(OK, dump(obs, point + 1), dump(obs, total, True, marker=True),
                            result(ctx, plan, steps[1], retry, frontier=point + 1))]
    elif kind == "prefix-conflict":
        point, offset = steps[0]["crash_after"], plan["conflict_offset"]
        delayed = probe.expected_requests(ctx["oracle"], ctx["timestamps"], steps[1]["state_sync_delay"])
        rows = call_rows(ctx, ["replay"] * offset)
        rows.append({"sequence": offset, "request": delayed[offset]["request"], "request_sha256": "0" * 64,
                     "response_length": 0, "origin": "replay",
                     "error": "dependency log prefix conflict: offset holds another request"})
        conflict = result(ctx, plan, steps[1], rows, complete=False, frontier=point + 1)
        conflict["prefix_conflict"] = True
        saved = dump(obs, point + 1)
        attempts = [attempt(KILLED, [], saved, None, {"counted_calls": call_rows(ctx, ["fetch"] * (point + 1))}),
                    attempt(OK, saved, copy.deepcopy(saved), conflict),
                    attempt(OK, saved, dump(obs, total, True, marker=True),
                            result(ctx, plan, steps[2], call_rows(
                                ctx, ["replay"] * (point + 1) + ["fetch"] * (total - point - 1)), frontier=point + 1))]
    elif kind == "control-changed-response":
        value = result(ctx, plan, steps[0], call_rows(ctx, ["fetch"] * 3), complete=False)
        attempts = [attempt(OK, [], [], value)]
    else:
        delayed = probe.expected_requests(ctx["oracle"], ctx["timestamps"], steps[0]["state_sync_delay"])
        attempts = [attempt(OK, [], [], result(ctx, plan, steps[0], call_rows(ctx, ["fetch"] * total, delayed)))]
    return {"plan": ran, "attempts": attempts}


class OracleTests(unittest.TestCase):
    def test_expected_requests_follow_the_block_times(self):
        ctx = context()
        requests = [row["request"] for row in ctx["requests"]]
        self.assertEqual(requests[:2], ["GET /bor/spans/0", "GET /bor/spans/1"])
        self.assertEqual(requests[2], "GET /clerk/time?from_id=1&to_time=2026-11-10T13:25:43Z&pagination.limit=50")
        self.assertEqual(requests[4], "GET /clerk/time?from_id=3&to_time=2026-11-10T13:25:52Z&pagination.limit=50")
        delayed = probe.expected_requests(ctx["oracle"], ctx["timestamps"], 1)
        self.assertEqual(delayed[2]["request"],
                         "GET /clerk/time?from_id=1&to_time=2026-11-10T13:25:42Z&pagination.limit=50")
        self.assertEqual(delayed[:2], ctx["requests"][:2])

    def test_expected_digests_use_the_readme_formulas(self):
        ctx = context()
        items = (("genesis", b"genesis-bytes"), ("blocks", b"blocks-bytes"), ("bor-source-commit", COMMIT.encode()))
        capsule = source_inputs.capsule_digest(items)
        chain = source_inputs.chain_start(capsule)
        for sequence, row in enumerate(ctx["requests"]):
            body = (FIXTURE / "stub" / row["body"]).read_bytes()
            chain = source_inputs.chain_next(chain, source_inputs.entry_digest(sequence, row["request"].encode(), body))
        self.assertEqual(ctx["expected"]["capsule"], capsule.hex())
        self.assertEqual(ctx["expected"]["consumed"], source_inputs.consumed_digest(capsule, 5, chain).hex())
        self.assertNotEqual(ctx["body_sha256"]["clerk-a-from-1.json"], ctx["body_sha256"]["clerk-b-from-1.json"])

    def test_every_scenario_passes_when_it_matches_the_oracle(self):
        ctx = context()
        self.assertEqual(sorted(ctx["plans"]), ["ac-cap-04-01", "ac-cap-04-02", "ac-cap-04-03",
                                                 "control-changed-response", "control-delay"])
        for name in ctx["plans"]:
            self.assertEqual(probe.checked_compare(name, passing(ctx, name), ctx), [], name)


class CompareControls(unittest.TestCase):
    """Each control breaks one property and expects the judge to refuse it."""

    def setUp(self):
        self.ctx = context()

    def refused(self, name, change, text):
        observed = passing(self.ctx, name)
        change(observed)
        failures = probe.checked_compare(name, observed, self.ctx)
        self.assertTrue(any(text in failure for failure in failures), failures)

    def test_plan_and_exit_status(self):
        self.refused("ac-cap-04-01", lambda o: o["plan"].update(observation="x"), "did not run the oracle plan")
        self.refused("ac-cap-04-01", lambda o: o["attempts"].pop(), "did not run the oracle plan")
        self.refused("ac-cap-04-01", lambda o: o["attempts"][0]["status"].update(signal=""), "not lost at its crash")
        self.refused("ac-cap-04-01", lambda o: o["attempts"][0].update(crash_result=None), "not lost at its crash")
        self.refused("ac-cap-04-01", lambda o: o["attempts"][1]["status"].update(exit_code=1), "exited with")
        self.refused("ac-cap-04-01", lambda o: o["attempts"][1]["status"].update(signal="killed"), "exited with")
        self.refused("control-delay", lambda o: o["attempts"][0].update(result=None), "left no result")

    def test_replay_after_loss(self):
        name = "ac-cap-04-01"
        calls = lambda o: o["attempts"][1]["result"]["counted_calls"]
        self.refused(name, lambda o: o["attempts"][0]["crash_result"]["counted_calls"].pop(), "lost attempt: 2 counted")
        self.refused(name, lambda o: o["attempts"][0].update(store_after=[
            row for row in o["attempts"][0]["store_after"] if not row["key"].endswith("/log/0000000000000002")]),
            "exactly the saved prefix")
        self.refused(name, lambda o: o["attempts"][1]["result"].update(frontier_at_open=2), "durable frontier")
        self.refused(name, lambda o: calls(o)[2].update(origin="fetch"), "origin fetch, expected replay")
        self.refused(name, lambda o: calls(o)[2].update(response_sha256=self.ctx["body_sha256"]["clerk-b-from-1.json"]),
                     "differ from the saved response")
        self.refused(name, lambda o: calls(o)[3].update(request="GET /clerk/time?from_id=4"), "expected sequence")
        self.refused(name, lambda o: calls(o)[3].update(request_sha256="1" * 64), "request digest differs")
        self.refused(name, lambda o: calls(o)[4].update(error="boom"), "failed: boom")
        self.refused(name, lambda o: calls(o)[1].update(sequence=7), "expected sequence")
        self.refused(name, lambda o: calls(o).pop(), "4 counted calls, expected 5")
        self.refused(name, lambda o: o["attempts"][1]["result"].update(head_hash="0x1"), "differs from the sealed")
        self.refused(name, lambda o: o["attempts"][1]["result"].update(head_root="0x1"), "differs from the sealed")
        self.refused(name, lambda o: o["attempts"][1]["result"].update(import_error="x"), "did not complete")
        self.refused(name, lambda o: o["attempts"][1]["result"].update(inserted=8), "did not complete")
        self.refused(name, lambda o: o["attempts"][1]["result"]["consumed"].update(digest="0" * 64),
                     "consumed-manifest digest differs")

    def test_changed_response_never_reaches_bor(self):
        changed = self.ctx["body_sha256"]["clerk-b-from-1.json"]
        # Make the changed body the expected one at a fetched offset, so only the changed-body rule can refuse it.
        self.ctx["expected"]["entries"][4]["response_sha256"] = changed
        observed = passing(self.ctx, "ac-cap-04-01")
        failures = probe.checked_compare("ac-cap-04-01", observed, self.ctx)
        self.assertTrue(any("changed external response reached Bor" in f for f in failures), failures)

    def test_prefix_conflict(self):
        name = "ac-cap-04-02"
        conflict = lambda o: o["attempts"][1]["result"]
        self.refused(name, lambda o: conflict(o)["counted_calls"][2].update(error="other error"), "prefix conflict")
        self.refused(name, lambda o: conflict(o)["counted_calls"][2].update(request=self.ctx["requests"][2]["request"]),
                     "prefix conflict")
        self.refused(name, lambda o: conflict(o)["counted_calls"].append(conflict(o)["counted_calls"][1]),
                     "prefix conflict")
        self.refused(name, lambda o: conflict(o)["counted_calls"][0].update(origin="fetch"), "conflicting retry")
        self.refused(name, lambda o: conflict(o).update(prefix_conflict=False), "did not fail with the prefix")
        self.refused(name, lambda o: conflict(o).pop("import_error"), "did not fail with the prefix")
        self.refused(name, lambda o: conflict(o).update(consumed={"digest": "x"}), "closed the log")
        self.refused(name, lambda o: conflict(o).update(stand_in_marker={"kind": "x"}), "closed the log")
        self.refused(name, lambda o: o["attempts"][1]["store_after"].pop(), "changed the store")
        self.refused(name, lambda o: o["attempts"][1]["store_after"][1].update(value_sha256="9" * 64),
                     "changed the store")
        self.refused(name, lambda o: o["attempts"][1].update(store_before=[]), "changed the store")
        # Before and after agree with each other but not with the store the lost attempt left.
        self.refused(name, lambda o: o["attempts"][1].update(store_before=[], store_after=[]), "changed the store")
        self.refused(name, lambda o: o["attempts"][2]["result"].update(frontier_at_open=0), "keep the saved frontier")
        self.refused(name, lambda o: o["attempts"][2]["result"]["counted_calls"][0].update(origin="fetch"),
                     "reopened retry")
        self.refused(name, lambda o: o["attempts"][2]["result"].update(head_hash="0x1"), "reopened retry")

    def test_conflict_offset_must_be_a_real_conflict(self):
        self.ctx["plans"]["ac-cap-04-02"]["conflict_offset"] = 1
        observed = passing(self.ctx, "ac-cap-04-02")
        observed["plan"]["attempts"] = self.ctx["plans"]["ac-cap-04-02"]["attempts"]
        failures = probe.checked_compare("ac-cap-04-02", observed, self.ctx)
        self.assertTrue(any("conflict offset" in f for f in failures), failures)
        self.ctx["plans"]["ac-cap-04-02"]["conflict_offset"] = 3
        failures = probe.checked_compare("ac-cap-04-02", passing(self.ctx, "ac-cap-04-02"), self.ctx)
        self.assertTrue(any("conflict offset" in f for f in failures), failures)

    def test_tail_append(self):
        name = "ac-cap-04-03"
        retry = lambda o: o["attempts"][1]["result"]
        self.refused(name, lambda o: o["attempts"][0]["store_after"].append(
            {"key": probe.LOG_PREFIX + "ac-cap-04-03/log/0000000000000004", "value_sha256": "0", "value_length": 1}),
            "exactly the saved prefix")
        self.refused(name, lambda o: retry(o).update(frontier_at_open=3), "durable frontier")
        self.refused(name, lambda o: retry(o)["counted_calls"][1].update(origin="fetch"), "origin fetch")
        self.refused(name, lambda o: retry(o)["counted_calls"][2].update(origin="replay"), "origin replay")
        self.refused(name, lambda o: o["attempts"][1]["store_after"][1].update(value_sha256="9" * 64),
                     "saved prefix key")
        self.refused(name, lambda o: o["attempts"][1]["store_after"].pop(1), "saved prefix key")
        self.refused(name, lambda o: o["attempts"][1].update(store_after=dump("ac-cap-04-03", 4, True, True)),
                     "tail was not appended")
        self.refused(name, lambda o: retry(o).update(head_root="0x1"), "differs from the sealed")
        self.refused(name, lambda o: retry(o)["consumed"].update(digest="0" * 64), "final consumed manifest")
        self.refused(name, lambda o: retry(o)["consumed"].update(count=4), "final consumed manifest")
        self.refused(name, lambda o: retry(o)["consumed"].update(capsule_digest="0" * 64), "final consumed manifest")
        self.refused(name, lambda o: retry(o).pop("consumed"), "final consumed manifest")
        self.refused(name, lambda o: retry(o)["stand_in_marker"].update(consumed_manifest_digest="0" * 64),
                     "does not bind")
        self.refused(name, lambda o: retry(o)["stand_in_marker"].update(kind="exec-ready"), "does not bind")
        self.refused(name, lambda o: retry(o)["stand_in_marker"].update(observation="x"), "does not bind")
        self.refused(name, lambda o: o["attempts"][1]["store_after"].pop(), "marker is not in the store")

    def test_frontier_key_may_change_in_the_tail(self):
        observed = passing(self.ctx, "ac-cap-04-03")
        self.assertEqual(probe.checked_compare("ac-cap-04-03", observed, self.ctx), [])
        frontier = [row for row in observed["attempts"][0]["store_after"] if row["key"].endswith("/frontier")]
        after = [row for row in observed["attempts"][1]["store_after"] if row["key"].endswith("/frontier")]
        self.assertNotEqual(frontier[0]["value_sha256"], after[0]["value_sha256"])

    def test_controls(self):
        self.refused("control-changed-response", lambda o: o["attempts"][0]["result"].pop("import_error"),
                     "did not make native import fail")
        self.refused("control-changed-response", lambda o: o["attempts"][0]["result"].update(inserted=9),
                     "did not make native import fail")
        self.refused("control-delay", lambda o: o["attempts"][0]["result"]["counted_calls"][2].update(
            request=self.ctx["requests"][2]["request"]), "delayed request sequence")
        self.refused("control-delay", lambda o: o["attempts"][0]["result"].update(head_hash="0x1"),
                     "differs from the sealed")

    def test_unknown_kind_and_malformed_observation(self):
        self.ctx["plans"]["control-delay"]["kind"] = "other"
        self.assertTrue(any("unknown scenario kind" in f
                            for f in probe.checked_compare("control-delay", passing(context(), "control-delay"), self.ctx)))
        failures = probe.checked_compare("ac-cap-04-01", {"plan": None, "attempts": [None, None]}, context())
        self.assertTrue(failures)
        observed = passing(self.ctx, "ac-cap-04-01")
        observed["attempts"][1]["result"]["counted_calls"] = None
        self.assertTrue(any("malformed observation" in f for f in probe.checked_compare("ac-cap-04-01", observed,
                                                                                         context())))

    def test_to_time_format(self):
        self.assertEqual(probe.to_time(1794317143, 0), "2026-11-10T13:25:43Z")
        self.assertEqual(probe.to_time(1794317143, 1), "2026-11-10T13:25:42Z")
        self.assertEqual(probe.to_time(0, 0), "1970-01-01T00:00:00Z")

    def test_log_key_helpers(self):
        rows = dump("obs", 2, True, marker=True) + dump("other", 3)
        keys = probe.log_keys(rows, "obs")
        self.assertEqual(probe.entry_sequences(keys, "obs"), [0, 1])
        self.assertTrue(all(key.startswith(probe.LOG_PREFIX + "obs/") for key in keys))
        self.assertNotIn(probe.LOG_PREFIX + "ob/log/0000000000000000", probe.log_keys(dump("ob", 1), "obs"))


if __name__ == "__main__":
    unittest.main()
