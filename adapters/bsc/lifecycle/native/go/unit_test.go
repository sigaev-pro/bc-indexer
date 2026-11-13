package bsclifecycle

// Unit tests of the pure parts: the evidence digests (expected values
// computed independently from the README formulas), the capture config and
// the profile check.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
)

func TestEvidenceDigests(t *testing.T) {
	a, b := strings.Repeat("11", 32), strings.Repeat("22", 32)
	if got := CanonicalEvidence("bsc.set-head", decision.Head{Number: 5, Hash: a},
		decision.Head{Number: 4, Hash: b}); got != "ea27763178e260b33123a27f8f3fdf8464a87ce28fe142cf925515dc8919ef9e" {
		t.Fatalf("canonical evidence %s", got)
	}
	if got := ClearEvidence(lifecycle.Finalized, "bsc.set-head",
		decision.Head{Number: 7, Hash: a}); got != "816684aafd8d793db30db0a6d10cf4336deaa81de94324f92fd5b0b8fdb533da" {
		t.Fatalf("clear evidence %s", got)
	}
	if got := NotAppliedEvidence(strings.Repeat("ab", 32),
		common.HexToHash(strings.Repeat("33", 32))); got != "ca76d451f47484fd76b864d6cc7fe8804fd40d39bcc68af06858002253d7d3eb" {
		t.Fatalf("not-applied evidence %s", got)
	}
	// The caller is bound: the same heads under another caller differ.
	if CanonicalEvidence("bsc.shutdown", decision.Head{Number: 5, Hash: a}, decision.Head{Number: 4, Hash: b}) ==
		CanonicalEvidence("bsc.set-head", decision.Head{Number: 5, Hash: a}, decision.Head{Number: 4, Hash: b}) {
		t.Fatal("the caller is not bound")
	}
}

func TestLifecycleIntent(t *testing.T) {
	for kind, want := range map[decision.Kind]bool{decision.Canonical: true, decision.Finality: true,
		decision.Import: false, decision.Side: false} {
		if lifecycleIntent(kind) != want {
			t.Fatalf("%s: want %v", kind, want)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigEnv, "")
	if cfg, err := LoadConfig(); cfg != nil || err != nil {
		t.Fatalf("no config: %v %v", cfg, err)
	}
	write := func(body string) string {
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := `{"store_id":"0123456789abcdef0123456789abcdef","producer_id":"bsc-node","run_id":"run-1",` +
		`"native_commit":"54afaff4da31ac9e49ba646a4f65104edac5a2f3","genesis_file":"/genesis.json"}`
	t.Setenv(ConfigEnv, write(good))
	if cfg, err := LoadConfig(); err != nil || cfg.StoreID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("good config: %v %v", cfg, err)
	}
	for name, body := range map[string]string{
		"unknown field": strings.TrimSuffix(good, "}") + `,"extra":1}`,
		"short store":   strings.Replace(good, "0123456789abcdef0123456789abcdef", "0123", 1),
		"short commit":  strings.Replace(good, "54afaff4da31ac9e49ba646a4f65104edac5a2f3", "54afaff4", 1),
		"no genesis":    strings.Replace(good, `"/genesis.json"`, `""`, 1),
		"bad producer":  strings.Replace(good, "bsc-node", "BSC Node", 1),
		"not json":      "{",
	} {
		t.Setenv(ConfigEnv, write(body))
		if _, err := LoadConfig(); !errors.Is(err, ErrConfig) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	t.Setenv(ConfigEnv, filepath.Join(dir, "missing.json"))
	if _, err := LoadConfig(); !errors.Is(err, ErrConfig) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestCheckProfile(t *testing.T) {
	base := func() *ethconfig.Config {
		cfg := ethconfig.Defaults
		cfg.StateScheme, cfg.SyncMode = rawdb.HashScheme, ethconfig.FullSync
		return &cfg
	}
	if err := CheckProfile(base()); err != nil {
		t.Fatal(err)
	}
	on := true
	for name, mutate := range map[string]func(*ethconfig.Config){
		"snap sync":   func(c *ethconfig.Config) { c.SyncMode = ethconfig.SnapSync },
		"vote":        func(c *ethconfig.Config) { c.Miner.VoteEnable = true },
		"builder":     func(c *ethconfig.Config) { c.Miner.Mev.Enabled = &on },
		"path scheme": func(c *ethconfig.Config) { c.StateScheme = rawdb.PathScheme },
		"archive":     func(c *ethconfig.Config) { c.NoPruning = true },
	} {
		cfg := base()
		mutate(cfg)
		if err := CheckProfile(cfg); !errors.Is(err, stateprovider.ErrProfile) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	off := false
	cfg := base()
	cfg.Miner.Mev.Enabled = &off
	if err := CheckProfile(cfg); err != nil {
		t.Fatalf("builder off: %v", err)
	}
}
