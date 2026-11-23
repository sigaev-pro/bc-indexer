package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sigaev-pro/bc-indexer/gateway/destination"
)

// The role checks of E06-07 (LIVE_CONFIG_SPEC, "Валидация и остановка"):
// the structure of the whole document is validated, while required
// fields, local files and credentials are checked only for the selected
// role. The check is side-effect-free: it reads the YAML and the
// referenced files, and it creates no directory, database, stream or run.

// ResolvedTLS is one TLS block with its file paths resolved against the
// YAML directory.
type ResolvedTLS struct {
	Mode     TLSMode
	CAFile   string
	CertFile string
	KeyFile  string
}

// PolygonConfig is the resolved finality.polygon section of a polygon
// live role.
type PolygonConfig struct {
	L1Endpoint                 Endpoint
	L1TLS                      ResolvedTLS
	SourcesFile                string
	ExpectedRootChainAddress   string
	ExpectedChildBlockInterval uint64
}

// LiveConfig is the validated live-role view of one YAML file: the
// closed profile entry, the parsed desired binding, the resolved paths
// and the applied defaults.
type LiveConfig struct {
	Path       string
	File       *File
	Profile    Profile
	NativeKind NativeKind

	Binding     destination.Binding
	BindingPath string

	Endpoint Endpoint
	TLS      ResolvedTLS
	LiveDir  string
	LogLevel LogLevel
	Polygon  *PolygonConfig
}

// GatewayConfig is the validated gateway-role view of one YAML file.
type GatewayConfig struct {
	Path    string
	File    *File
	Profile Profile

	Binding     destination.Binding
	BindingPath string

	Listen  Endpoint
	TLS     ResolvedTLS
	NATS    Endpoint
	NATSTLS ResolvedTLS

	CredentialsPath string
	Stream          string
	SubjectPrefix   string
	MaxFrameBytes   uint64
	GatewayDir      string
	LogLevel        LogLevel
}

// CheckLive loads and validates the live role of the YAML at path.
func CheckLive(path string) (*LiveConfig, error) {
	f, err := Load(path)
	if err != nil {
		return nil, err
	}
	base, err := yamlDir(path)
	if err != nil {
		return nil, err
	}
	var errs ErrorList
	common(f, &errs)

	prof := profileOf(f, &errs)

	// The whole document is structure-checked; the sections below exist
	// for the live role and are optional for the gateway role.
	if f.Gateway != nil {
		checkGatewayShape(f, base, false, &errs)
	}
	checkFinalityAllowed(prof, RoleLive, f.Finality, &errs)

	// native is required for live and must match the profile.
	var kind NativeKind
	if f.Native == nil {
		errf(&errs, ExitConfig, "native", "the section is required for the live role")
	} else {
		kind = checkNative(f, prof, true, &errs)
	}

	// delivery is required for live: endpoint and mutual-or-disabled TLS.
	var endpoint Endpoint
	var tls ResolvedTLS
	if f.Delivery == nil {
		errf(&errs, ExitConfig, "delivery", "the section is required for the live role")
	} else {
		endpoint = checkEndpointField(f.Delivery.GatewayEndpoint, "delivery.gateway_endpoint",
			map[string]bool{"unix": true, "https": true}, &errs)
		tls = checkTLSBlock(&f.Delivery.TLS, "delivery.tls", false, &errs)
		checkTLSWithEndpoint(tls, endpoint, "delivery.tls", &errs)
		tls.resolve(base)
		if tls.Mode == TLSMutual {
			checkReadableFile(tls.CAFile, "delivery.tls.ca_file", &errs)
			checkReadableFile(tls.CertFile, "delivery.tls.cert_file", &errs)
			checkReadableFile(tls.KeyFile, "delivery.tls.key_file", &errs)
		}
	}

	liveDir := checkStorageDir(f.Storage.LiveDir, "storage.live_dir", base, &errs)
	gatewayDir := ""
	if f.Storage.GatewayDir != "" {
		gatewayDir = checkStorageDir(f.Storage.GatewayDir, "storage.gateway_dir", base, &errs)
		checkStorageSeparation(liveDir, gatewayDir, &errs)
	}

	binding, bindingPath := checkBinding(f, base, &errs)

	var polygon *PolygonConfig
	if prof.Polygon {
		polygon = checkPolygon(f, base, &errs)
	}

	level := checkLogLevel(f.Runtime.LogLevel, &errs)

	if len(errs) > 0 {
		return nil, firstError(errs)
	}
	return &LiveConfig{
		Path:        path,
		File:        f,
		Profile:     prof,
		NativeKind:  kind,
		Binding:     binding,
		BindingPath: bindingPath,
		Endpoint:    endpoint,
		TLS:         tls,
		LiveDir:     liveDir,
		LogLevel:    level,
		Polygon:     polygon,
	}, nil
}

