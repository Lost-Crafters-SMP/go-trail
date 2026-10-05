package trail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stalledAsyncSink blocks selected calls using explicit handshakes, never sleeps.
// Only the processor writer accesses recordingSink until a barrier has returned.
type stalledAsyncSink struct {
	recordingSink
	entered      chan struct{}
	release      chan struct{}
	blockType    func(Record) bool
	flushEntered chan struct{}
	flushRelease chan struct{}
	failure      error
	once         sync.Once
	inside       atomic.Int32
	overlaps     atomic.Bool
}

func (s *stalledAsyncSink) enter() {
	if s.inside.Add(1) != 1 {
		s.overlaps.Store(true)
	}
}

func (s *stalledAsyncSink) WriteRecord(r Record) error {
	s.enter()
	defer s.inside.Add(-1)
	if s.blockType != nil && s.blockType(r) {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	if s.failure != nil {
		return s.failure
	}
	return s.recordingSink.WriteRecord(r)
}

func (s *stalledAsyncSink) Flush(ctx context.Context) error {
	s.enter()
	defer s.inside.Add(-1)
	if s.flushEntered != nil {
		close(s.flushEntered)
		<-s.flushRelease
		s.flushEntered = nil
	}
	return s.recordingSink.Flush(ctx)
}

func (s *stalledAsyncSink) Shutdown(ctx context.Context) error {
	s.enter()
	defer s.inside.Add(-1)
	return s.recordingSink.Shutdown(ctx)
}

func blockedCaptureSink() *stalledAsyncSink {
	return &stalledAsyncSink{
		entered: make(chan struct{}), release: make(chan struct{}),
		blockType: func(r Record) bool { _, ok := r.(CaptureStart); return ok },
	}
}

func awaitAsync(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("async handshake timed out")
	}
}

