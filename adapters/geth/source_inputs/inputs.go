// Package sourceinputs keeps the inputs of one native observation: a sealed
// static input capsule and an append-only log of consumed dependency
// responses (E01-02.inputs, REQ-CAP-04).
//
// This file is shared: the same bytes compile inside the Geth, BSC and Bor
// module graphs and with the standard library alone. It imports no client
// type. A portable test checks that the three copies are identical.
package sourceinputs

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

// Store is the subset of the native key-value store that this package uses.
// The native ethdb.Database of each accepted client satisfies it. Writes are
// durable only after SyncKeyValue returns nil; before that, a power loss may
// keep any subset of them. After a process crash, unsynced writes may still
// be visible and be lost later, so SealCapsule and OpenLog sync what they
// read before they rely on it.
type Store interface {
	Has(key []byte) (bool, error)
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	SyncKeyValue() error
}

// ChunkBytes is the largest value of one stored data chunk.
const ChunkBytes = 64 * 1024

// keyPrefix starts every key. The key length is never 32 bytes and the key
// never starts with the native code prefix, so the native offline pruner
// never deletes it. IDs cannot contain "/", so keys of two observations
// never collide.
const keyPrefix = "extractor-source-inputs-v1/"

// Domain tags of the digests. Each is followed by one zero byte.
const (
	tagCapsule  = "extractor-source-inputs-capsule-v1"
	tagEntry    = "extractor-source-inputs-entry-v1"
	tagChain    = "extractor-source-inputs-chain-v1"
	tagConsumed = "extractor-source-inputs-consumed-v1"
)

var (
	// ErrInvalid rejects a malformed argument.
	ErrInvalid = errors.New("invalid source input")
	// ErrCapsuleConflict rejects a capsule that differs from the sealed one.
	ErrCapsuleConflict = errors.New("conflicting input capsule")
	// ErrNoCapsule reports a log opened before its capsule was sealed.
	ErrNoCapsule = errors.New("input capsule is not sealed")
	// ErrPrefixConflict rejects a request that differs from the durable
	// request at the same offset.
	ErrPrefixConflict = errors.New("dependency log prefix conflict")
	// ErrCorrupt reports stored data that fails its digest or format check.
	ErrCorrupt = errors.New("corrupt source input record")
	// ErrClosed refuses a new response after the consumed manifest was closed.
	ErrClosed = errors.New("dependency log is closed")
	// ErrUnconsumed refuses to close a log while durable responses remain
	// that this attempt has not consumed.
	ErrUnconsumed = errors.New("durable responses were not consumed")
	// ErrFailed reports a log that stopped after an earlier error.
	ErrFailed = errors.New("dependency log stopped after an error")
)

var idPattern = regexp.MustCompile(`^[0-9a-z][0-9a-z._-]{0,127}$`)

// Item is one named static input, such as a header or body encoding.
type Item struct {
	Name string
	Data []byte
}

// ItemRef describes one sealed item.
type ItemRef struct {
	Name   string `json:"name"`
	Length uint64 `json:"length"`
	SHA256 string `json:"sha256"`
}

// Capsule is the sealed static-input manifest of one observation.
type Capsule struct {
	Observation string    `json:"observation"`
	Items       []ItemRef `json:"items"`
	Digest      string    `json:"digest"`
}

// Entry is one durable dependency record.
type Entry struct {
	Sequence       uint64 `json:"sequence"`
	RequestSHA256  string `json:"request_sha256"`
	RequestLength  uint64 `json:"request_length"`
	ResponseSHA256 string `json:"response_sha256"`
	ResponseLength uint64 `json:"response_length"`
}

// Consumed is the final consumed-input manifest of one observation.
type Consumed struct {
	Observation   string `json:"observation"`
	CapsuleDigest string `json:"capsule_digest"`
	Count         uint64 `json:"count"`
	Chain         string `json:"chain"`
	Digest        string `json:"digest"`
}

type frontier struct {
	Count uint64 `json:"count"`
	Chain string `json:"chain"`
}

func capsuleKey(id string) []byte { return []byte(keyPrefix + id + "/capsule") }

func itemChunkKey(id string, item, chunk uint64) []byte {
	return []byte(fmt.Sprintf("%s%s/capsule/%016x/%016x", keyPrefix, id, item, chunk))
}

func entryKey(id string, seq uint64) []byte {
	return []byte(fmt.Sprintf("%s%s/log/%016x", keyPrefix, id, seq))
}