// CheckGateway loads and validates the gateway role of the YAML at path.
func CheckGateway(path string) (*GatewayConfig, error) {
	f, err := Load(path)
	if err != nil {
		return nil, err
	}
	base, err := yamlDir(path)
	if err != nil {
		return nil, err
	}
	var errs ErrorList
	common(f, &errs)

	prof := profileOf(f, &errs)

	// The live sections are structure-checked when present; their files
	// and requiredness belong to the live role only.
	if f.Native != nil {
		checkNative(f, prof, false, &errs)
	}
	if f.Delivery != nil {
		var e Endpoint
		if f.Delivery.GatewayEndpoint != "" {
			e = checkEndpointField(f.Delivery.GatewayEndpoint, "delivery.gateway_endpoint",
				map[string]bool{"unix": true, "https": true}, &errs)
		}
		if tlsBlockPresent(f.Delivery.TLS) {
			t := checkTLSBlock(&f.Delivery.TLS, "delivery.tls", false, &errs)
			if e.Raw != "" {
				checkTLSWithEndpoint(t, e, "delivery.tls", &errs)
			}
		}
	}
	if f.Finality != nil {
		checkFinalityAllowed(prof, RoleGateway, f.Finality, &errs)
	}

	gw, tls, nats, natstls, creds := checkGatewayShape(f, base, true, &errs)

	maxFrame := DefaultMaxFrameBytes
	if f.Transport.MaxFrameBytes != nil {
		if *f.Transport.MaxFrameBytes == 0 {
			errf(&errs, ExitConfig, "transport.max_frame_bytes", "the value must be a positive integer")
		} else {
			maxFrame = *f.Transport.MaxFrameBytes
		}
	}

	gatewayDir := checkStorageDir(f.Storage.GatewayDir, "storage.gateway_dir", base, &errs)
	if f.Storage.LiveDir != "" {
		liveDir := checkStorageDir(f.Storage.LiveDir, "storage.live_dir", base, &errs)
		checkStorageSeparation(liveDir, gatewayDir, &errs)
	}

	binding, bindingPath := checkBinding(f, base, &errs)
	if bindingPath != "" {
		// D54: stream and subject_prefix are verifiable duplicates of
		// the desired descriptor, never an alternative route.
		if f.Gateway != nil && f.Gateway.NATS.Stream != "" && f.Gateway.NATS.Stream != binding.Stream {
			errf(&errs, ExitConfig, "gateway.nats.stream",
				"stream %s does not equal the desired descriptor stream %s",
				quote(f.Gateway.NATS.Stream), quote(binding.Stream))
		}
		if f.Gateway != nil && f.Gateway.NATS.SubjectPrefix != "" && f.Gateway.NATS.SubjectPrefix != binding.SubjectPrefix {
			errf(&errs, ExitConfig, "gateway.nats.subject_prefix",
				"subject_prefix %s does not equal the desired descriptor prefix %s",
				quote(f.Gateway.NATS.SubjectPrefix), quote(binding.SubjectPrefix))
		}
	}

	level := checkLogLevel(f.Runtime.LogLevel, &errs)

	stream, prefix := "", ""
	if f.Gateway != nil {
		stream, prefix = f.Gateway.NATS.Stream, f.Gateway.NATS.SubjectPrefix
	}

	if len(errs) > 0 {
		return nil, firstError(errs)
	}
	return &GatewayConfig{
		Path:            path,
		File:            f,
		Profile:         prof,
		Binding:         binding,
		BindingPath:     bindingPath,
		Listen:          gw,
		TLS:             tls,
		NATS:            nats,
		NATSTLS:         natstls,
		CredentialsPath: creds,
		Stream:          stream,
		SubjectPrefix:   prefix,
		MaxFrameBytes:   maxFrame,
		GatewayDir:      gatewayDir,
		LogLevel:        level,
	}, nil
}

