"""Bounded source acquisition and independent retained-attempt controls."""

import copy
import http.client
import io
import json
import os
from pathlib import Path
import signal
import ssl
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.parse
from unittest.mock import patch

import build_contract as contract
import build_source_evidence as source_evidence
import build_source_transport as transport


URL = "https://api.github.com/repos/example/client/releases?per_page=100"


class SourceEvidenceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()

    def observation(self):
        path = self.root / "body.json"
        path.write_bytes(b"[]")
        row = {"url": URL, "status": 200, "headers": {}, "body": contract.reference(self.root, path), "observed_at": 100.0}
        return source_evidence.fixture_observation(row, self.root)

    def test_complete_fixture_binds_its_source_bytes(self):
        row = self.observation()
        source_evidence.validate_observation(row, self.root, True)
        changed = copy.deepcopy(row)
        changed["observed_at"] += 1
        with self.assertRaises(ValueError):
            source_evidence.validate_observation(changed, self.root, True)

    def test_missing_acquisition_is_not_current_evidence(self):
        row = self.observation()
        row.pop("acquisition")
        with self.assertRaises(ValueError):
            source_evidence.validate_observation(row, self.root, True)

    def test_fresh_hashes_do_not_hide_changed_attempt_contracts(self):
        row = self.observation()
        path = self.root / row["acquisition"]["path"]
        original = json.loads(path.read_text())
        mutations = [lambda j: j.update(schema_version=True), lambda j: j.update(url=URL + "&page=2"),
                     lambda j: j.update(synthetic=False), lambda j: j.update(outcome="RECOVERED_TRANSPORT"),
                     lambda j: j["attempts"][0].update(ordinal=True), lambda j: j["attempts"][0].update(state="STARTED"),
                     lambda j: j["attempts"][0].update(finished_at=None), lambda j: j["attempts"][0].update(truncated=True),
                     lambda j: j["attempts"][0].update(observed_bytes=0),
                     lambda j: j["attempts"].append(copy.deepcopy(j["attempts"][0])),
                     lambda j: j["attempts"][0]["body"].update(sha256="0" * 64)]
        for mutate in mutations:
            changed = copy.deepcopy(original)
            mutate(changed)
            path.write_bytes(contract.canonical(changed))
            row["acquisition"] = contract.reference(self.root, path)
            with self.subTest(mutation=mutate), self.assertRaises(ValueError):
                source_evidence.validate_observation(row, self.root, True)

    def test_both_independent_passes_and_nested_files_are_rechecked(self):
        # Structural acceptance controls only; these fabricated inputs claim no live proof.
        paths = {}
        for directory, mode in (("check", "check"), ("update", "update")):
            root = self.root / directory
            root.mkdir()
            body = root / "body"
            body.write_bytes(b"[]")
            row = source_evidence.fixture_observation({"url": URL, "status": 200, "headers": {},
                "body": contract.reference(root, body), "observed_at": 1.0}, root)
            journal_path = root / row["acquisition"]["path"]
            journal = json.loads(journal_path.read_text())
            journal["synthetic"] = False
            journal_path.write_bytes(contract.canonical(journal))
            row["acquisition"] = contract.reference(root, journal_path)
            report = {"schema_version": 1, "contract": "component-update-v1", "mode": mode,
                      "synthetic": False, "complete": True, "observations": [row]}
            for name in ("report.json", "independent-report.json"):
                path = root / name
                path.write_bytes(contract.canonical(report))
                paths[(mode, name)] = path
        proof_path = self.root / "signature.json"
        proof = {"check_report": contract.reference(self.root, paths[("check", "independent-report.json")])}
        proof_path.write_bytes(contract.canonical(proof))
        updater = {"signature_proof": contract.reference(self.root, proof_path),
                   "check_report": contract.reference(self.root, paths[("check", "report.json")]),
                   "report": contract.reference(self.root, paths[("update", "report.json")]),
                   "independent_report": contract.reference(self.root, paths[("update", "independent-report.json")])}
        source_evidence.validate_updater_journals(updater, self.root)
        for path in (paths[("check", "independent-report.json")], self.root / "check/body",
                     next((self.root / "check/fixture-acquisitions").glob("*.json")), self.root / "update/body"):
            original = path.read_bytes()
            path.write_bytes(original + b"changed")
            with self.subTest(path=path), self.assertRaises(ValueError):
                source_evidence.validate_updater_journals(updater, self.root)
            path.unlink()
            with self.subTest(missing=path), self.assertRaises(ValueError):
                source_evidence.validate_updater_journals(updater, self.root)
            path.write_bytes(original)
        proof["check_report"] = updater["independent_report"]
        proof_path.write_bytes(contract.canonical(proof))
        changed = {**updater, "signature_proof": contract.reference(self.root, proof_path)}
        with self.assertRaisesRegex(ValueError, "another invocation"):
            source_evidence.validate_updater_journals(changed, self.root)


