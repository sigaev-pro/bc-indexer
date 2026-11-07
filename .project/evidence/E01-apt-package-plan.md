# E01-01.builds: APT archive-plan parsing

Date: 2026-11-08 (Europe/Moscow). Parent: [issue #11](https://github.com/sigaev-pro/bc-indexer/issues/11).
Author: `/root/boot03_controller`. Branch: `build/apt-package-plan`.
Base: `4a767f64d8742cba0d79c7fc6bcf7979ee19875c`, accepted PR #114.

## Actual failure and diagnosis

Operation `0973d8349b6f10a74b7e6c5ebb2a8b4b` in
`run_D95_builds_1794083568` failed before compilation. Command 241 ran APT
with `--download-only --print-uris` and exited 0. Its 4,113-byte stdout has
SHA-256 `e5530ded3ee44b3bfdf030f74f199fc1e8a8384bf7ee8d935e46973be70a3293`.
The output contains 19 archive rows. The first zlib row and two other security
rows have three fields; stable-package rows include an MD5Sum fourth field.
The trusted helper incorrectly required exactly four fields.

The retained zlib index record has the expected size 88,720 and SHA-256
`8c27c342230503b73dfde629642c3b352e87b554e06d60cf58772f236a305ceb`,
but no MD5sum. The adjacent bzip2-doc record has both SHA-256 and MD5sum.
APT's [print-URI source](https://sources.debian.org/src/apt/3.3.5/apt-private/private-install.cc)
emits its available display hash after the archive size. The saved output and
index fields establish that the missing display hash is not missing package
authentication. The source reference explains the format; it is not a claim
about a newly executed APT version.

The original failed run remains unchanged. Its retained result reports
`cleanup_complete: true`; the coordinator independently confirmed absence of
the owned supervisor, worker, and candidate groups. This repair does not turn
the failed operation into PASS.

## Change and verification

The helper accepts three fields or four with a syntactically valid display
digest. It still requires membership in the authenticated exact index and
matching archive size. The signed index's SHA-256 remains the archive and
cache integrity authority. No optional MD5 digest is used for authorization.
The source URI is confined to the configured local mirror and decoded once;
APT's destination filename retains its encoded epoch. Malformed fields,
query/fragment/foreign URI components, traversal, invalid escapes, unsafe
basenames, and cache symlinks fail closed.

A red-first test used the exact first two saved APT rows with disposable
archive bytes and independent SHA-256 records. Before the fix it exited 1 at
the four-field requirement. After the fix, six new methods cover both real
row shapes and encoded names; missing digest with incorrect size/membership;
changed archive bytes for both shapes; malformed and unsafe paths; optional
digest versus signed SHA-256; and existing/dangling cache symlinks.

The focused parser, executor, material, and retained-snapshot suites passed
all 34 methods in 1.143 seconds (wrapper exit 0, 1.53 seconds). Command:

```bash
cd .project/automation
python3 -m unittest test_build_apt_plan test_build_execution test_build_materials test_build_package_snapshot
```

These tests use disposable bytes, not package installation or native builds.
Temporary fixtures were removed. No background process was started. No pin,
product, corpus, controller, or historical evidence changed. Full build and
runtime remain NOT_RUN in this author block. Documentation/whitespace checks
and the publication checkpoint are recorded in PROJECT_STATUS.

Next: publish the draft referencing #11; let the coordinator run a fresh
bounded package-plan/install diagnostic on the frozen head, then obtain both
independent reviews and required CI before protected acceptance. Issue #11
remains incomplete.
