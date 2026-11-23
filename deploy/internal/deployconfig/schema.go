// The strict schemas of the live collector file and the gateway file
// (LIVE_CONFIG_SPEC, "Общая схема YAML v1"). One file is one network and
// one role: the live file carries the collector fields, the gateway file
// carries the gateway fields, and neither requires the other role's
// sections. Unknown or duplicate keys are rejected at every level, and
// the TLS objects follow the tagged-mode rules: disabled forbids extra
// fields, server requires ca_file, mutual requires ca_file, cert_file
// and key_file.
package deployconfig

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// TLSMode is the tagged TLS mode of a transport.
type TLSMode string

// The three TLS modes.
const (
	TLSDisabled TLSMode = "disabled"
	TLSServer   TLSMode = "server"
	TLSMutual   TLSMode = "mutual"
)

// TLSConfig is the tagged TLS object.
type TLSConfig struct {
	Mode     TLSMode `yaml:"mode"`
	CAFile   string  `yaml:"ca_file"`
	CertFile string  `yaml:"cert_file"`
	KeyFile  string  `yaml:"key_file"`
}

// Validate applies the mode rules and returns the effective fields.
func (t TLSConfig) Validate() error {
	switch t.Mode {
	case TLSDisabled:
		if t.CAFile != "" || t.CertFile != "" || t.KeyFile != "" {
			return fmt.Errorf("tls mode disabled forbids ca_file, cert_file and key_file")
		}
	case TLSServer:
		if t.CAFile == "" {
			return fmt.Errorf("tls mode server requires ca_file")
		}
		if t.CertFile != "" || t.KeyFile != "" {
			return fmt.Errorf("tls mode server forbids cert_file and key_file")
		}
	case TLSMutual:
		if t.CAFile == "" || t.CertFile == "" || t.KeyFile == "" {
			return fmt.Errorf("tls mode mutual requires ca_file, cert_file and key_file")
		}
	default:
		return fmt.Errorf("unknown tls mode %q", t.Mode)
	}
	return nil
}

// LiveConfig is the live collector file: schema_version, network,
// native, destination, delivery, storage and runtime.
type LiveConfig struct {
	SchemaVersion int `yaml:"schema_version"`
	Network       struct {
		Profile string `yaml:"profile"`
	} `yaml:"network"`
	Native struct {
		Kind string `yaml:"kind"`
	} `yaml:"native"`
	Destination struct {
		BindingFile string `yaml:"binding_file"`
	} `yaml:"destination"`
	Delivery struct {
		GatewayEndpoint string    `yaml:"gateway_endpoint"`
		TLS             TLSConfig `yaml:"tls"`
	} `yaml:"delivery"`
	Storage struct {
		LiveDir string `yaml:"live_dir"`
	} `yaml:"storage"`
	Runtime struct {
		LogLevel string `yaml:"log_level"`
	} `yaml:"runtime"`
}

// LoadLiveConfig reads and validates one live collector file.
func LoadLiveConfig(path string) (LiveConfig, error) {
	var c LiveConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("live config: %w", err)
	}
	if err := DecodeStrict(data, &c); err != nil {
		return c, fmt.Errorf("live config %s: %w", path, err)
	}
	if c.SchemaVersion != 1 {
		return c, fmt.Errorf("live config %s: schema_version %d, want 1", path, c.SchemaVersion)
	}
	if c.Network.Profile == "" {
		return c, fmt.Errorf("live config %s: network.profile is required", path)
	}
	network, err := ByProfile(c.Network.Profile)
	if err != nil {
		return c, fmt.Errorf("live config %s: %w", path, err)
	}
	if c.Native.Kind == "" {
		return c, fmt.Errorf("live config %s: native.kind is required", path)
	}
	if c.Native.Kind != network.NativeKind {
		return c, fmt.Errorf("live config %s: native.kind %q does not match profile %s (%s)",
			path, c.Native.Kind, c.Network.Profile, network.NativeKind)
	}
	if c.Destination.BindingFile == "" {
		return c, fmt.Errorf("live config %s: destination.binding_file is required", path)
	}
	if c.Delivery.GatewayEndpoint == "" {
		return c, fmt.Errorf("live config %s: delivery.gateway_endpoint is required", path)
	}
	if err := validateGatewayEndpoint(c.Delivery.GatewayEndpoint); err != nil {
		return c, fmt.Errorf("live config %s: %w", path, err)
	}
	if err := c.Delivery.TLS.Validate(); err != nil {
		return c, fmt.Errorf("live config %s: delivery.tls: %w", path, err)
	}
	if c.Delivery.TLS.Mode == TLSServer {
		return c, fmt.Errorf("live config %s: delivery.tls server-only mode is not allowed for the adapter to gateway transport", path)
	}
	if c.Delivery.TLS.Mode != TLSDisabled && !strings.HasPrefix(c.Delivery.GatewayEndpoint, "https://") {
		return c, fmt.Errorf("live config %s: TLS requires an https endpoint", path)
	}
	if c.Delivery.TLS.Mode == TLSDisabled && !strings.HasPrefix(c.Delivery.GatewayEndpoint, "unix://") {
		return c, fmt.Errorf("live config %s: disabled TLS is only valid with a unix endpoint", path)
	}
	if c.Storage.LiveDir == "" {
		return c, fmt.Errorf("live config %s: storage.live_dir is required", path)
	}
	if err := validateLogLevel(c.Runtime.LogLevel); err != nil {
		return c, fmt.Errorf("live config %s: %w", path, err)
	}
	return c, nil
}

