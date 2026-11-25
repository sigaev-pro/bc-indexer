package rocksdb

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/linxGnu/grocksdb"
)

// The engine writes its settings into an OPTIONS file at every open. This
// is what the linked engine really got, independent of what Open asked for,
// so the tests read it rather than the options object alone.
//
// parseOptions returns, per section of the file, the key/value pairs. A
// section is "[Version]", "[DBOptions]" or "[CFOptions \"default\"]".
func parseOptions(text string) map[string]map[string]string {
	out := map[string]map[string]string{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "["):
			section = strings.TrimSpace(line[1 : len(line)-1])
			out[section] = map[string]string{}
		case section == "":
			// The comment header of the file.
		default:
			key, value, ok := strings.Cut(line, "=")
			if ok {
				out[section][key] = value
			}
		}
	}
	return out
}

func TestEngineSettingsMatchTheBaseline(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openTest(t, dir)

	settings := parseOptions(string(readFile(t, onlyFile(t, dir, "OPTIONS-", ""))))
	want := []struct{ section, key, value string }{
		// The linked engine is the pinned one (COMPONENT_STACK).
		{"Version", "rocksdb_version", "10.4.2"},
		// A record that was applied is never rolled back
		// (research/ROCKSDB_STACK; REC-T11).
		{"DBOptions", "wal_recovery_mode", "kTolerateCorruptedTailRecords"},
		{"DBOptions", "paranoid_checks", "true"},
		// LZ4 for every level, the bottommost one included
		// (research/ROCKSDB_STACK). The codec is stored with each block, so
		// the first durable byte fixes it.
		{`CFOptions "default"`, "compression", "kLZ4Compression"},
		{`CFOptions "default"`, "bottommost_compression", "kLZ4Compression"},
		// A store outside tests keeps the engine's default of 64 MiB. The
		// test hook that shrinks it must not reach a real store.
		{`CFOptions "default"`, "write_buffer_size", "67108864"},
	}
	for _, w := range want {
		got, ok := settings[w.section][w.key]
		if !ok || got != w.value {
			t.Errorf("%s: %s = %q (found %v), want %q", w.section, w.key, got, ok, w.value)
		}
	}
	// The bottommost options apply only when they are enabled.
	if opts := settings[`CFOptions "default"`]["bottommost_compression_opts"]; !strings.Contains(opts, "enabled=true") {
		t.Errorf("bottommost_compression_opts = %q, want enabled=true in it", opts)
	}

	// The options object the store holds says the same.
	s := st.(*store)
	if got := s.opts.GetCompression(); got != grocksdb.LZ4Compression {
		t.Errorf("compression = %v, want LZ4", got)
	}
	if got := s.opts.GetBottommostCompression(); got != grocksdb.LZ4Compression {
		t.Errorf("bottommost compression = %v, want LZ4", got)
	}
	// Reads verify block checksums, so a damaged table file is an error
	// and not other data.
	if !s.ro.VerifyChecksums() {
		t.Error("reads must verify checksums")
	}
}
