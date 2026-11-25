package rocksdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linxGnu/grocksdb"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// LIVE_CONFIG_SPEC ("Validation and shutdown"): SIGKILL is handled by the
// next start as a crash. These tests kill a helper process that is writing,
// reopen the store, and check what a crash may leave: every unit that had
// returned is whole, and nothing is partly there.
//
// A unit is one commit: a batch, or a single write. What is not covered
// here, and why, is in README.md under "Limits".

const (
	// crashReportAfter is the number of batches the helper reports before
	// the kill: eight of each size of mixedPlan.
	crashReportAfter = 24
	// crashInBatchMax bounds the pause the helper takes inside a batch. A
	// kill that lands in that pause hits a batch in the middle.
	crashInBatchMax = 60 * time.Millisecond

	// bulkValueBytes is the size of one write of bulkPlan. The values do
	// not compress.
	bulkValueBytes = 64 << 10

	// The engine jobs the bulk helper can kill itself during.
	duringFlush      = "flush"
	duringCompaction = "compaction"

	// testBufferBytes is the engine's write buffer in the bulk helper: a
	// flush starts after 64 values and a merge after four flushes. A store
	// outside tests keeps the engine's default of 64 MiB.
	testBufferBytes = 4 << 20
)

// crashPlan is what a helper writes, and how the store is checked after the
// kill. Unit i is one commit.
type crashPlan interface {
	// write commits unit i and returns when the store promised to keep it.
	write(st storage.Storage, i int) error
	// size is the number of keys in unit i.
	size(i int) int
	// check verifies one key/value pair of the reopened store and tells
	// which unit the key belongs to.
	check(key, value []byte) (unit int, ok bool)
}

// crashValue fills n bytes from a sequence that depends on the key alone,
// so the parent can compute it again and compare.
func crashValue(i, j, n int) []byte {
	out := make([]byte, n)
	x := uint64(i)*0x9E3779B97F4A7C15 ^ uint64(j+1)*0xC2B2AE3D27D4EB4F
	var word [8]byte
	for k := 0; k < n; k += 8 {
		x ^= x >> 12
		x ^= x << 25
		x ^= x >> 27
		binary.LittleEndian.PutUint64(word[:], x*2685821657736338717)
		copy(out[k:], word[:])
	}
	return out
}

const batchMark = "batch/"

// mixedPlan alternates small batches, multi-block ones, and large ones. A
// large batch spans many blocks of the log, so a kill inside it can tear it
// anywhere.
type mixedPlan struct{}

func (p mixedPlan) shape(i int) (keys, valueBytes int) {
	switch i % 3 {
	case 0:
		return 4, 215
	case 1:
		return 4, 32 << 10
	default:
		return 8, 64 << 10
	}
}

func (p mixedPlan) write(st storage.Storage, i int) error {
	keys, n := p.shape(i)
	b := storage.NewBatch()
	for j := 0; j < keys; j++ {
		b.Put(fmt.Appendf(nil, "%s%08d/%d", batchMark, i, j), crashValue(i, j, n))
	}
	return st.Apply(b)
}

func (p mixedPlan) size(i int) int {
	keys, _ := p.shape(i)
	return keys
}

func (p mixedPlan) check(key, value []byte) (int, bool) {
	var i, j int
	if _, err := fmt.Sscanf(string(key), batchMark+"%8d/%d", &i, &j); err != nil {
		return 0, false
	}
	keys, n := p.shape(i)
	return i, j < keys && bytes.Equal(value, crashValue(i, j, n))
}

// bulkPlan writes one key of 64 KiB per unit. The keys are spread at random
// over the key space, so the files the engine flushes all overlap. Only then
// does the engine merge them when it compacts. With keys in sequence each
// flushed file covers a range of its own, and the engine just moves the file
// to the next level without rewriting it.
type bulkPlan struct{}

// key spreads unit i over the key space: the first five bytes come from a
// hash of the unit number, and the number itself follows in the clear, so
// the check can tell the unit from the key.
func (p bulkPlan) key(i int) []byte {
	x := uint64(i+1) * 0x9E3779B97F4A7C15
	x ^= x >> 29
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 32
	var head [8]byte
	binary.BigEndian.PutUint64(head[:], x)
	key := make([]byte, 0, 5+1+len(batchMark)+10)
	key = append(key, head[:5]...)
	return append(key, fmt.Sprintf("/%s%010d", batchMark, i)...)
}

