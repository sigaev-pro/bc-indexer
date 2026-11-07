# AUT-05 synthetic updater transport evidence

Issue: [#103](https://github.com/sigaev-pro/bc-indexer/issues/103).
Authorization: D93. Author invocation: `d93-aut05-author-038`.
Branch: `aut/05-updater-fixtures`.
Accepted base: `21eb4a7deab2808c008321136be33feaeb23d29d`.
Date: 2026-11-07.

## Problem and change

The accepted updater contract permits gzip and XZ Debian package indexes and
resolves synthetic transport requests by exact URL. The active lock chooses
gzip for the base channel and XZ for security and updates, on both architectures.
The trusted success fixture supplied only gzip. An implementation preserving
those recorded URLs therefore received four missing-source responses before
native compilation. The failed E01-01.builds diagnostic remains retained by the
coordinator; this correction does not replace that result with a product PASS.

The fixture now serves both compressed forms for each of the three channels and
two architectures. Each pair is compressed from the same package-record bytes.
Transport references hash the actual compressed body, including its byte size.
The default expected candidate still selects one gzip index for each pair;
the alternate bodies are available to product implementations that choose XZ
or preserve the active lock's mixed selection. No compression preference is
added to the product contract or report validator.

Only the fixture, its tests, this report, and project status change. The accepted
component registry, active locks, product worktree, acceptance logic, canonical
run, and ledger remain untouched. All 41 components and existing source checks
remain required. Fixtures are synthetic; native builds and native/NATS runtime
remain NOT_RUN. No live acquisition or service was started.

## Verification

The first focused run used the new tests against the original fixture. It exited
1: one coverage assertion and eight errors exposed absent XZ observations,
including the four active-lock URLs. The errors include the dependent decoding
check after an absent body. This failed run is retained here as regression
evidence; the original eight tests were unchanged.

After the fixture repair, the same command passed all 12 tests in 3.55 seconds:

```sh
python3 -m unittest discover -s .project/automation -p test_build_updates.py
```

The four new methods independently check:

- All six active-lock index URLs and the complete set of 12 supported responses.
- Equal decompressed records, real package/architecture coverage, distinct
  compressed hashes, and exact body hash/size references for both forms.
- Complete check and update reports using all gzip, all XZ, or active-lock mixed
  URLs, retaining all 41 components and NOT_RUN compatibility.
- Rejection when an index claims the other representation's hash despite equal
  decompressed content.

Full portable CI exited 0 in 124.59 seconds on the checked working source:
287 tests passed with two opt-in Docker tests skipped, component checks passed,
and 104 documents/1,084 local links passed. Source checks covered 58 Python and
21 JSON files. All 84 catalog entries, 16 normative mirrors, and the original
v2 file remain unchanged. Runtime remains NOT_RUN.

```sh
python3 .project/automation/ci.py
git diff --check
```

The CI report identifies base HEAD `21eb4a7deab2808c008321136be33feaeb23d29d`
with modified working source. These implementation bytes are the ones committed
in the handoff; the only edits after that CI run record its result in status and
this report, followed by a document check:

| Checked source | SHA-256 |
|---|---|
| `build_update_vectors.py` | `214d6152623c4e686cf8eb1bb62db3aed52ffb32e6fb237425ff5b861f4f1711` |
| `test_build_updates.py` | `0517cad6e9dc22aea894b75dd7bee83115258102f5a8d95b3e9108354e3e0ac0` |

Both independent exact-head review axes and protected acceptance remain pending.

## Handoff

The coordinator owns review, publication, protected merge, and the safe accepted
source handoff before retrying the product diagnostic. Preserve the original
diagnostic and session history. This maintenance result does not accept a
product build or authorize an unrecorded runtime claim.