// CheckRole dispatches on the role.
func CheckRole(path string, role Role) (any, error) {
	switch role {
	case RoleLive:
		return CheckLive(path)
	case RoleGateway:
		return CheckGateway(path)
	}
	return nil, &Error{Field: "role", Code: ExitConfig, Reason: "unknown role " + quote(string(role))}
}

// yamlDir returns the absolute directory of the YAML file: the base of
// every relative path in it (D78), whatever the process cwd is.
func yamlDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", &Error{Field: "config", Code: ExitConfig, Reason: "cannot resolve " + quote(path) + ": " + err.Error()}
	}
	return filepath.Dir(abs), nil
}

// common validates the fields every role shares.
func common(f *File, errs *ErrorList) {
	if f.SchemaVersion != SchemaVersion1 {
		errf(errs, ExitConfig, "schema_version", "value must be exactly %d, got %d",
			SchemaVersion1, f.SchemaVersion)
	}
}

// profileOf resolves the closed registry entry of network.profile.
func profileOf(f *File, errs *ErrorList) Profile {
	if f.Network.Profile == "" {
		errf(errs, ExitConfig, "network.profile", "the field is required")
		return Profile{}
	}
	prof, ok := LookupProfile(f.Network.Profile)
	if !ok {
		errf(errs, ExitConfig, "network.profile",
			"unknown profile %s; the closed registry is %s and the deferred Base/Arbitrum families are not supported (D42)",
			quote(f.Network.Profile), strings.Join(ProfileNames(), ", "))
		return Profile{}
	}
	return prof
}

// checkNative validates the declared kind against the profile. An
// empty kind is an error only for the running live role; a present
// value is always checked.
func checkNative(f *File, prof Profile, required bool, errs *ErrorList) NativeKind {
	if f.Native.Kind == "" {
		if required {
			errf(errs, ExitConfig, "native.kind", "the field is required for the live role")
		}
		return ""
	}
	switch NativeKind(f.Native.Kind) {
	case NativeGeth, NativeBsc, NativeBor:
	default:
		errf(errs, ExitConfig, "native.kind", "unknown kind %s, want geth, bsc or bor", quote(f.Native.Kind))
		return ""
	}
	kind := NativeKind(f.Native.Kind)
	if prof.Name != "" && kind != prof.Kind {
		errf(errs, ExitConfig, "native.kind", "kind %s does not match profile %s, want %s",
			quote(f.Native.Kind), quote(prof.Name), quote(string(prof.Kind)))
	}
	return kind
}

// checkFinalityAllowed enforces the D63 rule: finality.polygon exists
// only in a polygon live role.
func checkFinalityAllowed(prof Profile, role Role, fin *Finality, errs *ErrorList) {
	if fin == nil || fin.Polygon == nil {
		return
	}
	if role != RoleLive || !prof.Polygon {
		errf(errs, ExitConfig, "finality.polygon",
			"the section is allowed only in a polygon live role (D63), not profile %s role %s",
			quote(prof.Name), quote(string(role)))
	}
}

// tlsBlockPresent reports whether any field of a TLS block is set: an
// entirely absent block is skipped for the non-running role, while any
// present value demands the full explicit shape.
func tlsBlockPresent(t TLS) bool {
	return t.Mode != "" || t.CAFile != "" || t.CertFile != "" || t.KeyFile != ""
}

