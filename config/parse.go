package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// The strict reader of the YAML schema v1. Two layers enforce the closed
// document: a schema walk over the parsed node tree rejects unknown and
// duplicate keys, wrong scalar types, aliases, merge keys and extra
// documents with a field path and line; a typed decode with
// KnownFields(true) then binds the values and reports overflows. Nothing
// is defaulted on the way in: defaults are applied by the role check,
// never by the parser.

// yamlSpec is one node of the closed schema tree: either a mapping with
// declared children or a scalar of one declared YAML type.
type yamlSpec struct {
	mapKeys map[string]yamlSpec
	scalar  string // "!!int" or "!!str" when this node is a scalar
}

func scalarSpec(tag string) yamlSpec { return yamlSpec{scalar: tag} }

func mapSpec(keys map[string]yamlSpec) yamlSpec { return yamlSpec{mapKeys: keys} }

// tlsSpec is the shared TLS object shape.
var tlsSpec = mapSpec(map[string]yamlSpec{
	"mode":      scalarSpec("!!str"),
	"ca_file":   scalarSpec("!!str"),
	"cert_file": scalarSpec("!!str"),
	"key_file":  scalarSpec("!!str"),
})

// fileSpec is the whole document schema: exactly the declared keys of
// LIVE_CONFIG_SPEC, no more.
var fileSpec = mapSpec(map[string]yamlSpec{
	"schema_version": scalarSpec("!!int"),
	"network": mapSpec(map[string]yamlSpec{
		"profile": scalarSpec("!!str"),
	}),
	"destination": mapSpec(map[string]yamlSpec{
		"binding_file": scalarSpec("!!str"),
	}),
	"native": mapSpec(map[string]yamlSpec{
		"kind": scalarSpec("!!str"),
	}),
	"delivery": mapSpec(map[string]yamlSpec{
		"gateway_endpoint": scalarSpec("!!str"),
		"tls":              tlsSpec,
	}),
	"storage": mapSpec(map[string]yamlSpec{
		"live_dir":    scalarSpec("!!str"),
		"gateway_dir": scalarSpec("!!str"),
	}),
	"gateway": mapSpec(map[string]yamlSpec{
		"listen": scalarSpec("!!str"),
		"tls":    tlsSpec,
		"nats": mapSpec(map[string]yamlSpec{
			"server":           scalarSpec("!!str"),
			"tls":              tlsSpec,
			"credentials_file": scalarSpec("!!str"),
			"stream":           scalarSpec("!!str"),
			"subject_prefix":   scalarSpec("!!str"),
		}),
	}),
	"transport": mapSpec(map[string]yamlSpec{
		"max_frame_bytes": scalarSpec("!!int"),
	}),
	"runtime": mapSpec(map[string]yamlSpec{
		"log_level": scalarSpec("!!str"),
	}),
	"finality": mapSpec(map[string]yamlSpec{
		"polygon": mapSpec(map[string]yamlSpec{
			"l1_endpoint":                   scalarSpec("!!str"),
			"sources_file":                  scalarSpec("!!str"),
			"l1_tls":                        tlsSpec,
			"expected_root_chain_address":   scalarSpec("!!str"),
			"expected_child_block_interval": scalarSpec("!!int"),
		}),
	}),
})

// Load reads and strictly parses the YAML file at path. It creates
// nothing and writes nothing.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{Field: "config", Code: ExitConfig, Reason: "cannot read " + quote(path) + ": " + err.Error()}
	}
	if len(data) > MaxYAMLBytes {
		return nil, &Error{Field: "config", Code: ExitConfig,
			Reason: fmt.Sprintf("file is %d bytes, bound is %d", len(data), MaxYAMLBytes)}
	}
	return Parse(data)
}

// Parse strictly parses YAML bytes. The returned file carries only the
// declared keys; anything else is an error listing every violation.
func Parse(data []byte) (*File, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &Error{Field: "config", Code: ExitConfig, Reason: "the document is empty"}
		}
		return nil, &Error{Field: "config", Code: ExitConfig, Reason: "YAML syntax: " + err.Error()}
	}
	// A stream with several documents is rejected: the decoder would
	// otherwise silently keep the first one.
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, &Error{Field: "config", Code: ExitConfig, Reason: "exactly one YAML document is allowed"}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, &Error{Field: "config", Code: ExitConfig,
			Reason: fmt.Sprintf("expected one mapping document, got %s", nodeKindName(doc.Kind))}
	}

	var errs ErrorList
	walk(doc.Content[0], "", fileSpec, &errs)
	if len(errs) > 0 {
		return nil, firstError(errs)
	}

	var f File
	vdec := yaml.NewDecoder(bytes.NewReader(data))
	vdec.KnownFields(true)
	if err := vdec.Decode(&f); err != nil {
		return nil, decodeErrors(err)
	}
	return &f, nil
}

