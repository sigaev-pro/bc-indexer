package destination

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// validBindingJSON builds one descriptor JSON body from field overrides,
// computing the correct descriptor digest when asked to.
func validBindingJSON(t *testing.T, mutate func(map[string]any), fixDigest bool) []byte {
	t.Helper()
	body := map[string]any{
		"schema_version":    1,
		"destination_id":    strings.Repeat("0a", 16),
		"generation":        "1",
		"network":           map[string]any{"family": "EVM", "chain_id": strings.Repeat("11", 32), "genesis_hash": strings.Repeat("22", 32)},
		"expected_account":  "IDX_ETH",
		"stream":            "EXTRACTOR_ETH_V3",
		"subject_prefix":    "evm.v3.eth",
		"descriptor_digest": "",
	}
	if mutate != nil {
		mutate(body)
	}
	if fixDigest {
		b, err := bindingFromJSON(t, body)
		if err == nil {
			if digest, derr := b.DescriptorDigestComputed(); derr == nil {
				body["descriptor_digest"] = hexEncode(digest[:])
			}
		}
		// A body that already fails content validation keeps its digest:
		// the case expects rejection anyway.
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

// bindingFromJSON decodes the body without digest verification by parsing
// with the digest field zeroed.
func bindingFromJSON(t *testing.T, body map[string]any) (Binding, error) {
	t.Helper()
	trimmed := map[string]any{}
	for k, v := range body {
		trimmed[k] = v
	}
	trimmed["descriptor_digest"] = strings.Repeat("00", 32)
	raw, err := json.Marshal(trimmed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b, err := parseDescriptor(raw)
	if err != nil {
		return Binding{}, err
	}
	b.DescriptorDigest = proto.Hash32{}
	return b, nil
}

func TestParseBindingAcceptsAndIgnoresFormatting(t *testing.T) {
	compact := validBindingJSON(t, nil, true)
	// Whitespace and key order never matter: reformat through a generic
	// decode/encode round trip.
	var generic map[string]any
	if err := json.Unmarshal(compact, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	reformatted, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		t.Fatalf("marshal indent: %v", err)
	}
	for name, data := range map[string][]byte{"compact": compact, "reformatted": reformatted} {
		b, err := ParseBinding(data)
		if err != nil {
			t.Fatalf("%s: ParseBinding: %v", name, err)
		}
		if b.Generation != 1 || b.Stream != "EXTRACTOR_ETH_V3" || b.SubjectPrefix != "evm.v3.eth" {
			t.Fatalf("%s: wrong fields: %+v", name, b)
		}
		if err := b.Validate(); err != nil {
			t.Fatalf("%s: Validate: %v", name, err)
		}
	}
}

func TestParseBindingDigestIndependentOfJSON(t *testing.T) {
	base, err := bindingFromJSON(t, mustGeneric(t, validBindingJSON(t, nil, false)))
	if err != nil {
		t.Fatal(err)
	}
	// Same content, different JSON: digest equal.
	other, otherErr := bindingFromJSON(t, mustGeneric(t, validBindingJSON(t, func(m map[string]any) {
		m["expected_account"] = "IDX_ETH" // same value, set again
	}, false)))
	if otherErr != nil {
		t.Fatal(otherErr)
	}
	d1, err := base.DescriptorDigestComputed()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	d2, err := other.DescriptorDigestComputed()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("digest changed with JSON formatting")
	}
	// Every identity field changes the digest.
	for _, field := range []string{"destination_id", "generation", "expected_account", "stream", "subject_prefix"} {
		mutated, merr := bindingFromJSON(t, mustGeneric(t, validBindingJSON(t, func(m map[string]any) {
			switch field {
			case "destination_id":
				m["destination_id"] = strings.Repeat("0b", 16)
			case "generation":
				m["generation"] = "2"
			case "expected_account":
				m["expected_account"] = "IDX_BSC"
			case "stream":
				m["stream"] = "EXTRACTOR_BSC_V3"
			case "subject_prefix":
				m["subject_prefix"] = "evm.v3.bsc"
			}
		}, false)))
		if merr != nil {
			t.Fatalf("%s: %v", field, merr)
		}
		d3, err := mutated.DescriptorDigestComputed()
		if err != nil {
			t.Fatalf("%s: digest: %v", field, err)
		}
		if d3 == d1 {
			t.Fatalf("digest ignored the %s change", field)
		}
	}
}

func mustGeneric(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return generic
}

func TestParseBindingRejectsStructuralDamage(t *testing.T) {
	cases := map[string][]byte{
		"duplicate key": []byte(`{"schema_version":1,"schema_version":1,"destination_id":"` +
			strings.Repeat("0a", 16) + `","generation":"1","network":{"family":"EVM","chain_id":"` +
			strings.Repeat("11", 32) + `","genesis_hash":"` + strings.Repeat("22", 32) +
			`"},"expected_account":"A","stream":"S","subject_prefix":"evm.v3.eth","descriptor_digest":"` +
			strings.Repeat("00", 32) + `"}`),
		"unknown key": validBindingJSON(t, func(m map[string]any) { m["url"] = "nats://x" }, true),
		"unknown network key": validBindingJSON(t, func(m map[string]any) {
			m["network"].(map[string]any)["chain_id2"] = "1"
		}, true),
		"missing key": func() []byte {
			body := mustGeneric(t, validBindingJSON(t, nil, true))
			delete(body, "stream")
			out, _ := json.Marshal(body)
			return out
		}(),
		"wrong schema version": validBindingJSON(t, func(m map[string]any) { m["schema_version"] = 2 }, true),
		"schema not integer":   validBindingJSON(t, func(m map[string]any) { m["schema_version"] = "1" }, true),
		"generation with leading zero": validBindingJSON(t, func(m map[string]any) {
			m["generation"] = "01"
		}, true),
		"generation zero":     validBindingJSON(t, func(m map[string]any) { m["generation"] = "0" }, true),
		"generation negative": validBindingJSON(t, func(m map[string]any) { m["generation"] = "-1" }, true),
		"generation not canonical": validBindingJSON(t, func(m map[string]any) {
			m["generation"] = "1e3"
		}, true),
		"generation as integer": validBindingJSON(t, func(m map[string]any) { m["generation"] = 1 }, true),
		"uppercase hex": validBindingJSON(t, func(m map[string]any) {
			m["destination_id"] = strings.Repeat("0A", 16)
		}, false),
		"0x prefix": validBindingJSON(t, func(m map[string]any) {
			m["destination_id"] = "0x" + strings.Repeat("0a", 15)
		}, false),
		"short id": validBindingJSON(t, func(m map[string]any) {
			m["destination_id"] = strings.Repeat("0a", 15)
		}, false),
		"wildcard stream":    validBindingJSON(t, func(m map[string]any) { m["stream"] = "S.*" }, false),
		"whitespace stream":  validBindingJSON(t, func(m map[string]any) { m["stream"] = "S S" }, false),
		"wildcard prefix":    validBindingJSON(t, func(m map[string]any) { m["subject_prefix"] = "evm.*" }, false),
		"empty prefix token": validBindingJSON(t, func(m map[string]any) { m["subject_prefix"] = "evm..eth" }, false),
		"wrong digest": validBindingJSON(t, func(m map[string]any) {
			m["descriptor_digest"] = strings.Repeat("07", 32)
		}, false),
		"control char account": validBindingJSON(t, func(m map[string]any) {
			m["expected_account"] = "A\u0000B"
		}, false),
		"not an object": []byte(`[1,2,3]`),
		"empty":         []byte{},
		"bom":           append([]byte{0xEF, 0xBB, 0xBF}, validBindingJSON(t, nil, true)...),
		"oversize": func() []byte {
			body := mustGeneric(t, validBindingJSON(t, nil, true))
			body["expected_account"] = strings.Repeat("a", MaxDescriptorBytes)
			out, _ := json.Marshal(body)
			return out
		}(),
	}
	for name, data := range cases {
		if _, err := ParseBinding(data); err == nil {
			t.Errorf("%s: ParseBinding accepted a damaged descriptor", name)
		}
	}
}

func TestSubjectBuilding(t *testing.T) {
	s, err := Subject("evm.v3.eth", proto.FamilyTx, proto.KindApplied)
	if err != nil {
		t.Fatalf("Subject: %v", err)
	}
	if s != "evm.v3.eth.tx.applied" {
		t.Fatalf("subject %q", s)
	}
	if _, err := Subject("evm.v3.eth", proto.Family(9), proto.KindApplied); err == nil {
		t.Fatalf("unknown family accepted")
	}
	if _, err := Subject("evm..eth", proto.FamilyTx, proto.KindApplied); err == nil {
		t.Fatalf("invalid prefix accepted")
	}
}

func TestSubjectMatching(t *testing.T) {
	cases := []struct {
		pattern, subject string
		want             bool
	}{
		{"evm.v3.>", "evm.v3.eth.tx.applied", true},
		{"evm.v3.*.tx.>", "evm.v3.eth.tx.applied", true},
		{"evm.v3.eth.tx.applied", "evm.v3.eth.tx.applied", true},
		{"evm.v3.*.*.applied", "evm.v3.eth.tx.applied", true},
		{"evm.v3.bsc.>", "evm.v3.eth.tx.applied", false},
		{"evm.v3.eth.tx", "evm.v3.eth.tx.applied", false},
		{"evm.>", "evm.v3.eth.tx.applied", true},
		{">", "evm.v3.eth.tx.applied", false}, // ">" alone is not a valid pattern position 0
	}
	for _, c := range cases {
		if got := SubjectMatches(c.pattern, c.subject); got != c.want {
			t.Errorf("SubjectMatches(%q, %q) = %v, want %v", c.pattern, c.subject, got, c.want)
		}
	}
	if !RouteCovered([]string{"evm.v3.>"}, "evm.v3.eth.tx.applied", "evm.v3.eth.block.removed") {
		t.Fatalf("route not covered")
	}
	if RouteCovered([]string{"evm.v3.bsc.>"}, "evm.v3.eth.tx.applied") {
		t.Fatalf("route covered by the wrong network")
	}
}
