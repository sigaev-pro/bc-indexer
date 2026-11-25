package rocksdb

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Other tests start this test binary again as a helper process. The kind of
// helper comes from an environment variable, so a normal run has nothing to
// do here.
const (
	helperKindEnv = "BCI_ROCKSDB_HELPER_KIND"
	helperDirEnv  = "BCI_ROCKSDB_HELPER_DIR"
	helperSyncEnv = "BCI_ROCKSDB_HELPER_SYNC"
	helperSeedEnv = "BCI_ROCKSDB_HELPER_SEED"

	// helperIdle opens the store and holds it until it is killed.
	helperIdle = "idle"
	// helperCrash commits batches of mixed sizes until it is killed.
	helperCrash = "crash"
	// helperBulk writes single values until it kills itself while the
	// engine flushes ("bulk:flush") or merges table files
	// ("bulk:compaction").
	helperBulk = "bulk"
)

// helperFailures are the first words by which a helper reports that it
// could not go on.
var helperFailures = []string{"OPENFAIL", "APPLYFAIL", "BADSEED", "BADKIND", "BADPROPERTY", "BADKILL"}

func TestHelperProcess(t *testing.T) {
	kind := os.Getenv(helperKindEnv)
	if kind == "" {
		t.Skip("body of the helper processes that other tests start")
	}
	dir := os.Getenv(helperDirEnv)
	synced := os.Getenv(helperSyncEnv) == "1"
	seed, err := strconv.ParseInt(os.Getenv(helperSeedEnv), 10, 64)
	if err != nil {
		fmt.Println("BADSEED")
		os.Exit(2)
	}
	name, target, _ := strings.Cut(kind, ":")
	switch name {
	case helperIdle:
		idleHelper(dir)
	case helperCrash:
		crashHelper(dir, synced, seed)
	case helperBulk:
		bulkHelper(dir, target, seed)
	default:
		fmt.Println("BADKIND")
		os.Exit(2)
	}
	os.Exit(0)
}

// helper is a running helper process. One goroutine reads its output.
type helper struct {
	t          *testing.T
	cmd        *exec.Cmd
	lines      <-chan string
	stderr     *syncBuffer
	stop       chan struct{}
	readerDone chan struct{}
	killOnce   sync.Once
	exitErr    error
}

// startHelper starts a helper of the given kind on dir. The helper is killed
// when the test ends, also when the test fails, so a failing test never waits
// for a helper that would run forever.
func startHelper(t *testing.T, kind, dir string, synced bool, seed int64) *helper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	syncFlag := "0"
	if synced {
		syncFlag = "1"
	}
	cmd.Env = append(os.Environ(),
		helperKindEnv+"="+kind,
		helperDirEnv+"="+dir,
		helperSyncEnv+"="+syncFlag,
		helperSeedEnv+"="+strconv.FormatInt(seed, 10),
	)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// What the helper writes to stderr (a panic, for example) is kept for
	// the failure message.
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string)
	h := &helper{
		t:          t,
		cmd:        cmd,
		lines:      lines,
		stderr:     stderr,
		stop:       make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	go func() {
		defer close(h.readerDone)
		defer close(lines)
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-h.stop:
				return
			}
		}
	}()
	t.Cleanup(h.kill)
	return h
}

// next returns the next line of the helper's output. It fails the test when
// the helper stopped, reported a failure, or printed nothing for timeout.
// The end of the helper's stderr goes into the message.
func (h *helper) next(timeout time.Duration) string {
	h.t.Helper()
	select {
	case line, ok := <-h.lines:
		if !ok {
			h.t.Fatalf("the helper stopped before it was killed. The end of its stderr:\n%s", h.stderr.tail(600))
		}
		h.check(line)
		return line
	case <-time.After(timeout):
		h.t.Fatalf("the helper printed nothing for %v. The end of its stderr:\n%s", timeout, h.stderr.tail(600))
		return ""
	}
}

// untilEnd returns every line the helper printed until it died by itself.
// The test fails after timeout when it does not, or when it reports a
// failure.
func (h *helper) untilEnd(timeout time.Duration) []string {
	h.t.Helper()
	var out []string
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-h.lines:
			if !ok {
				h.kill() // reaps the helper; it has already exited
				if h.exitErr == nil {
					h.t.Fatal("the helper exited by itself instead of being killed")
				}
				return out
			}
			h.check(line)
			out = append(out, line)
		case <-timer.C:
			h.t.Fatalf("the helper did not kill itself within %v. The end of its stderr:\n%s", timeout, h.stderr.tail(600))
			return nil
		}
	}
}

// check fails the test for a line by which the helper reports a failure.
func (h *helper) check(line string) {
	h.t.Helper()
	for _, prefix := range helperFailures {
		if strings.HasPrefix(line, prefix) {
			h.t.Fatalf("the helper failed: %s. The end of its stderr:\n%s", line, h.stderr.tail(600))
		}
	}
}

// kill stops the helper if it still runs and reaps it. It may be called more
// than once. The exit status is kept in exitErr.
func (h *helper) kill() {
	h.killOnce.Do(func() {
		close(h.stop)
		if h.cmd.Process != nil {
			h.cmd.Process.Kill()
		}
		// The reader owns the pipe. It reaches the end of the output when the
		// helper exits, and Wait must not close the pipe before that.
		select {
		case <-h.readerDone:
		case <-time.After(20 * time.Second):
			h.t.Log("the reader of the helper's output did not finish")
		}
		// A killed helper exits with "signal: killed"; that is expected.
		h.exitErr = h.cmd.Wait()
	})
}

// syncBuffer collects the stderr of a helper. os/exec writes it from its own
// goroutine while the test may read it, so the buffer is guarded.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// tail returns the last n bytes written.
func (b *syncBuffer) tail(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := b.buf.String()
	if len(text) > n {
		return text[len(text)-n:]
	}
	return text
}

// idleHelper opens the store, reports it, and holds it until it is killed.
func idleHelper(dir string) {
	st, err := Open(dir, Options{})
	if err != nil {
		fmt.Println("OPENFAIL " + err.Error())
		os.Exit(3)
	}
	if err := st.Put([]byte("owner"), []byte("helper")); err != nil {
		fmt.Println("APPLYFAIL " + err.Error())
		os.Exit(4)
	}
	fmt.Println("OPEN")
	for {
		time.Sleep(time.Hour)
	}
}
