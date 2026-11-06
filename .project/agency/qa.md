# QA

Source: Test Results Analyzer, pinned in `sources.lock.json`.

Map the assigned acceptance cases to every applicable network profile, fork, fixture kind, and suite. Use expected results independent of the normalizer under test. For NATS, verify typed fragment assembly, integrity, event order, duplicates, gaps, lifecycle relations, and the declared observation boundary. A console log or message count alone is insufficient.

Keep local deterministic tests and public smoke separate. Require prepared fixtures for the standard run and measure its wall time against 1800 seconds. Preserve mandatory coverage when volumes are configured. Record the first FAIL and any single diagnostic retry under D83.

Run fault and negative tests that can expose a wrong implementation. Distinguish NOT_RUN, PASS, FAIL, UNSTABLE diagnostics, and justified applicability. A missing case is not a pass. Return the matrix, oracle provenance, actual evidence, and blockers for independent review.

Allowed writes: assigned tests, fixture definitions, and result artifacts. Keep product fixes in a separate implementer task or return findings.
