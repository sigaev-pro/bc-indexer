# E01-01.builds: isolated Go checksum metadata

Date: 2026-11-08 (Europe/Moscow). Parent: [issue #11](https://github.com/sigaev-pro/bc-indexer/issues/11).
Author: `/root/boot03_controller`. Branch: `build/module-metadata`.
Base: `0299a653cae572e3f044f8746782d681b7778599`, accepted PR #116.

## Retained failure

Operation `a7f0906b899f026ac8556a8824cc240a` in
`run_D95_builds_1794090095` passed official sources, images, signatures, package
installation, and all 225 installed package archives. Command 249 ran native
Geth `go mod download -json all` with Go 1.27.3. It returned 348 JSON records
without `Error`, then exited 1 with:

```text
go: updating go.sum: open /source/go.sum: read-only file system
```

The stdout digest is
`5ed522c0bee0e26bfad2505cfdb8c116d4bd17885e12ed9def450260b68f51d0`;
stderr digest is
`ff5b832bea1dabc6aa267377d9ef2e08cc23195ea7ef46734bbf50a76dbe2cf5`.
The failed result remains unchanged, SHA-256
`178fdf19b9a04ca57650fdb9ae5d7c4aa2dfe53212f5b9084b9a6500de625cb5`.
The coordinator independently verified cleanup of supervisor 38486, worker
38520, all recorded groups, and owned containers. Compilation did not start.

Read-only inspection verified that `source.tar` has SHA-256
`196cd5ef574476b8bf6c0dc0a6d4a467fa94fedd7bf5cc4ab06a84957e1aa658`.
Archived and extracted `go.mod`/`go.sum` bytes still match; 272 downloaded
checksum entries are absent from the original checksum file. A failed command
cannot be accepted simply because its preceding downloads succeeded.

## Narrow repair and proof

Copy the original metadata into each component's owned `metadata` directory.
The source mount stays read-only. Use `-modfile` alongside `-mod=readonly` for
every Go phase; the [Go module reference](https://go.dev/ref/mod#build-commands)
defines the paired alternate checksum file while preserving the module root.
No source, dependency pin, product schema, or general cache/overlay framework
changes.

Fixed capture commands retain both metadata files through existing command
stdout descriptors. The trusted verifier requires their exact component and
container, successful ordered native acquisition with checksum authentication
and no private-module exemptions, unchanged copied `go.mod`, preserved original
checksum entries, and agreement with selected downloads. Additional `/go.mod`
rows may represent authenticated graph-loading versions outside the selected
closure. Their proof is the native Go command evidence; no independent offline
checksum-database replay is claimed. New unselected archive hashes fail.
The same checks run before compilation, after compilation, and during final
materialized validation, including post-capture mutation and missing-file checks.

The initial red controls exited 1: the expected alternate-modfile flag was
absent and no isolated metadata-copy API existed. Portable tests cover immutable
source copies and mount options, separate component flags, unsafe archive inputs,
changed/removed/conflicting checksums, selected hash mismatch, allowed graph-only
sums, missing/wrong-component captures, private checksum exemptions, inconsistent
later Go flags, and post-capture tampering. An offline host-Go control uses only
synthetic local modules to verify relative-replacement semantics; it does not
establish a pinned native build.

## Separate coordinator probe

Before this implementation was accepted, the coordinator ran a bounded native
flag-semantics probe using accepted source `0299a653`, fresh pinned Geth source,
and copied retained module cache. The container had no network, used
`GOPROXY=off` and `GOSUMDB=sum.golang.org`, and removed copied ziphash files before
native download/list/verify. All three commands passed in 50.37 seconds with
348 modules. The source root stayed `/source`; the alternate module path was
`/proof/sources/geth/metadata/go.mod`. Original source metadata stayed unchanged.
Generated checksums grew from 574 to 846 rows: 272 additions and no extra
graph-only rows for this Geth case. Generated checksum SHA-256:
`32438e90c4958abfd2e5ffe1819ab443bea58c9e09897d22db1b849188170e06`.

Probe: `run_D95_modfile_probe_1794092601/1433cb92a11c3bf2add7b4c62388df9b`.
Result SHA-256:
`a7089862cdbd5095da6a69ce6c2b6f9af744acaa9e8846626dd4488a335b5186`.
The coordinator independently verified that all groups and containers were
absent. This proves native flag behavior, not this implementation or a full
build. The earlier failed run remains FAIL; native builds/runtime remain NOT_RUN.

## Checks and handoff

All 25 focused module-metadata/executor/material tests passed in 0.353 seconds.
All 128 trusted build tests passed in 35.147 seconds (wrapper exit 0, elapsed
35.589 seconds), with two existing opt-in Docker checks skipped. The ten new
methods include the actual offline host-Go replacement control. Documentation
checks passed for 112 documents, 1,111 local links, 84 catalog entries, 16
normative mirrors, and unchanged original v2; whitespace checks passed.

```bash
cd .project/automation
python3 -m unittest test_build_module_metadata test_build_execution test_build_materials
cd ../..
python3 -m unittest discover -s .project/automation -p 'test_build*.py'
python3 .project/automation/check_docs.py
git diff --cached --check
```

Integration uses `runner.prepare_module_metadata(identifier, archive)`, the
existing `go_environment(identifier, platform, epoch)`, and fixed `runner.inside`
capture commands for the two metadata paths. Call
`build_materials.module_metadata(identifier, closure, runner.root, runner.commands)`
after captures, after compilation, and during final validation. Capture only once,
after native download/list/verify and before compilation; later checks verify the
original captured references. The `closure` uses existing `source_archive`,
`downloads`, `modules`, and `verification` descriptors.

No author Docker, live module download, or full build ran. Tests completed and
cleaned their disposable files; no background author process remains. Publish
the checked draft, freeze the head, and obtain independent review and required
CI before the coordinator's next actual build. Issue #11 remains incomplete.
