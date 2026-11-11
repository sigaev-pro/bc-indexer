package bsc

// Static manifest of one BSC block observation. The items and their checks
// started as the E01-02.inputs BSC probe (test/harness/fixtures/
// source_inputs_bsc). This block moves the genesis item to the shared
// canonical JSON rule (D99 item 3) and checks every value item in Go: the
// fork id is recomputed from the decoded genesis config with the native
// forkid package, never from JSON key names.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/extractor/admission"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	"github.com/ethereum/go-ethereum/rlp"
)

// StaticItemNames lists the static capsule items of one BSC block
// observation, in their sealed order.
var StaticItemNames = []string{"block-rlp", "parent-hash", "parent-header-rlp", "parent-root",
	"ancestor-headers-rlp", "genesis-json", "fork-id", "parlia-snapshot-keys", "native-commit",
	"normalization-profile", "state-profile"}

const (
	// StateProfile is the static native profile item: the capture profile of
	// E01-02.pins with native snapshots off.
	StateProfile = "state.scheme=hash;gcmode=full;snapshots=off"
	// NormalizationProfile is the test-only normalization profile item; the
	// product profile belongs to E04-01.block_record (D99 item 4).
	NormalizationProfile = "test-only-probe-v1"
	// CheckpointInterval is checkpointInterval of the pinned Parlia source:
	// the snapshot walk stops at a block number that it divides.
	CheckpointInterval = 1024
	// BlockHashWindow is the number of ancestors that BLOCKHASH can read.
	BlockHashWindow = 256
	// SnapshotPrefix is the key prefix of a stored Parlia snapshot.
	SnapshotPrefix = "parlia-"
)

// ErrStaticInput reports a static capsule that is incomplete or
// inconsistent, or a home that does not match it.
var ErrStaticInput = errors.New("static input check failed")

// AncestorFloor returns the lowest ancestor number whose header the import
// of block n (n >= 2) may read through the chain: the Parlia snapshot walk
// reads headers from the parent down to the checkpoint below it, and
// BLOCKHASH reads the 256 most recent ancestors.
func AncestorFloor(n uint64) uint64 {
	floor := (n - 1) / CheckpointInterval * CheckpointInterval
	if n > BlockHashWindow && n-BlockHashWindow < floor {
		floor = n - BlockHashWindow
	}
	return floor
}

// SnapshotDependency names the stored Parlia snapshot that the import may
// read: the snapshot of the checkpoint at or below the parent. Its content
// is not static; it is a logged dependency response.
type SnapshotDependency struct {
	Checkpoint uint64   `json:"checkpoint"`
	Keys       []string `json:"keys"`
}

// SnapshotKey returns the key of the stored Parlia snapshot of a block.
func SnapshotKey(hash common.Hash) []byte {
	return append([]byte(SnapshotPrefix), hash.Bytes()...)
}

// SnapshotFor returns the snapshot dependency of the block after parent.
func SnapshotFor(parent *types.Header, ancestors []*types.Header) (SnapshotDependency, error) {
	checkpoint := parent.Number.Uint64() / CheckpointInterval * CheckpointInterval
	for _, header := range append([]*types.Header{parent}, ancestors...) {
		if header.Number.Uint64() == checkpoint {
			return SnapshotDependency{Checkpoint: checkpoint, Keys: []string{hex.EncodeToString(SnapshotKey(header.Hash()))}}, nil
		}
	}
	return SnapshotDependency{}, fmt.Errorf("%w: no header of checkpoint %d", ErrStaticInput, checkpoint)
}

// KeyBytes returns the declared keys as bytes.
func (s SnapshotDependency) KeyBytes() ([][]byte, error) {
	var keys [][]byte
	for _, key := range s.Keys {
		data, err := hex.DecodeString(key)
		if err != nil || !bytes.HasPrefix(data, []byte(SnapshotPrefix)) {
			return nil, fmt.Errorf("%w: snapshot key %q", ErrStaticInput, key)
		}
		keys = append(keys, data)
	}
	return keys, nil
}

// ForkID returns the fork-id item: the EIP-2124 fork identifier of the
// block's own rules, as {"hash","next"} JSON. The native forkid package
// gathers the forks from the ChainConfig fields, so no JSON key name is
// interpreted here.
func ForkID(config *Genesis, number, time uint64) ([]byte, error) {
	id := forkid.NewID(config.Genesis.Config, config.Block, number, time)
	return json.Marshal(map[string]string{"hash": hex.EncodeToString(id.Hash[:]), "next": fmt.Sprint(id.Next)})
}

