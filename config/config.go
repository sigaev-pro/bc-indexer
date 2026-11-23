package config

// The YAML schema v1 value types, the closed profile registry and the
// endpoint and TLS vocabularies of LIVE_CONFIG_SPEC ("Общая схема YAML
// v1", "Native-профили"). The structs carry exactly the declared keys;
// parse.go enforces that nothing else appears in the file.

// SchemaVersion1 is the only supported YAML schema version.
const SchemaVersion1 = 1

// MaxYAMLBytes bounds the YAML file itself, mirroring the descriptor
// bound of gateway/destination. A larger file is rejected before parsing.
const MaxYAMLBytes = 256 * 1024

// DefaultMaxFrameBytes is the D52 default of transport.max_frame_bytes:
// 2 MiB covering the full TransportFrame together with NATS headers.
const DefaultMaxFrameBytes uint64 = 2097152

// Role is the role one YAML file configures: the live collector with the
// embedded adapter, or the gateway of that network. One file, one role
// (D48); a missing section of the other role is not an error.
type Role string

// The roles of LIVE_CONFIG_SPEC. RoleNone is the absent value and never
// a default for a decision.
const (
	RoleLive    Role = "live"
	RoleGateway Role = "gateway"
)

// ParseRole converts a CLI role argument. Unknown roles are rejected,
// not defaulted.
func ParseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleLive:
		return RoleLive, nil
	case RoleGateway:
		return RoleGateway, nil
	}
	return "", &Error{Field: "role", Code: ExitConfig, Reason: "unknown role " + quote(s) + ", want live or gateway"}
}

// NativeKind is the native client family of a profile.
type NativeKind string

// The native kinds. The YAML never selects an executable or version; it
// only names the family whose adapter is embedded in the running client.
const (
	NativeGeth NativeKind = "geth"
	NativeBsc  NativeKind = "bsc"
	NativeBor  NativeKind = "bor"
)

// Profile is one closed registry entry of D82: the network profile name,
// the native kind it runs and whether it is a public testnet with its own
// genesis and L1 set. A testnet is never an alias of its mainnet.
type Profile struct {
	Name      string
	Kind      NativeKind
	IsTestnet bool
	// Polygon marks the two Bor profiles whose live role requires the
	// finality.polygon section (D63).
	Polygon bool
}

// ProfileByKind groups the registry by native kind for table tests.
var profiles = []Profile{
	{Name: "ethereum-mainnet", Kind: NativeGeth},
	{Name: "bsc-mainnet", Kind: NativeBsc},
	{Name: "polygon-pos-mainnet", Kind: NativeBor, Polygon: true},
	{Name: "ethereum-sepolia", Kind: NativeGeth, IsTestnet: true},
	{Name: "ethereum-hoodi", Kind: NativeGeth, IsTestnet: true},
	{Name: "bsc-testnet", Kind: NativeBsc, IsTestnet: true},
	{Name: "polygon-pos-amoy", Kind: NativeBor, IsTestnet: true, Polygon: true},
}

// LookupProfile resolves a profile name to the closed registry entry.
// Unknown names, including the deferred Base/Arbitrum families of D42,
// are rejected.
func LookupProfile(name string) (Profile, bool) {
	for _, p := range profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// ProfileNames lists the closed registry in declaration order.
func ProfileNames() []string {
	out := make([]string, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, p.Name)
	}
	return out
}

// File is the whole YAML document. Every field of the dictionary is
// present as a declared key; sections of the other role are optional and
// their requiredness is decided by the checked role. Optional scalars
// keep their zero value when absent.
type File struct {
	SchemaVersion int         `yaml:"schema_version"`
	Network       Network     `yaml:"network"`
	Destination   Destination `yaml:"destination"`
	Native        *Native     `yaml:"native,omitempty"`
	Delivery      *Delivery   `yaml:"delivery,omitempty"`
	Storage       Storage     `yaml:"storage"`
	Gateway       *Gateway    `yaml:"gateway,omitempty"`
	Transport     Transport   `yaml:"transport"`
	Runtime       Runtime     `yaml:"runtime"`
	Finality      *Finality   `yaml:"finality,omitempty"`
}

