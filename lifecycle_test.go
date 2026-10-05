package trail

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeClock is a deterministic clock advanced manually by tests.
type fakeClock struct {
	wall time.Time
	tick time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{wall: time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() timeReading {
	return timeReading{wall: c.wall, tick: c.tick}
}

// advance moves both the tick and the wall reading.
func (c *fakeClock) advance(d time.Duration) {
	c.tick += d
	c.wall = c.wall.Add(d)
}

// stepWall moves only the wall reading, simulating a clock adjustment.
func (c *fakeClock) stepWall(d time.Duration) {
	c.wall = c.wall.Add(d)
}

// fakeIDGenerator returns scripted identifiers in order and fails when a
// script is exhausted.
type fakeIDGenerator struct {
	traceIDs []TraceID
	spanIDs  []SpanID
	traceErr error
	spanErr  error
	ti       int
	si       int
}

func (g *fakeIDGenerator) newTraceID() (TraceID, error) {
	if g.traceErr != nil {
		return TraceID{}, g.traceErr
	}
	if g.ti >= len(g.traceIDs) {
		return TraceID{}, errors.New("trail: fake trace ID script exhausted")
	}
	id := g.traceIDs[g.ti]
	g.ti++
	return id, nil
}

func (g *fakeIDGenerator) newSpanID() (SpanID, error) {
	if g.spanErr != nil {
		return SpanID{}, g.spanErr
	}
	if g.si >= len(g.spanIDs) {
		return SpanID{}, errors.New("trail: fake span ID script exhausted")
	}
	id := g.spanIDs[g.si]
	g.si++
	return id, nil
}

// randomIDs overrides the scripted generator with the production one for
// tests whose concurrency makes scripted IDs impractical.
func randomIDs() ProviderOption {
	return func(cfg *providerConfig) {
		cfg.ids = &randomIDGenerator{}
	}
}

func testTraceID(last byte) TraceID {
	var id TraceID
	id[15] = last
	return id
}

func testSpanID(last byte) SpanID {
	var id SpanID
	id[7] = last
	return id
}

// newLifecycleProvider builds an enabled provider with deterministic seams.
func newLifecycleProvider(t *testing.T, opts ...ProviderOption) (*Provider, *recordingSink, *fakeClock, *fakeIDGenerator) {
	t.Helper()
	sink := &recordingSink{}
	processor, err := NewSyncProcessor(sink)
	if err != nil {
		t.Fatalf("NewSyncProcessor unexpected error: %v", err)
	}
	clock := newFakeClock()
	ids := &fakeIDGenerator{
		traceIDs: []TraceID{testTraceID(1), testTraceID(2), testTraceID(3), testTraceID(4)},
		spanIDs:  []SpanID{testSpanID(1), testSpanID(2), testSpanID(3), testSpanID(4), testSpanID(5), testSpanID(6)},
	}
	allOpts := append([]ProviderOption{
		func(cfg *providerConfig) {
			cfg.clock = clock
			cfg.ids = ids
		},
	}, opts...)
	provider, err := NewProvider(processor, allOpts...)
	if err != nil {
		t.Fatalf("NewProvider unexpected error: %v", err)
	}
	return provider, sink, clock, ids
}

// spanStarts returns the SpanStart records the sink received.
func spanStarts(sink *recordingSink) []SpanStart {
	var starts []SpanStart
	for _, r := range sink.records {
		if s, ok := r.(SpanStart); ok {
			starts = append(starts, s)
		}
	}
	return starts
}

func spanEnds(sink *recordingSink) []SpanEnd {
	var ends []SpanEnd
	for _, r := range sink.records {
		if s, ok := r.(SpanEnd); ok {
			ends = append(ends, s)
		}
	}
	return ends
}

func traceEnds(sink *recordingSink) []TraceEnd {
	var ends []TraceEnd
	for _, r := range sink.records {
		if s, ok := r.(TraceEnd); ok {
			ends = append(ends, s)
		}
	}
	return ends
}

func TestNewProviderNilProcessor(t *testing.T) {
	if _, err := NewProvider(nil); err == nil {
		t.Fatal("NewProvider(nil) succeeded, want error")
	}
}

func TestNewProviderEmitsCaptureStart(t *testing.T) {
	_, sink, clock, _ := newLifecycleProvider(t)
	if len(sink.records) != 1 {
		t.Fatalf("capture emitted %d records, want 1", len(sink.records))
	}
	capture, ok := sink.records[0].(CaptureStart)
	if !ok {
		t.Fatalf("first record = %T, want CaptureStart", sink.records[0])
	}
	if capture.Seq != 1 {
		t.Fatalf("capture Seq = %d, want 1", capture.Seq)
	}
	if !capture.Wall.Equal(clock.wall) {
		t.Fatalf("capture Wall = %v, want clock wall %v", capture.Wall, clock.wall)
	}
}

func TestNewProviderCaptureStartFailure(t *testing.T) {
	sink := &recordingSink{writeErr: errors.New("no space")}
	processor, err := NewSyncProcessor(sink)
	if err != nil {
		t.Fatalf("NewSyncProcessor unexpected error: %v", err)
	}
	if _, err := NewProvider(processor); err == nil {
		t.Fatal("NewProvider with failing sink succeeded, want error")
	}
	// Ownership stayed with the caller: the processor can still be shut down.
	if err := processor.Shutdown(context.Background()); !errors.Is(err, sink.writeErr) {
		t.Fatalf("processor.Shutdown error = %v, want combined error wrapping %v", err, sink.writeErr)
	}
}

func TestStartRootRecord(t *testing.T) {
	provider, sink, clock, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	clock.advance(10 * time.Millisecond)
	ctx, span := tracer.Start(context.Background(), "command.run")

	starts := spanStarts(sink)
	if len(starts) != 1 {
		t.Fatalf("emitted %d SpanStart records, want 1", len(starts))
	}
	got := starts[0]
	sc := span.SpanContext()
	if got.TraceID != sc.TraceID() || got.SpanID != sc.SpanID() {
		t.Fatalf("record IDs = %v/%v, want span IDs %v/%v", got.TraceID, got.SpanID, sc.TraceID(), sc.SpanID())
	}
	if got.ParentSpanID.IsValid() {
		t.Fatalf("root ParentSpanID = %v, want zero", got.ParentSpanID)
	}
	if got.RootSpanID != got.SpanID {
		t.Fatalf("root RootSpanID = %v, want %v", got.RootSpanID, got.SpanID)
	}
	if got.Scope != "myapp" || got.Name != "command.run" {
		t.Fatalf("scope/name = %q/%q, want myapp/command.run", got.Scope, got.Name)
	}
	if got.Seq != 2 {
		t.Fatalf("root Seq = %d, want 2 (after capture)", got.Seq)
	}
	if got.Elapsed != 10*time.Millisecond {
		t.Fatalf("root Elapsed = %v, want 10ms", got.Elapsed)
	}
	if !got.Wall.Equal(clock.wall) {
		t.Fatalf("root Wall = %v, want clock wall %v", got.Wall, clock.wall)
	}
	if SpanFromContext(ctx) != span {
		t.Fatal("returned context does not carry the span")
	}
	if !span.IsRecording() {
		t.Fatal("started span is not recording")
	}
}

func TestChildJoinAndEndOrder(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	parentCtx, parent := tracer.Start(context.Background(), "parent")
	_, child := tracer.Start(parentCtx, "child")
	if child.SpanContext().TraceID() != parent.SpanContext().TraceID() {
		t.Fatal("child trace ID differs from parent")
	}

	starts := spanStarts(sink)
	childStart := starts[1]
	if childStart.ParentSpanID != parent.SpanContext().SpanID() {
		t.Fatalf("child ParentSpanID = %v, want %v", childStart.ParentSpanID, parent.SpanContext().SpanID())
	}
	if childStart.RootSpanID != parent.SpanContext().SpanID() {
		t.Fatalf("child RootSpanID = %v, want root %v", childStart.RootSpanID, parent.SpanContext().SpanID())
	}
	if len(traceEnds(sink)) != 0 {
		t.Fatal("trace ended while spans are live")
	}

	// Root ends first with a live child: no trace end yet.
	parent.End()
	if len(traceEnds(sink)) != 0 {
		t.Fatal("trace ended while child is live")
	}
	child.End()
	ends := traceEnds(sink)
	if len(ends) != 1 {
		t.Fatalf("emitted %d TraceEnd records, want 1", len(ends))
	}
	if ends[0].TraceID != parent.SpanContext().TraceID() || ends[0].RootSpanID != parent.SpanContext().SpanID() {
		t.Fatalf("TraceEnd = %v, want trace %v root %v", ends[0], parent.SpanContext().TraceID(), parent.SpanContext().SpanID())
	}
}

func TestChildEndsBeforeRoot(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	parentCtx, parent := tracer.Start(context.Background(), "parent")
	_, child := tracer.Start(parentCtx, "child")
	child.End()
	if len(traceEnds(sink)) != 0 {
		t.Fatal("trace ended while root is live")
	}
	parent.End()
	if len(traceEnds(sink)) != 1 {
		t.Fatalf("emitted %d TraceEnd records, want 1 after root end", len(traceEnds(sink)))
	}
}

func TestDuplicateEndSingleRecord(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, span := tracer.Start(context.Background(), "op")
	span.End()
	span.End()
	if got := len(spanEnds(sink)); got != 1 {
		t.Fatalf("emitted %d SpanEnd records after duplicate End, want 1", got)
	}
	if span.IsRecording() {
		t.Fatal("ended span still records")
	}
	if sc := span.SpanContext(); !sc.IsValid() {
		t.Fatal("identifiers unavailable after End")
	}
}

func TestWithNewRoot(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, parent := tracer.Start(context.Background(), "parent")
	_, root := tracer.Start(context.Background(), "fresh", WithNewRoot())
	if root.SpanContext().TraceID() == parent.SpanContext().TraceID() {
		t.Fatal("WithNewRoot reused the parent trace")
	}
	starts := spanStarts(sink)
	if starts[1].ParentSpanID.IsValid() {
		t.Fatalf("WithNewRoot ParentSpanID = %v, want zero", starts[1].ParentSpanID)
	}
}

func TestEndedParentStartsNewRoot(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	ctx, parent := tracer.Start(context.Background(), "parent")
	parent.End()
	_, child := tracer.Start(ctx, "child")
	if child.SpanContext().TraceID() == parent.SpanContext().TraceID() {
		t.Fatal("child joined the ended parent's trace")
	}
	starts := spanStarts(sink)
	if starts[1].ParentSpanID.IsValid() {
		t.Fatalf("child of ended parent has ParentSpanID %v, want zero", starts[1].ParentSpanID)
	}
}

func TestForeignParentStartsNewRoot(t *testing.T) {
	providerA, sinkA, _, _ := newLifecycleProvider(t)
	providerB, sinkB, _, _ := newLifecycleProvider(t, func(cfg *providerConfig) {
		cfg.ids = &fakeIDGenerator{
			traceIDs: []TraceID{testTraceID(0x21), testTraceID(0x22)},
			spanIDs:  []SpanID{testSpanID(0x21), testSpanID(0x22), testSpanID(0x23)},
		}
	})

	ctx, foreign := providerA.Tracer("a").Start(context.Background(), "foreign")
	_, local := providerB.Tracer("b").Start(ctx, "local")
	if local.SpanContext().TraceID() == foreign.SpanContext().TraceID() {
		t.Fatal("local span joined a foreign provider's trace")
	}
	localStarts := spanStarts(sinkB)
	if len(localStarts) != 1 || localStarts[0].ParentSpanID.IsValid() {
		t.Fatalf("foreign parent leaked into local records: %v", localStarts)
	}
	if len(spanStarts(sinkA)) != 1 {
		t.Fatal("foreign provider recorded the local span")
	}
}

func TestEmptySpanNameNormalized(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")
	_, span := tracer.Start(context.Background(), "")
	_ = span
	if got := spanStarts(sink)[0].Name; got != "unnamed" {
		t.Fatalf("empty span name recorded as %q, want unnamed", got)
	}
}

func TestSpanIDGenerationFailure(t *testing.T) {
	provider, sink, _, ids := newLifecycleProvider(t)
	ids.spanErr = errors.New("entropy exhausted")
	tracer := provider.Tracer("myapp")

	ctx := context.Background()
	startedCtx, span := tracer.Start(ctx, "op")
	if span.IsRecording() {
		t.Fatal("span records despite ID generation failure")
	}
	if startedCtx != ctx {
		t.Fatal("failed admission replaced the context")
	}
	if got := len(spanStarts(sink)); got != 0 {
		t.Fatalf("emitted %d SpanStart records despite failure, want 0", got)
	}
	if provider.state.stickyErr == nil {
		t.Fatal("ID generation failure not latched")
	}
}

func TestStartOutputFailureReturnsNonRecording(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, parent := tracer.Start(context.Background(), "parent")
	sink.writeErr = errors.New("disk full")

	// The SyncProcessor latches the first write failure and stops output, so
	// later admissions fail without touching the sink.
	ctx, child := tracer.Start(context.Background(), "child")
	if child.IsRecording() {
		t.Fatal("child records despite output failure")
	}
	if ctx != context.Background() {
		t.Fatal("failed admission replaced the context")
	}
	if provider.state.stickyErr == nil {
		t.Fatal("output failure not latched")
	}
	sink.writeErr = nil
	ctx2, span := tracer.Start(context.Background(), "op")
	if span.IsRecording() || ctx2 != context.Background() {
		t.Fatal("admission recovered after latched output failure")
	}
	if got := len(spanStarts(sink)); got != 1 {
		t.Fatalf("emitted %d SpanStart records, want only the pre-failure root", got)
	}
	// The latched processor also drops the parent's end records: the journal
	// keeps the unfinished start, which is the designed crash-tail behavior,
	// and the loss is reported through the latched error.
	parent.End()
	if got := len(spanEnds(sink)); got != 0 {
		t.Fatalf("emitted %d SpanEnd records after latched failure, want 0", got)
	}
	if got := len(traceEnds(sink)); got != 0 {
		t.Fatalf("emitted %d TraceEnd records after latched failure, want 0", got)
	}
}

func TestMonotonicDurationSurvivesWallAdjustment(t *testing.T) {
	provider, sink, clock, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	clock.advance(5 * time.Millisecond)
	_, span := tracer.Start(context.Background(), "op")
	clock.advance(200 * time.Millisecond)
	clock.stepWall(-90 * time.Minute) // wall clock correction backwards
	span.End()

	ends := spanEnds(sink)
	if len(ends) != 1 {
		t.Fatalf("emitted %d SpanEnd records, want 1", len(ends))
	}
	if got, want := ends[0].Duration, 200*time.Millisecond; got != want {
		t.Fatalf("duration = %v, want tick-derived %v", got, want)
	}
	start := spanStarts(sink)[0]
	if !ends[0].Wall.Before(start.Wall) {
		t.Fatalf("end wall %v not before start wall %v; test no longer exercises a backwards step", ends[0].Wall, start.Wall)
	}
}

func TestSequenceNumbersIncrease(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t)
	tracer := provider.Tracer("myapp")

	_, parent := tracer.Start(context.Background(), "parent")
	_, child := tracer.Start(context.Background(), "child")
	child.End()
	parent.End()

	var prev uint64
	for _, r := range sink.records {
		var seq uint64
		switch r := r.(type) {
		case CaptureStart:
			seq = r.Seq
		case SpanStart:
			seq = r.Seq
		case SpanEnd:
			seq = r.Seq
		case TraceEnd:
			seq = r.Seq
		}
		if seq <= prev {
			t.Fatalf("record %T seq %d does not increase past %d", r, seq, prev)
		}
		prev = seq
	}
}

func TestConcurrentRootsCompleteIndependently(t *testing.T) {
	provider, sink, _, _ := newLifecycleProvider(t, randomIDs())
	tracer := provider.Tracer("myapp")

	const roots = 8
	var wg sync.WaitGroup
	for i := range roots {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rootCtx, root := tracer.Start(context.Background(), "root")
			_, child := tracer.Start(rootCtx, "child")
			if i%2 == 0 {
				child.End()
				root.End()
			} else {
				root.End()
				child.End()
			}
		}(i)
	}
	wg.Wait()

	if got := len(traceEnds(sink)); got != roots {
		t.Fatalf("emitted %d TraceEnd records, want %d", got, roots)
	}
	traceIDs := make(map[TraceID]bool)
	for _, start := range spanStarts(sink) {
		if start.ParentSpanID.IsValid() {
			continue
		}
		traceIDs[start.TraceID] = true
	}
	if len(traceIDs) != roots {
		t.Fatalf("created %d distinct root traces, want %d", len(traceIDs), roots)
	}
}

func TestChildStartRacingParentEnd(t *testing.T) {
	provider, _, _, _ := newLifecycleProvider(t, randomIDs())
	tracer := provider.Tracer("myapp")

	const iterations = 50
	for range iterations {
		ctx, parent := tracer.Start(context.Background(), "parent")
		var wg sync.WaitGroup
		var childTrace TraceID
		wg.Add(2)
		go func() {
			defer wg.Done()
			parent.End()
		}()
		go func() {
			defer wg.Done()
			_, child := tracer.Start(ctx, "child")
			childTrace = child.SpanContext().TraceID()
			child.End()
		}()
		wg.Wait()
		parentTrace := parent.SpanContext().TraceID()
		// Deliberately timing-dependent: the child either joined the active
		// parent or became a new root. Both are valid outcomes.
		if childTrace != parentTrace && !childTrace.IsValid() {
			t.Fatalf("child trace %v is neither the parent's %v nor a new valid root", childTrace, parentTrace)
		}
		if !parentTrace.IsValid() {
			t.Fatalf("parent trace %v is invalid", parentTrace)
		}
	}
}
