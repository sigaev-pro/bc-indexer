package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStrictNativeJSON(t *testing.T) {
	for _, data := range []string{`{"stream":"FENCE","seq":1,"seq":2}`, `{"x":{"a":1,"a":2}}`, `{} {}`, `[NaN]`} {
		var value any
		if strictJSON([]byte(data), &value) == nil {
			t.Fatalf("accepted invalid native JSON: %s", data)
		}
	}
	var value map[string]any
	if err := strictJSON([]byte("{\"stream\":\"FENCE\",\"seq\":1}\r\n"), &value); err != nil {
		t.Fatal(err)
	}
	if value["seq"] != json.Number("1") {
		t.Fatal("native number was coerced")
	}
}
func TestBinaryPayloadAndNativeMemberIdentity(t *testing.T) {
	expected := []byte{0, 13, 10, 255, 128, 0, 0, 0, 1, 17, 54, 91, 128, 165, 202, 239}
	if !bytes.Equal(payload(1, 16), expected) {
		t.Fatal("binary oracle changed")
	}
	if nativeMember("fence-1") != "W3vLCXAo" {
		t.Fatal("native member derivation changed")
	}
}
func TestNativeResponsePreservesBinaryFraming(t *testing.T) {
	for _, data := range [][]byte{[]byte("MSG _INBOX.X 1 5\r\n\x00\r\n\xff\x80\r\n"), []byte("MSG _INBOX.wrong 1 0\r\n\r\n"), []byte("MSG _INBOX.X 1 05\r\n12345\r\n")} {
		left, right := net.Pipe()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		done := make(chan struct{})
		go func() { defer close(done); defer right.Close(); right.Write(data) }()
		w := wire{conn: left, reader: bufio.NewReader(left), ctx: ctx}
		raw, body, err := w.response("_INBOX.X")
		if bytes.Contains(data, []byte("\x00")) {
			if err != nil || !bytes.Equal(raw, data) || !bytes.Equal(body, []byte{0, 13, 10, 255, 128}) {
				t.Fatal("binary response changed", err)
			}
		} else if err == nil {
			t.Fatal("accepted wrong correlation or size")
		}
		left.Close()
		cancel()
		<-done
	}
}
func TestRawWireRetainsAvailablePartialBytes(t *testing.T) {
	dir := t.TempDir()
	input, _ := os.Create(filepath.Join(dir, "in"))
	output, _ := os.Create(filepath.Join(dir, "out"))
	left, right := net.Pipe()
	c := &auditedConn{Conn: left, input: &boundedLog{file: input}, output: &boundedLog{file: output}}
	done := make(chan struct{})
	go func() { defer close(done); right.Write([]byte("MSG _INBOX.X 1 9\r\npart")); right.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w := wire{conn: c, reader: bufio.NewReader(c), ctx: ctx}
	if _, _, err := w.response("_INBOX.X"); err == nil {
		t.Fatal("partial body accepted")
	}
	c.Close()
	<-done
	data, err := os.ReadFile(filepath.Join(dir, "in"))
	if err != nil || !bytes.HasSuffix(data, []byte("part")) {
		t.Fatal("available partial bytes lost", err)
	}
}
func TestOutputOverflowRetainsOnlyBoundedPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	log := boundedLog{file: file}
	data := bytes.Repeat([]byte{255}, (2<<20)+17)
	n, err := log.Write(data)
	file.Close()
	stat, statErr := os.Stat(path)
	if err == nil || statErr != nil || n != 2<<20 || stat.Size() != 2<<20 || log.observed != int64(len(data)) {
		t.Fatal("overflow was accepted or prefix/count lost")
	}
}
func TestSnapshotRejectsLinksAndExpiredBudget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "data"), []byte("native"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if snapshotStore(ctx, source, filepath.Join(root, "expired")) == nil {
		t.Fatal("expired snapshot accepted")
	}
	if err := os.Symlink("data", filepath.Join(source, "alias")); err != nil {
		t.Fatal(err)
	}
	if snapshotStore(context.Background(), source, filepath.Join(root, "linked")) == nil {
		t.Fatal("store symlink accepted")
	}
}
func TestReadAndPublicationDeadlineAreFinite(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	defer left.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := wire{conn: left, reader: bufio.NewReader(left), ctx: ctx}
	if w.send([]byte("PING\r\n")) == nil {
		t.Fatal("dispatch after cancellation")
	}
	if _, err := w.line(); err == nil {
		t.Fatal("read after cancellation")
	}
	file, err := os.CreateTemp(t.TempDir(), "small")
	if err != nil {
		t.Fatal(err)
	}
	file.Write([]byte("two"))
	file.Close()
	if _, err = regularBytes(file.Name(), 1); err == nil {
		t.Fatal("oversized file read")
	}
}
