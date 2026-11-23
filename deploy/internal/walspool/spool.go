// Package walspool is the durable write-ahead spool of the mock
// adapter: every captured event is fsynced before it leaves the process,
// the acknowledged watermark is persisted after every confirmed
// delivery, and a restart replays exactly the pending suffix. There is
// no TTL, no quota and no deletion under pressure: a physical write
// error is explicit and WAL_SAVED is never declared without a durable
// write (LIVE_CONFIG_SPEC, "Состояние и запуск").
package walspool

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Event is one spooled record.
type Event struct {
	ID      uint64 `json:"id"`
	Height  uint64 `json:"height"`
	Route   string `json:"route"`
	Payload []byte `json:"payload"`
}

// meta is the persisted store identity and watermark. The producer and
// run identities survive restarts; a new store after disk loss gets new
// identities and never adopts a foreign pending run.
type meta struct {
	ProducerID string `json:"producer_id"`
	RunID      string `json:"run_id"`
	AckedID    uint64 `json:"acked_id"`
	Height     uint64 `json:"height"`
	NextID     uint64 `json:"next_id"`
}

// Spool is one adapter WAL directory.
type Spool struct {
	dir       string
	m         meta
	spoolFile *os.File
}

// Open opens or creates the spool in dir, compacts acknowledged events
// and returns the store identities with the pending suffix.
func Open(dir string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("wal: %w", err)
	}
	s := &Spool{dir: dir}
	if err := s.loadMeta(); err != nil {
		return nil, err
	}
	if err := s.compact(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.spoolPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("wal: %w", err)
	}
	s.spoolFile = f
	if err := s.syncDir(); err != nil {
		return nil, err
	}
	return s, nil
}

// ProducerID returns the 32 hex characters of the durable producer id.
func (s *Spool) ProducerID() string { return s.m.ProducerID }

// RunID returns the 32 hex characters of the durable run id.
func (s *Spool) RunID() string { return s.m.RunID }

// Height returns the last persisted mock chain height.
func (s *Spool) Height() uint64 { return s.m.Height }

func (s *Spool) spoolPath() string { return filepath.Join(s.dir, "spool.jsonl") }
func (s *Spool) metaPath() string  { return filepath.Join(s.dir, "meta.json") }
func (s *Spool) syncDir() error {
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// loadMeta reads or initializes the store identity, then repairs the
// sequence and height watermarks from the durable events. Identities are
// generated only for a genuinely new store and are never regenerated on
// retry (DEPLOYMENT_BOOTSTRAP_CONTRACT, "Environment init").
func (s *Spool) loadMeta() error {
	data, err := os.ReadFile(s.metaPath())
	switch {
	case os.IsNotExist(err):
		s.m = meta{
			ProducerID: newID16Hex(),
			RunID:      newID16Hex(),
		}
		if err := s.saveMeta(); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("wal meta: %w", err)
	default:
		if err := json.Unmarshal(data, &s.m); err != nil {
			return fmt.Errorf("wal meta: %w", err)
		}
		if s.m.ProducerID == "" || s.m.RunID == "" {
			return fmt.Errorf("wal meta: store identity is incomplete")
		}
	}
	// Append persists the identity change before the event bytes, so an
	// event beyond the persisted watermark means a store written before
	// that ordering (or a crash between the event fsync and an older
	// meta). Both converge to the maximum: an event id is never reused,
	// and a reserved id without its event stays a gap, never a loss.
	// A fresh identity runs the same repair: an orphan spool file without
	// its meta must not hand out its ids a second time.
	events, err := s.readAll()
	if err != nil {
		return err
	}
	repaired := false
	for _, e := range events {
		if e.ID > s.m.NextID {
			s.m.NextID = e.ID
			repaired = true
		}
		if e.Height > s.m.Height {
			s.m.Height = e.Height
			repaired = true
		}
	}
	if repaired {
		return s.saveMeta()
	}
	return nil
}

// saveMeta persists the current watermark atomically: write a temp file
// in the same directory, fsync it, rename over the target, then fsync
// the directory.
func (s *Spool) saveMeta() error {
	return s.saveMetaState(s.m)
}

// saveMetaState persists one watermark state atomically: write a temp
// file in the same directory, fsync it, rename over the target, then
// fsync the directory.
func (s *Spool) saveMetaState(m meta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := s.metaPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.metaPath()); err != nil {
		return err
	}
	return s.syncDir()
}

