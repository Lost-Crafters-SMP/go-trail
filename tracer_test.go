package trail

import (
	"context"
	"testing"
)

func TestZeroTracerDisabled(t *testing.T) {
	var tracer Tracer
	if tracer.Enabled() {
		t.Fatal("zero Tracer Enabled() = true, want false")
	}
	if got, want := tracer.Scope(), ""; got != want {
		t.Fatalf("Scope() = %q, want %q", got, want)
	}
}

func TestNilProviderTracerDisabled(t *testing.T) {
	var provider *Provider
	tracer := provider.Tracer("scope")
	if tracer.Enabled() {
		t.Fatal("nil Provider tracer Enabled() = true, want false")
	}
	if got, want := tracer.Scope(), "scope"; got != want {
		t.Fatalf("Scope() = %q, want %q", got, want)
	}
}

func TestZeroProviderDisabled(t *testing.T) {
	var provider Provider
	if provider.enabled() {
		t.Fatal("zero Provider enabled() = true, want false")
	}
	if tracer := provider.Tracer("scope"); tracer.Enabled() {
		t.Fatal("zero Provider tracer Enabled() = true, want false")
	}
}

func TestDisabledStartReturnsOriginalContext(t *testing.T) {
	tracer := Tracer{}
	ctx := context.Background()
	startedCtx, span := tracer.Start(ctx, "operation")
	if startedCtx != ctx {
		t.Fatal("disabled Start returned a different context")
	}
	if span.IsRecording() {
		t.Fatal("disabled Start returned a recording span")
	}
	if sc := span.SpanContext(); sc.IsValid() {
		t.Fatalf("disabled Start span SpanContext() = %v, want zero value", sc)
	}
	span.End()
	span.End()
}

func TestZeroSpanSafe(t *testing.T) {
	var span Span
	if span.IsRecording() {
		t.Fatal("zero Span IsRecording() = true, want false")
	}
	if sc := span.SpanContext(); sc.IsValid() {
		t.Fatalf("zero Span SpanContext() = %v, want zero value", sc)
	}
	span.End()
}

func TestContextWithSpanRoundTrip(t *testing.T) {
	span := Span{}
	ctx := ContextWithSpan(context.Background(), span)
	if got := SpanFromContext(ctx); got != span {
		t.Fatalf("SpanFromContext() = %v, want %v", got, span)
	}
}

func TestSpanFromContextWithoutSpan(t *testing.T) {
	span := SpanFromContext(context.Background())
	if span.state != nil {
		t.Fatal("SpanFromContext(background) returned a span with state")
	}
}

func TestSpanContextFromContext(t *testing.T) {
	if sc := SpanContextFromContext(context.Background()); sc.IsValid() {
		t.Fatalf("SpanContextFromContext(background) = %v, want zero value", sc)
	}
	span := Span{}
	ctx := ContextWithSpan(context.Background(), span)
	if sc := SpanContextFromContext(ctx); sc.IsValid() {
		t.Fatalf("SpanContextFromContext(zero span) = %v, want zero value", sc)
	}
}
