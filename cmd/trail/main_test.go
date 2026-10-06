package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"inspect", "unused", "--help"}} {
		var out, diag bytes.Buffer
		if code := run(args, &out, &diag); code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}
		if out.Len()+diag.Len() == 0 {
			t.Fatalf("%v: missing help", args)
		}
	}
}

func TestCommandHelpWithoutCapture(t *testing.T) {
	for _, command := range []string{"inspect", "trace", "span", "query", "stats", "export"} {
		for _, help := range []string{"--help", "-h"} {
			t.Run(command+help, func(t *testing.T) {
				var out, diag bytes.Buffer
				code := runWithOpen([]string{command, help}, &out, &diag, func(string) (io.ReadCloser, error) { t.Fatal("help tried to open a capture"); return nil, nil })
				if code != 0 || !bytes.Contains(out.Bytes(), []byte("trail "+command)) || !bytes.Contains(out.Bytes(), []byte("USAGE:")) {
					t.Fatalf("exit=%d stdout=%s stderr=%s", code, out.String(), diag.String())
				}
				if command != "query" && bytes.Contains(out.Bytes(), []byte("--attribute")) {
					t.Fatal("help advertises unsupported query flags")
				}
			})
		}
	}
}

func TestCommandsAndFormats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.trail.jsonl")
	const trace = "11111111111111111111111111111111"
	const span = "2222222222222222"
	input := "{\"format\":\"trail\",\"version\":1}\n" + `{"type":"capture_start","seq":"1","timeUnixNano":"0","elapsedNano":"0"}` + "\n" + `{"type":"span_start","seq":"2","timeUnixNano":"0","elapsedNano":"0","traceId":"11111111111111111111111111111111","spanId":"2222222222222222","rootSpanId":"2222222222222222","name":"operation","scope":"test"}` + "\n"
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"inspect", "trace", "span", "query", "stats", "export"} {
		for _, format := range []string{"text", "compact", "json", "ndjson", "toon"} {
			t.Run(command+"/"+format, func(t *testing.T) {
				args := []string{command, path}
				if command == "trace" {
					args = append(args, trace)
				}
				if command == "span" {
					args = append(args, span)
				}
				args = append(args, "--format", format)
				var out, diag bytes.Buffer
				if code := run(args, &out, &diag); code != 0 {
					t.Fatalf("exit=%d: %s", code, diag.String())
				}
				if out.Len() == 0 {
					t.Fatal("empty output")
				}
				if bytes.Contains(out.Bytes(), []byte("\x1b")) {
					t.Fatal("unexpected ANSI")
				}
			})
		}
	}
	for _, tc := range []struct {
		args []string
		code int
	}{{[]string{"span", path, "3333333333333333"}, 3}, {[]string{"query", path, "--name", "absent"}, 4}, {[]string{"query", path, "--attribute", "broken"}, 1}, {[]string{"trace", path, "111"}, 1}} {
		var out, diag bytes.Buffer
		if code := run(tc.args, &out, &diag); code != tc.code {
			t.Fatalf("%v: exit %d want %d", tc.args, code, tc.code)
		}
	}
}
