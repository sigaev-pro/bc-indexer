package bsc

import (
	"errors"
	"testing"

	bscbalance "github.com/sigaev-pro/bc-indexer/core/normalization/bsc/balance"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/calls"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/receipt"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/state"
	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

func TestClassifySystemCallSelectors(t *testing.T) {
	cases := []struct {
		input []byte
		want  SystemCallKind
	}{
		{append([]byte{0xe1, 0xc7, 0x39, 0x2a}, make([]byte, 32)...), SystemCallInitializeLegacy},
		{append([]byte{0x81, 0x29, 0xfc, 0x1c}, make([]byte, 32)...), SystemCallInitializeFeynman},
		{append([]byte{0xf3, 0x40, 0xfa, 0x01}, make([]byte, 32)...), SystemCallDistributeFees},
		{append([]byte{0x1e, 0x4c, 0x15, 0x24}, make([]byte, 32)...), SystemCallUpdateValidatorSet},
		{[]byte{0xde, 0xad, 0xbe, 0xef}, SystemCallUnregistered},
		{[]byte{0x01}, SystemCallUnregistered},
		{nil, SystemCallUnregistered},
	}
	for _, c := range cases {
		if got := ClassifySystemCall(c.input); got != c.want {
			t.Fatalf("ClassifySystemCall(%x) = %s, want %s", c.input, got, c.want)
		}
	}
}

func TestDistributionSplit(t *testing.T) {
	reward, burn, rest, err := DefaultDistribution().Split(u(10000))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if reward != u(625) || burn != u(1000) || rest != u(8375) {
		t.Fatalf("split drifted: reward %s burn %s rest %s", reward.Decimal(), burn.Decimal(), rest.Decimal())
	}
	// Floor arithmetic on a value that does not divide evenly.
	reward, burn, rest, _ = DefaultDistribution().Split(u(1))
	if !reward.IsZero() || !burn.IsZero() || rest != u(1) {
		t.Fatalf("floor split drifted: %s %s %s", reward.Decimal(), burn.Decimal(), rest.Decimal())
	}
	if _, _, _, err := (Distribution{625, 1000, 0}).Split(u(1)); err == nil {
		t.Fatal("zero denominator must fail")
	}
}

func TestExpectSystemTransitionGenericRules(t *testing.T) {
	accounts := SystemAccounts{Validator: fixtureCoinbase, ValidatorSetContract: fixtureValSet,
		RewardContract: fixtureReward, BurnAddress: fixtureBurn}
	before := map[balance.Address20]AccountPoint{
		fixtureCoinbase: {Balance: u(15000), Nonce: 0, Exists: true},
	}
	after := map[balance.Address20]AccountPoint{
		fixtureCoinbase: {Balance: u(5000), Nonce: 1, Exists: true},
	}
	// The generic sender rules hold for an unregistered selector.
	if err := ExpectSystemTransition(SystemCallUnregistered, fixtureCoinbase, u(10000), accounts,
		DefaultDistribution(), before, after); err != nil {
		t.Fatalf("unregistered transition: %v", err)
	}
	// Nonce does not rise: rejected.
	badNonce := map[balance.Address20]AccountPoint{fixtureCoinbase: {Balance: u(5000), Nonce: 2, Exists: true}}
	err := ExpectSystemTransition(SystemCallUnregistered, fixtureCoinbase, u(10000), accounts, DefaultDistribution(), before, badNonce)
	if !errors.Is(err, ErrSystemNonce) {
		t.Fatalf("nonce error = %v", err)
	}
	// Sender balance does not fall by the value: rejected.
	badBalance := map[balance.Address20]AccountPoint{fixtureCoinbase: {Balance: u(6000), Nonce: 1, Exists: true}}
	err = ExpectSystemTransition(SystemCallUnregistered, fixtureCoinbase, u(10000), accounts, DefaultDistribution(), before, badBalance)
	if !errors.Is(err, ErrSystemSender) {
		t.Fatalf("sender error = %v", err)
	}
	// A missing boundary point: rejected, never an invented zero.
	err = ExpectSystemTransition(SystemCallUnregistered, fixtureCoinbase, u(10000), accounts, DefaultDistribution(),
		map[balance.Address20]AccountPoint{}, after)
	if !errors.Is(err, ErrSystemPointMissing) {
		t.Fatalf("missing point error = %v", err)
	}
}

func TestExpectSystemTransitionKinds(t *testing.T) {
	accounts := SystemAccounts{Validator: fixtureCoinbase, ValidatorSetContract: fixtureValSet,
		RewardContract: fixtureReward, BurnAddress: fixtureBurn}
	dist := DefaultDistribution()

	// Initializers move no funds; the nonce still rises by one.
	staticBefore := map[balance.Address20]AccountPoint{
		fixtureCoinbase: {Balance: u(700), Nonce: 4, Exists: true},
		fixtureValSet:   {Balance: balance.UInt256{}, Nonce: 0, Exists: true},
	}
	staticAfter := map[balance.Address20]AccountPoint{
		fixtureCoinbase: {Balance: u(700), Nonce: 5, Exists: true},
		fixtureValSet:   {Balance: balance.UInt256{}, Nonce: 0, Exists: true},
	}
	moved := map[balance.Address20]AccountPoint{
		fixtureCoinbase: {Balance: u(700), Nonce: 5, Exists: true},
		fixtureValSet:   {Balance: u(1), Nonce: 0, Exists: true},
	}
	if err := ExpectSystemTransition(SystemCallInitializeLegacy, fixtureCoinbase, balance.UInt256{}, accounts, dist, staticBefore, staticAfter); err != nil {
		t.Fatalf("legacy initializer: %v", err)
	}
	if err := ExpectSystemTransition(SystemCallInitializeFeynman, fixtureCoinbase, balance.UInt256{}, accounts, dist, staticBefore, staticAfter); err != nil {
		t.Fatalf("feynman initializer: %v", err)
	}
	if err := ExpectSystemTransition(SystemCallInitializeLegacy, fixtureCoinbase, balance.UInt256{}, accounts, dist, staticBefore, moved); !errors.Is(err, ErrSystemStatic) {
		t.Fatalf("initializer moved funds: %v", err)
	}

	// Deposit distribution: 625/1000/8375 of 10000.
	before := map[balance.Address20]AccountPoint{
		fixtureCoinbase: {Balance: u(15000), Nonce: 0, Exists: true},
		fixtureValSet:   {Balance: u(0), Exists: true},
		fixtureReward:   {Balance: u(2), Exists: true},
		fixtureBurn:     {Balance: u(0), Exists: true},
	}
	after := map[balance.Address20]AccountPoint{
		fixtureCoinbase: {Balance: u(5000), Nonce: 1, Exists: true},
		fixtureValSet:   {Balance: u(8375), Exists: true},
		fixtureReward:   {Balance: u(627), Exists: true},
		fixtureBurn:     {Balance: u(1000), Exists: true},
	}
	if err := ExpectSystemTransition(SystemCallDistributeFees, fixtureCoinbase, u(10000), accounts, dist, before, after); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	wrong := map[balance.Address20]AccountPoint{
		fixtureCoinbase: {Balance: u(5000), Nonce: 1, Exists: true},
		fixtureValSet:   {Balance: u(8375), Exists: true},
		fixtureReward:   {Balance: u(628), Exists: true}, // one wei off
		fixtureBurn:     {Balance: u(1000), Exists: true},
	}
	if err := ExpectSystemTransition(SystemCallDistributeFees, fixtureCoinbase, u(10000), accounts, dist, before, wrong); !errors.Is(err, ErrSystemDeposit) {
		t.Fatalf("deposit mismatch error = %v", err)
	}

	// Election dusk transfer: the validator set balance empties into the
	// reward contract.
	electionBefore := map[balance.Address20]AccountPoint{
		fixtureCoinbase: {Balance: u(5000), Nonce: 1, Exists: true},
		fixtureValSet:   {Balance: u(8375), Exists: true},
		fixtureReward:   {Balance: u(627), Exists: true},
	}
	electionAfter := map[balance.Address20]AccountPoint{
		fixtureCoinbase: {Balance: u(5000), Nonce: 2, Exists: true},
		fixtureValSet:   {Balance: balance.UInt256{}, Exists: true},
		fixtureReward:   {Balance: u(9002), Exists: true},
	}
	if err := ExpectSystemTransition(SystemCallUpdateValidatorSet, fixtureCoinbase, balance.UInt256{}, accounts, dist, electionBefore, electionAfter); err != nil {
		t.Fatalf("election: %v", err)
	}
	kept := map[balance.Address20]AccountPoint{
		fixtureCoinbase: {Balance: u(5000), Nonce: 2, Exists: true},
		fixtureValSet:   {Balance: u(1), Exists: true},
		fixtureReward:   {Balance: u(9002), Exists: true},
	}
	if err := ExpectSystemTransition(SystemCallUpdateValidatorSet, fixtureCoinbase, balance.UInt256{}, accounts, dist, electionBefore, kept); !errors.Is(err, ErrSystemElection) {
		t.Fatalf("election mismatch error = %v", err)
	}
}

// The system envelope of a Parlia deposit: native system sender, zero gas
// price, the validator set contract as recipient, the deposit selector.
func systemEnvelope(selector [4]byte, value uint64) EnvelopeFacts {
	target := fixtureValSet
	input := append(selector[:], make([]byte, 32)...)
	return EnvelopeFacts{
		Position: 0, NativeType: 0, Wire: []byte{0xd0, 0x02},
		Sender: fixtureCoinbase, SenderProvenance: ProvenanceNativeDerived,
		Target: &target, TargetProvenance: ProvenanceNativeDerived,
		Value: u(value), Input: input, Nonce: 0, GasLimit: 30000,
		EffectiveGasPrice: balance.UInt256{},
	}
}

func systemReceipt() ReceiptFacts {
	status := true
	gasUsed := uint64(30000)
	return ReceiptFacts{
		Status: &status, Result: receipt.ResultSuccess,
		CumulativeGasUsed: 30000, GasUsed: &gasUsed,
		EffectiveGasPrice: balance.UInt256{}, NativeReceiptType: 0,
		ConsensusBytes: []byte{0xf9, 0x03},
	}
}

func systemRootFrame() Event {
	target := fixtureValSet
	caller := fixtureCoinbase
	return Event{FrameEntry: &calls.Frame{
		Depth: 0, Engine: balance.EngineNativeProtocol, Kind: calls.KindNativeProtocol,
		CallerAddress: &caller, Target: &target, Outcome: calls.OutcomeSuccess,
	}}
}

func systemAccounts() SystemAccounts {
	return SystemAccounts{Validator: fixtureCoinbase, ValidatorSetContract: fixtureValSet,
		RewardContract: fixtureReward, BurnAddress: fixtureBurn}
}

// The deposit system transaction: a NATIVE_PROTOCOL root frame, one
// BSC_SYSTEM_EXECUTION action keeping the four actual native mutations,
// an explicit-zero fee summary without a payer, the wire type preserved,
// and the independently expected sender/distribution transitions checked
// from the committed boundary points.
func TestNormalizeParliaDeposit(t *testing.T) {
	obs := &Observation{
		Envelope: systemEnvelope(selectorDistributeFees, 10000), Coinbase: fixtureCoinbase,
		Events: []Event{
			systemRootFrame(),
			groupEvent(20, bscbalance.RoleSystemExecution, 0,
				mut(fixtureCoinbase, 15000, 5000),
				mut(fixtureReward, 0, 625),
				mut(fixtureBurn, 0, 1000),
				mut(fixtureValSet, 0, 8375)),
		},
		Receipt: systemReceipt(),
		Boundary: []state.AccountChange{
			accountRow(fixtureCoinbase, 15000, 5000, 0, 1),
			accountRow(fixtureReward, 0, 625, 0, 0),
			accountRow(fixtureBurn, 0, 1000, 0, 0),
			accountRow(fixtureValSet, 0, 8375, 0, 0),
		},
		SystemAccounts: systemAccounts(),
	}
	record, err := New().Normalize(obs)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if record.Classification != ClassificationParliaSystem {
		t.Fatalf("classification = %s", record.Classification)
	}
	if record.SystemCallKind != SystemCallDistributeFees {
		t.Fatalf("system call kind = %s", record.SystemCallKind)
	}
	if record.Envelope.NativeType != 0 {
		t.Fatalf("the classification must preserve the wire type, got %d", record.Envelope.NativeType)
	}
	root := record.Calls.Root()
	if root.Engine != balance.EngineNativeProtocol || root.Kind != calls.KindNativeProtocol {
		t.Fatalf("system root frame engine %s kind %s", root.Engine, root.Kind)
	}
	if len(record.Actions) != 1 {
		t.Fatalf("want one system action, got %d", len(record.Actions))
	}
	a := record.Actions[0]
	if a.Cause.Operation != balance.OpBSCSystemExecution || a.Engine != balance.EngineNativeProtocol ||
		a.Kind != balance.KindNativeProtocolChange || !a.EffectsCommitted {
		t.Fatalf("system action drifted: %+v", a)
	}
	if len(a.Effects) != 4 {
		t.Fatalf("the four native mutations must all survive, got %d", len(a.Effects))
	}
	summary := record.Receipt.FeeSummary
	if summary.Payer != nil || !summary.NetCharge.IsZero() {
		t.Fatalf("zero-fee system summary must carry applicable zeros without a payer: %+v", summary)
	}
	if record.Receipt.Details == nil {
		t.Fatal("BscReceiptDetails must be present for a system transaction too")
	}
	if err := record.ReconcileBoundary(); err != nil {
		t.Fatalf("boundary reconciliation: %v", err)
	}
}

// An initializer system transaction: storage transitions without balance
// movements, the nonce still rises by one.
func TestNormalizeParliaInitializer(t *testing.T) {
	storageRow := state.AccountChange{
		Address: fixtureValSet, ExistedBefore: true, ExistsAfter: true,
		Storage: []state.StorageChange{{Key: blockHash1(), Before: blockHash0(), After: blockHash2()}},
	}
	obs := &Observation{
		Envelope: systemEnvelope(selectorInitializeLegacy, 0), Coinbase: fixtureCoinbase,
		Events:  []Event{systemRootFrame()},
		Receipt: systemReceipt(),
		Boundary: []state.AccountChange{
			accountRow(fixtureCoinbase, 700, 700, 4, 5),
			storageRow,
		},
		SystemAccounts: systemAccounts(),
	}
	record, err := New().Normalize(obs)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if record.SystemCallKind != SystemCallInitializeLegacy {
		t.Fatalf("kind = %s", record.SystemCallKind)
	}
	if len(record.Actions) != 0 {
		t.Fatalf("an initializer without balance movements has no balance actions, got %d", len(record.Actions))
	}
	if len(record.StateDiff.Accounts) != 2 {
		t.Fatalf("boundary accounts drifted: %d", len(record.StateDiff.Accounts))
	}
}

// The election system transaction: the dusk transfer empties the
// validator set contract into the reward contract.
func TestNormalizeParliaElection(t *testing.T) {
	obs := &Observation{
		Envelope: systemEnvelope(selectorUpdateValidatorSet, 0), Coinbase: fixtureCoinbase,
		Events: []Event{
			systemRootFrame(),
			groupEvent(21, bscbalance.RoleSystemExecution, 0,
				mut(fixtureValSet, 8375, 0),
				mut(fixtureReward, 627, 9002)),
		},
		Receipt: systemReceipt(),
		Boundary: []state.AccountChange{
			accountRow(fixtureCoinbase, 5000, 5000, 1, 2),
			accountRow(fixtureValSet, 8375, 0, 0, 0),
			accountRow(fixtureReward, 627, 9002, 0, 0),
		},
		SystemAccounts: systemAccounts(),
	}
	record, err := New().Normalize(obs)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if record.SystemCallKind != SystemCallUpdateValidatorSet {
		t.Fatalf("kind = %s", record.SystemCallKind)
	}
	if err := record.ReconcileBoundary(); err != nil {
		t.Fatalf("boundary reconciliation: %v", err)
	}
}

// A deposit whose observed distribution contradicts the expectation is an
// explicit normalization error, never a silently accepted record.
func TestNormalizeParliaDepositExpectationFailure(t *testing.T) {
	obs := &Observation{
		Envelope: systemEnvelope(selectorDistributeFees, 10000), Coinbase: fixtureCoinbase,
		Events: []Event{
			systemRootFrame(),
			groupEvent(20, bscbalance.RoleSystemExecution, 0,
				mut(fixtureCoinbase, 15000, 5000),
				mut(fixtureReward, 0, 700), // wrong share
				mut(fixtureBurn, 0, 1000),
				mut(fixtureValSet, 0, 8300)),
		},
		Receipt: systemReceipt(),
		Boundary: []state.AccountChange{
			accountRow(fixtureCoinbase, 15000, 5000, 0, 1),
			accountRow(fixtureReward, 0, 700, 0, 0),
			accountRow(fixtureBurn, 0, 1000, 0, 0),
			accountRow(fixtureValSet, 0, 8300, 0, 0),
		},
		SystemAccounts: systemAccounts(),
	}
	if _, err := New().Normalize(obs); !errors.Is(err, ErrSystemDeposit) {
		t.Fatalf("deposit expectation error = %v", err)
	}
}

func blockHash0() (h [32]byte) { return h }
func blockHash1() (h [32]byte) { h[0] = 1; return h }
func blockHash2() (h [32]byte) { h[0] = 2; return h }
