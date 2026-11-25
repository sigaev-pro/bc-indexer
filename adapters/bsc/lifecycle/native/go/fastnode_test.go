package bsclifecycle_test

// Path-scheme probes of the fast-node capture profile (D108) on the shipped
// node: eth.New with the capture config, so that the native patch of the
// profile is on the path: the read-only preflight, the synchronous flush of
// the path store, the rules that need the scheme of the opened store, and the
// startup rollback of a persistent layer above the head.
//
// TestFastNodeGoLiveCrash covers P4 of the recovery proof: a SIGKILL right
// after the live boundary record; the restart keeps H and raises no source
// invariant error. It also shows P1 on the shipped node: after each observed
// block is decided, the persistent state of the store is the state of the
// head. The store is of the scheme that EXTRACTOR_LIFECYCLE_STATE_SCHEME
// names (hash by default, path for the fast node).
// test/harness/fixtures/fastnode_bsc/fastnode_bsc.py runs and judges it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	bsclifecycle "github.com/ethereum/go-ethereum/extractor/bsc/lifecycle"
)

const (
	// envScheme names the state scheme of the probe stores (D108): hash, the
	// default, or path.
	envScheme = "EXTRACTOR_LIFECYCLE_STATE_SCHEME"
	// envMarker is the file where TestFastNodeGoLiveChild records what it
	// reached before it is killed.
	envMarker = "EXTRACTOR_LIFECYCLE_CHILD_MARKER"
)

// probeScheme is the state scheme of the probe stores.
func probeScheme() string {
	switch scheme := os.Getenv(envScheme); scheme {
	case "":
		return rawdb.HashScheme
	case rawdb.HashScheme, rawdb.PathScheme:
		return scheme
	default:
		panic("unknown state scheme " + scheme)
	}
}

// stateIds is the state id of root and the persistent state id of the store,
// as text; "none" when the store recorded no id for the root.
func stateIds(db ethdb.Database, root common.Hash) (string, string) {
	id := "none"
	if value := rawdb.ReadStateID(db, root); value != nil {
		id = fmt.Sprint(*value)
	}
	return id, fmt.Sprint(rawdb.ReadPersistentStateID(db))
}

// TestFastNodeGoLiveChild is the subprocess half of TestFastNodeGoLiveCrash:
// it builds a live store to the boundary, records the boundary and is killed
// with SIGKILL, so nothing of a shutdown runs.
func TestFastNodeGoLiveChild(t *testing.T) {
	if os.Getenv(envChildMode) != "golive-killed" {
		t.Skip("child only")
	}
	fx := loadFixture(t)
	home := probeEnv(t, envChildHome)
	n, err := openNode(fx, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.importNew("A1", "A2", "A3"); err != nil {
		t.Fatal(err)
	}
	live, err := n.life().GoLive()
	if err != nil {
		t.Fatal(err)
	}
	head := n.chain().CurrentBlock()
	id, persistent := stateIds(n.store(), head.Root)
	state := map[string]any{"scheme": n.chain().TrieDB().Scheme(), "boundary": live.Head.Number,
		"boundary_hash": live.Head.Hash, "head": fx.name(hexHash(head.Hash())), "head_state_id": id,
		"persistent_state_id": persistent, "phase": string(n.life().Gate().Phase())}
	data, err := json.Marshal(state)
	if err == nil {
		err = os.WriteFile(probeEnv(t, envMarker), data, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {}
}

// TestFastNodeGoLiveCrash covers P4: the node is killed with SIGKILL right
// after the boundary record, and the restart keeps H. The live import then
// decides two blocks, and after each the persistent state of the store is the
// state of the head (P1).
func TestFastNodeGoLiveCrash(t *testing.T) {
	fx := loadFixture(t)
	work := probeEnv(t, envWork)
	home := filepath.Join(work, "golive-killed")
	marker := filepath.Join(work, "golive-child.json")
	writeConfig(t, fx, filepath.Join(work, "capture-config.json"))
	if err := os.Setenv(bsclifecycle.ConfigEnv, filepath.Join(work, "capture-config.json")); err != nil {
		t.Fatal(err)
	}
	scheme := probeScheme()
	report := map[string]any{"schema_version": 1, "kind": "bsc-fastnode-golive", "state_scheme": scheme}

	cmd := exec.Command(os.Args[0], "-test.run", "^TestFastNodeGoLiveChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), envChildMode+"=golive-killed", envChildHome+"="+home, envMarker+"="+marker)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("child was not killed: %v\n%s", err, output)
	}
	if status, ok := exit.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child did not end by SIGKILL: %v\n%s", err, output)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("child state: %v\n%s", err, output)
	}
	var child map[string]any
	if err := json.Unmarshal(data, &child); err != nil {
		t.Fatal(err)
	}
	report["child"] = child

	// The stopped home: the head is the boundary H and its state is the
	// persistent state of the store.
	db := openDisk(t, home)
	stopped := map[string]any{"head": fx.name(hexHash(rawdb.ReadHeadBlockHash(db)))}
	id, persistent := stateIds(db, fx.blocks["A3"].Root())
	stopped["head_state_id"], stopped["persistent_state_id"] = id, persistent
	db.Close()
	report["stopped"] = stopped

	n, err := openNode(fx, home, nil)
	row := map[string]any{"restart_error": errText(err)}
	if err == nil {
		row["phase"] = string(n.life().Gate().Phase())
		row["head"] = fx.name(hexHash(n.chain().CurrentBlock().Hash()))
		if boundary, ok := n.life().Gate().Boundary(); ok {
			row["boundary"] = boundary.Head.Number
		} else {
			row["boundary"] = "none"
		}
		if view, _, ok, verr := n.life().ReadView(); verr == nil && ok {
			row["view_head"] = fx.name(view.Hash)
		} else {
			row["view_head"] = fmt.Sprint(verr)
		}
		decided := []map[string]any{}
		for _, name := range []string{"A4", "A5"} {
			before := len(decisions(fx, n.life()))
			importErr := errText(n.importNew(name))
			head := n.chain().CurrentBlock()
			id, persistent := stateIds(n.store(), head.Root)
			after := decisions(fx, n.life())
			decided = append(decided, map[string]any{"block": name, "import_error": importErr,
				"head": fx.name(hexHash(head.Hash())), "head_is_block": hexHash(head.Hash()) == hexHash(fx.blocks[name].Hash()),
				"head_state_id": id, "persistent_state_id": persistent,
				"new_decisions": len(after) - before})
		}
		row["decided"] = decided
		n.close()
	}
	report["restart"] = row
	writeJSON(t, filepath.Join(work, "golive-report.json"), report)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
}
