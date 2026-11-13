// Package wire installs the capture wiring of the lifecycle gate into the
// shipped `bor` binary: the --extractor.capture flag of `bor server` (the
// profile check after buildEth, the capture start after eth.New and before
// node.Start) and the refusal of manual chaindata commands on a capture
// store. The shipped main imports it for its side effect. See
// adapters/bor/lifecycle/native/README.md.
//
// Extractor patch (E01-02.lifecycle_seams, Bor block).
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/extractor/bordecision"
	"github.com/ethereum/go-ethereum/extractor/borimport"
	"github.com/ethereum/go-ethereum/extractor/borlifecycle"
	"github.com/ethereum/go-ethereum/extractor/borlifecycle/profile"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/internal/cli"
	"github.com/ethereum/go-ethereum/internal/cli/server"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
)

// ErrManualCommand refuses a manual command on a capture store with live pins
// or decision obligations.
var ErrManualCommand = errors.New("manual chaindata command refused on a capture store")

// decisionPrefix starts every record of the decision bridge (decision.go).
const decisionPrefix = "extractor-decision-v1/"

func init() {
	server.ExtractorCaptureProfile = Profile
	server.ExtractorCaptureStart = serverStart
	server.ExtractorPlainStore = PlainStore
	cli.ExtractorManualCommand = ManualCommand
}

// Capture is the capture configuration file named by --extractor.capture.
type Capture struct {
	StoreID       string `json:"store_id"`
	Producer      string `json:"producer"`
	Run           string `json:"run"`
	Genesis       string `json:"genesis"`
	ChainConfig   string `json:"chain_config"`
	Commit        string `json:"commit"`
	Normalization string `json:"normalization"`
	HeimdallHost  string `json:"heimdall_host"`
}

// ReadCapture reads and strictly decodes a capture configuration file.
func ReadCapture(path string) (Capture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Capture{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var c Capture
	if err := decoder.Decode(&c); err != nil {
		return Capture{}, fmt.Errorf("capture configuration %s: %w", path, err)
	}
	return c, nil
}

// Config returns the admission configuration of a capture node whose native
// instance directory is instanceDir.
func (c Capture) Config(instanceDir string) (borimport.Config, error) {
	genesis, err := os.ReadFile(c.Genesis)
	if err != nil {
		return borimport.Config{}, err
	}
	chainConfig, err := os.ReadFile(c.ChainConfig)
	if err != nil {
		return borimport.Config{}, err
	}
	return borimport.Config{InstanceDir: instanceDir, StoreID: c.StoreID, Producer: c.Producer, Run: c.Run,
		Genesis: genesis, ChainConfig: chainConfig, Commit: c.Commit, Normalization: c.Normalization,
		HeimdallHost: c.HeimdallHost}, nil
}

// Profile is the check of `bor server --extractor.capture` after buildEth and
// before eth.New: the capture profile, then the store preflight, which
// refuses a stopped store of another identity before the node opens it.
func Profile(config *server.Config) error {
	if _, err := profile.Check(config); err != nil {
		return err
	}
	capture, err := ReadCapture(config.ExtractorCapture)
	if err != nil {
		return err
	}
	cfg, err := capture.Config(filepath.Join(config.DataDir, "bor"))
	if err != nil {
		return err
	}
	var genesis core.Genesis
	if err := json.Unmarshal(cfg.Genesis, &genesis); err != nil {
		return err
	}
	if genesis.Config == nil || genesis.Config.ChainID == nil {
		return errors.New("capture genesis without a chain ID")
	}
	return borimport.Preflight(cfg, genesis.Config.ChainID.String(), genesis.ToBlock().Hash())
}

// Run is one started capture node.
type Run struct {
	Import  *borimport.Node
	Decided *bordecision.Node
	// Lifecycle is nil while the node waits in BOOTSTRAP.
	Lifecycle *borlifecycle.Node
	chain     *core.BlockChain
	stop      chan struct{}
}

// Start starts the capture of a native node after eth.New and before
// node.Start: the admission (borimport.Start), then the decision bridge with
// the lifecycle gate enabled (bordecision.Start). A store with a live
// boundary H recovers and goes live at once. A new capture store stays in
// BOOTSTRAP (D105): the initial native sync runs natively and uncaptured
// until GoLive.
func Start(stack *node.Node, backend *eth.Ethereum, capture Capture) (*Run, error) {
	cfg, err := capture.Config(stack.ResolvePath(""))
	if err != nil {
		return nil, err
	}
	chain := backend.BlockChain()
	imp, err := borimport.Start(cfg, backend.ChainDb(), chain)
	if err != nil {
		return nil, err
	}
	borlifecycle.Enable()
	decided, _, err := bordecision.Start(imp, nil)
	if err != nil {
		borlifecycle.Of(chain).Close()
		imp.Detach()
		return nil, err
	}
	run := &Run{Import: imp, Decided: decided, chain: chain, stop: make(chan struct{})}
	if decided.Bootstrapping() {
		return run, nil
	}
	if run.Lifecycle = borlifecycle.Of(chain); run.Lifecycle == nil {
		decided.Detach()
		return nil, errors.New("capture start: the lifecycle gate did not open")
	}
	decided.Attach(imp)
	return run, nil
}

// Bootstrapping reports whether the node waits in BOOTSTRAP.
func (r *Run) Bootstrapping() bool { return r.Lifecycle == nil }

// GoLive ends BOOTSTRAP: the recovery driver, H (the native head read under
// chainmu and the guard, its state root flushed), GoLive, and the decider and
// observer installed in the same lock.
func (r *Run) GoLive() error {
	if _, err := r.Decided.ExtractorGoLive(); err != nil {
		return err
	}
	if r.Lifecycle = borlifecycle.Of(r.chain); r.Lifecycle == nil {
		return errors.New("capture GoLive: the lifecycle gate did not open")
	}
	return nil
}

// watch calls GoLive once the native initial sync is done (eth.Synced).
func (r *Run) watch(backend *eth.Ethereum) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			if !backend.Synced() {
				continue
			}
			if err := r.GoLive(); err != nil {
				log.Crit("Extractor capture could not go live after the initial sync", "err", err)
			}
			log.Info("Extractor capture is live", "boundary", r.Lifecycle.Report().Boundary)
			return
		}
	}
}

