// Package trail provides lightweight local-first tracing for Go applications.
//
// Trail records application-defined operations as spans in a local journal
// that can be inspected or shared later, without a collector or server. The
// core handles follow a Provider, Tracer, and Span ownership model:
//
//   - A Provider owns tracing state and lifecycle for one capture.
//   - A Tracer creates spans attributed to a named instrumentation scope.
//   - A Span represents a single operation within a trace.
//
// Records flow from spans through a Processor to a Sink; SyncProcessor
// delivers synchronously, and go.lostcrafters.com/trail/file writes the
// versioned Trail JSONL journal.
//
// The zero values of Tracer and Span, and providers without initialized
// state, are disabled, safe handles: recording methods become no-ops and
// disabled span creation returns the caller's context unchanged. Lifecycle
// and format contracts are specified in docs/design.md.
package trail
