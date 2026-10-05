package trail

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type heldBatchSink struct {
	recordingSink
	start        chan struct{}
	startRelease chan struct{}
	batch        chan struct{}
	batchRelease chan struct{}
	batchFailure error
	calls        atomic.Int32
	maxBytes     int
}

func newHeldBatchSink() *heldBatchSink {
	return &heldBatchSink{start: make(chan struct{}), startRelease: make(chan struct{}), batch: make(chan struct{}), batchRelease: make(chan struct{}), maxBytes: 3072}
}
func (s *heldBatchSink) WriteRecord(r Record) error {
	s.calls.Add(1)
	if _, ok := r.(CaptureStart); ok {
		close(s.start)
		<-s.startRelease
	}
	return s.recordingSink.WriteRecord(r)
}
func (s *heldBatchSink) WriteRecords(records []Record) error {
	s.calls.Add(1)
	bytes := 0
	for _, r := range records {
		n, err := asyncRecordSize(r)
		if err != nil {
			return err
		}
		bytes += n
	}
	if bytes > s.maxBytes && len(records) > 1 {
		return errors.New("batch charge exceeded")
	}
	close(s.batch)
	<-s.batchRelease
	if s.batchFailure != nil {
		return s.batchFailure
	}
	for _, r := range records {
		if err := s.recordingSink.WriteRecord(r); err != nil {
			return err
		}
	}
	return nil
}

func TestAsyncBatchCreditsBarrierAndResume(t *testing.T) {
	s := newHeldBatchSink()
	p, ap := newAsyncTestProvider(t, s, []AsyncOption{WithMaxQueuedRecords(9), WithMaxBatchBytes(3072)})
	awaitAsync(t, s.start)
	tracer := p.Tracer("batch")
	_, a := tracer.Start(context.Background(), "a")
	a.End()
	_, b := tracer.Start(context.Background(), "b")
	b.End()
	close(s.startRelease)
	awaitAsync(t, s.batch)
	ap.mu.Lock()
	credits := ap.usedRecords
	queued := ap.count
	ap.mu.Unlock()
	if credits != 6 || queued != 0 {
		t.Fatalf("in-flight credits=%d queued=%d", credits, queued)
	}
	_, rejected := tracer.Start(context.Background(), "no capacity")
	if rejected.IsRecording() {
		t.Fatal("reused credits while batch in flight")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := ap.acquireControl(ctx); err != nil {
		t.Fatal(err)
	}
	barrier := ap.checkpoint(ctx, nil, false)
	cancel() // cancellation after admission, while a batch is in flight
	if !errors.Is(waitAsyncBarrier(ctx, barrier), context.Canceled) {
		t.Fatal("canceled pending Flush")
	}
	if !errors.Is(p.Shutdown(ctx), context.Canceled) {
		t.Fatal("canceled Shutdown")
	}
	close(s.batchRelease)
	var incomplete *IncompleteError
	if err := p.Shutdown(context.Background()); !errors.As(err, &incomplete) || incomplete.RejectedStarts != 1 {
		t.Fatalf("Shutdown loss accounting: %v", err)
	}
	starts := spanStarts(&s.recordingSink)
	ends := spanEnds(&s.recordingSink)
	if len(starts) != 2 || len(ends) != 2 || starts[0].Name != "a" || starts[1].Name != "b" {
		t.Fatal("FIFO/lifecycle lost")
	}
	ap.mu.Lock()
	defer ap.mu.Unlock()
	if ap.usedRecords != 0 || ap.usedBytes != 0 {
		t.Fatalf("remaining accounting %d/%d", ap.usedRecords, ap.usedBytes)
	}
}

type conformanceBatchAdapter struct{ Sink }

func (s conformanceBatchAdapter) WriteRecords(records []Record) error {
	for _, r := range records {
		if err := s.WriteRecord(r); err != nil {
			return err
		}
	}
	return nil
}

func TestAsyncBatchProcessorConformance(t *testing.T) {
	runProcessorConformance(t, func(s Sink) (Processor, error) {
		return NewAsyncProcessor(conformanceBatchAdapter{s}, WithMaxBatchBytes(3072))
	})
}

func TestAsyncBatchFailureStopsStream(t *testing.T) {
	s := newHeldBatchSink()
	boom := errors.New("partial batch failed")
	s.batchFailure = boom
	p, ap := newAsyncTestProvider(t, s, []AsyncOption{WithMaxBatchBytes(3072)})
	awaitAsync(t, s.start)
	_, span := p.Tracer("batch").Start(context.Background(), "root")
	span.End()
	close(s.startRelease)
	awaitAsync(t, s.batch)
	done := make(chan error, 1)
	go func() { done <- p.Flush(context.Background()) }()
	close(s.batchRelease)
	if err := <-done; !errors.Is(err, boom) {
		t.Fatalf("Flush=%v", err)
	}
	calls := s.calls.Load()
	if err := p.Shutdown(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Shutdown=%v", err)
	}
	if calls != s.calls.Load() {
		t.Fatal("output after batch failure")
	}
	if !errors.Is(ap.Process(CaptureStart{Seq: 100}), ErrProcessorShutdown) {
		t.Fatal("accepted after shutdown")
	}
}

func TestAsyncBatchOptionAndGenericFallback(t *testing.T) {
	for _, limit := range []int{-1, 1, 255, 65537} {
		if _, err := NewAsyncProcessor(&recordingSink{}, WithMaxBatchBytes(limit)); err == nil {
			t.Fatalf("accepted %d", limit)
		}
	}
	runProcessorConformance(t, func(s Sink) (Processor, error) { return NewAsyncProcessor(s, WithMaxBatchBytes(3072)) })
}
