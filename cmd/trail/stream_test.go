package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type lineWriter struct {
	pending []byte
	lines   chan []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	for {
		n := bytes.IndexByte(w.pending, '\n')
		if n < 0 {
			break
		}
		line := bytes.Clone(w.pending[:n])
		w.pending = w.pending[n+1:]
		w.lines <- line
	}
	return len(p), nil
}

const streamHeader = "{\"format\":\"trail\",\"version\":1}\n{\"type\":\"capture_start\",\"seq\":\"1\",\"timeUnixNano\":\"0\",\"elapsedNano\":\"0\"}\n"
const streamTrace = `{"type":"span_start","seq":"2","timeUnixNano":"0","elapsedNano":"0","traceId":"11111111111111111111111111111111","spanId":"2222222222222222","rootSpanId":"2222222222222222","name":"operation","scope":"test"}
{"type":"span_end","seq":"3","timeUnixNano":"1","elapsedNano":"1","traceId":"11111111111111111111111111111111","spanId":"2222222222222222","rootSpanId":"2222222222222222","durationNano":"1","droppedAttributes":"0","droppedEvents":"0","droppedStatusUpdates":"0"}
{"type":"trace_end","seq":"4","timeUnixNano":"1","elapsedNano":"1","traceId":"11111111111111111111111111111111","rootSpanId":"2222222222222222"}
`

func TestNDJSONCommandsBeforeEOF(t *testing.T) {
	for _, command := range []string{"export", "query"} {
		t.Run(command, func(t *testing.T) {
			r, w := io.Pipe()
			defer func() { _ = w.Close(); _ = r.Close() }()
			lines := make(chan []byte, 16)
			output := &lineWriter{lines: lines}
			done := make(chan int, 1)
			var diag bytes.Buffer
			go func() {
				done <- runWithOpen([]string{command, "staged", "--format", "ndjson"}, output, &diag, func(string) (io.ReadCloser, error) { return r, nil })
			}()
			read := func(want string) {
				t.Helper()
				select {
				case line := <-lines:
					var o map[string]any
					if err := json.Unmarshal(line, &o); err != nil {
						t.Fatal(err)
					}
					if o["type"] != want || o["schema_version"] != "trail.cli.v1" {
						t.Fatalf("unexpected item: %s", line)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("no output before EOF")
				}
			}
			if _, err := io.WriteString(w, streamHeader+streamTrace); err != nil {
				t.Fatal(err)
			}
			read("span")
			if command == "export" {
				read("trace")
			}
			second := strings.NewReplacer(`"seq":"2"`, `"seq":"5"`, `"seq":"3"`, `"seq":"6"`, `"seq":"4"`, `"seq":"7"`, "11111111111111111111111111111111", "33333333333333333333333333333333", "2222222222222222", "4444444444444444").Replace(streamTrace)
			if _, err := io.WriteString(w, second); err != nil {
				t.Fatal(err)
			}
			read("span")
			if command == "export" {
				read("trace")
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if command == "export" {
				read("capture_summary")
			} else {
				read("query_summary")
			}
			select {
			case code := <-done:
				if code != 0 {
					t.Fatalf("exit=%d: %s", code, diag.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("command failed to finish")
			}
		})
	}
}

func TestUnsupportedFlags(t *testing.T) {
	for _, args := range [][]string{{"stats", "unused", "--name", "x"}, {"trace", "unused", "11111111111111111111111111111111", "--format", "json", "--no-events"}, {"export", "unused", "--format", "json", "--color", "always"}} {
		var out, diag bytes.Buffer
		if code := run(args, &out, &diag); code != 1 || diag.Len() == 0 {
			t.Fatalf("%v: exit=%d diagnostics=%q", args, code, diag.String())
		}
	}
}

func TestTargetedReplayTwoPass(t *testing.T) {
	input := streamHeader + streamTrace
	for _, tc := range []struct{ command, id, key string }{{"trace", "11111111111111111111111111111111", "trace"}, {"span", "2222222222222222", "span"}} {
		t.Run(tc.command, func(t *testing.T) {
			opens := 0
			var out, diag bytes.Buffer
			code := runWithOpen([]string{tc.command, "capture", tc.id, "--format", "json"}, &out, &diag, func(string) (io.ReadCloser, error) { opens++; return io.NopCloser(strings.NewReader(input)), nil })
			if code != 0 || opens != 2 {
				t.Fatalf("exit=%d opens=%d: %s", code, opens, diag.String())
			}
			var o map[string]any
			if err := json.Unmarshal(out.Bytes(), &o); err != nil {
				t.Fatal(err)
			}
			if o[tc.key] == nil {
				t.Fatal("target absent")
			}
		})
	}
}

type waitingReader struct {
	*io.PipeReader
	reads   int
	waiting chan struct{}
}

func (r *waitingReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 2 {
		close(r.waiting)
	}
	return r.PipeReader.Read(p)
}

func TestNDJSONTraceFinalAncestry(t *testing.T) {
	for _, command := range []string{"export", "query"} {
		t.Run(command, func(t *testing.T) {
			pr, w := io.Pipe()
			r := &waitingReader{PipeReader: pr, waiting: make(chan struct{})}
			defer func() { _ = w.Close(); _ = r.Close() }()
			lines := make(chan []byte, 16)
			done := make(chan int, 1)
			var diag bytes.Buffer
			go func() {
				done <- runWithOpen([]string{command, "staged", "--format", "ndjson"}, &lineWriter{lines: lines}, &diag, func(string) (io.ReadCloser, error) { return r, nil })
			}()
			prefix := streamHeader + strings.Join(strings.Split(streamTrace, "\n")[:2], "\n") + "\n"
			if _, err := io.WriteString(w, prefix); err != nil {
				t.Fatal(err)
			}
			select {
			case <-r.waiting:
			case <-time.After(5 * time.Second):
				t.Fatal("replay did not consume prefix")
			}
			select {
			case item := <-lines:
				t.Fatalf("emitted before final ancestry: %s", item)
			default:
			}
			child := strings.NewReplacer(`"seq":"2"`, `"seq":"4"`, `"seq":"3"`, `"seq":"5"`, `"spanId":"2222222222222222"`, `"spanId":"3333333333333333"`).Replace(strings.Join(strings.Split(streamTrace, "\n")[:2], "\n"))
			child = strings.Replace(child, `"name":"operation"`, `"parentSpanId":"2222222222222222","name":"child"`, 1)
			completion := strings.Replace(strings.Split(streamTrace, "\n")[2], `"seq":"4"`, `"seq":"6"`, 1)
			if _, err := io.WriteString(w, child+"\n"+completion+"\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case item := <-lines:
				var root map[string]any
				if err := json.Unmarshal(item, &root); err != nil {
					t.Fatal(err)
				}
				children := root["children"].([]any)
				if len(children) != 1 || children[0] != "3333333333333333" {
					t.Fatalf("lost later child: %s", item)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no finalized span before EOF")
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-done:
				if code != 0 {
					t.Fatalf("exit=%d: %s", code, diag.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("replay did not finish")
			}
		})
	}
}
