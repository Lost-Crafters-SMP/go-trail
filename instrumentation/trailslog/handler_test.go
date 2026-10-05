package trailslog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/instrumentation/trailslog"
)

// recordingSink is a test sink that owns copies of the records it retains.
type recordingSink struct {
	mu      sync.Mutex
	records []trail.Record
}

func (s *recordingSink) WriteRecord(r trail.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, cloneRecord(r))
	return nil
}

func (s *recordingSink) Flush(context.Context) error    { return nil }
func (s *recordingSink) Shutdown(context.Context) error { return nil }

func (s *recordingSink) snapshot() []trail.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records)
}

func cloneRecord(r trail.Record) trail.Record {
	switch rec := r.(type) {
	case trail.SpanUpdate:
		rec.Attributes = cloneAttrs(rec.Attributes)
		return rec
	case trail.Event:
		rec.Attributes = cloneAttrs(rec.Attributes)
		return rec
	default:
		return r
	}
}

func cloneAttrs(attrs []trail.Attribute) []trail.Attribute {
	if attrs == nil {
		return nil
	}
	out := make([]trail.Attribute, len(attrs))
	for i, a := range attrs {
		switch a.Kind() {
		case trail.KindString:
			out[i] = trail.String(a.Key(), a.String())
		case trail.KindBool:
			out[i] = trail.Bool(a.Key(), a.Bool())
		case trail.KindInt64:
			out[i] = trail.Int64(a.Key(), a.Int64())
		case trail.KindUint64:
			out[i] = trail.Uint64(a.Key(), a.Uint64())
		case trail.KindFloat64:
			out[i] = trail.Float64(a.Key(), a.Float64())
		case trail.KindDuration:
			out[i] = trail.Duration(a.Key(), a.Duration())
		case trail.KindStrings:
			out[i] = trail.Strings(a.Key(), slices.Clone(a.Strings()))
		default:
			out[i] = a
		}
	}
	return out
}

func newTestTracer(t *testing.T) (trail.Tracer, *recordingSink) {
	t.Helper()
	sink := &recordingSink{}
	proc, err := trail.NewSyncProcessor(sink)
	if err != nil {
		t.Fatalf("new sync processor: %v", err)
	}
	p, err := trail.NewProvider(proc)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(func() {
		if err := p.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return p.Tracer("test"), sink
}

// lockedWriter serializes concurrent handler output.
type lockedWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func decodeJSONLine(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return m
}

func TestHandleNoSpanDelegatesUnchanged(t *testing.T) {
	var plain, decorated bytes.Buffer
	baseLogger := slog.New(slog.NewJSONHandler(&plain, nil))
	tracedLogger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(&decorated, nil)))

	baseLogger.Info("message", "key", "value")
	tracedLogger.Info("message", "key", "value")

	got := decodeJSONLine(t, strings.TrimSpace(decorated.String()))
	want := decodeJSONLine(t, strings.TrimSpace(plain.String()))
	delete(got, "time")
	delete(want, "time")
	if !equalMaps(got, want) {
		t.Fatalf("decorated output %v != plain %v", got, want)
	}
}

func equalMaps(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || v != w {
			return false
		}
	}
	return true
}

func TestHandleInjectsCorrelation(t *testing.T) {
	tracer, sink := newTestTracer(t)
	var out bytes.Buffer
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(&out, nil)))

	ctx, span := tracer.Start(context.Background(), "operation")
	logger.InfoContext(ctx, "message", "key", "value")
	span.End()

	m := decodeJSONLine(t, strings.TrimSpace(out.String()))
	sc := span.SpanContext()
	if m["trace_id"] != sc.TraceID().String() {
		t.Fatalf("trace_id = %v, want %s", m["trace_id"], sc.TraceID().String())
	}
	if m["span_id"] != sc.SpanID().String() {
		t.Fatalf("span_id = %v, want %s", m["span_id"], sc.SpanID().String())
	}
	if m["key"] != "value" {
		t.Fatalf("user attr = %v", m["key"])
	}
	// Only the capture's own lifecycle records exist; logging added none.
	var kinds []string
	for _, rec := range sink.snapshot() {
		switch rec.(type) {
		case trail.CaptureStart:
			kinds = append(kinds, "capture_start")
		case trail.SpanStart:
			kinds = append(kinds, "span_start")
		case trail.SpanEnd:
			kinds = append(kinds, "span_end")
		case trail.TraceEnd:
			kinds = append(kinds, "trace_end")
		}
	}
	if strings.Join(kinds, ",") != "capture_start,span_start,span_end,trace_end" {
		t.Fatalf("records = %v, want only capture/span lifecycle", kinds)
	}
}

func TestEndedSpanStillCorrelates(t *testing.T) {
	tracer, _ := newTestTracer(t)
	var out bytes.Buffer
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(&out, nil)))

	ctx, span := tracer.Start(context.Background(), "operation")
	span.End()
	logger.WarnContext(ctx, "after end")

	m := decodeJSONLine(t, strings.TrimSpace(out.String()))
	sc := span.SpanContext()
	if m["trace_id"] != sc.TraceID().String() || m["span_id"] != sc.SpanID().String() {
		t.Fatalf("identifiers not injected after End: %v", m)
	}
}

func TestEnabledDelegation(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(&out, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})))
	if logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug enabled through decorator, want delegated filtering")
	}
	if !logger.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("error disabled through decorator")
	}
	logger.DebugContext(context.Background(), "dropped")
	if out.Len() != 0 {
		t.Fatalf("debug record reached output: %q", out.String())
	}
}

