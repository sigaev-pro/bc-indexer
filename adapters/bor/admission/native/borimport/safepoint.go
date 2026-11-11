package borimport

import (
	"context"
	"sync"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/extractor/capture"
)

// OpcodeTag is the note tag of the opcode notes of SafePoint.
const OpcodeTag uint32 = 1

// SafePoint maps the interpreter safe point (D98 item 4) to core/capture: at
// each safe point it reserves callback credit, waiting for the worker when
// the budget is full; the opcode hook then notes the opcode from that credit
// inside the callback, where nothing may wait. The first capture error stops
// the capture; native execution continues unchanged and the error is
// reported by Err. The raw spool encoder that fills real fields belongs to
// E04-04.import_pressure.
type SafePoint struct {
	Capturer *capture.Capturer
	Ctx      context.Context
	// Credit is reserved at each safe point; it must cover one opcode note.
	Credit int64

	mu       sync.Mutex
	reserved bool
	calls    int
	err      error
}

// Hooks returns the tracer hooks of the safe point.
func (s *SafePoint) Hooks() *tracing.Hooks {
	return &tracing.Hooks{OnExtractorSafePoint: s.Hook, OnOpcode: s.OnOpcode}
}

// Hook is the safe point.
func (s *SafePoint) Hook(op byte, scope tracing.OpContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil || s.reserved {
		return
	}
	if err := s.Capturer.Reserve(s.Ctx, s.Credit); err != nil {
		s.err = err
		return
	}
	s.reserved = true
}

// OnOpcode notes the opcode inside the callback from reserved credit.
func (s *SafePoint) OnOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	if e := s.Capturer.EnterCallback(); e != nil {
		s.err = e
		return
	}
	if s.reserved {
		if e := s.Capturer.Note(OpcodeTag, []byte{op}); e != nil {
			s.err = e
		}
		s.reserved = false
	}
	if e := s.Capturer.ExitCallback(); e != nil && s.err == nil {
		s.err = e
	}
}

// Stats returns the safe point calls, the notes and the first error.
func (s *SafePoint) Stats() (calls, notes int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, int(s.Capturer.Stats().Frames), s.err
}
