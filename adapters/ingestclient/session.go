package ingestclient

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingest"
	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// Wire constants of the client (CONTROL_WIRE_CONTRACT, "Sizes, headers,
// memory" and the RPC table).
const (
	// MaxDataChunk is the largest payload of one data chunk RPC: 64 KiB,
	// the bound of the producer journal chunk.
	MaxDataChunk = producer.ChunkBytes
	// ProtocolVersion is the pinned control wire version of the session
	// header.
	ProtocolVersion = uint32(proto.ControlVersion)
	// Role is the role the client presents in the handshake: the adapter
	// embedded in the node, not the reference RPC tool.
	Role = "adapter"
)

// session is one authenticated session with the gateway: the token every
// mutating request carries and the generations it is bound to. A session
// lives in one gateway boot only.
type session struct {
	// token is the unpredictable session token; it is never logged.
	token []byte
	// storeEpoch is the gateway store identity the handshake answered.
	storeEpoch proto.ID16
	// retiredPrefix is the gateway's durable retired prefix of the run at
	// the handshake.
	retiredPrefix uint64
	// connectionGeneration and admissionGeneration are the generations the
	// session was opened at.
	connectionGeneration uint64
	admissionGeneration  uint64
}

// header builds the session header of one request
// ({protocol_version=2, request_id, session_token, producer_id, run_id,
// connection_generation, admission_generation, binding_digest}).
func (s *session) header(requestID []byte, id producer.Identity, binding proto.Hash32) *ingest.SessionHeader {
	return &ingest.SessionHeader{
		ProtocolVersion:      ProtocolVersion,
		SessionToken:         s.token,
		RequestId:            requestID,
		ProducerId:           id.ProducerID[:],
		RunId:                id.RunID[:],
		ConnectionGeneration: s.connectionGeneration,
		AdmissionGeneration:  s.admissionGeneration,
		BindingDigest:        binding[:],
	}
}

// handshake performs the two handshake calls (steps 1 and 2 of the call
// order) at one connection generation: HelloChallenge with producer, run,
// role and network digest, then OpenSession in NORMAL mode with the echoed
// challenge, the binding digest as both the active and the desired one, the
// producer's retired prefix and both generations. It answers the session,
// or a *WireError, or a transport or local error.
func (c *client) handshake(ctx context.Context, id producer.Identity, retiredPrefix, generation uint64) (*session, error) {
	networkDigest, err := c.cfg.Network.Digest()
	if err != nil {
		return nil, fmt.Errorf("%w: network: %v", ErrConfig, err)
	}
	hello, err := c.stub.HelloChallenge(ctx, &ingest.HelloChallengeRequest{
		ProducerId:    id.ProducerID[:],
		RunId:         id.RunID[:],
		Role:          Role,
		NetworkDigest: networkDigest[:],
	})
	if err != nil {
		return nil, err
	}
	if e := hello.GetError(); e != nil {
		return nil, wireError("HelloChallenge", e, 0)
	}
	if len(hello.GetChallenge()) != 16 {
		return nil, fmt.Errorf("%w: challenge of %d bytes, want 16", ErrProtocol, len(hello.GetChallenge()))
	}
	binding := c.cfg.Binding.DescriptorDigest
	open, err := c.stub.OpenSession(ctx, &ingest.OpenSessionRequest{
		EchoedChallenge:      hello.GetChallenge(),
		Mode:                 ingest.OpenSessionRequest_NORMAL,
		ActiveBindingDigest:  binding[:],
		DesiredBindingDigest: binding[:],
		RetiredPrefix:        retiredPrefix,
		AdmissionGeneration:  c.admission(),
		ConnectionGeneration: generation,
		ProducerId:           id.ProducerID[:],
		RunId:                id.RunID[:],
	})
	if err != nil {
		return nil, err
	}
	if e := open.GetError(); e != nil {
		return nil, wireError("OpenSession", e, 0)
	}
	if len(open.GetSessionToken()) != 32 {
		return nil, fmt.Errorf("%w: session token of %d bytes, want 32", ErrProtocol, len(open.GetSessionToken()))
	}
	if len(open.GetStoreEpoch()) != 16 {
		return nil, fmt.Errorf("%w: store epoch of %d bytes, want 16", ErrProtocol, len(open.GetStoreEpoch()))
	}
	var storeEpoch proto.ID16
	copy(storeEpoch[:], open.GetStoreEpoch())
	if storeEpoch.IsZero() {
		return nil, fmt.Errorf("%w: store epoch is zero", ErrProtocol)
	}
	if string(open.GetBindingDigest()) != string(binding[:]) {
		return nil, fmt.Errorf("%w: OpenSession answered binding %x, the desired binding is %x",
			ErrConfigBlocked, open.GetBindingDigest(), binding)
	}
	if act := open.GetRecoveryAction(); act != ingest.OpenSessionResponse_NONE {
		// REPLAY, FINISH_RELEASE, FINISH_SWITCH and FINISH_RESUME are the
		// recovery flows of the maintenance session, which this driver
		// does not open.
		return nil, fmt.Errorf("%w: gateway asks for recovery action %s, which the driver does not run",
			ErrBlocked, act)
	}
	if err := checkRetiredPrefix(open.GetRetiredPrefix(), retiredPrefix); err != nil {
		return nil, err
	}
	return &session{
		token:                open.GetSessionToken(),
		storeEpoch:           storeEpoch,
		retiredPrefix:        open.GetRetiredPrefix(),
		connectionGeneration: generation,
		admissionGeneration:  c.admission(),
	}, nil
}