func (p bulkPlan) write(st storage.Storage, i int) error {
	return st.Put(p.key(i), crashValue(i, 0, bulkValueBytes))
}

func (bulkPlan) size(int) int { return 1 }

func (p bulkPlan) check(key, value []byte) (int, bool) {
	mark := bytes.Index(key, []byte("/"+batchMark))
	if mark < 0 {
		return 0, false
	}
	i, err := strconv.Atoi(string(key[mark+len(batchMark)+1:]))
	if err != nil {
		return 0, false
	}
	return i, bytes.Equal(value, crashValue(i, 0, bulkValueBytes))
}

// crashHelper commits batches until it is killed. After each batch returns
// it prints the batch number. The pause inside a batch goes through the
// hook of Apply: a store that writes a batch one operation at a time has
// the pause between two of those writes.
func crashHelper(dir string, synced bool, seed int64) {
	opts := Options{sync: syncNever}
	if synced {
		opts = Options{}
	}
	st, err := Open(dir, opts)
	if err != nil {
		fmt.Println("OPENFAIL " + err.Error())
		os.Exit(3)
	}
	pause := rand.New(rand.NewSource(seed))
	betweenOps = func(done int) {
		if done == 1 {
			time.Sleep(time.Duration(pause.Int63n(int64(crashInBatchMax))))
		}
	}
	plan := mixedPlan{}
	for i := 0; ; i++ {
		if err := plan.write(st, i); err != nil {
			fmt.Println("APPLYFAIL " + err.Error())
			os.Exit(4)
		}
		fmt.Println(i)
	}
}

// engineJobs reports how many flushes and compactions the engine runs at
// this moment. The engine answers from the inside, unlike its log, which it
// buffers: a job's start line often reaches the log file only with its end
// line.
func engineJobs(db *grocksdb.DB) (flushes, compactions uint64, ok bool) {
	flushes, ok = db.GetIntProperty("rocksdb.num-running-flushes")
	if !ok {
		return 0, 0, false
	}
	compactions, ok = db.GetIntProperty("rocksdb.num-running-compactions")
	return flushes, compactions, ok
}

// selfKill ends the helper the way a crash does.
func selfKill() {
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		fmt.Println("BADKILL " + err.Error())
		os.Exit(5)
	}
	os.Exit(9) // not reached
}

// bulkHelper writes single values and kills itself while the engine runs
// the job named by during. It asks the engine after every write whether
// that job runs, waits up to 3 ms, asks again, and only then kills itself.
// So the kill lands in a running job for certain. It prints the unit
// numbers and, last, KILLING DURING the job.
func bulkHelper(dir, during string, seed int64) {
	if during != duringFlush && during != duringCompaction {
		fmt.Println("BADKIND")
		os.Exit(2)
	}
	testWriteBufferBytes = testBufferBytes
	st, err := Open(dir, Options{sync: syncNever})
	if err != nil {
		fmt.Println("OPENFAIL " + err.Error())
		os.Exit(3)
	}
	db := st.(*store).db
	plan := bulkPlan{}
	wait := rand.New(rand.NewSource(seed))
	running := func() bool {
		flushes, compactions, ok := engineJobs(db)
		if !ok {
			fmt.Println("BADPROPERTY")
			os.Exit(2)
		}
		if during == duringFlush {
			return flushes > 0
		}
		return compactions > 0
	}
	for i := 0; ; i++ {
		if err := plan.write(st, i); err != nil {
			fmt.Println("APPLYFAIL " + err.Error())
			os.Exit(4)
		}
		fmt.Println(i)
		if running() {
			time.Sleep(time.Duration(wait.Int63n(int64(3 * time.Millisecond))))
			if running() {
				fmt.Println("KILLING DURING " + strings.ToUpper(during))
				selfKill()
			}
		}
	}
}

