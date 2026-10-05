package trail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func lossSummaries(sink *recordingSink) []LossSummary {
	var summaries []LossSummary
	for _, record := range sink.records {
		if summary, ok := record.(LossSummary); ok {
			summaries = append(summaries, summary)
		}
	}
	return summaries
}

func TestLossSummaryCheckpoints(t *testing.T) {
	provider, sink, clock, _ := newLifecycleProvider(t)
	_, span := provider.Tracer("loss").Start(context.Background(), "live", WithAttributes(Attribute{}))
	if err := provider.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := lossSummaries(sink)[0]
	if first.DroppedAttributes != 1 || first.UnendedDroppedAttributes != 1 || first.UnendedSpans != 1 {
		t.Fatalf("first snapshot = %+v", first)
	}
	if !first.Wall.Equal(clock.wall) || first.Elapsed != clock.tick {
		t.Fatalf("snapshot clock = %+v", first)
	}
	span.SetAttributes(Attribute{})
	span.AddEvent("invalid attr", WithAttributes(Attribute{}))
	attrs := make([]Attribute, maxAttributesPerEvent)
	for i := range attrs {
		attrs[i] = String(strings.Repeat("k", i+1), strings.Repeat("v", maxStringValueBytes))
	}
	span.AddEvent("oversized", WithAttributes(attrs...))
	if err := provider.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := lossSummaries(sink)[1]
	if second.Seq <= first.Seq || second.DroppedAttributes != 3 || second.DroppedEvents != 1 ||
		second.UnendedDroppedAttributes != 3 || second.UnendedDroppedEvents != 1 {
		t.Fatalf("replacement snapshot = %+v", second)
	}
	span.End()
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	final := lossSummaries(sink)[2]
	if final.DroppedAttributes != 3 || final.DroppedEvents != 1 || final.UnendedSpans != 0 ||
		final.UnendedDroppedAttributes != 0 || final.UnendedDroppedEvents != 0 {
		t.Fatalf("final snapshot = %+v", final)
	}
	end := spanEnds(sink)[0]
	if end.DroppedAttributes != final.DroppedAttributes || end.DroppedEvents != final.DroppedEvents {
		t.Fatalf("per-span counts changed: %+v, %+v", end, final)
	}
	_ = provider.Shutdown(context.Background())
	_ = provider.Flush(context.Background())
	if len(lossSummaries(sink)) != 3 {
		t.Fatal("closed provider emitted another snapshot")
	}
}

func TestLossSummaryZeroAndUnfinished(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	if err := provider.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	zero := lossSummaries(sink)[0]
	if zero.RejectedStarts != 0 || zero.DroppedAttributes != 0 || zero.DroppedEvents != 0 || zero.UnendedSpans != 0 {
		t.Fatalf("zero snapshot = %+v", zero)
	}
	_, span := provider.Tracer("loss").Start(context.Background(), "forgotten")
	span.SetAttributes(Attribute{})
	err := provider.Shutdown(context.Background())
	var incomplete *IncompleteError
	if !errors.As(err, &incomplete) || incomplete.UnendedSpans != 1 {
		t.Fatalf("Shutdown = %v", err)
	}
	final := lossSummaries(sink)[1]
	if final.UnendedSpans != 1 || final.DroppedAttributes != 1 || final.UnendedDroppedAttributes != 1 || len(spanEnds(sink)) != 0 {
		t.Fatalf("unfinished evidence = %+v", final)
	}
}

func TestLossSummaryRejectedIDs(t *testing.T) {
	for _, kind := range []string{"span", "trace"} {
		t.Run(kind, func(t *testing.T) {
			provider, sink, _, ids := newLifecycleProvider(t)
			failure := errors.New("ID failure")
			if kind == "span" {
				ids.spanErr = failure
			} else {
				ids.traceErr = failure
			}
			_, span := provider.Tracer("loss").Start(context.Background(), "rejected")
			if span.IsRecording() {
				t.Fatal("failed IDs admitted a span")
			}
			if err := provider.Flush(context.Background()); !errors.Is(err, failure) {
				t.Fatalf("Flush = %v", err)
			}
			if got := lossSummaries(sink)[0].RejectedStarts; got != 1 {
				t.Fatalf("rejected starts = %d", got)
			}
		})
	}
}

