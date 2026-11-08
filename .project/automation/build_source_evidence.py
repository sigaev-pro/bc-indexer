"""Closed, hash-bound source GET evidence; never native-command acceptance."""

import copy
import hashlib
import math
from pathlib import Path
import re

import build_contract as contract


MAX_JOURNAL = 1024 * 1024
MAX_BODY = 1024 ** 3
MAX_GH_BODY = 256 * 1024 * 1024
MAX_PROCESS_CHUNK = 64 * 1024
MAX_HEADERS = 64 * 1024
MAX_STDERR = 1024 * 1024
OBSERVATION_KEYS = {"url", "status", "headers", "body", "observed_at", "acquisition"}
JOURNAL_KEYS = {"schema_version", "url", "synthetic", "transport", "outcome", "attempts"}
ATTEMPT_KEYS = {"ordinal", "state", "started_at", "finished_at", "observed_at", "status", "headers", "body",
                "error", "observed_bytes", "truncated", "process"}
ERROR_KEYS = {"type", "reason_type", "code", "errno", "retryable"}
SAFE_HEADERS = {"content-type", "content-length", "docker-content-digest", "link", "etag", "last-modified"}


def timestamp(value):
    return type(value) in (int, float) and math.isfinite(value)


def headers(value):
    return (type(value) is dict and all(isinstance(k, str) and isinstance(v, str) for k, v in value.items())
            and len(contract.canonical(value)) <= MAX_HEADERS
            and not {key.lower() for key in value} & {"authorization", "proxy-authorization", "set-cookie"})


def gh_header(path):
    """Read only a bounded header prefix; return the raw body's byte offset."""
    with Path(path).open("rb") as stream:
        prefix = stream.read(MAX_HEADERS + 4)
    delimiter = b"\r\n\r\n" if b"\r\n\r\n" in prefix else b"\n\n"
    header, found, _ = prefix.partition(delimiter)
    if not found:
        return None, {}, 0
    contract.require(len(header) <= MAX_HEADERS, "source response headers exceed their bound")
    lines = header.decode("utf-8").splitlines()
    match = re.fullmatch(r"HTTP/\S+ ([0-9]{3})(?: .*)?", lines[0])
    contract.require(match is not None, "source response has an invalid HTTP status")
    result = {}
    for line in lines[1:]:
        key, separator, value = line.partition(":")
        contract.require(separator and key.strip(), "source response has an invalid HTTP header")
        key = key.lower()
        if key in SAFE_HEADERS:
            contract.require(key not in result, "source response repeats a retained header")
            result[key] = value.strip()
    return int(match[1]), result, len(header) + len(delimiter)


def verify_body(root, reference, maximum):
    contract.reference_descriptor(reference)
    contract.require(reference["size_bytes"] <= maximum, "source evidence exceeds its byte bound")
    return contract.verify_reference(root, reference)


def retryable(error, transport, status):
    if status not in (None, 200):
        return False
    direct = error["type"]
    reason = error["reason_type"]
    if error["code"] == "NETWORK_TIMEOUT":
        return transport == "urllib" and ((direct == "TimeoutError" and reason is None)
                                           or (direct == "URLError" and reason == "TimeoutError"))
    if error["code"] == "SSL_EOF":
        return transport == "urllib" and ((direct == "SSLEOFError" and reason is None)
                                           or (direct == "URLError" and reason == "SSLEOFError"))
    return transport == "gh" and error["code"] == "GH_EOF" and direct == "GhAPIError" and reason is None