// checkGatewayShape validates the gateway section. Required fields,
// local files and credentials are checked only for the gateway role
// (required=true); for the other role the section is still
// structure-checked value by value, and absent values stay absent.
// Returns the resolved endpoint values.
func checkGatewayShape(f *File, base string, required bool, errs *ErrorList) (listen Endpoint, tls ResolvedTLS, nats Endpoint, natstls ResolvedTLS, creds string) {
	if f.Gateway == nil {
		if required {
			errf(errs, ExitConfig, "gateway", "the section is required for the gateway role")
		}
		return
	}
	gw := f.Gateway
	if required || gw.Listen != "" {
		listen = checkEndpointField(gw.Listen, "gateway.listen",
			map[string]bool{"unix": true, "https": true}, errs)
	}
	if required || tlsBlockPresent(gw.TLS) {
		tls = checkTLSBlock(&gw.TLS, "gateway.tls", false, errs)
		if listen.Raw != "" {
			checkTLSWithEndpoint(tls, listen, "gateway.tls", errs)
		}
		tls.resolve(base)
	}
	if required || gw.NATS.Server != "" {
		nats = checkEndpointField(gw.NATS.Server, "gateway.nats.server",
			map[string]bool{"nats": true, "tls": true}, errs)
	}
	if required || tlsBlockPresent(gw.NATS.TLS) {
		natstls = checkTLSBlock(&gw.NATS.TLS, "gateway.nats.tls", true, errs)
		if nats.Scheme == "nats" && natstls.Mode != TLSDisabled {
			errf(errs, ExitConfig, "gateway.nats.tls", "mode must be disabled for a nats:// server URL, got %s", quote(string(natstls.Mode)))
		}
		if nats.Scheme == "tls" && natstls.Mode == TLSDisabled {
			errf(errs, ExitConfig, "gateway.nats.tls", "mode must be server or mutual for a tls:// server URL")
		}
		natstls.resolve(base)
	}

	if gw.NATS.CredentialsFile != "" {
		creds = resolvePath(base, gw.NATS.CredentialsFile)
		if required {
			checkReadableFile(creds, "gateway.nats.credentials_file", errs)
		}
	}

	if required {
		if gw.NATS.Stream == "" {
			errf(errs, ExitConfig, "gateway.nats.stream", "the field is required for the gateway role")
		} else if err := destination.ValidateStreamName(gw.NATS.Stream); err != nil {
			errf(errs, ExitConfig, "gateway.nats.stream", "invalid stream name: %v", err)
		}
		if gw.NATS.SubjectPrefix == "" {
			errf(errs, ExitConfig, "gateway.nats.subject_prefix", "the field is required for the gateway role")
		} else if err := destination.ValidateSubjectPrefix(gw.NATS.SubjectPrefix); err != nil {
			errf(errs, ExitConfig, "gateway.nats.subject_prefix", "invalid subject prefix: %v", err)
		}
		if natstls.Mode == TLSServer {
			checkReadableFile(natstls.CAFile, "gateway.nats.tls.ca_file", errs)
		}
		if natstls.Mode == TLSMutual {
			checkReadableFile(natstls.CAFile, "gateway.nats.tls.ca_file", errs)
			checkReadableFile(natstls.CertFile, "gateway.nats.tls.cert_file", errs)
			checkReadableFile(natstls.KeyFile, "gateway.nats.tls.key_file", errs)
		}
		if tls.Mode == TLSMutual {
			checkReadableFile(tls.CAFile, "gateway.tls.ca_file", errs)
			checkReadableFile(tls.CertFile, "gateway.tls.cert_file", errs)
			checkReadableFile(tls.KeyFile, "gateway.tls.key_file", errs)
		}
	} else {
		// Present values of the non-running role still get their form
		// checked; requiredness does not apply.
		if gw.NATS.Stream != "" {
			if err := destination.ValidateStreamName(gw.NATS.Stream); err != nil {
				errf(errs, ExitConfig, "gateway.nats.stream", "invalid stream name: %v", err)
			}
		}
		if gw.NATS.SubjectPrefix != "" {
			if err := destination.ValidateSubjectPrefix(gw.NATS.SubjectPrefix); err != nil {
				errf(errs, ExitConfig, "gateway.nats.subject_prefix", "invalid subject prefix: %v", err)
			}
		}
	}
	return
}