func newAsyncTestProvider(t *testing.T, sink Sink, options []AsyncOption, providerOptions ...ProviderOption) (*Provider, *AsyncProcessor) {
	t.Helper()
	ap, err := NewAsyncProcessor(sink, options...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewProvider(ap, providerOptions...)
	if err != nil {
		_ = ap.Shutdown(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stalled, ok := sink.(*stalledAsyncSink); ok {
			for _, release := range []chan struct{}{stalled.release, stalled.flushRelease} {
				if release != nil {
					select {
					case <-release:
					default:
						close(release)
					}
				}
			}
		}
		_ = p.Shutdown(context.Background())
	})
	return p, ap
}

func TestAsyncCapacityReservationsAndLoss(t *testing.T) {
	for _, budget := range []string{"records", "bytes"} {
		t.Run(budget, func(t *testing.T) {
			sink := blockedCaptureSink()
			var opts []AsyncOption
			if budget == "records" {
				opts = []AsyncOption{WithMaxQueuedRecords(6)}
			} else {
				opts = []AsyncOption{WithMaxQueuedBytes(2048)}
			}
			var notified atomic.Int32
			p, ap := newAsyncTestProvider(t, sink, opts, WithErrorHandler(func(error) { notified.Add(1) }))
			awaitAsync(t, sink.entered)
			_, span := p.Tracer("async").Start(context.Background(), "accepted")
			if !span.IsRecording() {
				t.Fatal("initial root rejected")
			}
			_, rejected := p.Tracer("async").Start(context.Background(), "rejected")
			if rejected.IsRecording() {
				t.Fatal("reservation overcommit")
			}
			// For the byte budget consume all remaining ordinary space.
			if budget == "bytes" {
				span.AddEvent("fill", WithAttributes(String("k", strings.Repeat("v", 100))))
			}
			span.AddEvent("dropped")
			span.SetAttributes(String("a", "b"), Int("n", 1))
			span.SetStatus(StatusError, "dropped status")
			span.End() // must never wait for writer progress or free space
			_, second := p.Tracer("async").Start(context.Background(), "also rejected")
			if second.IsRecording() {
				t.Fatal("runtime End released undelivered credits")
			}
			ap.mu.Lock()
			if ap.usedRecords > ap.recordLimit || ap.usedBytes > ap.byteLimit {
				t.Error("budget overcommit")
			}
			ap.mu.Unlock()
			close(sink.release)
			var loss *IncompleteError
			if err := p.Flush(context.Background()); !errors.As(err, &loss) || loss.RejectedStarts != 2 {
				t.Fatalf("Flush = %v", err)
			}
			end := spanEnds(&sink.recordingSink)[0]
			if end.DroppedEvents != 1 || end.DroppedAttributes != 2 || end.DroppedStatusUpdates != 1 {
				t.Fatalf("drop counters = %+v", end)
			}
			summary := lossSummaries(&sink.recordingSink)[0]
			if summary.DroppedEvents != 1 || summary.DroppedAttributes != 2 || summary.DroppedStatusUpdates != 1 || summary.UnendedDroppedStatusUpdates != 0 {
				t.Fatalf("summary = %+v", summary)
			}
			if len(traceEnds(&sink.recordingSink)) != 1 || notified.Load() != 0 {
				t.Fatal("lifecycle lost or overflow notified as terminal")
			}
			_ = p.Shutdown(context.Background())
			awaitAsync(t, ap.done)
			if sink.overlaps.Load() {
				t.Fatal("overlapping sink calls")
			}
		})
	}
}

func TestAsyncQueuedPayloadOwnership(t *testing.T) {
	sink := blockedCaptureSink()
	p, ap := newAsyncTestProvider(t, sink, nil)
	awaitAsync(t, sink.entered)
	ctx, span := p.Tracer("async").Start(context.Background(), "root")
	_ = ctx
	ids := span.SpanContext()
	values := []string{"original"}
	attrs := []Attribute{Strings("array", values)}
	status := &SpanStatus{Code: StatusError, Description: "original"}
	p.state.mu.Lock()
	err := ap.Process(SpanUpdate{Seq: p.state.nextSeq(), TraceID: ids.TraceID(), SpanID: ids.SpanID(), RootSpanID: ids.SpanID(), Attributes: attrs, Status: status})
	p.state.unlockAndReport()
	if err != nil {
		t.Fatal(err)
	}
	eventAttrs := []Attribute{Strings("event-array", values)}
	p.state.mu.Lock()
	err = ap.Process(Event{Seq: p.state.nextSeq(), TraceID: ids.TraceID(), SpanID: ids.SpanID(), RootSpanID: ids.SpanID(), Name: "owned", Attributes: eventAttrs})
	p.state.unlockAndReport()
	if err != nil {
		t.Fatal(err)
	}
	values[0] = "changed"
	attrs[0] = String("changed", "changed")
	status.Description = "changed"
	eventAttrs[0] = String("changed", "changed")
	span.End()
	close(sink.release)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	updates := spanUpdates(&sink.recordingSink)
	if len(updates) != 1 || updates[0].Attributes[0].Strings()[0] != "original" || updates[0].Status.Description != "original" {
		t.Fatalf("queued payload changed: %+v", updates)
	}
	if evts := events(&sink.recordingSink); len(evts) != 1 || evts[0].Attributes[0].Strings()[0] != "original" {
		t.Fatalf("queued event changed: %+v", evts)
	}
}

func TestAsyncCheckpointLaneBoundedAndSecondFlushCancels(t *testing.T) {
	sink := blockedCaptureSink()
	p, ap := newAsyncTestProvider(t, sink, []AsyncOption{WithMaxQueuedRecords(6)})
	awaitAsync(t, sink.entered)
	_, span := p.Tracer("async").Start(context.Background(), "live")
	span.SetStatus(StatusOK, "") // normal space exhausted
	for range 100 {
		if err := ap.Process(LossSummary{Seq: 1000}); !errors.Is(err, ErrQueueFull) {
			t.Fatalf("direct control submission bypassed budget: %v", err)
		}
	}
	// Insert the first pending provider checkpoint deterministically using the
	// same permit/gate transaction used by Flush. The writer is stalled.
	if err := ap.acquireControl(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.state.mu.Lock()
	first := p.state.lossSnapshot()
	b := ap.checkpoint(context.Background(), &first, false)
	p.state.unlockAndReport()
	ap.mu.Lock()
	queued := ap.count
	used := ap.usedRecords
	ap.mu.Unlock()
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- p.Flush(ctx) }()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("second Flush = %v", err)
		}
	}
	ap.mu.Lock()
	if ap.count != queued || ap.usedRecords != used || len(ap.control) != 0 {
		t.Error("pending/canceled Flush grew checkpoint lane")
	}
	ap.mu.Unlock()
	span.SetStatus(StatusError, "second drop") // ordinary operation still returns
	span.End()
	close(sink.release)
	if err := waitAsyncBarrier(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := p.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	all := lossSummaries(&sink.recordingSink)
	if len(all) != 2 || all[0].UnendedDroppedStatusUpdates != 1 || all[1].DroppedStatusUpdates != 2 || all[1].UnendedDroppedStatusUpdates != 0 {
		t.Fatalf("replacement checkpoints = %+v", all)
	}
}

