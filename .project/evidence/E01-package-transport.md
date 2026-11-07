# E01-01.builds: bounded package transport recovery

Date: 2026-11-08 (Europe/Moscow). Parent: [issue #11](https://github.com/sigaev-pro/bc-indexer/issues/11).
Author: `/root/boot03_controller`. Branch: `build/package-transport-retry`.
Base: `b0d592d7b77db85209fbd668ec31f1bbc2979a5c`, accepted PR #115.

## Failure retained

Actual operation `b8a437fc11c030a96ef495b4b5534103` in
`run_D95_builds_1794086057` completed both live updater modes, both independent
source passes, image/signature checks, and package installation, then failed
before compilation while acquiring the installed package closure. Its output
records a URLError wrapping SSL `UNEXPECTED_EOF_WHILE_READING`. The original
result remains FAIL with native runtime NOT_RUN; result SHA-256 is
`f900762a64dd0980db09a171e11963f1ad4adef6c5024fdd89645f2d63e17da8`.
The coordinator independently confirmed cleanup of all owned groups and
containers.

The coordinator identified the next expected archive as
`libnettle8t64_3.10.1-1_amd64.deb`, size 305,436, SHA-256
`1b03d4a9cd9c8143ba50fe6396f36937e901a317d492d179f4596862c1731cfe`.
No output file existed for that inferred request. One later read-only fetch
returned HTTP 200 with that exact size/hash in 1.07 seconds. This demonstrates
observed transport recovery, not a permanent fix or an accepted build.

## Narrow repair

Only `Packages.archive` opts into the two-attempt helper, for an exact pinned
official package GET. Only the specified typed SSL EOF, direct or wrapped in
URLError, permits its second attempt. Generic SSL strings, certificate errors,
HTTP statuses, timeouts/resets, malformed data, integrity errors, local file
errors, stop, and deadline exhaustion do not. No metadata/image/updater call,
runtime case, controller, source pin, or product interface changes.

Each dispatch requires a durable journal intent. Every attempt has a separate
body file. Failed bytes and their references remain present; the second GET
restarts from zero. Available response metadata and sanitized exception types
are retained without request credentials or arbitrary exception messages.
The body reference describes bytes written by this helper, not inaccessible
transport buffers. A recovered request is marked `RECOVERED_TRANSPORT`;
first-attempt success is distinct. Existing successful source-observation keys
remain unchanged.

The final seal verifies the protected journal descriptor and trusted memory,
every attempt body, and each original OS signature log against its captured
reference and command/hash expectations. It writes a sealed journal and new
signature logs containing `archive_fetches`, then updates the passed closure
references together. Original signature logs remain unchanged. Final materialized
validation requires this binding and checks every installed archive against
the journal and its signed index. Sealing prohibits subsequent package fetches.

Integration API for a fresh diagnostic:

```python
inventory = packages.inventory(container, compiler_image)
journal_ref = runner.seal_package_fetches([inventory])
summary = build_materials.package_transports([inventory], runner.root)
build_materials.os_closure(inventory, runner.root, runner.plan, time.time(), runner.commands)
```

The full native driver seals both compiler and runtime closures after their
work completes. Product BuildPlan/manifest shapes remain unchanged; this is an
internal trusted-log extension. The independent scope assessment permits this
technical preparation repair under the existing D83/D95 rules. Earlier failed
operations and runtime-case outcomes are never cleared or relabeled.

## Checks and next action

The initial deterministic red control failed because the old fetch helper did
not provide package-only retry opt-in. Thirteen new test methods exercise direct
and wrapped typed EOF, separate partial bodies and restart from zero, the exact
two-attempt ceiling, shrinking deadline/stop bounds, journal-write failures,
nonretryable transport/status/hash/size/file failures, unchanged non-opt-in
behavior, immutable sealing, altered original logs/journals/partials, and
replay/source-binding rejection. These are synthetic controls, not live network
or cryptographic evidence.

Two focused integration observations were addressed before freezing: check
deadline/stop again after the durable intent and immediately before dispatch;
retain bounded byte-valued partial data provided by a read exception while
keeping IncompleteRead nonretryable. Dedicated controls cover both boundaries,
including explicitly recorded truncation when exposed bytes exceed the bound.

All 113 trusted build tests passed in 32.548 seconds (wrapper exit 0, 32.81
seconds), with two opt-in Docker tests explicitly skipped. The 45 focused
transport/package/material/executor tests also passed before the final broader
check. Command:

```bash
python3 -m unittest discover -s .project/automation -p 'test_build*.py'
```

No live fetch, Docker, installation, or full build ran in this author block.
Temporary test files were cleaned; no background author process remains.
Documentation checks pass for 111 documents, 1,109 local links, 84 catalog
entries, 16 normative mirrors, and unchanged original v2; whitespace checks pass. Next:
publish the frozen draft referencing #11, let the coordinator run a fresh
bounded diagnostic with one genuine typed EOF injection, obtain independent
reviews and CI, and only then start a corrected full run after acceptance.
Issue #11 remains incomplete.

## Review repair: original logs and available bytes

Both independent Spec and Standards reviews requested changes on frozen head
`2926d6dd642532090309dae87aebfce2ecf4ff74` in
[PR #116](https://github.com/sigaev-pro/bc-indexer/pull/116). Final validation
did not bind the preserved original signature logs after sealing. Oversized
normal reads discarded available bytes, and wrapped read exceptions did not
retain partial bytes exposed only by their cause. These findings remained
blocking despite the previous portable checks.

The coordinator's diagnostic on that same head passed in 315.84 seconds:
the exact Go version, three signatures, 13 exact installed roots, 225 installed
packages, and exactly one explicitly injected/recovered TLS EOF. Its then-current
sealed proof validators passed. Result SHA-256:
`9571c28dd770a2f524023ec456118365ff0301eebbbea4f6d0251dfaa939142e`.
The coordinator independently verified that supervisor 21669, worker 21671,
all recorded candidate groups, and owned containers were absent. This is a
successful but insufficient diagnostic of the old head, not acceptance or
validation of the repair.

Writer ownership returned on 2026-11-08. Three direct regression methods failed
with five subcase failures before implementation: a wrapped cause's partial
bytes were omitted, oversized first/later read blocks lost their bounded
prefixes, and both `package_transports` and `os_closure` accepted a changed
original log after sealing. The red command exited 1.

Each immutable extension now includes the previously captured
`original_signature_log` descriptor. Both validators verify those original bytes
and require all original core fields, including JSON types, to match the
extension. No original file is rewritten or rehashed into a replacement trusted
expectation. Controls cover post-seal mutation, a missing descriptor, and
changed core fields even when the attacker recomputes local descriptors.

Package attempts now record `observed_body_size` and `body_truncated`. Oversized
normal reads retain their available prefix up to the exact archive bound before
failing. Partial bytes exposed through `URLError.reason` are retained when the
outer exception does not expose them; duplicate outer/cause data is retained
once. Oversized direct or wrapped partials remain nonretryable. Sealed journal
validation rejects omitted or truncated bytes. The existing two-attempt limit,
typed EOF classification, deadline/stop, exact hash/size checks, successful
source-observation fields, and seal API are unchanged.

Repair checks:

- All 18 transport tests passed in 0.302 seconds, including five new methods.
- All 118 trusted build tests passed in 33.073 seconds; the two existing opt-in
  Docker checks were skipped. Wrapper exit 0, elapsed 33.497 seconds.
- Documentation checks passed for 111 documents, 1,110 local links, 84 catalog
  entries, 16 normative mirrors, and unchanged original v2. Whitespace checks
  passed.

Commands:

```bash
cd .project/automation
python3 -m unittest test_build_package_transport
cd ../..
python3 -m unittest discover -s .project/automation -p 'test_build*.py'
python3 .project/automation/check_docs.py
git diff --check
```

All new controls use disposable synthetic files and mocked transport. No author
live fetch, Docker, full build, or background process ran; temporary test files
were cleaned. Publish the repair to PR #116 and freeze it for fresh independent
reviews, required GitHub CI, and a new coordinator diagnostic. The earlier
failed operation and review outcomes remain unchanged. Issue #11 is incomplete;
native builds and runtime remain NOT_RUN.
