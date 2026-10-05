package trail

import "context"

// contextKey is an unexported key type for span propagation through
// context.Context values.
type contextKey struct{}

// spanKey carries Span values through contexts.
var spanKey contextKey

// ContextWithSpan returns a context that carries span so that later Start
// calls can use it as a parent. The parent context is not mutated.
func ContextWithSpan(ctx context.Context, span Span) context.Context {
	return context.WithValue(ctx, spanKey, span)
}

// SpanFromContext returns the span carried by ctx, or a non-recording span
// when ctx carries none. Like ordinary context APIs, it requires a non-nil
// context.
func SpanFromContext(ctx context.Context) Span {
	span, _ := ctx.Value(spanKey).(Span)
	return span
}

// SpanContextFromContext returns the identifiers of the span carried by ctx,
// or the zero value when ctx carries none or the span is non-recording.
func SpanContextFromContext(ctx context.Context) SpanContext {
	return SpanFromContext(ctx).SpanContext()
}
