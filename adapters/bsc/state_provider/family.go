package stateprovider

// BSC-specific parts of the state provider: the retention floor and the
// state profile, read-only store opening, the guarded native pruner and the
// effective-profile check.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state/pruner"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/node"
	"github.com/naoina/toml"
)

// Family names the native client family of this package.
const Family = "bsc"

// retentionFloor is N of D108: the least number of blocks that a BSC capture
// node keeps behind its head. It is the native immutability threshold
// (params.FullImmutabilityThreshold), which a flag `--history.blocks` below
// it is raised to and which the default `--history.state` uses.
const retentionFloor uint64 = 600_000

// stateProfile is the capture state profile of BSC on a store of the given
// scheme, or empty for a scheme this build does not support. The scheme
// decides how a pin makes a state durable, so capsules and pins of one store
// never mix the two profiles.
func stateProfile(scheme string) string {
	switch scheme {
	case rawdb.HashScheme:
		return "state.scheme=hash;gcmode=full;snapshots=off"
	case rawdb.PathScheme:
		return "state.scheme=path;gcmode=full;history.blocks>=600000;history.state>=600000;tries=local"
	}
	return ""
}

// StateProfile is the capture state profile of BSC on a store of the given
// scheme: the static state-profile item of every capsule of that store. An
// empty string for a scheme this build does not support.
func StateProfile(scheme string) string { return stateProfile(scheme) }

// ProfileDigest is the SHA-256 of the state profile of the scheme, as 64
// lowercase hex digits: the profile field of every pin and the profile of the
// store identity, so that a store bound under another profile is refused.
func ProfileDigest(scheme string) string { return profileOf(scheme) }