// compact drops acknowledged events from the spool file at open time.
func (s *Spool) compact() error {
	events, err := s.readAll()
	if err != nil {
		return err
	}
	tmp := s.spoolPath() + ".compact"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range events {
		if e.ID <= s.m.AckedID {
			continue
		}
		line, err := json.Marshal(e)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.spoolPath()); err != nil {
		return err
	}
	return s.syncDir()
}

// readAll reads every committed event line of the spool file. Only the
// last append can be torn by a crash: a trailing fragment without its
// newline, or an unreadable final complete line, never reached its
// fsync, was never acknowledged and is skipped; compact rewrites the
// file without it (LIVE_CONFIG_SPEC: WAL_SAVED is never declared without
// a durable write). A corrupt line before the final one is real damage
// and stays an explicit error.
func (s *Spool) readAll() ([]Event, error) {
	data, err := os.ReadFile(s.spoolPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Event
	for offset := 0; offset < len(data); {
		nl := bytes.IndexByte(data[offset:], '\n')
		if nl < 0 {
			// Torn trailing write of the last append: not committed.
			break
		}
		line := data[offset : offset+nl]
		next := offset + nl + 1
		if len(line) == 0 {
			offset = next
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			if next == len(data) {
				// Torn final complete line of the last append.
				break
			}
			return nil, fmt.Errorf("wal spool line: %w", err)
		}
		out = append(out, e)
		offset = next
	}
	return out, nil
}

// Pending returns the unacknowledged events in sequence order.
func (s *Spool) Pending() ([]Event, error) {
	events, err := s.readAll()
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range events {
		if e.ID > s.m.AckedID {
			out = append(out, e)
		}
	}
	return out, nil
}

// Append durably appends one event and returns its id. The identity
// change (the reserved id and the height watermark) is persisted before
// the event bytes, and the event is fsynced before the call returns:
// WAL_SAVED is only declared after this succeeds. A crash at any point
// leaves either a reserved-but-unused id (a gap: ids are never reused,
// nothing is delivered twice under one id) or a fully durable event; a
// torn event line without its fsync is skipped at the next open.
func (s *Spool) Append(height uint64, route string, payload []byte) (uint64, error) {
	next := s.m
	next.NextID++
	if height > next.Height {
		next.Height = height
	}
	if err := s.saveMetaState(next); err != nil {
		return 0, fmt.Errorf("wal meta: %w", err)
	}
	// From here the id is reserved durably: even if the event write
	// fails, the in-memory state adopts it so the id is never handed out
	// twice.
	s.m = next
	e := Event{ID: next.NextID, Height: height, Route: route, Payload: payload}
	line, err := json.Marshal(e)
	if err != nil {
		return 0, err
	}
	if _, err := s.spoolFile.Write(append(line, '\n')); err != nil {
		return 0, fmt.Errorf("wal append: %w", err)
	}
	if err := s.spoolFile.Sync(); err != nil {
		return 0, fmt.Errorf("wal fsync: %w", err)
	}
	return e.ID, nil
}

// Ack persists the acknowledged watermark. Ids are acknowledged in
// order; an id below the watermark is a duplicate and is ignored.
func (s *Spool) Ack(id uint64) error {
	if id <= s.m.AckedID {
		return nil
	}
	s.m.AckedID = id
	return s.saveMeta()
}

// newID16Hex generates one 16-byte crypto-random identity in hex.
func newID16Hex() string {
	var b [16]byte
	if _, err := readRandom(b[:]); err != nil {
		panic(fmt.Sprintf("wal: identity generation: %v", err))
	}
	return hex.EncodeToString(b[:])
}
