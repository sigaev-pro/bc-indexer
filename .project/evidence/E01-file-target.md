# E01-01.builds: Native entry-point and source provenance

This is a scoped trusted-verifier correction for issue #11 and parent draft
PR #107, from accepted source
`0e64930f73336abdde7b547add6872714d52dd47`. It implements the native graph and
source-isolation requirements in [the assigned task](../docs/TASK_DECOMPOSITION.md#task-e01-01-builds)
and [the build contract](../automation/build-task-contract.md). It does not
complete the five-client build task or authorize downstream task #12.

## Retained failure

Full operation `run_D95_builds_1794102326 / c2d81fad7336b030c629b0661f0f67`
used source `0e64930` and product candidate `dc48176b2fc03e567cd5e89e10a833486a750285`.
Both updater modes, both independent refreshes, authenticated preparation, and
actual Geth/BSC compilation completed. Bor's import command succeeded, but
validation stopped before Bor compilation with
`nonstandard package has no module identity`. The terminal result SHA256 is
`2ae14c3b2db98aa42eaa427c6322b4c3571c295adcc8c83fb64feead7256baa4`.
The coordinator independently confirmed cleanup of the supervisor, worker,
53 recorded groups, and owned containers. Original operation evidence remains
unchanged and failed; two successful builds do not establish parent acceptance.

The author reproduced the exact error from all 1,921 rows in retained
`build-artifacts/logs/139.stdout` before changing the validator. Exactly one
nonstandard package lacks a Module: Go's synthetic `command-line-arguments`
root for the fixed Bor file target. The other 157 Main-module rows retain the
ordinary selected-module proof. The fixed native recipe already selects
`./cmd/cli/main.go`; changing that recipe is outside this correction.

## Corrected binding

The exception is limited to Bor's exact file-target root fields, with no extra
active files or dependency-only/error state. It requires the regular archived
`cmd/cli/main.go`, safe regular parent entries when present, the selected module,
the archive's Git commit comment, and the fixed import-command form with the
same selected commit stamp. Exactly one synthetic root and a separate ordinary
Main-module proof are required. Missing modules on other dependencies, NATS
imports, foreign caches, and changed local replacements retain their previous
rejection rules.

The existing collection path now passes its actual import argv and selected
source context to the graph validator. Final validation receives the import
argv only after existing metadata verification binds its unique successful
command, import-output descriptor, container, ordering, and offline policy.
The public schema, native build commands, client pins, and original v2 text are
unchanged. The metadata API still works before compilation exists.

## Checks and remaining limits

All 29 focused methods passed in 0.647 seconds, covering the new source/target
controls plus existing materials and module-metadata tests. Negative controls
include another component/module/commit, missing or linked archive entry,
duplicate or omitted root, changed path/name/file/match, extra active files,
dependency-only or error states, changed executable/flags/target/commit stamp,
an additional unowned dependency, foreign cache, and NATS imports. Existing
Prysm local-replacement and final command-provenance controls also pass.

Read-only replay of the actual retained input now passes all 1,921 packages,
including 157 ordinary Main-module rows and one synthetic file root. Removing
that root or changing the command target still rejects. The selected Bor commit
is `3a018e38467594508ca31ebeaf804837fb92834a`; its archived entry point is a
regular 194-byte file. The successful retained command record was checked
against its stdout/stderr references before replay. Input hashes before and
after replay match:

- Import output: `ce9146d805b89461ed88c36feda536289e99dfeaa8a64330969d8530676c742e`.
- Source archive: `a313bc56a136b054d44ef0a319bcbc0fcf0eddba046035ed2b89b83c83847e38`.
- Command history: `269c6b7191bb79b200f749cd1abc2247457e8edb40498692b7b9bb31ffa09dd3`.

The import-only checkpoint passed 168 affected build tests in 70.916 seconds
(71.456 seconds including the wrapper), with two existing opt-in Docker skips
and no ResourceWarning. The following confirmed observations then extended this
same native provenance correction before publication.

## Confirmed binary metadata and source binding

The coordinator's supervised synthetic probe
`run_D95_file_probe_1794105480 / 11399a3df3b0a3cb71a9c769670b` used the exact
pinned Go 1.27.3 linux/amd64 compiler. It recorded `path: command-line-arguments`
and a selected `(devel)` dependency, with no `mod` row. The retained stdout SHA256
is `2b05ff124d43c648a33278a041522924cfc64a245cecfa52f4d194ad381a68ae`;
the result SHA256 is `c2735dbaab0c7d01fe9b3efdf4631af9183915f62d12f282e6da1d9cb7ebf6d6`.
Cleanup was confirmed. This proves compiler behavior on a synthetic module,
not an actual Bor binary.

Actual retained Geth and BSC build-info also omit `-ldflags`. The original
five-argument validator rejects their source/settings check despite correct
module, Go 1.27.3, linux/amd64, CGO, and trimpath metadata. The author directly
reproduced the Geth failure; the coordinator independently reproduced both.
The retained stdout hashes are `d9de5c5ef451971288085af6935b1a2cb6f8415d8d34eb2dd1f0e2ea642b0c60`
and `978834415bd4cc36968644da0b1392664e073abe4692bdad7ce5d03a819014a5`.

Read-only comparison also confirmed that the reconstructed selected compile and
import arguments and environments match the actual retained Geth and BSC
commands. Their archive epochs are 1792757178 and 1793671467, respectively;
command-history bytes remain unchanged.

The shared `native_build` path now binds the exact selected archive and recipe,
successful import/compile/inspection commands, compiled binary, its runtime copy,
and the actual native version output. Only this internal authenticated proof
allows omitted flags. It requires the existing selected commit and version
checks, preserves compiler/platform/CGO/trimpath, and then verifies ELF, import
ownership, build-info, and OCI. Bor's unique `(devel)` source dependency is
accepted only after its file-target source/import context passes; other clients
keep their `mod` identity rule. The metadata format and public manifest fields
are unchanged. A plain five-argument `build_info` call still rejects missing
source flags, and a serialized or unrelated context cannot authorize them.

The failed full operation stopped before runtime version commands, so its Geth
and BSC rows still lack complete same-operation source proof. The coordinator's
separate actual version diagnostic
`run_D95_native_versions_1794105941 / 897fd8aeb053a5a87a30a6f2df38` reported the
selected Geth/BSC commits using unchanged original binaries and the pinned
runtime image, with cleanup confirmed. Its result SHA256 is
`639e6a1408fd38ec012fd9b13c0ce061dbacadb9490f5db7b5c00db39f611d16`.
That output is separate format evidence; it is not imported into the failed
operation or treated as a completed full build proof.

All 34 final focused methods passed in 1.332 seconds. They include synthetic
per-client source/command/version/ELF/OCI records for all five clients, omitted
and reported flags, ambiguous/replaced/foreign Bor roots, wrong source stamps,
substituted binary/inspection/version references, missing copies, command order,
wrong compiler/platform/CGO/trimpath, and NATS imports. These synthetic controls
execute no compiler or container. All 173 final affected build tests passed in
68.471 seconds (68.907 seconds including the wrapper), with two existing opt-in
Docker skips and no ResourceWarning. Documentation checks passed 114 files,
1,120 local links, 84 catalog entries, and 16 normative mirrors; original v2
bytes are unchanged. Whitespace checks passed. Exact-head CI and independent
reviews remain required after publication. The author performed no native
compilation, container launch, or live source acquisition.

The coordinator's remaining-client diagnostic must use the same call as final
materialized acceptance:
`materials.native_build(identifier, builds[identifier], modules[identifier], runner.root, plan, runner.commands)`.
It includes the per-client ELF/build-info/OCI checks; no diagnostic-only bypass
is provided. Actual remaining binaries and parent issue #11 acceptance remain
pending independent review and execution.

## Actual remaining-client diagnostic

The coordinator executed the reviewed helper after both independent code axes
and exact-head CI passed on `b382aae725c7cde4b9561cb2f149cbb0a63d6086`.
Base: `0e64930f73336abdde7b547add6872714d52dd47`.
Product candidate: `dc48176b2fc03e567cd5e89e10a833486a750285`.
CI: `42440734067`, check `128609219107`, PASS.

Operation: `run_D95_remaining_native_1794107042 / 34d6045183932e1cb8fcbf208aac`.
The actual diagnostic passed in 1,653.251 seconds, with exit 0 and complete
cleanup. Exact bindings:

- Helper SHA256: `44bb30c243e0e0e89c867ccf11c4a199b4b893e1ea54ac0d16ecb11677b0d0f9`.
- Request SHA256: `d84465902171ddfbfe2636a819dc3f6d5e91e5702e2cae958d388e720b9469fd`.
- Result SHA256: `76f5f0552fc5247c2b653ccf14b7b0859ebce2dcc4366489e42024aa9817d56d`.
- Diagnostic proof SHA256: `2c1dd7692617896a5bb44dd51692e24f94fed175bc3fa133387ae8400287c6ec`.
- Command history SHA256: `5c1be7a3986a2665982a12d70edbc2b11a1249b170a6190d3df6f8ead20a0c82`.

Actual native outputs:

- Bor: 182,359,592 bytes, SHA256 `6557c1389746b17f2e89f4754d6ede26345988ad970f2e0308dbc2524bde4d92`; version 2.11.0 and commit `3a018e38467594508ca31ebeaf804837fb92834a`.
- Prysm: 88,097,544 bytes, SHA256 `ce8002f20a6ca2590d8aa6629331da6541c37b0c1db8fe85087cac989600d9c3`; version v7.3.1 and commit `96d418cd964b083e37519a9df274030a1beac7db`.
- Heimdall: 143,290,184 bytes, SHA256 `8113fd5e33fea7e46414ce8b13115a3ff7b2c6a567c01e971b86daa843d6bfd5`; version 0.13.2 and commit `03090c94e7eaef344cd16efd6c968431c4041847`.

The existing recipes resolved separate module closures, compiled the clients,
ran version commands in the pinned runtime image and created OCI layouts.
The helper called the same `native_build` path as final materialized validation
for all three clients. Bor's actual binary has the expected file-target identity;
Prysm and Heimdall have ordinary module identities. All three omit recorded
linker flags, while their authenticated commands and version outputs establish
the selected source commits. Source, command, binary, version, ELF and OCI checks
passed. Original prior-operation inputs remained unchanged.

Spec (`/root/source_transport_spec`) and Standards (`/root/aut01_standards`)
independently accepted this scoped artifact evidence. Spec replayed all three
`native_build` checks. Standards verified the ordered command/copy/version
bindings and OCI binary contents. All 208 command records succeeded. Seven
cleanup records completed; supervisor 22841, worker group 22843 and containers
with nonce `05415ed461e51ef0c1e64032ee47304e4116dcddefbba1b13bcb78812c02303b`
are absent. There were no additional recorded candidate groups. No review
changed source artifacts or performed a native rerun.

This supports PR #119's verifier correction. It deliberately excludes the seven
other module closures, updater sweep, full preflight and native/NATS runtime
acceptance. It does not complete PR #107 or issue #11. The prior full operation
remains failed. A fresh full operation and its final artifact reviews are still
required before the parent task or downstream gate can advance.