// GatewayConfig is the gateway file: schema_version, network,
// destination, gateway (listen, tls, nats) and runtime.
type GatewayConfig struct {
	SchemaVersion int `yaml:"schema_version"`
	Network       struct {
		Profile string `yaml:"profile"`
	} `yaml:"network"`
	Destination struct {
		BindingFile string `yaml:"binding_file"`
	} `yaml:"destination"`
	Gateway struct {
		Listen string    `yaml:"listen"`
		TLS    TLSConfig `yaml:"tls"`
		NATS   struct {
			Server          string    `yaml:"server"`
			TLS             TLSConfig `yaml:"tls"`
			CredentialsFile string    `yaml:"credentials_file"`
			Stream          string    `yaml:"stream"`
			SubjectPrefix   string    `yaml:"subject_prefix"`
		} `yaml:"nats"`
	} `yaml:"gateway"`
	Storage struct {
		GatewayDir string `yaml:"gateway_dir"`
	} `yaml:"storage"`
	Transport struct {
		MaxFrameBytes uint64 `yaml:"max_frame_bytes"`
	} `yaml:"transport"`
	Runtime struct {
		LogLevel string `yaml:"log_level"`
	} `yaml:"runtime"`
}

// MaxFrameBytesDefault is the default transport frame budget: 2 MiB
// covering the full frame with its NATS headers.
const MaxFrameBytesDefault uint64 = 2097152

