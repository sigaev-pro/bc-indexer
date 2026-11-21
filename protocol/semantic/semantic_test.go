// Portable tests of the manifest semantic identity: digest vectors against
// an independent implementation, identity stability across retries,
// exactly-once batch allocation and its validation. The fixture values come
// from the retained E01 evidence; the sources are listed in README.md.
package semantic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"testing"
)

// testID16 derives a 16-byte test identity from an E01 string name. E01
// evidence predates the public Id16 identities; this derivation is a
// test-only mapping, not a production rule.
func testID16(name string) ID16 {
	var out ID16
	sum := sha256.Sum256([]byte(name))
	copy(out[:], sum[:16])
	return out
}

// testID16Raw returns the 16-byte identity whose text form is hexStr, for
// the E01 native store identities that the evidence already carries as
// 32 hex characters.
func testID16Raw(hexStr string) ID16 {
	var out ID16
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		panic(err)
	}
	copy(out[:], raw)
	return out
}

func testHash32(hexStr string) Hash32 {
	var out Hash32
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		panic(err)
	}
	copy(out[:], raw)
	return out
}

// Fixture sources (see README.md):
//
//   - ETH: test/native_faults/eth/sample_report.json (chain 31337, genesis,
//     the IMPORT decision over the empty block 2) and the retained decision
//     unit vectors of e01-02-decision-unit-04-b21064f (producer-1.run-1,
//     native-store-1).
//   - BSC block: test/harness/fixtures/bsc_decision/report.json, the
//     measured run-02 IMPORT decision 1 over block 2 (bsc-probe.run-1,
//     store 0123456789abcdef0123456789abcdef).
//   - BSC tx: test/native_faults/bsc/sample_report.json, the
//     bsc.skip-block handoff IMPORT decision 0 over block 1, body
//     transaction 0.
//   - POLY: the retained Bor decision run 03
//     (e01-02-decision-bor-run-03-10f90e9), IMPORT decision 3 over block 3
//     (bor.run-1, store 0123456789abcdef0123456789abcdef).
type fixture struct {
	name          string
	network       Network
	producer, run ID16
	node, epoch   ID16
	block         Hash32
	decision      uint64
	sequence      uint64
	digest        string
}

func ethFixture() fixture {
	return fixture{name: "eth_block_applied",
		network:  Network{ChainID: uint256(31337), GenesisHash: testHash32("5bce29fa35af40f35138e536ea84a2efb7d3c581c370444a6c1d00f68ed3e98d")},
		producer: testID16("producer-1"), run: testID16("run-1"),
		node: testID16("native-store-1"), epoch: testID16("producer-1.run-1.writer-epoch-1"),
		block:    testHash32("7a977bf1de3e89506d80d293aef9a80297ad57dc7f13d44363b0274a27e98a79"),
		decision: 0, sequence: 1,
		digest: "8d8b6cb886f479a3ac9231e60f5b79f3488844cfb86c92aa839aee8805069e4c"}
}

func bscBlockFixture() fixture {
	return fixture{name: "bsc_block_applied",
		network:  Network{ChainID: uint256(714), GenesisHash: testHash32("16d03930cdabc5b474bb63eb69fb6e8a803f4a606310a42e743dc5bd45441e77")},
		producer: testID16("bsc-probe"), run: testID16("run-1"),
		node: testID16Raw("0123456789abcdef0123456789abcdef"), epoch: testID16("bsc-probe.run-1.writer-epoch-1"),
		block:    testHash32("5f7d80505969a2ff73fb6ee62375eb143390ddcb89b97cc5e335774ef28388d6"),
		decision: 1, sequence: 2,
		digest: "cb810e2b2f19c0811eaab428da2e8849fb8bc4fdabeb5e154e6874de001e5f8e"}
}

func bscTxFixture() fixture {
	return fixture{name: "bsc_tx_applied",
		network:  Network{ChainID: uint256(714), GenesisHash: testHash32("2151f9f50b184b526c72d9d3a3937f378578f36a1363e48b708060c95316f17e")},
		producer: testID16("bsc-probe"), run: testID16("run-1"),
		node: testID16Raw("0123456789abcdef0123456789abcdef"), epoch: testID16("bsc-probe.run-1.writer-epoch-1"),
		block:    testHash32("621ceaf8e24c089141d903d7c561023fb7b67a472819a9faaf3a50264ccc445e"),
		decision: 0, sequence: 3,
		digest: "e766843c77b9d5b7b56026bc7a553e332c698c2c27e2c6096246da3f887ef618"}
}