// checkpointProcessor checks submission/barrier ordering and permits isolated
// acceptance failures without forcing the synchronous sink's failure latch.
type checkpointProcessor struct {
	recordingSink
	rejectStart    error
	rejectEnd      error
	rejectSummary  error
	cancelShutdown bool
}

func (p *checkpointProcessor) Process(r Record) error {
	if _, ok := r.(SpanStart); ok && p.rejectStart != nil {
		return p.rejectStart
	}
	if _, ok := r.(LossSummary); ok && p.rejectSummary != nil {
		return p.rejectSummary
	}
	if _, ok := r.(SpanEnd); ok && p.rejectEnd != nil {
		return p.rejectEnd
	}
	return p.WriteRecord(r)
}

func (p *checkpointProcessor) Flush(ctx context.Context) error {
	if _, ok := p.records[len(p.records)-1].(LossSummary); !ok {
		return errors.New("missing checkpoint before barrier")
	}
	return p.recordingSink.Flush(ctx)
}

func (p *checkpointProcessor) Shutdown(ctx context.Context) error {
	if p.cancelShutdown {
		p.cancelShutdown = false
		return context.Canceled
	}
	return p.recordingSink.Shutdown(ctx)
}

func TestLossSummaryProcessorRejectionAndBarrier(t *testing.T) {
	processor := &checkpointProcessor{rejectStart: errors.New("reject admission")}
	provider, err := NewProvider(processor)
	if err != nil {
		t.Fatal(err)
	}
	_, span := provider.Tracer("loss").Start(context.Background(), "rejected")
	if span.IsRecording() {
		t.Fatal("processor-rejected start admitted")
	}
	if err := provider.Flush(context.Background()); !errors.Is(err, processor.rejectStart) {
		t.Fatalf("Flush = %v", err)
	}
	if processor.flushCalls != 1 || lossSummaries(&processor.recordingSink)[0].RejectedStarts != 1 {
		t.Fatal("summary missing or barrier failed")
	}
}

func TestLossSummaryFailures(t *testing.T) {
	for _, operation := range []string{"Flush", "Shutdown"} {
		for _, failureKind := range []string{"accept", "write"} {
			t.Run(operation+"/"+failureKind, func(t *testing.T) {
				failure := errors.New("summary failed")
				processor := &checkpointProcessor{}
				var provider *Provider
				var err error
				if failureKind == "accept" {
					provider, err = NewProvider(processor)
					processor.rejectSummary = failure
				} else {
					provider, _, _, _ = newLifecycleProvider(t)
					provider.state.processor.(*SyncProcessor).sink.(*recordingSink).writeErr = failure
				}
				if err != nil {
					t.Fatal(err)
				}
				if operation == "Flush" {
					err = provider.Flush(context.Background())
				} else {
					err = provider.Shutdown(context.Background())
				}
				if !errors.Is(err, failure) {
					t.Fatalf("%s = %v, want summary failure", operation, err)
				}
				if err := provider.Shutdown(context.Background()); !errors.Is(err, failure) {
					t.Fatalf("terminal error lost summary failure: %v", err)
				}
			})
		}
	}
}

