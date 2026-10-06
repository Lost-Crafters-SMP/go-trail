package replay

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func syntheticCapture(count int) string {
	var b strings.Builder
	b.WriteString(fixture())
	seq := 2
	for i := 1; i <= count; i++ {
		tid := fmt.Sprintf("%032x", i)
		sid := fmt.Sprintf("%016x", i)
		fmt.Fprintf(&b, "{\"type\":\"span_start\",\"seq\":\"%d\",\"timeUnixNano\":\"0\",\"elapsedNano\":\"0\",\"traceId\":%q,\"spanId\":%q,\"rootSpanId\":%q,\"name\":\"operation\",\"scope\":\"test\"}\n", seq, tid, sid, sid)
		seq++
		fmt.Fprintf(&b, "{\"type\":\"span_end\",\"seq\":\"%d\",\"timeUnixNano\":\"0\",\"elapsedNano\":\"1\",\"traceId\":%q,\"spanId\":%q,\"rootSpanId\":%q,\"durationNano\":\"1\",\"droppedAttributes\":\"0\",\"droppedEvents\":\"0\",\"droppedStatusUpdates\":\"0\"}\n", seq, tid, sid, sid)
		seq++
		fmt.Fprintf(&b, "{\"type\":\"trace_end\",\"seq\":\"%d\",\"timeUnixNano\":\"0\",\"elapsedNano\":\"1\",\"traceId\":%q,\"rootSpanId\":%q}\n", seq, tid, sid)
		seq++
	}
	return b.String()
}

func TestReleaseCompletedDetail(t *testing.T) {
	if testing.Short() {
		t.Skip("large capture validation")
	}
	const count = 100000
	delivered := 0
	c, err := Read(strings.NewReader(syntheticCapture(count)), "synthetic", Options{ReleaseCompleted: true, OnTrace: func(*MachineTrace) error { delivered++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if delivered != count || c.TotalSpans != count || c.CompletedTraces != count || len(c.Traces) != 0 {
		t.Fatalf("delivered=%d totals=%d completed=%d retained=%d", delivered, c.TotalSpans, c.CompletedTraces, len(c.Traces))
	}
}

// generatedCapture avoids retaining the synthetic input itself in large tests.
type generatedCapture struct {
	count, index int
	pending      *strings.Reader
}

func (g *generatedCapture) Read(p []byte) (int, error) {
	if g.pending == nil {
		g.pending = strings.NewReader(fixture())
	}
	for {
		n, e := g.pending.Read(p)
		if n > 0 {
			return n, nil
		}
		if e != io.EOF {
			return n, e
		}
		if g.index >= g.count {
			return 0, io.EOF
		}
		g.index++
		i := g.index
		seq := 2 + (i-1)*3
		tid := fmt.Sprintf("%032x", i)
		sid := fmt.Sprintf("%016x", i)
		g.pending = strings.NewReader(fmt.Sprintf("{\"type\":\"span_start\",\"seq\":\"%d\",\"timeUnixNano\":\"0\",\"elapsedNano\":\"0\",\"traceId\":%q,\"spanId\":%q,\"rootSpanId\":%q,\"name\":\"operation\",\"scope\":\"test\"}\n{\"type\":\"span_end\",\"seq\":\"%d\",\"timeUnixNano\":\"0\",\"elapsedNano\":\"1\",\"traceId\":%q,\"spanId\":%q,\"rootSpanId\":%q,\"durationNano\":\"1\",\"droppedAttributes\":\"0\",\"droppedEvents\":\"0\",\"droppedStatusUpdates\":\"0\"}\n{\"type\":\"trace_end\",\"seq\":\"%d\",\"timeUnixNano\":\"0\",\"elapsedNano\":\"1\",\"traceId\":%q,\"rootSpanId\":%q}\n", seq, tid, sid, sid, seq+1, tid, sid, sid, seq+2, tid, sid))
	}
}

func TestRelease500K(t *testing.T) {
	if os.Getenv("TRAIL_LARGE_500K") != "1" {
		t.Skip("set TRAIL_LARGE_500K=1 for extended retention validation")
	}
	delivered := 0
	c, err := Read(&generatedCapture{count: 500000}, "synthetic", Options{ReleaseCompleted: true, DiscardSpanPayload: true, OnTrace: func(*MachineTrace) error { delivered++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if delivered != 500000 || len(c.Traces) != 0 || c.TotalSpans != 500000 {
		t.Fatalf("delivered=%d retained=%d spans=%d", delivered, len(c.Traces), c.TotalSpans)
	}
}

func BenchmarkReplayRetention(b *testing.B) {
	input := syntheticCapture(1000)
	for _, release := range []bool{false, true} {
		b.Run(fmt.Sprintf("release=%t", release), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(input)))
			for range b.N {
				_, err := Read(strings.NewReader(input), "synthetic", Options{ReleaseCompleted: release, OnTrace: func(*MachineTrace) error { return nil }})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
