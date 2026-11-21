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
  always carries native_type 0; kind 2 carries 1..127 (Bor StateSync pins
  127); kind 3 (optional Bor legacy storage) never carries native_type and
  is forbidden in body receipts.
- Network details are a closed oneof per family; ETH and BSC are
  explicitly-empty typed variants, Bor carries source_log_scopes.
- BlockComplete and CoverageUpdated have no payload variant (excluded by
  D25).
- SourcesBindingDigest (D80) is the mandatory Hash32 over the
  domain-separated canonical encoding of registry, family, source commit
  and ordered observation keys; identical inputs are deterministic and
  any input change changes the digest.

Provenance: family receipts and variants derive from the accepted E01
evidence (run records cited in .project/evidence/E01-05.type_paths.*.md);
the fixture-independent registry values follow the normative tables.

Open question for review: whether OptionalBorLegacy should also be
enumerated inside the Bor profile's Receipts (currently it is only
reachable through the constructor, mirroring its auxiliary-only origin).
