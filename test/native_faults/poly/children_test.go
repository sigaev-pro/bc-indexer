package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func init() {
	if len(os.Args) != 4 || os.Args[1] != "--fault-child" {
		return
	}
	mode, output := os.Args[2], os.Args[3]
	if err := os.WriteFile(output+".pid", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		panic(err)
	}
	switch mode {
	case "hang":
		fmt.Fprint(os.Stderr, "hanging-child")
		for {
			time.Sleep(time.Hour)
		}
	case "fail":
		os.WriteFile(output, []byte(`{"incorrect_success":true}`), 0600)
		fmt.Fprint(os.Stderr, strings.Repeat("x", 3000)+"last-error")
		os.Exit(7)
	case "missing":
	case "malformed":
		os.WriteFile(output, []byte(`{`), 0600)
	case "null":
		os.WriteFile(output, []byte(`null`), 0600)
	default:
		os.WriteFile(output, []byte(`{"continued":true}`), 0600)
	}
	os.Exit(0)
}

func TestDiagnosticTailBoundDuringWrites(t *testing.T) {
	var tail diagnosticTail
	written := []byte{}
	for _, part := range [][]byte{bytes.Repeat([]byte("a"), 1500), bytes.Repeat([]byte("b"), 1500), bytes.Repeat([]byte("c"), 3000)} {
		n, err := tail.Write(part)
		written = append(written, part...)
		want := written
		if len(want) > childDiagnosticLimit {
			want = want[len(want)-childDiagnosticLimit:]
		}
		if err != nil || n != len(part) || !bytes.Equal(tail.data, want) || len(tail.data) > childDiagnosticLimit {
			t.Fatalf("unbounded or incorrect diagnostic tail: %d bytes, consumed %d", len(tail.data), n)
		}
	}
}

func TestChildFailuresAreReapedAndLaterResultsSurvive(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	jobs := []childJob{}
	for _, mode := range []string{"hang", "fail", "missing", "malformed", "null", "success"} {
		out := t.TempDir() + "/result.json"
		jobs = append(jobs, childJob{args: []string{"--fault-child", mode, out}, output: out, identity: map[string]any{"id": mode}})
	}
	done := make(chan []map[string]any, 1)
	go func() { done <- runChildren(self, jobs, 150*time.Millisecond) }()
	var rows []map[string]any
	select {
	case rows = <-done:
	case <-time.After(3 * time.Second):
		// Clean up a deliberately weakened deadline before failing the test.
		for _, job := range jobs {
			data, _ := os.ReadFile(job.output + ".pid")
			pid, _ := strconv.Atoi(string(data))
			if pid > 0 {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("child deadline did not terminate the hanging process")
	}
	if len(rows) != len(jobs) {
		t.Fatalf("child failure suppressed later results: %d", len(rows))
	}
	for i, job := range jobs {
		data, err := os.ReadFile(job.output + ".pid")
		if err != nil {
			t.Fatalf("child did not finish its startup before parent returned: %v", err)
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil {
			t.Fatal(err)
		}
		var status syscall.WaitStatus
		if _, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); err != syscall.ECHILD {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %s was not reaped: %v", job.identity["id"], err)
		}
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Fatalf("child %s still exists: %v", job.identity["id"], err)
		}
		if i < len(jobs)-1 {
			message, ok := rows[i]["child_error"].(string)
			if !ok || message == "" || rows[i]["id"] != job.identity["id"] {
				t.Fatalf("child error or identity lost: %v", rows[i])
			}
		}
	}
	if !strings.Contains(rows[0]["child_error"].(string), "hanging-child") ||
		!strings.Contains(rows[1]["child_error"].(string), "exit status 7") ||
		!strings.HasSuffix(rows[1]["child_error"].(string), "last-error") {
		t.Fatalf("child diagnostics lost: %v", rows[:2])
	}
	for i, expected := range map[int]string{2: "no such file", 3: "unexpected end", 4: "child result is null"} {
		if !strings.Contains(rows[i]["child_error"].(string), expected) {
			t.Fatalf("child output error cause lost: %v", rows[i])
		}
	}
	encoded, err := json.Marshal(map[string]any{"handoff_runs": rows})
	if err != nil || !bytes.Contains(encoded, []byte(`"continued":true`)) {
		t.Fatalf("parent could not retain a report after child failures: %s %v", encoded, err)
	}
}

func TestChildStartFailureIsRecorded(t *testing.T) {
	row := runChild(t.TempDir()+"/absent", childJob{identity: map[string]any{"id": "start"}}, time.Second)
	if row["child_error"] == nil || row["id"] != "start" {
		t.Fatalf("start failure lost: %v", row)
	}
}
