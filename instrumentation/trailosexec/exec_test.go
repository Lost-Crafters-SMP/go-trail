package trailosexec_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/instrumentation/trailosexec"
)

// recordingSink is a test sink that owns copies of the records it retains.
type recordingSink struct {
	mu      sync.Mutex
	records []trail.Record
}

func (s *recordingSink) WriteRecord(r trail.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, cloneRecord(r))
	return nil
}

func (s *recordingSink) Flush(context.Context) error    { return nil }
func (s *recordingSink) Shutdown(context.Context) error { return nil }

func (s *recordingSink) snapshot() []trail.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records)
}

func cloneRecord(r trail.Record) trail.Record {
	switch rec := r.(type) {
	case trail.SpanUpdate:
		rec.Attributes = cloneAttrs(rec.Attributes)
		return rec
	case trail.Event:
		rec.Attributes = cloneAttrs(rec.Attributes)
		return rec
	default:
		return r
	}
}

func cloneAttrs(attrs []trail.Attribute) []trail.Attribute {
	if attrs == nil {
		return nil
	}
	out := make([]trail.Attribute, len(attrs))
	for i, a := range attrs {
		switch a.Kind() {
		case trail.KindString:
			out[i] = trail.String(a.Key(), a.String())
		case trail.KindBool:
			out[i] = trail.Bool(a.Key(), a.Bool())
		case trail.KindInt64:
			out[i] = trail.Int64(a.Key(), a.Int64())
		case trail.KindUint64:
			out[i] = trail.Uint64(a.Key(), a.Uint64())
		case trail.KindFloat64:
			out[i] = trail.Float64(a.Key(), a.Float64())
		case trail.KindDuration:
			out[i] = trail.Duration(a.Key(), a.Duration())
		case trail.KindStrings:
			out[i] = trail.Strings(a.Key(), slices.Clone(a.Strings()))
		default:
			out[i] = a
		}
	}
	return out
}

