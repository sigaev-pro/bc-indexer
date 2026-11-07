# E01-01.builds: retained signed package inputs

Date: 2026-11-07. Parent: [issue #11](https://github.com/sigaev-pro/bc-indexer/issues/11).
Author: `/root/boot03_controller`. Branch: `build/retained-package-snapshot`.
Accepted base: `d8c901e205a0957e6bac79ef3f6b120827527116` (PR #113).
This is a standalone trusted build-check repair under D95, not task acceptance.

## Observed failure

Actual operation `9676a622fe9d7f213db3d1a94773e819` in
`run_D95_builds_1794079699` exited 1 with
`official source payload differs from its immutable hash`. Its retained result
reports completed cleanup. The failure occurred during package-index
acquisition before container creation or compilation. Earlier successful
source checks and image acquisition do not convert this failed build into PASS.

Debian Security republished the mutable index URL after the candidate was
prepared. The plan pins index
`164725afc7af9fc86dfd019c09b84299dbdab681d3f086c94a411bf391b3bff0`.
The later publication contains
`28134e15f9ebb915d78602983560490300472ba465b7dab33782a83655ec4573`.
The trusted acquirer correctly rejected the new bytes, but had no way to use
the original retained pair. Refreshing pins repeatedly would not remove that
race or make the pinned build reproducible.

The earlier operation `72685b99f76898cf46c51efccbdb26a9` in
`run_D95_builds_1794077084` retained the original report and all three exact
linux/amd64 indexes. Its raw check-report digest is
`c22f2c1ab87f5f4f7886ecef60426778f094647932db54dedb481bb60b759d2e`.
The matching security `InRelease` digest is
`9975292378ba9ed0ab12ce5d30e653a2bd4580ed14b10b51f751c6c251dcc0e2`.
Its text names the pinned hash and declares a November 14, 16:46:01 UTC expiry.
Reading these fields is not cryptographic authentication. That earlier report
also contains a known incorrect Docker selection; neither that selection nor
an overall old verdict is an input to the package proof.

## Change and trust boundary

The trusted driver accepts paired `--package-source-report` and
`--package-source-report-sha256` arguments. It verifies the exact raw report,
real-source designation, unique official URLs, and selected HTTP 200 index and
InRelease observations. Selection uses the build plan's URL and pinned hash.
All three selected pairs must exist. Input reads are bounded, descriptor-relative,
and reject symlinks in files or ancestors and nonregular files such as FIFOs.
Unrelated historical body references are not opened.

The new proof retains a raw copy of the report and selected bodies. The
signature log binds their acquisition mode and exact original observation
metadata, including original times. Materialized validation checks that these
rows occur unchanged in the copied report and name the copied bytes by size
and digest. No fresh HTTP observation is invented. Once copied, the new proof
does not depend on the old directory.

Every run still executes the pinned image's `sqv` and keyring on raw InRelease
bytes. Authenticated Release output must bind the exact channel, index hash,
and size and pass current Date/Valid-Until checks. Stable trixie retains its
existing policy when Valid-Until is absent. Failed acquisition or signatures
cannot enable package installation. Complete package closure, archive hashes,
current live updater discovery, independent source refresh, final validation,
and cleanup remain mandatory. Ordinary fetching still uses the configured
URLs and exact pinned hashes.

The product BuildPlan and manifest shapes, active pins, corpus, controller,
and historical runs are unchanged. The acquisition field is inside the
already hash-bound trusted signature log. No shared cache service or product
implementation change is introduced.

## Checks and limitations

The initial test-harness run failed because its fake command method shadowed
unittest's `run`; that test-only naming error was corrected before the red
baseline. The two intended red controls then failed with one error and one
assertion: the old acquirer had no retained-input API and the CLI did not
recognize the paired options.

Thirteen new methods cover complete retained pairs despite an incomplete old
report and incorrect unrelated selection; exact report/body hashes; source,
channel, architecture, status, scope, and duplicate rejection; traversal,
ancestor/file symlinks, FIFOs, and size bounds; original metadata and copied
proof mutation; new signature failure; mixed publication, expiry, and future
Date rejection; ordinary live fetching; stop/copy interruption; and CLI option
pairing. Signature behavior is simulated in these portable tests. They do not
claim real cryptographic verification or native execution.

The broader trusted build suite initially passed 94 methods with two opt-in
Docker skips. A final focused run after adding explicit incomplete-acquisition
and copied-body checks passed 28 methods in 1.114 seconds. Final publication
checks then passed all 94 trusted build methods in 36.399 seconds, with the
same two opt-in Docker skips (exit 0, wrapper 36.73 seconds). Documentation
passed for 109 documents, 1,107 local links, 84 catalog entries, 16 normative
mirrors, and unchanged original v2. Whitespace checks passed. Commands:

```bash
python3 -m unittest discover -s .project/automation -p 'test_build*.py'
python3 .project/automation/check_docs.py
git diff --cached --check
```

Full CI runs on GitHub for the pushed head; independent Spec and Standards
review remain required.

A read-only copy diagnostic against the actual `c22f2c1a...` report used
disposable output while implementing the change. All three copied indexes
matched their pins, the original report digest remained unchanged, and
`signed` stayed unset. It performed no fetch, Docker command, signature check,
or build. The temporary directory was removed. Original evidence and active
inputs remain unchanged. Native builds and runtime are NOT_RUN in this block.

Next: publish the checked draft referencing #11, obtain exact-head CI and both
independent review axes, then let the coordinator start a new bounded actual
build with explicit retained input after protected merge. Do not resume or
relabel either historical failed operation.