// walk checks one node against its spec. Path is the dotted field path
// of the node itself ("" at the root).
func walk(n *yaml.Node, path string, spec yamlSpec, errs *ErrorList) {
	field := path
	if field == "" {
		field = "config"
	}
	switch n.Kind {
	case yaml.AliasNode:
		errf(errs, ExitConfig, field, "line %d: YAML aliases and merge keys are not accepted", n.Line)
		return
	case yaml.MappingNode:
		if spec.mapKeys == nil {
			errf(errs, ExitConfig, field, "line %d: a mapping is not allowed here, want %s", n.Line, spec.scalar)
			return
		}
		seen := make(map[string]int, len(n.Content))
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
				errf(errs, ExitConfig, field, "line %d: mapping keys must be plain strings", k.Line)
				continue
			}
			if prev, dup := seen[k.Value]; dup {
				errf(errs, ExitConfig, joinPath(path, k.Value),
					"line %d: duplicate key %s, first defined at line %d", k.Line, quote(k.Value), prev)
				continue
			}
			seen[k.Value] = k.Line
			child, known := spec.mapKeys[k.Value]
			if !known {
				errf(errs, ExitConfig, joinPath(path, k.Value),
					"line %d: unknown key %s, the schema is closed", k.Line, quote(k.Value))
				continue
			}
			walk(v, joinPath(path, k.Value), child, errs)
		}
	case yaml.ScalarNode, yaml.DocumentNode:
		// An explicit null is treated as an absent value; the role
		// check reports the missing field with its path.
		if n.Tag == "!!null" {
			return
		}
		if spec.mapKeys != nil {
			errf(errs, ExitConfig, field, "line %d: %s value is not allowed here, want a mapping", n.Line, n.Tag)
			return
		}
		if n.Tag != spec.scalar {
			errf(errs, ExitConfig, field, "line %d: value must be %s, got %s", n.Line, spec.scalar, n.Tag)
			return
		}
		// Every integer field of this schema is unsigned.
		if spec.scalar == "!!int" && strings.HasPrefix(n.Value, "-") {
			errf(errs, ExitConfig, field, "line %d: value must be a non-negative integer, got %s", n.Line, n.Value)
		}
	default:
		errf(errs, ExitConfig, field, "line %d: %s is not allowed here", n.Line, nodeKindName(n.Kind))
	}
}

// joinPath extends a dotted field path.
func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// nodeKindName renders a node kind for errors.
func nodeKindName(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "a document node"
	case yaml.SequenceNode:
		return "a sequence"
	case yaml.MappingNode:
		return "a mapping"
	case yaml.ScalarNode:
		return "a scalar"
	case yaml.AliasNode:
		return "an alias"
	}
	return fmt.Sprintf("node kind %d", int(k))
}

// decodeErrors converts decoder failures to located errors. The strict
// walk has already run, so the remaining failures are value bindings
// such as integer overflow.
func decodeErrors(err error) error {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return &Error{Field: "config", Code: ExitConfig, Reason: "YAML decode: " + err.Error()}
	}
	var errs ErrorList
	for _, msg := range te.Errors {
		line, msg := splitYAMLLine(msg)
		errs = append(errs, &Error{Field: "config", Line: line, Code: ExitConfig, Reason: msg})
	}
	return firstError(errs)
}

// splitYAMLLine strips the "line N: " prefix the decoder prepends.
func splitYAMLLine(msg string) (int, string) {
	if !strings.HasPrefix(msg, "line ") {
		return 0, msg
	}
	rest := msg[len("line "):]
	col := strings.IndexByte(rest, ':')
	if col <= 0 {
		return 0, msg
	}
	var line int
	for _, c := range rest[:col] {
		if c < '0' || c > '9' {
			return 0, msg
		}
		line = line*10 + int(c-'0')
	}
	return line, rest[col+2:]
}