func newTestTracer(t *testing.T) (trail.Tracer, *recordingSink) {
	t.Helper()
	sink := &recordingSink{}
	proc, err := trail.NewSyncProcessor(sink)
	if err != nil {
		t.Fatalf("new sync processor: %v", err)
	}
	p, err := trail.NewProvider(proc)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(func() {
		if err := p.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return p.Tracer("test"), sink
}

// TestHelperProcess is not a real test. It is the child process used by the
// suite, selected through environment variables.
func TestHelperProcess(_ *testing.T) {
	if os.Getenv("TRAILOEXEC_HELPER") == "" {
		return
	}
	defer os.Exit(0)
	switch os.Getenv("TRAILOEXEC_MODE") {
	case "exit3":
		os.Exit(3)
	case "sleep":
		time.Sleep(30 * time.Second)
	case "stdout":
		if _, err := os.Stdout.WriteString("stdout-data"); err != nil {
			os.Exit(1)
		}
	case "stderr":
		if _, err := os.Stderr.WriteString("stderr-data"); err != nil {
			os.Exit(1)
		}
	}
}

func helperCommand(t *testing.T, mode string, args ...string) *trailosexec.Cmd {
	t.Helper()
	arguments := append([]string{"-test.run=^TestHelperProcess$"}, args...)
	cmd := trailosexec.Command(os.Args[0], arguments...)
	cmd.Env = append(os.Environ(),
		"TRAILOEXEC_HELPER=1",
		"TRAILOEXEC_MODE="+mode,
	)
	return cmd
}

func traced(t *testing.T, cmd *trailosexec.Cmd, opts ...trailosexec.Option) (*trailosexec.Cmd, *recordingSink) {
	t.Helper()
	tracer, sink := newTestTracer(t)
	all := append([]trailosexec.Option{trailosexec.WithTracer(tracer)}, opts...)
	return cmd.Apply(all...), sink
}

func spanStarts(s *recordingSink) []trail.SpanStart {
	var out []trail.SpanStart
	for _, rec := range s.snapshot() {
		if ss, ok := rec.(trail.SpanStart); ok {
			out = append(out, ss)
		}
	}
	return out
}

func spanEnds(s *recordingSink) []trail.SpanEnd {
	var out []trail.SpanEnd
	for _, rec := range s.snapshot() {
		if se, ok := rec.(trail.SpanEnd); ok {
			out = append(out, se)
		}
	}
	return out
}

func spanEvents(s *recordingSink, id trail.SpanID) []trail.Event {
	var out []trail.Event
	for _, rec := range s.snapshot() {
		if e, ok := rec.(trail.Event); ok && e.SpanID == id {
			out = append(out, e)
		}
	}
	return out
}

func attrsFor(s *recordingSink, id trail.SpanID) map[string]trail.Attribute {
	out := make(map[string]trail.Attribute)
	for _, rec := range s.snapshot() {
		if u, ok := rec.(trail.SpanUpdate); ok && u.SpanID == id {
			for _, a := range u.Attributes {
				out[a.Key()] = a
			}
		}
	}
	return out
}

func statusFor(s *recordingSink, id trail.SpanID) *trail.SpanStatus {
	var last *trail.SpanStatus
	for _, rec := range s.snapshot() {
		if u, ok := rec.(trail.SpanUpdate); ok && u.SpanID == id && u.Status != nil {
			last = u.Status
		}
	}
	return last
}

func singleStart(t *testing.T, s *recordingSink) trail.SpanStart {
	t.Helper()
	starts := spanStarts(s)
	if len(starts) != 1 {
		t.Fatalf("span starts = %d, want 1", len(starts))
	}
	return starts[0]
}

// recordStrings collects every string a journal replay would expose.
func recordStrings(s *recordingSink) []string {
	var out []string
	for _, rec := range s.snapshot() {
		add := func(name string, attrs []trail.Attribute) {
			out = append(out, name)
			for _, a := range attrs {
				switch a.Kind() {
				case trail.KindString:
					out = append(out, a.String())
				case trail.KindStrings:
					out = append(out, a.Strings()...)
				}
			}
		}
		switch r := rec.(type) {
		case trail.SpanStart:
			add(r.Name, nil)
		case trail.SpanUpdate:
			add("", r.Attributes)
			if r.Status != nil {
				out = append(out, r.Status.Description)
			}
		case trail.Event:
			add(r.Name, r.Attributes)
		}
	}
	return out
}

func assertNoRecordText(t *testing.T, s *recordingSink, forbidden ...string) {
	t.Helper()
	for _, text := range recordStrings(s) {
		for _, f := range forbidden {
			if strings.Contains(text, f) {
				t.Fatalf("forbidden text %q recorded in %q", f, text)
			}
		}
	}
}

func TestRunSuccess(t *testing.T) {
	cmd, sink := traced(t, helperCommand(t, ""))
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	start := singleStart(t, sink)
	if start.Name != os.Args[0] {
		t.Fatalf("span name = %q, want command name as passed", start.Name)
	}
	attrs := attrsFor(sink, start.SpanID)
	if got := attrs["exec.args.count"].Int64(); got != 1 {
		t.Fatalf("exec.args.count = %d, want 1", got)
	}
	if got, want := attrs["process.pid"].Int64(), int64(cmd.Process.Pid); got != want {
		t.Fatalf("process.pid = %d, want %d", got, want)
	}
	exitCode, ok := attrs["process.exit.code"]
	if !ok || exitCode.Int64() != 0 {
		t.Fatalf("process.exit.code = (%v,%t), want recorded 0", exitCode.Int64(), ok)
	}
	if st := statusFor(sink, start.SpanID); st != nil {
		t.Fatalf("unexpected status on success: %+v", st)
	}
	if ends := spanEnds(sink); len(ends) != 1 || ends[0].SpanID != start.SpanID {
		t.Fatalf("span ends = %+v", spanEnds(sink))
	}
}

func TestRunNonzeroExit(t *testing.T) {
	cmd, sink := traced(t, helperCommand(t, "exit3"))
	err := cmd.Run()
	if err == nil {
		t.Fatal("run succeeded, want exit error")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %T, want *exec.ExitError", err)
	}
	start := singleStart(t, sink)
	st := statusFor(sink, start.SpanID)
	if st == nil || st.Code != trail.StatusError || st.Description != "exit status 3" {
		t.Fatalf("status = %+v, want Error \"exit status 3\"", st)
	}
	if got := attrsFor(sink, start.SpanID)["process.exit.code"].Int64(); got != 3 {
		t.Fatalf("process.exit.code = %d, want 3", got)
	}
}

func TestCommandNotFound(t *testing.T) {
	cmd := trailosexec.Command("trail-does-not-exist-xyz")
	cmd, sink := traced(t, cmd)
	err := cmd.Run()
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("error = %v, want exec.ErrNotFound", err)
	}
	start := singleStart(t, sink)
	st := statusFor(sink, start.SpanID)
	if st == nil || st.Code != trail.StatusError || st.Description != "executable not found" {
		t.Fatalf("status = %+v, want Error \"executable not found\"", st)
	}
	attrs := attrsFor(sink, start.SpanID)
	if _, ok := attrs["process.pid"]; ok {
		t.Fatal("process.pid recorded for failed start")
	}
	if _, ok := attrs["process.exit.code"]; ok {
		t.Fatal("process.exit.code recorded for failed start")
	}
	if ends := spanEnds(sink); len(ends) != 1 {
		t.Fatalf("failed start did not end span: %+v", spanEnds(sink))
	}
	if events := spanEvents(sink, start.SpanID); len(events) != 0 {
		t.Fatalf("error events recorded without WithErrorDetails: %+v", events)
	}
}

func TestErrorDetailsOptIn(t *testing.T) {
	cmd := trailosexec.Command("trail-does-not-exist-xyz")
	cmd, sink := traced(t, cmd, trailosexec.WithErrorDetails())
	if err := cmd.Run(); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("run: %v", err)
	}
	start := singleStart(t, sink)
	events := spanEvents(sink, start.SpanID)
	if len(events) != 1 || events[0].Name != "error" {
		t.Fatalf("error events = %+v, want one \"error\" event", events)
	}
	found := false
	for _, a := range events[0].Attributes {
		if a.Key() == "error.message" && strings.Contains(a.String(), "not found") {
			found = true
		}
	}
	if !found {
		t.Fatal("error.message attribute missing")
	}
}

