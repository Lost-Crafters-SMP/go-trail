package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestHelpTopicsAndPositions(t *testing.T) {
	for _, args := range [][]string{
		{"help", "inspect"}, {"help", "trace"},
		{"inspect", "unused", "--help"}, {"trace", "unused", "11111111111111111111111111111111", "--help"},
		{"span", "--help", "unused"}, {"query", "unused", "-h"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diag bytes.Buffer
			code := runWithOpen(args, &out, &diag, func(string) (io.ReadCloser, error) { t.Fatal("help opened a capture"); return nil, nil })
			if code != 0 || !bytes.Contains(out.Bytes(), []byte("USAGE:")) {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, out.String(), diag.String())
			}
		})
	}
}

func TestInterspersedFlagsAndRepeatedAttributes(t *testing.T) {
	input := streamHeader + strings.Split(streamTrace, "\n")[0] + "\n" + `{"type":"span_update","seq":"3","timeUnixNano":"0","elapsedNano":"0","traceId":"11111111111111111111111111111111","spanId":"2222222222222222","rootSpanId":"2222222222222222","attributes":[{"key":"label","type":"string","value":"a,b=c"},{"key":"enabled","type":"bool","value":false}]}` + "\n"
	var expected []byte
	for _, args := range [][]string{
		{"query", "capture", "--format", "json", "--attribute", "label=str:a,b=c", "--attribute", "enabled=bool:false"},
		{"query", "--format", "json", "--attribute", "label=str:a,b=c", "capture", "--attribute", "enabled=bool:false"},
		{"query", "--attribute=enabled=bool:false", "--format=json", "--attribute=label=str:a,b=c", "capture"},
	} {
		var out, diag bytes.Buffer
		code := runWithOpen(args, &out, &diag, func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(input)), nil })
		if code != 0 {
			t.Fatalf("%v: exit=%d stderr=%s", args, code, diag.String())
		}
		if expected == nil {
			expected = bytes.Clone(out.Bytes())
		} else if !bytes.Equal(out.Bytes(), expected) {
			t.Fatalf("argument order changed output: %v", args)
		}
	}
}

func TestParserUsageFailures(t *testing.T) {
	for _, args := range [][]string{
		{}, {"unknown"}, {"help", "unknown"}, {"inspect"}, {"trace", "capture"},
		{"inspect", "capture", "extra"}, {"trace", "capture", "bad-id"},
		{"stats", "capture", "--name", "unsupported"}, {"query", "capture", "--unknown"},
		{"query", "capture", "--top", "NaN"}, {"query", "capture", "--top", "-1"},
		{"query", "capture", "--mode", "other"}, {"query", "capture", "--format", "xml"},
		{"query", "capture", "--attribute", "label=str:a,b", "--attribute", "broken"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diag bytes.Buffer
			code := runWithOpen(args, &out, &diag, func(string) (io.ReadCloser, error) { t.Fatal("invalid arguments opened a capture"); return nil, nil })
			if code != 1 || diag.Len() == 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, out.String(), diag.String())
			}
		})
	}
}

func TestTargetedReplayRejectsChangedBytes(t *testing.T) {
	for _, command := range []string{"trace", "span"} {
		t.Run(command, func(t *testing.T) {
			opens := 0
			var out, diag bytes.Buffer
			code := runWithOpen(commandArgs(command), &out, &diag, func(string) (io.ReadCloser, error) {
				opens++
				input := matrixCapture()
				if opens == 2 {
					input = strings.Replace(input, `"name":"operation"`, `"name":"changed"`, 1)
				}
				return io.NopCloser(strings.NewReader(input)), nil
			})
			if code != 2 || out.Len() != 0 || !strings.Contains(diag.String(), "capture changed") {
				t.Fatalf("exit=%d output=%s stderr=%s", code, out.String(), diag.String())
			}
		})
	}
}
