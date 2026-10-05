package trail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unsafe"
)

func TestAsyncFreshRejectionSkipsOwnership(t *testing.T) {
	sink := blockedCaptureSink()
	p, ap := newAsyncTestProvider(t, sink, []AsyncOption{WithMaxQueuedRecords(6)})
	awaitAsync(t, sink.entered)
	_, span := p.Tracer("owned").Start(context.Background(), "root")
	var called bool
	err := processFresh(ap, Event{Name: "rejected"}, func(r Event) Event { called = true; return r })
	if !errors.Is(err, ErrQueueFull) || called {
		t.Fatalf("rejected ownership: err=%v, called=%v", err, called)
	}
	span.End()
	close(sink.release)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncResolvedProducerOwnership(t *testing.T) {
	sink := blockedCaptureSink()
	p, _ := newAsyncTestProvider(t, sink, nil)
	awaitAsync(t, sink.entered)
	// Short, valid payloads must not retain these large string backings.
	large := strings.Repeat("x", 8<<20)
	short := large[100:108]
	values := []string{short, "original"}
	attrs := []Attribute{Strings("list", values), String(short, short)}
	_, span := p.Tracer(short).Start(context.Background(), short, WithAttributes(attrs...))
	span.SetAttributes(attrs...)
	span.AddEvent(short, WithAttributes(attrs...))
	values[0], values[1] = "mutated", "mutated"
	attrs[0] = String("replaced", "mutated")
	span.End()
	close(sink.release)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	checkString := func(got string) {
		t.Helper()
		if got != short || unsafe.StringData(got) == unsafe.StringData(short) {
			t.Fatal("queued string lost its value or retained caller backing")
		}
	}
	checkAttributes := func(got []Attribute) {
		t.Helper()
		if len(got) != 2 {
			t.Fatalf("attributes = %+v", got)
		}
		for _, a := range got {
			if a.Kind() == KindStrings {
				v := a.Strings()
				if len(v) != 2 || v[1] != "original" || &v[0] == &values[0] {
					t.Fatal("nested caller slice retained or mutated")
				}
				checkString(v[0])
			} else {
				checkString(a.Key())
				checkString(a.String())
			}
		}
	}
	starts := spanStarts(&sink.recordingSink)
	checkString(starts[0].Name)
	checkString(starts[0].Scope)
	updates := spanUpdates(&sink.recordingSink)
	if len(updates) != 2 {
		t.Fatalf("updates = %+v", updates)
	}
	for _, r := range updates {
		checkAttributes(r.Attributes)
	}
	evts := events(&sink.recordingSink)
	if len(evts) != 1 {
		t.Fatalf("events = %+v", evts)
	}
	checkString(evts[0].Name)
	checkAttributes(evts[0].Attributes)
}

func TestAsyncResolvedBatchCompactsSpareCapacity(t *testing.T) {
	sink := blockedCaptureSink()
	p, _ := newAsyncTestProvider(t, sink, []AsyncOption{WithMaxQueuedBytes(2048)})
	awaitAsync(t, sink.entered)
	_, span := p.Tracer("owned").Start(context.Background(), "root")
	attrs := make([]Attribute, maxAttributesPerSpan)
	for i := range attrs {
		attrs[i] = String("key", "value")
	}
	span.SetAttributes(attrs...)
	span.End()
	close(sink.release)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	updates := spanUpdates(&sink.recordingSink)
	if len(updates) != 1 || len(updates[0].Attributes) != 1 || cap(updates[0].Attributes) != 1 {
		t.Fatalf("retained uncharged capacity: %+v", updates)
	}
}
