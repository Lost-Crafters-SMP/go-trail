package replay

import (
	"fmt"
	"io"
	"testing"
	"time"
)

func TestCompletedTraceDeliveredBeforeEOF(t *testing.T) {
	r, w := io.Pipe()
	delivered := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := Read(r, "capture", Options{ReleaseCompleted: true, OnTrace: func(*MachineTrace) error { delivered <- struct{}{}; return nil }})
		done <- err
	}()
	input := fixture(startLine(2, testRoot, "", 0), endLine(3, testRoot, 1, 1), fmt.Sprintf(`{"type":"trace_end","seq":"4","timeUnixNano":"0","elapsedNano":"1","traceId":%q,"rootSpanId":%q}`, testTrace, testRoot))
	if _, err := io.WriteString(w, input); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		_ = w.Close()
		t.Fatal("callback waited for EOF")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}