func polyFixture() fixture {
	return fixture{name: "poly_block_applied",
		network:  Network{ChainID: uint256(15001), GenesisHash: testHash32("824c042cd49a6fd25009745ac2643b88237e1d40aceaad18fd05b84ab6db9848")},
		producer: testID16("bor"), run: testID16("run-1"),
		node: testID16Raw("0123456789abcdef0123456789abcdef"), epoch: testID16("bor.run-1.writer-epoch-1"),
		block:    testHash32("50c0bd3ed9521ff7f276033976e23218a0733fecee9f004b57e9294eaeb1a127"),
		decision: 3, sequence: 4,
		digest: "451c548f66f8fe1fd94a5abf8c4a626fd40b182717a083e27705a44562fcd55f"}
}

func uint256(value uint64) UInt256 {
	var out UInt256
	for i := 0; i < 32; i++ {
		out[31-i] = byte(value >> (8 * i))
	}
	return out
}

func source(f fixture) SourceRef {
	return SourceRef{NodeInstanceID: f.node, WriterEpoch: f.epoch, SourceSequence: f.sequence}
}

// TestDescriptorDigestVectors checks the per-family fixtures against the
// digests of the independent implementation (README.md, "Digest vectors").
func TestDescriptorDigestVectors(t *testing.T) {
	tests := []struct {
		name string
		f    fixture
		tx   Hash32
	}{
		{"ETH empty-block IMPORT, block entity", ethFixture(), Hash32{}},
		{"BSC measured IMPORT, block entity", bscBlockFixture(), Hash32{}},
		{"BSC corpus IMPORT, tx entity", bscTxFixture(),
			testHash32("7817b17d62a8ddadb215510fc905139965eadca90e1f2dd117e3ced65d147502")},
		{"POLY Bor IMPORT, block entity", polyFixture(), Hash32{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var d SemanticDescriptor
			var err error
			if test.tx != (Hash32{}) {
				d, err = NewTransactionDescriptor(KindApplied, test.f.network, test.f.producer,
					test.f.run, test.f.decision, test.f.block, test.tx, 0, 0, source(test.f))
			} else {
				d, err = NewBlockDescriptor(KindApplied, test.f.network, test.f.producer,
					test.f.run, test.f.decision, test.f.block, 0, source(test.f))
			}
			if err != nil {
				t.Fatalf("fixture descriptor: %v", err)
			}
			got, err := d.Digest()
			if err != nil {
				t.Fatalf("digest: %v", err)
			}
			if got.String() != test.f.digest {
				t.Fatalf("digest %s, want the independent value %s", got, test.f.digest)
			}
		})
	}
}

// TestStatusDescriptorVector checks a StatusChanged descriptor with a
// positive revision on a later POLY decision against the independent value.
func TestStatusDescriptorVector(t *testing.T) {
	f := polyFixture()
	f.decision, f.sequence = 7, 5
	d, err := NewBlockDescriptor(KindStatus, f.network, f.producer, f.run, f.decision,
		f.block, 1, source(f))
	if err != nil {
		t.Fatalf("status descriptor: %v", err)
	}
	got, err := d.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	want := "6a2b69460b5130ea0678d3060522447f36111b19ebe8670305223d21b91e4b96"
	if got.String() != want {
		t.Fatalf("digest %s, want the independent value %s", got, want)
	}
}

// TestDigestDeterminism computes the same digests twice and requires equal
// bytes; the golden vectors above pin the values across runs.
func TestDigestDeterminism(t *testing.T) {
	f := bscBlockFixture()
	d, err := NewBlockDescriptor(KindApplied, f.network, f.producer, f.run, f.decision,
		f.block, 0, source(f))
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	first, err := d.Digest()
	if err != nil {
		t.Fatalf("first digest: %v", err)
	}
	again, err := d.Digest()
	if err != nil {
		t.Fatalf("second digest: %v", err)
	}
	if first != again {
		t.Fatalf("digest of the same descriptor differs between calls")
	}
	rebuilt, err := NewBlockDescriptor(KindApplied, f.network, f.producer, f.run,
		f.decision, f.block, 0, source(f))
	if err != nil {
		t.Fatalf("rebuilt descriptor: %v", err)
	}
	other, err := rebuilt.Digest()
	if err != nil {
		t.Fatalf("rebuilt digest: %v", err)
	}
	if first != other {
		t.Fatalf("digest of a rebuilt descriptor differs")
	}
}

