package trail

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestProviderFlushBarrier(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	span.End()

	if err := provider.Flush(context.Background()); err != nil {
		t.Fatalf("Flush unexpected error: %v", err)
	}
	if sink.flushCalls != 1 {
		t.Fatalf("sink flush calls = %d, want 1", sink.flushCalls)
	}
	if sink.closeCalls != 0 {
		t.Fatalf("sink closed by Flush, want open")
	}
	// Active spans do not make Flush fail.
	_, live := tracer.Start(context.Background(), "live")
	defer live.End()
	if err := provider.Flush(context.Background()); err != nil {
		t.Fatalf("Flush with active spans error = %v, want nil", err)
	}
	live.End()
}

func TestProviderFlushReportsAdmissionLoss(t *testing.T) {
	provider, _, _, _ := newLifecycleProvider(t, randomIDs())
	tracer := provider.Tracer("myapp")

	for range maxActiveSpans {
		_, span := tracer.Start(context.Background(), "hold")
		if !span.IsRecording() {
			t.Fatalf("setup: admission rejected below the active-span cap")
		}
	}
	_, rejected := tracer.Start(context.Background(), "over")
	if rejected.IsRecording() {
		t.Fatal("setup: admission beyond cap accepted")
	}

	err := provider.Flush(context.Background())
	var incomplete *IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Flush error = %v, want IncompleteError", err)
	}
	if incomplete.RejectedStarts != 1 {
		t.Fatalf("RejectedStarts = %d, want 1", incomplete.RejectedStarts)
	}
	if incomplete.UnendedSpans != 0 {
		t.Fatalf("UnendedSpans = %d, want 0 while spans are merely active", incomplete.UnendedSpans)
	}
}

func TestProviderShutdownClean(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	ctx, root := tracer.Start(context.Background(), "root")
	_, child := tracer.Start(ctx, "child")
	child.End()
	root.End()

	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown unexpected error: %v", err)
	}
	if sink.closeCalls != 1 {
		t.Fatalf("sink close calls = %d, want 1", sink.closeCalls)
	}
	// Repeated shutdown returns the same terminal result.
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated Shutdown error = %v, want nil", err)
	}
	if sink.closeCalls != 1 {
		t.Fatalf("repeated Shutdown closed the sink again: %d", sink.closeCalls)
	}
	// Admission and span methods are closed after shutdown.
	_, span := tracer.Start(context.Background(), "late")
	if span.IsRecording() {
		t.Fatal("Start admitted after Shutdown")
	}
	root.SetAttributes(String("k", "v"))
	root.AddEvent("late")
	root.End()
	if got := len(sink.records); got != 6 { // capture, 2 starts, 2 ends, trace_end
		t.Fatalf("journal has %d records after post-shutdown mutations, want 6", got)
	}
}

func TestProviderShutdownReportsUnendedSpans(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	ctx, root := tracer.Start(context.Background(), "root")
	_, child := tracer.Start(ctx, "child")
	child.End()
	// root stays live.

	err := provider.Shutdown(context.Background())
	var incomplete *IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Shutdown error = %v, want IncompleteError", err)
	}
	if incomplete.UnendedSpans != 1 {
		t.Fatalf("UnendedSpans = %d, want 1", incomplete.UnendedSpans)
	}
	// The unended start remains in the journal without a fabricated end.
	ends := spanEnds(sink)
	if len(ends) != 1 || ends[0].SpanID != child.SpanContext().SpanID() {
		t.Fatalf("span ends = %+v, want only the ended child", ends)
	}
	if len(traceEnds(sink)) != 0 {
		t.Fatal("trace completion claimed for an unended root")
	}
	_ = root
}

func TestProviderShutdownCanceledResumes(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	cancel()
	err := provider.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled Shutdown error = %v, want context.DeadlineExceeded", err)
	}
	if sink.closeCalls != 0 {
		t.Fatal("sink closed despite canceled shutdown")
	}
	// Admission closed even though cleanup did not finish; span methods no-op.
	if _, late := tracer.Start(context.Background(), "late"); late.IsRecording() {
		t.Fatal("admission reopened after canceled shutdown")
	}
	span.End()
	if got := len(spanEnds(sink)); got != 0 {
		t.Fatalf("End recorded after canceled shutdown: %d", got)
	}
	// A later shutdown with a fresh context finishes cleanup once.
	err = provider.Shutdown(context.Background())
	var incomplete *IncompleteError
	if !errors.As(err, &incomplete) || incomplete.UnendedSpans != 1 {
		t.Fatalf("resumed Shutdown error = %v, want IncompleteError with 1 unended span", err)
	}
	if sink.closeCalls != 1 {
		t.Fatalf("sink close calls = %d, want 1", sink.closeCalls)
	}
}

func TestProviderShutdownCombinesStickyAndLoss(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t, randomIDs())
	tracer := provider.Tracer("myapp")

	_, ended := tracer.Start(context.Background(), "ended")
	_, unended := tracer.Start(context.Background(), "unended")
	writeErr := errors.New("disk full")
	sink.writeErr = writeErr
	ended.SetAttributes(String("k", "v")) // fails and latches
	ended.End()                           // fails and latches; span bookkeeping released

	err := provider.Shutdown(context.Background())
	if !errors.Is(err, writeErr) {
		t.Fatalf("Shutdown error = %v, want combined error wrapping %v", err, writeErr)
	}
	var incomplete *IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Shutdown error = %v, want joined IncompleteError", err)
	}
	if incomplete.UnendedSpans != 1 {
		t.Fatalf("UnendedSpans = %d, want 1", incomplete.UnendedSpans)
	}
	_ = unended
}

func TestDisabledProviderFlushShutdownNoOps(t *testing.T) {
	var provider *Provider
	if err := provider.Flush(context.Background()); err != nil {
		t.Fatalf("nil Provider Flush error = %v", err)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Provider Shutdown error = %v", err)
	}
	var zero Provider
	if err := zero.Flush(context.Background()); err != nil {
		t.Fatalf("zero Provider Flush error = %v", err)
	}
	if err := zero.Shutdown(context.Background()); err != nil {
		t.Fatalf("zero Provider Shutdown error = %v", err)
	}
}

func TestConcurrentWorkFlushAndShutdown(t *testing.T) {
	provider, _, _, _ := newLifecycleProvider(t, randomIDs())
	tracer := provider.Tracer("myapp")

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, span := tracer.Start(context.Background(), "op")
				span.SetAttributes(String("k", "v"))
				span.AddEvent("event")
				span.End()
				if err := provider.Flush(context.Background()); err != nil {
					var incomplete *IncompleteError
					if !errors.As(err, &incomplete) {
						t.Errorf("concurrent Flush error = %v, want nil or IncompleteError", err)
						return
					}
				}
			}
		}()
	}
	// Let work interleave with flushes, then stop and shut down.
	time.Sleep(5 * time.Millisecond)
	close(stop)
	wg.Wait()
	if err := provider.Shutdown(context.Background()); err != nil {
		var incomplete *IncompleteError
		if !errors.As(err, &incomplete) {
			t.Fatalf("Shutdown error = %v, want nil or IncompleteError", err)
		}
	}
}