func TestWithAttrsPassthrough(t *testing.T) {
	tracer, _ := newTestTracer(t)
	var out bytes.Buffer
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(&out, nil))).With(slog.String("service", "build"))

	ctx, span := tracer.Start(context.Background(), "operation")
	logger.InfoContext(ctx, "message")
	span.End()

	m := decodeJSONLine(t, strings.TrimSpace(out.String()))
	if m["service"] != "build" {
		t.Fatalf("WithAttrs value = %v", m["service"])
	}
	if m["trace_id"] != span.SpanContext().TraceID().String() {
		t.Fatalf("trace_id = %v", m["trace_id"])
	}
}

func TestWithGroupGroupsCorrelation(t *testing.T) {
	tracer, _ := newTestTracer(t)
	var out bytes.Buffer
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(&out, nil))).WithGroup("req")

	ctx, span := tracer.Start(context.Background(), "operation")
	logger.InfoContext(ctx, "message", "code", 200)
	span.End()

	m := decodeJSONLine(t, strings.TrimSpace(out.String()))
	group, ok := m["req"].(map[string]any)
	if !ok {
		t.Fatalf("no req group in %v", m)
	}
	if group["trace_id"] != span.SpanContext().TraceID().String() {
		t.Fatalf("grouped trace_id = %v", group["trace_id"])
	}
	if group["code"] != float64(200) {
		t.Fatalf("grouped user attr = %v", group["code"])
	}
}

func TestCollisionUserWins(t *testing.T) {
	tracer, _ := newTestTracer(t)
	var out bytes.Buffer
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(&out, nil)))

	ctx, span := tracer.Start(context.Background(), "operation")
	logger.InfoContext(ctx, "message", "trace_id", "user-supplied")
	span.End()

	m := decodeJSONLine(t, strings.TrimSpace(out.String()))
	if m["trace_id"] != "user-supplied" {
		t.Fatalf("trace_id = %v, want user-supplied", m["trace_id"])
	}
	if m["span_id"] != span.SpanContext().SpanID().String() {
		t.Fatalf("span_id = %v, want injection to continue for non-colliding keys", m["span_id"])
	}
}

func TestCollisionAllKeysSkipsInjection(t *testing.T) {
	tracer, _ := newTestTracer(t)
	var out bytes.Buffer
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(&out, nil)))

	ctx, span := tracer.Start(context.Background(), "operation")
	logger.InfoContext(ctx, "message", "trace_id", "a", "span_id", "b")
	span.End()

	m := decodeJSONLine(t, strings.TrimSpace(out.String()))
	if m["trace_id"] != "a" || m["span_id"] != "b" {
		t.Fatalf("user values not preserved: %v", m)
	}
}

func TestWithFieldNames(t *testing.T) {
	tracer, _ := newTestTracer(t)
	var out bytes.Buffer
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(&out, nil),
		trailslog.WithFieldNames("trace.identifier", "")))

	ctx, span := tracer.Start(context.Background(), "operation")
	logger.InfoContext(ctx, "message")
	span.End()

	m := decodeJSONLine(t, strings.TrimSpace(out.String()))
	if m["trace.identifier"] != span.SpanContext().TraceID().String() {
		t.Fatalf("custom trace field = %v", m["trace.identifier"])
	}
	if _, present := m["trace_id"]; present {
		t.Fatal("default trace field still injected alongside custom name")
	}
	if m["span_id"] != span.SpanContext().SpanID().String() {
		t.Fatalf("default span field = %v (empty WithFieldNames arg must keep it)", m["span_id"])
	}
}

func TestTextHandlerCompatibility(t *testing.T) {
	tracer, _ := newTestTracer(t)
	var out bytes.Buffer
	logger := slog.New(trailslog.NewHandler(slog.NewTextHandler(&out, nil)))

	ctx, span := tracer.Start(context.Background(), "operation")
	logger.ErrorContext(ctx, "message")
	span.End()

	if !strings.Contains(out.String(), "trace_id="+span.SpanContext().TraceID().String()) {
		t.Fatalf("text output missing correlation: %q", out.String())
	}
}

func TestConcurrentHandle(t *testing.T) {
	tracer, _ := newTestTracer(t)
	out := &lockedWriter{}
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(out, nil)))

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 50 {
				if (g+i)%2 == 0 {
					ctx, span := tracer.Start(context.Background(), "op")
					logger.InfoContext(ctx, "with span")
					span.End()
				} else {
					logger.Info("without span")
				}
			}
		}(g)
	}
	wg.Wait()
	if !strings.Contains(out.String(), "trace_id=") && !strings.Contains(out.String(), "\"trace_id\"") {
		t.Fatal("no correlated records in concurrent output")
	}
}

func TestNewHandlerNilBasePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewHandler(nil) did not panic")
		}
	}()
	trailslog.NewHandler(nil)
}

func BenchmarkSlogNoSpan(b *testing.B) {
	out := &lockedWriter{}
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(out, nil)))
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		logger.InfoContext(ctx, "message", "key", "value")
	}
}

func BenchmarkSlogWithSpan(b *testing.B) {
	tracer, _ := func() (trail.Tracer, *recordingSink) {
		sink := &recordingSink{}
		proc, err := trail.NewSyncProcessor(sink)
		if err != nil {
			b.Fatalf("new sync processor: %v", err)
		}
		p, err := trail.NewProvider(proc)
		if err != nil {
			b.Fatalf("new provider: %v", err)
		}
		return p.Tracer("bench"), sink
	}()
	out := &lockedWriter{}
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(out, nil)))
	ctx, span := tracer.Start(context.Background(), "op")
	defer span.End()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		logger.InfoContext(ctx, "message", "key", "value")
	}
}