// readReports reads the helper's output until it reported n units, or until
// it printed a line that starts with stopAt. It returns the number of units
// the helper reported.
func readReports(t *testing.T, h *helper, n int, stopAt string) int {
	t.Helper()
	reported := 0
	for {
		line := h.next(120 * time.Second)
		if stopAt != "" && strings.HasPrefix(line, stopAt) {
			return reported
		}
		if number, err := strconv.Atoi(line); err == nil {
			reported = number + 1
			if stopAt == "" && reported >= n {
				return reported
			}
		}
	}
}

// checkAfterKill reopens the store and applies the rule of a crash: every
// unit that the helper reported is whole with the values that were written,
// and no unit at all is partly there.
func checkAfterKill(t *testing.T, dir string, reported int, plan crashPlan) {
	t.Helper()
	st, err := Open(dir, Options{sync: syncNever})
	if err != nil {
		t.Fatalf("the store does not open after the kill: %v", err)
	}
	counts := map[int]int{}
	highest := -1
	err = st.Iterate(nil, nil, func(k, v []byte) error {
		unit, ok := plan.check(k, v)
		if !ok {
			return fmt.Errorf("key %q is not a key that was written, or holds another value", k)
		}
		counts[unit]++
		highest = max(highest, unit)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= highest; i++ {
		size := plan.size(i)
		if counts[i] != 0 && counts[i] != size {
			t.Fatalf("after the kill unit %d is partly visible: %d of %d keys", i, counts[i], size)
		}
		if i < reported && counts[i] != size {
			t.Fatalf("unit %d returned before the kill and holds %d of %d keys after it", i, counts[i], size)
		}
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d units reported before the kill, %d in the store after it", reported, highest+1)
}

// Each kill is a subtest, so that one kill which fails does not hide what
// the other kills found.
func TestSurvivesKillWhileCommittingBatches(t *testing.T) {
	for _, mode := range []struct {
		name     string
		synced   bool
		kills    int
		maxDelay time.Duration
	}{
		{"synced", true, 4, 160 * time.Millisecond},
		{"unsynced", false, 3, 70 * time.Millisecond},
	} {
		t.Run(mode.name, func(t *testing.T) {
			for kill := 0; kill < mode.kills; kill++ {
				t.Run(fmt.Sprintf("kill %d", kill), func(t *testing.T) {
					seed := time.Now().UnixNano()
					t.Logf("seed %d", seed) // printed with a failure, to run the same kill again
					dir := filepath.Join(t.TempDir(), "wal")
					h := startHelper(t, helperCrash, dir, mode.synced, seed)
					reported := readReports(t, h, crashReportAfter, "")
					// The next batch has just begun. Let the kill fall at a
					// random point of it.
					time.Sleep(time.Duration(rand.New(rand.NewSource(seed ^ 0x5DEECE66D)).Int63n(int64(mode.maxDelay) + 1)))
					h.kill()
					checkAfterKill(t, dir, reported, mixedPlan{})
				})
			}
		})
	}
}

// The same rule while the engine flushes a full write buffer to a table
// file, and while it merges table files. Those jobs run in the background.
// The helper kills itself while the engine reports one as running.
func TestSurvivesKillDuringFlushAndCompaction(t *testing.T) {
	for _, work := range []struct {
		during, name string
		kills        int
	}{
		{duringFlush, "flush", 3},
		{duringCompaction, "compaction", 3},
	} {
		t.Run(work.name, func(t *testing.T) {
			last := "KILLING DURING " + strings.ToUpper(work.during)
			for kill := 0; kill < work.kills; kill++ {
				t.Run(fmt.Sprintf("kill %d", kill), func(t *testing.T) {
					seed := time.Now().UnixNano()
					t.Logf("seed %d", seed)
					dir := filepath.Join(t.TempDir(), "wal")
					h := startHelper(t, helperBulk+":"+work.during, dir, false, seed)
					lines := h.untilEnd(120 * time.Second)
					if len(lines) == 0 || lines[len(lines)-1] != last {
						t.Fatalf("the helper's last line is %q, want %q", lastOf(lines), last)
					}
					reported := 0
					for _, line := range lines {
						if number, err := strconv.Atoi(line); err == nil {
							reported = number + 1
						}
					}
					checkAfterKill(t, dir, reported, bulkPlan{})
				})
			}
		})
	}
}

func lastOf(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}
