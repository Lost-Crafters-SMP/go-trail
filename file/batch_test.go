package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.lostcrafters.com/trail"
)

func TestBatchEquivalentBoundariesAndOversized(t *testing.T) {
	for _, name := range []string{"small", "exact", "oversized"} {
		t.Run(name, func(t *testing.T) {
			records := scratchRecords()
			if name != "small" {
				r := records[1].(trail.SpanStart)
				r.Name = ""
				line, err := encodeRecord(r)
				if err != nil {
					t.Fatal(err)
				}
				length := maxRetainedScratchBytes - (len(line) + 1 - len(r.Name))
				if name == "oversized" {
					length++
				}
				r.Name = strings.Repeat("x", length)
				records = []trail.Record{r, records[0]}
			}
			f := &observedFile{limit: -1}
			s := &Sink{f: f}
			var want []byte
			for _, r := range records {
				line, e := encodeRecord(r)
				if e != nil {
					t.Fatal(e)
				}
				want = append(want, line...)
				want = append(want, '\n')
			}
			if err := s.WriteRecords(records); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(f.Bytes(), want) {
				t.Fatal("batch output/FIFO differs")
			}
			if len(s.batch) != 0 || cap(s.batch) > maxRetainedScratchBytes {
				t.Fatal("pending bytes or unbounded output buffer")
			}
			if name == "small" && f.calls != 1 {
				t.Fatalf("writes=%d", f.calls)
			}
			if name != "small" && f.calls != 2 {
				t.Fatalf("boundary writes=%d", f.calls)
			}
			if name == "oversized" && cap(s.scratch) > maxRetainedScratchBytes {
				t.Fatal("oversized scratch retained")
			}
			if err := s.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := s.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(s.batch) != 0 || s.batch != nil {
				t.Fatal("shutdown retained batch")
			}
		})
	}
}

func TestBatchFailureAndTornPrefix(t *testing.T) {
	records := scratchRecords()
	first, _ := encodeRecord(records[0])
	boom := errors.New("failed")
	for _, tc := range []struct {
		name    string
		limit   int
		failure error
		fails   bool
	}{
		{"progress", 11, nil, false}, {"short-zero", 0, nil, true}, {"immediate", 0, boom, true}, {"torn-prefix", len(first) + 1 + 20, boom, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &observedFile{limit: tc.limit, failure: tc.failure}
			s := &Sink{f: f}
			err := s.WriteRecords(records)
			if (err != nil) != tc.fails {
				t.Fatalf("err=%v", err)
			}
			if !tc.fails {
				return
			}
			calls := f.calls
			if !errors.Is(s.WriteRecords(records), err) || !errors.Is(s.WriteRecord(records[0]), err) || f.calls != calls {
				t.Fatal("write after terminal failure")
			}
			if !errors.Is(s.Flush(context.Background()), err) || !errors.Is(s.Shutdown(context.Background()), err) {
				t.Fatal("terminal failure not surfaced")
			}
			if tc.name == "torn-prefix" {
				lines := bytes.Split(f.Bytes(), []byte{'\n'})
				if len(lines) != 2 || !json.Valid(lines[0]) || json.Valid(lines[1]) {
					t.Fatal("valid prefix/torn final line damaged")
				}
			}
		})
	}
}

func TestBatchValidationAndLongThenSmall(t *testing.T) {
	f := &observedFile{limit: -1}
	s := &Sink{f: f}
	if err := s.WriteRecords([]trail.Record{nil}); err == nil || f.Len() != 0 {
		t.Fatal("invalid record emitted bytes")
	}
	r := scratchRecords()[1].(trail.SpanStart)
	r.Name = strings.Repeat("x", 2*maxRetainedScratchBytes)
	records := []trail.Record{r}
	for range 100 {
		records = append(records, trail.CaptureStart{Seq: 1})
	}
	if err := s.WriteRecords(records); err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 {
		t.Fatalf("long + small writes=%d", f.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(s.Shutdown(ctx), context.Canceled) {
		t.Fatal("canceled Shutdown")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestObservedAsyncFileWrites(t *testing.T) {
	for _, target := range []int{0, 3072, 12288, 49152} {
		for _, operation := range []string{"StartEnd", "AddEvent", "SetAttributes"} {
			sink, err := Open(filepath.Join(t.TempDir(), "observed.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			observed := &countingFile{File: sink.f.(*os.File)}
			sink.f = observed
			ap, err := trail.NewAsyncProcessor(sink, trail.WithMaxQueuedRecords(2048), trail.WithMaxQueuedBytes(4<<20), trail.WithMaxBatchBytes(target))
			if err != nil {
				t.Fatal(err)
			}
			p, err := trail.NewProvider(ap)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			tracer := p.Tracer("benchmark")
			var span trail.Span
			if operation != "StartEnd" {
				_, span = tracer.Start(ctx, "work")
			}
			for range 64 {
				for range 256 {
					switch operation {
					case "StartEnd":
						_, root := tracer.Start(ctx, "work")
						if !root.IsRecording() {
							t.Fatal("rejected")
						}
						root.End()
					case "AddEvent":
						span.AddEvent("checkpoint")
					case "SetAttributes":
						span.SetAttributes(trail.String("mode", "fast"), trail.Int("count", 12))
					}
				}
				if err := p.Flush(ctx); err != nil {
					t.Fatal(err)
				}
			}
			span.End()
			if err := p.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(sink.path)
			if err != nil {
				t.Fatal(err)
			}
			records := bytes.Count(data, []byte{'\n'}) - 1
			t.Logf("target=%d operation=%s records=%d calls=%d bytes=%d writes/record=%.6f bytes/write=%.2f bytes/record=%.2f", target, operation, records, observed.calls, observed.written, float64(observed.calls)/float64(records), float64(observed.written)/float64(observed.calls), float64(observed.written)/float64(records))
			if target == 0 && observed.calls != records {
				t.Fatal("unbatched count")
			}
			if target > 0 && observed.calls >= records {
				t.Fatal("batch did not reduce output calls")
			}
		}
	}
}
