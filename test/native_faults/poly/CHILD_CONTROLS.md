# Portable child-process controls

Both native probe loops call the stdlib-only `children.go` helper with a
30-second child deadline. Preparation copies and hashes that exact file.
The helper uses `exec.CommandContext` and `Run`, including `Wait`, to kill
and reap timed-out children. It retains the final 2,000 stderr bytes during
collection, records a child error on execution or output failure, and
continues the job list.

Run the isolated behavioral controls with:

```sh
python3 test/mutate.py test/native_faults/poly/children.mutants.json --output NEW_OUTPUT
```

The standard Go backend copies only `children.go` and `children_test.go`
into an isolated module. The test binary launches itself as real hanging,
nonzero-exit, missing-output, malformed-output, null-output and successful
children. Controls verify process termination and reaping, retained error
identity and diagnostic tail, bounded diagnostic collection, and successful
serialization of a later result. Tests use Unix process APIs on the recorded
host toolchain; they provide portable functional evidence, not native Bor
profile acceptance. Native preparation and execution remain separate.

The test child budget is 150 milliseconds. A three-second outer test bound
kills a deliberately unbounded mutant and waits for the collector before
failing. This cleanup bound is not an observation used to declare success.