// checkRetiredPrefix applies the rule of DELIVERY_STATE_MACHINE ("Release и
// потеря Gateway store") to the two retired prefixes: the gateway may be one
// release ahead of the producer (a lost ReleaseAck), and nothing else.
//
// DECISION (contract reference: DELIVERY_STATE_MACHINE, "Release и потеря
// Gateway store"): a gateway that is behind the producer lost its store
// after retiring batches, and a gateway more than one release ahead disagrees
// with the producer. Neither is recovered in the tracer bullet: the baseline
// adoption and RecoveryRelease that the contract names for it are not wired
// (E03-05). The driver stops with BLOCKED before it touches a batch, which
// names the cause instead of retrying a Release the gateway will refuse.
func checkRetiredPrefix(gatewayPrefix, producerPrefix uint64) error {
	switch {
	case gatewayPrefix == producerPrefix:
		return nil
	case gatewayPrefix == producerPrefix+1:
		return nil
	default:
		return fmt.Errorf("%w: gateway retired prefix %d against producer retired prefix %d, want equal or one ahead",
			ErrBlocked, gatewayPrefix, producerPrefix)
	}
}

// connectionGenerationKey is the one key the driver keeps in the storage of
// the producer journal, under its own prefix.
const connectionGenerationKey = "driver/meta/connection_generation"

// saveConnectionGeneration increments the connection generation and saves
// it before the session that uses it is opened. CONTROL_WIRE_CONTRACT
// ("Базовые кодирования и идентичности"): the connection generation counts
// RPC sessions, starts at 1 and never reuses a value.
func saveConnectionGeneration(st storage.Storage) (uint64, error) {
	var current uint64
	if raw, err := st.Get([]byte(connectionGenerationKey)); err == nil {
		if len(raw) != 8 {
			return 0, fmt.Errorf("%w: %s holds %d bytes, want 8", ErrJournalData, connectionGenerationKey, len(raw))
		}
		current = binary.BigEndian.Uint64(raw)
	} else if err != storage.ErrNotFound {
		return 0, err
	}
	if current == ^uint64(0) {
		return 0, fmt.Errorf("%w: connection generation overflow", producer.ErrCounterOverflow)
	}
	current++
	if err := st.Put([]byte(connectionGenerationKey), binary.BigEndian.AppendUint64(nil, current)); err != nil {
		return 0, err
	}
	return current, nil
}

// readConnectionGeneration returns the saved connection generation, 0 when
// none was saved yet.
func readConnectionGeneration(st storage.Storage) (uint64, error) {
	raw, err := st.Get([]byte(connectionGenerationKey))
	if err == storage.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(raw) != 8 {
		return 0, fmt.Errorf("%w: %s holds %d bytes, want 8", ErrJournalData, connectionGenerationKey, len(raw))
	}
	return binary.BigEndian.Uint64(raw), nil
}
