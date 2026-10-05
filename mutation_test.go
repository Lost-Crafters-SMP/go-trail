package trail

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
)

// spanUpdates returns the SpanUpdate records the sink received.
func spanUpdates(sink *recordingSink) []SpanUpdate {
	var updates []SpanUpdate
	for _, r := range sink.records {
		if u, ok := r.(SpanUpdate); ok {
			updates = append(updates, u)
		}
	}
	return updates
}

// events returns the Event records the sink received.
func events(sink *recordingSink) []Event {
	var evts []Event
	for _, r := range sink.records {
		if e, ok := r.(Event); ok {
			evts = append(evts, e)
		}
	}
	return evts
}

func TestStartWithAttributesEmitsBoundedUpdate(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op",
		WithAttributes(String("b", "two"), String("a", "one")))
	span.End()

	updates := spanUpdates(sink)
	if len(updates) != 1 {
		t.Fatalf("emitted %d SpanUpdate records, want 1", len(updates))
	}
	update := updates[0]
	if got := keysOf(update.Attributes); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("update attributes = %v, want sorted [a b]", got)
	}
	if update.Status != nil {
		t.Fatalf("update carries status %+v", update.Status)
	}
	if update.SpanID != span.SpanContext().SpanID() {
		t.Fatal("update span ID does not match the started span")
	}
	// The update follows span_start in sequence order.
	start := spanStarts(sink)[0]
	if update.Seq != start.Seq+1 {
		t.Fatalf("update Seq = %d, want %d", update.Seq, start.Seq+1)
	}
}

func TestSetAttributesUpdateAndOverwrite(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	span.SetAttributes(String("k", "first"))
	span.SetAttributes(String("k", "second"))
	span.End()

	updates := spanUpdates(sink)
	if len(updates) != 2 {
		t.Fatalf("emitted %d SpanUpdate records, want 2", len(updates))
	}
	if updates[0].Attributes[0].String() != "first" || updates[1].Attributes[0].String() != "second" {
		t.Fatalf("updates = %+v", updates)
	}
}

func TestSetAttributesDropsInvalidAndCounts(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	span.SetAttributes(String("", "no key"), Float64("f", math.NaN()), String("k", "v"))
	span.End()

	if got := len(spanUpdates(sink)); got != 1 {
		t.Fatalf("emitted %d SpanUpdate records, want 1", got)
	}
	end := spanEnds(sink)[0]
	if end.DroppedAttributes != 2 {
		t.Fatalf("droppedAttributes = %d, want 2", end.DroppedAttributes)
	}
}

func TestAddEventRecords(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	span.AddEvent("cache.miss", WithAttributes(Int("candidates", 12)))
	span.AddEvent("plain")
	span.End()

	evts := events(sink)
	if len(evts) != 2 {
		t.Fatalf("emitted %d Event records, want 2", len(evts))
	}
	if evts[0].Name != "cache.miss" || len(evts[0].Attributes) != 1 || evts[0].Attributes[0].Int64() != 12 {
		t.Fatalf("first event = %+v", evts[0])
	}
	if evts[1].Name != "plain" || len(evts[1].Attributes) != 0 {
		t.Fatalf("second event = %+v", evts[1])
	}
	if evts[0].SpanID != span.SpanContext().SpanID() || evts[0].TraceID != span.SpanContext().TraceID() {
		t.Fatal("event routing IDs do not match the span")
	}
}

func TestAddEventEmptyNameNormalizedAndCounted(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	span.AddEvent("")
	span.End()

	evts := events(sink)
	if len(evts) != 1 || evts[0].Name != unnamedSpanName {
		t.Fatalf("events = %+v, want one unnamed placeholder", evts)
	}
}

func TestRecordError(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	span.RecordError(nil) // no-op
	span.RecordError(errors.New("lookup failed"), WithAttributes(String("provider", "modrinth")))
	span.End()

	evts := events(sink)
	if len(evts) != 1 {
		t.Fatalf("emitted %d Event records, want 1", len(evts))
	}
	event := evts[0]
	if event.Name != "error" {
		t.Fatalf("event name = %q, want error", event.Name)
	}
	// Attributes are sorted; error.message is present with the others.
	got := keysOf(event.Attributes)
	wantUnsorted := map[string]string{"error.message": "lookup failed", "provider": "modrinth"}
	if len(got) != 2 {
		t.Fatalf("event attributes = %v", got)
	}
	for _, a := range event.Attributes {
		if want, ok := wantUnsorted[a.Key()]; !ok || a.String() != want {
			t.Fatalf("attribute %q = %q, want %q", a.Key(), a.String(), want)
		}
	}
	// RecordError does not set status.
	for _, u := range spanUpdates(sink) {
		if u.Status != nil {
			t.Fatalf("RecordError set status %+v", u.Status)
		}
	}
}

func TestRecordErrorDerivedMessageWins(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	span.RecordError(errors.New("derived"), WithAttributes(String("error.message", "supplied")))
	span.End()

	event := events(sink)[0]
	for _, a := range event.Attributes {
		if a.Key() == "error.message" && a.String() != "derived" {
			t.Fatalf("error.message = %q, want derived to win", a.String())
		}
	}
}

