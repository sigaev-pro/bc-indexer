# AUT-03: Isolated build acceptance

Issue: [98](https://github.com/sigaev-pro/bc-indexer/issues/98). Authorization: D92. This explicit BOOT-09 maintenance follow-up remains outside the fixed 84-task catalog. Status: IN_PROGRESS. The working tree contains a candidate build recipe; the canonical run has not enabled it. No product build is accepted.

## Owned Docker supervision substep

The new resource guard binds a random namespace to one run, task, operation, and exact image references. It records and syncs each creation intent before asking Docker to create a container, then records the immutable returned ID. Cleanup validates all ownership labels before mutation, stops/removes only those IDs, and confirms an empty namespace. An unresolved creation, daemon error, foreign label, or failed removal remains uncertain. A missing container listing is not treated as proof that an outstanding create request cannot finish later.

Supervised build commands can now carry this explicit resource request. Normal completion, timeout, and parent-watchdog termination retain Docker cleanup evidence in addition to host process-group evidence. Existing commands do not gain Docker authority. This substep does not implement a build command, enable a product adapter, or alter accepted evidence receipts.

Local checks:

- Ten ownership and creation-intent unit tests passed.
- Five controller signal tests and three backend tests passed.
- An explicitly authorized disposable Docker fixture passed in 10.04 seconds. It used the selected Debian content reference, network disabled, one CPU, 128 MiB RAM, no mounts, and no exposed host ports. A live container survived its host worker until supervisor timeout; the guard stopped and removed container `2c04783ec45ff9d3e303da31cc8833f4b6d3595e5fb7712613324f514816eab5`, then confirmed absence.
- The strengthened parent-watchdog fixture passed in 6.63 seconds. A separate ready file proves the daemon container is running before the parent kills the supervisor. The guard then removed container `0606811c6f02c7a02799b83610e7b408b0d6c9a3adc47333e1d880621ada03ab`; the owned namespace was empty. An earlier two-fixture run also passed but used buffered stdout for readiness, so it established deadline cleanup rather than this stronger live-container stop condition.

The real fixture command is opt-in: `EXTRACTOR_DOCKER_FIXTURE=1 python3 -m unittest discover -s .project/automation -p test_build_supervision.py`. Normal offline CI skips these Docker cases. Fixtures created no native node or network service. Their resources and temporary files were removed. Native chain runtime remains NOT_RUN.

Full tracked-tree offline CI passed before this substep commit: exit 0 in 106.11 seconds, 219 tests (the two opt-in Docker cases skipped), component checks, 99 documents, 1,067 local links, 16 mirrors, all 84 catalog entries, and unchanged original v2. This is supervision infrastructure evidence, not build task acceptance.

## Complete adapter candidate

The adapter now defines `build-artifacts-v1` and `component-update-v1`. Product code supplies a closed plan, manifest preflight, and the updater. Trusted code owns fixed source archives, isolated Go closures, signed package acquisition, compiler execution, binary metadata checks, OCI materialization, and retained command/cleanup evidence. Coverage is five native/support clients, ten Go module roots, compiler/runtime OS closures, all seven network-profile bindings, and the 41-component registry. Source-owned replacements and both candidate architectures remain explicit.

The first actual execution route selects linux/amd64. Arm64 remains NOT_RUN. Host Docker/Git/Python/GitHub CLI evidence is distinct from future deployment pins. The route does not require a second full rebuild or claim bit-identical rebuild results; `repeat_build` remains NOT_RUN. Native chain execution and NATS delivery remain NOT_RUN.

Exact task selection now covers verification, external-candidate adoption, public proof binding, retained captures, and receipt replay. Other product tasks stay unconfigured. Independent controls reject stale task/head/base/source/output/publication bindings and preserve the existing artifact receipt path.

The delegated author ended at the provider usage limit before completing the final handoff. The owner requested continuation. Root preserved all changes, confirmed no remaining author/test/controller process or running container, recorded the ended session without claiming task success, and took sole write ownership. The fixed D92 deadline, counters, task catalog, and original receipts were retained. The accepted E01 evidence main was handed off as configuration epoch 3 before further work.

At takeover, all 47 build-adapter tests passed in 3.62 seconds, with the two opt-in Docker tests skipped. The first full-CI attempt stopped at document checks because the new linked contract module had not yet been staged; it did not run the full test suite. Final integrated results will be recorded below before review. Root's separate signed-index preparation diagnostics are not product acceptance or substitutes for these candidate checks.

The next full-CI attempt stopped at whitespace checks for extra final blank lines in six new modules. After staging and fixing those lines, full CI passed in 101.00 seconds: 254 tests, all 54 component negatives, 99 documents, 1,069 local links, 16 normative mirrors, the unchanged 84-task catalog, and unchanged original v2. The two opt-in Docker tests were skipped in this offline run. These results cover the complete candidate before integration of accepted E01 evidence main; integration and exact-head review remain required.

## Independent review and first repair

Both independent axes reviewed head `453315a19dc2abc8c55dee20a5a18f3cb562d360` against accepted base `eda0a33fb6a8611ac661d5ae717709e1bab87030` and returned REQUEST_CHANGES. Spec reproduced two defects: the pinned Prysm source-contained go-bip39 replacement was rejected, and identical-byte external-lock rewrites escaped the guard. Standards reproduced a third: candidate preflight could modify an artifact after its last trusted validation. Both reviewers completed their subprocesses and returned read-only ownership before root resumed edits. Their independent checks included the 47-test adapter suite, six extra Spec controls, 17 existing evidence-task regressions, and 22 acceptance regressions; no native build was claimed.

The first repair retains a separately hashed local-module inventory derived from the exact source archive and verifies package confinement. Every governed candidate external lock now participates in before/after fingerprints. Candidate commands run in owned process groups; descendants are stopped, the direct child is reaped, and live group members block success. A final trusted materialized validation follows all candidate preflights. Four new regression tests cover the Prysm shape and escape cases, both external locks, delayed descendant writes on normal exit and timeout, and synchronous artifact mutation after candidate success. The targeted suite passes 51 tests in 7.66 seconds, with two opt-in Docker cases skipped. Full repaired CI and both exact-head re-reviews remain required.

## Second review and process-ownership repair

Both re-review axes checked `f624f56de88cc79bae6f95b9c1b8e99a3b56cbe4`. They confirmed the original artifact, local-module, and external-lock fixes, including an archive from the exact pinned Prysm commit with 16 local files. They still returned REQUEST_CHANGES: nested candidate groups survived outer driver SIGTERM/SIGKILL, and normal cleanup signaled a group after `communicate()` had reaped its leader. Their disposable reproductions and processes were removed before root resumed ownership. These failed results remain part of the attempt history.

Implementation attempt 3 / repair cycle 2 uses a trusted live group leader with a parent lifeline. The leader kills its own group if the driver disappears. Normal cleanup uses the existing unreaped-child invariant and reaps only after confirming group absence. A durable intent precedes dispatch; the outer supervisor and parent watchdog inspect candidate-group cleanup without signaling stale recorded PIDs. The frozen trusted source sets include the new guard. New real-process regressions cover outer SIGTERM, SIGKILL, operator stop, deadline, unresolved intents, and the no-signal-after-reap rule. The targeted suite passes 55 tests in 19.52 seconds, with the two opt-in Docker cases skipped. Full CI and both final exact-head reviews remain required.

## Final bounded review result

Spec passed head `1c2c3d4fd2026a20565a00045472ecc2c5b79e18`, including independent parent SIGTERM/SIGKILL, pre-dispatch parent death, expired deadline, unresolved-intent, and no-signal-after-reap checks. Its targeted suite ran 55 tests in 17.933 seconds, with two Docker cases skipped. Standards also confirmed both ownership fixes and ran the same suite in 19.28 seconds, but retained one P2 normal-cleanup race.

The driver closes its lifeline before `finish_group`. The guard then kills its own group concurrently with explicit termination. A plain wrapper delaying the existing termination call by 0.0001 or 0.001 seconds reproduced a valid command being rejected: `terminate_group` returned `cleanup-denied`, while immediate bounded confirmation returned true. The leader stayed unreaped; syscall tracing recorded EPERM from `killpg(SIGTERM)`. This is a completion defect, not false acceptance. The observation remains retained even though other controls passed. Both reviewers cleaned their fixtures and returned ownership.

The proposed repair keeps the normal lifeline open through explicit owned-group cleanup and closes it in an outer `finally`, retaining failure handling and parent-death protection. It needs a scheduling-window regression and both fresh independent reviews. No fourth implementation was started: DEVELOPMENT_AUTOPILOT:85 allows two review-repair cycles (three implementations), and D92 preserves the bound. Status is BLOCKED_REVIEW_LIMIT. Draft PR #100 remains unmerged, the original BOOT-09 receipt is unchanged, and product E01-01.builds remains blocked. Extra-cycle authorization is required before more implementation; the original deadline is unchanged.

## Retained checks and uncompleted acceptance

## Owner-authorized additional repair

The owner explicitly authorized steps 1–3 and one additional repair/review cycle. D92's addendum records implementation attempt 4 / repair cycle 3; all earlier findings remain retained and issue #11 stays out of scope. The new scheduling regression failed on the old code (three assertion failures and two errors). Keeping the lifeline open through cleanup removes the competing self-termination. A subsequent targeted run exposed another Darwin signal-transition denial in an existing preflight test. Cleanup now resolves that denial only after the existing bounded check proves group absence while the leader remains unreaped; live groups and lost ownership still fail.

Three new tests cover scheduling delays from zero through 50 milliseconds, lifeline closure on cleanup failure, and both actual-absence and still-live cases after a reported signal denial. The targeted suite then passed 58 tests with two Docker skips in 23.75 seconds. That run reported a fixture child not yet reaped; the test now retains its owned process and explicitly reaps it after absence confirmation. Full CI and both exact-head reviews follow; no acceptance is claimed from the targeted result alone.

Full CI for the complete additional repair exited 0 in 117.82 seconds. Its logs and exit record are retained, including the earlier failed controls. The checked candidate still requires both independent exact-head verdicts and protected GitHub acceptance; issue #11 remains outside this authorized follow-up.

Full CI for the second repaired tree exited 0 in 114.36 seconds. Durable stdout, stderr, timestamps, and exit status were retained. The targeted and complete checks do not replace both final exact-head independent reviews or protected GitHub acceptance.

Full repaired CI exited 0 in 104.34 seconds. Its stdout, stderr, command, times, and actual exit code are retained separately from the interrupted run. This is validation of the first repaired tree; it is not an independent review or product build result. Both exact-head re-reviews are still required after committing it.

Integration with accepted main `eda0a33fb6a8611ac661d5ae717709e1bab87030` preserved both status histories and all accepted E01 files. The earlier interrupted integrated run had no retained result and is not counted as a pass. Its replacement retained stdout, stderr, and the actual exit code: full CI exited 0 in 101.11 seconds, with 254 tests (two opt-in Docker cases skipped), all 54 component negatives, 101 documents, 1,075 local links, 16 normative mirrors, the unchanged 84-task catalog, and unchanged original v2. The run checked the staged integration tree before its merge commit.

Commit the checked integration and obtain independent exact-head review and protected acceptance. Missing signed Debian package closures, source observations, compiled bytes, or cleanup evidence must block product acceptance. The current pinned caches are preparation only. Preserve deadline `1794032247`, shared invocation accounting, task attempts, original receipts, and all accepted E01 evidence behavior.