func TestStartWaitSplit(t *testing.T) {
	cmd, sink := traced(t, helperCommand(t, "stdout"))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	start := singleStart(t, sink)
	if len(spanEnds(sink)) != 0 {
		t.Fatal("span ended before Wait")
	}
	if _, ok := attrsFor(sink, start.SpanID)["process.pid"]; !ok {
		t.Fatal("process.pid not recorded after Start")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if len(spanEnds(sink)) != 1 {
		t.Fatalf("span ends = %d, want 1 after Wait", len(spanEnds(sink)))
	}
}

func TestOutputSingleSpan(t *testing.T) {
	cmd, sink := traced(t, helperCommand(t, "stdout"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if string(out) != "stdout-data" {
		t.Fatalf("output = %q", out)
	}
	start := singleStart(t, sink)
	if len(spanEnds(sink)) != 1 || spanEnds(sink)[0].SpanID != start.SpanID {
		t.Fatal("output did not produce exactly one ended span")
	}
	if got := attrsFor(sink, start.SpanID)["process.exit.code"].Int64(); got != 0 {
		t.Fatalf("exit code = %d", got)
	}
	if _, ok := attrsFor(sink, start.SpanID)["process.pid"]; !ok {
		t.Fatal("process.pid not recorded for Output")
	}
	assertNoRecordText(t, sink, "stdout-data")
}

func TestCombinedOutputSingleSpan(t *testing.T) {
	cmd, sink := traced(t, helperCommand(t, "stderr"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("combined output: %v", err)
	}
	if !strings.Contains(string(out), "stderr-data") {
		t.Fatalf("combined output = %q", out)
	}
	start := singleStart(t, sink)
	if _, ok := attrsFor(sink, start.SpanID)["process.pid"]; !ok {
		t.Fatal("process.pid not recorded for CombinedOutput")
	}
	assertNoRecordText(t, sink, "stderr-data")
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := trailosexec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "TRAILOEXEC_HELPER=1", "TRAILOEXEC_MODE=sleep")
	cmd, sink := traced(t, cmd)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	err := cmd.Run()
	cancel()
	if err == nil {
		t.Fatal("canceled run succeeded")
	}
	start := singleStart(t, sink)
	if st := statusFor(sink, start.SpanID); st == nil || st.Code != trail.StatusError {
		t.Fatalf("status = %+v, want Error", st)
	}
	if _, ok := attrsFor(sink, start.SpanID)["process.exit.code"]; !ok {
		t.Fatal("exit code not recorded for killed process")
	}
	if len(spanEnds(sink)) != 1 {
		t.Fatal("canceled run did not end span")
	}
}

func TestDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cmd := trailosexec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "TRAILOEXEC_HELPER=1", "TRAILOEXEC_MODE=sleep")
	cmd, sink := traced(t, cmd)
	if err := cmd.Run(); err == nil {
		t.Fatal("deadline run succeeded")
	}
	start := singleStart(t, sink)
	if st := statusFor(sink, start.SpanID); st == nil || st.Code != trail.StatusError {
		t.Fatalf("status = %+v, want Error", st)
	}
	if len(spanEnds(sink)) != 1 {
		t.Fatal("deadline run did not end span")
	}
}

func TestDoubleStart(t *testing.T) {
	cmd, sink := traced(t, helperCommand(t, ""))
	if err := cmd.Start(); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := cmd.Start(); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("second start error = %v, want already started", err)
	}
	singleStart(t, sink)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if len(spanEnds(sink)) != 1 {
		t.Fatal("double start produced extra spans")
	}
}

