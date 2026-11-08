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

## Independent review repair

The published initial candidate was `8560c00cc3d62d50b4a04bd4d0ab0089e2a0c7c3`
in draft PR #118. Exact-head portable CI passed 377 tests and all 54 component
negatives in 184.784 seconds, with two existing opt-in Docker skips. The tree
was clean. Both independent review axes still returned `REQUEST_CHANGES`:

- An invented effective GitHub credential echoed by a mocked response could
  enter raw stdout and the body artifact. The original header-only secret list
  did not include environment or credential-helper authentication.
- `http.client.IncompleteRead` after a retained prefix escaped terminal attempt
  handling. Its available partial bytes were lost and the journal stayed
  `STARTED`, although later runner work was fenced.

The first three regression methods reproduced 11 failing subcases and one
helper-path error against that candidate. These failures and the initial CI
result remain historical evidence; passing tests did not override the reviews.

The repair privately resolves and pins effective GitHub authentication within
the original request budget. A helper's bounded stdout/stderr never becomes an
artifact. Raw request streams use bounded lookbehind before writes, including
encoded values split across reads. An echo rejects the request without retry
and clears unsafe owned output. HTTP framing exceptions now retain the bounded
prefix plus available partial bytes and finish the attempt as a nonretryable
failure. The journal fields and allowed recovery classes are unchanged.

All 26 focused methods pass with tracemalloc and ResourceWarnings enabled
(3.814 seconds). Controls cover both environment variables, helper credentials,
raw and encoded body/header/stderr echoes, split reads, helper failure/size/time
bounds, process and pipe cleanup, partial-byte truncation, and terminal journal
persistence failure. All credentials and responses in these controls are
invented; no live credential or network lookup occurred. The affected build
suite then passed all 159 tests in 65.789 seconds (66.144 seconds including the
wrapper), with two existing opt-in Docker skips and no ResourceWarning. Document
checks passed 113 files, 1,116 links, 84 catalog entries, and 16 normative mirrors;
the original v2 bytes remain unchanged. Whitespace checks also passed. New
exact-head CI and both independent reviews remain required after publication.

## Raw-stream overflow review repair

Candidate `090fdc4a7c12d5c0db12a7f0afda8d5481134706` fixed both original
findings. The Spec axis passed it, but Standards retained a further
`REQUEST_CHANGES`: a raw output overflow discarded the safe prefix available in
the overflowing read without representing the omitted raw bytes. The independent
reproduction used unchanged header constants, an 8-byte body cap, and 100,019
produced stdout bytes. It retained 65,536 bytes against a 65,544-byte raw cap.
The decoded-body fields stayed zero/false/null because parsing never began.

The new actual-process regression reproduced that eight-byte prefix loss before
the repair. Additional controls exercise first and later overflowing blocks,
stdout and stderr, exact-bound success, and the newly required accounting fields.
Raw counts now record bytes actually returned by reads. Each safe available
prefix is retained before a nonretryable overflow rejection. Four closed
internal process fields bind stdout/stderr observed counts and truncation flags;
they do not relabel raw process bytes as decoded body bytes. The validator checks
types, sizes, and finite bounds, and rejects raw truncation as success or eligible
recovery. Existing credential checks still run before retention and can discard
unsafe output. Public BuildManifest fields and allowed retry classes are intact.

All 30 focused methods pass with tracemalloc and ResourceWarnings enabled
(4.716 seconds), including fresh-hash tamper controls and the previous credential
and incomplete-read regressions. All 163 affected build tests then passed in
66.757 seconds (67.167 seconds including the wrapper), with two existing opt-in
Docker skips and no ResourceWarning. Document checks passed 113 files, 1,117
links, 84 catalog entries, and 16 normative mirrors; original v2 bytes remain
unchanged. Whitespace checks passed. New exact-head CI and independent reviews
remain required. The two earlier review outcomes and their
passing checks remain recorded above; neither grants acceptance of this repair.
No live request, credential lookup, container, or native build was performed.

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
