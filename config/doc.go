// Package config is the E06-07 output: the strict YAML schema v1 and the
// side-effect-free validator of the live and gateway role configuration
// (LIVE_CONFIG_SPEC, "Общая схема YAML v1"). One YAML file describes one
// network and one role; the environment starts the node and hands the
// file to the embedded adapter, and `extractor config check` validates it
// without creating any database, stream, directory or run.
//
// The closed rules this package owns:
//
//   - Unknown and duplicate keys are rejected at every level, scalars
//     must use the declared YAML type (an integer field rejects `1.0`),
//     and aliases, merge keys and multi-document streams are rejected:
//     there is no hidden substitution.
//   - Relative paths resolve against the directory of the YAML file, not
//     the process working directory (D78); only
//     `finality.polygon.sources_file` is absolute-only even when a file
//     sits next to the YAML (D78 field-specific restriction).
//   - The profile registry is the closed seven-profile table of D82;
//     public testnets are not aliases of their mainnets, and unknown
//     profiles such as the deferred Base/Arbitrum families are rejected
//     (D42).
//   - TLS blocks are explicit: `disabled` forbids the file fields,
//     `server` requires `ca_file`, `mutual` requires `ca_file`,
//     `cert_file` and `key_file`. The adapter-to-Gateway link allows
//     `mutual` only; server-only TLS is a gateway NATS option.
//   - `destination.binding_file` names the immutable desired
//     DestinationBinding descriptor of both roles; the gateway YAML
//     `stream` and `subject_prefix` must equal the descriptor exactly
//     (D54), and the local check verifies descriptor form and digest
//     without claiming any remote metadata is verified (D77).
//
// logs.go (E06-12) reports control states through the state names the
// native and delivery journals already persist: ADMITTED, EXEC_READY and
// the decision close on the native side, READY, SEALED, NATS_ACKED and
// RETIRED on the delivery side. There is no second parallel journal, no
// simplified RECEIVED/WAL_SAVED vocabulary, and no deduplication or
// completeness verifier.
//
// maintenance.go (E06-08) plans a configuration generation replacement
// without overwriting persistent state: it classifies the change as
// restart-only, as a destination cutover that needs a completed DRAIN, or
// as an incompatible replacement that is rejected before any write. The
// same file owns the DRAIN/Resume mode gate and the version preflight
// rules of the compatibility policy (an incompatible persistent store
// format is never allowed; an incompatible wire/plan upgrade happens only
// after the old build finished the drain).
//
// The package performs no network I/O and mutates nothing on disk. File
// reads are limited to the YAML file, the referenced TLS and credential
// files, the polygon sources file and the binding descriptor.
package config
