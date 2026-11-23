// Package importpath is the import-pressure area of the BSC normalization
// module (E04-04.import_pressure).
//
// It streams a full block import through the normalizer with a bounded
// in-flight window: one record is normalized, emitted and released before
// the window frees the next slot, so collector memory stays bounded no
// matter how many transactions a block holds, and native results never
// depend on the window size. Capture recovery re-normalizes the same
// corpus from scratch and compares the record digests prefix-wise: the
// counters are never continued after already repeated operations.
//
// Normative anchors:
//
//   - .project/docs/TASK_DECOMPOSITION.md, E04-04.import_pressure: the
//     full import path and large-field/slow-worker behavior; native
//     results do not change, collector memory stays bounded, system
//     scopes are not mixed
//   - .project/docs/CURRENT_SEMANTICS.md, D74: capture recovery keeps the
//     same logical IDs/orders/scopes for the same corpus; repeated
//     execution verifies the prefix instead of continuing counters
package importpath

import (
	"errors"
	"fmt"

	bsc "github.com/sigaev-pro/bc-indexer/core/normalization/bsc"
	bscblock "github.com/sigaev-pro/bc-indexer/core/normalization/bsc/block"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// Sentinel errors of the import path.
var (
	ErrWindowInvalid  = errors.New("bsc/importpath: the in-flight window must be positive")
	ErrSinkNil        = errors.New("bsc/importpath: the sink is mandatory")
	ErrPrefixMismatch = errors.New("bsc/importpath: a recovered record differs from the retained prefix")
	ErrSystemScopeMix = errors.New("bsc/importpath: a system transaction and an ordinary transaction share one record")
)

// Sink receives the normalized records of one import. Emit blocks until it
// accepts the record: that backpressure is what bounds collector memory.
type Sink interface {
	// EmitRecord receives one normalized transaction record.
	EmitRecord(record *bsc.Record) error
	// EmitBlock receives one projected block record.
	EmitBlock(metadata block.BlockMetadata) error
}

// BlockInput is one observed block of the import: the header facts, the
// transaction observations in body order and the Parlia family facts.
type BlockInput struct {
	Header       bscblock.SourceHeader
	Transactions []*bsc.Observation
	ParliaFacts  bscblock.ParliaFacts
}

// Runner streams block imports through the normalizer with a bounded
// in-flight window. The window bounds how many normalized records exist
// at once; the normalized output is identical for every window size.
type Runner struct {
	// Normalizer folds each observation.
	Normalizer *bsc.Normalizer
	// Window is the maximum number of in-flight records. It must be
	// positive.
	Window int
}

// New returns a runner with the default normalizer and the given window.
func New(window int) (*Runner, error) {
	if window < 1 {
		return nil, fmt.Errorf("%w: %d", ErrWindowInvalid, window)
	}
	return &Runner{Normalizer: bsc.New(), Window: window}, nil
}

// Import streams one block through the normalizer. Each transaction is
// normalized alone: the observation is released as soon as its record is
// emitted, so the peak memory is the window, not the block. The Parlia
// system transactions of the block normalize through their own
// records — the system scope never mixes into an ordinary record and no
// outside-tx stream replaces a mandatory body transaction.
func (r *Runner) Import(input BlockInput, sink Sink) error {
	if sink == nil {
		return ErrSinkNil
	}
	metadata, err := bscblock.Project(input.Header, uint64(len(input.Transactions)), input.ParliaFacts)
	if err != nil {
		return err
	}
	if err := sink.EmitBlock(metadata); err != nil {
		return err
	}
	seenSystem := false
	for i, obs := range input.Transactions {
		if obs == nil {
			return fmt.Errorf("transaction %d: %w", i, ErrSinkNil)
		}
		record, err := r.Normalizer.Normalize(obs)
		if err != nil {
			return fmt.Errorf("transaction %d: %w", i, err)
		}
		// A system transaction is the block's own body boundary: it never
		// follows an ordinary transaction in the retained fixture order,
		// and an ordinary transaction never follows the system boundary
		// inside one record. Mixing them would smear the system scope
		// into a user record.
		switch record.Classification {
		case bsc.ClassificationParliaSystem:
			seenSystem = true
		case bsc.ClassificationOrdinary:
			// The retained fixture order puts every user transaction
			// before the Parlia system transactions of the block; a user
			// record after the system boundary would mix the scopes.
			if seenSystem {
				return fmt.Errorf("transaction %d: %w: ordinary transaction after the system boundary", i, ErrSystemScopeMix)
			}
		default:
			return fmt.Errorf("transaction %d: %w: %s", i, ErrSystemScopeMix, record.Classification)
		}
		if err := sink.EmitRecord(record); err != nil {
			return err
		}
	}
	return nil
}

// Recover re-imports the same corpus after a capture recovery and checks
// it against the retained record digests: every re-normalized record must
// equal the retained prefix digest-for-digest. The counters of the new
// run are its own; nothing continues after the already repeated
// operations, which is why the comparison is a fresh prefix check and not
// a counter diff. A shorter retained prefix is checked as far as it goes;
// the digests beyond it belong to the new run alone.
func (r *Runner) Recover(blocks []BlockInput, sink Sink, retainedPrefix [][32]byte) error {
	if sink == nil {
		return ErrSinkNil
	}
	index := 0
	for bi := range blocks {
		if err := r.Import(blocks[bi], checkingSink{sink: sink, prefix: retainedPrefix, next: &index}); err != nil {
			return err
		}
	}
	return nil
}

// checkingSink compares every emitted record digest against the retained
// prefix in order.
type checkingSink struct {
	sink   Sink
	prefix [][32]byte
	next   *int
}

func (c checkingSink) EmitRecord(record *bsc.Record) error {
	if *c.next < len(c.prefix) {
		want := c.prefix[*c.next]
		got := record.Digest()
		if want != got {
			return fmt.Errorf("%w: record %d", ErrPrefixMismatch, *c.next)
		}
	}
	*c.next++
	return c.sink.EmitRecord(record)
}

func (c checkingSink) EmitBlock(metadata block.BlockMetadata) error {
	return c.sink.EmitBlock(metadata)
}
