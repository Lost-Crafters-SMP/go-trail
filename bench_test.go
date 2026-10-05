package trail

import (
	"context"
	"testing"
)

func BenchmarkDisabledStartEnd(b *testing.B) {
	var tracer Tracer
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, span := tracer.Start(ctx, "operation")
		span.End()
	}
}

func BenchmarkDisabledGlobalStartEnd(b *testing.B) {
	SetDefaultProvider(nil)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, span := GetTracer("myapp").Start(ctx, "operation")
		span.End()
	}
}

func BenchmarkDisabledSpanMethods(b *testing.B) {
	var span Span
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		span.SetAttributes(String("key", "value"), Int("count", 1))
		span.AddEvent("event")
		span.SetStatus(StatusError, "failed")
		span.RecordError(errBenchmark)
		span.End()
	}
}

// BenchmarkDisabledOptionConstruction documents the small constant cost of
// building option values on disabled calls; the option closure and argument
// slices are inherent to the option pattern, and copying is still deferred
// until recording.
func BenchmarkDisabledOptionConstruction(b *testing.B) {
	var span Span
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		span.AddEvent("event", WithAttributes(Bool("ok", true)))
	}
}

var errBenchmark = &benchmarkError{}

type benchmarkError struct{}

func (*benchmarkError) Error() string { return "benchmark" }

func BenchmarkSpanContextFromContext(b *testing.B) {
	ctx := ContextWithSpan(context.Background(), Span{})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if sc := SpanContextFromContext(ctx); sc.IsValid() {
			b.Fatal("zero span context is valid")
		}
	}
}