// Genesis is the decoded genesis of the home: its canonical JSON, the decoded
// value and the genesis block that the native chain holds.
type Genesis struct {
	Canonical []byte
	Genesis   *core.Genesis
	Block     *types.Block
}

// LoadGenesis canonicalizes a genesis file with the shared rule, decodes it
// and checks that it builds the given genesis block.
func LoadGenesis(file []byte, block *types.Block) (*Genesis, error) {
	canonical, err := admission.CanonicalJSON(file)
	if err != nil {
		return nil, fmt.Errorf("%w: genesis: %v", ErrStaticInput, err)
	}
	genesis := new(core.Genesis)
	if err := json.Unmarshal(canonical, genesis); err != nil {
		return nil, fmt.Errorf("%w: genesis: %v", ErrStaticInput, err)
	}
	if genesis.Config == nil {
		return nil, fmt.Errorf("%w: genesis has no chain config", ErrStaticInput)
	}
	if block == nil || genesis.ToBlock().Hash() != block.Hash() {
		return nil, fmt.Errorf("%w: genesis does not build the home's genesis block", ErrStaticInput)
	}
	return &Genesis{Canonical: canonical, Genesis: genesis, Block: block}, nil
}

// BlockItems returns the static capsule items of the observation of block,
// in their sealed order. header returns the canonical header of a number
// below the block.
func BlockItems(block *types.Block, header func(uint64) *types.Header, genesis *Genesis,
	nativeCommit string) ([]sourceinputs.Item, error) {
	n := block.NumberU64()
	if n == 0 {
		return nil, fmt.Errorf("%w: block 0 has no parent", ErrStaticInput)
	}
	blockRLP, err := rlp.EncodeToBytes(block)
	if err != nil {
		return nil, err
	}
	parent := header(n - 1)
	if parent == nil {
		return nil, fmt.Errorf("%w: no parent header of block %d", ErrStaticInput, n)
	}
	if parent.Hash() != block.ParentHash() {
		return nil, fmt.Errorf("%w: parent header of block %d has another hash", ErrStaticInput, n)
	}
	ancestors := []*types.Header{}
	for number := n - 1; n >= 2 && number > AncestorFloor(n); {
		number--
		ancestor := header(number)
		if ancestor == nil {
			return nil, fmt.Errorf("%w: ancestor header %d", ErrStaticInput, number)
		}
		ancestors = append(ancestors, ancestor)
	}
	parentRLP, err := rlp.EncodeToBytes(parent)
	if err != nil {
		return nil, err
	}
	ancestorsRLP, err := rlp.EncodeToBytes(ancestors)
	if err != nil {
		return nil, err
	}
	fork, err := ForkID(genesis, n, block.Time())
	if err != nil {
		return nil, err
	}
	dependency, err := SnapshotFor(parent, ancestors)
	if err != nil {
		return nil, err
	}
	snapshot, err := json.Marshal(dependency)
	if err != nil {
		return nil, err
	}
	return []sourceinputs.Item{
		{Name: "block-rlp", Data: blockRLP},
		{Name: "parent-hash", Data: parent.Hash().Bytes()},
		{Name: "parent-header-rlp", Data: parentRLP},
		{Name: "parent-root", Data: parent.Root.Bytes()},
		{Name: "ancestor-headers-rlp", Data: ancestorsRLP},
		{Name: "genesis-json", Data: genesis.Canonical},
		{Name: "fork-id", Data: fork},
		{Name: "parlia-snapshot-keys", Data: snapshot},
		{Name: "native-commit", Data: []byte(nativeCommit)},
		{Name: "normalization-profile", Data: []byte(NormalizationProfile)},
		{Name: "state-profile", Data: []byte(StateProfile)},
	}, nil
}

// Static is the checked static manifest of one block observation.
type Static struct {
	Block     *types.Block
	Parent    *types.Header
	Ancestors []*types.Header // descending, from the grandparent
	Root      common.Hash
	Snapshot  SnapshotDependency
}

