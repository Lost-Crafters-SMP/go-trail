package trail

import (
	"sync/atomic"
)

// A Span represents a single operation within a trace.
//
// The zero value is a disabled, safe handle: all methods are no-ops except
// the accessors, which return zero values. Copies of a Span share underlying
// state, so copying cannot duplicate End.
type Span struct {
	state *spanState
}

// spanState holds the state of a recording span. The ended flag is atomic so
// advisory checks stay race-free; its authoritative transitions happen under
// the provider admission gate.
type spanState struct {
	provider   *providerState
	traceID    TraceID
	spanID     SpanID
	rootSpanID SpanID
	start      timeReading
	trace      *traceState

	ended atomic.Bool
}

// End ends the span, submitting its end record and, when the trace becomes
// quiescent, the trace end record. It is a no-op for non-recording spans and
// for spans that have already ended; only the first call wins. End does not
// end children.
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
// non-recording spans; identifiers remain available after End.
func (s Span) SpanContext() SpanContext {
	if s.state == nil {
		return SpanContext{}
	}
	return s.state.spanContext()
}

// end processes the winning End call: mark ended, submit span_end, release
// the live count, and submit trace_end when the root has ended and no
// admitted span remains live. Output failures release bookkeeping and latch
// the error rather than blocking span state.
func (s *spanState) end() {
	if s.ended.Load() {
		return
	}
	ps := s.provider
	reading := ps.clock.now()
	duration := reading.tick - s.start.tick

	ps.mu.Lock()
	defer ps.mu.Unlock()

	if s.ended.Load() {
		return // a concurrent End won the admission gate
	}
	s.ended.Store(true)

	if err := ps.processor.Process(SpanEnd{
		Seq:        ps.nextSeq(),
		Wall:       reading.wall,
		Elapsed:    reading.tick,
		TraceID:    s.traceID,
		SpanID:     s.spanID,
		RootSpanID: s.rootSpanID,
		Duration:   duration,
	}); err != nil {
		ps.latchError(err)
	}

	ts := s.trace
	ts.live--
	if s.spanID == ts.rootSpanID {
		ts.rootEnded = true
	}
	if ts.rootEnded && ts.live == 0 {
		if err := ps.processor.Process(TraceEnd{
			Seq:        ps.nextSeq(),
			Wall:       reading.wall,
			Elapsed:    reading.tick,
			TraceID:    ts.traceID,
			RootSpanID: ts.rootSpanID,
		}); err != nil {
			ps.latchError(err)
		}
		delete(ps.traces, ts.traceID)
	}
}

// isRecording reports whether the span has not ended.
func (s *spanState) isRecording() bool {
	return !s.ended.Load()
}

// spanContext returns the span's identifiers.
func (s *spanState) spanContext() SpanContext {
	return NewSpanContext(s.traceID, s.spanID)
}
