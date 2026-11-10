package stateprovider

// Geth-specific parts of the state provider: read-only store opening, the
// guarded native pruner and the effective-profile check.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/ethereum/go-ethereum/common"
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
const Family = "geth"

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
// The caller must pass the store opened exclusively by this process (the
// native node stopped), so no pin can be added between the check and the
// prune.
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

// CheckProfile enforces the capture constraints on the effective config: the
// hash state scheme and full garbage collection. Other values stay as the
// operator set them.
func CheckProfile(cfg *ethconfig.Config) (EffectiveProfile, error) {
	if cfg == nil {
		return EffectiveProfile{}, errors.New("missing effective config")
	}
	profile := EffectiveProfile{StateScheme: cfg.StateScheme, GCMode: "full"}
	if cfg.NoPruning {
		profile.GCMode = "archive"
	}
	var problems []string
	if cfg.StateScheme != rawdb.HashScheme {
		problems = append(problems, fmt.Sprintf("state scheme %q, want %q", cfg.StateScheme, rawdb.HashScheme))
	}
	if cfg.NoPruning {
		problems = append(problems, "gcmode archive, want full")
	}
	if len(problems) != 0 {
		return profile, fmt.Errorf("%w: %s", ErrProfile, strings.Join(problems, "; "))
	}
	return profile, nil
}
