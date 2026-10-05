// Package trailslog correlates slog records with active Trail spans.
//
// NewHandler wraps an existing slog.Handler and injects the trace and span
// identifiers of the span carried by the logging context into each record
// before delegating. Formatting, level filtering, and output remain the
// wrapped handler's; the decorator only adds correlation fields.
//
// The package writes no Trail records. It reads identifiers from the context
// supplied to each log call, so callers must pass the request or operation
// context through their logging calls for correlation to happen. When no valid
// span is active, records are delegated unchanged.
package trailslog

import (
	"context"
	"log/slog"

	"go.lostcrafters.com/trail"
)

// Default correlation field names. Values are the fixed-width lowercase
// hexadecimal identifier spellings used by Trail's journal.
const (
	DefaultTraceFieldName = "trace_id"
	DefaultSpanFieldName  = "span_id"
)

// An Option configures a correlation handler. Options can only be created by
// this package.
type Option func(*config)

// config collects Option values.
type config struct {
	traceKey string
	spanKey  string
}

// WithFieldNames overrides the correlation field names. An empty argument
// leaves that field's default name in effect.
func WithFieldNames(traceID, spanID string) Option {
	return func(c *config) {
		if traceID != "" {
			c.traceKey = traceID
		}
		if spanID != "" {
			c.spanKey = spanID
		}
	}
}

// handler decorates a base handler with Trail correlation fields.
type handler struct {
	base     slog.Handler
	traceKey string
	spanKey  string
}

// NewHandler returns a slog.Handler that decorates base with Trail
// correlation fields. It panics when base is nil, matching slog.New.
func NewHandler(base slog.Handler, opts ...Option) slog.Handler {
	if base == nil {
		panic("trailslog: nil handler")
	}
	cfg := config{traceKey: DefaultTraceFieldName, spanKey: DefaultSpanFieldName}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return &handler{base: base, traceKey: cfg.traceKey, spanKey: cfg.spanKey}
}

// Enabled reports the wrapped handler's decision unchanged.
func (h *handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

// Handle injects correlation fields when the context carries a valid Trail
// span and delegates to the wrapped handler.
//
// Injected fields are placed before the record's own attributes. A
// record-level attribute that already uses a correlation field name wins: that
// field is not injected. Attributes supplied earlier through WithAttrs cannot
// be inspected here and are not treated as collisions. When the context
// carries no valid span, the record is delegated unchanged.
func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if ctx == nil {
		ctx = context.Background()
	}
	sc := trail.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return h.base.Handle(ctx, r)
	}
	haveTrace, haveSpan := false, false
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case h.traceKey:
			haveTrace = true
		case h.spanKey:
			haveSpan = true
		}
		return !haveTrace || !haveSpan
	})
	injected := make([]slog.Attr, 0, 2)
	if !haveTrace {
		injected = append(injected, slog.String(h.traceKey, sc.TraceID().String()))
	}
	if !haveSpan {
		injected = append(injected, slog.String(h.spanKey, sc.SpanID().String()))
	}
	if len(injected) == 0 {
		return h.base.Handle(ctx, r)
	}
	out := slog.Record{
		Time:    r.Time,
		PC:      r.PC,
		Level:   r.Level,
		Message: r.Message,
	}
	out.AddAttrs(injected...)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(a)
		return true
	})
	return h.base.Handle(ctx, out)
}

// WithAttrs returns a correlation handler around the wrapped handler's
// WithAttrs result; the supplied attributes pass through unchanged.
func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handler{base: h.base.WithAttrs(attrs), traceKey: h.traceKey, spanKey: h.spanKey}
}

// WithGroup returns a correlation handler around the wrapped handler's
// WithGroup result. Consistent with the slog.Handler contract, the group
// qualifies all subsequent record attributes, including the injected
// correlation fields; the correlation fields are not moved out of the group.
func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{base: h.base.WithGroup(name), traceKey: h.traceKey, spanKey: h.spanKey}
}
