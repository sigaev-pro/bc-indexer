// Package finalityobserver is the E05-01.polygon_observer owner block of
// the Bor adapter: the native Polygon milestone/checkpoint statuses and the
// separately verified L1-finalized checkpoint evidence for GC
// (CURRENT_SEMANTICS "Polygon evidence and L1 GC" D63), with the D80
// sources_binding_digest bound to every observation and immutable on replay
// and config replacement.
//
// The package performs no network I/O of its own: the supporting Heimdall
// and the own Polygon L1 node are injected seams, network reads happen
// outside hooks and native locks, and every deterministic function is a
// pure function of its inputs. The durable observation stream is the
// injected Store; tests use the memory store of this package.
package finalityobserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
)

// Role names of the two supporting sources in the sources_file schema.
const (
	RoleL1Execution = "POLYGON_L1_EXECUTION"
	RoleHeimdall    = "POLYGON_HEIMDALL"
)

// ErrSources rejects a sources_file that breaks the strict schema: wrong
// version, unknown or duplicate fields, comments or substitutions, or a
// missing identity. There is no partial acceptance.
var ErrSources = errors.New("finality_observer: invalid sources file")

// L1SourceBinding is the l1 half of the sources_file: the deployment ID of
// the own Polygon L1 Geth, its role, its endpoint and the verified network
// identity. The endpoint must equal the configured l1_endpoint elsewhere;
// this package pins what was verified, it does not choose endpoints.
type L1SourceBinding struct {
	DeploymentID block.Id16            `json:"deployment_id"`
	Role         string                `json:"role"`
	Endpoint     string                `json:"endpoint"`
	Network      block.NetworkIdentity `json:"network"`
}

// HeimdallSourceBinding is the heimdall half of the sources_file: the
// deployment ID of the own Heimdall, its role, its endpoint and the
// verified Heimdall identity. The chain id is the Heimdall chain id string,
// the genesis digest is of the verified bootstrap genesis JSON document.
type HeimdallSourceBinding struct {
	DeploymentID          block.Id16   `json:"deployment_id"`
	Role                  string       `json:"role"`
	Endpoint              string       `json:"endpoint"`
	ChainID               string       `json:"chain_id"`
	GenesisDocumentSHA256 block.Hash32 `json:"genesis_document_sha256"`
}

// Sources is the parsed, strictly validated finality.polygon.sources_file
// with its D80 sources_binding_digest: the SHA-256 of the exact verified
// file bytes. Whitespace changes the digest; the digest is not a deployment
// ID and not a remote attestation.
type Sources struct {
	SchemaVersion uint16
	L1            L1SourceBinding
	Heimdall      HeimdallSourceBinding
	Digest        block.Hash32
}

// SourcesFileJSON is the wire shape of the file. Every field is required;
// decoding runs with unknown fields and duplicate keys refused.
type SourcesFileJSON struct {
	SchemaVersion *uint16                `json:"schema_version"`
	L1            *L1SourceBinding       `json:"l1"`
	Heimdall      *HeimdallSourceBinding `json:"heimdall"`
}

