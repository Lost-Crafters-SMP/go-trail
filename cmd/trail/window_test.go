package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestInspectWindowSemantics(t *testing.T) {
	data, err := io.ReadAll(&commandCapture{count: 3})
	if err != nil {
		t.Fatal(err)
	}
	input := strings.ReplaceAll(string(data), `"attributes":`, `"status":{"code":"error"},"attributes":`) + `{"type":"span_start","seq":"17","timeUnixNano":"0","elapsedNano":"4000000","traceId":"00000000000000000000000000000004","spanId":"0000000000000004","rootSpanId":"0000000000000004","name":"incomplete","scope":"test"}` + "\n" + `{"type":"span_update","seq":"18","timeUnixNano":"0","elapsedNano":"4000000","traceId":"00000000000000000000000000000004","spanId":"0000000000000004","rootSpanId":"0000000000000004","status":{"code":"error"}}` + "\n"
	for _, tc := range []struct {
		name                     string
		flags                    []string
		slow, errors, incomplete int
	}{
		{"no window", nil, 3, 4, 1}, {"since", []string{"--since", "2ms"}, 2, 3, 1}, {"until", []string{"--until", "2ms"}, 2, 2, 0}, {"both", []string{"--since", "2ms", "--until", "3ms"}, 2, 2, 0}, {"empty", []string{"--since", "10ms"}, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			args := append([]string{"inspect", "capture", "--format", "json"}, tc.flags...)
			if code := runWithOpen(args, &out, &diag, func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(input)), nil }); code != 0 {
				t.Fatalf("exit=%d: %s", code, diag.String())
			}
			var d map[string]any
			if err := json.Unmarshal(out.Bytes(), &d); err != nil {
				t.Fatal(err)
			}
			c := d["capture"].(map[string]any)
			if c["traces_total"] != "4" || c["spans_total"] != "4" || c["events_total"] != "3" || c["traces_completed"] != "3" {
				t.Fatalf("window changed global totals: %v", c)
			}
			for _, item := range []struct {
				key   string
				count int
			}{{"slowest_traces", tc.slow}, {"error_spans", tc.errors}, {"incomplete_traces", tc.incomplete}} {
				if len(c[item.key].([]any)) != item.count {
					t.Fatalf("%s=%v", item.key, c[item.key])
				}
			}
			_, windowed := c["detail_window"]
			if windowed != (len(tc.flags) > 0) {
				t.Fatal("window metadata absent or invented")
			}
			out.Reset()
			args = append([]string{"inspect", "capture"}, tc.flags...)
			if code := runWithOpen(args, &out, &diag, func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(input)), nil }); code != 0 {
				t.Fatal(code)
			}
			if !strings.Contains(out.String(), "CAPTURE TOTALS (whole file)") {
				t.Fatal("unlabelled global totals")
			}
			if windowed && !strings.Contains(out.String(), "DETAIL WINDOW") {
				t.Fatal("unlabelled detail window")
			}
		})
	}
}
