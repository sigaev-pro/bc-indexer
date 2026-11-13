package bsclifecycle

// The node wiring: the capture config, the profile check, the start of the
// admission, decision and lifecycle seams, and the manual database guard.

import (
	"fmt"
	"os"
	"regexp"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	bscdecision "github.com/ethereum/go-ethereum/extractor/bsc/decision"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
)

// ConfigEnv names the capture config file of a BSC node. When it is set,
// eth.New checks the capture profile, builds Parlia with the system-call
// API and attaches the lifecycle.
const ConfigEnv = "EXTRACTOR_BSC_CONFIG"

var (
	storeIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	partPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// FileConfig is the capture config file of a node.
type FileConfig struct {
	StoreID      string `json:"store_id"`
	ProducerID   string `json:"producer_id"`
	RunID        string `json:"run_id"`
	NativeCommit string `json:"native_commit"`
	GenesisFile  string `json:"genesis_file"`
}

// LoadConfig reads the config file that ConfigEnv names; nil without one.
func LoadConfig() (*FileConfig, error) {
	path := os.Getenv(ConfigEnv)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConfig, err)
	}
	var cfg FileConfig
	if err := strictDecode(data, &cfg); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrConfig, path, err)
	}
	if !storeIDPattern.MatchString(cfg.StoreID) || !partPattern.MatchString(cfg.ProducerID) ||
		!partPattern.MatchString(cfg.RunID) || !commitPattern.MatchString(cfg.NativeCommit) || cfg.GenesisFile == "" {
		return nil, fmt.Errorf("%w: %s: store_id, producer_id, run_id, native_commit and genesis_file", ErrConfig, path)
	}
	return &cfg, nil
}

// CheckProfile is the capture profile check of eth.New, before the store is
// opened: the state profile of E01-02.pins (hash scheme, full garbage
// collection, no block-data pruning), full sync only (bsc.snap-sync), and no
// voting or builder role (bsc.sealing). Mining is refused when it starts
// (Ethereum.StartMining).
func CheckProfile(cfg *ethconfig.Config) error {
	if _, err := stateprovider.CheckProfile(cfg); err != nil {
		return err
	}
	if cfg.SyncMode != downloader.FullSync {
		return fmt.Errorf("%w: sync mode %s (bsc.snap-sync)", stateprovider.ErrProfile, cfg.SyncMode)
	}
	if cfg.Miner.VoteEnable {
		return fmt.Errorf("%w: vote enabled (bsc.sealing)", stateprovider.ErrProfile)
	}
	if cfg.Miner.Mev.Enabled != nil && *cfg.Miner.Mev.Enabled {
		return fmt.Errorf("%w: MEV builder role (bsc.sealing)", stateprovider.ErrProfile)
	}
	return nil
}

// noMirror is the mirror of a node without E05-01.registry: no decision is
// applied, so every decision stays a delivery obligation (D101).
type noMirror struct{}

func (noMirror) Applied(uint64, string) (bool, error) { return false, nil }

// Config configures Attach.
type Config struct {
	// Producer is the producer run of the store.
	Producer admission.Producer
	// StoreID is the native store ID of E01-02.pins.
	StoreID string
	// NativeCommit is the BSC source commit of the build.
	NativeCommit string
	// GenesisFile is the genesis file of the home.
	GenesisFile []byte
	// Store, when set, holds the extractor records instead of the native
	// database (probes pass a power-loss model over it).
	Store sourceinputs.Store
	// Mirror is the decision mirror (E05-01.registry); nil is a mirror that
	// applies nothing.
	Mirror decision.Mirror
	// Emit receives the trace events; it must not call the lifecycle.
	Emit func(bsc.Event)
}

// NodeConfig returns the Attach config of a config file.
func NodeConfig(file FileConfig) (Config, error) {
	genesis, err := os.ReadFile(file.GenesisFile)
	if err != nil {
		return Config{}, fmt.Errorf("%w: %v", ErrConfig, err)
	}
	return Config{Producer: admission.Producer{ProducerID: file.ProducerID, RunID: file.RunID},
		StoreID: file.StoreID, NativeCommit: file.NativeCommit, GenesisFile: genesis}, nil
}

// Attach starts the admission seam with the input cleanup of D100 and the
// decision bridge, binds the system-call API of Parlia to the chain and the
// seam, installs the lifecycle as the import hook and the lifecycle seam of
// the chain, opens the gate (client bsc) and runs the recovery driver
// inside the gate's recovery passes. A restart goes live at the stored H; a
// first start stays in BOOTSTRAP until GoLive.
func Attach(chain *core.BlockChain, db ethdb.Database, calls *bsc.SystemCallBackend, cfg Config,
	opts Options) (*Lifecycle, error) {
	if chain.TrieDB().Scheme() != rawdb.HashScheme {
		return nil, fmt.Errorf("%w: the lifecycle needs the hash state scheme", ErrConfig)
	}
	if calls == nil {
		return nil, fmt.Errorf("%w: missing system-call API", ErrConfig)
	}
	emit := cfg.Emit
	if emit == nil {
		emit = func(bsc.Event) {}
	}
	mirror := cfg.Mirror
	if mirror == nil {
		mirror = noMirror{}
	}
	var store admission.DeleteStore = db
	if cfg.Store != nil {
		deleter, ok := cfg.Store.(admission.DeleteStore)
		if !ok {
			return nil, fmt.Errorf("%w: the store cannot delete records", ErrConfig)
		}
		store = deleter
	}
	identity := stateprovider.StoreIdentity{StoreID: cfg.StoreID, ChainID: chain.Config().ChainID.String(),
		GenesisHash: chain.Genesis().Hash(), StateScheme: rawdb.HashScheme}
	calls.Bind(chain, nil)
	hook, err := bscdecision.Start(admission.NewGuard(), db, chain, bscdecision.Config{Seam: bsc.Config{
		Producer: cfg.Producer, Identity: identity, GenesisFile: cfg.GenesisFile, NativeCommit: cfg.NativeCommit,
		Store: store, Emit: emit}, Mirror: mirror})
	if err != nil {
		return nil, err
	}
	calls.Bind(chain, hook.Seam())
	l := &Lifecycle{hook: hook, chain: chain, db: db, store: store, storeID: cfg.StoreID, mirror: mirror,
		roots: bscdecision.NewRoots(db), opts: opts, emit: emit}
	release := hook.Acquire()
	adm, token := hook.Seam().Admission()
	l.gate, err = lifecycle.Open(token, store, adm, cfg.StoreID, "bsc", false)
	release()
	if err != nil {
		return nil, err
	}
	chain.SetExtractorHook(l)
	chain.SetExtractorLifecycle(l)
	attached.Store(chain, l)
	emit(bsc.Event{"event": "gate-opened", "phase": string(l.gate.Phase())})
	if opts.SkipRecovery {
		return l, nil
	}
	if err := l.recover(); err != nil {
		return nil, err
	}
	return l, nil
}

// RequireNoOpenCapture is the manual database guard of the shipped geth on
// a BSC capture store (bsc.database-commands): a command that deletes or
// rewrites chaindata must not run while an extractor pin is live. Every
// ADMITTED or EXEC_READY observation keeps the pin of its parent until its
// decision closes it, so a live pin also covers every open obligation of the
// import path. It is the ledger check of the other retention guards.
func RequireNoOpenCapture(db ethdb.Database) error {
	return stateprovider.RequireNoLivePins(db)
}
