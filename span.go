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
// the provider admission gate, which also serializes mutations against End.
type spanState struct {
	provider   *providerState
	traceID    TraceID
	spanID     SpanID
	rootSpanID SpanID
	start      timeReading
	trace      *traceState

	keys         map[string]struct{}
	droppedAttrs uint64
	droppedEvts  uint64

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

// SetAttributes records bounded attribute updates on the span. Invalid
// attributes, over-limit new keys, and oversized update batches are dropped
// with counters; updates to existing keys remain permitted at the key
// limit. It is a no-op after End.
func (s Span) SetAttributes(attrs ...Attribute) {
	if s.state != nil {
		s.state.setAttributes(attrs)
	}
}

// AddEvent records a timestamped event with optional attributes on the
// span. It is a no-op after End.
func (s Span) AddEvent(name string, opts ...EventOption) {
	if s.state != nil {
		var cfg startConfig
		for _, opt := range opts {
			if opt != nil {
				opt(&cfg)
			}
		}
		s.state.addEvent(name, cfg.attributes, nil)
	}
}

// RecordError records one event named "error" carrying an error.message
// attribute derived from err.Error(), plus any supplied attributes; the
// derived error.message overwrites a supplied key with the same name. It is
// a no-op for nil errors and after End, and it does not set status, walk
// causes, or capture a stack.
func (s Span) RecordError(err error, opts ...EventOption) {
	if err == nil || s.state == nil {
		return
	}
	var cfg startConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	s.state.addEvent("error", cfg.attributes, err)
}

// SetStatus records an explicit status update; the last accepted call wins,
// including a reset to Unset. Description is meaningful only for Error and
// is cleared for other codes. Status is never inferred at End. It is a
// no-op after End.
func (s Span) SetStatus(code StatusCode, description string) {
	if s.state != nil {
		s.state.setStatus(code, description)
	}
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

	if s.ended.Load() || !ps.admitting() {
		return // a concurrent End won the gate, or the provider stopped admitting
	}
	s.ended.Store(true)

	if err := ps.processor.Process(SpanEnd{
		Seq:               ps.nextSeq(),
		Wall:              reading.wall,
		Elapsed:           reading.tick,
		TraceID:           s.traceID,
		SpanID:            s.spanID,
		RootSpanID:        s.rootSpanID,
		Duration:          duration,
		DroppedAttributes: s.droppedAttrs,
		DroppedEvents:     s.droppedEvts,
	}); err != nil {
		ps.latchError(err)
	}

	ts := s.trace
	ts.live--
	ps.liveSpans--
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

// setAttributes resolves a bounded update against the span's keys and
// submits it. Invalid or over-limit pairs count as drops; an oversized
// batch is dropped whole.
func (s *spanState) setAttributes(attrs []Attribute) {
	ps := s.provider
	reading := ps.clock.now()

	ps.mu.Lock()
	defer ps.mu.Unlock()
	if s.ended.Load() || !ps.admitting() {
		return // already ended, or the provider stopped admitting
	}
	newKeys, resolved, dropped := resolveSpanAttributes(s.keys, attrs)
	s.keys = newKeys
	s.droppedAttrs += uint64(dropped)
	if len(resolved) == 0 {
		return
	}
	if oversizedRecord("", resolved) {
		s.droppedAttrs += uint64(len(resolved))
		return
	}
	if err := ps.processor.Process(SpanUpdate{
		Seq:        ps.nextSeq(),
		Wall:       reading.wall,
		Elapsed:    reading.tick,
		TraceID:    s.traceID,
		SpanID:     s.spanID,
		RootSpanID: s.rootSpanID,
		Attributes: resolved,
	}); err != nil {
		ps.latchError(err)
	}
}

// addEvent records one bounded event; err non-nil marks an error event
// whose message is derived outside the admission gate.
func (s *spanState) addEvent(name string, attrs []Attribute, err error) {
	ps := s.provider
	// User formatting happens before the gate: no error/string code runs
	// while internal locks are held.
	if err != nil {
		attrs = append(attrs, String("error.message", err.Error()))
	}
	if name == "" {
		name = unnamedSpanName
	}
	reading := ps.clock.now()

	ps.mu.Lock()
	defer ps.mu.Unlock()
	if s.ended.Load() || !ps.admitting() {
		return // already ended, or the provider stopped admitting
	}
	name = truncateUTF8(name, maxNameBytes)
	resolved, dropped := resolveEventAttributes(attrs)
	s.droppedAttrs += uint64(dropped)
	if oversizedRecord(name, resolved) {
		s.droppedEvts++
		return
	}
	if err := ps.processor.Process(Event{
		Seq:        ps.nextSeq(),
		Wall:       reading.wall,
		Elapsed:    reading.tick,
		TraceID:    s.traceID,
		SpanID:     s.spanID,
		RootSpanID: s.rootSpanID,
		Name:       name,
		Attributes: resolved,
	}); err != nil {
		ps.latchError(err)
	}
}

// setStatus submits an explicit status update with the documented clearing
// rules.
func (s *spanState) setStatus(code StatusCode, description string) {
	if code != StatusUnset && code != StatusOK && code != StatusError {
		return // unknown codes are ignored
	}
	if code != StatusError {
		description = ""
	} else {
		description = truncateUTF8(description, maxStringValueBytes)
	}
	ps := s.provider
	reading := ps.clock.now()

	ps.mu.Lock()
	defer ps.mu.Unlock()
	if s.ended.Load() || !ps.admitting() {
		return // already ended, or the provider stopped admitting
	}
	if err := ps.processor.Process(SpanUpdate{
		Seq:        ps.nextSeq(),
		Wall:       reading.wall,
		Elapsed:    reading.tick,
		TraceID:    s.traceID,
		SpanID:     s.spanID,
		RootSpanID: s.rootSpanID,
		Status:     &SpanStatus{Code: code, Description: description},
	}); err != nil {
		ps.latchError(err)
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
