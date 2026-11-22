package poly

import "errors"

// Sentinel errors of the normalizer. Every rejection maps to one of them so
// callers can distinguish violation classes without matching message text.
// A known error is a local fatal condition before READY: the normalizer
// never publishes a successful record with empty mandatory details in place
// of an error (MESSAGE_CONTRACT section 3, capture_status row).
var (
	// ErrCapture marks malformed capture input: an inconsistent or
	// incomplete native observation that cannot be normalized.
	ErrCapture = errors.New("poly: malformed capture")

	// ErrTypeNotApplicable marks a native transaction type outside the
	// closed Polygon body registry for the observed fork state.
	ErrTypeNotApplicable = errors.New("poly: transaction type not applicable in the Polygon profile")

	// ErrStateSyncMismatch marks a body StateSync transaction that does not
	// match the native state-sync records of its block (the native import
	// refuses the same condition with its state-sync mismatch error).
	ErrStateSyncMismatch = errors.New("poly: state-sync mismatch")

	// ErrAuthorization marks source authorization tuples in a profile where
	// EIP-7702 is not active: the closed empty profile rejects instead of
	// dropping them.
	ErrAuthorization = errors.New("poly: authorization tuples are not applicable in the Polygon profile")

	// ErrOrder marks a violation of the shared D74 execution_order rules.
	ErrOrder = errors.New("poly: execution order violation")

	// ErrScope marks a violation of the rollback scope tree rules.
	ErrScope = errors.New("poly: rollback scope violation")

	// ErrLogScope marks a violation of the D75 source_log_scopes rules.
	ErrLogScope = errors.New("poly: source log scope violation")

	// ErrLegacyDisabled marks an optional legacy Bor receipt record that was
	// requested while the optional scope is OFF (the default).
	ErrLegacyDisabled = errors.New("poly: legacy Bor receipt records are disabled (optional scope is OFF)")

	// ErrLegacyNotFixtured marks an enabled optional scope without the
	// declared typed fixtures E04-02 requires.
	ErrLegacyNotFixtured = errors.New("poly: enabled optional scope requires declared typed fixtures")

	// ErrLegacySource marks a legacy Bor receipt source read that is corrupt
	// or inconsistent; it is never converted into an empty successful record.
	ErrLegacySource = errors.New("poly: legacy Bor receipt source error")

	// ErrBlock marks block-level incoherence between the header, the body
	// inventory and the Bor context.
	ErrBlock = errors.New("poly: block incoherence")
)