class FakeRunner:
    def __init__(self):
        self.stopped = False
        self.commands = []

    def remaining(self, cap):
        contract.require(not self.stopped, "operation stopped")
        return cap


class Response(io.BytesIO):
    def __init__(self, data=b"[]", url="https://raw.githubusercontent.com/example/client/main/data.json", status=200):
        super().__init__(data)
        self.url, self.status, self.headers = url, status, {"Content-Type": "application/json"}


def tls_eof():
    incoming, outgoing = ssl.MemoryBIO(), ssl.MemoryBIO()
    connection = ssl.create_default_context().wrap_bio(incoming, outgoing, server_side=False, server_hostname="example.org")
    incoming.write_eof()
    try:
        connection.do_handshake()
    except ssl.SSLEOFError as error:
        return error
    raise AssertionError("MemoryBIO did not create the typed EOF control")


class SourceTransportTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.runner = FakeRunner()
        environment = patch.dict(os.environ, {"GH_TOKEN": "invented_default_source_test_token", "GITHUB_TOKEN": ""})
        environment.start()
        self.addCleanup(environment.stop)

    def acquisition(self, github=False, maximum=transport.MAX_BODY, runner=None):
        self.runner = runner or FakeRunner()
        directory = self.root / str(len(list(self.root.iterdir())))
        directory.mkdir()
        url = URL if github else "https://raw.githubusercontent.com/example/client/main/data.json"
        return transport.Acquisition(self.runner, url, directory, maximum=maximum)

    def test_exact_network_errors_recover_once_with_bound_attempts(self):
        for error in (TimeoutError(), urllib.error.URLError(TimeoutError()), tls_eof(), urllib.error.URLError(tls_eof())):
            acquisition = self.acquisition()
            with patch.object(acquisition.opener, "open", side_effect=[error, Response()]) as opened:
                row = acquisition.fetch()
            self.assertEqual(opened.call_count, 2)
            journal = source_evidence.validate_observation(row, acquisition.root, False)
            self.assertEqual(journal["outcome"], "RECOVERED_TRANSPORT")
            self.assertEqual([item["state"] for item in journal["attempts"]], ["FAILED", "SUCCEEDED"])
            self.assertEqual(self.runner.commands, [])

    def test_unknown_http_certificate_and_local_failures_do_not_retry(self):
        for error in (urllib.error.URLError("timed out"), ssl.SSLError("EOF"), ssl.SSLCertVerificationError(),
                      PermissionError(), ValueError("malformed source")):
            acquisition = self.acquisition()
            with patch.object(acquisition.opener, "open", side_effect=error) as opened:
                with self.assertRaises(type(error)):
                    acquisition.fetch()
            self.assertEqual(opened.call_count, 1)
            self.assertFalse(acquisition.journal["attempts"][0]["error"]["retryable"])
        acquisition = self.acquisition()
        with patch.object(acquisition.opener, "open", return_value=Response(status=503)) as opened:
            with self.assertRaises(ValueError):
                acquisition.fetch()
        self.assertEqual(opened.call_count, 1)
        self.assertEqual(acquisition.journal["attempts"][0]["status"], 503)

    def test_attempt_limit_preserves_both_failures(self):
        acquisition = self.acquisition()
        with patch.object(acquisition.opener, "open", side_effect=TimeoutError()) as opened:
            with self.assertRaises(TimeoutError):
                acquisition.fetch()
        self.assertEqual(opened.call_count, 2)
        self.assertEqual(acquisition.journal["outcome"], "FAILED")
        self.assertEqual([row["ordinal"] for row in acquisition.journal["attempts"]], [1, 2])

    def test_partial_bytes_are_retained_and_never_concatenated_with_success(self):
        error = urllib.error.URLError(TimeoutError())
        error.reason.partial = b"partial"
        failed = Response()
        failed.read = lambda amount: (_ for _ in ()).throw(error)
        acquisition = self.acquisition()
        with patch.object(acquisition.opener, "open", side_effect=[failed, Response(b"final")]):
            row = acquisition.fetch()
        journal = source_evidence.validate_observation(row, acquisition.root, False)
        partial = journal["attempts"][0]
        self.assertEqual(contract.verify_reference(acquisition.root, partial["body"]).read_bytes(), b"partial")
        self.assertEqual(partial["observed_bytes"], 7)
        self.assertEqual(contract.verify_reference(acquisition.root, row["body"]).read_bytes(), b"final")

    def test_oversized_partial_is_bounded_and_never_retried(self):
        error = TimeoutError()
        error.partial = b"123456789"
        failed = Response()
        failed.read = lambda amount: (_ for _ in ()).throw(error)
        acquisition = self.acquisition(maximum=4)
        with patch.object(acquisition.opener, "open", return_value=failed) as opened:
            with self.assertRaises(ValueError):
                acquisition.fetch()
        attempt = acquisition.journal["attempts"][0]
        self.assertEqual(opened.call_count, 1)
        self.assertEqual((attempt["observed_bytes"], attempt["truncated"], attempt["body"]["size_bytes"]), (9, True, 4))
        self.assertEqual(contract.verify_reference(acquisition.root, attempt["body"]).read_bytes(), b"1234")

    def test_authenticated_redirect_and_response_cannot_retain_credentials(self):
        token = "fixture-token-not-a-real-credential"
        for payload in (token.encode(), ''.join('%%%02X' % ord(char) for char in token).encode()):
            acquisition = self.acquisition()
            acquisition.headers = {"Authorization": "Bearer " + token}
            acquisition.secrets = [token.encode()]
            with patch.object(acquisition.opener, "open", return_value=Response(payload)) as opened:
                with self.assertRaises(ValueError):
                    acquisition.fetch()
            self.assertEqual(opened.call_count, 1)
            for path in acquisition.directory.iterdir():
                self.assertNotIn(token.encode(), path.read_bytes())
                self.assertNotIn(payload, path.read_bytes())
        request = transport.urllib.request.Request("https://registry-1.docker.io/v2/library/golang/tags/list",
                                                    headers={"Authorization": "Bearer " + token})
        policy = transport.RedirectPolicy()
        for destination in ("https://go.dev/other", request.full_url + "?echo=" + token):
            with self.subTest(destination=destination), self.assertRaises(ValueError):
                policy.redirect_request(request, None, 302, "redirect", {}, destination)

    def test_intent_and_failed_evidence_must_persist_before_dispatch(self):
        acquisition = self.acquisition()
        with patch.object(acquisition, "persist", side_effect=OSError("disk full")), patch.object(acquisition.opener, "open") as opened:
            with self.assertRaises(OSError):
                acquisition.fetch()
        opened.assert_not_called()
        acquisition = self.acquisition()
        original = acquisition.persist
        def persist():
            if acquisition.current["state"] == "FAILED":
                raise OSError("disk full")
            original()
        with patch.object(acquisition, "persist", side_effect=persist), patch.object(acquisition.opener, "open", side_effect=TimeoutError()) as opened:
            with self.assertRaises(OSError):
                acquisition.fetch()
        self.assertEqual(opened.call_count, 1)

    def test_stop_after_failed_persistence_forbids_second_request(self):
        acquisition = self.acquisition()
        original = acquisition.persist
        def persist():
            original()
            if acquisition.current["state"] == "FAILED":
                self.runner.stopped = True
        with patch.object(acquisition, "persist", side_effect=persist), patch.object(acquisition.opener, "open", side_effect=TimeoutError()) as opened:
            with self.assertRaises(transport.RequestDeadline):
                acquisition.fetch()
        self.assertEqual(opened.call_count, 1)

    def test_total_deadline_interrupts_blocking_reads_and_restores_alarm(self):
        before_handler = signal.getsignal(signal.SIGALRM)
        response = Response()
        def slow_read(amount):
            time.sleep(0.2)
            return b""
        response.read = slow_read
        with patch.object(transport, "REQUEST_SECONDS", 0.05):
            acquisition = self.acquisition()
        started = time.monotonic()
        with patch.object(acquisition.opener, "open", return_value=response) as opened:
            with self.assertRaises(transport.RequestDeadline):
                acquisition.fetch()
        self.assertLess(time.monotonic() - started, 0.18)
        self.assertEqual(opened.call_count, 1)
        self.assertEqual(signal.getsignal(signal.SIGALRM), before_handler)
        self.assertEqual(signal.getitimer(signal.ITIMER_REAL), (0.0, 0.0))

    def test_earlier_caller_alarm_is_delivered_and_restored(self):
        previous = signal.getsignal(signal.SIGALRM)
        calls = []
        signal.signal(signal.SIGALRM, lambda number, frame: calls.append(number))
        signal.setitimer(signal.ITIMER_REAL, 0.02)
        try:
            with self.assertRaises(transport.CallerInterrupted), transport.request_alarm(time.monotonic() + 0.15):
                time.sleep(0.06)
            self.assertEqual(calls, [signal.SIGALRM])
            self.assertEqual(signal.getitimer(signal.ITIMER_REAL), (0.0, 0.0))
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, previous)

    def test_caller_timeout_is_not_network_recovery_and_fences_later_dispatch(self):
        previous = signal.getsignal(signal.SIGALRM)
        calls = []
        def interrupted(number, frame):
            calls.append(number)
            raise TimeoutError("caller cancellation")
        acquisition = self.acquisition()
        signal.signal(signal.SIGALRM, interrupted)
        signal.setitimer(signal.ITIMER_REAL, 0.02)
        def blocked_open(*args, **kwargs):
            time.sleep(0.2)
            return Response()
        try:
            with patch.object(acquisition.opener, "open", side_effect=blocked_open) as opened:
                with self.assertRaisesRegex(TimeoutError, "caller cancellation"):
                    acquisition.fetch()
            self.assertEqual(opened.call_count, 1)
            self.assertEqual(calls, [signal.SIGALRM])
            self.assertEqual(signal.getsignal(signal.SIGALRM), interrupted)
            self.assertEqual(acquisition.journal["attempts"][0]["error"]["code"], "REQUEST_DEADLINE")
            with self.assertRaisesRegex(ValueError, "fenced"):
                self.acquisition(runner=self.runner)
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, previous)

    def test_terminal_body_persistence_failure_fences_other_requests(self):
        acquisition = self.acquisition()
        reference = contract.reference
        def fail_body(root, path):
            if str(path).endswith(".body"):
                raise OSError("body persistence failed")
            return reference(root, path)
        with patch.object(acquisition.opener, "open", return_value=Response()) as opened, \
                patch.object(contract, "reference", side_effect=fail_body):
            with self.assertRaises(OSError):
                acquisition.fetch()
        self.assertEqual(opened.call_count, 1)
        with self.assertRaisesRegex(ValueError, "fenced"):
            self.acquisition(runner=self.runner)

    def test_post_guard_alarm_cannot_admit_later_source_or_native_work(self):
        previous = signal.getsignal(signal.SIGALRM)
        acquisition = self.acquisition()
        def caller(number, frame):
            raise TimeoutError("caller post-guard deadline")
        def delayed_validation(*args):
            self.assertEqual(signal.getsignal(signal.SIGALRM), caller)
            time.sleep(0.15)
        signal.signal(signal.SIGALRM, caller)
        signal.setitimer(signal.ITIMER_REAL, 0.05)
        try:
            with patch.object(acquisition.opener, "open", return_value=Response()) as opened, \
                    patch.object(source_evidence, "validate_observation", side_effect=delayed_validation):
                with self.assertRaisesRegex(TimeoutError, "post-guard"):
                    acquisition.fetch()
            self.assertEqual(opened.call_count, 1)
            with self.assertRaises(ValueError):
                self.acquisition(runner=self.runner)
            with self.assertRaisesRegex(ValueError, "forbids more work"):
                transport.build_execution.Runner.remaining(self.runner, 30)
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, previous)

    def test_consumed_or_rearmed_caller_timer_does_not_reset_its_bound(self):
        previous = signal.getsignal(signal.SIGALRM)
        calls = []
        signal.signal(signal.SIGALRM, lambda number, frame: calls.append(number))
        signal.setitimer(signal.ITIMER_REAL, 0.06)
        try:
            acquisition = self.acquisition()
            with patch.object(acquisition.opener, "open", return_value=Response()):
                acquisition.fetch()
            runner = self.runner
            inherited = runner.source_caller_deadline
            time.sleep(0.08)
            self.assertEqual(calls, [signal.SIGALRM])
            signal.setitimer(signal.ITIMER_REAL, 2)
            with self.assertRaises(transport.RequestDeadline):
                self.acquisition(runner=runner)
            self.assertEqual(runner.source_caller_deadline, inherited)
            runner.plan = {"limits": {"command_seconds": 30}}
            runner.deadline = time.time() + 30
            runner.monotonic_deadline = time.monotonic() + 30
            runner.request = {"stop_file": str(self.root / "absent-stop")}
            with self.assertRaisesRegex(ValueError, "deadline or stop"):
                transport.build_execution.Runner.remaining(runner, 30)
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, previous)

    def gh_process(self, scripts):
        original = subprocess.Popen
        processes = []
        def launch(argv, **kwargs):
            process = original([sys.executable, "-I", "-c", scripts.pop(0)], **kwargs)
            processes.append(process)
            return process
        return launch, processes

    def test_gh_environment_credentials_never_enter_retained_outputs(self):
        token = "invented_gh_source_secret_928"
        encoded = "".join("%%%02x" % value for value in token.encode())
        for source in ("GH_TOKEN", "GITHUB_TOKEN"):
            for location, echo in (("body", token), ("body", encoded), ("header", token), ("stderr", encoded)):
                with self.subTest(source=source, location=location):
                    output = "HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n"
                    output += "X-Trace: " + echo + "\r\n" if location == "header" else ""
                    output += "\r\n" + (echo if location == "body" else "[]")
                    error = echo if location == "stderr" else ""
                    launch, processes = self.gh_process(["import sys;sys.stdout.write(" + repr(output) + ");sys.stderr.write(" + repr(error) + ")"])
                    acquisition = self.acquisition(github=True)
                    with patch.dict(os.environ, {"GH_TOKEN": "", "GITHUB_TOKEN": "", source: token}), patch.object(transport.subprocess, "Popen", side_effect=launch):
                        with self.assertRaisesRegex(ValueError, "credential"):
                            acquisition.fetch()
                    journal = json.loads(acquisition.path.read_text())
                    self.assertEqual(journal["outcome"], "FAILED")
                    self.assertFalse(journal["attempts"][0]["error"]["retryable"])
                    self.assertTrue(self.runner.source_acquisition_blocked)
                    self.assertEqual(len(processes), 1)
                    self.assertTrue(all(p.poll() is not None and p.stdout.closed and p.stderr.closed for p in processes))
                    for path in acquisition.root.rglob("*"):
                        if path.is_file():
                            self.assertFalse(transport.credential_echo(path.read_bytes(), [token.encode()]), str(path))

    def test_gh_helper_credential_is_private_and_pinned_for_the_request(self):
        token = "invented_helper_source_secret_745"
        for echo in (False, True):
            with self.subTest(echo=echo):
                output = "HTTP/2.0 200 OK\r\n\r\n" + (token if echo else "[]")
                launch, processes = self.gh_process(["print(" + repr(token) + ")", "import sys;sys.stdout.write(" + repr(output) + ")"])
                calls = []
                def record(argv, **kwargs):
                    calls.append((argv, kwargs.get("env")))
                    return launch(argv, **kwargs)
                acquisition = self.acquisition(github=True)
                with patch.dict(os.environ, {"GH_TOKEN": "", "GITHUB_TOKEN": ""}), patch.object(transport.subprocess, "Popen", side_effect=record):
                    if echo:
                        with self.assertRaisesRegex(ValueError, "credential"):
                            acquisition.fetch()
                    else:
                        row = acquisition.fetch()
                        source_evidence.validate_observation(row, acquisition.root, False)
                self.assertEqual(calls[0][0], ["gh", "auth", "token", "--hostname", "github.com"])
                self.assertEqual(calls[1][1]["GH_TOKEN"], token)
                self.assertEqual(len(processes), 2)
                self.assertTrue(all(p.poll() is not None and p.stdout.closed and p.stderr.closed for p in processes))
                for path in acquisition.root.rglob("*"):
                    if path.is_file():
                        self.assertNotIn(token.encode(), path.read_bytes())

    def test_gh_encoded_split_reads_cannot_write_credentials(self):
        token = "invented_split_gh_credential_134"
        encoded = "".join("%%%02x" % value for value in token.encode())
        encoded = urllib.parse.quote(urllib.parse.quote(encoded, safe=""), safe="")
        for stream in ("stdout", "stderr"):
            with self.subTest(stream=stream):
                output = "HTTP/2.0 200 OK\r\n\r\n" + "x" * 1000 + encoded
                launch, processes = self.gh_process(["import sys;sys." + stream + ".write(" + repr(output) + ")"])
                acquisition = self.acquisition(github=True)
                read = os.read
                with patch.dict(os.environ, {"GH_TOKEN": token}), patch.object(transport.subprocess, "Popen", side_effect=launch), patch.object(transport.os, "read", side_effect=lambda fd, count: read(fd, min(count, 7))):
                    with self.assertRaisesRegex(ValueError, "credential"):
                        acquisition.fetch()
                attempt = json.loads(acquisition.path.read_text())["attempts"][0]
                self.assertEqual(attempt["state"], "FAILED")
                self.assertEqual(attempt["error"]["code"], "REJECTED")
                for field in ("stdout", "stderr"):
                    self.assertEqual(contract.verify_reference(acquisition.root, attempt["process"][field]).read_bytes(), b"")
                self.assertTrue(all(p.poll() is not None and p.stdout.closed and p.stderr.closed for p in processes))

    def test_gh_overflow_retains_safe_raw_prefix_and_exact_raw_accounting(self):
        for field in ("stdout", "stderr"):
            with self.subTest(field=field):
                acquisition = self.acquisition(github=True, maximum=8)
                bound = 8 + source_evidence.MAX_HEADERS if field == "stdout" else source_evidence.MAX_STDERR
                payload = (b"HTTP/2.0 200 OK\r\n\r\n" if field == "stdout" else b"") + b"x" * (100000 if field == "stdout" else bound + 10000)
                script = "import sys;sys." + field + ".buffer.write(" + repr(payload[:19] if field == "stdout" else b"") + "+b'x'*" + str(len(payload) - (19 if field == "stdout" else 0)) + ");sys." + field + ".flush()"
                launch, processes = self.gh_process([script])
                observed = {"stdout": bytearray(), "stderr": bytearray()}
                read = os.read
                def observe(fd, count):
                    block = read(fd, count)
                    if processes:
                        for name in observed:
                            pipe = getattr(processes[-1], name)
                            if not pipe.closed and fd == pipe.fileno():
                                observed[name].extend(block)
                    return block
                with patch.object(transport.subprocess, "Popen", side_effect=launch), patch.object(transport.os, "read", side_effect=observe):
                    with self.assertRaisesRegex(ValueError, "exceeds its bound"):
                        acquisition.fetch()
                journal = json.loads(acquisition.path.read_text())
                attempt = journal["attempts"][0]
                proof = attempt["process"]
                self.assertEqual(attempt["state"], "FAILED")
                self.assertFalse(attempt["error"]["retryable"])
                self.assertEqual((attempt["observed_bytes"], attempt["truncated"], attempt["body"]), (0, False, None))
                self.assertEqual(contract.verify_reference(acquisition.root, proof[field]).read_bytes(), bytes(observed[field][:bound]))
                self.assertEqual(proof[field]["size_bytes"], bound)
                for name in observed:
                    self.assertEqual(proof[name + "_observed_bytes"], len(observed[name]))
                    self.assertEqual(proof[name + "_truncated"], len(observed[name]) > proof[name]["size_bytes"])
                source_evidence.validate_process(proof, attempt, journal, acquisition.root)
                self.assertEqual(len(processes), 1)
                self.assertTrue(all(p.poll() is not None and p.stdout.closed and p.stderr.closed for p in processes))

    def test_gh_raw_prefix_bound_on_first_and_later_overflow_blocks(self):
        for field in ("stdout", "stderr"):
            for prefix in (b"", b"abc"):
                with self.subTest(field=field, prefix=prefix):
                    acquisition = self.acquisition(github=True)
                    acquisition.current = {"process": {field + "_observed_bytes": 0, field + "_truncated": False}}
                    path = acquisition.root / "raw-output"
                    with path.open("wb") as output:
                        state = (output, 8, bytearray(), field)
                        if prefix:
                            acquisition.gh_output(state, prefix)
                        with self.assertRaisesRegex(ValueError, "exceeds its bound"):
                            acquisition.gh_output(state, b"123456789012")
                    self.assertEqual(path.read_bytes(), (prefix + b"123456789012")[:8])
                    self.assertEqual(acquisition.current["process"][field + "_observed_bytes"], len(prefix) + 12)
                    self.assertTrue(acquisition.current["process"][field + "_truncated"])

    def test_gh_raw_accounting_is_closed_bounded_and_required_for_success(self):
        launch, processes = self.gh_process(["import sys;sys.stdout.write('HTTP/2.0 200 OK\\r\\n\\r\\n[]')"])
        acquisition = self.acquisition(github=True)
        with patch.object(transport.subprocess, "Popen", side_effect=launch):
            row = acquisition.fetch()
        journal = source_evidence.validate_observation(row, acquisition.root, False)
        attempt = journal["attempts"][0]
        proof = attempt["process"]
        for field in ("stdout", "stderr"):
            self.assertEqual(proof[field + "_observed_bytes"], proof[field]["size_bytes"])
            self.assertFalse(proof[field + "_truncated"])
            mutations = [lambda p: p.pop(field + "_observed_bytes"),
                         lambda p: p.update({field + "_observed_bytes": True}),
                         lambda p: p.update({field + "_observed_bytes": -1}),
                         lambda p: p.update({field + "_observed_bytes": 1 << 63, field + "_truncated": True}),
                         lambda p: p.update({field + "_truncated": True}),
                         lambda p: p.update({field + "_observed_bytes": p[field]["size_bytes"] + 1, field + "_truncated": True})]
            for change in mutations:
                altered = copy.deepcopy(journal)
                change(altered["attempts"][0]["process"])
                acquisition.path.write_bytes(contract.canonical(altered))
                changed_row = {**row, "acquisition": contract.reference(acquisition.root, acquisition.path)}
                with self.subTest(field=field, mutation=change), self.assertRaises(ValueError):
                    source_evidence.validate_observation(changed_row, acquisition.root, False)
        self.assertTrue(all(p.poll() is not None for p in processes))

    def test_gh_raw_exact_bound_does_not_mark_omission(self):
        for field in ("stdout", "stderr"):
            with self.subTest(field=field):
                acquisition = self.acquisition(github=True)
                acquisition.secrets = [b"invented-boundary-credential"]
                acquisition.current = {"process": {field + "_observed_bytes": 0, field + "_truncated": False}}
                path = acquisition.root / "raw-exact-bound"
                with path.open("wb") as output:
                    state = (output, 8, bytearray(), field)
                    acquisition.gh_output(state, b"123")
                    acquisition.gh_output(state, b"45678")
                    acquisition.gh_output(state, b"", final=True)
                self.assertEqual(path.read_bytes(), b"12345678")
                self.assertEqual(acquisition.current["process"][field + "_observed_bytes"], 8)
                self.assertFalse(acquisition.current["process"][field + "_truncated"])

    def test_gh_private_helper_failure_is_bounded_clean_and_terminal(self):
        for script, seconds in (("print('x'*100000)", 10), ("import time;time.sleep(10)", 0.1), ("raise SystemExit(1)", 10)):
            with self.subTest(script=script):
                with patch.object(transport, "REQUEST_SECONDS", seconds):
                    acquisition = self.acquisition(github=True)
                launch, processes = self.gh_process([script])
                with patch.dict(os.environ, {"GH_TOKEN": "", "GITHUB_TOKEN": ""}), patch.object(transport.subprocess, "Popen", side_effect=launch):
                    with self.assertRaises((ValueError, transport.RequestDeadline, subprocess.TimeoutExpired)):
                        acquisition.fetch()
                self.assertEqual(len(processes), 1)
                self.assertTrue(all(p.poll() is not None and p.stdout.closed and p.stderr.closed for p in processes))
                journal = json.loads(acquisition.path.read_text())
                self.assertEqual(journal["outcome"], "FAILED")
                self.assertFalse(journal["attempts"][0]["error"]["retryable"])
                self.assertTrue(self.runner.source_acquisition_blocked)
                self.assertEqual(list(acquisition.directory.glob("*.stdout")), [])
                self.assertEqual(list(acquisition.directory.glob("*.stderr")), [])

    def test_incomplete_http_read_preserves_prefix_and_partial_without_retry(self):
        for partial, maximum in ((b"available-tail", 100), (b"x" * 100, 12)):
            with self.subTest(maximum=maximum):
                acquisition = self.acquisition(maximum=maximum)
                response = Response()
                with patch.object(response, "read", side_effect=[b"prefix", http.client.IncompleteRead(partial, 1000)]), patch.object(acquisition.opener, "open", return_value=response) as opened:
                    with self.assertRaises((http.client.IncompleteRead, ValueError)):
                        acquisition.fetch()
                journal = json.loads(acquisition.path.read_text())
                attempt = journal["attempts"][0]
                self.assertEqual(journal["outcome"], "FAILED")
                self.assertEqual(attempt["state"], "FAILED")
                self.assertEqual(attempt["observed_bytes"], 6 + len(partial))
                self.assertEqual(attempt["truncated"], 6 + len(partial) > maximum)
                self.assertFalse(attempt["error"]["retryable"])
                self.assertEqual(contract.verify_reference(acquisition.root, attempt["body"]).read_bytes(), (b"prefix" + partial)[:maximum])
                self.assertEqual(opened.call_count, 1)
                self.assertTrue(self.runner.source_acquisition_blocked)
                self.assertTrue(response.closed)

    def test_incomplete_http_read_cannot_continue_after_terminal_persistence_failure(self):
        acquisition = self.acquisition()
        response = Response()
        persist = acquisition.persist
        def fail_terminal():
            if acquisition.journal["outcome"] == "FAILED":
                raise OSError("injected terminal journal failure")
            persist()
        with patch.object(response, "read", side_effect=[b"prefix", http.client.IncompleteRead(b"partial", 10)]), patch.object(acquisition.opener, "open", return_value=response) as opened, patch.object(acquisition, "persist", side_effect=fail_terminal):
            with self.assertRaisesRegex(OSError, "terminal journal"):
                acquisition.fetch()
        self.assertEqual(opened.call_count, 1)
        self.assertTrue(response.closed)
        self.assertTrue(self.runner.source_acquisition_blocked)
        with self.assertRaisesRegex(ValueError, "fenced"):
            transport.Acquisition(self.runner, URL, acquisition.root)

    def test_exact_gh_eof_recovers_with_raw_process_evidence(self):
        error = 'Get "' + URL + '": EOF\n'
        scripts = ["import sys;sys.stderr.write(" + repr(error) + ");sys.exit(1)",
                   "import sys;sys.stdout.write('HTTP/2.0 200 OK\\r\\nContent-Type: application/json\\r\\n\\r\\n[]')"]
        launch, processes = self.gh_process(scripts)
        acquisition = self.acquisition(github=True)
        with patch.object(transport.subprocess, "Popen", side_effect=launch):
            row = acquisition.fetch()
        journal = source_evidence.validate_observation(row, acquisition.root, False)
        self.assertEqual(journal["outcome"], "RECOVERED_TRANSPORT")
        self.assertEqual(journal["attempts"][0]["error"]["code"], "GH_EOF")
        self.assertEqual(self.runner.commands, [])
        self.assertTrue(all(process.poll() is not None for process in processes))
        self.assertTrue(all(process.stdout.closed and process.stderr.closed for process in processes))
        original = copy.deepcopy(journal)
        for mutation in (lambda j: j["attempts"][0]["process"].update(exit_code=0),
                         lambda j: j["attempts"][0]["process"]["argv"].__setitem__(-1, URL + "&page=2"),
                          lambda j: j["attempts"][0]["process"].update(stdout_observed_bytes=1, stdout_truncated=True),
                         lambda j: j["attempts"][1]["body"].update(sha256="0" * 64)):
            changed = copy.deepcopy(original)
            mutation(changed)
            acquisition.path.write_bytes(contract.canonical(changed))
            row["acquisition"] = contract.reference(acquisition.root, acquisition.path)
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                source_evidence.validate_observation(row, acquisition.root, False)

    def test_gh_other_failures_and_total_deadline_do_not_retry(self):
        for diagnostic in ("EOF\n", 'Get "https://api.github.com/other": EOF\n', "HTTP 401: Bad credentials\n"):
            launch, processes = self.gh_process(["import sys;sys.stderr.write(" + repr(diagnostic) + ");sys.exit(1)"])
            acquisition = self.acquisition(github=True)
            with patch.object(transport.subprocess, "Popen", side_effect=launch):
                with self.assertRaises(transport.GhAPIError):
                    acquisition.fetch()
            self.assertEqual(len(processes), 1)
            self.assertIsNotNone(processes[0].poll())
        with patch.object(transport, "REQUEST_SECONDS", 0.1):
            acquisition = self.acquisition(github=True)
        launch, processes = self.gh_process(["import time;time.sleep(10)"])
        with patch.object(transport.subprocess, "Popen", side_effect=launch):
            with self.assertRaises(transport.RequestDeadline):
                acquisition.fetch()
        self.assertEqual(len(processes), 1)
        self.assertIsNotNone(processes[0].poll())
        self.assertTrue(processes[0].stdout.closed and processes[0].stderr.closed)


if __name__ == "__main__":
    unittest.main()
