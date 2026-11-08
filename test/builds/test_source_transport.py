"""Bounded source GET recovery with invented responses and no network access."""

import http.client
import io
import json
import os
from pathlib import Path
import signal
import ssl
import sys
import tempfile
import time
import unittest
from unittest import mock
import urllib.error

sys.path[:0] = [str(Path(__file__).resolve().parents[2] / "scripts/versioning"),
               str(Path(__file__).resolve().parents[2] / "build/native")]
import sources
from core import ref

URL = "https://go.dev/dl/?mode=json"


class Response(io.BytesIO):
    status = 200
    headers = {"content-type": "application/json"}


def eof():
    incoming, outgoing = ssl.MemoryBIO(), ssl.MemoryBIO()
    client = ssl.create_default_context().wrap_bio(incoming, outgoing, server_hostname="example.invalid")
    incoming.write_eof()
    try:
        client.do_handshake()
    except ssl.SSLEOFError as error:
        assert error.reason == "UNEXPECTED_EOF_WHILE_READING"
        return error
    raise AssertionError("Expected a real typed TLS EOF")


class SourceTransportTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.transport = sources.Transport(self.root / "sources")

    def journal(self):
        return json.loads(ref(self.root, self.transport.rows[URL]["acquisition"]).read_bytes())

    def test_positive_typed_failures_retry_once_and_bind_success(self):
        for index, failure in enumerate((TimeoutError(), urllib.error.URLError(TimeoutError()),
                                         eof(), urllib.error.URLError(eof()))):
            with self.subTest(failure=type(failure).__name__):
                self.transport = sources.Transport(self.root / ("case-" + str(index)))
                with mock.patch.object(self.transport.opener, "open", side_effect=[failure, Response(b"[]")]) as opened:
                    self.assertEqual(self.transport.data(URL), b"[]")
                self.assertEqual(opened.call_count, 2)
                value = self.journal()
                self.assertEqual(value["outcome"], "RECOVERED_TRANSPORT")
                self.assertEqual([a["state"] for a in value["attempts"]], ["FAILED", "SUCCEEDED"])
                self.assertTrue(value["attempts"][0]["error"]["retryable"])
                row, last = self.transport.rows[URL], value["attempts"][-1]
                for key in ("status", "headers", "body", "observed_at"):
                    self.assertEqual(row[key], last[key])

    def test_third_attempt_is_never_dispatched(self):
        with mock.patch.object(self.transport.opener, "open", side_effect=TimeoutError()) as opened:
            with self.assertRaises(ValueError):
                self.transport.fetch(URL)
        self.assertEqual(opened.call_count, 2)
        self.assertEqual(self.journal()["outcome"], "FAILED")
        self.assertEqual(len(self.journal()["attempts"]), 2)

    def test_unclassified_certificate_http_parse_and_local_errors_never_retry(self):
        failures = [urllib.error.URLError("unknown cause"), ConnectionResetError(),
                    urllib.error.URLError(ssl.SSLCertVerificationError(1, "certificate failed")),
                    ssl.SSLError(1, "unknown TLS failure"), ValueError("invalid response"),
                    OSError("local failure")]
        for index, failure in enumerate(failures):
            with self.subTest(failure=type(failure).__name__):
                self.transport = sources.Transport(self.root / ("failure-" + str(index)))
                with mock.patch.object(self.transport.opener, "open", side_effect=failure) as opened:
                    with self.assertRaises(ValueError):
                        self.transport.fetch(URL)
                self.assertEqual(opened.call_count, 1)
                self.assertFalse(self.journal()["attempts"][0]["error"]["retryable"])
        for status in (401, 403, 429, 500, 503):
            self.transport = sources.Transport(self.root / ("http-" + str(status)))
            response = Response(b"failure")
            response.status = status
            with mock.patch.object(self.transport.opener, "open", return_value=response) as opened:
                with self.assertRaises(ValueError):
                    self.transport.fetch(URL)
            self.assertEqual(opened.call_count, 1)
        self.transport = sources.Transport(self.root / "parse")
        with mock.patch.object(self.transport.opener, "open", return_value=Response(b"not JSON")) as opened:
            with self.assertRaises(ValueError):
                self.transport.json(URL)
        self.assertEqual(opened.call_count, 1)
        self.assertEqual(self.journal()["outcome"], "FIRST_ATTEMPT_SUCCESS")

    def test_intent_is_retained_before_network_dispatch(self):
        def opened(*args, **kwargs):
            value = json.loads(next(self.transport.directory.rglob("journal.json")).read_bytes())
            self.assertEqual(value["attempts"][-1]["state"], "STARTED")
            return Response(b"[]")
        with mock.patch.object(self.transport.opener, "open", opened):
            self.transport.fetch(URL)
        self.assertEqual(self.journal()["outcome"], "FIRST_ATTEMPT_SUCCESS")

    def test_failed_bytes_and_wrapped_partial_remain_separate_from_success(self):
        failure = eof()
        failure.partial = b"partial"
        wrapped = urllib.error.URLError(failure)
        wrapped.partial = b"partial"
        class Broken(Response):
            def read(self, size):
                raise wrapped
        with mock.patch.object(self.transport.opener, "open", side_effect=[Broken(), Response(b"[]")]):
            self.transport.fetch(URL)
        first, second = self.journal()["attempts"]
        self.assertEqual(ref(self.root, first["body"]).read_bytes(), b"partial")
        self.assertEqual(first["observed_bytes"], 7)
        self.assertEqual(ref(self.root, second["body"]).read_bytes(), b"[]")
        self.assertNotEqual(first["body"]["path"], second["body"]["path"])

    def test_oversized_and_incomplete_reads_preserve_bounded_bytes_without_retry(self):
        for index, partial in enumerate((False, True)):
            self.transport = sources.Transport(self.root / ("partial-" + str(index)))
            class Broken(Response):
                returned = False
                def read(self, size):
                    if partial:
                        raise http.client.IncompleteRead(b"abcdefgh", 10)
                    if self.returned:
                        return b""
                    self.returned = True
                    return b"abcdefgh"
            with mock.patch.object(sources, "BODY_LIMIT", 5), \
                 mock.patch.object(self.transport.opener, "open", return_value=Broken()) as opened:
                with self.assertRaises(ValueError):
                    self.transport.fetch(URL)
            self.assertEqual(opened.call_count, 1)
            attempt = self.journal()["attempts"][0]
            self.assertEqual(ref(self.root, attempt["body"]).read_bytes(), b"abcde")
            self.assertEqual(attempt["observed_bytes"], 8)
            self.assertTrue(attempt["truncated"])

    def test_body_read_has_total_deadline_and_restores_handlers(self):
        class Slow(Response):
            def read(self, size):
                time.sleep(0.3)
                return b"[]"
        old = signal.getsignal(signal.SIGALRM)
        stop_handlers = [signal.getsignal(signum) for signum in (signal.SIGTERM, signal.SIGINT)]
        started = time.monotonic()
        with mock.patch.object(sources, "REQUEST_SECONDS", 0.04), \
             mock.patch.object(self.transport.opener, "open", return_value=Slow()) as opened:
            with self.assertRaises(ValueError):
                self.transport.fetch(URL)
        self.assertLess(time.monotonic() - started, 0.25)
        self.assertEqual(opened.call_count, 1)
        self.assertEqual(self.journal()["attempts"][0]["error"]["code"], "REQUEST_DEADLINE")
        self.assertEqual(signal.getsignal(signal.SIGALRM), old)
        self.assertEqual([signal.getsignal(s) for s in (signal.SIGTERM, signal.SIGINT)], stop_handlers)

    def test_no_dispatch_after_journal_delay_exhausts_request(self):
        write = self.transport.write_journal
        def delayed(*args):
            result = write(*args)
            time.sleep(0.04)
            return result
        with mock.patch.object(sources, "REQUEST_SECONDS", 0.02), \
             mock.patch.object(self.transport, "write_journal", delayed), \
             mock.patch.object(self.transport.opener, "open") as opened:
            with self.assertRaises(ValueError):
                self.transport.fetch(URL)
        opened.assert_not_called()

    def test_persistence_timeout_never_becomes_network_retry(self):
        with mock.patch.object(os, "fsync", side_effect=TimeoutError("disk timeout")), \
             mock.patch.object(self.transport.opener, "open") as opened:
            with self.assertRaises(OSError):
                self.transport.fetch(URL)
        opened.assert_not_called()

    def test_failed_attempt_journal_persistence_blocks_all_later_dispatch(self):
        original = self.transport.write_journal
        def persist(path, journal, previous):
            if journal["attempts"][-1]["state"] == "FAILED":
                raise TimeoutError("local persistence timeout")
            return original(path, journal, previous)
        with mock.patch.object(self.transport, "write_journal", persist), \
             mock.patch.object(self.transport.opener, "open", side_effect=TimeoutError()) as opened:
            with self.assertRaises(OSError):
                self.transport.fetch(URL)
            with self.assertRaisesRegex(ValueError, "persistence failed"):
                self.transport.fetch(URL + "&page=2")
        self.assertEqual(opened.call_count, 1)
        intent = json.loads(next(self.transport.directory.rglob("journal.json")).read_bytes())
        self.assertEqual(intent["attempts"][0]["state"], "STARTED")

    def test_mutated_intent_is_not_rehashed_into_success(self):
        def opened(*args, **kwargs):
            next(self.transport.directory.rglob("journal.json")).write_bytes(b"{}")
            return Response(b"[]")
        with mock.patch.object(self.transport.opener, "open", opened) as network:
            with self.assertRaisesRegex(ValueError, "changed"):
                self.transport.fetch(URL)
            self.assertNotIn(URL, self.transport.rows)
            with self.assertRaisesRegex(ValueError, "persistence failed"):
                self.transport.fetch(URL + "&page=2")

    def test_second_attempt_keeps_original_deadline(self):
        deadlines = []
        def opened(*args, **kwargs):
            deadlines.append(self.transport.request_deadline)
            if len(deadlines) == 1:
                time.sleep(0.02)
                raise TimeoutError()
            time.sleep(0.3)
            return Response(b"[]")
        started = time.monotonic()
        with mock.patch.object(sources, "REQUEST_SECONDS", 0.06), \
             mock.patch.object(self.transport.opener, "open", opened):
            with self.assertRaises(ValueError):
                self.transport.fetch(URL)
        self.assertEqual(len(deadlines), 2)
        self.assertEqual(deadlines[0], deadlines[1])
        self.assertLess(time.monotonic() - started, 0.25)
        self.assertEqual(self.journal()["attempts"][-1]["error"]["code"], "REQUEST_DEADLINE")

    def test_outer_timeout_handler_is_not_network_timeout(self):
        old = signal.getsignal(signal.SIGALRM)
        def outer(*args):
            raise TimeoutError("outer operation deadline")
        signal.signal(signal.SIGALRM, outer)
        signal.setitimer(signal.ITIMER_REAL, 0.04)
        try:
            with mock.patch.object(self.transport.opener, "open", side_effect=lambda *a, **k: time.sleep(0.3)) as opened:
                with self.assertRaises(ValueError):
                    self.transport.fetch(URL)
            self.assertEqual(opened.call_count, 1)
            self.assertEqual(self.journal()["attempts"][0]["error"]["code"], "REQUEST_DEADLINE")
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, old)

    def test_expired_outer_alarm_blocks_later_url_dispatch(self):
        previous = signal.getsignal(signal.SIGALRM)
        calls = []
        def outer(*args):
            calls.append("expired")
            raise TimeoutError("outer operation deadline")
        def opened(request, **kwargs):
            if request.full_url == URL:
                time.sleep(0.2)
            return Response(b"[]")
        signal.signal(signal.SIGALRM, outer)
        signal.setitimer(signal.ITIMER_REAL, 0.03)
        try:
            with mock.patch.object(self.transport.opener, "open", side_effect=opened) as network:
                with self.assertRaises(ValueError):
                    self.transport.fetch(URL)
                self.assertEqual(calls, ["expired"])
                self.assertEqual(signal.getitimer(signal.ITIMER_REAL), (0, 0))
                self.assertIs(signal.getsignal(signal.SIGALRM), outer)
                with self.assertRaisesRegex(ValueError, "outer.*deadline"):
                    self.transport.fetch(URL + "&page=2")
                self.assertEqual(network.call_count, 1)
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, previous)
        self.assertEqual(self.journal()["attempts"][0]["error"]["code"], "REQUEST_DEADLINE")

    def test_terminal_diagnostic_write_or_reference_failure_blocks_later_url(self):
        for phase in ("write", "reference"):
            with self.subTest(phase=phase):
                self.transport = sources.Transport(self.root / ("terminal-" + phase))
                write, describe = Path.write_bytes, sources.describe
                def fail_write(path, payload):
                    if path.name == "failure.body":
                        raise OSError("terminal diagnostic write failed")
                    return write(path, payload)
                def fail_reference(root, path):
                    if Path(path).name == "failure.body":
                        raise OSError("terminal diagnostic reference failed")
                    return describe(root, path)
                def opened(request, **kwargs):
                    response = Response(b"temporarily unavailable" if request.full_url == URL else b"[]")
                    response.status = 503 if request.full_url == URL else 200
                    return response
                with mock.patch.object(self.transport.opener, "open", side_effect=opened) as network, \
                     mock.patch.object(Path, "write_bytes", fail_write if phase == "write" else write), \
                     mock.patch.object(sources, "describe", fail_reference if phase == "reference" else describe):
                    with self.assertRaises(OSError):
                        self.transport.fetch(URL)
                    with self.assertRaisesRegex(ValueError, "persistence failed"):
                        self.transport.fetch(URL + "&page=2")
                    self.assertTrue(self.transport.persistence_failed)
                self.assertEqual(network.call_count, 1)
                self.assertNotIn(URL, self.transport.rows)
                journal = json.loads(next(self.transport.directory.rglob("journal.json")).read_bytes())
                self.assertEqual(journal["outcome"], "FAILED")
                self.assertEqual(journal["attempts"][0]["status"], 503)
                self.assertEqual(ref(self.root, journal["attempts"][0]["body"]).read_bytes(), b"temporarily unavailable")

    def test_normal_and_exception_exit_restore_remaining_outer_timer(self):
        old = signal.getsignal(signal.SIGALRM)
        def outer(*args):
            raise AssertionError("Outer timer unexpectedly expired")
        signal.signal(signal.SIGALRM, outer)
        try:
            for fail in (False, True):
                signal.setitimer(signal.ITIMER_REAL, 2)
                try:
                    with sources.request_timer(time.monotonic() + 1):
                        time.sleep(0.02)
                        if fail:
                            raise ValueError("test exception")
                except ValueError:
                    self.assertTrue(fail)
                remaining, interval = signal.getitimer(signal.ITIMER_REAL)
                self.assertGreater(remaining, 1)
                self.assertLess(remaining, 1.99)
                self.assertEqual(interval, 0)
                self.assertIs(signal.getsignal(signal.SIGALRM), outer)
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, old)

    def test_guard_installation_failure_is_explicit_and_restores_handler(self):
        previous = signal.getsignal(signal.SIGALRM)
        original = signal.setitimer
        calls = []
        def install(*args):
            calls.append(args)
            if len(calls) == 1:
                raise OSError("timer installation unavailable")
            return original(*args)
        with mock.patch.object(signal, "setitimer", install), \
             mock.patch.object(self.transport.opener, "open") as opened:
            with self.assertRaises(ValueError):
                self.transport.fetch(URL)
        opened.assert_not_called()
        self.assertIs(signal.getsignal(signal.SIGALRM), previous)
        self.assertEqual(self.journal()["attempts"][0]["error"]["type"], "DeadlineGuardUnavailable")

    def test_interrupt_has_no_retry_and_retains_started_intent(self):
        with mock.patch.object(self.transport.opener, "open", side_effect=KeyboardInterrupt()) as opened:
            with self.assertRaises(KeyboardInterrupt):
                self.transport.fetch(URL)
        self.assertEqual(opened.call_count, 1)
        journal = json.loads(next(self.transport.directory.rglob("journal.json")).read_bytes())
        self.assertEqual(journal["attempts"][-1]["state"], "STARTED")

    def test_prior_alarm_deadline_is_not_extended(self):
        old = signal.getsignal(signal.SIGALRM)
        class OuterDeadline(Exception):
            pass
        def outer(*args):
            raise OuterDeadline()
        signal.signal(signal.SIGALRM, outer)
        signal.setitimer(signal.ITIMER_REAL, 0.04)
        try:
            with self.assertRaises(OuterDeadline):
                with sources.request_timer(time.monotonic() + 2):
                    time.sleep(0.3)
            self.assertIs(signal.getsignal(signal.SIGALRM), outer)
        finally:
            signal.setitimer(signal.ITIMER_REAL, 0)
            signal.signal(signal.SIGALRM, old)


if __name__ == "__main__":
    unittest.main()
