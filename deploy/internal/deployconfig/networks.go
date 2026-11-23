// Package deployconfig holds the deployment-side configuration of the
// three-network extractor stack (E06-13 deployment, E06-09 live start):
// the fixed network table, the strict live/gateway YAML schemas of
// LIVE_CONFIG_SPEC, and the self-contained reader of the immutable
// DestinationBinding descriptor with its exact digests
// (CONTROL_WIRE_CONTRACT, "DestinationBinding file" and "Stream metadata
// identity D77").
//
// The package is written against the contract text, not against the
// repository libraries: the mock services must build inside the deploy/
// Docker context. The bootstrap tool cross-validates every generated
// binding with the authoritative gateway/destination implementation
// before it is used anywhere.
package deployconfig

import "fmt"

// NetworkKey identifies one circuit of the deployment: the service DNS
// prefix, the volume prefix and the compose network suffix all derive
// from it.
type NetworkKey string

// The three baseline mainnet circuits (D82: each additional testnet
// profile is a separate contour with its own verified manifest; the
// mainnet defaults are never inherited).
const (
	Eth     NetworkKey = "eth"
	Bsc     NetworkKey = "bsc"
	Polygon NetworkKey = "polygon"
)

// Networks is the fixed network table of the deployment, in evaluation
// order.
var Networks = []NetworkDefinition{
	{
		Key:            Eth,
		Profile:        "ethereum-mainnet",
		NativeKind:     "geth",
		ChainID:        "0000000000000000000000000000000000000000000000000000000000000001",
		GenesisHash:    "d4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3",
		Account:        "IDX_ETH",
		Stream:         "EXTRACTOR_ETH_V3",
		SubjectPrefix:  "evm.v3.eth",
		BlockInterval:  "12s",
		GatewayService: "eth-gateway",
	},
	{
		Key:            Bsc,
		Profile:        "bsc-mainnet",
		NativeKind:     "bsc",
		ChainID:        "0000000000000000000000000000000000000000000000000000000000000038",
		GenesisHash:    "0d21840abff46b96c84b2ac9e10e4f5cdaeb5691cb79a95e9b23a3e76393b4db",
		Account:        "IDX_BSC",
		Stream:         "EXTRACTOR_BSC_V3",
		SubjectPrefix:  "evm.v3.bsc",
		BlockInterval:  "3s",
		GatewayService: "bsc-gateway",
	},
	{
		Key:            Polygon,
		Profile:        "polygon-pos-mainnet",
		NativeKind:     "bor",
		ChainID:        "0000000000000000000000000000000000000000000000000000000000000089",
		GenesisHash:    "a9c28ce2141b56c474f1dc504bee9b01eb1bd7d1a507980d6e5fe09cf54f7c44",
		Account:        "IDX_POLY",
		Stream:         "EXTRACTOR_POLY_V3",
		SubjectPrefix:  "evm.v3.poly",
		BlockInterval:  "2s",
		GatewayService: "polygon-gateway",
	},
}

// NetworkDefinition is one circuit of the deployment: the chain identity
// of the binding, the NATS account/stream/subject namespace and the
// mock-capture cadence.
type NetworkDefinition struct {
	// Key is the short network key of service names and subjects.
	Key NetworkKey
	// Profile is the network.profile value of LIVE_CONFIG_SPEC.
	Profile string
	// NativeKind is the native.kind value that matches the profile.
	NativeKind string
	// ChainID is the UInt256 chain id, 64 lowercase hex characters.
	ChainID string
	// GenesisHash is the Hash32 genesis hash, 64 lowercase hex characters.
	GenesisHash string
	// Account is the NATS account name: the exact account field of the
	// UserInfo answer, never a display name.
	Account string
	// Stream is the literal stream name of the network.
	Stream string
	// SubjectPrefix is the closed subject prefix of the network.
	SubjectPrefix string
	// BlockInterval is the mock capture cadence of the adapter.
	BlockInterval string
	// GatewayService is the service DNS name of the network gateway.
	GatewayService string
}

// ByProfile returns the network of a network.profile value, or an error
// for an unknown profile: the schema never falls back to a default.
func ByProfile(profile string) (NetworkDefinition, error) {
	for _, n := range Networks {
		if n.Profile == profile {
			return n, nil
		}
	}
	return NetworkDefinition{}, fmt.Errorf("unknown network profile %q", profile)
}

// ByKey returns the network of a NetworkKey.
func ByKey(key NetworkKey) (NetworkDefinition, error) {
	for _, n := range Networks {
		if n.Key == key {
			return n, nil
		}
	}
	return NetworkDefinition{}, fmt.Errorf("unknown network key %q", key)
}

// AdapterIdentity is the mTLS client identity of the network's adapter:
// client certificates carry it as the common name, and the gateway of
// the same network rejects any other identity before any state changes.
func (n NetworkDefinition) AdapterIdentity() string {
	return "deploy/" + string(n.Key) + "/adapter"
}

// GatewayIdentity is the mTLS server identity of the network's gateway.
func (n NetworkDefinition) GatewayIdentity() string {
	return "deploy/" + string(n.Key) + "/gateway"
}

// EventSubjects returns the concrete subject set of the network: every
// family/kind route of MESSAGE_CONTRACT section 7 that the stream must
// cover (tx and block families, applied/removed/status kinds).
func (n NetworkDefinition) EventSubjects() []string {
	var out []string
	for _, family := range []string{"tx", "block"} {
		for _, kind := range []string{"applied", "removed", "status"} {
			out = append(out, n.SubjectPrefix+"."+family+"."+kind)
		}
	}
	return out
}