// ReadStatic reads and checks the stored capsule of an observation against
// the home's genesis and native commit.
func ReadStatic(db sourceinputs.Store, id string, genesis *Genesis, nativeCommit string) (Static, error) {
	capsule, err := sourceinputs.ReadCapsule(db, id)
	if err != nil {
		return Static{}, err
	}
	if len(capsule.Items) != len(StaticItemNames) {
		return Static{}, fmt.Errorf("%w: %d capsule items, want %d", ErrStaticInput, len(capsule.Items), len(StaticItemNames))
	}
	items := make(map[string][]byte, len(capsule.Items))
	for index, ref := range capsule.Items {
		if ref.Name != StaticItemNames[index] {
			return Static{}, fmt.Errorf("%w: capsule item %d is %q, want %q", ErrStaticInput, index, ref.Name, StaticItemNames[index])
		}
		if items[ref.Name], err = sourceinputs.ReadItem(db, id, index); err != nil {
			return Static{}, err
		}
	}
	return DecodeStatic(items, genesis, nativeCommit)
}

// DecodeStatic decodes the items of one block observation and checks that
// the block, the parent, the parent root, the ancestor window and the
// declared snapshot dependency agree with each other, and that the genesis,
// fork id, native commit and profile items equal the home's values.
func DecodeStatic(items map[string][]byte, genesis *Genesis, nativeCommit string) (Static, error) {
	bad := func(format string, args ...any) (Static, error) {
		return Static{}, fmt.Errorf("%w: "+format, append([]any{ErrStaticInput}, args...)...)
	}
	for _, name := range StaticItemNames {
		if _, ok := items[name]; !ok {
			return bad("missing item %s", name)
		}
	}
	static := Static{Block: new(types.Block), Parent: new(types.Header)}
	if err := rlp.DecodeBytes(items["parent-header-rlp"], static.Parent); err != nil {
		return bad("parent header: %v", err)
	}
	if !bytes.Equal(items["parent-hash"], static.Parent.Hash().Bytes()) {
		return bad("parent header hash differs from parent-hash")
	}
	if !bytes.Equal(items["parent-root"], static.Parent.Root.Bytes()) {
		return bad("parent-root differs from the parent header root")
	}
	if err := rlp.DecodeBytes(items["block-rlp"], static.Block); err != nil {
		return bad("block: %v", err)
	}
	n := static.Parent.Number.Uint64() + 1
	if static.Block.NumberU64() != n || static.Block.ParentHash() != static.Parent.Hash() {
		return bad("block %d is not the child of the parent header", static.Block.NumberU64())
	}
	if err := rlp.DecodeBytes(items["ancestor-headers-rlp"], &static.Ancestors); err != nil {
		return bad("ancestor headers: %v", err)
	}
	want := uint64(0)
	if n >= 2 {
		want = n - 1 - AncestorFloor(n)
	}
	if uint64(len(static.Ancestors)) != want {
		return bad("%d ancestor headers, want %d", len(static.Ancestors), want)
	}
	child := static.Parent
	for _, ancestor := range static.Ancestors {
		if ancestor.Hash() != child.ParentHash || ancestor.Number.Uint64()+1 != child.Number.Uint64() {
			return bad("ancestor header %d does not link", ancestor.Number.Uint64())
		}
		child = ancestor
	}
	dependency, err := SnapshotFor(static.Parent, static.Ancestors)
	if err != nil {
		return Static{}, err
	}
	if encoded, err := json.Marshal(dependency); err != nil || !bytes.Equal(encoded, items["parlia-snapshot-keys"]) {
		return bad("parlia-snapshot-keys differs from the headers")
	}
	if !bytes.Equal(items["genesis-json"], genesis.Canonical) {
		return bad("genesis-json differs from the home's canonical genesis")
	}
	fork, err := ForkID(genesis, n, static.Block.Time())
	if err != nil || !bytes.Equal(items["fork-id"], fork) {
		return bad("fork-id differs from the genesis config")
	}
	if !bytes.Equal(items["native-commit"], []byte(nativeCommit)) {
		return bad("native-commit differs from the home's commit")
	}
	if !bytes.Equal(items["normalization-profile"], []byte(NormalizationProfile)) ||
		!bytes.Equal(items["state-profile"], []byte(StateProfile)) {
		return bad("profile items differ")
	}
	static.Root, static.Snapshot = static.Parent.Root, dependency
	return static, nil
}
