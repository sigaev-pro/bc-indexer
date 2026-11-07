# AUT-03: Isolated build acceptance

Issue: [98](https://github.com/sigaev-pro/bc-indexer/issues/98). Authorization: D92. This explicit BOOT-09 maintenance follow-up remains outside the fixed 84-task catalog. Status: IN_PROGRESS. No build recipe is enabled and no product build is accepted yet.

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

## Remaining work

Freeze the constrained build-plan and updater interfaces, add independent conformance cases and actual materialization checks, connect exact task/proof/adoption/publication/receipt selection, and obtain independent exact-head review. Missing signed Debian package closures, source observations, compiled bytes, or cleanup evidence must block acceptance. The current pinned caches are preparation only. Preserve deadline `1794032247`, shared invocation accounting, task attempts, original receipts, and all accepted E01 evidence behavior.
