# protocol/profiles — E02-10.closed_profiles (#25)

Closed registry v1 of payload variants, receipt encodings (D75) and
network-detail profiles for ETH/BSC/POR (Bor). Normative anchors:
MESSAGE_CONTRACT.md sections 3.1-3.2 and 4; TRANSACTION_TYPE_REGISTRY.md.

Design invariants (each carries a test):
- Value 0 of every enum is INVALID and never a default success.
- Unknown registry version, family, kind, variant or details selection is
  a closed-registry error; there is no Any/JSON fallback and no invented
  zero-value field.
- Receipt encoding native_type presence is fixed by the D75 table: kind 1
  always carries native_type 0; kind 2 always carries a present native_type
  1..127 (missing is a schema error per AC-DAT-02-03; Bor StateSync pins
  127); kind 3 (optional Bor legacy storage) never carries native_type.
  Kinds 1/2 are forbidden in the auxiliary slot and kind 3 is forbidden in
  body receipts; the closed Origin enum (BODY_MANDATORY /
  BOR_LEGACY_SYNTHETIC) mirrors the D75 applicability.
- Network details are a closed oneof per family; ETH and BSC are
  explicitly-empty typed variants, Bor carries source_log_scopes.
- BlockComplete and CoverageUpdated have no payload variant (excluded by
  D25).
- SourcesBindingDigest (D80/IR-02) is the mandatory Hash32 = SHA-256 of
  the exact verified sources_file bytes, required by
  SourceObservationRef and L1Checkpoint; absence is a schema error,
  whitespace changes the digest, and recovery reads the retained digest,
  never the current file.

Provenance: family receipts and variants derive from the accepted E01
evidence (run records cited in .project/evidence/E01-05.type_paths.*.md);
the fixture-independent registry values follow the normative tables.

Open question for review: whether OptionalBorLegacy should also be
enumerated inside the Bor profile's Receipts (currently it is only
reachable through the constructor, mirroring its auxiliary-only origin).
