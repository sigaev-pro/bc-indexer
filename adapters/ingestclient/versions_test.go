package ingestclient

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The node build is offline on a module cache that holds exactly the
// third-party versions of the accepted BSC go.mod (COMPONENT_STACK, the
// module input of the capture image). A module that compiles into the node
// graph requires none above them, so this table is the ceiling of every
// third-party module this module and its nested module may need. The values
// are those of the accepted BSC go.mod.
var accepted = map[string]string{
	"google.golang.org/grpc":                    "v1.80.0",
	"google.golang.org/protobuf":                "v1.36.11",
	"golang.org/x/net":                          "v0.55.0",
	"golang.org/x/sys":                          "v0.46.0",
	"golang.org/x/text":                         "v0.38.0",
	"google.golang.org/genproto/googleapis/rpc": "v0.0.0-20260401024825-9d38bb4040a9",
	"gopkg.in/yaml.v3":                          "v3.0.1",
}

// repository is the module prefix of the repository-local modules, which
// have no published version and are replaced by directory.
const repository = "github.com/sigaev-pro/bc-indexer/"

func TestModuleRequiresNoVersionAboveTheAcceptedGraph(t *testing.T) {
	// What both modules declare.
	for _, mod := range []string{"go.mod", filepath.Join("ingest", "go.mod")} {
		declared := 0
		for path, version := range requirements(t, mod) {
			if strings.HasPrefix(path, repository) {
				continue
			}
			checkVersion(t, mod+" requires "+path, version)
			declared++
		}
		if declared == 0 {
			t.Fatalf("%s declares no third-party requirement", mod)
		}
	}
	// What the build of this module resolves, offline: the module cache
	// holds exactly what a build needs, as the cache of the node does.
	list := exec.Command("go", "list", "-deps", "-f",
		"{{if .Module}}{{.Module.Path}} {{.Module.Version}}{{end}}", "./...")
	list.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOWORK=off")
	out, err := list.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	resolved := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path, version, _ := strings.Cut(line, " ")
		if strings.HasPrefix(path, repository) {
			continue
		}
		checkVersion(t, "the build resolves "+path, version)
		resolved++
	}
	if resolved == 0 {
		t.Fatal("the build resolves no third-party module; the listing is not what it should be")
	}
}

// requirements returns the requirements of one go.mod by path.
func requirements(t *testing.T, mod string) map[string]string {
	t.Helper()
	out, err := exec.Command("go", "mod", "edit", "-json", mod).Output()
	if err != nil {
		t.Fatalf("go mod edit -json %s: %v", mod, err)
	}
	var parsed struct {
		Require []struct{ Path, Version string }
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	out = nil
	requirements := make(map[string]string, len(parsed.Require))
	for _, requirement := range parsed.Require {
		requirements[requirement.Path] = requirement.Version
	}
	return requirements
}

// checkVersion requires version to be the accepted one or below it.
func checkVersion(t *testing.T, what, version string) {
	t.Helper()
	path := what[strings.LastIndex(what, " ")+1:]
	ceiling, known := accepted[path]
	if !known {
		t.Fatalf("%s (%s) is not in the accepted graph; the node cache does not hold it", what, version)
	}
	if version != ceiling && (strings.Contains(ceiling, "-") || !below(version, ceiling)) {
		t.Fatalf("%s is %s, above the accepted %s", what, version, ceiling)
	}
}

// below reports whether a tagged version sorts below another one.
func below(version, ceiling string) bool {
	parse := func(v string) []int {
		parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
		out := make([]int, len(parts))
		for i, part := range parts {
			out[i], _ = strconv.Atoi(part)
		}
		return out
	}
	low, high := parse(version), parse(ceiling)
	for i := range low {
		if i >= len(high) || low[i] > high[i] {
			return false
		}
		if low[i] < high[i] {
			return true
		}
	}
	return true
}

func TestBelowComparesTaggedVersions(t *testing.T) {
	cases := map[string]bool{
		"v1.80.0 v1.80.0": true, "v1.79.9 v1.80.0": true, "v1.80.1 v1.80.0": false,
		"v2.0.0 v1.80.0": false, "v1.9.0 v1.10.0": true, "v1.80.0.1 v1.80.0": false,
	}
	for pair, want := range cases {
		version, ceiling, _ := strings.Cut(pair, " ")
		if got := below(version, ceiling); got != want {
			t.Fatalf("below(%s, %s) = %v, want %v", version, ceiling, got, want)
		}
	}
}
