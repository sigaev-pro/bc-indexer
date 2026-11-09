package stateprovider

// Bor-specific parts of the state provider: read-only store opening, the
// guarded native pruner and the effective-profile check of `bor server`.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state/pruner"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/internal/cli/server"
)

// Family names the native client family of this package.
const Family = "bor"

// Preflight checks a stopped native instance directory (the directory that
// holds `chaindata`) before capture starts. It opens the store read-only and
// never writes, so a rejected home is neither wiped nor reinitialized.
func Preflight(instanceDir string, expected StoreIdentity) (StoreReport, error) {
	if err := checkLayout(instanceDir); err != nil {
		return StoreReport{}, err
	}
	db, engine, err := openReadOnly(filepath.Join(instanceDir, "chaindata"))
	if err != nil {
		return StoreReport{}, err
	}
	defer db.Close()
	report, err := checkStore(db, expected)
	report.Engine = engine
	return report, err
}

// PruneState runs the native offline state pruner only when no pin is live.
// The check and the prune share one exclusively opened store, so a running
// node cannot add a pin in between.
func PruneState(db ethdb.Database, instanceDir string, bloomSize uint64, root common.Hash) error {
	if err := RequireNoLivePins(db); err != nil {
		return err
	}
	native, err := pruner.NewPruner(db, pruner.Config{Datadir: instanceDir, BloomSize: bloomSize})
	if err != nil {
		return err
	}
	return native.Prune(root)
}

// RecoverPruning resumes an interrupted native prune only when no pin is
// live; otherwise the node must not start.
func RecoverPruning(db ethdb.Database, instanceDir string) error {
	if err := RequireNoLivePins(db); err != nil {
		return err
	}
	return pruner.RecoverPruning(instanceDir, db)
}

// checkLayout rejects state left by an interrupted offline prune. Native
// startup would resume that prune and delete everything outside its target.
func checkLayout(instanceDir string) error {
	info, err := os.Stat(filepath.Join(instanceDir, "chaindata"))
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: no native chaindata in %s", ErrWrongStore, instanceDir)
	}
	entries, err := os.ReadDir(instanceDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "statebloom.") {
			return fmt.Errorf("%w: interrupted state prune %s", ErrForbiddenPruning, entry.Name())
		}
	}
	return nil
}

func openReadOnly(chaindata string) (ethdb.Database, string, error) {
	var (
		kv  ethdb.KeyValueStore
		err error
	)
	engine := rawdb.PreexistingDatabase(chaindata)
	switch engine {
	case rawdb.DBPebble:
		kv, err = pebble.New(chaindata, 16, 16, "", true)
	case rawdb.DBLeveldb:
		kv, err = leveldb.New(chaindata, 16, 16, "", true)
	default:
		return nil, "", fmt.Errorf("%w: no native database in %s", ErrWrongStore, chaindata)
	}
	if err != nil {
		return nil, "", err
	}
	ancient := filepath.Join(chaindata, "ancient")
	if info, statErr := os.Stat(ancient); statErr != nil || !info.IsDir() {
		return rawdb.NewDatabase(kv), engine, nil
	}
	db, err := rawdb.Open(kv, rawdb.OpenOptions{Ancient: ancient, ReadOnly: true})
	if err != nil {
		kv.Close()
		return nil, "", err
	}
	return db, engine, nil
}

// EffectiveProfile reports the capture-relevant effective native values.
type EffectiveProfile struct {
	StateScheme string `json:"state_scheme"`
	GCMode      string `json:"gcmode"`
	SyncMode    string `json:"syncmode"`
}

// EffectiveConfig returns the configuration that `bor server` builds from
// args with its native precedence. It needs the native overlay file.
func EffectiveConfig(args []string) (*server.Config, error) {
	return server.ExtractorEffectiveConfig(args)
}

// CheckProfile enforces the first Bor capture profile on the effective
// config: hash state scheme, full garbage collection, full sync, and the
// ordinary sequential import path. Pipelined state-root import, parallel
// stateless import, witness sync, preconfirmations, the sequencer stream and
// the parallel EVM (BlockSTM, D60) are rejected. Other values stay as the
// operator set them.
func CheckProfile(cfg *server.Config) (EffectiveProfile, error) {
	if cfg == nil {
		return EffectiveProfile{}, errors.New("missing effective config")
	}
	profile := EffectiveProfile{StateScheme: cfg.StateScheme, GCMode: cfg.GcMode, SyncMode: cfg.SyncMode}
	var problems []string
	want := func(ok bool, problem string) {
		if !ok {
			problems = append(problems, problem)
		}
	}
	want(cfg.StateScheme == rawdb.HashScheme, fmt.Sprintf("state.scheme %q, want %q", cfg.StateScheme, rawdb.HashScheme))
	want(cfg.GcMode == "full", fmt.Sprintf("gcmode %q, want full", cfg.GcMode))
	want(cfg.SyncMode == "full", fmt.Sprintf("syncmode %q, want full", cfg.SyncMode))
	want(cfg.ParallelEVM != nil && !cfg.ParallelEVM.Enable, "parallelevm.enable")
	want(cfg.ParallelEVM != nil && !cfg.ParallelEVM.Enforce, "parallelevm.enforce")
	want(cfg.Pipeline != nil && !cfg.Pipeline.EnableImportSRC, "pipeline.enable-import-src")
	want(cfg.Witness != nil && !cfg.Witness.EnableParallelStatelessImport, "witness.parallelstatelessimport")
	want(cfg.Witness != nil && !cfg.Witness.SyncWithWitnesses, "witness.syncwithwitnesses")
	want(cfg.Relay != nil && !cfg.Relay.EnablePreconfs, "relay.enable-preconfs")
	want(cfg.JsonRPC != nil && !cfg.JsonRPC.AcceptPreconfTx, "accept-preconf-tx")
	want(cfg.Sequencer != nil && !cfg.Sequencer.Enabled, "sequencer.enabled")
	if len(problems) != 0 {
		return profile, fmt.Errorf("%w: %s", ErrProfile, strings.Join(problems, "; "))
	}
	return profile, nil
}
