package bsclifecycle

// The error-aware attestation read of the import decision (REQ-LIF-06,
// AC-LIF-06-04 and 06-05). The decision evidence of the BSC decision bridge
// reads the justified block (GetJustifiedNumberAndHash) and the finalized
// block (GetFinalizedHeader) through the native Parlia calls, which turn a
// snapshot without an attestation into the genesis block. Before that, the
// decision step reads the snapshot attestation of the block through
// lifecycle.ReadAttestation over the same evidence reader: a correct
// snapshot without an attestation is GENESIS_FALLBACK with the real genesis
// hash, and a lookup error is a source error that stops the import before
// any write, never genesis or empty evidence.

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	bscdecision "github.com/ethereum/go-ethereum/extractor/bsc/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

// errNoEvidence reports a decision step without an EXEC_READY observation
// of its block.
var errNoEvidence = errors.New("no EXEC_READY observation of the block")

// ReadBlockAttestation is the error-aware attestation read of header through
// reader: the native snapshot lookup of the engine for the attestation, and
// the genesis header of the reader for the fallback.
func ReadBlockAttestation(engine *parlia.Parlia, reader consensus.ChainHeaderReader,
	header *types.Header) (lifecycle.AttestationEvidence, error) {
	snapshot := func() (*lifecycle.Attestation, error) {
		att, err := engine.ExtractorAttestation(reader, header)
		if err != nil {
			return nil, err
		}
		if refusing, ok := reader.(interface{ Err() error }); ok && refusing.Err() != nil {
			return nil, refusing.Err()
		}
		if att == nil {
			return nil, nil
		}
		return &lifecycle.Attestation{SourceNumber: att.SourceNumber, SourceHash: hexHash(att.SourceHash),
			TargetNumber: att.TargetNumber, TargetHash: hexHash(att.TargetHash)}, nil
	}
	genesis := func() (string, error) {
		g := reader.GetHeaderByNumber(0)
		if g == nil {
			return "", fmt.Errorf("genesis header is unavailable")
		}
		return hexHash(g.Hash()), nil
	}
	return lifecycle.ReadAttestation(snapshot, genesis)
}

// attestation reads the attestation evidence of an observed block in its
// decision step, through the evidence reader of the decision bridge (the
// capsule window, the block and the genesis).
func (l *Lifecycle) attestation(chain *core.BlockChain, block *types.Block) (lifecycle.AttestationEvidence, error) {
	engine, ok := chain.Engine().(*parlia.Parlia)
	if !ok {
		return lifecycle.AttestationEvidence{}, bscdecision.ErrEngine
	}
	seam := l.hook.Seam()
	adm, token := seam.Admission()
	key, err := execReadyKey(adm, token, block.Hash())
	if err != nil {
		return lifecycle.AttestationEvidence{}, err
	}
	static, err := seam.Static(key)
	if err != nil {
		return lifecycle.AttestationEvidence{}, err
	}
	var reader consensus.ChainHeaderReader = bscdecision.NewEvidenceReader(chain, static, block.Header(),
		seam.Genesis())
	if l.opts.AttestationReader != nil {
		reader = l.opts.AttestationReader(reader, block.Header())
	}
	evidence, err := ReadBlockAttestation(engine, reader, block.Header())
	if err != nil {
		l.emit(bsc.Event{"event": "attestation-refused", "observation": key, "number": block.NumberU64(),
			"error": err.Error()})
		return lifecycle.AttestationEvidence{}, err
	}
	l.emit(bsc.Event{"event": "attestation", "observation": key, "number": block.NumberU64(),
		"status": string(evidence.Status), "justified": evidence.Justified, "finalized": evidence.Finalized,
		"digest": evidence.Digest})
	return evidence, nil
}

// execReadyKey returns the key of the open EXEC_READY observation of a
// block.
func execReadyKey(adm *admission.Admission, token *admission.Token, hash common.Hash) (string, error) {
	observations, err := adm.Observations(token)
	if err != nil {
		return "", err
	}
	want := hexHash(hash)
	for _, obs := range observations {
		if obs.State == admission.ExecReady && obs.Candidate.Hash == want {
			return obs.Key, nil
		}
	}
	return "", fmt.Errorf("%w: %s", errNoEvidence, want)
}