func TestLossSummaryFailureNotifiesOnceAndClosesSink(t *testing.T) {
	var notifications int
	provider, sink, _, _ := newLifecycleProvider(t, WithErrorHandler(func(error) {
		notifications++
	}))
	failure := errors.New("summary write failed")
	sink.writeErr = failure
	if err := provider.Flush(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := provider.Shutdown(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if notifications != 1 || sink.closeCalls != 1 {
		t.Fatalf("notifications = %d, closes = %d; want one each", notifications, sink.closeCalls)
	}
}

func TestLossSummaryCanceledShutdownResumesWithoutDuplicate(t *testing.T) {
	processor := &checkpointProcessor{cancelShutdown: true}
	provider, err := NewProvider(processor)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := provider.Flush(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := provider.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(lossSummaries(&processor.recordingSink)) != 0 {
		t.Fatal("canceled entry submitted summary")
	}
	if err := provider.Shutdown(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(lossSummaries(&processor.recordingSink)) != 1 || processor.closeCalls != 1 {
		t.Fatal("resumed shutdown duplicated summary or missed cleanup")
	}
}

func TestLossSummaryRetainsDropsAfterFailedEnd(t *testing.T) {
	processor := &checkpointProcessor{rejectEnd: errors.New("end rejected")}
	provider, err := NewProvider(processor)
	if err != nil {
		t.Fatal(err)
	}
	_, span := provider.Tracer("loss").Start(context.Background(), "ended", WithAttributes(Attribute{}))
	span.End()
	if err := provider.Shutdown(context.Background()); !errors.Is(err, processor.rejectEnd) {
		t.Fatal(err)
	}
	final := lossSummaries(&processor.recordingSink)[0]
	if final.DroppedAttributes != 1 || final.UnendedSpans != 0 || final.UnendedDroppedAttributes != 0 || len(spanEnds(&processor.recordingSink)) != 0 {
		t.Fatalf("failed-End snapshot = %+v", final)
	}
}

func TestLossSummaryConcurrentDropsAndCheckpoints(t *testing.T) {
	sink := &recordingSink{}
	processor, err := NewSyncProcessor(sink)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewProvider(processor)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, span := provider.Tracer("loss").Start(context.Background(), "worker")
			for range 100 {
				span.SetAttributes(Attribute{})
				span.AddEvent("event", WithAttributes(Attribute{}))
			}
			span.End()
		})
	}
	wg.Go(func() {
		for range 20 {
			if err := provider.Flush(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
	wg.Wait()
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	var prev uint64
	for _, summary := range lossSummaries(sink) {
		if summary.DroppedAttributes < prev || summary.UnendedDroppedAttributes > summary.DroppedAttributes {
			t.Fatalf("inconsistent concurrent snapshot = %+v", summary)
		}
		prev = summary.DroppedAttributes
	}
	final := lossSummaries(sink)[20]
	if final.DroppedAttributes != 1600 || final.UnendedDroppedAttributes != 0 || final.UnendedSpans != 0 {
		t.Fatalf("final concurrent snapshot = %+v", final)
	}
}

func TestLossSummaryOversizedAttributeBatches(t *testing.T) {
	attrs := make([]Attribute, maxAttributesPerSpan)
	for i := range attrs {
		attrs[i] = String(strings.Repeat("k", i+1), strings.Repeat("v", maxStringValueBytes))
	}
	for _, initial := range []bool{true, false} {
		t.Run(map[bool]string{true: "initial", false: "mutation"}[initial], func(t *testing.T) {
			provider, sink, _, _ := newLifecycleProvider(t)
			var opts []StartOption
			if initial {
				opts = []StartOption{WithAttributes(attrs...)}
			}
			_, span := provider.Tracer("loss").Start(context.Background(), "oversized", opts...)
			if !initial {
				span.SetAttributes(attrs...)
			}
			if err := provider.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			summary := lossSummaries(sink)[0]
			if summary.DroppedAttributes != maxAttributesPerSpan || summary.UnendedDroppedAttributes != maxAttributesPerSpan {
				t.Fatalf("oversized-batch snapshot = %+v", summary)
			}
			span.End()
			if end := spanEnds(sink)[0]; end.DroppedAttributes != summary.DroppedAttributes {
				t.Fatalf("span_end and snapshot disagree: %+v", end)
			}
		})
	}
}
