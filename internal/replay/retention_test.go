package replay

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"go.lostcrafters.com/trail"
)

type failingReader struct {
	reader   io.Reader
	err      error
	failures int
}

func (r *failingReader) Read(p []byte) (int, error) {
	n, e := r.reader.Read(p)
	if errors.Is(e, io.EOF) {
		r.failures++
		if r.failures == 1 {
			return n, r.err
		}
	}
	return n, e
}

func TestReaderFailureIsNotRecoverable(t *testing.T) {
	failure := errors.New("reader failure")
	for _, best := range []bool{false, true} {
		r := &failingReader{reader: strings.NewReader(fixture()), err: failure}
		if _, err := Read(r, "capture", Options{BestEffort: best}); !errors.Is(err, failure) {
			t.Fatalf("best=%t: error=%v", best, err)
		}
		if r.failures != 1 {
			t.Fatal("retried a failed reader")
		}
	}
}

func TestActiveTraceRetention(t *testing.T) {
	const count = 2048
	var input strings.Builder
	input.WriteString(fixture(startLine(2, testRoot, "", 0)))
	seq := 3
	for i := range count {
		sid := fmt.Sprintf("%016x", i+1)
		input.WriteString(startLine(seq, sid, testRoot, 0) + "\n")
		seq++
		fmt.Fprintf(&input, "{\"type\":\"span_update\",\"seq\":\"%d\",\"timeUnixNano\":\"0\",\"elapsedNano\":\"0\",\"traceId\":%q,\"rootSpanId\":%q,\"spanId\":%q,\"attributes\":[{\"key\":\"payload\",\"type\":\"string\",\"value\":\"retained until end\"}]}\n", seq, testTrace, testRoot, sid)
		seq++
		fmt.Fprintf(&input, "{\"type\":\"event\",\"seq\":\"%d\",\"timeUnixNano\":\"0\",\"elapsedNano\":\"0\",\"traceId\":%q,\"rootSpanId\":%q,\"spanId\":%q,\"name\":\"checkpoint\"}\n", seq, testTrace, testRoot, sid)
		seq++
		input.WriteString(endLine(seq, sid, 0, 0) + "\n")
		seq++
	}
	for _, discard := range []bool{false, true} {
		m := &RetentionStats{}
		c, err := Read(strings.NewReader(input.String()), "active", Options{DiscardSpanPayload: discard, ReleaseCompleted: true, OnTrace: func(*MachineTrace) error { return nil }, Retention: m})
		if err != nil {
			t.Fatal(err)
		}
		if m.RetainedSpans != count+1 || m.CompletionIDs != 0 || c.LossAccounting.LiveSpans != 1 {
			t.Fatalf("active history lost: %+v", m)
		}
		want := count
		if discard {
			want = 0
		}
		if m.RetainedAttributes != want || m.RetainedEvents != want {
			t.Fatalf("discard=%t: %+v", discard, m)
		}
		t.Logf("discard payload=%t: %+v", discard, m)
	}
}

func TestCompletionIDsPreserveLateRecordDetection(t *testing.T) {
	completion := fmt.Sprintf(`{"type":"trace_end","seq":"4","timeUnixNano":"0","elapsedNano":"0","traceId":%q,"rootSpanId":%q}`, testTrace, testRoot)
	input := fixture(startLine(2, testRoot, "", 0), endLine(3, testRoot, 0, 0), completion, endLine(5, testRoot, 0, 0))
	for _, best := range []bool{false, true} {
		m := &RetentionStats{}
		c, err := Read(strings.NewReader(input), "test", Options{ReleaseCompleted: true, BestEffort: best, OnTrace: func(*MachineTrace) error { return nil }, Retention: m})
		if best {
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Diagnostics) != 1 {
				t.Fatal("late record hidden")
			}
		} else if err == nil {
			t.Fatal("late record accepted")
		}
		if m.CompletionIDs != 1 || m.RetainedSpans != 0 {
			t.Fatalf("unexpected retention: %+v", m)
		}
	}
}

func TestGlobalAdditiveIDsDoNotAcquireSpanSemantics(t *testing.T) {
	completion := fmt.Sprintf(`{"type":"trace_end","seq":"4","timeUnixNano":"0","elapsedNano":"0","traceId":%q,"rootSpanId":%q}`, testTrace, testRoot)
	summary := fmt.Sprintf(`{"type":"loss_summary","seq":"5","timeUnixNano":"0","elapsedNano":"0","traceId":%q,"spanId":%q,"rejectedStarts":"0","droppedAttributes":"0","droppedEvents":"0","droppedStatusUpdates":"0","unendedSpans":"0","unendedDroppedAttributes":"0","unendedDroppedEvents":"0","unendedDroppedStatusUpdates":"0"}`, testTrace, testRoot)
	input := fixture(startLine(2, testRoot, "", 0), endLine(3, testRoot, 0, 0), completion, summary)
	input = strings.Replace(input, `"type":"capture_start"`, fmt.Sprintf(`"type":"capture_start","traceId":%q,"spanId":%q`, testTrace, testRoot), 1)
	id, _ := trail.ParseTraceID(testTrace)
	sid, _ := trail.ParseSpanID(testRoot)
	c, err := Read(strings.NewReader(input), "test", Options{DiscardSpanPayload: true, PayloadSpan: sid, TargetTrace: id, ReleaseCompleted: true, OnTrace: func(*MachineTrace) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if c.LossAccounting.Summary == nil || len(c.Diagnostics) != 0 {
		t.Fatal("global fields interpreted as a late span record")
	}
}

func TestInvalidStartDoesNotCreateGhostTrace(t *testing.T) {
	c, err := Read(strings.NewReader(fixture(startLine(2, testChild, "", 0))), "test", Options{BestEffort: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.TotalTraces != 0 || c.TotalSpans != 0 || len(c.Traces) != 0 || len(c.Diagnostics) != 1 {
		t.Fatalf("invalid start reconstructed state: %+v", c)
	}
}
