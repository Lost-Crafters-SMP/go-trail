package file_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/file"
)

func tempJournal(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "capture.trail.jsonl")
}

func writeAndReadLine(t *testing.T, record trail.Record) journalLine {
	t.Helper()
	path := tempJournal(t)
	sink, err := file.Open(path)
	if err != nil {
		t.Fatalf("Open unexpected error: %v", err)
	}
	if err := sink.WriteRecord(record); err != nil {
		t.Fatalf("WriteRecord(%T) unexpected error: %v", record, err)
	}
	if err := sink.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown unexpected error: %v", err)
	}
	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("journal has %d lines, want header and record", len(lines))
	}
	return decodeLine(t, lines[1])
}

func TestSpanUpdateEncodesRouting(t *testing.T) {
	record := trail.SpanUpdate{
		Seq: 7, Wall: time.Unix(1700000000, 22), Elapsed: 22 * time.Nanosecond,
		TraceID: testTraceID(0x11), SpanID: testSpanID(0x33), RootSpanID: testSpanID(0x22),
		Attributes: []trail.Attribute{trail.String("mode", "fast")},
	}
	decoded := writeAndReadLine(t, record)
	if decoded.Type != "span_update" || decoded.Seq != "7" || decoded.ElapsedNano != "22" {
		t.Fatalf("span_update line = %+v", decoded)
	}
	if decoded.Scope != "" || decoded.Name != "" || decoded.DurationNano != "" {
		t.Fatal("span_update carries span-only fields")
	}
}

func TestSpanUpdateEncodesStatus(t *testing.T) {
	record := trail.SpanUpdate{
		Seq: 6, Wall: time.Unix(1700000000, 22), Elapsed: 22 * time.Nanosecond,
		TraceID: testTraceID(0x11), SpanID: testSpanID(0x33), RootSpanID: testSpanID(0x22),
		Status: &trail.SpanStatus{Code: trail.StatusError, Description: "lookup failed"},
	}
	decoded := writeAndReadLine(t, record)
	if decoded.Type != "span_update" || decoded.Seq != "6" {
		t.Fatalf("span_update line = %+v", decoded)
	}
}

func TestEventEncodesNameAndAttributes(t *testing.T) {
	record := trail.Event{
		Seq: 4, Wall: time.Unix(1700000000, 15), Elapsed: 15 * time.Nanosecond,
		TraceID: testTraceID(0x11), SpanID: testSpanID(0x33), RootSpanID: testSpanID(0x22),
		Name: "cache.miss",
		Attributes: []trail.Attribute{
			trail.Int("candidates", 12),
		},
	}
	decoded := writeAndReadLine(t, record)
	if decoded.Type != "event" || decoded.Name != "cache.miss" || decoded.Seq != "4" {
		t.Fatalf("event line = %+v", decoded)
	}
	if decoded.DurationNano != "" {
		t.Fatal("event carries durationNano")
	}
}

func TestSpanUpdateRequiresContent(t *testing.T) {
	sink, err := file.Open(tempJournal(t))
	if err != nil {
		t.Fatalf("Open unexpected error: %v", err)
	}
	defer func() { _ = sink.Shutdown(context.Background()) }()
	empty := trail.SpanUpdate{
		Seq: 1, Wall: time.Now(),
		TraceID: testTraceID(1), SpanID: testSpanID(1), RootSpanID: testSpanID(1),
	}
	if err := sink.WriteRecord(empty); err == nil {
		t.Fatal("empty span_update accepted, want error")
	}
}

func TestAttributeAndStatusWireValues(t *testing.T) {
	path := tempJournal(t)
	sink, err := file.Open(path)
	if err != nil {
		t.Fatalf("Open unexpected error: %v", err)
	}
	event := trail.Event{
		Seq: 1, Wall: time.Unix(0, 0),
		TraceID: testTraceID(1), SpanID: testSpanID(1), RootSpanID: testSpanID(1),
		Name: "attrs",
		Attributes: []trail.Attribute{
			trail.Bool("b", true),
			trail.Duration("d", 1500*time.Nanosecond),
			trail.Float64("f", 2.5),
			trail.Int("i", -3),
			trail.String("s", "text"),
			trail.Strings("ss", []string{"x", "y"}),
			trail.Uint64("u", 1<<63),
		},
	}
	if err := sink.WriteRecord(event); err != nil {
		t.Fatalf("WriteRecord unexpected error: %v", err)
	}
	withDescription := trail.SpanUpdate{
		Seq: 2, Wall: time.Unix(0, 0),
		TraceID: testTraceID(1), SpanID: testSpanID(1), RootSpanID: testSpanID(1),
		Status: &trail.SpanStatus{Code: trail.StatusError, Description: "boom"},
	}
	if err := sink.WriteRecord(withDescription); err != nil {
		t.Fatalf("WriteRecord unexpected error: %v", err)
	}
	withoutDescription := trail.SpanUpdate{
		Seq: 3, Wall: time.Unix(0, 0),
		TraceID: testTraceID(1), SpanID: testSpanID(1), RootSpanID: testSpanID(1),
		Status: &trail.SpanStatus{Code: trail.StatusOK},
	}
	if err := sink.WriteRecord(withoutDescription); err != nil {
		t.Fatalf("WriteRecord unexpected error: %v", err)
	}
	if err := sink.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown unexpected error: %v", err)
	}

	lines := readLines(t, path)
	eventLine := lines[1]
	wantSubstrings := []string{
		`"attributes":[{"key":"b","type":"bool","value":true}`,
		`{"key":"d","type":"duration","value":"1500"}`,
		`{"key":"f","type":"float64","value":2.5}`,
		`{"key":"i","type":"int64","value":"-3"}`,
		`{"key":"s","type":"string","value":"text"}`,
		`{"key":"ss","type":"strings","value":["x","y"]}`,
		`{"key":"u","type":"uint64","value":"9223372036854775808"}`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(eventLine, want) {
			t.Fatalf("event line missing %s\n%s", want, eventLine)
		}
	}
	if !strings.Contains(lines[2], `"status":{"code":"error","description":"boom"}`) {
		t.Fatalf("error status line = %s", lines[2])
	}
	if !strings.Contains(lines[3], `"status":{"code":"ok"}`) {
		t.Fatalf("ok status line = %s", lines[3])
	}
}
