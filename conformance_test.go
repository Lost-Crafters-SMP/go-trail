package trail

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// processorFactory builds a Processor over a sink. The shared conformance
// suite below runs the same behavioral contract through any factory; a
// future AsyncProcessor must pass it unchanged.
type processorFactory func(Sink) (Processor, error)

func syncProcessorFactory(sink Sink) (Processor, error) {
	processor, err := NewSyncProcessor(sink)
	if err != nil {
		return nil, err
	}
	return processor, nil
}

// concurrencySink records records and tracks whether calls ever overlap.
type concurrencySink struct {
	recordingSink
	inside   atomic.Int32
	overlaps bool
}

func (s *concurrencySink) enter() bool {
	if s.inside.Add(1) != 1 {
		s.overlaps = true
		return false
	}
	return true
}

func (s *concurrencySink) leave() {
	s.inside.Add(-1)
}

func (s *concurrencySink) WriteRecord(record Record) error {
	ok := s.enter()
	defer s.leave()
	if !ok {
		return nil
	}
	return s.recordingSink.WriteRecord(record)
}

func (s *concurrencySink) Flush(ctx context.Context) error {
	ok := s.enter()
	defer s.leave()
	if !ok {
		return nil
	}
	return s.recordingSink.Flush(ctx)
}

func (s *concurrencySink) Shutdown(ctx context.Context) error {
	ok := s.enter()
	defer s.leave()
	if !ok {
		return nil
	}
	return s.recordingSink.Shutdown(ctx)
}

// newConformanceProvider builds an enabled provider from a factory and
// sink. Assertions in the suite observe the sink only after Flush or
// Shutdown barriers.
func newConformanceProvider(t *testing.T, factory processorFactory, sink Sink) *Provider {
	t.Helper()
	processor, err := factory(sink)
	if err != nil {
		t.Fatalf("processor factory unexpected error: %v", err)
	}
	provider, err := NewProvider(processor)
	if err != nil {
		t.Fatalf("NewProvider unexpected error: %v", err)
	}
	return provider
}

func TestSyncProcessorConformance(t *testing.T) {
	runProcessorConformance(t, syncProcessorFactory)
}

