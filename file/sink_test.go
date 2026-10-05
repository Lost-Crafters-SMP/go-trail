package file_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/file"
)

// journalLine is a decoded journal line. Numeric fields decode from decimal
// strings, mirroring the format's precision-loss avoidance.
type journalLine struct {
	Type          string `json:"type"`
	Seq           string `json:"seq"`
	TimeUnixNano  string `json:"timeUnixNano"`
	ElapsedNano   string `json:"elapsedNano"`
	TraceID       string `json:"traceId"`
	SpanID        string `json:"spanId"`
	RootSpanID    string `json:"rootSpanId"`
	ParentSpanID  string `json:"parentSpanId"`
	Scope         string `json:"scope"`
	Name          string `json:"name"`
	DurationNano  string `json:"durationNano"`
	DroppedAttrs  string `json:"droppedAttributes"`
	DroppedEvents string `json:"droppedEvents"`
	Extra         bool   `json:"extra,omitempty"`
}

func testTraceID(last byte) trail.TraceID {
	var id trail.TraceID
	id[15] = last
	return id
}

func testSpanID(last byte) trail.SpanID {
	var id trail.SpanID
	id[7] = last
	return id
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("journal does not end with a newline")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, line := range lines {
		if line == "" {
			t.Fatalf("line %d is blank", i+1)
		}
	}
	return lines
}

func decodeLine(t *testing.T, line string) journalLine {
	t.Helper()
	var decoded journalLine
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("decode line %q: %v", line, err)
	}
	return decoded
}

func TestOpenCreatesExclusiveFileWithHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.trail.jsonl")
	sink, err := file.Open(path)
	if err != nil {
		t.Fatalf("Open unexpected error: %v", err)
	}
	if err := sink.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown unexpected error: %v", err)
	}
	if _, err := file.Open(path); err == nil {
		t.Fatal("second Open succeeded, want exclusive-create failure")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil {
			t.Fatalf("stat: %v", err)
		} else if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("file mode = %o, want 600", perm)
		}
	}
	lines := readLines(t, path)
	if len(lines) != 1 || lines[0] != `{"format":"trail","version":1}` {
		t.Fatalf("journal header = %v, want the version line only", lines)
	}
}

func TestOpenMissingDirectoryFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "capture.trail.jsonl")
	if _, err := file.Open(path); err == nil {
		t.Fatal("Open in a missing directory succeeded, want error")
	}
}

func TestWriteRecordEncodesJournalLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.trail.jsonl")
	sink, err := file.Open(path)
	if err != nil {
		t.Fatalf("Open unexpected error: %v", err)
	}
	wall := time.Unix(1700000000, 42).UTC()
	records := []trail.Record{
		trail.CaptureStart{Seq: 1, Wall: wall},
		trail.SpanStart{
			Seq: 2, Wall: wall, Elapsed: 10 * time.Nanosecond,
			TraceID: testTraceID(0x11), SpanID: testSpanID(0x22), RootSpanID: testSpanID(0x22),
			Scope: "example/resolver", Name: "resolver.reconcile",
		},
		trail.SpanStart{
			Seq: 3, Wall: wall, Elapsed: 15 * time.Nanosecond,
			TraceID: testTraceID(0x11), SpanID: testSpanID(0x33), RootSpanID: testSpanID(0x22),
			ParentSpanID: testSpanID(0x22),
			Scope:        "example/resolver", Name: "provider.lookup",
		},
		trail.SpanEnd{
			Seq: 4, Wall: wall, Elapsed: 20 * time.Nanosecond,
			TraceID: testTraceID(0x11), SpanID: testSpanID(0x22), RootSpanID: testSpanID(0x22),
			Duration: 10 * time.Nanosecond,
		},
		trail.SpanEnd{
			Seq: 5, Wall: wall, Elapsed: 25 * time.Nanosecond,
			TraceID: testTraceID(0x11), SpanID: testSpanID(0x33), RootSpanID: testSpanID(0x22),
			Duration: 10 * time.Nanosecond,
		},
		trail.TraceEnd{
			Seq: 6, Wall: wall, Elapsed: 25 * time.Nanosecond,
			TraceID: testTraceID(0x11), RootSpanID: testSpanID(0x22),
		},
	}
	for _, record := range records {
		if err := sink.WriteRecord(record); err != nil {
			t.Fatalf("WriteRecord(%T) unexpected error: %v", record, err)
		}
	}
	if err := sink.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown unexpected error: %v", err)
	}

	lines := readLines(t, path)
	if len(lines) != len(records)+1 {
		t.Fatalf("journal has %d lines, want %d", len(lines), len(records)+1)
	}
	capture := decodeLine(t, lines[1])
	if capture.Type != "capture_start" || capture.Seq != "1" || capture.ElapsedNano != "0" {
		t.Fatalf("capture_start line = %+v", capture)
	}
	if capture.TimeUnixNano != "1700000000000000042" {
		t.Fatalf("capture timeUnixNano = %s", capture.TimeUnixNano)
	}
	root := decodeLine(t, lines[2])
	if root.Type != "span_start" || root.TraceID != "00000000000000000000000000000011" ||
		root.SpanID != "0000000000000022" || root.RootSpanID != "0000000000000022" {
		t.Fatalf("root span_start line = %+v", root)
	}
	if root.ParentSpanID != "" {
		t.Fatalf("root parentSpanId = %q, want absent", root.ParentSpanID)
	}
	if root.Scope != "example/resolver" || root.Name != "resolver.reconcile" {
		t.Fatalf("root scope/name = %q/%q", root.Scope, root.Name)
	}
	child := decodeLine(t, lines[3])
	if child.ParentSpanID != "0000000000000022" || child.ElapsedNano != "15" {
		t.Fatalf("child span_start line = %+v", child)
	}
	rootEnd := decodeLine(t, lines[4])
	if rootEnd.Type != "span_end" || rootEnd.DurationNano != "10" ||
		rootEnd.DroppedAttrs != "0" || rootEnd.DroppedEvents != "0" {
		t.Fatalf("root span_end line = %+v", rootEnd)
	}
	traceEnd := decodeLine(t, lines[6])
	if traceEnd.Type != "trace_end" || traceEnd.RootSpanID != "0000000000000022" {
		t.Fatalf("trace_end line = %+v", traceEnd)
	}
}

