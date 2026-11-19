# Portable controls for the installed Bor guard hook

These tests exercise the real installed admission package's mutex behavior.
They complement the native Bor vote/GoLive handoff; they do not give native
or public-profile credit. The generated package is a source snapshot, not a
Git commit. The mutation report records the host Go version separately from
the accepted native compiler.

For each campaign, create a fresh owned directory outside the checkout.
Copy `admission.go` and `canonical.go` from the retained passing preparation's
`bor/source/extractor/admission` directory. Verify `admission.go` equals the
repository admission source with exactly the `guard_contention_probe` patch
applied, and verify `canonical.go` equals the repository source. Retain a
`sources.json` with the preparation path and SHA-256, installed-source paths
and SHA-256 values, and the control source commit. Copy this directory's
test and mutation table beside those two files and add a minimal `go.mod`
for module `bor-guard-control` with the recorded host Go version.

Run `test/mutate.py SNAPSHOT/guard_hook.mutants.json --output NEW_OUTPUT`
with offline Go settings and a dedicated disposable compiler cache. Preserve
the generated module, binding record, mutation report and command logs. The
baseline runs three tests: nil hook, uncontended acquisition, and genuine
contention. Four mutants separately remove each hook event or each locking
path. Missing contention uses a one-second failure bound; successful tests
use channel ordering and mutex inspection, not elapsed time as evidence.

The acquired callback waits until the original holder has released, then
checks the same mutex directly. This prevents a removed blocking `Lock`
from appearing valid merely because the original holder still owns it.
