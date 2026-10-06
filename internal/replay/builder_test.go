package replay

import (
	"fmt"
	"strings"
	"testing"
)

const testTrace = "11111111111111111111111111111111"
const testRoot = "2222222222222222"
const testChild = "3333333333333333"

func fixture(lines ...string) string {
	return "{\"format\":\"trail\",\"version\":1}\n" + strings.Join(append([]string{`{"type":"capture_start","seq":"1","timeUnixNano":"0","elapsedNano":"0"}`}, lines...), "\n") + "\n"
}

func startLine(seq int, id, parent string, elapsed int) string {
	p := ""
	if parent != "" {
		p = fmt.Sprintf(`,"parentSpanId":%q`, parent)
	}
	return fmt.Sprintf(`{"type":"span_start","seq":"%d","timeUnixNano":"0","elapsedNano":"%d","traceId":%q,"spanId":%q,"rootSpanId":%q,"name":"operation","scope":"test"%s}`, seq, elapsed, testTrace, id, testRoot, p)
}

func endLine(seq int, id string, elapsed, duration int) string {
	return fmt.Sprintf(`{"type":"span_end","seq":"%d","timeUnixNano":"0","elapsedNano":"%d","traceId":%q,"spanId":%q,"rootSpanId":%q,"durationNano":"%d","droppedAttributes":"0","droppedEvents":"0","droppedStatusUpdates":"0"}`, seq, elapsed, testTrace, id, testRoot, duration)
}

func TestTraceLifetimeSeparateFromRoot(t *testing.T) {
	input := fixture(startLine(2, testRoot, "", 10), startLine(3, testChild, testRoot, 20), endLine(4, testRoot, 30, 20), endLine(5, testChild, 70, 50), fmt.Sprintf(`{"type":"trace_end","seq":"6","timeUnixNano":"0","elapsedNano":"70","traceId":%q,"rootSpanId":%q}`, testTrace, testRoot))
	c, err := Read(strings.NewReader(input), "capture", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range c.Traces {
		if tr.TraceLifetimeNanos == nil || *tr.TraceLifetimeNanos != "60" {
			t.Fatalf("lifetime=%v", tr.TraceLifetimeNanos)
		}
		if tr.RootSpanDurationNanos == nil || *tr.RootSpanDurationNanos != "20" {
			t.Fatalf("root duration=%v", tr.RootSpanDurationNanos)
		}
	}
	if c.LossAccounting.Summary != nil {
		t.Fatal("missing summary interpreted as known")
	}
}

func TestUnendedAndUnresolved(t *testing.T) {
	c, err := Read(strings.NewReader(fixture(startLine(2, testChild, testRoot, 10))), "capture", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range c.Traces {
		if tr.TraceLifetimeNanos != nil {
			t.Fatal("invented lifetime")
		}
		for _, s := range tr.Spans {
			if s.DurationNanos != nil || s.Ended || !s.UnresolvedParent || s.ParentSpanID.String() != testRoot {
				t.Fatalf("span=%+v", s)
			}
		}
	}
	if len(c.Diagnostics) != 1 {
		t.Fatalf("diagnostics=%v", c.Diagnostics)
	}
}

func TestDuplicateEndRecovery(t *testing.T) {
	input := fixture(startLine(2, testRoot, "", 0), endLine(3, testRoot, 0, 0), endLine(4, testRoot, 1, 1))
	if _, err := Read(strings.NewReader(input), "capture", Options{}); err == nil {
		t.Fatal("accepted duplicate end")
	}
	c, err := Read(strings.NewReader(input), "capture", Options{BestEffort: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range c.Traces {
		if *tr.RootSpan.DurationNanos != "0" {
			t.Fatal("duplicate overwrote authoritative zero")
		}
	}
	if _, err := Read(strings.NewReader(input), "capture", Options{BestEffort: true, Strict: true}); err == nil {
		t.Fatal("strict accepted diagnostic")
	}
}