// StoreScheme returns the state scheme that a stopped native instance
// directory records, or an empty string for a store without state. It opens
// the store read-only and never writes.
func StoreScheme(instanceDir string) (string, error) {
	db, _, err := openReadOnly(filepath.Join(instanceDir, "chaindata"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	return rawdb.ReadStateScheme(db), nil
}

// legacyOffset is the offset that the prune-block or pruneancient option of
// an older BSC client left in the key-value store: the first block it kept.
func legacyOffset(db ethdb.KeyValueStore) uint64 { return rawdb.ReadLegacyOffset(db) }

// ancientTail is the first block that the ancient store still holds; the
// native freezer moves it when `--history.blocks` prunes block data.
func ancientTail(db ethdb.Database) (uint64, error) { return db.Tail() }

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
	report, err := checkStore(db, nil, expected)
	report.Engine = engine
	return report, err
}

// nativeTriesInMemory is the BSC default of --triesInMemory, which the
// native prune-state command passes to its pruner.
const nativeTriesInMemory = 128

// PruneState runs the native offline state pruner only when no pin is live.
// The caller must pass the store opened exclusively by this process (the
// native node stopped), so no pin can be added between the check and the
// prune.
func PruneState(db ethdb.Database, instanceDir string, bloomSize uint64, root common.Hash) error {
	if err := RequireNoLivePins(db); err != nil {
		return err
	}
	native, err := pruner.NewPruner(db, pruner.Config{Datadir: instanceDir, BloomSize: bloomSize}, nativeTriesInMemory)
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
	return pruner.RecoverPruning(instanceDir, db, nativeTriesInMemory)
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
	// A legacy offset is left by the prune-block or pruneancient option of an
	// older client, which deletes old block data. Native Open would rewrite
	// it into the freezer metadata and delete it, even read-only, so the
	// store is opened without its ancient part and the preflight measures
	// the offset as the oldest block of the store.
	if legacyOffset(kv) > 0 {
		return rawdb.NewDatabase(kv), engine, nil
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
	StateScheme        string `json:"state_scheme"`
	GCMode             string `json:"gcmode"`
	PruneAncientData   bool   `json:"prune_ancient_data"`
	BlockHistory       uint64 `json:"block_history"`
	StateHistory       uint64 `json:"state_history"`
	TriesVerifyMode    string `json:"tries_verify_mode"`
	HistoryMode        string `json:"history_mode"`
	TransactionHistory uint64 `json:"transaction_history"`
	LogHistory         uint64 `json:"log_history"`
}

// nativeDump mirrors the sections that `geth dumpconfig` writes.
type nativeDump struct {
	Eth      ethconfig.Config
	Node     node.Config
	Ethstats struct {
		URL string `toml:",omitempty"`
	}
	Metrics metrics.Config
}

// DecodeEffectiveConfig parses the TOML that the native `dumpconfig` command
// prints after it applies config file, environment and flag precedence. It
// uses the native field naming and rejects unknown fields.
func DecodeEffectiveConfig(data []byte) (*ethconfig.Config, error) {
	settings := toml.Config{
		NormFieldName: func(_ reflect.Type, key string) string { return key },
		FieldToKey:    func(_ reflect.Type, field string) string { return field },
		MissingField: func(rt reflect.Type, field string) error {
			return fmt.Errorf("unknown native config field %s.%s", rt.String(), field)
		},
	}
	var dump nativeDump
	if err := settings.Unmarshal(data, &dump); err != nil {
		return nil, err
	}
	return &dump.Eth, nil
}

// CheckProfile enforces the fast-node capture profile (D108) on the effective
// config, before the store opens:
//
//   - the state scheme is `hash`, `path` or none. None is what the flags of
//     the official pruned snapshot produce; the native parser then keeps the
//     scheme of the store, and CheckStoreProfile applies the rules that
//     depend on it;
//   - full garbage collection;
//   - tries are kept and verified locally (`--tries-verify-mode local`);
//   - block data of the last N blocks or of the entire chain: a nonzero
//     `--history.blocks` below N is refused.
//
// `--pruneancient` is a deprecated option; the node turns it into a block
// history of N, so it is recorded and not refused. The transaction and log
// indexes and the chain history mode do not feed the capture path: they are
// recorded. Other values stay as the operator set them.
func CheckProfile(cfg *ethconfig.Config) (EffectiveProfile, error) {
	if cfg == nil {
		return EffectiveProfile{}, errors.New("missing effective config")
	}
	profile := EffectiveProfile{StateScheme: cfg.StateScheme, GCMode: "full",
		PruneAncientData: cfg.PruneAncientData, BlockHistory: cfg.BlockHistory, StateHistory: cfg.StateHistory,
		TriesVerifyMode: cfg.TriesVerifyMode.String(), HistoryMode: cfg.HistoryMode.String(),
		TransactionHistory: cfg.TransactionHistory, LogHistory: cfg.LogHistory}
	if cfg.NoPruning {
		profile.GCMode = "archive"
	}
	var problems []string
	problems = append(problems, schemeProblems(cfg.StateScheme)...)
	if cfg.NoPruning {
		problems = append(problems, "gcmode archive, want full")
	}
	if cfg.BlockHistory != 0 && cfg.BlockHistory < retentionFloor {
		problems = append(problems, fmt.Sprintf("history.blocks %d keeps fewer than %d blocks", cfg.BlockHistory,
			retentionFloor))
	}
	if cfg.StateScheme == rawdb.PathScheme {
		problems = append(problems, pathProblems(cfg)...)
	}
	if cfg.TriesVerifyMode != core.LocalVerify {
		problems = append(problems, fmt.Sprintf("tries-verify-mode %q keeps no verified trie, want local",
			cfg.TriesVerifyMode))
	}
	if len(problems) != 0 {
		return profile, fmt.Errorf("%w: %s", ErrProfile, strings.Join(problems, "; "))
	}
	return profile, nil
}

// CheckStoreProfile completes CheckProfile with the scheme of the opened
// store. The effective scheme may be none; the native parser then keeps the
// scheme of the store, which the rules that depend on the scheme need: under
// the path scheme, a nonzero `--history.state` below N is refused. A config
// that names another scheme than the store is refused. The returned profile
// names the scheme of the store.
func CheckStoreProfile(cfg *ethconfig.Config, scheme string) (EffectiveProfile, error) {
	profile, err := CheckProfile(cfg)
	if err != nil {
		return profile, err
	}
	var problems []string
	if profileOf(scheme) == "" {
		problems = append(problems, fmt.Sprintf("state scheme %q, want %q or %q", scheme, rawdb.HashScheme,
			rawdb.PathScheme))
	} else {
		if cfg.StateScheme != "" && cfg.StateScheme != scheme {
			problems = append(problems, fmt.Sprintf("state scheme %q is not the %q scheme of the store",
				cfg.StateScheme, scheme))
		}
		profile.StateScheme = scheme
		if scheme == rawdb.PathScheme {
			problems = append(problems, pathProblems(cfg)...)
		}
	}
	if len(problems) != 0 {
		return profile, fmt.Errorf("%w: %s", ErrProfile, strings.Join(problems, "; "))
	}
	return profile, nil
}

// schemeProblems returns the problem of an effective state scheme that this
// profile does not accept: only `hash`, `path` or none.
func schemeProblems(scheme string) []string {
	if scheme == "" || profileOf(scheme) != "" {
		return nil
	}
	return []string{fmt.Sprintf("state scheme %q, want %q, %q or none", scheme, rawdb.HashScheme, rawdb.PathScheme)}
}

// pathProblems returns the problems of the rules that hold under the path
// scheme: the state history keeps N blocks or the entire chain.
func pathProblems(cfg *ethconfig.Config) []string {
	if cfg.StateHistory != 0 && cfg.StateHistory < retentionFloor {
		return []string{fmt.Sprintf("history.state %d keeps fewer than %d blocks", cfg.StateHistory, retentionFloor)}
	}
	return nil
}