def validate_process(process, attempt, journal, root):
    if journal["transport"] == "urllib":
        contract.require(process is None, "urllib evidence contains another transport's process")
        return
    contract.require(type(process) is dict and set(process) == {"argv", "exit_code", "stdout", "stderr",
                                                              "stdout_observed_bytes", "stdout_truncated",
                                                              "stderr_observed_bytes", "stderr_truncated"}
                     and process["argv"] == ["gh", "api", "--include", "--method", "GET", journal["url"]]
                     and type(process["exit_code"]) is int, "source subprocess has another command or no terminal result")
    stdout = verify_body(root, process["stdout"], MAX_GH_BODY + MAX_HEADERS)
    stderr = verify_body(root, process["stderr"], MAX_STDERR)
    for field, path, bound in (("stdout", stdout, MAX_GH_BODY + MAX_HEADERS), ("stderr", stderr, MAX_STDERR)):
        observed, truncated = process[field + "_observed_bytes"], process[field + "_truncated"]
        contract.require(type(observed) is int and path.stat().st_size <= observed <= bound + MAX_PROCESS_CHUNK
                         and type(truncated) is bool and truncated is (observed > path.stat().st_size),
                         "source raw process byte accounting differs or exceeds its bound")
        contract.require(not truncated or attempt["state"] == "FAILED" and attempt["error"] is not None
                         and not attempt["error"]["retryable"], "truncated raw source output cannot prove success or recovery")
    if attempt["error"] is not None and attempt["error"]["code"] == "GH_EOF":
        expected = ('Get "' + journal["url"] + '": EOF\n').encode()
        contract.require(process["exit_code"] == 1 and stderr.read_bytes() == expected,
                         "GitHub EOF evidence has another exit or diagnostic")
    if attempt["state"] == "SUCCEEDED" or attempt["error"] is not None and attempt["error"]["code"] == "GH_EOF":
        status, actual_headers, offset = gh_header(stdout)
        with stdout.open("rb") as stream:
            stream.seek(offset)
            digest = hashlib.file_digest(stream, "sha256").hexdigest()
        contract.require((attempt["state"] != "SUCCEEDED" or process["exit_code"] == 0)
                         and attempt["body"] is not None and status == attempt["status"] and actual_headers == attempt["headers"]
                         and stdout.stat().st_size - offset == attempt["body"]["size_bytes"]
                         and digest == attempt["body"]["sha256"], "GitHub process does not bind its successful observation")


def validate_observation(row, root, synthetic):
    contract.require(type(row) is dict and set(row) == OBSERVATION_KEYS and type(synthetic) is bool
                     and isinstance(row["url"], str) and type(row["status"]) is int and 0 <= row["status"] <= 599
                     and headers(row["headers"]) and timestamp(row["observed_at"]),
                     "source observation lacks its closed acquisition reference")
    path = verify_body(root, row["acquisition"], MAX_JOURNAL)
    journal = contract.parse_json(path.read_bytes())
    contract.require(type(journal) is dict and set(journal) == JOURNAL_KEYS and type(journal["schema_version"]) is int
                     and journal["schema_version"] == 1 and journal["url"] == row["url"] and journal["synthetic"] is synthetic
                     and journal["transport"] in {"urllib", "gh"} and type(journal["attempts"]) is list
                     and 1 <= len(journal["attempts"]) <= 2, "source acquisition has another identity or unbounded attempts")
    contract.require(journal["transport"] != "gh" or not synthetic and row["url"].startswith("https://api.github.com/"),
                     "GitHub acquisition uses another source or claims a synthetic process")
    for ordinal, attempt in enumerate(journal["attempts"], 1):
        contract.require(type(attempt) is dict and set(attempt) == ATTEMPT_KEYS and type(attempt["ordinal"]) is int
                         and attempt["ordinal"] == ordinal and attempt["state"] in {"FAILED", "SUCCEEDED"}
                         and all(timestamp(attempt[key]) for key in ("started_at", "finished_at", "observed_at"))
                         and (attempt["status"] is None or type(attempt["status"]) is int and 100 <= attempt["status"] <= 599)
                         and headers(attempt["headers"]) and type(attempt["observed_bytes"]) is int
                         and attempt["observed_bytes"] >= 0 and type(attempt["truncated"]) is bool,
                         "source attempt is unfinished, malformed, or out of order")
        retained = 0
        if attempt["body"] is not None:
            verify_body(root, attempt["body"], MAX_BODY)
            retained = attempt["body"]["size_bytes"]
        contract.require(attempt["observed_bytes"] >= retained
                         and attempt["truncated"] is (attempt["observed_bytes"] > retained), "source partial-byte accounting differs")
        error = attempt["error"]
        if attempt["state"] == "SUCCEEDED":
            contract.require(error is None and attempt["status"] == 200 and attempt["body"] is not None
                             and not attempt["truncated"], "successful source attempt is incomplete")
        else:
            contract.require(type(error) is dict and set(error) == ERROR_KEYS
                             and isinstance(error["type"], str) and re.fullmatch(r"[A-Za-z_][A-Za-z_0-9]{0,127}", error["type"])
                             and (error["reason_type"] is None or isinstance(error["reason_type"], str)
                                  and re.fullmatch(r"[A-Za-z_][A-Za-z_0-9]{0,127}", error["reason_type"]))
                             and error["code"] in {"NETWORK_TIMEOUT", "SSL_EOF", "GH_EOF", "REQUEST_DEADLINE", "UNKNOWN_TRANSPORT", "LOCAL_IO", "REJECTED"}
                             and (error["errno"] is None or type(error["errno"]) is int and -4096 <= error["errno"] <= 4096)
                             and type(error["retryable"]) is bool
                             and error["retryable"] is retryable(error, journal["transport"], attempt["status"]),
                             "source error classification or retry eligibility differs")
            contract.require(not error["retryable"] or not attempt["truncated"], "truncated source evidence cannot authorize recovery")
        validate_process(attempt["process"], attempt, journal, root)
        if ordinal < len(journal["attempts"]):
            contract.require(attempt["state"] == "FAILED" and error["retryable"] and not synthetic,
                             "source recovery followed a success, fixture, or nonretryable failure")
    final = journal["attempts"][-1]
    if final["state"] == "SUCCEEDED":
        outcome = "FIRST_ATTEMPT_SUCCESS" if len(journal["attempts"]) == 1 else "RECOVERED_TRANSPORT"
        contract.require(journal["outcome"] == outcome
                         and all(row[key] == final[key] for key in ("status", "headers", "body", "observed_at")),
                         "source observation differs from its successful attempt")
    else:
        contract.require(journal["outcome"] == "FAILED" and row["status"] == 0 and row["headers"] == {}
                         and row["observed_at"] == final["observed_at"], "failed source observation hides its acquisition failure")
    verify_body(root, row["body"], MAX_BODY)
    return journal