func runProcessorConformance(t *testing.T, factory processorFactory) {
	t.Run("loss snapshots precede barriers", func(t *testing.T) {
		sink := &concurrencySink{recordingSink: recordingSink{}}
		provider := newConformanceProvider(t, factory, sink)
		_, span := provider.Tracer("conf").Start(context.Background(), "live", WithAttributes(Attribute{}))
		if err := provider.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
		first := lossSummaries(&sink.recordingSink)
		if len(first) != 1 || first[0].UnendedDroppedAttributes != 1 {
			t.Fatalf("checkpoint not delivered by Flush: %+v", first)
		}
		span.End()
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		all := lossSummaries(&sink.recordingSink)
		if len(all) != 2 || all[1].DroppedAttributes != 1 || all[1].UnendedDroppedAttributes != 0 {
			t.Fatalf("final snapshot not delivered by Shutdown: %+v", all)
		}
	})

	t.Run("ordered journal for ordered input", func(t *testing.T) {
		sink := &concurrencySink{recordingSink: recordingSink{}}
		provider := newConformanceProvider(t, factory, sink)
		tracer := provider.Tracer("conf")

		ctx, root := tracer.Start(context.Background(), "root", WithAttributes(String("a", "1")))
		_, child := tracer.Start(ctx, "child")
		child.SetAttributes(String("b", "2"))
		child.AddEvent("child.event")
		child.End()
		root.SetStatus(StatusError, "failed")
		root.End()

		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown unexpected error: %v", err)
		}
		want := []string{
			"capture_start", "span_start", "span_update", "span_start",
			"span_update", "event", "span_end", "span_update", "span_end", "trace_end", "loss_summary",
		}
		if len(sink.records) != len(want) {
			t.Fatalf("journal has %d records, want %d: %+v", len(sink.records), len(want), sink.records)
		}
		var prev uint64
		for i, record := range sink.records {
			var kind string
			var seq uint64
			switch r := record.(type) {
			case CaptureStart:
				kind, seq = "capture_start", r.Seq
			case SpanStart:
				kind, seq = "span_start", r.Seq
			case SpanUpdate:
				kind, seq = "span_update", r.Seq
			case Event:
				kind, seq = "event", r.Seq
			case SpanEnd:
				kind, seq = "span_end", r.Seq
			case TraceEnd:
				kind, seq = "trace_end", r.Seq
			case LossSummary:
				kind, seq = "loss_summary", r.Seq
			}
			if kind != want[i] {
				t.Fatalf("record %d = %s, want %s", i, kind, want[i])
			}
			if seq <= prev {
				t.Fatalf("record %d seq %d does not increase past %d", i, seq, prev)
			}
			prev = seq
		}
		if sink.overlaps {
			t.Fatal("sink calls overlapped")
		}
	})

	t.Run("duplicate end records one end", func(t *testing.T) {
		sink := &concurrencySink{recordingSink: recordingSink{}}
		provider := newConformanceProvider(t, factory, sink)
		tracer := provider.Tracer("conf")
		_, span := tracer.Start(context.Background(), "op")
		span.End()
		span.End()
		if err := provider.Flush(context.Background()); err != nil {
			t.Fatalf("Flush unexpected error: %v", err)
		}
		count := 0
		for _, r := range sink.records {
			if _, ok := r.(SpanEnd); ok {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("SpanEnd records = %d, want 1", count)
		}
	})

	t.Run("root can end before child", func(t *testing.T) {
		sink := &concurrencySink{recordingSink: recordingSink{}}
		provider := newConformanceProvider(t, factory, sink)
		tracer := provider.Tracer("conf")
		ctx, root := tracer.Start(context.Background(), "root")
		_, child := tracer.Start(ctx, "child")
		root.End()
		child.End()
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown unexpected error: %v", err)
		}
		last := sink.records[len(sink.records)-2]
		if _, ok := last.(TraceEnd); !ok {
			t.Fatalf("last record = %T, want TraceEnd", last)
		}
	})

	t.Run("unfinished span reports incomplete capture", func(t *testing.T) {
		sink := &concurrencySink{recordingSink: recordingSink{}}
		provider := newConformanceProvider(t, factory, sink)
		tracer := provider.Tracer("conf")
		_, span := tracer.Start(context.Background(), "hangs")
		_ = span
		err := provider.Shutdown(context.Background())
		var incomplete *IncompleteError
		if !errors.As(err, &incomplete) || incomplete.UnendedSpans != 1 {
			t.Fatalf("Shutdown error = %v, want IncompleteError with 1 unended span", err)
		}
		started := false
		for _, r := range sink.records {
			if _, ok := r.(SpanStart); ok {
				started = true
			}
		}
		if !started {
			t.Fatal("unfinished span left no start record")
		}
	})

	t.Run("output failure latches and reports", func(t *testing.T) {
		sink := &concurrencySink{recordingSink: recordingSink{}}
		provider := newConformanceProvider(t, factory, sink)
		tracer := provider.Tracer("conf")
		_, span := tracer.Start(context.Background(), "op")
		failure := errors.New("broken output")
		sink.writeErr = failure
		span.SetAttributes(String("k", "v"))
		span.End()
		err := provider.Shutdown(context.Background())
		if !errors.Is(err, failure) {
			t.Fatalf("Shutdown error = %v, want error wrapping %v", err, failure)
		}
	})

	t.Run("flush is a barrier and does not close output", func(t *testing.T) {
		sink := &concurrencySink{recordingSink: recordingSink{}}
		provider := newConformanceProvider(t, factory, sink)
		tracer := provider.Tracer("conf")
		_, span := tracer.Start(context.Background(), "op")
		span.End()
		if err := provider.Flush(context.Background()); err != nil {
			t.Fatalf("Flush unexpected error: %v", err)
		}
		if sink.flushCalls == 0 {
			t.Fatal("Flush did not reach the sink")
		}
		if sink.closeCalls != 0 {
			t.Fatal("Flush closed the sink")
		}
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown unexpected error: %v", err)
		}
		if sink.closeCalls != 1 {
			t.Fatalf("sink close calls = %d, want 1", sink.closeCalls)
		}
	})

	t.Run("shutdown terminal result repeats", func(t *testing.T) {
		sink := &concurrencySink{recordingSink: recordingSink{}}
		provider := newConformanceProvider(t, factory, sink)
		_, span := provider.Tracer("conf").Start(context.Background(), "op")
		span.End()
		first := provider.Shutdown(context.Background())
		second := provider.Shutdown(context.Background())
		if !errors.Is(first, second) {
			t.Fatalf("repeated Shutdown = %v then %v, want identical", first, second)
		}
		if sink.closeCalls != 1 {
			t.Fatalf("sink close calls = %d, want 1", sink.closeCalls)
		}
	})
}