// ParseSources parses and validates the exact sources_file bytes. The file
// is strict UTF-8 JSON without unknown or duplicate fields, comments or
// substitutions; schema_version is the integer 1; the roles are the two
// closed role names; identities are non-zero. The returned Sources keeps
// the digest of the exact bytes it validated.
func ParseSources(fileBytes []byte) (*Sources, error) {
	if len(fileBytes) == 0 {
		return nil, fmt.Errorf("%w: empty file", ErrSources)
	}
	if !utf8.Valid(fileBytes) {
		return nil, fmt.Errorf("%w: not valid UTF-8", ErrSources)
	}
	if err := rejectDuplicateKeys(fileBytes); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(fileBytes))
	dec.DisallowUnknownFields()
	var raw SourcesFileJSON
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSources, err)
	}
	if err := trailingData(dec); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSources, err)
	}
	if raw.SchemaVersion == nil || raw.L1 == nil || raw.Heimdall == nil {
		return nil, fmt.Errorf("%w: schema_version, l1 and heimdall are all required", ErrSources)
	}
	if *raw.SchemaVersion != 1 {
		return nil, fmt.Errorf("%w: schema_version %d, want 1", ErrSources, *raw.SchemaVersion)
	}
	if raw.L1.Role != RoleL1Execution {
		return nil, fmt.Errorf("%w: l1 role %q, want %q", ErrSources, raw.L1.Role, RoleL1Execution)
	}
	if raw.Heimdall.Role != RoleHeimdall {
		return nil, fmt.Errorf("%w: heimdall role %q, want %q", ErrSources, raw.Heimdall.Role, RoleHeimdall)
	}
	if raw.L1.DeploymentID.IsZero() {
		return nil, fmt.Errorf("%w: l1 deployment id is zero", ErrSources)
	}
	if raw.Heimdall.DeploymentID.IsZero() {
		return nil, fmt.Errorf("%w: heimdall deployment id is zero", ErrSources)
	}
	if raw.L1.Endpoint == "" {
		return nil, fmt.Errorf("%w: l1 endpoint is empty", ErrSources)
	}
	if raw.Heimdall.Endpoint == "" {
		return nil, fmt.Errorf("%w: heimdall endpoint is empty", ErrSources)
	}
	if err := raw.L1.Network.Validate(); err != nil {
		return nil, fmt.Errorf("%w: l1 network: %v", ErrSources, err)
	}
	if raw.Heimdall.ChainID == "" {
		return nil, fmt.Errorf("%w: heimdall chain id is empty", ErrSources)
	}
	if raw.Heimdall.GenesisDocumentSHA256.IsZero() {
		return nil, fmt.Errorf("%w: heimdall genesis document digest is zero", ErrSources)
	}
	digest := block.Hash32(profiles.ComputeSourcesBindingDigest(fileBytes))
	return &Sources{
		SchemaVersion: *raw.SchemaVersion,
		L1:            *raw.L1,
		Heimdall: HeimdallSourceBinding{
			DeploymentID:          raw.Heimdall.DeploymentID,
			Role:                  raw.Heimdall.Role,
			Endpoint:              raw.Heimdall.Endpoint,
			ChainID:               raw.Heimdall.ChainID,
			GenesisDocumentSHA256: raw.Heimdall.GenesisDocumentSHA256,
		},
		Digest: digest,
	}, nil
}

// HeimdallIdentity is the verified identity of the own Heimdall: the chain
// id string and the digest of the verified bootstrap genesis JSON document.
// The digest refers to that document; it never substitutes a block hash.
type HeimdallIdentity struct {
	ChainID               string       `json:"chain_id"`
	GenesisDocumentSHA256 block.Hash32 `json:"genesis_document_sha256"`
}

// Identity of the Heimdall binding as observed by the source.
func (b HeimdallSourceBinding) Identity() HeimdallIdentity {
	return HeimdallIdentity{
		ChainID:               b.ChainID,
		GenesisDocumentSHA256: b.GenesisDocumentSHA256,
	}
}

// trailingData rejects anything after the first JSON value.
func trailingData(dec *json.Decoder) error {
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing data after the JSON value")
	}
	return nil
}

// rejectDuplicateKeys walks the JSON token stream and refuses any object
// with a repeated key. encoding/json silently keeps the last value; the
// strict file must not depend on that behavior.
func rejectDuplicateKeys(fileBytes []byte) error {
	dec := json.NewDecoder(bytes.NewReader(fileBytes))
	dec.UseNumber()
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyTok.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, dup := seen[key]; dup {
					return fmt.Errorf("duplicate field %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := dec.Token() // closing brace
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := dec.Token() // closing bracket
			return err
		default:
			return nil
		}
	}
	if err := walk(); err != nil {
		return fmt.Errorf("%w: %v", ErrSources, err)
	}
	return nil
}
