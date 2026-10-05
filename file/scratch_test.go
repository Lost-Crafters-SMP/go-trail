package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.lostcrafters.com/trail"
)

type observedFile struct {
	bytes.Buffer
	calls   int
	limit   int
	failure error
}

func (f *observedFile) Write(b []byte) (int, error) {
	f.calls++
	if f.limit >= 0 && f.limit < len(b) {
		b = b[:f.limit]
	}
	n, _ := f.Buffer.Write(b)
	return n, f.failure
}
func (*observedFile) Sync() error  { return nil }
func (*observedFile) Close() error { return nil }

func scratchRecords() []trail.Record {
	tid, sid := trail.TraceID{1}, trail.SpanID{1}
	return []trail.Record{
		trail.CaptureStart{Seq: 1},
		trail.SpanStart{Seq: 2, TraceID: tid, SpanID: sid, RootSpanID: sid, Name: "escape\n\x00\x80", Scope: "scope"},
		trail.Event{Seq: 3, TraceID: tid, SpanID: sid, RootSpanID: sid, Name: "event", Attributes: []trail.Attribute{trail.String("key", "value"), trail.Strings("list", []string{"a", "b"})}},
		trail.SpanUpdate{Seq: 4, TraceID: tid, SpanID: sid, RootSpanID: sid, Status: &trail.SpanStatus{Code: trail.StatusError, Description: "failed"}},
		trail.SpanEnd{Seq: 5, TraceID: tid, SpanID: sid, RootSpanID: sid},
		trail.TraceEnd{Seq: 6, TraceID: tid, RootSpanID: sid},
		trail.LossSummary{Seq: 7},
	}
}

func TestScratchEquivalentAndBounded(t *testing.T) {
	f := &observedFile{limit: -1}
	s := &Sink{f: f}
	var want []byte
	for _, r := range scratchRecords() {
		line, err := encodeRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, line...)
		want = append(want, '\n')
		if err := s.WriteRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(f.Bytes(), want) {
		t.Fatal("scratch output differs from nil-buffer encoding")
	}
	if f.calls != 7 {
		t.Fatalf("writes=%d", f.calls)
	}
	r := scratchRecords()[1].(trail.SpanStart)
	r.Name = strings.Repeat("x", 2*maxRetainedScratchBytes)
	if err := s.WriteRecord(r); err != nil {
		t.Fatal(err)
	}
	if s.scratch != nil {
		t.Fatal("oversized record retained scratch")
	}
	if err := s.WriteRecord(trail.CaptureStart{Seq: 8}); err != nil {
		t.Fatal(err)
	}
	if cap(s.scratch) > maxRetainedScratchBytes {
		t.Fatal("retention limit exceeded")
	}
	before := f.Len()
	if err := s.WriteRecord(nil); err == nil {
		t.Fatal("unsupported record accepted")
	}
	if err := s.WriteRecord(trail.SpanStart{Seq: 1}); err == nil {
		t.Fatal("invalid IDs accepted")
	}
	if f.Len() != before {
		t.Fatal("validation wrote bytes")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.scratch != nil {
		t.Fatal("closed sink retained scratch")
	}
}

func TestScratchStandaloneConcurrent(t *testing.T) {
	f := &observedFile{limit: -1}
	s := &Sink{f: f}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				if err := s.WriteRecord(trail.CaptureStart{Seq: 1}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	lines := bytes.Split(bytes.TrimSpace(f.Bytes()), []byte{'\n'})
	if len(lines) != 800 {
		t.Fatalf("lines=%d", len(lines))
	}
	for _, line := range lines {
		if !json.Valid(line) {
			t.Fatal("interleaved output")
		}
	}
}

func TestScratchWriteFailures(t *testing.T) {
	boom := errors.New("write failed")
	for _, tc := range []struct {
		name    string
		limit   int
		failure error
		wantErr bool
	}{
		{"short-progress", 7, nil, false}, {"zero", 0, nil, true}, {"partial-error", 7, boom, true}, {"immediate-error", 0, boom, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &observedFile{limit: tc.limit, failure: tc.failure}
			s := &Sink{f: f}
			err := s.WriteRecord(trail.CaptureStart{Seq: 1})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v", err)
			}
			if tc.wantErr {
				calls := f.calls
				if !errors.Is(s.WriteRecord(trail.CaptureStart{Seq: 2}), err) || f.calls != calls {
					t.Fatal("terminal write retried")
				}
				if !errors.Is(s.Flush(context.Background()), err) {
					t.Fatal("Flush lost failure")
				}
			}
		})
	}
}

// countingFile counts actual os.File.Write invocations, not kernel retries.
type countingFile struct {
	*os.File
	calls, written int
}

func (f *countingFile) Write(b []byte) (int, error) {
	f.calls++
	n, e := f.File.Write(b)
	f.written += n
	return n, e
}

func TestMeasuredWrites(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "count.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	c := &countingFile{File: f}
	s := &Sink{f: c}
	for _, r := range scratchRecords() {
		if err := s.WriteRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Logf("records=7 os.File.Write calls=%d bytes=%d bytes/write=%.2f", c.calls, c.written, float64(c.written)/float64(c.calls))
	if c.calls != 7 {
		t.Fatalf("writes=%d", c.calls)
	}
}

func TestWriteAllZeroProgress(t *testing.T) {
	n, err := writeAll(&observedFile{limit: 0}, []byte("line\n"))
	if n != 0 || err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
