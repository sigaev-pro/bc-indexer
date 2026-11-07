# AUT-02: Reader boundary artifact verification

Issue: [#95](https://github.com/sigaev-pro/bc-indexer/issues/95). Authorization: D92. Existing requirement: D87 in [TESTING_CONTRACT](../docs/TESTING_CONTRACT.md). This maintenance issue is outside the fixed 84-task catalog.

Status: READY_FOR_REVIEW. This is a trusted artifact interface and verifier extension. It does not implement the product schema or accept a native/NATS runtime result.

## Change

The [reader evidence contract](../automation/reader-evidence-contract.md) defines the required schema version 1 artifact, its six separate conditions, exact profile/case binding, hashed chunks, phase records, and noncircular RunManifest output binding. It preserves honest NOT_RUN, FAIL, incomplete, and predeclared negative-case evidence. Reader completion and semantic success remain separate.

The trusted driver adds 80 independent D87 inputs: 12 valid controls and 68 invalid mutations. Controls cover all seven profiles, an empty stream interval, preparation NOT_RUN, incomplete delivery with FAIL, a completed reader with a semantic FAIL, and a declared negative case. Mutations exercise evidence omission, changed/missing chunks, duplicate identities, fake completion, incorrect output binding, profile/case mismatch, and phase deadlines. They are synthetic artifact inputs, never actual native or stream evidence.

The original 54 vectors remain byte-identical under their canonical encoding, with SHA-256 `b02e755d6e728366a742fc16fff5d64946cbfa6a3d3e1f8d0c1ddfb1a39fc27c`. The expanded driver has 134 vectors and four profile expansions. Required product outputs now include the reader schema. The contract and vector module are included in the frozen trusted pack, so changing them requires a reviewed configuration handoff.

## Local verification

Commands ran in the isolated AUT-02 worktree based on `a076de93d948b08892171fd84dd5545cc344eaf6`:

- `python3 -m unittest discover -s .project/automation -p test_reader_evidence_vectors.py`: PASS, 10 tests.
- `python3 .project/automation/ci.py`: PASS, 171 tests, 54 component negatives, 94 documents, 1,058 local links, 16 mirrors, 63 REQ, 217 AC, 68 product tasks, 84 catalog entries, and unchanged original v2. Measured command duration: 92.19 seconds. This run validated the modified source before this evidence note was added.

The protocol-stub tests validate driver plumbing and rejection behavior. Their lookup table is explicitly a disposable bootstrap fixture; it is not the product validator and cannot establish E01 acceptance. The real schema candidate still requires its own independent review and the actual supervised trusted driver proof.

The coordinator also ran this trusted driver against the independently authored E01 schema's working source. All 138 conformance outcomes passed in 16.15 seconds, with runtime NOT_RUN and vector digest `7c2058cabf4f5fba1398cc3c5e5188cdeb7c1f2a1ba400248b5aea3aceb7f1b9`. This was a diagnostic before the product's final commit, not an exact-head acceptance capture. The source and product still require their separate reviewed acceptance paths.

## Remaining acceptance

The integrated tree passed `python3 .project/automation/ci.py` with exit 0 in 103.46 seconds: 205 tests and the required component checks. This check ran after conflict resolution and before this evidence/status update. Both reader-required output validation and the separate product unittest command remain in the task recipe. Controller reservations `d92-aut02-spec-012` and `d92-aut02-standards-013` succeeded before review dispatch; the canonical run advanced to revision 5 with 13 consumed invocations, without changing its epoch or deadline.

Accepted AUT-01 main `c9ebf91044b72c0d554707ebc735a5b0ec77dff7` is now integrated. Conflict resolution retains the reader schema in required outputs and AUT-01's explicit isolated product unittest command; all prior launch fixes and history remain intact. The canonical D92 run was initialized STOPPED with 11 consumed invocations, configuration epoch 1, revision 3, and the original deadline `1794032247`. The completed D86 run and original BOOT-03 dependency receipt are unchanged. This integration does not adopt a new live configuration or accept the product task.

Review the final integrated commit independently; pass protected CI; retain pre-merge capture and verified merge facts. Then adopt the updated trusted source through the supported safe handoff before accepting E01-01.evidence. Runtime source closure, producer durability, terminal fence conformance, full stream coverage, reconstruction, and semantic comparison remain NOT_RUN here.
