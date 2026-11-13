// Extractor patch (E01-02.lifecycle_seams, Bor block): the write choke points
// of the lifecycle gate in the native accessors. The preparation adds this
// file to the accepted Bor tree; see adapters/bor/lifecycle/native/README.md.

package rawdb

import "github.com/ethereum/go-ethereum/log"

var (
	// ExtractorRequire, if set, checks a canonical or head write (effect
	// CANONICAL) against the lifecycle gate before the native accessor
	// writes. extractor/borlifecycle sets it for the one capture chain of
	// the process; a nil hook keeps the native behavior.
	ExtractorRequire func(effect string) error
	// ExtractorFinality, if set, wraps one native finality write (the
	// finalized block hash and the whitelist milestone, checkpoint, lock and
	// future-milestone records): it records a FINALITY decision intent for
	// key and its new value (deleted for a delete) before the write, and the
	// returned done writes the marker, syncs and resolves after it.
	ExtractorFinality func(name string, key, value []byte, deleted bool) (done func() error, err error)
)

// extractorRequireCanonical stops the process when a canonical or head write
// bypasses the gate: the native write fails closed.
func extractorRequireCanonical() {
	if ExtractorRequire == nil {
		return
	}
	if err := ExtractorRequire("CANONICAL"); err != nil {
		log.Crit("Extractor lifecycle refused a canonical write", "err", err)
	}
}

// extractorFinality returns the completion of one native finality write, or
// stops the process when the gate refuses it.
func extractorFinality(name string, key, value []byte, deleted bool) func() {
	if ExtractorFinality == nil {
		return func() {}
	}
	done, err := ExtractorFinality(name, key, value, deleted)
	if err != nil {
		log.Crit("Extractor lifecycle refused a finality write", "name", name, "err", err)
	}
	return func() {
		if err := done(); err != nil {
			log.Crit("Extractor lifecycle could not decide a finality write", "name", name, "err", err)
		}
	}
}
