package importpath

import (
	"errors"
	"testing"
	"time"

	bsc "github.com/sigaev-pro/bc-indexer/core/normalization/bsc"
	bscbalance "github.com/sigaev-pro/bc-indexer/core/normalization/bsc/balance"
	bscblock "github.com/sigaev-pro/bc-indexer/core/normalization/bsc/block"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/calls"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/receipt"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/state"
	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

var (
	payer     = balance.Address20{0x7e}
	recipient = balance.Address20{0x40}
	coinbase  = balance.Address20{0x68}
	valSet    = balance.MustParseAddress("0000000000000000000000000000000000001000")
	reward    = balance.MustParseAddress("0000000000000000000000000000000000001002")
	burn      = balance.MustParseAddress("000000000000000000000000000000000000dead")
)

func u(v uint64) balance.UInt256 { return balance.UInt256FromUint64(v) }

func mut(addr balance.Address20, before, after uint64) bscbalance.Mutation {
	return bscbalance.Mutation{Address: addr, Before: u(before), After: u(after)}
}

// oneOrdinaryObservation builds the corpus-shaped ordinary transfer used
// by the import fixtures.
func oneOrdinaryObservation(t *testing.T, position uint64, payerBefore uint64) *bsc.Observation {
	t.Helper()
	target := recipient
	owner := uint64(0)
	status := true
	gasUsed := uint64(20000)
	return &bsc.Observation{
		Envelope: bsc.EnvelopeFacts{
			Position: position, NativeType: 0, Wire: []byte{0xd0},
			Sender: payer, SenderProvenance: bsc.ProvenanceNativeDerived,
			Target: &target, TargetProvenance: bsc.ProvenanceNativeDerived,
			Value: u(5), Nonce: position, GasLimit: 21000, EffectiveGasPrice: u(2),
		},
		Coinbase: coinbase,
		Events: []bsc.Event{
			{Group: &bscbalance.Group{OpID: 1, Role: bscbalance.RoleGasPurchase, NativeSnapshot: 0,
				Mutations: []bscbalance.Mutation{mut(payer, payerBefore, payerBefore-42000)}}},
			{FrameEntry: &calls.Frame{Depth: 0, Engine: balance.EngineEVM, Kind: calls.KindCall,
				CallerAddress: &payer, Target: &target, Outcome: calls.OutcomeSuccess}},
			{Snapshot: &bsc.SnapshotEvent{NativeID: 1, Kind: bsc.SnapshotCreate, OwnerFrameIndex: &owner}},
			{Group: &bscbalance.Group{OpID: 2, Role: bscbalance.RoleTransfer, FramePresent: true, FrameID: 0, NativeSnapshot: 1,
				Mutations: []bscbalance.Mutation{mut(payer, payerBefore-42000, payerBefore-42005), mut(recipient, 0, 5)}}},
			{Snapshot: &bsc.SnapshotEvent{NativeID: 1, Kind: bsc.SnapshotEnd}},
			{Group: &bscbalance.Group{OpID: 3, Role: bscbalance.RoleGasReturn, NativeSnapshot: 0,
				Mutations: []bscbalance.Mutation{mut(payer, payerBefore-42005, payerBefore-40005)}}},
			{Group: &bscbalance.Group{OpID: 4, Role: bscbalance.RoleFeeRecipientCredit, NativeSnapshot: 0,
				Mutations: []bscbalance.Mutation{mut(bscbalance.FeeRecipient, 0, 40000)}}},
		},
		Receipt: bsc.ReceiptFacts{
			Status: &status, Result: receipt.ResultSuccess,
			CumulativeGasUsed: 20000, GasUsed: &gasUsed,
			EffectiveGasPrice: u(2), NativeReceiptType: 0, ConsensusBytes: []byte{0xf9},
		},
		Boundary: []state.AccountChange{
			{Address: payer, ExistedBefore: true, ExistsAfter: true,
				BalanceBefore: u(payerBefore), BalanceAfter: u(payerBefore - 40005), NonceBefore: position, NonceAfter: position + 1},
			{Address: recipient, ExistsAfter: true, BalanceAfter: u(5)},
			{Address: bscbalance.FeeRecipient, ExistsAfter: true, BalanceAfter: u(40000)},
		},
	}
}

// oneSystemObservation builds a Parlia deposit: the system sender, zero
// gas price, the validator set contract, the 625/1000/10000 distribution.
func oneSystemObservation(t *testing.T, position uint64, swept uint64) *bsc.Observation {
	t.Helper()
	target := valSet
	caller := coinbase
	status := true
	input := append([]byte{0xf3, 0x40, 0xfa, 0x01}, make([]byte, 32)...)
	rewardShare := swept * 625 / 10000
	burnShare := swept * 1000 / 10000
	rest := swept - rewardShare - burnShare
	return &bsc.Observation{
		Envelope: bsc.EnvelopeFacts{
			Position: position, NativeType: 0, Wire: []byte{0xd0},
			Sender: coinbase, SenderProvenance: bsc.ProvenanceNativeDerived,
			Target: &target, TargetProvenance: bsc.ProvenanceNativeDerived,
			Value: u(swept), Input: input, Nonce: position, GasLimit: 30000,
			EffectiveGasPrice: balance.UInt256{},
		},
		Coinbase: coinbase,
		Events: []bsc.Event{
			{FrameEntry: &calls.Frame{Depth: 0, Engine: balance.EngineNativeProtocol, Kind: calls.KindNativeProtocol,
				CallerAddress: &caller, Target: &target, Outcome: calls.OutcomeSuccess}},
			{Group: &bscbalance.Group{OpID: 20, Role: bscbalance.RoleSystemExecution, NativeSnapshot: 0,
				Mutations: []bscbalance.Mutation{
					mut(coinbase, swept, 0),
					mut(reward, 0, rewardShare),
					mut(burn, 0, burnShare),
					mut(valSet, 0, rest),
				}}},
		},
		Receipt: bsc.ReceiptFacts{
			Status: &status, Result: receipt.ResultSuccess,
			CumulativeGasUsed: 30000, EffectiveGasPrice: balance.UInt256{},
			NativeReceiptType: 0, ConsensusBytes: []byte{0xf9},
		},
		Boundary: []state.AccountChange{
			{Address: coinbase, ExistedBefore: true, ExistsAfter: true,
				BalanceBefore: u(swept), BalanceAfter: balance.UInt256{}, NonceBefore: position, NonceAfter: position + 1},
			{Address: reward, ExistsAfter: true, BalanceAfter: u(rewardShare)},
			{Address: burn, ExistsAfter: true, BalanceAfter: u(burnShare)},
			{Address: valSet, ExistsAfter: true, BalanceAfter: u(rest)},
		},
		SystemAccounts: bsc.SystemAccounts{Validator: coinbase, ValidatorSetContract: valSet,
			RewardContract: reward, BurnAddress: burn},
	}
}

func importBlock(t *testing.T, txs ...*bsc.Observation) BlockInput {
	t.Helper()
	var parent block.Hash32
	parent[0] = 0x01
	var hash block.Hash32
	hash[0] = 0xbb
	count := uint64(len(txs))
	return BlockInput{
		Header: bscblock.SourceHeader{
			Hash: hash, ParentHash: parent, UncleHash: block.Hash32{0xdd},
			Beneficiary: block.Address20(coinbase),
			StateRoot:   block.Hash32{0x02}, TransactionsRoot: block.Hash32{0x03},
			ReceiptsRoot: block.Hash32{0x04}, LogsBloom: block.Bloom{0x05},
			Difficulty: block.UInt256{0x06}, Number: 12, GasLimit: 40000000,
			GasUsed: 100000, Time: 1795046400, ExtraData: []byte{0x07},
			MixDigest: block.Hash32{0x08}, Nonce: block.Nonce{0x09},
			SourceRLP: []byte{0xf9, 0x01}, BaseFee: &block.UInt256{},
		},
		Transactions: txs,
		ParliaFacts:  bscblock.ParliaFacts{SystemTransactionCount: &count},
	}
}

type collectingSink struct {
	blocks  []block.BlockMetadata
	records []*bsc.Record
	// release blocks until unblocked, modeling a slow worker.
	blockUntil chan struct{}
}

func (s *collectingSink) EmitRecord(record *bsc.Record) error {
	if s.blockUntil != nil {
		<-s.blockUntil
	}
	s.records = append(s.records, record)
	return nil
}

func (s *collectingSink) EmitBlock(metadata block.BlockMetadata) error {
	s.blocks = append(s.blocks, metadata)
	return nil
}

// The full import path: the block record and every transaction record of
// the corpus-shaped fixture, system transaction included, with the Parlia
// family observation on the block.
func TestImportStreamsFullBlock(t *testing.T) {
	runner, err := New(1)
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	sink := &collectingSink{}
	input := importBlock(t,
		oneOrdinaryObservation(t, 0, 50000),
		oneSystemObservation(t, 1, 10000),
	)
	if err := runner.Import(input, sink); err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(sink.blocks) != 1 || sink.blocks[0].SourceTransactionCount != 2 {
		t.Fatalf("block record drifted: %+v", sink.blocks)
	}
	if count, _ := sink.blocks[0].Details.Bsc.ParliaSystemTransactionCount.Get(); count != 2 {
		t.Fatalf("parlia observation drifted: %d", count)
	}
	if len(sink.records) != 2 {
		t.Fatalf("want 2 records, got %d", len(sink.records))
	}
	if sink.records[0].Classification != bsc.ClassificationOrdinary ||
		sink.records[1].Classification != bsc.ClassificationParliaSystem {
		t.Fatalf("classifications drifted: %s, %s",
			sink.records[0].Classification, sink.records[1].Classification)
	}
}

// An empty block is a first-class import: the block record publishes with
// count 0 and no transaction records.
func TestImportEmptyBlock(t *testing.T) {
	runner, _ := New(2)
	sink := &collectingSink{}
	if err := runner.Import(importBlock(t), sink); err != nil {
		t.Fatalf("empty import: %v", err)
	}
	if len(sink.blocks) != 1 || sink.blocks[0].SourceTransactionCount != 0 {
		t.Fatal("the empty block must publish with the observed zero count")
	}
	if len(sink.records) != 0 {
		t.Fatalf("an empty block emits no records, got %d", len(sink.records))
	}
}

// A slow worker back-pressures the import: the runner blocks on the sink,
// and no record is lost or reordered.
func TestImportSlowWorker(t *testing.T) {
	runner, _ := New(1)
	gate := make(chan struct{})
	sink := &collectingSink{blockUntil: gate}
	input := importBlock(t, oneOrdinaryObservation(t, 0, 50000))
	done := make(chan error, 1)
	go func() { done <- runner.Import(input, sink) }()
	select {
	case err := <-done:
		t.Fatalf("the import finished without the worker: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("import after unblock: %v", err)
	}
	if len(sink.records) != 1 {
		t.Fatalf("slow worker lost the record: %d", len(sink.records))
	}
}

// Capture recovery re-normalizes the same corpus and verifies the
// retained prefix digest-for-digest; the counters are never continued.
func TestRecoverVerifiesPrefix(t *testing.T) {
	runner, _ := New(1)
	first := &collectingSink{}
	blocks := []BlockInput{importBlock(t,
		oneOrdinaryObservation(t, 0, 50000),
		oneSystemObservation(t, 1, 10000),
	)}
	if err := runner.Import(blocks[0], first); err != nil {
		t.Fatalf("first import: %v", err)
	}
	prefix := make([][32]byte, 0, len(first.records))
	for _, r := range first.records {
		prefix = append(prefix, r.Digest())
	}

	second := &collectingSink{}
	if err := runner.Recover(blocks, second, prefix); err != nil {
		t.Fatalf("recovery of the same corpus failed: %v", err)
	}
	if len(second.records) != len(first.records) {
		t.Fatalf("recovery changed the record count: %d vs %d", len(second.records), len(first.records))
	}

	// A shorter retained prefix checks as far as it goes.
	third := &collectingSink{}
	if err := runner.Recover(blocks, third, prefix[:1]); err != nil {
		t.Fatalf("partial prefix recovery failed: %v", err)
	}

	// A corrupted prefix is rejected.
	tampered := append([][32]byte{}, prefix...)
	tampered[0] = [32]byte{0x01}
	fourth := &collectingSink{}
	if err := runner.Recover(blocks, fourth, tampered); !errors.Is(err, ErrPrefixMismatch) {
		t.Fatalf("tampered prefix error = %v", err)
	}

	// A different corpus cannot reuse the retained prefix: the first
	// record here is a system deposit, not the ordinary transfer the
	// retained prefix starts with.
	changed := []BlockInput{importBlock(t, oneSystemObservation(t, 0, 12345))}
	fifth := &collectingSink{}
	if err := runner.Recover(changed, fifth, prefix); !errors.Is(err, ErrPrefixMismatch) {
		t.Fatalf("changed corpus error = %v", err)
	}
}

// An ordinary transaction after the Parlia system boundary of the same
// block mixes the scopes of the two kinds and is rejected.
func TestImportRejectsScopeMixing(t *testing.T) {
	runner, _ := New(1)
	sink := &collectingSink{}
	input := importBlock(t,
		oneSystemObservation(t, 0, 10000),
		oneOrdinaryObservation(t, 1, 50000),
	)
	if err := runner.Import(input, sink); !errors.Is(err, ErrSystemScopeMix) {
		t.Fatalf("scope mixing error = %v", err)
	}
}

func TestWindowInvalid(t *testing.T) {
	if _, err := New(0); !errors.Is(err, ErrWindowInvalid) {
		t.Fatalf("window error = %v", err)
	}
}

func TestNilSinkRejected(t *testing.T) {
	runner, _ := New(1)
	if err := runner.Import(importBlock(t), nil); !errors.Is(err, ErrSinkNil) {
		t.Fatalf("nil sink error = %v", err)
	}
	if err := runner.Recover(nil, nil, nil); !errors.Is(err, ErrSinkNil) {
		t.Fatalf("nil sink recover error = %v", err)
	}
}