func TestWriteRecordRejectsInvalidRecords(t *testing.T) {
	sink, err := file.Open(filepath.Join(t.TempDir(), "capture.trail.jsonl"))
	if err != nil {
		t.Fatalf("Open unexpected error: %v", err)
	}
	defer func() { _ = sink.Shutdown(context.Background()) }()
	if err := sink.WriteRecord(nil); err == nil {
		t.Fatal("WriteRecord(nil) succeeded, want error")
	}
	bad := trail.SpanStart{Seq: 1, Wall: time.Now(), SpanID: testSpanID(1), RootSpanID: testSpanID(1)}
	writeErr := sink.WriteRecord(bad)
	if writeErr == nil {
		t.Fatal("WriteRecord with zero trace ID succeeded, want error")
	}
	var fieldErr *file.FieldError
	if !errors.As(writeErr, &fieldErr) {
		t.Fatalf("error = %v, want *file.FieldError", writeErr)
	}
}

func TestNameAndScopeEscaping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.trail.jsonl")
	sink, err := file.Open(path)
	if err != nil {
		t.Fatalf("Open unexpected error: %v", err)
	}
	record := trail.SpanStart{
		Seq: 1, Wall: time.Unix(0, 0),
		TraceID: testTraceID(1), SpanID: testSpanID(1), RootSpanID: testSpanID(1),
		Scope: "we\"ird", Name: "line\nbreak \x00ctrl invalid\x80utf8",
	}
	if err := sink.WriteRecord(record); err != nil {
		t.Fatalf("WriteRecord unexpected error: %v", err)
	}
	if err := sink.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown unexpected error: %v", err)
	}
	lines := readLines(t, path)
	decoded := decodeLine(t, lines[1])
	if decoded.Scope != "we\"ird" {
		t.Fatalf("scope = %q, want escaped round trip", decoded.Scope)
	}
	if decoded.Name != "line\nbreak \x00ctrl invalid\ufffdutf8" {
		t.Fatalf("name = %q, want escaped and normalized round trip", decoded.Name)
	}
}

func TestShutdownIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.trail.jsonl")
	sink, err := file.Open(path)
	if err != nil {
		t.Fatalf("Open unexpected error: %v", err)
	}
	first := sink.Shutdown(context.Background())
	second := sink.Shutdown(context.Background())
	if first != nil || second != nil {
		t.Fatalf("repeated Shutdown = %v then %v, want nil twice", first, second)
	}
	if err := sink.WriteRecord(trail.CaptureStart{Seq: 1, Wall: time.Now()}); !errors.Is(err, file.ErrSinkShutdown) {
		t.Fatalf("WriteRecord after Shutdown error = %v, want ErrSinkShutdown", err)
	}
	if err := sink.Flush(context.Background()); !errors.Is(err, file.ErrSinkShutdown) {
		t.Fatalf("Flush after Shutdown error = %v, want ErrSinkShutdown", err)
	}
}

func TestTornTailLeavesCompleteLinesUsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.trail.jsonl")
	sink, err := file.Open(path)
	if err != nil {
		t.Fatalf("Open unexpected error: %v", err)
	}
	if err := sink.WriteRecord(trail.CaptureStart{Seq: 1, Wall: time.Unix(1700000000, 0)}); err != nil {
		t.Fatalf("WriteRecord unexpected error: %v", err)
	}
	record := trail.SpanStart{
		Seq: 2, Wall: time.Unix(1700000000, 10),
		TraceID: testTraceID(1), SpanID: testSpanID(2), RootSpanID: testSpanID(2),
		Scope: "example/resolver", Name: "resolver.reconcile",
	}
	if err := sink.WriteRecord(record); err != nil {
		t.Fatalf("WriteRecord unexpected error: %v", err)
	}
	if err := sink.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown unexpected error: %v", err)
	}

	// Simulate a crash mid-write by truncating into the last line.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	truncated := data[:len(data)-20]
	if err := os.WriteFile(path, truncated, 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// The reader contract: complete lines parse; the torn final line is
	// identified and ignored, not silently skipped.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var complete []string
	var lastComplete bool
	for scanner.Scan() {
		line := scanner.Text()
		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			lastComplete = false
			continue // torn tail: reported and ignored by readers
		}
		complete = append(complete, line)
		lastComplete = true
	}
	if len(complete) != 2 {
		t.Fatalf("recovered %d complete lines, want 2 (header and capture_start)", len(complete))
	}
	if lastComplete {
		t.Fatal("torn final line was not detected")
	}
}
