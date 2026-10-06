package render

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/internal/replay"
)

func benchmarkDocument(count, attrs, events int) Object {
	tid, _ := trail.ParseTraceID("11111111111111111111111111111111")
	rid, _ := trail.ParseSpanID("0000000000000001")
	duration := "123456789"
	t := &replay.MachineTrace{TraceID: tid, RootSpanID: rid, Status: replay.TraceStatusCompleted, TraceLifetimeNanos: &duration, RootSpanDurationNanos: &duration, WallStart: time.Unix(0, 0).UTC().Format(time.RFC3339Nano), Spans: map[trail.SpanID]*replay.MachineSpan{}}
	for i := range count {
		a := make([]replay.MachineAttribute, 0, attrs)
		for j := range attrs {
			a = append(a, replay.MachineAttribute{Key: fmt.Sprintf("attribute.%d", j), Type: replay.KindString, StringValue: "representative value"})
		}
		e := make([]replay.MachineEvent, 0, events)
		for j := range events {
			e = append(e, replay.MachineEvent{Seq: uint64(j + 1), Name: "checkpoint", ElapsedNanos: 123456789, WallTime: time.Unix(0, 123456789).UTC()})
		}
		sid, _ := trail.ParseSpanID(fmt.Sprintf("%016x", i+1))
		s := &replay.MachineSpan{SpanID: sid, TraceID: tid, RootSpanID: rid, Name: "process execution", Scope: "application", WallStart: t.WallStart, WallEnd: &t.WallStart, DurationNanos: &duration, Ended: true, Attributes: a, Events: e, DroppedAttrs: "0", DroppedEvents: "0", DroppedStatus: "0", StartSeq: uint64(i + 2)}
		if i > 0 {
			s.ParentSpanID = rid
			s.ParentResolved = true
			t.Spans[rid].Children = append(t.Spans[rid].Children, sid)
		}
		t.Spans[sid] = s
	}
	return Object{"trace": Trace(t)}
}

func serializationSample(name string, count, attrs, events int) Object {
	if name == "inspect" || name == "diagnosticsLossHeavy" {
		c := &replay.MachineCapture{Path: "capture.trail.jsonl", Header: replay.JournalHeader{Format: "trail", Version: 1}, CaptureStart: &replay.CaptureStartRecord{WallTime: time.Unix(0, 0).UTC()}, TotalTraces: 100, CompletedTraces: 98, TotalSpans: 500, EndedSpans: 497, TotalEvents: 1000, Traces: map[trail.TraceID]*replay.MachineTrace{}}
		if name == "diagnosticsLossHeavy" {
			c.LossAccounting.Summary = &replay.MachineLossSummary{Seq: 2000, DroppedAttributes: 100, DroppedEvents: 12}
			for i := range 20 {
				c.Diagnostics = append(c.Diagnostics, replay.MachineDiagnostic{Type: "unresolved_parent", Line: i + 1, Message: "parent missing or started after child"})
			}
		}
		return Object{"capture": Capture(c, false)}
	}
	d := benchmarkDocument(count, attrs, events)
	if name == "query100" || name == "query1000" {
		return Object{"spans": d["trace"].(Object)["spans"]}
	}
	return d
}

func BenchmarkSerialization(b *testing.B) {
	for _, tc := range []struct {
		name                 string
		count, attrs, events int
	}{{"inspect", 0, 0, 0}, {"trace5", 5, 2, 1}, {"trace50", 50, 2, 1}, {"trace500", 500, 2, 1}, {"query100", 100, 0, 0}, {"query1000", 1000, 0, 0}, {"attributeHeavy", 20, 20, 0}, {"eventHeavy", 30, 2, 10}, {"diagnosticsLossHeavy", 0, 0, 0}} {
		for _, format := range []string{"json", "toon"} {
			b.Run(tc.name+"/"+format, func(b *testing.B) {
				doc := serializationSample(tc.name, tc.count, tc.attrs, tc.events)
				var out bytes.Buffer
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					out.Reset()
					if err := Write(&out, format, doc); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(out.Len()), "output-B")
			})
		}
	}
}