func TestSetStatusLastWinsAndClearsDescription(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	span.SetStatus(StatusError, "lookup failed")
	span.SetStatus(StatusOK, "should be cleared")
	span.SetStatus(StatusUnset, "also cleared")
	span.End()

	updates := spanUpdates(sink)
	statuses := make([]*SpanStatus, 0, len(updates))
	for _, u := range updates {
		if u.Status != nil {
			statuses = append(statuses, u.Status)
		}
	}
	if len(statuses) != 3 {
		t.Fatalf("recorded %d status updates, want 3", len(statuses))
	}
	if statuses[0].Code != StatusError || statuses[0].Description != "lookup failed" {
		t.Fatalf("first status = %+v", statuses[0])
	}
	if statuses[1].Code != StatusOK || statuses[1].Description != "" {
		t.Fatalf("ok status = %+v, want cleared description", statuses[1])
	}
	if statuses[2].Code != StatusUnset || statuses[2].Description != "" {
		t.Fatalf("unset status = %+v, want cleared description", statuses[2])
	}
}

func TestMutationsAfterEndAreNoOps(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	span.End()
	span.SetAttributes(String("k", "v"))
	span.AddEvent("late")
	span.RecordError(errors.New("late"))
	span.SetStatus(StatusError, "late")

	if got := len(spanUpdates(sink)); got != 0 {
		t.Fatalf("emitted %d SpanUpdate records after End, want 0", got)
	}
	if got := len(events(sink)); got != 0 {
		t.Fatalf("emitted %d Event records after End, want 0", got)
	}
}

func TestOverLimitKeysDropExistingUpdatesAllowed(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	attrs := make([]Attribute, 0, maxAttributesPerSpan+1)
	for i := range maxAttributesPerSpan {
		attrs = append(attrs, Int("key"+string(rune('a'+i/26))+string(rune('a'+i%26)), i))
	}
	span.SetAttributes(attrs...)
	span.SetAttributes(Int("key"+string(rune('a'+0))+string(rune('a'+0)), 999)) // existing key: allowed
	span.SetAttributes(Int("zz", 1))                                            // new key: dropped
	span.End()

	updates := spanUpdates(sink)
	if len(updates) != 2 {
		t.Fatalf("emitted %d SpanUpdate records, want 2", len(updates))
	}
	if got := len(updates[0].Attributes); got != maxAttributesPerSpan {
		t.Fatalf("first update has %d attributes, want %d", got, maxAttributesPerSpan)
	}
	end := spanEnds(sink)[0]
	if end.DroppedAttributes != 1 {
		t.Fatalf("droppedAttributes = %d, want 1", end.DroppedAttributes)
	}
}

func TestOversizedUpdateDroppedWholeAndCounted(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	attrs := make([]Attribute, 0, 40)
	for i := range 40 {
		attrs = append(attrs, String("key"+string(rune('a'+i/26))+string(rune('a'+i%26)), strings.Repeat("v", maxStringValueBytes)))
	}
	span.SetAttributes(attrs...)
	span.End()

	if got := len(spanUpdates(sink)); got != 0 {
		t.Fatalf("emitted %d SpanUpdate records, want 0 for oversized batch", got)
	}
	end := spanEnds(sink)[0]
	if end.DroppedAttributes != 40 {
		t.Fatalf("droppedAttributes = %d, want 40", end.DroppedAttributes)
	}
}

func TestOversizedEventDroppedWholeAndCounted(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	attrs := make([]Attribute, 0, 32)
	for i := range 32 {
		attrs = append(attrs, String("key"+string(rune('a'+i/26))+string(rune('a'+i%26)), strings.Repeat("v", maxStringValueBytes)))
	}
	span.AddEvent("large", WithAttributes(attrs...))
	span.End()

	if got := len(events(sink)); got != 0 {
		t.Fatalf("emitted %d Event records, want 0 for oversized event", got)
	}
	end := spanEnds(sink)[0]
	if end.DroppedEvents != 1 {
		t.Fatalf("droppedEvents = %d, want 1", end.DroppedEvents)
	}
}

func TestActiveSpanCapRejectsAndReleases(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t, randomIDs())
	tracer := provider.Tracer("myapp")

	spans := make([]Span, 0, maxActiveSpans)
	for range maxActiveSpans {
		_, span := tracer.Start(context.Background(), "hold")
		if !span.IsRecording() {
			t.Fatalf("admission rejected below the active-span cap at %d spans", len(spans))
		}
		spans = append(spans, span)
	}
	_, rejected := tracer.Start(context.Background(), "over")
	if rejected.IsRecording() {
		t.Fatal("admission accepted beyond the active-span cap")
	}
	if provider.state.rejectedStarts != 1 {
		t.Fatalf("rejectedStarts = %d, want 1", provider.state.rejectedStarts)
	}

	// Ending one span releases capacity for one new admission.
	spans[0].End()
	_, readmitted := tracer.Start(context.Background(), "fresh")
	if !readmitted.IsRecording() {
		t.Fatal("admission still rejected after releasing capacity")
	}
	if got := len(spanStarts(sink)); got != maxActiveSpans+1 {
		t.Fatalf("emitted %d SpanStart records, want %d", got, maxActiveSpans+1)
	}
}

func TestUpdateEventAndEndRace(t *testing.T) {
	provider, _, _, _ := newLifecycleProvider(t, randomIDs())
	tracer := provider.Tracer("myapp")

	const iterations = 50
	for range iterations {
		_, span := tracer.Start(context.Background(), "op")
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			span.SetAttributes(String("k", "v"))
		}()
		go func() {
			defer wg.Done()
			span.AddEvent("event")
		}()
		go func() {
			defer wg.Done()
			span.End()
		}()
		wg.Wait()
		if span.IsRecording() {
			t.Fatal("span still recording after End")
		}
	}
}
