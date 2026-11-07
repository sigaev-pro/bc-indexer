# E01-01.builds: standalone GitHub pagination check

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
