package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, errors.New("output failure") }

func matrixCapture() string {
	lines := strings.Split(streamTrace, "\n")
	update := `{"type":"span_update","seq":"3","timeUnixNano":"0","elapsedNano":"0","traceId":"11111111111111111111111111111111","spanId":"2222222222222222","rootSpanId":"2222222222222222","attributes":[{"key":"k","type":"int64","value":"1"}],"status":{"code":"error","description":"recorded failure"}}`
	return streamHeader + lines[0] + "\n" + update + "\n" + strings.Replace(lines[1], `"seq":"3"`, `"seq":"4"`, 1) + "\n" + strings.Replace(lines[2], `"seq":"4"`, `"seq":"5"`, 1) + "\n"
}

func commandArgs(command string) []string {
	args := []string{command, "capture"}
	if command == "trace" {
		args = append(args, "11111111111111111111111111111111")
	}
	if command == "span" {
		args = append(args, "2222222222222222")
	}
	return args
}

func TestCommandFlagFormatMatrix(t *testing.T) {
	flags := []struct {
		name, value, commands string
		textOnly              bool
	}{
		{"best-effort", "", "all", false}, {"strict", "", "all", false}, {"fail-on-error", "", "all", false},
		{"top", "1", "inspect query", false}, {"since", "0s", "inspect query", false}, {"until", "1s", "inspect query", false},
		{"max-depth", "1", "trace", true}, {"max-spans", "1", "trace", true}, {"no-events", "", "trace", true},
		{"sort", "duration", "trace query", false},
		{"status", "error", "query", false}, {"scope", "test", "query", false}, {"name", "operation", "query", false},
		{"min-duration", "0s", "query", false}, {"attribute", "k=int:1", "query", false}, {"incomplete", "", "query", false},
		{"trace-id", "11111111111111111111111111111111", "query", false}, {"span-id", "2222222222222222", "query", false}, {"mode", "traces", "query", false},
		{"color", "never", "all", true}, {"local-time", "", "all", true},
	}
	for _, command := range []string{"inspect", "stats", "query", "trace", "span", "export"} {
		for _, format := range []string{"text", "json", "ndjson", "compact", "toon"} {
			for _, f := range flags {
				t.Run(command+"/"+format+"/"+f.name, func(t *testing.T) {
					allowed := f.commands == "all" || strings.Contains(" "+f.commands+" ", " "+command+" ")
					if f.textOnly && format != "text" {
						allowed = false
					}
					if f.name == "sort" && command == "trace" && format != "text" {
						allowed = false
					}
					want := 0
					if !allowed {
						want = 1
					} else if f.name == "fail-on-error" {
						want = 5
					} else if f.name == "incomplete" {
						want = 4
					}
					args := append(commandArgs(command), "--format", format, "--"+f.name)
					if f.value != "" {
						args = append(args, f.value)
					}
					var out, diag bytes.Buffer
					code := runWithOpen(args, &out, &diag, func(string) (io.ReadCloser, error) {
						if !allowed {
							t.Fatal("unsupported flag opened capture")
						}
						return io.NopCloser(strings.NewReader(matrixCapture())), nil
					})
					if code != want {
						t.Fatalf("exit=%d want=%d: %s", code, want, diag.String())
					}
					if code == 1 && diag.Len() == 0 {
						t.Fatal("missing usage diagnostic")
					}
					if bytes.Contains(out.Bytes(), []byte("\x1b")) {
						t.Fatal("unexpected ANSI")
					}
				})
			}
		}
	}
	for _, command := range []string{"inspect", "stats", "query", "trace", "span", "export"} {
		t.Run(command+"/unsupported-format", func(t *testing.T) {
			var out, diag bytes.Buffer
			args := append(commandArgs(command), "--format", "xml")
			code := runWithOpen(args, &out, &diag, func(string) (io.ReadCloser, error) { t.Fatal("unsupported format opened capture"); return nil, nil })
			if code != 1 || diag.Len() == 0 {
				t.Fatalf("exit=%d: %s", code, diag.String())
			}
		})
		t.Run(command+"/help", func(t *testing.T) {
			var out, diag bytes.Buffer
			if code := run([]string{command, "--help"}, &out, &diag); code != 0 {
				t.Fatal(code)
			}
			for _, f := range flags {
				listed := bytes.Contains(out.Bytes(), []byte("--"+f.name))
				want := f.commands == "all" || strings.Contains(" "+f.commands+" ", " "+command+" ")
				if listed != want {
					t.Fatalf("help lists %s=%t want=%t", f.name, listed, want)
				}
			}
		})
	}
}

func TestExitCodesAndRecovery(t *testing.T) {
	valid := matrixCapture()
	bad := strings.Replace(valid, `"type":"span_end"`, `"type":"unknown"`, 1)
	for _, tc := range []struct {
		name, input string
		args        []string
		code        int
	}{
		{"success", valid, []string{"inspect", "capture"}, 0},
		{"error status", valid, []string{"inspect", "capture", "--fail-on-error"}, 5},
		{"missing span", valid, []string{"span", "capture", "3333333333333333"}, 3},
		{"missing trace", valid, []string{"trace", "capture", "33333333333333333333333333333333"}, 3},
		{"empty query", valid, []string{"query", "capture", "--name", "absent"}, 4},
		{"invalid ID", valid, []string{"trace", "capture", "short"}, 1},
		{"invalid attribute", valid, []string{"query", "capture", "--attribute", "key=untyped"}, 1},
		{"malformed capture", "not JSON\n", []string{"inspect", "capture"}, 2},
		{"unsupported version", strings.Replace(valid, `"version":1`, `"version":2`, 1), []string{"inspect", "capture", "--best-effort"}, 2},
		{"bad record", bad, []string{"inspect", "capture"}, 2},
		{"best effort", bad, []string{"inspect", "capture", "--best-effort"}, 0},
		{"strict best effort", bad, []string{"inspect", "capture", "--best-effort", "--strict"}, 2},
		{"strict unresolved", streamHeader + strings.Replace(strings.Split(streamTrace, "\n")[0], `"spanId":"2222222222222222"`, `"spanId":"3333333333333333","parentSpanId":"4444444444444444"`, 1) + "\n", []string{"inspect", "capture", "--strict"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			code := runWithOpen(tc.args, &out, &diag, func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(tc.input)), nil })
			if code != tc.code {
				t.Fatalf("exit=%d want=%d: %s", code, tc.code, diag.String())
			}
			if tc.name == "best effort" && !strings.Contains(out.String(), "DIAGNOSTICS") {
				t.Fatal("recovered corruption hidden")
			}
		})
	}
}

func TestOutputFailureExitCode(t *testing.T) {
	for _, command := range []string{"inspect", "stats", "query", "trace", "span", "export"} {
		for _, format := range []string{"text", "json", "ndjson", "compact", "toon"} {
			t.Run(command+"/"+format, func(t *testing.T) {
				var diag bytes.Buffer
				args := append(commandArgs(command), "--format", format)
				if code := runWithOpen(args, brokenOutput{}, &diag, func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(matrixCapture())), nil }); code != 2 || !strings.Contains(diag.String(), "output failure") {
					t.Fatalf("exit=%d: %s", code, diag.String())
				}
			})
		}
	}
}
