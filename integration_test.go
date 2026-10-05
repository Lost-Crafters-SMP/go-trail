package trail_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/file"
)

// TestPipelineWritesJournalOnDisk drives the full pipeline through a real
// file sink and checks the resulting journal.
func TestPipelineWritesJournalOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.trail.jsonl")
	sink, err := file.Open(path)
	if err != nil {
		t.Fatalf("file.Open unexpected error: %v", err)
	}
	processor, err := trail.NewSyncProcessor(sink)
	if err != nil {
		t.Fatalf("NewSyncProcessor unexpected error: %v", err)
	}
	provider, err := trail.NewProvider(processor)
	if err != nil {
		t.Fatalf("NewProvider unexpected error: %v", err)
	}
	tracer := provider.Tracer("example/resolver")

	ctx, root := tracer.Start(context.Background(), "resolver.reconcile")
	childCtx, child := tracer.Start(ctx, "provider.lookup")
	_, grandchild := tracer.Start(childCtx, "candidate.fetch")
	grandchild.End()
	child.End()
	root.End()

	if err := provider.Flush(context.Background()); err != nil {
		t.Fatalf("provider.Flush unexpected error: %v", err)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("provider.Shutdown unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	var lines []map[string]any
	for _, line := range splitLines(string(data)) {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("decode line %q: %v", line, err)
		}
		lines = append(lines, decoded)
	}
	if len(lines) != 9 {
		t.Fatalf("journal has %d records, want 9 (header + capture + 3 starts + 3 ends + trace_end)", len(lines))
	}
	if format := lines[0]["format"]; format != "trail" {
		t.Fatalf("header format = %v, want trail", format)
	}
	if version := lines[0]["version"]; version != float64(1) {
		t.Fatalf("header version = %v, want 1", version)
	}
	wantTypes := []string{
		"capture_start", "span_start", "span_start", "span_start",
		"span_end", "span_end", "span_end", "trace_end",
	}
	for i, want := range wantTypes {
		if got := lines[i+1]["type"]; got != want {
			t.Fatalf("record %d type = %v, want %v", i+1, got, want)
		}
	}
	for i, record := range lines[1:] {
		if record["seq"] != strconv.Itoa(i+1) {
			t.Fatalf("record %d seq = %v, want %d", i+1, record["seq"], i+1)
		}
	}
	rootStart := lines[2]
	if _, ok := rootStart["parentSpanId"]; ok {
		t.Fatal("root span_start carries parentSpanId")
	}
	if rootStart["rootSpanId"] != rootStart["spanId"] {
		t.Fatalf("root rootSpanId = %v, spanId = %v", rootStart["rootSpanId"], rootStart["spanId"])
	}
	childStart := lines[3]
	if childStart["parentSpanId"] != rootStart["spanId"] {
		t.Fatalf("child parentSpanId = %v, want root %v", childStart["parentSpanId"], rootStart["spanId"])
	}
	if childStart["traceId"] != rootStart["traceId"] {
		t.Fatal("child trace differs from root trace")
	}
	traceEnd := lines[8]
	if traceEnd["rootSpanId"] != rootStart["spanId"] {
		t.Fatalf("trace_end rootSpanId = %v, want %v", traceEnd["rootSpanId"], rootStart["spanId"])
	}
	if _, ok := traceEnd["durationNano"]; ok {
		t.Fatal("trace_end carries durationNano")
	}
}

// TestUnfinishedSpanSurvivesAsJournalStart verifies crash usefulness: a span
// that never ends still leaves a reconstructable start on disk.
func TestUnfinishedSpanSurvivesAsJournalStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hang.trail.jsonl")
	sink, err := file.Open(path)
	if err != nil {
		t.Fatalf("file.Open unexpected error: %v", err)
	}
	processor, err := trail.NewSyncProcessor(sink)
	if err != nil {
		t.Fatalf("NewSyncProcessor unexpected error: %v", err)
	}
	provider, err := trail.NewProvider(processor)
	if err != nil {
		t.Fatalf("NewProvider unexpected error: %v", err)
	}
	tracer := provider.Tracer("example/hang")
	_, hanging := tracer.Start(context.Background(), "stuck.operation")
	_ = hanging // simulate a hang: the span never ends
	if err := provider.Shutdown(context.Background()); err == nil {
		t.Fatal("Shutdown with a live span succeeded, want incomplete-capture error")
	} else {
		var incomplete *trail.IncompleteError
		if !errors.As(err, &incomplete) || incomplete.UnendedSpans != 1 {
			t.Fatalf("Shutdown error = %v, want IncompleteError with 1 unended span", err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	lines := splitLines(string(data))
	if len(lines) != 3 {
		t.Fatalf("hung journal has %d lines, want 3 (header, capture_start, span_start)", len(lines))
	}
	var spanStart map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &spanStart); err != nil {
		t.Fatalf("decode hung span_start: %v", err)
	}
	if spanStart["name"] != "stuck.operation" {
		t.Fatalf("hung span name = %v", spanStart["name"])
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
