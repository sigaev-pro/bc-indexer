// Round-trip and crash-point tests of the WAL spool: every crash
// window of the two durable writes of Append must reopen into a state
// where an event id is never reused and a torn write is skipped, never
// a failed open (LIVE_CONFIG_SPEC, "Состояние и запуск": no silent
// reinitialization, no loss of pending bytes).
package walspool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// openSpool opens a spool in a fresh temporary directory.
func openSpool(t *testing.T) (*Spool, string) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s, s.dir
}

// appendEvent appends one event and returns its id.
func appendEvent(t *testing.T, s *Spool, height uint64, route string) uint64 {
	t.Helper()
	id, err := s.Append(height, route, []byte(fmt.Sprintf(`{"height":%d}`, height)))
	if err != nil {
		t.Fatalf("append %d: %v", height, err)
	}
	return id
}

// pendingIDs returns the ids of the pending suffix.
func pendingIDs(t *testing.T, s *Spool) []uint64 {
	t.Helper()
	pending, err := s.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	var ids []uint64
	for _, e := range pending {
		ids = append(ids, e.ID)
	}
	return ids
}

func idsEqual(got []uint64, want ...uint64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// rawAppendLine writes one event line directly into the spool file and
// fsyncs it, without touching meta: the exact on-disk state of the old
// Append order crashing between the spool fsync and saveMeta.
func rawAppendLine(t *testing.T, d string, e Event) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(d, "spool.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("raw append open: %v", err)
	}
	defer f.Close()
	line, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("raw append marshal: %v", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatalf("raw append write: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("raw append fsync: %v", err)
	}
}

// truncateLastLine removes the final event line of the spool file: the
// on-disk state of the new Append order crashing after the meta fsync
// but before the event bytes reached the disk.
func truncateLastLine(t *testing.T, d string) {
	t.Helper()
	path := filepath.Join(d, "spool.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	cut := bytes.LastIndex(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if cut < 0 {
		t.Fatalf("spool holds less than two lines")
	}
	if err := os.WriteFile(path, data[:cut+1], 0o644); err != nil {
		t.Fatalf("truncate spool: %v", err)
	}
}

// TestRoundTrip covers open, append, pending, restart, ack and
// compaction: the durable identities survive the restart, the pending
// suffix replays exactly, and acknowledged events disappear.
func TestRoundTrip(t *testing.T) {
	s, d := openSpool(t)
	producer, run := s.ProducerID(), s.RunID()
	if len(producer) != 32 || len(run) != 32 {
		t.Fatalf("identities %q/%q are not 32 hex characters", producer, run)
	}
	for h := uint64(1); h <= 3; h++ {
		if id := appendEvent(t, s, h, "block.applied"); id != h {
			t.Fatalf("append %d returned id %d", h, id)
		}
	}
	if got := s.Height(); got != 3 {
		t.Fatalf("height %d, want 3", got)
	}
	if got := pendingIDs(t, s); !idsEqual(got, 1, 2, 3) {
		t.Fatalf("pending %v, want [1 2 3]", got)
	}

	// Restart: same identities, same pending suffix, same heights.
	s2, err := Open(d)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.ProducerID() != producer || s2.RunID() != run {
		t.Fatalf("identities changed across restart: %q/%q", s2.ProducerID(), s2.RunID())
	}
	pending, err := s2.Pending()
	if err != nil {
		t.Fatalf("pending after restart: %v", err)
	}
	if len(pending) != 3 || pending[2].ID != 3 || pending[2].Height != 3 {
		t.Fatalf("pending after restart: %+v", pending)
	}
	if !bytes.Equal(pending[0].Payload, []byte(`{"height":1}`)) {
		t.Fatalf("payload after restart: %q", pending[0].Payload)
	}

	// Ack in order, restart: the acknowledged prefix is compacted away.
	if err := s2.Ack(2); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := s2.Ack(1); err != nil {
		t.Fatalf("duplicate ack: %v", err)
	}
	s3, err := Open(d)
	if err != nil {
		t.Fatalf("reopen after ack: %v", err)
	}
	if got := pendingIDs(t, s3); !idsEqual(got, 3) {
		t.Fatalf("pending after compaction %v, want [3]", got)
	}
	if id := appendEvent(t, s3, 4, "tx.applied"); id != 4 {
		t.Fatalf("append after restart returned id %d, want 4", id)
	}
	if s3.ProducerID() != producer {
		t.Fatalf("identities changed after compaction")
	}
}

// TestCrashBetweenSpoolFsyncAndMeta covers the crash window of the old
// Append order: the event line is durable, the meta watermark is not.
// The reopen must adopt the event (no loss) and never reuse its id.
func TestCrashBetweenSpoolFsyncAndMeta(t *testing.T) {
	s, d := openSpool(t)
	appendEvent(t, s, 1, "block.applied")
	appendEvent(t, s, 2, "block.applied")
	// id 3 is fsynced into the spool but meta still ends at NextID 2.
	rawAppendLine(t, d, Event{ID: 3, Height: 3, Route: "tx.applied", Payload: []byte(`{"height":3}`)})

	s2, err := Open(d)
	if err != nil {
		t.Fatalf("reopen after crash: %v", err)
	}
	if got := pendingIDs(t, s2); !idsEqual(got, 1, 2, 3) {
		t.Fatalf("pending after crash %v, want [1 2 3]", got)
	}
	if id := appendEvent(t, s2, 4, "tx.applied"); id != 4 {
		t.Fatalf("append after crash returned id %d, want 4 (no id reuse)", id)
	}
	if got := s2.Height(); got != 4 {
		t.Fatalf("height after crash %d, want 4", got)
	}
}

// TestCrashBetweenMetaAndEvent covers the crash window of the current
// Append order: the identity change is durable, the event bytes are
// not. The reserved id stays a gap; it is never reused.
func TestCrashBetweenMetaAndEvent(t *testing.T) {
	s, d := openSpool(t)
	appendEvent(t, s, 1, "block.applied")
	appendEvent(t, s, 2, "block.applied")
	appendEvent(t, s, 3, "block.applied")
	// The durable event of id 3 never reached the disk.
	truncateLastLine(t, d)

	s2, err := Open(d)
	if err != nil {
		t.Fatalf("reopen after crash: %v", err)
	}
	if got := pendingIDs(t, s2); !idsEqual(got, 1, 2) {
		t.Fatalf("pending after crash %v, want [1 2]", got)
	}
	// id 3 was reserved durably before its event: the next append must
	// hand out 4, never 3 again.
	if id := appendEvent(t, s2, 4, "tx.applied"); id != 4 {
		t.Fatalf("append after crash returned id %d, want 4 (reserved ids are never reused)", id)
	}
	if got := pendingIDs(t, s2); !idsEqual(got, 1, 2, 4) {
		t.Fatalf("pending %v, want [1 2 4] (gap at 3)", got)
	}
}

// TestTornLastLineSkipped covers a crash in the middle of the event
// write: a trailing fragment without its newline, and an unreadable
// final complete line, are uncommitted writes. Open must succeed, drop
// exactly the torn tail and keep every earlier event.
func TestTornLastLineSkipped(t *testing.T) {
	for name, torn := range map[string][]byte{
		"fragment without newline": []byte(`{"id":3,"height":3,"route":"tx.appl`),
		"unreadable final line":    append([]byte(`{"id":3,"heig`), '\n'),
		"zero-filled tail":         []byte{0, 0, 0, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			s, d := openSpool(t)
			appendEvent(t, s, 1, "block.applied")
			appendEvent(t, s, 2, "block.applied")
			f, err := os.OpenFile(filepath.Join(d, "spool.jsonl"), os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				t.Fatalf("open spool: %v", err)
			}
			if _, err := f.Write(torn); err != nil {
				t.Fatalf("write torn tail: %v", err)
			}
			f.Close()

			s2, err := Open(d)
			if err != nil {
				t.Fatalf("reopen with torn tail: %v", err)
			}
			if got := pendingIDs(t, s2); !idsEqual(got, 1, 2) {
				t.Fatalf("pending after torn tail %v, want [1 2]", got)
			}
			// The torn file is physically rewritten without the tail:
			// exactly the two committed lines, newline-terminated.
			data, err := os.ReadFile(filepath.Join(d, "spool.jsonl"))
			if err != nil {
				t.Fatalf("read spool: %v", err)
			}
			if n := bytes.Count(data, []byte("\n")); n != 2 || !bytes.HasSuffix(data, []byte("\n")) {
				t.Fatalf("rewritten spool holds %d lines: %q", n, data)
			}
			if bytes.Contains(data, []byte(`"id":3`)) {
				t.Fatalf("torn fragment survived the reopen: %q", data)
			}
		})
	}
}