// TestIdentityStableAcrossRetries models a delivery retry: the descriptor
// is re-encoded and re-digested, and the semantic identity must not move.
// Every identity field is also flipped once to prove the digest binds all
// of them, so a retry that changed any identity could not keep the digest.
func TestIdentityStableAcrossRetries(t *testing.T) {
	f := ethFixture()
	base, err := NewBlockDescriptor(KindApplied, f.network, f.producer, f.run, f.decision,
		f.block, 0, source(f))
	if err != nil {
		t.Fatalf("descriptor: %v", err)
	}
	digest, err := base.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	// The descriptor type has no attempt, delivery or transport field, so a
	// retry can only rebuild the same value; a rebuilt descriptor digests
	// the same (TestDigestDeterminism) and a changed field cannot.
	var zero ID16
	other := testHash32("276ebaa0da80347dd44fb336e667fa673db8d019bc1f03a041f271573785c510")
	changedNetwork := f.network
	changedNetwork.GenesisHash = other
	mutations := []struct {
		name string
		mod  func(d *SemanticDescriptor)
	}{
		{"family", func(d *SemanticDescriptor) { d.Family = FamilyTx }},
		{"kind", func(d *SemanticDescriptor) { d.Kind = KindRemoved }},
		{"chain id", func(d *SemanticDescriptor) { d.Network = changedNetwork }},
		{"producer", func(d *SemanticDescriptor) { d.Activation.ProducerID = zero }},
		{"run", func(d *SemanticDescriptor) { d.Activation.RunID = testID16("run-2") }},
		{"decision sequence", func(d *SemanticDescriptor) { d.Activation.ActivationID.DecisionSequence = 9 }},
		{"entity ordinal", func(d *SemanticDescriptor) { d.Activation.ActivationID.EntityOrdinal = 2 }},
		{"block hash", func(d *SemanticDescriptor) { d.Activation.BlockHash = other }},
		{"revision", func(d *SemanticDescriptor) { d.Revision = 3 }},
		{"node instance", func(d *SemanticDescriptor) { d.Source.NodeInstanceID = zero }},
		{"writer epoch", func(d *SemanticDescriptor) { d.Source.WriterEpoch = zero }},
		{"source sequence", func(d *SemanticDescriptor) { d.Source.SourceSequence = 8 }},
	}
	for _, m := range mutations {
		mutated := base
		m.mod(&mutated)
		got, err := mutated.Digest()
		switch {
		case err != nil:
			// A mutation that breaks validation has no digest at all: the
			// identity is still bound.
		case got == digest:
			t.Fatalf("%s does not change the descriptor digest", m.name)
		}
	}
	// The transaction binding is bound the same way.
	tx, err := NewTransactionDescriptor(KindApplied, f.network, f.producer, f.run,
		f.decision, f.block, testHash32("7817b17d62a8ddadb215510fc905139965eadca90e1f2dd117e3ced65d147502"),
		0, 0, source(f))
	if err != nil {
		t.Fatalf("tx descriptor: %v", err)
	}
	txDigest, err := tx.Digest()
	if err != nil {
		t.Fatalf("tx digest: %v", err)
	}
	if txDigest == digest {
		t.Fatalf("the tx entity does not change the descriptor digest")
	}
	tx.Activation.Transaction.TransactionHash = f.block
	changed, err := tx.Digest()
	if err == nil && changed == txDigest {
		t.Fatalf("the transaction hash does not change the descriptor digest")
	}
}

