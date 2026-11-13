// Package profile is the capture profile check of `bor server` for the
// lifecycle gate: the state provider's effective-profile check
// (state_provider.CheckProfile) and the roles that write blocks without an
// observation, each refusal named by the REFUSE caller it keeps out. See
// adapters/bor/lifecycle/native/README.md.
//
// Extractor patch (E01-02.lifecycle_seams, Bor block).
package profile

import (
	"errors"
	"fmt"
	"strings"

	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/internal/cli/server"
)

// ErrProfile refuses a configuration outside the capture profile.
var ErrProfile = errors.New("bor server configuration is outside the capture profile")

// Refusal is one refused setting and the REFUSE caller it keeps out ("" for
// a profile value that no caller row names, such as the state scheme).
type Refusal struct {
	Setting string `json:"setting"`
	Caller  string `json:"caller"`
}

// Check refuses the effective configuration of a capture node when
// state_provider.CheckProfile refuses it or a block-writing role is on:
// sealing (miner.enabled, the developer mode, bor.devfakeauthor). It returns
// every refusal; a nil error means the profile holds.
func Check(cfg *server.Config) ([]Refusal, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: missing effective config", ErrProfile)
	}
	refusals := []Refusal{}
	want := func(ok bool, setting, caller string) {
		if !ok {
			refusals = append(refusals, Refusal{Setting: setting, Caller: caller})
		}
	}
	want(cfg.StateScheme == "hash", "state.scheme", "")
	want(cfg.GcMode == "full", "gcmode", "")
	want(cfg.SyncMode == "full", "syncmode", "bor.snap-sync")
	want(cfg.ParallelEVM != nil && !cfg.ParallelEVM.Enable && !cfg.ParallelEVM.Enforce, "parallelevm", "bor.parallel")
	want(cfg.Pipeline != nil && !cfg.Pipeline.EnableImportSRC, "pipeline.enable-import-src", "bor.pipelined")
	want(cfg.Witness != nil && !cfg.Witness.EnableParallelStatelessImport && !cfg.Witness.SyncWithWitnesses,
		"witness", "bor.stateless")
	want(cfg.Relay != nil && !cfg.Relay.EnablePreconfs, "relay.enable-preconfs", "bor.preconf")
	want(cfg.JsonRPC != nil && !cfg.JsonRPC.AcceptPreconfTx, "accept-preconf-tx", "bor.preconf")
	want(cfg.Sequencer != nil && !cfg.Sequencer.Enabled, "sequencer.enabled", "bor.preconf")
	want(cfg.Sealer != nil && !cfg.Sealer.Enabled, "mine", "bor.seal-write")
	want(cfg.Developer != nil && !cfg.Developer.Enabled, "dev", "bor.seal-write")
	want(!cfg.DevFakeAuthor, "bor.devfakeauthor", "bor.seal-write")
	_, err := stateprovider.CheckProfile(cfg)
	if len(refusals) == 0 && err == nil {
		return refusals, nil
	}
	settings := make([]string, 0, len(refusals))
	for _, r := range refusals {
		settings = append(settings, r.Setting)
	}
	return refusals, fmt.Errorf("%w: %s (%v)", ErrProfile, strings.Join(settings, ", "), err)
}