func TestAsyncFlushHighWaterAndConcurrentProducers(t *testing.T) {
	flushEntered := make(chan struct{})
	flushRelease := make(chan struct{})
	lateEntered := make(chan struct{})
	lateRelease := make(chan struct{})
	sink := &stalledAsyncSink{
		flushEntered: flushEntered, flushRelease: flushRelease,
		entered: lateEntered, release: lateRelease,
		blockType: func(r Record) bool { e, ok := r.(Event); return ok && e.Name == "later" },
	}
	p, _ := newAsyncTestProvider(t, sink, nil)
	_, span := p.Tracer("async").Start(context.Background(), "live")
	result := make(chan error, 1)
	go func() { result <- p.Flush(context.Background()) }()
	awaitAsync(t, flushEntered)
	producerDone := make(chan struct{})
	go func() { span.AddEvent("later"); close(producerDone) }()
	awaitAsync(t, producerDone) // must return while Sink.Flush is stalled
	close(flushRelease)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	awaitAsync(t, lateEntered) // Flush did not wait for this later write
	span.End()
	close(lateRelease)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncCanceledShutdownDrainsAndRetries(t *testing.T) {
	sink := blockedCaptureSink()
	p, ap := newAsyncTestProvider(t, sink, nil)
	awaitAsync(t, sink.entered)
	_, span := p.Tracer("async").Start(context.Background(), "work")
	span.End()
	ctx, cancel := context.WithCancel(context.Background())
	if err := ap.acquireControl(ctx); err != nil {
		t.Fatal(err)
	}
	p.state.mu.Lock()
	p.state.stage = stageClosing
	final := p.state.lossSnapshot()
	p.state.finalSummaryAttempted = true
	ap.stopAdmission()
	b := ap.checkpoint(ctx, &final, true)
	p.state.unlockAndReport()
	cancel()
	if err := waitAsyncBarrier(ctx, b); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_, late := p.Tracer("async").Start(context.Background(), "late")
	if late.IsRecording() {
		t.Fatal("admission reopened")
	}
	close(sink.release)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(lossSummaries(&sink.recordingSink)) != 1 || len(spanEnds(&sink.recordingSink)) != 1 || sink.closeCalls != 1 {
		t.Fatal("canceled shutdown discarded or duplicated accepted work")
	}
	awaitAsync(t, ap.done)
}

func TestAsyncBackgroundFailureCallbackReentry(t *testing.T) {
	sink := blockedCaptureSink()
	sink.failure = errors.New("disk failed")
	var p *Provider
	var notifications atomic.Int32
	callbackDone := make(chan struct{})
	p, ap := newAsyncTestProvider(t, sink, nil, WithErrorHandler(func(err error) {
		notifications.Add(1)
		if !errors.Is(err, sink.failure) {
			t.Errorf("callback = %v", err)
		}
		if err := p.Shutdown(context.Background()); !errors.Is(err, sink.failure) {
			t.Errorf("reentrant Shutdown = %v", err)
		}
		close(callbackDone)
	}))
	awaitAsync(t, sink.entered)
	_, span := p.Tracer("async").Start(context.Background(), "queued")
	span.End()
	close(sink.release)
	awaitAsync(t, callbackDone)
	if err := p.Flush(context.Background()); !errors.Is(err, sink.failure) {
		t.Fatal(err)
	}
	if notifications.Load() != 1 || sink.closeCalls != 1 {
		t.Fatal("failure storm or missed cleanup")
	}
	awaitAsync(t, ap.done)
}

func TestAsyncConcurrentShutdownAndNoWorkerLeak(t *testing.T) {
	for range 20 {
		sink := &concurrencySink{}
		p, ap := newAsyncTestProvider(t, sink, nil)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				_, span := p.Tracer("async").Start(context.Background(), "work")
				span.AddEvent("event")
				span.End()
			})
		}
		wg.Wait()
		for range 4 {
			wg.Go(func() {
				if err := p.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		awaitAsync(t, ap.done)
		if sink.overlaps || sink.closeCalls != 1 {
			t.Fatal("concurrent cleanup overlapped or duplicated")
		}
	}
}

func TestAsyncInvalidOptionsLeaveOwnership(t *testing.T) {
	sink := &recordingSink{}
	for _, opts := range [][]AsyncOption{{WithMaxQueuedRecords(5)}, {WithMaxQueuedBytes(2047)}} {
		if p, err := NewAsyncProcessor(sink, opts...); err == nil || p != nil {
			t.Fatal("invalid options accepted")
		}
	}
	if _, err := NewAsyncProcessor(nil); err == nil {
		t.Fatal("nil sink accepted")
	}
	var typedNil *recordingSink
	if _, err := NewAsyncProcessor(typedNil); err == nil {
		t.Fatal("typed nil sink accepted")
	}
	if sink.closeCalls != 0 {
		t.Fatal("constructor failure took ownership")
	}
}

func TestAsyncDirectProcessorTerminalContract(t *testing.T) {
	sink := &recordingSink{}
	p, err := NewAsyncProcessor(sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Process(CaptureStart{Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if err := p.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 1 {
		t.Fatal("direct Flush did not deliver high-water mark")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.Process(CaptureStart{Seq: 2}); !errors.Is(err, ErrProcessorShutdown) {
		t.Fatal(err)
	}
	if err := p.Flush(context.Background()); !errors.Is(err, ErrProcessorShutdown) {
		t.Fatal(err)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.closeCalls != 1 {
		t.Fatal("direct Shutdown closed twice")
	}
}

func TestAsyncPublicShutdownCancellationDuringFlush(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	sink := &stalledAsyncSink{flushEntered: entered, flushRelease: release}
	p, ap := newAsyncTestProvider(t, sink, nil)
	_, span := p.Tracer("async").Start(context.Background(), "work")
	span.End()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- p.Shutdown(ctx) }()
	awaitAsync(t, entered)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Shutdown = %v", err)
	}
	select {
	case <-ap.done:
		t.Fatal("writer exited while sink call stalled")
	default:
	}
	close(release)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.closeCalls != 1 || len(lossSummaries(&sink.recordingSink)) != 1 {
		t.Fatal("retry duplicated final summary or missed closure")
	}
	awaitAsync(t, ap.done)
}

func TestAsyncCanceledEntryClosesAdmissionWithoutCheckpoint(t *testing.T) {
	sink := &recordingSink{}
	p, ap := newAsyncTestProvider(t, sink, nil)
	_, span := p.Tracer("async").Start(context.Background(), "unfinished")
	span.SetAttributes(Attribute{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := p.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	span.End() // admission stays closed; no synthesized End
	var loss *IncompleteError
	if err := p.Shutdown(context.Background()); !errors.As(err, &loss) || loss.UnendedSpans != 1 {
		t.Fatalf("retry = %v", err)
	}
	if all := lossSummaries(sink); len(all) != 1 || all[0].UnendedDroppedAttributes != 1 || len(spanEnds(sink)) != 0 {
		t.Fatalf("unfinished checkpoints = %+v", all)
	}
	awaitAsync(t, ap.done)
}

func TestAsyncFlushAndCleanupFailuresNotifySeparately(t *testing.T) {
	sink := &recordingSink{flushErr: errors.New("flush failed"), closeErr: errors.New("close failed")}
	var notifications atomic.Int32
	sent := make(chan struct{}, 2)
	p, ap := newAsyncTestProvider(t, sink, nil, WithErrorHandler(func(error) { notifications.Add(1); sent <- struct{}{} }))
	if err := p.Flush(context.Background()); !errors.Is(err, sink.flushErr) {
		t.Fatal(err)
	}
	if err := p.Shutdown(context.Background()); !errors.Is(err, sink.flushErr) || !errors.Is(err, sink.closeErr) {
		t.Fatal(err)
	}
	awaitAsync(t, sent)
	awaitAsync(t, sent)
	if notifications.Load() != 2 {
		t.Fatalf("notifications = %d, want first failure + cleanup", notifications.Load())
	}
	awaitAsync(t, ap.done)
}

func TestAsyncInitialUpdateAndChildLifecycleUnderSaturation(t *testing.T) {
	t.Run("initial attributes", func(t *testing.T) {
		sink := blockedCaptureSink()
		p, _ := newAsyncTestProvider(t, sink, []AsyncOption{WithMaxQueuedRecords(6)})
		awaitAsync(t, sink.entered)
		_, span := p.Tracer("async").Start(context.Background(), "root", WithAttributes(String("initial", "dropped")))
		span.End()
		close(sink.release)
		if err := p.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if end := spanEnds(&sink.recordingSink)[0]; end.DroppedAttributes != 1 {
			t.Fatalf("initial drop = %+v", end)
		}
	})
	t.Run("root ends before child", func(t *testing.T) {
		sink := blockedCaptureSink()
		p, _ := newAsyncTestProvider(t, sink, []AsyncOption{WithMaxQueuedRecords(9)})
		awaitAsync(t, sink.entered)
		ctx, root := p.Tracer("async").Start(context.Background(), "root")
		_, child := p.Tracer("async").Start(ctx, "child")
		if !root.IsRecording() || !child.IsRecording() {
			t.Fatal("reserved lifecycle setup rejected")
		}
		child.AddEvent("fill")
		child.AddEvent("dropped")
		root.End()
		child.End()
		close(sink.release)
		if err := p.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		ends := spanEnds(&sink.recordingSink)
		traces := traceEnds(&sink.recordingSink)
		if len(ends) != 2 || len(traces) != 1 || ends[1].DroppedEvents != 1 || traces[0].Seq <= ends[1].Seq {
			t.Fatal("saturation lost or reordered child lifecycle")
		}
	})
}

func TestAsyncBlockedErrorHandlerDoesNotOwnWriterLifetime(t *testing.T) {
	sink := blockedCaptureSink()
	sink.failure = errors.New("write failed")
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p, ap := newAsyncTestProvider(t, sink, nil, WithErrorHandler(func(error) {
		close(entered)
		<-release
		close(returned)
	}))
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	awaitAsync(t, sink.entered)
	close(sink.release)
	awaitAsync(t, entered)
	if err := p.Shutdown(context.Background()); !errors.Is(err, sink.failure) {
		t.Fatal(err)
	}
	awaitAsync(t, ap.done)
	close(release)
	awaitAsync(t, returned)
}
