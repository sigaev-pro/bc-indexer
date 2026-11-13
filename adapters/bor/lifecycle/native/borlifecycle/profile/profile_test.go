package profile

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/internal/cli/server"
)

func capture() *server.Config {
	cfg := server.DefaultConfig()
	cfg.StateScheme, cfg.GcMode, cfg.SyncMode = "hash", "full", "full"
	cfg.ParallelEVM.Enable, cfg.ParallelEVM.Enforce = false, false
	return cfg
}

func TestCheck(t *testing.T) {
	if refusals, err := Check(capture()); err != nil || len(refusals) != 0 {
		t.Fatalf("capture profile refused: %v %v", refusals, err)
	}
	cases := map[string]func(*server.Config){
		"bor.snap-sync":  func(c *server.Config) { c.SyncMode = "snap" },
		"bor.parallel":   func(c *server.Config) { c.ParallelEVM.Enforce = true },
		"bor.pipelined":  func(c *server.Config) { c.Pipeline.EnableImportSRC = true },
		"bor.stateless":  func(c *server.Config) { c.Witness.SyncWithWitnesses = true },
		"bor.preconf":    func(c *server.Config) { c.Sequencer.Enabled = true },
		"bor.seal-write": func(c *server.Config) { c.Sealer.Enabled = true },
		"":               func(c *server.Config) { c.GcMode = "archive" },
	}
	for caller, change := range cases {
		cfg := capture()
		change(cfg)
		refusals, err := Check(cfg)
		if !errors.Is(err, ErrProfile) || len(refusals) != 1 || refusals[0].Caller != caller {
			t.Fatalf("%q: %v %v", caller, refusals, err)
		}
	}
	if _, err := Check(nil); !errors.Is(err, ErrProfile) {
		t.Fatal(err)
	}
}