// checkPolygon validates the whole finality.polygon section of a polygon
// live role and resolves its files.
func checkPolygon(f *File, base string, errs *ErrorList) *PolygonConfig {
	fin := f.Finality
	if fin == nil || fin.Polygon == nil {
		errf(errs, ExitConfig, "finality.polygon", "the section is required for a polygon live role")
		return nil
	}
	p := fin.Polygon
	out := &PolygonConfig{}

	out.L1Endpoint = checkEndpointField(p.L1Endpoint, "finality.polygon.l1_endpoint",
		map[string]bool{"http": true, "https": true}, errs)
	out.L1TLS = checkTLSBlock(&p.L1TLS, "finality.polygon.l1_tls", true, errs)
	switch out.L1Endpoint.Scheme {
	case "http":
		if out.L1TLS.Mode != TLSDisabled {
			errf(errs, ExitConfig, "finality.polygon.l1_tls", "mode must be disabled for an http L1 endpoint, got %s", quote(string(out.L1TLS.Mode)))
		}
	case "https":
		if out.L1TLS.Mode == TLSDisabled {
			errf(errs, ExitConfig, "finality.polygon.l1_tls", "mode must be server or mutual for an https L1 endpoint")
		}
	}
	out.L1TLS.resolve(base)
	if out.L1TLS.Mode == TLSServer {
		checkReadableFile(out.L1TLS.CAFile, "finality.polygon.l1_tls.ca_file", errs)
	}
	if out.L1TLS.Mode == TLSMutual {
		checkReadableFile(out.L1TLS.CAFile, "finality.polygon.l1_tls.ca_file", errs)
		checkReadableFile(out.L1TLS.CertFile, "finality.polygon.l1_tls.cert_file", errs)
		checkReadableFile(out.L1TLS.KeyFile, "finality.polygon.l1_tls.key_file", errs)
	}

	if p.SourcesFile == "" {
		errf(errs, ExitConfig, "finality.polygon.sources_file", "the field is required for a polygon live role")
	} else {
		// D78: the field-specific restriction wins over the general
		// relative-path rule, even when a file sits next to the YAML.
		if !filepath.IsAbs(p.SourcesFile) {
			errf(errs, ExitConfig, "finality.polygon.sources_file",
				"the path must be absolute, got relative %s", quote(p.SourcesFile))
		} else {
			out.SourcesFile = filepath.Clean(p.SourcesFile)
			checkReadableFile(out.SourcesFile, "finality.polygon.sources_file", errs)
		}
	}

	if p.ExpectedRootChainAddress == "" {
		errf(errs, ExitConfig, "finality.polygon.expected_root_chain_address", "the field is required for a polygon live role")
	} else if !isLowerHex40(p.ExpectedRootChainAddress) {
		errf(errs, ExitConfig, "finality.polygon.expected_root_chain_address",
			"value must be 40 lowercase hex characters without 0x, got %s", quote(p.ExpectedRootChainAddress))
	} else {
		out.ExpectedRootChainAddress = p.ExpectedRootChainAddress
	}

	if p.ExpectedChildBlockInterval == 0 {
		errf(errs, ExitConfig, "finality.polygon.expected_child_block_interval", "the value must be a positive integer")
	} else {
		out.ExpectedChildBlockInterval = p.ExpectedChildBlockInterval
	}
	return out
}

// checkTLSBlock validates the shape of one TLS object and returns its
// mode. allowServer marks the blocks where server-only TLS is offered
// (gateway NATS and the polygon L1); the adapter-to-gateway link is
// mutual or disabled only.
func checkTLSBlock(t *TLS, field string, allowServer bool, errs *ErrorList) ResolvedTLS {
	if t.Mode == "" {
		errf(errs, ExitConfig, joinPath(field, "mode"), "the field is required")
		return ResolvedTLS{}
	}
	mode, ok := ParseTLSMode(t.Mode)
	if !ok {
		errf(errs, ExitConfig, joinPath(field, "mode"), "unknown mode %s, want disabled, server or mutual", quote(t.Mode))
		return ResolvedTLS{}
	}
	set := func() bool { return t.CAFile != "" || t.CertFile != "" || t.KeyFile != "" }
	switch mode {
	case TLSDisabled:
		if set() {
			errf(errs, ExitConfig, field, "mode disabled forbids ca_file, cert_file and key_file")
		}
	case TLSServer:
		if !allowServer {
			errf(errs, ExitConfig, joinPath(field, "mode"), "server-only TLS is not allowed on this link, want mutual")
		}
		if t.CAFile == "" {
			errf(errs, ExitConfig, joinPath(field, "ca_file"), "the field is required in server mode")
		}
		if t.CertFile != "" || t.KeyFile != "" {
			errf(errs, ExitConfig, field, "server mode forbids cert_file and key_file")
		}
	case TLSMutual:
		if t.CAFile == "" {
			errf(errs, ExitConfig, joinPath(field, "ca_file"), "the field is required in mutual mode")
		}
		if t.CertFile == "" {
			errf(errs, ExitConfig, joinPath(field, "cert_file"), "the field is required in mutual mode")
		}
		if t.KeyFile == "" {
			errf(errs, ExitConfig, joinPath(field, "key_file"), "the field is required in mutual mode")
		}
	}
	return ResolvedTLS{Mode: mode, CAFile: t.CAFile, CertFile: t.CertFile, KeyFile: t.KeyFile}
}

