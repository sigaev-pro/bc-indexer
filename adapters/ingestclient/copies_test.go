package ingestclient

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The two generated files of the nested module ingest are byte-identical
// copies of the files of the gateway ingest module: the client and the
// gateway share one wire definition, and the module never imports the
// gateway's. A change of the gateway's files is a change to copy here, in
// the same commit.
func TestGeneratedFilesAreByteIdenticalToTheGateway(t *testing.T) {
	for _, name := range []string{"ingest.pb.go", "ingest_grpc.pb.go"} {
		copy, err := os.ReadFile(filepath.Join("ingest", name))
		if err != nil {
			t.Fatalf("the copy of %s: %v", name, err)
		}
		origin, err := os.ReadFile(filepath.Join("..", "..", "gateway", "ingest", name))
		if err != nil {
			t.Fatalf("the gateway's %s: %v", name, err)
		}
		if !bytes.Equal(copy, origin) {
			t.Fatalf("%s differs from gateway/ingest/%s: copy the generated file again, byte for byte", name, name)
		}
	}
	// The copies are the generated code and nothing else: the package
	// holds exactly these two files.
	entries, err := os.ReadDir("ingest")
	if err != nil {
		t.Fatal(err)
	}
	var goFiles []string
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".go" {
			goFiles = append(goFiles, entry.Name())
		}
	}
	if len(goFiles) != 2 {
		t.Fatalf("package ingest holds %v, want only the two generated files", goFiles)
	}
}
