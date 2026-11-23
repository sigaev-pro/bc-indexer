// Package eventlog writes the mandatory control events of D52: one JSON
// line per event with a timestamp, the event code, the process instance,
// the network and role, and the applicable identities. Control outcomes
// survive disabled debug; secrets, credentials and full argv are never
// logged.
package eventlog

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Logger is one process control-event writer.
type Logger struct {
	mu       sync.Mutex
	instance string
	network  string
	role     string
	debug    bool
}

// New creates the control logger of one process instance.
func New(instance, network, role, logLevel string) *Logger {
	return &Logger{instance: instance, network: network, role: role, debug: logLevel == "debug"}
}

// Event writes one control event with its fields.
func (l *Logger) Event(code string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := make(map[string]any, len(fields)+5)
	for k, v := range fields {
		entry[k] = v
	}
	entry["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	entry["event_code"] = code
	entry["process_instance"] = l.instance
	entry["network"] = l.network
	entry["role"] = l.role
	line, err := json.Marshal(entry)
	if err != nil {
		line = []byte(fmt.Sprintf(`{"event_code":%q,"error":"marshal"}`, code))
	}
	os.Stdout.Write(append(line, '\n'))
}

// Debug writes a debug-level control event; debug detail disappears at
// higher levels, control outcomes never do.
func (l *Logger) Debug(code string, fields map[string]any) {
	if !l.debug {
		return
	}
	l.Event(code, fields)
}

// Fatal writes a FATAL control event and exits with the project exit
// code of the failure class (LIVE_CONFIG_SPEC, "Валидация и остановка"):
// 2 for configuration, 3 for busy or incompatible state, 4 for an
// unsupported build or source, 5 for an unrecoverable runtime or disk
// error.
func (l *Logger) Fatal(code string, exit int, fields map[string]any) {
	l.Event("FATAL", fields)
	l.Event(code, nil)
	os.Exit(exit)
}

// Errorf renders an error field.
func Errorf(err error) map[string]any {
	return map[string]any{"error": err.Error()}
}