// resolve resolves the file fields of one block against the YAML
// directory, in place.
func (t *ResolvedTLS) resolve(base string) {
	t.CAFile = resolvePath(base, t.CAFile)
	t.CertFile = resolvePath(base, t.CertFile)
	t.KeyFile = resolvePath(base, t.KeyFile)
}

// checkTLSWithEndpoint enforces the scheme/mode pairing: a UDS endpoint
// is protected by filesystem permissions and forbids TLS, a TCP endpoint
// requires TLS.
func checkTLSWithEndpoint(t ResolvedTLS, e Endpoint, field string, errs *ErrorList) {
	if e.Raw == "" || t.Mode == "" {
		return
	}
	if e.Scheme == "unix" && t.Mode != TLSDisabled {
		errf(errs, ExitConfig, field, "mode must be disabled for a unix socket endpoint, got %s", quote(string(t.Mode)))
	}
	if e.Scheme != "unix" && t.Mode == TLSDisabled {
		errf(errs, ExitConfig, field, "mode must be mutual for a TCP endpoint, got disabled")
	}
}

// checkEndpointField parses one endpoint URI against its allowed
// schemes. Userinfo is rejected everywhere: credentials live in files.
func checkEndpointField(raw, field string, allowed map[string]bool, errs *ErrorList) Endpoint {
	if raw == "" {
		errf(errs, ExitConfig, field, "the field is required")
		return Endpoint{}
	}
	e, err := ParseEndpoint(raw, allowed)
	if err != nil {
		*errs = append(*errs, &Error{Field: field, Code: ExitConfig, Reason: err.Error()})
		return Endpoint{}
	}
	return e
}

// ParseEndpoint parses and validates one endpoint URI form.
func ParseEndpoint(raw string, allowed map[string]bool) (Endpoint, error) {
	sep := strings.Index(raw, "://")
	if sep <= 0 {
		return Endpoint{}, fmt.Errorf("value %s is not a URI with an explicit scheme", quote(raw))
	}
	e := Endpoint{Raw: raw, Scheme: raw[:sep]}
	if !allowed[e.Scheme] {
		return Endpoint{}, fmt.Errorf("scheme %s is not allowed here, want one of %s",
			quote(e.Scheme), joinSortedKeys(allowed))
	}
	rest := raw[sep+3:]
	if strings.ContainsAny(rest, "?#") {
		return Endpoint{}, fmt.Errorf("query and fragment parts are not accepted in %s", quote(raw))
	}
	if e.Scheme == "unix" {
		if rest == "" || rest[0] != '/' {
			return Endpoint{}, fmt.Errorf("unix endpoint %s must carry an absolute socket path (unix:///path)", quote(raw))
		}
		e.Path = rest
		return e, nil
	}
	// TCP forms: authority only, no path.
	authority, path, hasPath := strings.Cut(rest, "/")
	if hasPath && path != "" {
		return Endpoint{}, fmt.Errorf("a path is not accepted in %s", quote(raw))
	}
	if strings.Contains(authority, "@") {
		return Endpoint{}, fmt.Errorf("userinfo is not accepted in %s; credentials belong in files", quote(raw))
	}
	host, port, err := splitHostPort(authority)
	if err != nil {
		return Endpoint{}, err
	}
	if host == "" {
		return Endpoint{}, fmt.Errorf("host is missing in %s", quote(raw))
	}
	if port == "" {
		return Endpoint{}, fmt.Errorf("an explicit port is required in %s", quote(raw))
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Endpoint{}, fmt.Errorf("port %s is not a valid TCP port", quote(port))
	}
	e.Host, e.Port = host, port
	return e, nil
}

// splitHostPort splits an authority, accepting bracketed IPv6 literals.
func splitHostPort(authority string) (host, port string, err error) {
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", "", fmt.Errorf("unterminated IPv6 literal in %s", quote(authority))
		}
		host = authority[:end+1]
		rest := authority[end+1:]
		if rest == "" {
			return host, "", nil
		}
		if rest[0] != ':' {
			return "", "", fmt.Errorf("unexpected content after the IPv6 literal in %s", quote(authority))
		}
		return host, rest[1:], nil
	}
	host, port, found := strings.Cut(authority, ":")
	if !found {
		return host, "", nil
	}
	if strings.Contains(port, ":") {
		return "", "", fmt.Errorf("several colons in %s; bracket IPv6 literals", quote(authority))
	}
	return host, port, nil
}

