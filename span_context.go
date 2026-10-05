package trail

// SpanContext carries the identifiers of a span without its mutable state.
//
// The zero value is invalid. Values are immutable; copying a SpanContext
// cannot affect any span.
type SpanContext struct {
	traceID TraceID
	spanID  SpanID
}

// NewSpanContext returns a span context from explicit identifiers. The result
// is valid only when both identifiers are non-zero.
func NewSpanContext(traceID TraceID, spanID SpanID) SpanContext {
	return SpanContext{traceID: traceID, spanID: spanID}
}

// TraceID returns the trace identifier.
func (sc SpanContext) TraceID() TraceID {
	return sc.traceID
}

// SpanID returns the span identifier.
func (sc SpanContext) SpanID() SpanID {
	return sc.spanID
}

// IsValid reports whether both identifiers are non-zero.
func (sc SpanContext) IsValid() bool {
	return sc.traceID.IsValid() && sc.spanID.IsValid()
}

// String returns the identifiers as "traceID:spanID" in lowercase hexadecimal.
func (sc SpanContext) String() string {
	return sc.traceID.String() + ":" + sc.spanID.String()
}