def fixture_observation(row, root):
    """Bind an explicit synthetic response without claiming a network request."""
    row = copy.deepcopy(row)
    row.pop("acquisition", None)
    root = Path(root)
    key = contract.digest(row)
    target = root / "fixture-acquisitions" / (key + ".json")
    target.parent.mkdir(exist_ok=True)
    success = row["status"] == 200
    attempt = {"ordinal": 1, "state": "SUCCEEDED" if success else "FAILED", "started_at": row["observed_at"],
               "finished_at": row["observed_at"], "observed_at": row["observed_at"], "status": row["status"],
               "headers": row["headers"], "body": row["body"], "observed_bytes": row["body"]["size_bytes"],
               "truncated": False, "process": None, "error": None if success else
               {"type": "HTTPError", "reason_type": None, "code": "REJECTED", "errno": None, "retryable": False}}
    journal = {"schema_version": 1, "url": row["url"], "synthetic": True, "transport": "urllib",
               "outcome": "FIRST_ATTEMPT_SUCCESS" if success else "FAILED", "attempts": [attempt]}
    payload = contract.canonical(journal)
    if target.exists():
        contract.require(target.read_bytes() == payload, "synthetic acquisition identity was reused")
    else:
        with target.open("xb") as stream:
            stream.write(payload)
    if not success:
        diagnostic = target.with_suffix(".failure")
        diagnostic.write_bytes(b"Source acquisition failed: HTTPError")
        row.update(status=0, headers={}, body=contract.reference(root, diagnostic))
    row["acquisition"] = contract.reference(root, target)
    return row


def validate_report_journals(path, mode):
    """Recheck retained bytes after candidate commands, not source freshness again."""
    path = Path(path)
    contract.require(path.stat().st_size <= 32 * 1024 * 1024, "retained updater report exceeds its bound")
    report = contract.parse_json(path.read_bytes())
    contract.require(type(report) is dict and type(report.get("schema_version")) is int and report["schema_version"] == 1
                     and report.get("contract") == "component-update-v1" and report.get("mode") == mode
                     and report.get("synthetic") is False and report.get("complete") is True
                     and type(report.get("observations")) is list and 1 <= len(report["observations"]) <= 4096,
                     "retained source pass has another mode or is incomplete")
    seen = set()
    for row in report["observations"]:
        validate_observation(row, path.parent, False)
        contract.require(row["url"] not in seen, "retained source pass repeats a URL")
        seen.add(row["url"])


def validate_updater_journals(updater, root):
    proof_path = contract.verify_reference(root, updater["signature_proof"])
    contract.require(proof_path.stat().st_size <= MAX_JOURNAL, "updater signature proof exceeds its bound")
    proof = contract.parse_json(proof_path.read_bytes())
    contract.require(type(proof) is dict and "check_report" in proof, "updater proof omits its independent check pass")
    check = contract.verify_reference(root, updater["check_report"])
    update = contract.verify_reference(root, updater["report"])
    independent_check = contract.verify_reference(root, proof["check_report"])
    independent_update = contract.verify_reference(root, updater["independent_report"])
    contract.require(independent_check == check.parent / "independent-report.json"
                     and independent_update == update.parent / "independent-report.json"
                     and check.parent != update.parent, "independent source pass belongs to another invocation")
    for path, mode in ((check, "check"), (update, "update"), (independent_check, "check"), (independent_update, "update")):
        validate_report_journals(path, mode)