// Network carries the profile name.
type Network struct {
	Profile string `yaml:"profile"`
}

// Destination names the immutable desired descriptor file both roles
// share. It never carries NATS credentials (D48, D54).
type Destination struct {
	BindingFile string `yaml:"binding_file"`
}

// Native declares the adapter family of the live role.
type Native struct {
	Kind string `yaml:"kind"`
}

// Delivery is the adapter-to-gateway link of the live role.
type Delivery struct {
	GatewayEndpoint string `yaml:"gateway_endpoint"`
	TLS             TLS    `yaml:"tls"`
}

// Storage holds the RocksDB directories. live_dir belongs to the
// adapter, gateway_dir to the gateway; they are separate writers and
// neither may contain the other.
type Storage struct {
	LiveDir    string `yaml:"live_dir"`
	GatewayDir string `yaml:"gateway_dir"`
}

// Gateway is the whole gateway-role section.
type Gateway struct {
	Listen string      `yaml:"listen"`
	TLS    TLS         `yaml:"tls"`
	NATS   GatewayNATS `yaml:"nats"`
}

// GatewayNATS is the gateway's single NATS instance (D82: one instance,
// no HA promise).
type GatewayNATS struct {
	Server          string `yaml:"server"`
	TLS             TLS    `yaml:"tls"`
	CredentialsFile string `yaml:"credentials_file"`
	Stream          string `yaml:"stream"`
	SubjectPrefix   string `yaml:"subject_prefix"`
}

// Transport carries the publication frame limit. A nil MaxFrameBytes is
// an absent field and takes the D52 default; an explicit zero is a
// positive-integer violation, not a default.
type Transport struct {
	MaxFrameBytes *uint64 `yaml:"max_frame_bytes"`
}

// Runtime carries process-level settings.
type Runtime struct {
	LogLevel string `yaml:"log_level"`
}

// Finality is the polygon-only supporting-source section (D63).
type Finality struct {
	Polygon *PolygonFinality `yaml:"polygon,omitempty"`
}

// PolygonFinality pins the dedicated supporting L1 of a polygon live
// role. The values are generated by the environment from verified
// bootstrap chain parameters; the adapter compares online responses
// against them and never accepts an arbitrary answer.
type PolygonFinality struct {
	L1Endpoint                 string `yaml:"l1_endpoint"`
	SourcesFile                string `yaml:"sources_file"`
	L1TLS                      TLS    `yaml:"l1_tls"`
	ExpectedRootChainAddress   string `yaml:"expected_root_chain_address"`
	ExpectedChildBlockInterval uint64 `yaml:"expected_child_block_interval"`
}

// TLS is the shared tagged TLS object. Mode selects the shape; the
// validator enforces which file fields each mode allows.
type TLS struct {
	Mode     string `yaml:"mode"`
	CAFile   string `yaml:"ca_file"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// TLSMode is the closed mode set of the TLS objects.
type TLSMode string

// The TLS modes. TLSDisabled forbids the file fields; TLSServer requires
// ca_file; TLSMutual requires ca_file, cert_file and key_file.
const (
	TLSDisabled TLSMode = "disabled"
	TLSServer   TLSMode = "server"
	TLSMutual   TLSMode = "mutual"
)

// ParseTLSMode converts a mode scalar. Unknown modes are rejected, not
// defaulted.
func ParseTLSMode(s string) (TLSMode, bool) {
	switch TLSMode(s) {
	case TLSDisabled:
		return TLSDisabled, true
	case TLSServer:
		return TLSServer, true
	case TLSMutual:
		return TLSMutual, true
	}
	return "", false
}

// Endpoint is one parsed endpoint URI. Scheme carries the wire form,
// Host and Port the TCP address, Path the UDS socket path. Userinfo is
// never accepted: credentials live in files, not URLs.
type Endpoint struct {
	Raw    string
	Scheme string
	Host   string
	Port   string
	Path   string
}

// Address returns host:port of a TCP endpoint.
func (e Endpoint) Address() string { return e.Host + ":" + e.Port }
