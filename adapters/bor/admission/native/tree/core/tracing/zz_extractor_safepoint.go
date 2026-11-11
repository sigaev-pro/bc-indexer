// Extractor patch (E01-02.admission, Bor block): the product VM safe point
// (D98 item 4). The preparation adds this file to the accepted Bor tree and
// the OnExtractorSafePoint field to Hooks; see
// adapters/bor/admission/native/README.md.

package tracing

// ExtractorSafePointHook is the VM safe point. The interpreter calls it on
// the native goroutine between VM steps, before the opcode hook and before
// the opcode changes memory or state, and never inside another hook. Only
// traced execution calls it: the prefetchers clear the tracer. The hook may
// wait (for capture budget); it returns nothing, so it cannot change native
// execution. A capture failure is recorded by the hook's owner and stops the
// observation after native execution.
type ExtractorSafePointHook = func(op byte, scope OpContext)