func TestWaitBeforeStart(t *testing.T) {
	cmd, sink := traced(t, helperCommand(t, ""))
	if err := cmd.Wait(); err == nil || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("wait error = %v, want not started", err)
	}
	if got := len(sink.snapshot()); got != 1 { // capture_start only
		t.Fatalf("records = %d, want only capture_start", got)
	}
}

func TestWaitTwice(t *testing.T) {
	cmd, sink := traced(t, helperCommand(t, ""))
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := cmd.Wait(); err == nil || !strings.Contains(err.Error(), "already called") {
		t.Fatalf("second wait error = %v, want already called", err)
	}
	if len(spanStarts(sink)) != 1 || len(spanEnds(sink)) != 1 {
		t.Fatal("second wait disturbed span accounting")
	}
}

func TestPipes(t *testing.T) {
	cmd, sink := traced(t, helperCommand(t, "stdout"))
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	data, err := io.ReadAll(pipe)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if string(data) != "stdout-data" {
		t.Fatalf("pipe data = %q", data)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	singleStart(t, sink)
	assertNoRecordText(t, sink, "stdout-data")
}

func TestProcessStateExposed(t *testing.T) {
	cmd, sink := traced(t, helperCommand(t, ""))
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatal("promoted ProcessState unavailable")
	}
	singleStart(t, sink)
}