func responseChunkKey(id string, seq, chunk uint64) []byte {
	return []byte(fmt.Sprintf("%s%s/log/%016x/%016x", keyPrefix, id, seq, chunk))
}

func frontierKey(id string) []byte { return []byte(keyPrefix + id + "/frontier") }

func closedKey(id string) []byte { return []byte(keyPrefix + id + "/closed") }

func sum(data []byte) [32]byte { return sha256.Sum256(data) }

func u64(value uint64) []byte {
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], value)
	return out[:]
}

func tagged(tag string) []byte { return append([]byte(tag), 0) }

// CapsuleDigest returns SHA-256 of the tag, the item count, and for each item
// in order its name length, name, data length and data SHA-256.
func CapsuleDigest(items []ItemRef) (string, error) {
	buf := tagged(tagCapsule)
	buf = append(buf, u64(uint64(len(items)))...)
	for _, item := range items {
		digest, err := decodeDigest(item.SHA256)
		if err != nil {
			return "", err
		}
		buf = append(buf, u64(uint64(len(item.Name)))...)
		buf = append(buf, item.Name...)
		buf = append(buf, u64(item.Length)...)
		buf = append(buf, digest[:]...)
	}
	digest := sum(buf)
	return hex.EncodeToString(digest[:]), nil
}

// EntryDigest returns SHA-256 of the tag, the sequence, the request SHA-256
// and length, and the response SHA-256 and length.
func EntryDigest(entry Entry) (string, error) {
	request, err := decodeDigest(entry.RequestSHA256)
	if err != nil {
		return "", err
	}
	response, err := decodeDigest(entry.ResponseSHA256)
	if err != nil {
		return "", err
	}
	buf := tagged(tagEntry)
	buf = append(buf, u64(entry.Sequence)...)
	buf = append(buf, request[:]...)
	buf = append(buf, u64(entry.RequestLength)...)
	buf = append(buf, response[:]...)
	buf = append(buf, u64(entry.ResponseLength)...)
	digest := sum(buf)
	return hex.EncodeToString(digest[:]), nil
}

// ChainStart returns SHA-256 of the chain tag and the capsule digest.
func ChainStart(capsuleDigest string) (string, error) {
	capsule, err := decodeDigest(capsuleDigest)
	if err != nil {
		return "", err
	}
	digest := sum(append(tagged(tagChain), capsule[:]...))
	return hex.EncodeToString(digest[:]), nil
}

// ChainNext returns SHA-256 of the previous chain value and the entry digest.
func ChainNext(chain, entryDigest string) (string, error) {
	previous, err := decodeDigest(chain)
	if err != nil {
		return "", err
	}
	entry, err := decodeDigest(entryDigest)
	if err != nil {
		return "", err
	}
	digest := sum(append(previous[:], entry[:]...))
	return hex.EncodeToString(digest[:]), nil
}

// ConsumedDigest returns SHA-256 of the consumed tag, the capsule digest, the
// entry count and the chain value after the last entry.
func ConsumedDigest(capsuleDigest string, count uint64, chain string) (string, error) {
	capsule, err := decodeDigest(capsuleDigest)
	if err != nil {
		return "", err
	}
	last, err := decodeDigest(chain)
	if err != nil {
		return "", err
	}
	buf := tagged(tagConsumed)
	buf = append(buf, capsule[:]...)
	buf = append(buf, u64(count)...)
	buf = append(buf, last[:]...)
	digest := sum(buf)
	return hex.EncodeToString(digest[:]), nil
}

