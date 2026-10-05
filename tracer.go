package trail

import "context"

// A Tracer creates spans attributed to a named instrumentation scope.
//
// The zero value is a disabled, safe handle: Start returns the caller's
// context and a non-recording span. Tracer values with the same provider and
// scope are equivalent; copies share underlying state.
type Tracer struct {
	provider *Provider
	scope    string
}

// Enabled reports whether the tracer's provider can record spans.
func (t Tracer) Enabled() bool {
	return t.provider.enabled()
}

// Scope returns the tracer's instrumentation scope name. An empty scope means
// unknown; Trail does not infer or default one.
func (t Tracer) Scope() string {
	return t.scope
}

// Start starts a span named name as a child of the span carried by ctx.
//
// When the tracer is disabled, Start returns ctx unchanged and a
// non-recording span without allocating. Parent selection and span admission
// rules are described in docs/design.md.
func (t Tracer) Start(ctx context.Context, name string, opts ...StartOption) (context.Context, Span) {
	if !t.Enabled() {
		return ctx, Span{}
	}
	// Span admission is implemented with the lifecycle milestone; no enabled
	// provider exists before then.
	_, _ = name, opts
	return ctx, Span{}
}