// LoadGatewayConfig reads and validates one gateway file.
func LoadGatewayConfig(path string) (GatewayConfig, error) {
	var c GatewayConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("gateway config: %w", err)
	}
	if err := DecodeStrict(data, &c); err != nil {
		return c, fmt.Errorf("gateway config %s: %w", path, err)
	}
	if c.SchemaVersion != 1 {
		return c, fmt.Errorf("gateway config %s: schema_version %d, want 1", path, c.SchemaVersion)
	}
	if c.Network.Profile == "" {
		return c, fmt.Errorf("gateway config %s: network.profile is required", path)
	}
	if _, err := ByProfile(c.Network.Profile); err != nil {
		return c, fmt.Errorf("gateway config %s: %w", path, err)
	}
	if c.Destination.BindingFile == "" {
		return c, fmt.Errorf("gateway config %s: destination.binding_file is required", path)
	}
	if c.Gateway.Listen == "" {
		return c, fmt.Errorf("gateway config %s: gateway.listen is required", path)
	}
	if !strings.HasPrefix(c.Gateway.Listen, "https://") && !strings.HasPrefix(c.Gateway.Listen, "unix://") {
		return c, fmt.Errorf("gateway config %s: gateway.listen must be https://host:port or unix:///path", path)
	}
	if err := c.Gateway.TLS.Validate(); err != nil {
		return c, fmt.Errorf("gateway config %s: gateway.tls: %w", path, err)
	}
	if c.Gateway.TLS.Mode == TLSServer {
		return c, fmt.Errorf("gateway config %s: gateway.tls server-only mode is not allowed for the adapter to gateway transport", path)
	}
	if (c.Gateway.TLS.Mode == TLSDisabled) != strings.HasPrefix(c.Gateway.Listen, "unix://") {
		return c, fmt.Errorf("gateway config %s: TLS mode and listen scheme disagree", path)
	}
	if c.Gateway.NATS.Server == "" {
		return c, fmt.Errorf("gateway config %s: gateway.nats.server is required", path)
	}
	if err := validateNATSURL(c.Gateway.NATS.Server, c.Gateway.NATS.TLS.Mode); err != nil {
		return c, fmt.Errorf("gateway config %s: %w", path, err)
	}
	if err := c.Gateway.NATS.TLS.Validate(); err != nil {
		return c, fmt.Errorf("gateway config %s: gateway.nats.tls: %w", path, err)
	}
	if c.Gateway.NATS.Stream == "" {
		return c, fmt.Errorf("gateway config %s: gateway.nats.stream is required", path)
	}
	if err := validateLiteral(c.Gateway.NATS.Stream, "stream name", false); err != nil {
		return c, fmt.Errorf("gateway config %s: %w", path, err)
	}
	if c.Gateway.NATS.SubjectPrefix == "" {
		return c, fmt.Errorf("gateway config %s: gateway.nats.subject_prefix is required", path)
	}
	if err := validateLiteral(c.Gateway.NATS.SubjectPrefix, "subject prefix", true); err != nil {
		return c, fmt.Errorf("gateway config %s: %w", path, err)
	}
	if c.Storage.GatewayDir == "" {
		return c, fmt.Errorf("gateway config %s: storage.gateway_dir is required", path)
	}
	if c.Transport.MaxFrameBytes == 0 {
		c.Transport.MaxFrameBytes = MaxFrameBytesDefault
	}
	if c.Transport.MaxFrameBytes > 64*1024*1024 {
		return c, fmt.Errorf("gateway config %s: transport.max_frame_bytes %d is above the sanity bound", path, c.Transport.MaxFrameBytes)
	}
	if err := validateLogLevel(c.Runtime.LogLevel); err != nil {
		return c, fmt.Errorf("gateway config %s: %w", path, err)
	}
	return c, nil
}

// LoadBindingFile reads, parses and validates the binding descriptor of
// one file path.
func LoadBindingFile(path string) (Binding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Binding{}, fmt.Errorf("binding file: %w", err)
	}
	b, err := ParseBinding(data)
	if err != nil {
		return Binding{}, fmt.Errorf("binding file %s: %w", path, err)
	}
	return b, nil
}

// validateLogLevel accepts the four levels with info as the default.
func validateLogLevel(level string) error {
	switch level {
	case "", "debug", "info", "warn", "error":
		return nil
	}
	return fmt.Errorf("runtime.log_level %q is not debug, info, warn or error", level)
}

// validateGatewayEndpoint accepts the two live endpoint forms.
func validateGatewayEndpoint(endpoint string) error {
	if strings.HasPrefix(endpoint, "unix://") {
		if !strings.HasPrefix(endpoint, "unix:///") {
			return fmt.Errorf("delivery.gateway_endpoint %q is not an absolute UDS path", endpoint)
		}
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("delivery.gateway_endpoint %q: %w", endpoint, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("delivery.gateway_endpoint %q must be https://host:port or unix:///path", endpoint)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("delivery.gateway_endpoint %q holds a path", endpoint)
	}
	return nil
}

// validateNATSURL checks the scheme against the TLS mode: disabled is
// nats://, every other mode is tls://.
func validateNATSURL(server string, mode TLSMode) error {
	u, err := url.Parse(server)
	if err != nil {
		return fmt.Errorf("gateway.nats.server %q: %w", server, err)
	}
	switch mode {
	case TLSDisabled:
		if u.Scheme != "nats" {
			return fmt.Errorf("gateway.nats.server %q must be nats:// with tls disabled", server)
		}
	default:
		if u.Scheme != "tls" {
			return fmt.Errorf("gateway.nats.server %q must be tls:// with tls %s", server, mode)
		}
	}
	if u.Host == "" {
		return fmt.Errorf("gateway.nats.server %q holds no host", server)
	}
	return nil
}
