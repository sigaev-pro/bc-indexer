package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

const childDiagnosticLimit = 2000

// diagnosticTail bounds retained stderr while the child is still running.
type diagnosticTail struct{ data []byte }

func (w *diagnosticTail) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= childDiagnosticLimit {
		w.data = append(w.data[:0], p[len(p)-childDiagnosticLimit:]...)
	} else {
		w.data = append(w.data, p...)
		if len(w.data) > childDiagnosticLimit {
			w.data = w.data[len(w.data)-childDiagnosticLimit:]
		}
	}
	return n, nil
}

type childJob struct {
	args     []string
	output   string
	identity map[string]any
}

func runChildren(executable string, jobs []childJob, timeout time.Duration) []map[string]any {
	results := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		results = append(results, runChild(executable, job, timeout))
	}
	return results
}

func runChild(executable string, job childJob, timeout time.Duration) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, job.args...)
	var stderr diagnosticTail
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	failure := func(err error) map[string]any {
		result := map[string]any{}
		for key, value := range job.identity {
			result[key] = value
		}
		result["child_error"] = fmt.Sprintf("%v: %s", err, stderr.data)
		return result
	}
	// Run includes Wait: returning a result must not leave an unreaped child.
	if err := cmd.Run(); err != nil {
		return failure(err)
	}
	data, err := os.ReadFile(job.output)
	if err != nil {
		return failure(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return failure(err)
	}
	if result == nil {
		return failure(fmt.Errorf("child result is null"))
	}
	return result
}