func decodeDigest(value string) ([32]byte, error) {
	var out [32]byte
	if len(value) != 64 {
		return out, fmt.Errorf("%w: digest %q", ErrCorrupt, value)
	}
	if _, err := hex.Decode(out[:], []byte(value)); err != nil || hex.EncodeToString(out[:]) != value {
		return out, fmt.Errorf("%w: digest %q", ErrCorrupt, value)
	}
	return out, nil
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func readRecord(db Store, key []byte, value any) (bool, error) {
	ok, err := db.Has(key)
	if err != nil || !ok {
		return false, err
	}
	data, err := db.Get(key)
	if err != nil {
		return false, err
	}
	if err := strictDecode(data, value); err != nil {
		return false, fmt.Errorf("%w: %s: %v", ErrCorrupt, key, err)
	}
	return true, nil
}

func putRecord(db Store, key []byte, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return db.Put(key, data)
}

func chunkCount(length uint64) uint64 { return (length + ChunkBytes - 1) / ChunkBytes }

// putChunks writes data as chunks of at most ChunkBytes. It does not sync.
func putChunks(db Store, data []byte, key func(uint64) []byte) error {
	for index := uint64(0); index < chunkCount(uint64(len(data))); index++ {
		start := index * ChunkBytes
		end := min(start+ChunkBytes, uint64(len(data)))
		if err := db.Put(key(index), data[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// readChunks reads the chunks of length bytes. Every chunk except the last
// must hold exactly ChunkBytes, and the whole SHA-256 must match the record.
// A missing or wrong-sized chunk is ErrCorrupt; a store error passes through.
func readChunks(db Store, length uint64, digest string, key func(uint64) []byte) ([]byte, error) {
	data := make([]byte, 0, length)
	for index := uint64(0); index < chunkCount(length); index++ {
		ok, err := db.Has(key(index))
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: missing chunk %d", ErrCorrupt, index)
		}
		chunk, err := db.Get(key(index))
		if err != nil {
			return nil, err
		}
		want := min(uint64(ChunkBytes), length-index*ChunkBytes)
		if uint64(len(chunk)) != want {
			return nil, fmt.Errorf("%w: chunk %d has %d bytes, want %d", ErrCorrupt, index, len(chunk), want)
		}
		data = append(data, chunk...)
	}
	got := sum(data)
	if hex.EncodeToString(got[:]) != digest {
		return nil, fmt.Errorf("%w: data digest mismatch", ErrCorrupt)
	}
	return data, nil
}

func validID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%w: observation ID %q", ErrInvalid, id)
	}
	return nil
}

// SealCapsule writes the static inputs once, in the given order, and syncs
// them. The chunks are synced before the manifest, and the synced manifest
// is the commit point: chunks left by an interrupted seal are never read and
// are overwritten. Sealing the same items again verifies the stored chunks;
// different items are ErrCapsuleConflict.
func SealCapsule(db Store, id string, items []Item) (Capsule, error) {
	if err := validID(id); err != nil {
		return Capsule{}, err
	}
	refs := make([]ItemRef, len(items))
	names := make(map[string]bool, len(items))
	for index, item := range items {
		if !idPattern.MatchString(item.Name) || names[item.Name] {
			return Capsule{}, fmt.Errorf("%w: item name %q", ErrInvalid, item.Name)
		}
		names[item.Name] = true
		digest := sum(item.Data)
		refs[index] = ItemRef{Name: item.Name, Length: uint64(len(item.Data)), SHA256: hex.EncodeToString(digest[:])}
	}
	digest, err := CapsuleDigest(refs)
	if err != nil {
		return Capsule{}, err
	}
	capsule := Capsule{Observation: id, Items: refs, Digest: digest}
	existing, ok, err := readCapsule(db, id)
	if err != nil {
		return Capsule{}, err
	}
	if ok {
		if existing.Digest != digest {
			return Capsule{}, fmt.Errorf("%w: sealed %s, requested %s", ErrCapsuleConflict, existing.Digest, digest)
		}
		for index := range refs {
			if _, err := ReadItem(db, id, index); err != nil {
				return Capsule{}, err
			}
		}
		// The manifest may be visible but unsynced after a process crash.
		if err := db.SyncKeyValue(); err != nil {
			return Capsule{}, err
		}
		return existing, nil
	}
	for index, item := range items {
		if err := putChunks(db, item.Data, func(chunk uint64) []byte {
			return itemChunkKey(id, uint64(index), chunk)
		}); err != nil {
			return Capsule{}, err
		}
	}
	if err := db.SyncKeyValue(); err != nil {
		return Capsule{}, err
	}
	if err := putRecord(db, capsuleKey(id), capsule); err != nil {
		return Capsule{}, err
	}
	if err := db.SyncKeyValue(); err != nil {
		return Capsule{}, err
	}
	return capsule, nil
}

func readCapsule(db Store, id string) (Capsule, bool, error) {
	var capsule Capsule
	ok, err := readRecord(db, capsuleKey(id), &capsule)
	if err != nil || !ok {
		return Capsule{}, false, err
	}
	digest, err := CapsuleDigest(capsule.Items)
	if err != nil || capsule.Observation != id || capsule.Digest != digest {
		return Capsule{}, false, fmt.Errorf("%w: capsule manifest of %s", ErrCorrupt, id)
	}
	return capsule, true, nil
}

// ReadCapsule returns the sealed manifest, or ErrNoCapsule.
func ReadCapsule(db Store, id string) (Capsule, error) {
	if err := validID(id); err != nil {
		return Capsule{}, err
	}
	capsule, ok, err := readCapsule(db, id)
	if err != nil {
		return Capsule{}, err
	}
	if !ok {
		return Capsule{}, fmt.Errorf("%w: %s", ErrNoCapsule, id)
	}
	return capsule, nil
}

// ReadItem returns the saved bytes of one sealed item after checking them.
func ReadItem(db Store, id string, index int) ([]byte, error) {
	capsule, err := ReadCapsule(db, id)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(capsule.Items) {
		return nil, fmt.Errorf("%w: item index %d", ErrInvalid, index)
	}
	item := capsule.Items[index]
	return readChunks(db, item.Length, item.SHA256, func(chunk uint64) []byte {
		return itemChunkKey(id, uint64(index), chunk)
	})
}

// Log is one attempt's view of the dependency log. Every attempt starts at
// the first logical request of the observation. It is not safe for
// concurrent use: the caller holds the native observation guard, so requests
// arrive in native order.
type Log struct {
	db      Store
	id      string
	capsule string
	durable frontier
	closed  *Consumed
	cursor  uint64
	chain   string
	err     error
}

// OpenLog opens the log of a sealed capsule. It checks the durable frontier
// against the chain of every durable entry, and a closed manifest against
// the frontier.
func OpenLog(db Store, id string) (*Log, error) {
	capsule, err := ReadCapsule(db, id)
	if err != nil {
		return nil, err
	}
	start, err := ChainStart(capsule.Digest)
	if err != nil {
		return nil, err
	}
	durable := frontier{Count: 0, Chain: start}
	if _, err := readRecord(db, frontierKey(id), &durable); err != nil {
		return nil, err
	}
	chain := start
	for seq := uint64(0); seq < durable.Count; seq++ {
		entry, err := readEntry(db, id, seq)
		if err != nil {
			return nil, err
		}
		digest, err := EntryDigest(entry)
		if err != nil {
			return nil, err
		}
		if chain, err = ChainNext(chain, digest); err != nil {
			return nil, err
		}
	}
	if chain != durable.Chain {
		return nil, fmt.Errorf("%w: frontier chain of %s", ErrCorrupt, id)
	}
	log := &Log{db: db, id: id, capsule: capsule.Digest, durable: durable, chain: start}
	var closed Consumed
	ok, err := readRecord(db, closedKey(id), &closed)
	if err != nil {
		return nil, err
	}
	if ok {
		want, err := consumed(id, capsule.Digest, durable)
		if err != nil {
			return nil, err
		}
		if closed != want {
			return nil, fmt.Errorf("%w: closed manifest of %s", ErrCorrupt, id)
		}
		log.closed = &closed
	}
	// After a process crash, the frontier and its entries may be visible but
	// not yet synced. Sync them before any of them is replayed, so a replayed
	// response is always durable.
	if err := db.SyncKeyValue(); err != nil {
		return nil, err
	}
	return log, nil
}

func readEntry(db Store, id string, seq uint64) (Entry, error) {
	var entry Entry
	ok, err := readRecord(db, entryKey(id, seq), &entry)
	if err != nil {
		return Entry{}, err
	}
	if !ok {
		return Entry{}, fmt.Errorf("%w: entry %d of %s", ErrCorrupt, seq, id)
	}
	return entry, nil
}

func consumed(id, capsule string, durable frontier) (Consumed, error) {
	digest, err := ConsumedDigest(capsule, durable.Count, durable.Chain)
	if err != nil {
		return Consumed{}, err
	}
	return Consumed{Observation: id, CapsuleDigest: capsule, Count: durable.Count, Chain: durable.Chain, Digest: digest}, nil
}

// Frontier returns the number of durable entries.
func (l *Log) Frontier() uint64 { return l.durable.Count }

// Cursor returns the number of entries this attempt consumed.
func (l *Log) Cursor() uint64 { return l.cursor }

func (l *Log) fail(err error) error {
	l.err = err
	return err
}

// Next returns the response for the next request of this attempt.
//
// Below the durable frontier it returns the saved bytes and never calls
// fetch; a different request at that offset is ErrPrefixConflict and changes
// nothing. At the frontier it calls fetch once for the raw response bytes,
// writes the response chunks and the entry, syncs, then writes the advanced
// frontier and syncs again, and only then returns the bytes. A power loss
// before the second sync leaves the frontier unchanged, so the response was
// never handed out and a retry fetches again; after it, a retry returns the
// saved bytes. A fetch error writes nothing and leaves the log usable. Any
// other error stops the log; reopen it to continue.
func (l *Log) Next(request []byte, fetch func() ([]byte, error)) ([]byte, error) {
	if l.err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFailed, l.err)
	}
	if len(request) == 0 {
		return nil, fmt.Errorf("%w: empty request", ErrInvalid)
	}
	requestDigest := sum(request)
	requestSHA := hex.EncodeToString(requestDigest[:])
	if l.cursor < l.durable.Count {
		return l.replay(requestSHA)
	}
	if l.closed != nil {
		return nil, fmt.Errorf("%w: %s at %d", ErrClosed, l.id, l.cursor)
	}
	response, err := fetch()
	if err != nil {
		return nil, fmt.Errorf("fetch dependency %d of %s: %w", l.cursor, l.id, err)
	}
	response = bytes.Clone(response)
	responseDigest := sum(response)
	entry := Entry{
		Sequence:       l.cursor,
		RequestSHA256:  requestSHA,
		RequestLength:  uint64(len(request)),
		ResponseSHA256: hex.EncodeToString(responseDigest[:]),
		ResponseLength: uint64(len(response)),
	}
	digest, err := EntryDigest(entry)
	if err != nil {
		return nil, l.fail(err)
	}
	chain, err := ChainNext(l.chain, digest)
	if err != nil {
		return nil, l.fail(err)
	}
	seq := l.cursor
	if err := putChunks(l.db, response, func(chunk uint64) []byte {
		return responseChunkKey(l.id, seq, chunk)
	}); err != nil {
		return nil, l.fail(err)
	}
	if err := putRecord(l.db, entryKey(l.id, seq), entry); err != nil {
		return nil, l.fail(err)
	}
	if err := l.db.SyncKeyValue(); err != nil {
		return nil, l.fail(err)
	}
	next := frontier{Count: seq + 1, Chain: chain}
	if err := putRecord(l.db, frontierKey(l.id), next); err != nil {
		return nil, l.fail(err)
	}
	if err := l.db.SyncKeyValue(); err != nil {
		return nil, l.fail(err)
	}
	l.durable, l.chain, l.cursor = next, chain, seq+1
	return response, nil
}