// Stop detaches the capture of a stopped node.
func (r *Run) Stop() {
	close(r.stop)
	r.Decided.Detach()
	r.Lifecycle.Close()
}

// ErrPlainStore refuses `bor server` without --extractor.capture on a capture
// store.
var ErrPlainStore = errors.New("bor server without --extractor.capture refused on a capture store")

// PlainStore refuses `bor server` without --extractor.capture on a store with
// a live pin or a decision record: native imports, rewinds and whitelist
// writes would run there without the gate.
func PlainStore(config *server.Config) error {
	if err := ManualCommand("bor server", filepath.Join(config.DataDir, "bor", "chaindata"), nil); err != nil {
		return fmt.Errorf("%w: %w", ErrPlainStore, err)
	}
	return nil
}

func serverStart(config *server.Config, stack *node.Node, backend *eth.Ethereum) error {
	capture, err := ReadCapture(config.ExtractorCapture)
	if err != nil {
		return err
	}
	run, err := Start(stack, backend, capture)
	if err != nil {
		return err
	}
	if run.Bootstrapping() {
		go run.watch(backend)
	}
	return nil
}

// ManualCommand refuses a manual command on a capture store with a live pin
// (state_provider.RequireNoLivePins) or any decision record: with no mirror
// (until E05-01.registry) every decision record keeps an obligation or a
// retained member. A directory without native chaindata passes.
func ManualCommand(command, chaindata string, db ethdb.Database) error {
	if db == nil {
		opened, err := openReadOnly(chaindata)
		if err != nil || opened == nil {
			return err
		}
		defer opened.Close()
		db = opened
	}
	if err := stateprovider.RequireNoLivePins(db); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrManualCommand, command, err)
	}
	it := db.NewIterator([]byte(decisionPrefix), nil)
	defer it.Release()
	if it.Next() {
		return fmt.Errorf("%w: %s: decision record %q", ErrManualCommand, command, it.Key())
	}
	return it.Error()
}

func openReadOnly(chaindata string) (ethdb.Database, error) {
	var (
		kv  ethdb.KeyValueStore
		err error
	)
	switch rawdb.PreexistingDatabase(chaindata) {
	case rawdb.DBPebble:
		kv, err = pebble.New(chaindata, 16, 16, "", true)
	case rawdb.DBLeveldb:
		kv, err = leveldb.New(chaindata, 16, 16, "", true)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rawdb.NewDatabase(kv), nil
}
