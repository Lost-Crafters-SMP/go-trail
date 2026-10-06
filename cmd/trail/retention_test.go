package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"go.lostcrafters.com/trail/internal/replay"
)

type commandCapture struct {
	count, index int
	pending      *strings.Reader
}

func (g *commandCapture) Close() error { return nil }
func (g *commandCapture) Read(p []byte) (int, error) {
	if g.pending == nil {
		g.pending = strings.NewReader(streamHeader)
	}
	for {
		n, e := g.pending.Read(p)
		if n > 0 {
			return n, nil
		}
		if e != io.EOF {
			return n, e
		}
		if g.index == g.count {
			return 0, io.EOF
		}
		g.index++
		i := g.index
		seq := 2 + (i-1)*5
		tid := fmt.Sprintf("%032x", i)
		sid := fmt.Sprintf("%016x", i)
		start := i * 1000000
		duration := i % 100
		common := fmt.Sprintf(`"timeUnixNano":"0","traceId":%q,"rootSpanId":%q`, tid, sid)
		g.pending = strings.NewReader(fmt.Sprintf("{\"type\":\"span_start\",\"seq\":\"%d\",%s,\"elapsedNano\":\"%d\",\"spanId\":%q,\"name\":\"operation\",\"scope\":\"test\"}\n{\"type\":\"span_update\",\"seq\":\"%d\",%s,\"elapsedNano\":\"%d\",\"spanId\":%q,\"attributes\":[{\"key\":\"payload\",\"type\":\"string\",\"value\":%q}]}\n{\"type\":\"event\",\"seq\":\"%d\",%s,\"elapsedNano\":\"%d\",\"spanId\":%q,\"name\":\"checkpoint\"}\n{\"type\":\"span_end\",\"seq\":\"%d\",%s,\"elapsedNano\":\"%d\",\"spanId\":%q,\"durationNano\":\"%d\",\"droppedAttributes\":\"0\",\"droppedEvents\":\"0\",\"droppedStatusUpdates\":\"0\"}\n{\"type\":\"trace_end\",\"seq\":\"%d\",%s,\"elapsedNano\":\"%d\"}\n", seq, common, start, sid, seq+1, common, start, sid, strings.Repeat("x", 512), seq+2, common, start, sid, seq+3, common, start+duration, sid, duration, seq+4, common, start+duration))
	}
}

func TestCommandRetentionProfiles(t *testing.T) {
	count, fullCount := 64, 64
	if os.Getenv("TRAIL_COMMAND_LARGE") == "1" {
		count, fullCount = 500000, 10000
	}
	for _, tc := range []struct {
		name         string
		args         []string
		full, target bool
		code         int
	}{
		{"inspect", []string{"inspect", "synthetic", "--format", "json"}, false, false, 0},
		{"stats", []string{"stats", "synthetic", "--format", "json"}, false, false, 0},
		{"query spans", []string{"query", "synthetic", "--format", "ndjson"}, false, false, 0},
		{"query nonmatches", []string{"query", "synthetic", "--name", "never", "--format", "json"}, false, false, 4},
		{"query top", []string{"query", "synthetic", "--top", "10", "--format", "json"}, false, false, 0},
		{"query traces", []string{"query", "synthetic", "--mode", "traces", "--top", "10", "--format", "json"}, false, false, 0},
		{"trace", []string{"trace", "synthetic", "00000000000000000000000000000001", "--format", "json"}, false, true, 0},
		{"span", []string{"span", "synthetic", "0000000000000001", "--format", "json"}, false, true, 0},
		{"export ndjson", []string{"export", "synthetic", "--format", "ndjson"}, false, false, 0},
		{"export compact", []string{"export", "synthetic", "--format", "compact"}, false, false, 0},
		{"export json", []string{"export", "synthetic", "--format", "json"}, true, false, 0},
		{"export toon", []string{"export", "synthetic", "--format", "toon"}, true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := count
			if tc.full {
				n = fullCount
			}
			var diag bytes.Buffer
			passes := []replay.RetentionStats{}
			code := runObserved(tc.args, io.Discard, &diag, func(string) (io.ReadCloser, error) { return &commandCapture{count: n}, nil }, func(s replay.RetentionStats) { passes = append(passes, s) })
			if code != tc.code {
				t.Fatalf("exit=%d: %s", code, diag.String())
			}
			first := passes[0]
			if first.ReconstructedSpans != uint64(n) {
				t.Fatalf("spans reconstructed=%d", first.ReconstructedSpans)
			}
			if tc.full {
				if first.RetainedSpans != n || first.RetainedAttributes != n || first.RetainedEvents != n || first.CompletionIDs != 0 {
					t.Fatalf("full reconstruction: %+v", first)
				}
			} else {
				if first.RetainedSpans != 0 || first.RetainedAttributes != 0 || first.RetainedEvents != 0 || first.PeakSpans != 1 || first.PeakAttributes != 1 || first.PeakEvents != 1 || first.CompletionIDs != n {
					t.Fatalf("release profile: %+v", first)
				}
			}
			if tc.target {
				if len(passes) != 2 || passes[1].ReconstructedSpans != 1 || passes[1].RetainedSpans != 1 || passes[1].RetainedAttributes != 1 || passes[1].RetainedEvents != 1 {
					t.Fatalf("targeted passes: %+v", passes)
				}
			}
			t.Logf("spans=%d passes=%+v completion-ID key bytes=%d (excluding map overhead)", n, passes, first.CompletionIDs*16)
		})
	}
}