func (l *Log) replay(requestSHA string) ([]byte, error) {
	entry, err := readEntry(l.db, l.id, l.cursor)
	if err != nil {
		return nil, l.fail(err)
	}
	if entry.RequestSHA256 != requestSHA {
		return nil, l.fail(fmt.Errorf("%w: %s offset %d holds request %s, retry sent %s",
			ErrPrefixConflict, l.id, l.cursor, entry.RequestSHA256, requestSHA))
	}
	response, err := readChunks(l.db, entry.ResponseLength, entry.ResponseSHA256, func(chunk uint64) []byte {
		return responseChunkKey(l.id, entry.Sequence, chunk)
	})
	if err != nil {
		return nil, l.fail(err)
	}
	digest, err := EntryDigest(entry)
	if err != nil {
		return nil, l.fail(err)
	}
	if l.chain, err = ChainNext(l.chain, digest); err != nil {
		return nil, l.fail(err)
	}
	l.cursor++
	return response, nil
}

// Finish closes the final consumed-input manifest after a completed
// execution and syncs it. The attempt must have consumed every durable
// entry. After Finish, Next still replays the saved prefix but refuses a new
// response. Finishing a closed log again returns the same manifest.
func (l *Log) Finish() (Consumed, error) {
	if l.err != nil {
		return Consumed{}, fmt.Errorf("%w: %v", ErrFailed, l.err)
	}
	if l.cursor != l.durable.Count {
		return Consumed{}, fmt.Errorf("%w: consumed %d of %d", ErrUnconsumed, l.cursor, l.durable.Count)
	}
	if l.closed != nil {
		return *l.closed, nil
	}
	manifest, err := consumed(l.id, l.capsule, l.durable)
	if err != nil {
		return Consumed{}, l.fail(err)
	}
	if err := putRecord(l.db, closedKey(l.id), manifest); err != nil {
		return Consumed{}, l.fail(err)
	}
	if err := l.db.SyncKeyValue(); err != nil {
		return Consumed{}, l.fail(err)
	}
	l.closed = &manifest
	return manifest, nil
}
