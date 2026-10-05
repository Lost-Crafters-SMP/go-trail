package trail

// A Span represents a single operation within a trace.
//
// The zero value is a disabled, safe handle: all methods are no-ops except
// the accessors, which return zero values. Copies of a Span share underlying
// state, so copying cannot duplicate End.
type Span struct {
	state *spanState
}

// spanState holds the mutable state of a recording span. A nil state marks a
// non-recording span. Lifecycle state arrives with the milestone that
// implements it.
type spanState struct{}

// End ends the span. It is a no-op for non-recording spans and for spans
// that have already ended; only the first call wins.
func (s Span) End() {
	if s.state != nil {
		s.state.end()
	}
}

// IsRecording reports whether the span still records operations.
func (s Span) IsRecording() bool {
	return s.state != nil && s.state.isRecording()
}

// SpanContext returns the span's identifiers. It returns the zero value for
// non-recording spans.
func (s Span) SpanContext() SpanContext {
	if s.state == nil {
		return SpanContext{}
	}
	return s.state.spanContext()
}

// end ends the span; recording lifecycle arrives with the lifecycle milestone.
func (s *spanState) end() {}

// isRecording reports whether the span still records; see end.
func (s *spanState) isRecording() bool {
	return false
}

// spanContext returns the span's identifiers; see Span.SpanContext.
func (s *spanState) spanContext() SpanContext {
	return SpanContext{}
}
