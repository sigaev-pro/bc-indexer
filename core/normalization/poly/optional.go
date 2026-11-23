package poly

// Options declares the optional scope of the normalizer (E04-02).
//
// The default is OFF and OFF is the mandatory verified case: with a zero
// Options value the normalizer publishes the mandatory transaction scope
// only — body transactions with their full native effects, including the
// post-Madhugiri body StateSync transaction — and no outside-tx records.
// Turning the optional scope off never changes native transaction effects.
//
// Enabling the optional legacy Bor receipt requires the declared typed
// fixtures of the enabled branch: an enabled scope without fixtures is an
// error, and enabling the record never mixes it into the body inventory.
type Options struct {
	// LegacyReceipt enables the optional BorLegacyReceiptRecord output.
	LegacyReceipt bool
	// DeclaredFixtures lists the typed fixture identifiers the enabled
	// branch is declared with (the POLY-L01..L07 shapes of E00-15 and their
	// successors).
	DeclaredFixtures []string
}

// IsOff reports whether the optional scope is fully off (the default).
func (o Options) IsOff() bool { return !o.LegacyReceipt }
