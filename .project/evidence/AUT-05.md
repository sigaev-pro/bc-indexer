# AUT-05 updater fixture and provenance evidence

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

The compression-only commit changed the fixture, its tests, this report, and
project status. The expanded provenance repair below also changes the order of
checks in `build_updates.py`. The accepted component registry, active locks,
product worktree, canonical run, and ledger remain untouched. All 41 components
and existing source checks remain required. Fixtures are synthetic; native
builds and native/NATS runtime remain NOT_RUN. No live acquisition or service
was started.

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

The compression-only candidate was committed as
`dbd4c90f32ab68430f5b9448ed47f24b9581deec`. Its independent Spec review passed
under invocation `d93-aut05-spec-040`, with cleanup confirmed. That result remains
historical; both review axes must examine the expanded candidate below.

## Expanded provenance repair

The coordinator's next isolated diagnostic used product
`b67d89989268ba39acb3d4777e5416eb2ada6a01` and proposed verifier source `dbd4c90`.
Discovery completed, but validation failed after 45 controls with
`component source references are missing or unverified`. The retained report is
`aut05-provenance-diagnostic.md` in the canonical private D93 evidence directory.
Its cleanup was confirmed; it was not canonical verification or acceptance.

Reserved author invocation `d93-aut05-author-041` independently reproduced the
cause in a disposable fixture. The unchanged report passed and read Chart.yaml.
Adding only its exact immutable URL to `nats-chart.sources` caused rejection
before the validator read that URL. The source-reference gate ran before the
later Chart.yaml authentication in the same function.

The repair moves the existing source-reference gate after all required content
checks. Chart version, selected server version, unique metadata fields, exact
metadata object, immutable URL, actual bytes, and SHA-256 checks are unchanged.
Every component still needs nonempty sources already used by validation; merely
recording an observation does not satisfy that condition.

Five new test methods first produced eight failures and three errors on the
unrepaired source: direct check/update positives and the extra-observation control
failed early, while metadata negatives could not reach their intended checks.
After the ordering repair, all 17 focused updater tests passed in 6.28 seconds.
The new checks cover valid Chart.yaml provenance in both modes, changed declared
metadata, changed chart/server versions with recomputed hashes, missing/duplicate
version fields, missing observations/files, forged hashes/bytes, and a correctly
stored source that was never validated. All earlier compression and negative
tests remain in the suite.

Expanded-source portable CI exited 0 in 144.69 seconds: 292 tests passed with
two opt-in Docker skips, component checks passed, and 104 documents/1,086 local
links passed. Source checks covered 58 Python and 21 JSON files; the 84 catalog
entries, 16 normative mirrors, and original v2 file remain unchanged. This run
identified HEAD `dbd4c90f32ab68430f5b9448ed47f24b9581deec` with modified working
source. The following checked implementation bytes are unchanged by the final
status/report update and commit; final document checks run before handoff:

| Checked source | SHA-256 |
|---|---|
| `build_updates.py` | `7b4d681a0d414076910794e432734bcab3e47fd45981b51f9ba667eb2840d36c` |
| `test_build_updates.py` | `36c6537239994355dc9b0506817a059ef700e197a7a9df8d7cf06be5976eae0f` |

The compression fixture is byte-identical to `dbd4c90`. These checks establish
portable maintenance behavior only. Runtime remains NOT_RUN; no product files,
locks, canonical run, or ledger were changed, and no live acquisition occurred.

Both independent exact-head review axes and protected acceptance remain pending.

## Handoff

The coordinator owns review, publication, protected merge, and the safe accepted
source handoff before retrying the product diagnostic. Preserve the original
diagnostic and session history. This maintenance result does not accept a
product build or authorize an unrecorded runtime claim.
