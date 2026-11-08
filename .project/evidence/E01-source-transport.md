# E01-01.builds: bounded trusted source recovery

Parent: [issue #11](https://github.com/sigaev-pro/bc-indexer/issues/11).
Author: `/root/boot04_preflight`, after explicit writer transfer from
`/root/boot03_controller`. Branch: `build/source-transport`.
Base: `23eb96dffd3019bb4c6b1c345ab29707a5ec1751`, accepted PR #117.

## Preserved failures

Actual operation `2b78ba25349204d63d61149c699c19` in
`run_D95_builds_1794094441` failed trusted GitHub command 15:

```text
Get "https://api.github.com/repositories/265775217/releases?per_page=100&page=2": EOF
```

The complete product discovery had already passed. The operation retained exit
1 and complete cleanup. A separate bounded probe later received HTTP 200 and
33 rows; that probe did not repair or accept the failed operation.

The one unchanged diagnostic retry, operation
`19e78cfb03ebc965a6a62af27f8040` in `run_D95_builds_retry_1794095305`, failed
earlier during product discovery. Bor page 3 retained a `TimeoutError`; NATS Go
retained only `URLError`, so its underlying cause is unknown. Both full
operations remain failed, cleanup is retained, and neither began native
compilation. No further unchanged full retry belongs to this block.

## Implemented boundary

The [build contract](../automation/build-task-contract.md#bounded-source-get-evidence)
now defines one explicit acquisition reference on each current source
observation. The closed journal binds at most two same-URL attempts and all
retained body/process bytes. The product author implements the same interface
independently. Fixture transport inputs retain their five-field shape; output
reports receive separate, explicitly synthetic journals. Historical package
reports remain untrusted input-only exceptions and still require fresh signature
verification before use.

The trusted refresh uses a dedicated bounded GET helper. It retries only typed
network timeouts, the exact approved SSL EOF class/reason, or the exact observed
GitHub CLI EOF diagnostic. HTTP, certificate, size, content, filesystem,
credential-exposure, stop, deadline, and unknown errors never gain a retry.
The monotonic 60-second request budget spans both attempts. The helper persists
intent before dispatch and failed evidence before recovery. Earlier caller
alarms retain their handler and cancellation semantics. Terminal failure fences
the runner; cleanup remains permitted. Token requests and native/build commands
do not use this recovery path.

Failed GitHub requests retain their actual process exit and raw stdout/stderr in
the source journal. They never become successful `manifest.commands` entries.
The existing native-command success rule is unchanged. Response bytes are never
concatenated across attempts, and authenticated redirects or response bodies
cannot expose known credential values in evidence.

The internal updater signature proof now references the check-mode independent
report, which was previously returned and discarded by the driver. Final
materialized validation rechecks all four report paths: product check/update and
both independent refreshes. Their nested journal, body, and process references
are checked again after candidate preflights. No public BuildManifest field,
selected pin, native graph, or runtime acceptance rule changed.

## Checks and retained repair history

The first focused check failed because acquisition evidence support did not
exist. Integration then rejected old source fixtures lacking the new reference;
the fixtures were explicitly rebound so pagination and chart controls still
reach their intended semantic checks. The 32 source/pagination controls passed.

A short GitHub timeout test exposed an alarm firing again during owned-child
cleanup, leaving pipe warnings. The request alarm now ends before child reaping
and pipe closure. Direct controls also distinguish an inherited caller handler's
`TimeoutError` from a network timeout and reject later dispatch after cancellation
or terminal persistence failure. Eighteen focused tests passed with tracemalloc
and ResourceWarnings enabled, without warnings or remaining owned subprocesses.
They cover exact retry classes and limits, safe partial bytes, raw GitHub EOF
binding, aggregate read deadlines, previous alarms, persistence/stop fencing,
credential exposure, fresh-hash mutations, and changed/deleted/cross-pass proof
references.

All 151 affected build tests passed in 67.691 seconds (wrapper elapsed 68.130
seconds), with two existing opt-in Docker checks skipped. No Docker test was
enabled. A supervised synthetic interface check against product commit
`e3e6c555c6d3785b0a32cd1582389d7f4c50185f` returned complete discovery with 148
observations, exit 0, and confirmed cleanup. This checked protocol compatibility
only; it did not accept that product commit. Its separate review and repair
remain part of the product branch history.

```bash
cd .project/automation
python3 -B -X tracemalloc=5 -W always::ResourceWarning -m unittest test_build_source_transport
cd ../..
python3 -B -m unittest discover -s .project/automation -p 'test_build*.py'
```

No live request, Docker action, native build, full verification operation,
original-run mutation, or controller mutation was performed by this author.
Runtime remains NOT_RUN. Required next steps are exact-head CI, separate Spec
and Standards reviews, and the coordinator's bounded actual-source diagnostic.
Only then may a corrected full operation attempt all existing parent gates.

The first full portable CI passed 375 tests and 54 component negatives in
182.481 seconds, with two existing Docker skips. It also checked 113 documents,
1,115 local links, 84 catalog entries, 16 mirrors, and unchanged original v2.
That run preceded the final inherited-deadline correction: a direct replay on
the checked implementation demonstrated that a consumed/rearmed caller timer
could admit a later URL. The runner now retains the earliest observed monotonic
caller deadline across the whole fetch and future source/native admission.
The post-guard exception case also rejects later work. All 20 focused tests pass
with tracemalloc and ResourceWarnings enabled in 2.176 seconds. The earlier CI
result is retained rather than represented as proof of this later correction;
the frozen candidate requires its own cumulative CI result.

## Diagnostic interface

`build_source_transport.fetch(runner, url, report_directory, headers=None,
maximum=256 * 1024 * 1024)` returns the six-field observation and writes its
journal/attempt files below the existing report directory. The runner must
provide `remaining(seconds)`, enforcing its existing operation deadline and stop
file. The helper shares the runner's terminal-failure fence; do not suppress that
failure and continue dispatch. It automatically uses the fixed GitHub CLI path
for `api.github.com` and bounded urllib for other approved source URLs.
`build_source_evidence.validate_observation(row, report_directory, False)` checks
the retained proof without making another request. Any injected failure belongs
in the diagnostic's explicit provenance and cannot be presented as a naturally
observed network failure or full build acceptance.
