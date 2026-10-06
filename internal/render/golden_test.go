package render

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/internal/replay"
)

const goldenJournal = `{"format":"trail","version":1}
{"type":"capture_start","seq":"1","timeUnixNano":"0","elapsedNano":"0"}
{"type":"span_start","seq":"2","timeUnixNano":"0","elapsedNano":"0","traceId":"11111111111111111111111111111111","spanId":"2222222222222222","rootSpanId":"2222222222222222","name":"root with a long name and quoted \"label\"","scope":"test"}
{"type":"span_update","seq":"3","timeUnixNano":"0","elapsedNano":"0","traceId":"11111111111111111111111111111111","spanId":"2222222222222222","rootSpanId":"2222222222222222","attributes":[{"key":"empty","type":"string","value":""},{"key":"enabled","type":"bool","value":false},{"key":"int","type":"int64","value":"-9223372036854775808"},{"key":"uint","type":"uint64","value":"18446744073709551615"},{"key":"duration","type":"duration","value":"25000000"},{"key":"ratio","type":"float64","value":2.5},{"key":"labels","type":"strings","value":["a,b","quoted \"value\""]}]}
{"type":"event","seq":"4","timeUnixNano":"0","elapsedNano":"0","traceId":"11111111111111111111111111111111","spanId":"2222222222222222","rootSpanId":"2222222222222222","name":"checkpoint"}
{"type":"span_end","seq":"5","timeUnixNano":"0","elapsedNano":"0","traceId":"11111111111111111111111111111111","spanId":"2222222222222222","rootSpanId":"2222222222222222","durationNano":"0","droppedAttributes":"0","droppedEvents":"0","droppedStatusUpdates":"0"}
{"type":"span_start","seq":"6","timeUnixNano":"1","elapsedNano":"1","traceId":"11111111111111111111111111111111","spanId":"3333333333333333","rootSpanId":"2222222222222222","parentSpanId":"4444444444444444","name":"unended child","scope":"test.child"}
`

func TestReadableGoldens(t *testing.T) {
	c, err := replay.Read(strings.NewReader(goldenJournal), "capture.trail.jsonl", replay.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := trail.ParseTraceID("11111111111111111111111111111111")
	rid, _ := trail.ParseSpanID("2222222222222222")
	sid, _ := trail.ParseSpanID("3333333333333333")
	trace := c.Traces[tid]
	unknown := Capture(c, false)
	stats := Capture(c, false)
	delete(stats, "incomplete_traces")
	c.LossAccounting.Summary = &replay.MachineLossSummary{Seq: 7, UnendedSpans: 1}
	known := Capture(c, false)
	c.TailCondition = &replay.TailCondition{Type: "torn_tail", Line: 8, Message: "incomplete JSON at EOF"}
	torn := Capture(c, false)
	for _, tc := range []struct {
		name string
		doc  Object
	}{
		{"inspect-unknown", Object{"capture": unknown}},
		{"inspect-zero", Object{"capture": known}},
		{"inspect-torn", Object{"capture": torn}},
		{"stats", Object{"capture": stats}},
		{"trace", Object{"trace": Trace(trace)}},
		{"span", Object{"span": Span(trace.Spans[rid])}},
		{"span-unended", Object{"span": Span(trace.Spans[sid])}},
		{"query", Object{"spans": []Object{Span(trace.Spans[rid]), Span(trace.Spans[sid])}}},
		{"export", Object{"capture": Capture(c, true)}},
	} {
		for _, format := range []string{"text", "toon"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				var out bytes.Buffer
				if err := Write(&out, format, tc.doc); err != nil {
					t.Fatal(err)
				}
				checkGolden(t, tc.name+"."+format, out.Bytes())
			})
		}
	}
	t.Run("trace-truncated", func(t *testing.T) {
		var out bytes.Buffer
		if err := WriteWithOptions(&out, "text", Object{"trace": Trace(trace)}, TextOptions{MaxDepth: 0, MaxSpans: 1, NoEvents: true, Sort: "start"}); err != nil {
			t.Fatal(err)
		}
		checkGolden(t, "trace-truncated.text", out.Bytes())
	})
	for _, status := range []replay.MachineStatusCode{replay.StatusOK, replay.StatusError} {
		t.Run("status-"+string(status), func(t *testing.T) {
			trace.Spans[rid].Status = &replay.MachineStatus{Code: status, Description: "explicit Trail status"}
			var out bytes.Buffer
			if err := Write(&out, "text", Object{"trace": Trace(trace)}); err != nil {
				t.Fatal(err)
			}
			checkGolden(t, "trace-"+string(status)+".text", out.Bytes())
		})
	}
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("TRAIL_UPDATE_GOLDENS") == "1" {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch: %s\nactual:\n%s\nexpected:\n%s", path, got, want)
	}
}