func TestFieldMutationBeforeStart(t *testing.T) {
	dir := t.TempDir()
	cmd := helperCommand(t, "", "payload-arg")
	cmd.Dir = dir
	cmd.Env = append([]string{"TRAILOEXEC_CUSTOM=custom-value"}, cmd.Env...)
	cmd, sink := traced(t, cmd,
		trailosexec.WithArgs(),
		trailosexec.WithEnv(),
		trailosexec.WithDir(),
		trailosexec.WithPath(),
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	attrs := attrsFor(sink, singleStart(t, sink).SpanID)
	if got := attrs["exec.dir"].String(); got != dir {
		t.Fatalf("exec.dir = %q, want %q", got, dir)
	}
	if args := attrs["exec.args"].Strings(); !slices.Contains(args, "payload-arg") {
		t.Fatalf("exec.args = %v", args)
	}
	env := attrs["exec.env"].Strings()
	found := false
	for _, kv := range env {
		if kv == "TRAILOEXEC_CUSTOM=custom-value" {
			found = true
		}
	}
	if !found {
		t.Fatal("TRAILOEXEC_CUSTOM missing from exec.env")
	}
	if got := attrs["exec.path"].String(); got != cmd.Path {
		t.Fatalf("exec.path = %q, want %q", got, cmd.Path)
	}
}

func TestDefaultsRecordMinimal(t *testing.T) {
	cmd := helperCommand(t, "", "payload-arg")
	cmd.Env = append(cmd.Env, "TRAILOEXEC_CUSTOM=custom-value")
	cmd, sink := traced(t, cmd)
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	attrs := attrsFor(sink, singleStart(t, sink).SpanID)
	for _, key := range []string{"exec.args", "exec.env", "exec.dir", "exec.path"} {
		if _, ok := attrs[key]; ok {
			t.Fatalf("%s recorded without opt-in", key)
		}
	}
	assertNoRecordText(t, sink, "payload-arg", "custom-value", "TRAILOEXEC_HELPER")
	if _, ok := attrs["exec.args.count"]; !ok {
		t.Fatal("exec.args.count missing")
	}
}

func TestBypassEmbeddedCmd(t *testing.T) {
	cmd, sink := traced(t, helperCommand(t, ""))
	if err := cmd.Cmd.Run(); err != nil {
		t.Fatalf("embedded run: %v", err)
	}
	if got := len(sink.snapshot()); got != 1 {
		t.Fatalf("records = %d, want only capture_start (bypass recorded)", got)
	}
}

func TestDisabledTracerNoRecords(t *testing.T) {
	cmd := helperCommand(t, "").Apply(trailosexec.WithTracer(trail.Tracer{}))
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestCommandRootsAndCommandContextParents(t *testing.T) {
	tracer, sink := newTestTracer(t)

	rootCmd := helperCommand(t, "").Apply(trailosexec.WithTracer(tracer))
	if err := rootCmd.Run(); err != nil {
		t.Fatalf("root run: %v", err)
	}
	if start := singleStart(t, sink); start.ParentSpanID.IsValid() {
		t.Fatalf("Command span parented: %+v", start)
	}

	parentCtx, parent := tracer.Start(context.Background(), "parent")
	cmd := trailosexec.CommandContext(parentCtx, os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "TRAILOEXEC_HELPER=1", "TRAILOEXEC_MODE=")
	cmd = cmd.Apply(trailosexec.WithTracer(tracer))
	if err := cmd.Run(); err != nil {
		t.Fatalf("child run: %v", err)
	}
	starts := spanStarts(sink)
	var child *trail.SpanStart
	for i := range starts {
		if starts[i].ParentSpanID == parent.SpanContext().SpanID() {
			child = &starts[i]
		}
	}
	if child == nil {
		t.Fatalf("CommandContext span not parented from context: %+v", starts)
	}
	parent.End()
}

func TestConcurrentRuns(t *testing.T) {
	tracer, sink := newTestTracer(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := helperCommand(t, "").Apply(trailosexec.WithTracer(tracer))
			if err := cmd.Run(); err != nil {
				t.Errorf("run: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := len(spanStarts(sink)); got != 4 {
		t.Fatalf("span starts = %d, want 4", got)
	}
	if got := len(spanEnds(sink)); got != 4 {
		t.Fatalf("span ends = %d, want 4", got)
	}
}
