package eth

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
)

// The corpus receipt of transaction (1,0): status 1, cumulative gas 21000,
// empty logs, zero bloom. The consensus RLP is computed by hand from the
// pinned MarshalBinary contract (D75): the status byte encodes itself, the
// gas is a 2-byte scalar, the bloom is a 256-byte string and the empty
// logs list is 0xc0.
func consensusNoLogsHex(t *testing.T) []byte {
	t.Helper()
	out, err := hex.DecodeString(
		"f9010801" + // list header f9 0108 (payload 264) + status 0x01
			"825208" + // cumulative gas 21000
			"b90100" + stringsRepeat("00", 256) +
			"c0")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func stringsRepeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func corpusReceiptNoLogs() *Receipt {
	return &Receipt{
		Status:            Some(uint8(1)),
		Result:            ResultSuccess,
		CumulativeGasUsed: 21000,
		GasUsed:           Some(uint64(21000)),
		EffectiveGasPrice: Some(gwei(2)),
		NetworkDetails:    NetworkDetails{Eth: &EthReceiptDetails{}},
	}
}

func TestSourceEncodingLegacyMatchesHandComputedRLP(t *testing.T) {
	r := corpusReceiptNoLogs()
	enc, err := SourceEncoding(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if enc.Kind != KindLegacyConsensusRLP {
		t.Fatalf("kind = %d, want the legacy consensus RLP", enc.Kind)
	}
	if nt, _ := enc.NativeType.Get(); nt != 0 {
		t.Fatalf("native_type = %d, want the mandatory 0", nt)
	}
	want := consensusNoLogsHex(t)
	if !bytes.Equal(enc.Bytes, want) {
		t.Fatalf("legacy consensus bytes = %x, want %x", enc.Bytes, want)
	}
	if err := VerifySourceEncoding(enc, r); err != nil {
		t.Fatalf("the writer's own bytes must verify: %v", err)
	}
}

func TestSourceEncodingTypedEnvelopePrefix(t *testing.T) {
	r := corpusReceiptNoLogs()
	r.CumulativeGasUsed = 42000
	enc, err := SourceEncoding(r, 2)
	if err != nil {
		t.Fatal(err)
	}
	if enc.Kind != KindTypedConsensusEnvelope {
		t.Fatalf("kind = %d, want the typed envelope", enc.Kind)
	}
	if enc.Bytes[0] != 0x02 {
		t.Fatalf("the typed envelope starts with the type byte, got %x", enc.Bytes[0])
	}
	// No outer RLP-string wrapper: the second byte is the consensus list
	// header, not a string header.
	if enc.Bytes[1]&0xc0 != 0xc0 {
		t.Fatalf("the byte after the type must open the consensus list, got %x", enc.Bytes[1])
	}
	if err := VerifySourceEncoding(enc, r); err != nil {
		t.Fatal(err)
	}
}

func TestVerifySourceEncodingWithLogs(t *testing.T) {
	// The corpus receipt of transaction (1,2): one log with one topic and
	// 32 bytes of data (expected/eth.json).
	r := corpusReceiptNoLogs()
	r.CumulativeGasUsed = 184655
	r.GasUsed = Some(uint64(142655))
	r.Logs = []Log{{
		Address:       corpusParent,
		Topics:        []Hash32{valueHash(0xa1)},
		Data:          value32(5),
		TxLogIndex:    0,
		BlockLogIndex: 0,
	}}
	enc, err := SourceEncoding(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySourceEncoding(enc, r); err != nil {
		t.Fatal(err)
	}
	// Any disagreement with the typed receipt is an error.
	tampered := *r
	tampered.CumulativeGasUsed = 999
	if err := VerifySourceEncoding(enc, &tampered); !errors.Is(err, ErrReceiptEncoding) {
		t.Fatalf("cumulative mismatch error = %v", err)
	}
	tampered = *r
	tampered.Logs = nil
	if err := VerifySourceEncoding(enc, &tampered); !errors.Is(err, ErrReceiptEncoding) {
		t.Fatalf("log count mismatch error = %v", err)
	}
	// Trailing bytes are rejected.
	bad := enc
	bad.Bytes = append(append([]byte{}, enc.Bytes...), 0x00)
	if err := VerifySourceEncoding(bad, r); !errors.Is(err, ErrRLPTrailing) {
		t.Fatalf("trailing byte error = %v", err)
	}
}

func TestVerifySourceEncodingRejectsBadShapes(t *testing.T) {
	r := corpusReceiptNoLogs()
	enc, _ := SourceEncoding(r, 0)

	wrongType := enc
	wrongType.Kind = KindTypedConsensusEnvelope
	if err := VerifySourceEncoding(wrongType, r); !errors.Is(err, ErrReceiptEncoding) {
		t.Fatalf("kind 2 without the type byte error = %v", err)
	}

	empty := SourceReceiptEncoding{Kind: KindLegacyConsensusRLP, NativeType: Some(uint32(0))}
	if err := ValidateSourceEncodingShape(empty); !errors.Is(err, ErrReceiptEncoding) {
		t.Fatalf("empty bytes error = %v", err)
	}

	missingType := SourceReceiptEncoding{Kind: KindLegacyConsensusRLP, Bytes: enc.Bytes}
	if err := ValidateSourceEncodingShape(missingType); !errors.Is(err, ErrReceiptEncoding) {
		t.Fatalf("kind 1 without native_type error = %v", err)
	}

	invalid := SourceReceiptEncoding{Kind: ReceiptEncodingKind(9), NativeType: Some(uint32(0)), Bytes: enc.Bytes}
	if err := ValidateSourceEncodingShape(invalid); err == nil {
		t.Fatal("an unknown kind must be rejected by the closed registry")
	}
	// The closed ETH profile also rejects the Bor-only storage encoding.
	bor := SourceReceiptEncoding{Kind: profiles.KindBorLegacyStorageRLP, Bytes: enc.Bytes}
	if err := ValidateSourceEncodingShape(bor); err == nil {
		t.Fatal("kind 3 is forbidden for ETH body receipts")
	}
}

func TestReceiptValidateOutcomeRules(t *testing.T) {
	r := corpusReceiptNoLogs()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.NetworkDetails = NetworkDetails{Eth: &EthReceiptDetails{}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	both := *r
	both.PostStateRoot = Some(patternHash(1))
	if err := both.Validate(); !errors.Is(err, ErrReceiptShape) {
		t.Fatalf("both outcome branches error = %v", err)
	}
	neither := corpusReceiptNoLogs()
	neither.Status = None[uint8]()
	if err := neither.Validate(); !errors.Is(err, ErrReceiptShape) {
		t.Fatalf("no outcome error = %v", err)
	}
	preByzantium := corpusReceiptNoLogs()
	preByzantium.Status = None[uint8]()
	preByzantium.PostStateRoot = Some(patternHash(2))
	if err := preByzantium.Validate(); err != nil {
		t.Fatal(err)
	}
	badStatus := corpusReceiptNoLogs()
	badStatus.Status = Some(uint8(2))
	if err := badStatus.Validate(); !errors.Is(err, ErrReceiptShape) {
		t.Fatalf("status 2 error = %v", err)
	}
	conflict := corpusReceiptNoLogs()
	conflict.Result = ResultRevert
	if err := conflict.Validate(); !errors.Is(err, ErrReceiptShape) {
		t.Fatalf("status 1 with REVERT error = %v", err)
	}
	noDetails := corpusReceiptNoLogs()
	noDetails.NetworkDetails = NetworkDetails{}
	if err := noDetails.Validate(); !errors.Is(err, ErrReceiptShape) {
		t.Fatalf("missing network details error = %v", err)
	}
}

func TestWriteLogConsensusFraming(t *testing.T) {
	// [address(20), topics([32]), data(32)]: payload 21 + 34 + 33 = 88.
	log := Log{Address: corpusParent, Topics: []Hash32{valueHash(0xa1)}, Data: value32(5)}
	var buf bytes.Buffer
	if err := WriteLog(&buf, &log); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	if got[0] != 0xf8 || got[1] != 88 {
		t.Fatalf("log list header = %x %x, want f8 58 (long list, one length byte)", got[0], got[1])
	}
	if got[2] != 0x94 {
		t.Fatalf("address header = %x, want 94", got[2])
	}
	if got[23] != 0xe1 {
		t.Fatalf("topics list header = %x, want e1", got[23])
	}
	if got[24] != 0xa0 {
		t.Fatalf("topic string header = %x, want a0", got[24])
	}
	if got[57] != 0xa0 {
		t.Fatalf("data header = %x, want a0", got[57])
	}
}

func TestAuthorizationSourceTuplesPreservedIncludingInvalidAndDuplicate(t *testing.T) {
	delegate := mustAddr(typePathDelegate)
	tuples := []AuthorizationTuple{
		{ChainID: Some(u256("31337")), Address: delegate, Nonce: 0, YParity: 1, R: patternHash(1), S: patternHash(2)},
		// Duplicate of the first tuple: preserved in source order.
		{ChainID: Some(u256("31337")), Address: delegate, Nonce: 0, YParity: 1, R: patternHash(1), S: patternHash(2)},
		// Invalid for execution: wrong chain id.
		{ChainID: Some(u256("1")), Address: delegate, Nonce: 5, YParity: 0, R: patternHash(3), S: patternHash(4)},
	}
	kept, err := NormalizeAuthorizationList(tuples)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 3 {
		t.Fatalf("kept %d tuples, want all 3", len(kept))
	}
	for i := range kept {
		if kept[i] != tuples[i] {
			t.Errorf("tuple %d changed during normalization", i)
		}
	}
}

func TestValidateExternalEnvelopeCannotFilterTuples(t *testing.T) {
	// The envelope view has no authorization list field at all: filtering
	// the tuple list from envelope validation is impossible by
	// construction.
	env := ExternalEnvelope{
		Sender: corpusSender, Nonce: 4, ChainID: u256("31337"), NativeType: 4,
		GasLimit: 150000, SignatureSigned: true,
		ExpectedSender:  Some(corpusSender),
		ExpectedNonce:   Some(uint64(4)),
		ExpectedChainID: Some(u256("31337")),
	}
	if err := ValidateExternalEnvelope(env); err != nil {
		t.Fatalf("a valid envelope must pass: %v", err)
	}
	bad := env
	bad.ExpectedNonce = Some(uint64(3))
	if err := ValidateExternalEnvelope(bad); !errors.Is(err, ErrExternalValidation) {
		t.Fatalf("nonce mismatch error = %v", err)
	}
	bad = env
	bad.SignatureSigned = false
	if err := ValidateExternalEnvelope(bad); !errors.Is(err, ErrExternalValidation) {
		t.Fatalf("missing signature error = %v", err)
	}
}

func TestAuthorizationOutcomesAgainstStateDiff(t *testing.T) {
	// The type-path oracle: the authority code is ef0100 plus the delegate
	// address after either authorization transaction, and the authority
	// nonce advances; the authority pays no fee.
	authority := mustAddr(typePathAuthority)
	delegate := mustAddr(typePathDelegate)
	designation := DesignationCode(delegate)
	if len(designation) != 23 || designation[0] != 0xef || designation[1] != 0x01 || designation[2] != 0x00 {
		t.Fatalf("designation code = %x, want the ef0100 marker plus the address", designation)
	}
	tuples := []AuthorizationTuple{
		{ChainID: Some(u256("31337")), Address: delegate, Nonce: 0, YParity: 0, R: patternHash(1), S: patternHash(2)},
		{ChainID: Some(u256("31337")), Address: delegate, Nonce: 1, YParity: 1, R: patternHash(3), S: patternHash(4)},
	}
	outcomes := []AuthorizationTupleOutcome{
		{TupleIndex: 0, Applied: true, Authority: authority, NonceAfter: 1},
		{TupleIndex: 1, Applied: true, Authority: authority, NonceAfter: 2},
	}
	diff, err := BuildStateDiff([]AccountObservation{{
		Address: authority,
		Before:  AccountPoint{Exists: true, Balance: u256("0"), Nonce: 0, CodeHash: fixtureCodeHash(nil), Code: []byte{}},
		After:   AccountPoint{Exists: true, Balance: u256("0"), Nonce: 2, CodeHash: fixtureCodeHash(designation), Code: designation},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAuthorizationOutcomes(tuples, outcomes, &diff); err != nil {
		t.Fatalf("the applied tuples must match the designation code: %v", err)
	}
	// A wrong designation code is caught.
	wrong := DesignationCode(corpusParent)
	wrongDiff, err := BuildStateDiff([]AccountObservation{{
		Address: authority,
		Before:  AccountPoint{Exists: true, Nonce: 0, CodeHash: fixtureCodeHash(nil), Code: []byte{}},
		After:   AccountPoint{Exists: true, Nonce: 1, CodeHash: fixtureCodeHash(wrong), Code: wrong},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAuthorizationOutcomes(tuples[:1], outcomes[:1], &wrongDiff); err == nil {
		t.Fatal("a designation code that disagrees with the tuple must fail the cross-check")
	}
	// An outcome naming an absent tuple is rejected.
	if err := ValidateAuthorizationOutcomes(tuples[:1], outcomes, &diff); err == nil {
		t.Fatal("an outcome for a missing tuple must be rejected")
	}
}