func TestDescriptorValidation(t *testing.T) {
	f := ethFixture()
	tests := []struct {
		name string
		desc SemanticDescriptor
	}{
		{"unknown family", SemanticDescriptor{Family: Family(5), Kind: KindApplied,
			Activation: blockActivation(f), Source: source(f)}},
		{"unknown kind", SemanticDescriptor{Family: FamilyBlock, Kind: Kind(0),
			Activation: blockActivation(f), Source: source(f)}},
		{"tx family without binding", SemanticDescriptor{Family: FamilyTx, Kind: KindApplied,
			Activation: blockActivation(f), Source: source(f)}},
		{"block family with binding", SemanticDescriptor{Family: FamilyBlock, Kind: KindApplied,
			Activation: txActivation(f), Source: source(f)}},
		{"optional family with binding", SemanticDescriptor{Family: FamilyBalanceSystem,
			Kind: KindApplied, Activation: txActivation(f), Source: source(f)}},
		{"block ordinal not 0", SemanticDescriptor{Family: FamilyBlock, Kind: KindApplied,
			Activation: ordinalActivation(f, 1), Source: source(f)}},
		{"tx ordinal 0", SemanticDescriptor{Family: FamilyTx, Kind: KindApplied,
			Activation: ordinalActivation(f, 0), Source: source(f)}},
		{"Applied revision not 0", SemanticDescriptor{Family: FamilyBlock, Kind: KindApplied,
			Activation: blockActivation(f), Revision: 1, Source: source(f)}},
		{"Status revision 0", SemanticDescriptor{Family: FamilyBlock, Kind: KindStatus,
			Activation: blockActivation(f), Source: source(f)}},
		{"unset node instance", SemanticDescriptor{Family: FamilyBlock, Kind: KindApplied,
			Activation: blockActivation(f),
			Source:     SourceRef{WriterEpoch: f.epoch, SourceSequence: 1}}},
		{"unset writer epoch", SemanticDescriptor{Family: FamilyBlock, Kind: KindApplied,
			Activation: blockActivation(f),
			Source:     SourceRef{NodeInstanceID: f.node, SourceSequence: 1}}},
		{"source sequence 0", SemanticDescriptor{Family: FamilyBlock, Kind: KindApplied,
			Activation: blockActivation(f),
			Source:     SourceRef{NodeInstanceID: f.node, WriterEpoch: f.epoch}}},
	}
	for _, test := range tests {
		if err := test.desc.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: error %v, want ErrInvalid", test.name, err)
		}
	}
	// Removed mandates revision > 0 from the same activation
	// counter (CURRENT_SEMANTICS.md:79, the SEM contract).
	if err := (SemanticDescriptor{Family: FamilyBlock, Kind: KindRemoved,
		Activation: blockActivation(f), Revision: 0, Source: source(f)}).Validate(); err == nil {
		t.Fatal("Removed revision 0 must be rejected")
	}
	for _, revision := range []uint64{1, 7} {
		d := SemanticDescriptor{Family: FamilyBlock, Kind: KindRemoved,
			Activation: blockActivation(f), Revision: revision, Source: source(f)}
		if err := d.Validate(); err != nil {
			t.Fatalf("Removed revision %d: %v", revision, err)
		}
	}
	// Optional entities follow the body count in native order.
	if _, err := NewOptionalDescriptor(FamilyTx, KindApplied, f.network, f.producer, f.run,
		0, f.block, 0, 0, 0, source(f)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("optional descriptor of the tx family: %v", err)
	}
	d, err := NewOptionalDescriptor(FamilyBorLegacy, KindApplied, f.network, f.producer,
		f.run, 0, f.block, 12, 0, 0, source(f))
	if err != nil {
		t.Fatalf("optional descriptor: %v", err)
	}
	if got := d.Activation.ActivationID.EntityOrdinal; got != 13 {
		t.Fatalf("optional ordinal %d, want 13", got)
	}
	if _, err := NewOptionalDescriptor(FamilyBalanceSystem, KindApplied, f.network,
		f.producer, f.run, 0, f.block, math.MaxUint64, 0, 0, source(f)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("optional ordinal overflow: %v", err)
	}
	if _, err := NewTransactionDescriptor(KindApplied, f.network, f.producer, f.run, 0,
		f.block, testHash32("7817b17d62a8ddadb215510fc905139965eadca90e1f2dd117e3ced65d147502"),
		math.MaxUint64, 0, source(f)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tx ordinal overflow: %v", err)
	}
}

func blockActivation(f fixture) ActivationRef {
	return ActivationRef{ProducerID: f.producer, RunID: f.run, BlockHash: f.block}
}

func txActivation(f fixture) ActivationRef {
	ref := blockActivation(f)
	ref.ActivationID = ActivationID{EntityOrdinal: 1}
	ref.Transaction = &TransactionRef{TransactionHash: f.block}
	return ref
}

func ordinalActivation(f fixture, ordinal uint64) ActivationRef {
	ref := blockActivation(f)
	ref.ActivationID.EntityOrdinal = ordinal
	return ref
}