// joinSortedKeys renders the allowed schemes of an endpoint field.
func joinSortedKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Schemes are few; keep the declaration order stable by sorting.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return strings.Join(keys, ", ")
}

// resolvePath applies the D78 rule: absolute values stay, relative
// values join the YAML directory. Resolution never creates anything.
func resolvePath(base, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

// checkStorageDir validates and resolves one storage directory field.
func checkStorageDir(dir, field, base string, errs *ErrorList) string {
	if dir == "" {
		errf(errs, ExitConfig, field, "the field is required")
		return ""
	}
	return resolvePath(base, dir)
}

// checkStorageSeparation rejects equal or nested live and gateway
// directories. The native datadir is compared the same way at state
// opening; it is not part of the YAML.
func checkStorageSeparation(liveDir, gatewayDir string, errs *ErrorList) {
	if liveDir == "" || gatewayDir == "" {
		return
	}
	a, b := resolveSymlinks(liveDir), resolveSymlinks(gatewayDir)
	if a == b {
		errf(errs, ExitConfig, "storage.gateway_dir",
			"gateway_dir %s equals live_dir %s; each store has its own writer", quote(gatewayDir), quote(liveDir))
		return
	}
	if containsPath(a, b) || containsPath(b, a) {
		errf(errs, ExitConfig, "storage.gateway_dir",
			"gateway_dir %s and live_dir %s must not contain one another", quote(gatewayDir), quote(liveDir))
	}
}

// resolveSymlinks resolves a path when it exists, best effort, mirroring
// the state-opening rule that symlinks resolve before comparison.
func resolveSymlinks(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// containsPath reports whether parent contains child (or equals it).
func containsPath(parent, child string) bool {
	if parent == child {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

// checkReadableFile requires an existing regular file. Reading it is
// the check's only filesystem effect.
func checkReadableFile(abs, field string, errs *ErrorList) {
	st, err := os.Stat(abs)
	if err != nil {
		errf(errs, ExitConfig, field, "cannot access %s: %v", quote(abs), err)
		return
	}
	if st.IsDir() {
		errf(errs, ExitConfig, field, "%s is a directory, a file is required", quote(abs))
	}
}

// checkBinding loads and parses the desired descriptor. The check
// verifies the local descriptor form and digest (D77) and claims
// nothing about remote stream metadata.
func checkBinding(f *File, base string, errs *ErrorList) (destination.Binding, string) {
	if f.Destination.BindingFile == "" {
		errf(errs, ExitConfig, "destination.binding_file", "the field is required for both roles")
		return destination.Binding{}, ""
	}
	path := resolvePath(base, f.Destination.BindingFile)
	checkReadableFile(path, "destination.binding_file", errs)
	data, err := os.ReadFile(path)
	if err != nil {
		errf(errs, ExitConfig, "destination.binding_file", "cannot read %s: %v", quote(path), err)
		return destination.Binding{}, path
	}
	if len(data) > destination.MaxDescriptorBytes {
		errf(errs, ExitConfig, "destination.binding_file",
			"descriptor is %d bytes, bound is %d", len(data), destination.MaxDescriptorBytes)
		return destination.Binding{}, path
	}
	b, err := destination.ParseBinding(data)
	if err != nil {
		errf(errs, ExitConfig, "destination.binding_file", "invalid descriptor: %v", err)
		return destination.Binding{}, path
	}
	return b, path
}

// checkLogLevel parses the optional level with its info default.
func checkLogLevel(s string, errs *ErrorList) LogLevel {
	if s == "" {
		return LevelInfo
	}
	level, err := ParseLogLevel(s)
	if err != nil {
		errf(errs, ExitConfig, "runtime.log_level", "unknown level %s, want debug, info, warn or error", quote(s))
		return LevelInfo
	}
	return level
}

// isLowerHex40 matches the root-chain address form: 40 lowercase hex
// characters without the 0x prefix.
func isLowerHex40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
