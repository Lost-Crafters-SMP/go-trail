package trailhttp

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.lostcrafters.com/trail"
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

func spanStarts(s *recordingSink) []trail.SpanStart {
	var out []trail.SpanStart
	for _, rec := range s.snapshot() {
		if ss, ok := rec.(trail.SpanStart); ok {
			out = append(out, ss)
		}
	}
	return out
}

func spanEnds(s *recordingSink) []trail.SpanEnd {
	var out []trail.SpanEnd
	for _, rec := range s.snapshot() {
		if se, ok := rec.(trail.SpanEnd); ok {
			out = append(out, se)
		}
	}
	return out
}

func spanEvents(s *recordingSink, id trail.SpanID) []trail.Event {
	var out []trail.Event
	for _, rec := range s.snapshot() {
		if e, ok := rec.(trail.Event); ok && e.SpanID == id {
			out = append(out, e)
		}
	}
	return out
}

func attrsFor(s *recordingSink, id trail.SpanID) map[string]trail.Attribute {
	out := make(map[string]trail.Attribute)
	for _, rec := range s.snapshot() {
		if u, ok := rec.(trail.SpanUpdate); ok && u.SpanID == id {
			for _, a := range u.Attributes {
				out[a.Key()] = a
			}
		}
	}
	return out
}

func statusFor(s *recordingSink, id trail.SpanID) *trail.SpanStatus {
	var last *trail.SpanStatus
	for _, rec := range s.snapshot() {
		if u, ok := rec.(trail.SpanUpdate); ok && u.SpanID == id && u.Status != nil {
			last = u.Status
		}
	}
	return last
}

func singleStart(t *testing.T, s *recordingSink) trail.SpanStart {
	t.Helper()
	starts := spanStarts(s)
	if len(starts) != 1 {
		t.Fatalf("span starts = %d, want 1", len(starts))
	}
	return starts[0]
}

// recordStrings collects every string a journal replay would expose.
func recordStrings(s *recordingSink) []string {
	var out []string
	add := func(name string, attrs []trail.Attribute) {
		out = append(out, name)
		for _, a := range attrs {
			switch a.Kind() {
			case trail.KindString:
				out = append(out, a.String())
			case trail.KindStrings:
				out = append(out, a.Strings()...)
			}
		}
	}
	for _, rec := range s.snapshot() {
		switch r := rec.(type) {
		case trail.SpanStart:
			add(r.Name, nil)
		case trail.SpanUpdate:
			add("", r.Attributes)
			if r.Status != nil {
				out = append(out, r.Status.Description)
			}
		case trail.Event:
			add(r.Name, r.Attributes)
		}
	}
	return out
}

func assertNoRecordText(t *testing.T, s *recordingSink, forbidden ...string) {
	t.Helper()
	for _, text := range recordStrings(s) {
		for _, f := range forbidden {
			if strings.Contains(text, f) {
				t.Fatalf("forbidden text %q recorded in %q", f, text)
			}
		}
	}
}