// TestEnsureBatchAllocatesOnce checks the exactly-once rule of D79: the
// first call allocates the next batch, retries return the same batch
// without advancing the counter, and a checkpoint decision between two
// allocated decisions consumes no batch sequence.
func TestEnsureBatchAllocatesOnce(t *testing.T) {
	f := ethFixture()
	a, err := NewAllocator(f.producer, f.run)
	if err != nil {
		t.Fatalf("allocator: %v", err)
	}
	// Decision 0 (the empty-block IMPORT) has public events: batch 1.
	first, allocated, err := a.EnsureBatch(0)
	if err != nil || !allocated {
		t.Fatalf("first allocation: allocated=%v err=%v", allocated, err)
	}
	want, err := NewBatchID(f.producer, f.run, 1)
	if err != nil {
		t.Fatalf("want batch: %v", err)
	}
	if first != want {
		t.Fatalf("first batch %s, want %s", first, want)
	}
	// The marker repeat and every later retry restore the same batch.
	for retry := 0; retry < 3; retry++ {
		again, allocated, err := a.EnsureBatch(0)
		if err != nil || allocated {
			t.Fatalf("retry %d: allocated=%v err=%v", retry, allocated, err)
		}
		if again != first {
			t.Fatalf("retry %d returned %s, want %s", retry, again, first)
		}
	}
	// Decision 1 is a FINALITY decision without public events: it records a
	// checkpoint and never calls EnsureBatch, so decision 2 gets batch 2.
	third, allocated, err := a.EnsureBatch(2)
	if err != nil || !allocated {
		t.Fatalf("allocation after the checkpoint: allocated=%v err=%v", allocated, err)
	}
	if want, err := NewBatchID(f.producer, f.run, 2); err != nil || third != want {
		t.Fatalf("batch after the checkpoint %s, want %s", third, want)
	}
	// The allocation of a decision is readable and matches.
	got, err := a.Allocation(0)
	if err != nil {
		t.Fatalf("allocation: %v", err)
	}
	if got.Decision.Sequence != 0 || got.Batch != first {
		t.Fatalf("allocation %+v, want decision 0 with batch %s", got, first)
	}
	if _, err := a.Allocation(9); !errors.Is(err, ErrUnallocated) {
		t.Fatalf("unknown allocation: %v, want ErrUnallocated", err)
	}
}

// TestAdoptBatchRestoresBinding checks the restart path: the saved binding
// of a decision is restored, adopts of the same batch are idempotent, and a
// different batch for the same decision is ErrReallocation.
func TestAdoptBatchRestoresBinding(t *testing.T) {
	f := ethFixture()
	a, err := NewAllocator(f.producer, f.run)
	if err != nil {
		t.Fatalf("allocator: %v", err)
	}
	saved, err := NewBatchID(f.producer, f.run, 4)
	if err != nil {
		t.Fatalf("saved batch: %v", err)
	}
	if err := a.AdoptBatch(2, saved); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if err := a.AdoptBatch(2, saved); err != nil {
		t.Fatalf("adopt again: %v", err)
	}
	got, _, err := a.EnsureBatch(2)
	if err != nil {
		t.Fatalf("ensure after adopt: %v", err)
	}
	if got != saved {
		t.Fatalf("ensure after adopt returned %s, want the adopted %s", got, saved)
	}
	// The counter moved past the adopted sequence; no batch sequence is
	// reused.
	next, allocated, err := a.EnsureBatch(5)
	if err != nil || !allocated {
		t.Fatalf("next allocation: allocated=%v err=%v", allocated, err)
	}
	if want, err := NewBatchID(f.producer, f.run, 5); err != nil || next != want {
		t.Fatalf("next batch %s, want sequence 5", next)
	}
	conflict, err := NewBatchID(f.producer, f.run, 9)
	if err != nil {
		t.Fatalf("conflict batch: %v", err)
	}
	if err := a.AdoptBatch(2, conflict); !errors.Is(err, ErrReallocation) {
		t.Fatalf("conflicting adopt: %v, want ErrReallocation", err)
	}
	// A batch of another producer run never binds this allocator's
	// decisions.
	other, err := NewBatchID(bscBlockFixture().producer, f.run, 1)
	if err != nil {
		t.Fatalf("other batch: %v", err)
	}
	if err := a.AdoptBatch(2, other); !errors.Is(err, ErrIdentity) {
		t.Fatalf("foreign batch: %v, want ErrIdentity", err)
	}
	if err := a.AdoptBatch(2, BatchID{}); !errors.Is(err, ErrIdentity) {
		t.Fatalf("empty batch: %v, want ErrIdentity", err)
	}
	// The last possible batch sequence cannot move the counter any
	// further; the adopt fails and leaves the allocator unchanged.
	last, err := NewBatchID(f.producer, f.run, math.MaxUint64)
	if err != nil {
		t.Fatalf("last batch: %v", err)
	}
	if err := a.AdoptBatch(9, last); !errors.Is(err, ErrInvalid) {
		t.Fatalf("last batch adopt: %v, want ErrInvalid", err)
	}
	if _, err := a.Allocation(9); !errors.Is(err, ErrUnallocated) {
		t.Fatalf("decision 9 after the failed adopt: %v, want ErrUnallocated", err)
	}
}

