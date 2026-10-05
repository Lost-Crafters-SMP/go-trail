package trail

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// unnamedSpanName is the documented placeholder for empty span names.
const unnamedSpanName = "unnamed"

// A Provider owns tracing state and lifecycle for a set of tracers.
//
// The zero value and a nil Provider are disabled, safe handles. Enabled
// providers are created by NewProvider, which owns the processor after
// successful construction. A provider serves any number of concurrent
// traces; it never owns application goroutines.
type Provider struct {
	state *providerState
}

// providerState holds the state of an enabled provider.
type providerState struct {
	processor Processor
	ids       idGenerator
	clock     clock

	// mu is the admission gate: it serializes admission, sequence
	// assignment, trace bookkeeping transitions, and lifecycle changes into
	// single transactions that Shutdown cannot split.
	mu     sync.Mutex
	seq    uint64
	traces map[TraceID]*traceState

	liveSpans      int
	rejectedStarts uint64

	closed    bool
	stickyErr error
}

// traceState is the admission bookkeeping for one live trace: its root,
// whether the root ended, and the count of live admitted spans. Completed
// spans are not collected.
type traceState struct {
	traceID    TraceID
	rootSpanID SpanID
	rootEnded  bool
	live       int
}

// NewProvider returns an enabled provider that submits records to processor.
//
// On success it submits the capture start record and takes ownership of
// processor. A non-nil error leaves ownership with the caller, who remains
// responsible for shutting the processor down. Passing a nil processor,
// including an interface holding a typed nil, is invalid.
func NewProvider(processor Processor, opts ...ProviderOption) (*Provider, error) {
	if processor == nil {
		return nil, errors.New("trail: NewProvider requires a non-nil processor")
	}
	cfg := providerConfig{
		ids:   &randomIDGenerator{},
		clock: newRealClock(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	ps := &providerState{
		processor: processor,
		ids:       cfg.ids,
		clock:     cfg.clock,
		traces:    make(map[TraceID]*traceState),
	}
	if err := ps.submitCaptureStart(); err != nil {
		return nil, err
	}
	return &Provider{state: ps}, nil
}

// Tracer returns a handle whose spans are attributed to scope. The scope is
// a component or instrumentation name; an empty scope means unknown. Tracer
// handles are cheap values that share the provider's state. Overlong scopes
// are truncated on a UTF-8 boundary.
func (p *Provider) Tracer(scope string) Tracer {
	return Tracer{provider: p, scope: truncateUTF8(scope, maxNameBytes)}
}

// enabled reports whether the provider can admit recording spans. It is safe
// to call on a nil Provider.
func (p *Provider) enabled() bool {
	return p != nil && p.state != nil
}

// submitCaptureStart emits the first record of the capture through the
// processor. A failure fails provider construction.
func (ps *providerState) submitCaptureStart() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	reading := ps.clock.now()
	if err := ps.processor.Process(CaptureStart{Seq: ps.nextSeq(), Wall: reading.wall}); err != nil {
		ps.latchError(err)
		return fmt.Errorf("trail: submit capture start: %w", err)
	}
	return nil
}

// start admits a span for tracer and returns its recording context and
// handle. Admission failures return the original context and a
// non-recording span.
func (ps *providerState) start(ctx context.Context, tracer Tracer, name string, opts []StartOption) (context.Context, Span) {
	var cfg startConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if name == "" {
		name = unnamedSpanName
	}
	name = truncateUTF8(name, maxNameBytes)

	spanID, err := ps.ids.newSpanID()
	if err != nil {
		ps.latchError(fmt.Errorf("trail: generate span ID: %w", err))
		return ctx, Span{}
	}

	// Sample times in the calling goroutine before waiting for admission so
	// gate contention never extends measured durations.
	reading := ps.clock.now()

	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.closed {
		return ctx, Span{}
	}
	// Active-span capacity bounds provider-owned bookkeeping; a full
	// provider rejects admission without recording. The loss is counted for
	// Flush and Shutdown reporting.
	if ps.liveSpans >= maxActiveSpans {
		ps.rejectedStarts++
		return ctx, Span{}
	}

	// The parent must be an active span of this provider: ended or foreign
	// spans start new roots instead of joining.
	var parent *spanState
	if !cfg.newRoot {
		if candidate := SpanFromContext(ctx); candidate.state != nil &&
			candidate.state.provider == ps && !candidate.state.ended.Load() {
			parent = candidate.state
		}
	}

	var traceID TraceID
	var parentSpanID SpanID
	var rootSpanID SpanID
	var ts *traceState
	if parent != nil {
		traceID = parent.traceID
		parentSpanID = parent.spanID
		rootSpanID = parent.rootSpanID
		if ts = ps.traces[traceID]; ts == nil {
			// Unreachable while the parent is live; defend anyway.
			parent = nil
			parentSpanID = SpanID{}
		}
	}
	if parent == nil {
		for {
			traceID, err = ps.ids.newTraceID()
			if err != nil {
				ps.latchError(fmt.Errorf("trail: generate trace ID: %w", err))
				return ctx, Span{}
			}
			if _, exists := ps.traces[traceID]; !exists {
				break
			}
		}
		rootSpanID = spanID
	}

	record := SpanStart{
		Seq:          ps.nextSeq(),
		Wall:         reading.wall,
		Elapsed:      reading.tick,
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
		RootSpanID:   rootSpanID,
		Scope:        tracer.scope,
		Name:         name,
	}
	if err := ps.processor.Process(record); err != nil {
		ps.latchError(err)
		return ctx, Span{}
	}
	if ts == nil {
		ts = &traceState{traceID: traceID, rootSpanID: rootSpanID, live: 1}
		ps.traces[traceID] = ts
	} else {
		ts.live++
	}
	ps.liveSpans++
	st := &spanState{
		provider:   ps,
		traceID:    traceID,
		spanID:     spanID,
		rootSpanID: rootSpanID,
		start:      reading,
		trace:      ts,
	}
	// Initial attributes are emitted as a bounded update following
	// span_start; oversized or invalid entries are dropped with counters.
	if len(cfg.attributes) > 0 {
		newKeys, resolved, dropped := resolveSpanAttributes(st.keys, cfg.attributes)
		st.keys = newKeys
		st.droppedAttrs += uint64(dropped)
		switch {
		case len(resolved) == 0:
		case oversizedRecord("", resolved):
			st.droppedAttrs += uint64(len(resolved))
		default:
			if err := ps.processor.Process(SpanUpdate{
				Seq:        ps.nextSeq(),
				Wall:       reading.wall,
				Elapsed:    reading.tick,
				TraceID:    traceID,
				SpanID:     spanID,
				RootSpanID: rootSpanID,
				Attributes: resolved,
			}); err != nil {
				ps.latchError(err)
			}
		}
	}
	return ContextWithSpan(ctx, Span{state: st}), Span{state: st}
}

// nextSeq returns the next capture-wide sequence number. Sequence numbers
// are assigned under the admission gate; a failed submission consumes its
// number, so gaps in a journal signal possible loss.
func (ps *providerState) nextSeq() uint64 {
	ps.seq++
	return ps.seq
}

// latchError records the first pipeline error for reporting by Flush and
// Shutdown. It must be called while holding the admission gate.
func (ps *providerState) latchError(err error) {
	if ps.stickyErr == nil {
		ps.stickyErr = err
	}
}
