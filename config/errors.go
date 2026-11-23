package config

import (
	"errors"
	"fmt"
	"strings"
)

// ExitCode is the project exit-code class of LIVE_CONFIG_SPEC
// ("Валидация и остановка"): 0 success, 2 CLI/YAML/native configuration,
// 3 busy or incompatible state, 4 unsupported build or incompatible
// source, 5 unrecoverable runtime or disk error. The original diagnostic
// stays wrapped in the reason; secrets and full native argv dumps are
// never part of an error.
type ExitCode int

// The exit-code classes.
const (
	ExitOK          ExitCode = 0
	ExitConfig      ExitCode = 2
	ExitState       ExitCode = 3
	ExitUnsupported ExitCode = 4
	ExitFatal       ExitCode = 5
)

// Error is one located configuration error: a field path, the YAML line
// when known, the exit-code class and the reason. A failed check reports
// every collected error, and no error partially applies settings.
type Error struct {
	Field  string
	Line   int
	Code   ExitCode
	Reason string
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("config: %s (line %d): %s", e.Field, e.Line, e.Reason)
	}
	return fmt.Sprintf("config: %s: %s", e.Field, e.Reason)
}

// Exit maps an error to its exit-code class. Errors without a location
// keep the configuration class: a check failure is never a success.
func Exit(err error) ExitCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ExitConfig
}

// ErrorList joins several located errors. Check results report all of
// them instead of stopping at the first.
type ErrorList []*Error

func (l ErrorList) Error() string {
	parts := make([]string, 0, len(l))
	for _, e := range l {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}

// errf appends one located error to the list.
func errf(errs *ErrorList, code ExitCode, field, format string, args ...any) {
	*errs = append(*errs, &Error{Field: field, Code: code, Reason: fmt.Sprintf(format, args...)})
}

// firstError converts a collected list to a single error value.
func firstError(errs ErrorList) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	return errs
}

// quote renders a scalar for an error reason.
func quote(s string) string { return "'" + s + "'" }