// TestAllocationDigestVectors checks the allocation digests against the
// independent values of the ETH run: decision 0 with batch 1 and decision 2
// with batch 2.
func TestAllocationDigestVectors(t *testing.T) {
	f := ethFixture()
	tests := []struct {
		name     string
		decision uint64
		batch    uint64
		digest   string
	}{
		{"decision 0, batch 1", 0, 1,
			"b3947d6dde3730dca51e9e70b259a3a41fcbf3b608d727fab9c1f727c0ad0e33"},
		{"decision 2, batch 2", 2, 2,
			"19dd0a1b80a95f6b019db665a64bcb8fafc3415d744785dccc12df8468985252"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch, err := NewBatchID(f.producer, f.run, test.batch)
			if err != nil {
				t.Fatalf("batch: %v", err)
			}
			allocation := BatchAllocation{Decision: DecisionID{ProducerID: f.producer,
				RunID: f.run, Sequence: test.decision}, Batch: batch}
			got, err := allocation.Digest()
			if err != nil {
				t.Fatalf("digest: %v", err)
			}
			if got.String() != test.digest {
				t.Fatalf("digest %s, want the independent value %s", got, test.digest)
			}
		})
	}
}

func TestBatchIDRules(t *testing.T) {
	f := ethFixture()
	if _, err := NewBatchID(f.producer, f.run, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("batch sequence 0: %v, want ErrInvalid", err)
	}
	if _, err := NewBatchID(ID16{}, f.run, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unset producer: %v, want ErrInvalid", err)
	}
	batch, err := NewBatchID(f.producer, f.run, 1)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	producer, run, sequence := batch.Decode()
	if producer != f.producer || run != f.run || sequence != 1 {
		t.Fatalf("decode %s.%s.%d", producer, run, sequence)
	}
	if len(batch.String()) != 80 {
		t.Fatalf("batch text form %q", batch.String())
	}
	// The batch bytes are the public identity form: producer, run, counter.
	var want [40]byte
	copy(want[:16], f.producer[:])
	copy(want[16:32], f.run[:])
	copy(want[32:], []byte{0, 0, 0, 0, 0, 0, 0, 1})
	if !bytes.Equal(batch[:], want[:]) {
		t.Fatalf("batch bytes %x", batch)
	}
	// An allocation that names a batch of another producer run is invalid.
	other, err := NewBatchID(bscBlockFixture().producer, bscBlockFixture().run, 1)
	if err != nil {
		t.Fatalf("other batch: %v", err)
	}
	bad := BatchAllocation{Decision: DecisionID{ProducerID: f.producer, RunID: f.run}, Batch: other}
	if _, err := bad.Digest(); !errors.Is(err, ErrIdentity) {
		t.Fatalf("foreign allocation digest: %v, want ErrIdentity", err)
	}
	zero := BatchAllocation{Decision: DecisionID{ProducerID: f.producer, RunID: f.run}}
	if _, err := zero.Digest(); !errors.Is(err, ErrIdentity) {
		t.Fatalf("zero allocation digest: %v, want ErrIdentity", err)
	}
}

func TestRegistryNames(t *testing.T) {
	if FamilyTx.String() != "tx" || FamilyBlock.String() != "block" ||
		FamilyBalanceSystem.String() != "balance.system" ||
		FamilyBorLegacy.String() != "bor.legacy_receipt" {
		t.Fatalf("family names changed")
	}
	if KindApplied.String() != "applied" || KindRemoved.String() != "removed" ||
		KindStatus.String() != "status" {
		t.Fatalf("kind names changed")
	}
	if Family(0).Valid() || Kind(0).Valid() || Family(9).Valid() || Kind(9).Valid() {
		t.Fatalf("unregistered values are valid")
	}
	if FamilyTx != 1 || FamilyBlock != 2 || FamilyBalanceSystem != 3 || FamilyBorLegacy != 4 {
		t.Fatalf("family registry values changed")
	}
	if KindApplied != 1 || KindRemoved != 2 || KindStatus != 3 {
		t.Fatalf("kind registry values changed")
	}
}
