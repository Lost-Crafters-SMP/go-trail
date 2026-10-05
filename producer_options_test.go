package trail

import (
	"context"
	"slices"
	"testing"
)

func TestProducerOptionOrderAndNil(t *testing.T) {
	for _, factory := range []struct {
		name string
		new  func(Sink) (Processor, error)
	}{{"sync", syncProcessorFactory}, {"async", asyncProcessorFactory}} {
		t.Run(factory.name, func(t *testing.T) {
			sink := &recordingSink{}
			p := newConformanceProvider(t, factory.new, sink)
			var calls []int
			first := func(c *startConfig) { calls = append(calls, 1); c.attributes = []Attribute{String("key", "first")} }
			second := func(c *startConfig) { calls = append(calls, 2); c.attributes = []Attribute{String("key", "second")} }
			_, span := p.Tracer("options").Start(context.Background(), "work", nil, first, nil, second)
			span.AddEvent("ordered", nil, first, nil, second)
			span.AddEvent("nil-only", nil, nil)
			span.AddEvent("empty")
			span.End()
			if err := p.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(calls, []int{1, 2, 1, 2}) {
				t.Fatalf("calls = %v", calls)
			}
			updates := spanUpdates(sink)
			if len(updates) != 1 || updates[0].Attributes[0].String() != "second" {
				t.Fatalf("updates = %+v", updates)
			}
			evts := events(sink)
			if len(evts) != 3 || evts[0].Attributes[0].String() != "second" || len(evts[1].Attributes) != 0 || len(evts[2].Attributes) != 0 {
				t.Fatalf("events = %+v", evts)
			}
		})
	}
}