// TestCorruptMiddleLineFails keeps real corruption explicit: damage
// before the final line is never silently skipped.
func TestCorruptMiddleLineFails(t *testing.T) {
	s, d := openSpool(t)
	for h := uint64(1); h <= 3; h++ {
		appendEvent(t, s, h, "block.applied")
	}
	path := filepath.Join(d, "spool.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	lines := bytes.Split(data, []byte("\n"))
	lines[1] = []byte(`{"id":"corrupt`)
	if err := os.WriteFile(path, bytes.Join(lines, []byte("\n")), 0o644); err != nil {
		t.Fatalf("write spool: %v", err)
	}
	if _, err := Open(d); err == nil {
		t.Fatalf("open succeeded despite a corrupt middle line")
	}
}

// TestTornTailOnFirstAppend covers a fresh store whose very first
// append was torn: open must succeed with an empty pending suffix.
func TestTornTailOnFirstAppend(t *testing.T) {
	d := t.TempDir()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d, "spool.jsonl"), []byte(`{"id":1,"hei`), 0o644); err != nil {
		t.Fatalf("write spool: %v", err)
	}
	// A spool file without a meta store: initialize the identity, then
	// repair (the torn event is dropped, the store opens empty).
	s, err := Open(d)
	if err != nil {
		t.Fatalf("open with torn first append: %v", err)
	}
	if got := pendingIDs(t, s); len(got) != 0 {
		t.Fatalf("pending %v, want none", got)
	}
}

// TestOrphanSpoolWithoutMeta covers a spool file holding complete
// events but no meta store: the open adopts the events and never hands
// out their ids again.
func TestOrphanSpoolWithoutMeta(t *testing.T) {
	d := t.TempDir()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line, err := json.Marshal(Event{ID: 2, Height: 5, Route: "tx.applied", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d, "spool.jsonl"), append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write spool: %v", err)
	}
	s, err := Open(d)
	if err != nil {
		t.Fatalf("open orphan spool: %v", err)
	}
	if got := pendingIDs(t, s); !idsEqual(got, 2) {
		t.Fatalf("pending %v, want [2]", got)
	}
	if got := s.Height(); got != 5 {
		t.Fatalf("height %d, want 5", got)
	}
	if id := appendEvent(t, s, 6, "tx.applied"); id != 3 {
		t.Fatalf("append returned id %d, want 3 (orphan ids are never reused)", id)
	}
}
