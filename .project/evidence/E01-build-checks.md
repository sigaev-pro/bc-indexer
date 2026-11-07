# E01-01.builds: standalone version-source checks

Date: 2026-11-07. Parent: [issue #11](https://github.com/sigaev-pro/bc-indexer/issues/11). Direct author: `d95-build-checks-author-001`. Base: `146746ee2a60d51450e7e3e4a96039c03c4ad689`, the accepted D95 workflow [PR #110](https://github.com/sigaev-pro/bc-indexer/pull/110).

## Block outcome

The trusted version-source reader can follow GitHub pagination from a configured named repository endpoint to its numeric repository alias. An HTTP 200 observation from the exact configured repository metadata URL must bind a positive non-boolean integer ID, exact `full_name`, and exact canonical named API URL. Retained observation hashes and source references remain required. The original origin and endpoint tail, 64-page bound, and ambiguous-next-link rejection remain enforced.

Loop detection uses the bound GitHub endpoint identity and normalized query ordering. A named page followed by its numeric alias with reordered query parameters is rejected as the same logical page. Non-GitHub behavior is unchanged. Synthetic vectors retain metadata and numeric second pages without claiming live-source or runtime results.

The updater, vectors, pagination tests, and pagination-only contract prose are extracted byte-for-byte from retained `db07c163d3d5679308d04bb9b9c3b967973333c0`. The existing build verifier already registers both updater modules, so its registration is unchanged. This block contains no controller, stop, ledger, or recovery changes.

## Failure history and checks

The earlier version-source check failed on legitimate GitHub numeric pagination. Independent review later identified an alias/query-order loop that raw URL tracking missed. Both failures are retained in the existing evidence; this extraction uses the repaired implementation and regression tests. It does not accept the wider legacy maintenance branch or replace the original failed run.

The combined unittest discovery of `test_build_pagination.py` and `test_build_updates.py` passed all 24 methods in 6.85 seconds (wrapper exit 0, 7.03 seconds). The seven pagination methods cover valid release/tag aliases, exact metadata and body hashes, unavailable or forged metadata, wrong repository/tail/origin, loops, ambiguous links, the 64-page limit, and unchanged non-GitHub path rules. The existing 17 updater methods cover synthetic check/update reports, compressed indexes, source provenance, and negative controls.

Focused commands:

```bash
python3 -m unittest discover -s .project/automation -p 'test_build_pagination.py'
python3 -m unittest discover -s .project/automation -p 'test_build_updates.py'
```

Documentation validation passed for 107 documents, 1,103 local links, 84 catalog entries, 16 normative mirrors, and unchanged original v2. Both working-tree and staged whitespace checks passed. Full CI runs on GitHub for the pushed commit; it is not repeated locally for this extraction. Independent Spec and Standards review remain required before merge.

| Extracted file | SHA-256 |
| --- | --- |
| `build_updates.py` | `d9a82fe76f1ddb60c362ce9135bd47ef9c11ad63cd1e61b9774d6d7227ad7198` |
| `build_update_vectors.py` | `c18a7c4946332337079216a766a1b9cf5921c1dea9793a9179be5f3bed1e5208` |
| `test_build_pagination.py` | `f73b3e8dde6d2bcc7a6e0a5184ed88ac8a7be80e4b61fd5c645b04012d8f4c9f` |

## Remaining parent work

This block fixes a standalone check; it does not complete issue #11 or unlock its dependents. Actual version-source verification, pinned native/supporting builds, manifests, and all applicable ETH/BSC/POLY profile evidence remain required. Live acquisition, native builds, and native/NATS runtime are NOT_RUN in this block. The canonical run, original failed candidate, product sources, active locks, and specification remain unchanged. Disposable test fixtures were cleaned up. Next: publish this draft, obtain both independent exact-head reviews and GitHub CI, then let the coordinator merge through protection and continue direct build verification.

## Docker Engine release-selection follow-up

Date: 2026-11-07. Author: `/root/boot03_controller`. Base:
`fe14767132587d93bac502a7782606879c1a962a`, which accepted the pagination block
above through [PR #111](https://github.com/sigaev-pro/bc-indexer/pull/111).
Branch: `build/engine-release-tags`. This remains a partial issue #11 block.

The coordinator reported that a live product check selected legacy `v28.5.2`
instead of the available Engine tag `docker-v29.9.3`. The product scripts have a
separate writer. This block independently reproduced the equivalent trusted
verifier defect from retained synthetic source observations: reversing those
two rows still selected `v28.5.2`. A second control showed that the generic
parser incorrectly admitted unrelated `api/v999.0.0` and `client/v999.0.0`
module tags. The existing parser stripped namespaces but did not understand
the Engine prefix. The active lock already records `docker-v29.9.3`; no pin
was changed to repair the check.

Before the source fix, three targeted regression methods exited 1 in 2.389
seconds with eight assertion failures and six errors. They covered numeric
selection, unwanted tag forms, and full report validation. The two-row control
failed in both orders, and valid modern-tag reports failed selection. These
failures remain part of this record; they are not native build failures.

The repair applies only to the `docker-engine` component from `moby/moby`.
It accepts complete `docker-vN.N.N` and legacy `vN.N.N` tags, compares their
three numeric components, and keeps the original tag for exact source
resolution. Drafts, prereleases, suffixes, partial versions, and unrelated
module tags are excluded. Existing source formats, synthetic transport,
pagination, source hashes, annotated-tag resolution, and candidate restrictions
remain intact. No verifier registration change was needed.

The focused command, run from `.project/automation`, was:

```bash
python3 -m unittest test_build_updates test_build_pagination
```

It passed all 30 methods in 10.684 seconds (wrapper exit 0, 10.89 seconds).
The six new methods include both tag orders, numeric minor-version ordering,
invalid-only sources, draft/prerelease flags and malformed flags, component
repository identity, unaffected source formats, and complete check/update
reports with authenticated tag objects. Report negatives reject older or
normalized-away tags and a forged peeled commit. The other 24 methods retain
the existing updater/vector and pagination controls. These are synthetic
checks, not live-source acceptance.

Documentation checks pass for 107 documents, 1,104 local links, 84 catalog
entries, 16 normative mirrors, and unchanged original v2. Whitespace checks
pass. Full CI is delegated to GitHub for the pushed head; independent Spec and
Standards review remain pending.

Native builds and runtime remain NOT_RUN. The earlier live failure, original
failed candidate, and canonical evidence remain unchanged. Disposable fixtures
were cleaned up, and no background command was started. Next: publish the
checked commit in a draft PR referencing #11, obtain GitHub CI and independent
Spec and Standards review, then continue direct builds after protected merge.
