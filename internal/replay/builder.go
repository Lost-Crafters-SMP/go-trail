package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.lostcrafters.com/trail"
)

// Options controls explicit recovery and diagnostic handling.
type Options struct {
	BestEffort   bool
	Strict       bool
	MaxLineBytes int
	// OnTrace is invoked when trace_end validates a completed trace, or at EOF
	// for remaining traces. ReleaseCompleted drops completed detail after delivery.
	OnTrace          func(*MachineTrace) error
	ReleaseCompleted bool
	// DiscardSpanPayload discards attributes/events after each validated span
	// record for aggregate/lifecycle-only consumers. Lifecycle, ancestry, names,
	// statuses and root timing metadata remain until trace completion.
	DiscardSpanPayload bool
	OnSpanEnded        func(*MachineSpan) error
	// TargetTrace is for a second pass over an already validated journal.
	// Non-target lifecycle records are decoded but not reconstructed.
	TargetTrace trail.TraceID
	// PayloadSpan limits attributes/events to one span during targeted replay.
	PayloadSpan trail.SpanID
	Retention   *RetentionStats
}

// Read validates and reconstructs a journal in recorded order. By default it
// retains the full capture; options can deliver and release completed traces,
// discard ended payloads, or select a trace in an already validated second pass.
func Read(r io.Reader, path string, opts Options) (*MachineCapture, error) {
	d := NewDecoder(r, opts.MaxLineBytes)
	h, err := d.Header()
	if err != nil {
		return nil, err
	}
	c := &MachineCapture{Path: path, Header: h, Traces: map[trail.TraceID]*MachineTrace{}}
	completed := map[trail.TraceID]struct{}{}
	for {
		rec, e := d.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			var formatError *FormatError
			// Reader failures cannot be recovered by skipping a journal record;
			// oversized lines likewise have not been fully drained.
			if !opts.BestEffort || !errors.As(e, &formatError) || formatError.Field == "line" {
				return c, e
			}
			c.Diagnostics = append(c.Diagnostics, MachineDiagnostic{Type: "format_error", Line: d.line, Message: e.Error()})
			continue
		}
		if c.CaptureStart == nil && rec.Type != "capture_start" {
			return c, &FormatError{rec.Line, "capture_start", fmt.Errorf("first record must be capture_start")}
		}
		if rec.Seq <= c.LastRecordSeq {
			return c, &FormatError{rec.Line, "seq", fmt.Errorf("sequence must increase")}
		}
		if c.LastRecordSeq != 0 && rec.Seq != c.LastRecordSeq+1 {
			c.Diagnostics = append(c.Diagnostics, MachineDiagnostic{Type: "sequence_gap", Line: rec.Line, Message: "possible missing records"})
		}
		c.LastRecordSeq = rec.Seq
		c.LastRecordElapsedNanos = rec.ElapsedNanos
		c.ObservedElapsedNanos = max(c.ObservedElapsedNanos, rec.ElapsedNanos)
		traceRecord := isSpanRecord(rec.Type) || rec.Type == "trace_end"
		if opts.TargetTrace.IsValid() && traceRecord {
			if raw, ok := rec.Fields["traceId"]; ok {
				var text string
				_ = json.Unmarshal(raw, &text)
				id, _ := trail.ParseTraceID(text)
				if id != opts.TargetTrace {
					continue
				}
			}
		}
		if raw, ok := rec.Fields["traceId"]; ok && traceRecord {
			var text string
			_ = json.Unmarshal(raw, &text)
			id, _ := trail.ParseTraceID(text)
			if _, ok := completed[id]; ok {
				e = fmt.Errorf("record after trace_end")
			}
		}
		var observed *MachineSpan
		priorAttrs, priorEvents := 0, 0
		priorSpans := c.TotalSpans
		if opts.Retention != nil {
			tid, _ := requiredString(rec.Fields, "traceId")
			sid, _ := requiredString(rec.Fields, "spanId")
			ti, _ := trail.ParseTraceID(tid)
			si, _ := trail.ParseSpanID(sid)
			if t := c.Traces[ti]; t != nil {
				observed = t.Spans[si]
				if observed != nil {
					priorAttrs = len(observed.Attributes)
					priorEvents = len(observed.Events)
				}
			}
		}
		if e == nil {
			e = c.apply(rec)
		}
		if e != nil {
			diagnostic := MachineDiagnostic{Type: "lifecycle_error", Line: rec.Line, Message: e.Error()}
			if tid, err := requiredString(rec.Fields, "traceId"); err == nil {
				diagnostic.TraceID, _ = trail.ParseTraceID(tid)
			}
			if sid, err := requiredString(rec.Fields, "spanId"); err == nil {
				diagnostic.SpanID, _ = trail.ParseSpanID(sid)
			}
			c.Diagnostics = append(c.Diagnostics, diagnostic)
			if t := c.Traces[diagnostic.TraceID]; t != nil {
				t.Diagnostics = append(t.Diagnostics, diagnostic)
				if s := t.Spans[diagnostic.SpanID]; s != nil {
					s.Diagnostics = append(s.Diagnostics, diagnostic)
				}
			}
			if !opts.BestEffort {
				return c, &FormatError{rec.Line, "lifecycle", e}
			}
			continue
		}
		if m := opts.Retention; m != nil {
			if c.TotalSpans > priorSpans {
				m.ReconstructedSpans++
				m.RetainedSpans++
			}
			tid, _ := requiredString(rec.Fields, "traceId")
			sid, _ := requiredString(rec.Fields, "spanId")
			ti, _ := trail.ParseTraceID(tid)
			si, _ := trail.ParseSpanID(sid)
			if t := c.Traces[ti]; t != nil {
				observed = t.Spans[si]
			}
			if observed != nil {
				m.RetainedAttributes += len(observed.Attributes) - priorAttrs
				m.RetainedEvents += len(observed.Events) - priorEvents
			}
			m.PeakSpans = max(m.PeakSpans, m.RetainedSpans)
			m.PeakTraces = max(m.PeakTraces, len(c.Traces))
			m.PeakAttributes = max(m.PeakAttributes, m.RetainedAttributes)
			m.PeakEvents = max(m.PeakEvents, m.RetainedEvents)
		}
		if opts.PayloadSpan.IsValid() && isSpanRecord(rec.Type) {
			if text, err := requiredString(rec.Fields, "spanId"); err == nil {
				sid, _ := trail.ParseSpanID(text)
				if sid != opts.PayloadSpan {
					tid, _ := requiredString(rec.Fields, "traceId")
					id, _ := trail.ParseTraceID(tid)
					s := c.Traces[id].Spans[sid]
					if m := opts.Retention; m != nil {
						m.RetainedAttributes -= len(s.Attributes)
						m.RetainedEvents -= len(s.Events)
					}
					s.Attributes = nil
					s.Events = nil
				}
			}
		}
		if opts.OnTrace != nil && rec.Type == "trace_end" {
			text, _ := requiredString(rec.Fields, "traceId")
			id, _ := trail.ParseTraceID(text)
			t := c.Traces[id]
			c.resolveTrace(t)
			if err := opts.OnTrace(t); err != nil {
				return c, err
			}
			if opts.ReleaseCompleted {
				if m := opts.Retention; m != nil {
					m.RetainedSpans -= len(t.Spans)
					for _, s := range t.Spans {
						m.RetainedAttributes -= len(s.Attributes)
						m.RetainedEvents -= len(s.Events)
					}
				}
				delete(c.Traces, id)
				completed[id] = struct{}{}
				if m := opts.Retention; m != nil {
					m.CompletionIDs = len(completed)
				}
			}
		}
		if rec.Type == "span_end" {
			tid, _ := requiredString(rec.Fields, "traceId")
			sid, _ := requiredString(rec.Fields, "spanId")
			ti, _ := trail.ParseTraceID(tid)
			si, _ := trail.ParseSpanID(sid)
			s := c.Traces[ti].Spans[si]
			if opts.OnSpanEnded != nil {
				if err := opts.OnSpanEnded(s); err != nil {
					return c, err
				}
			}
		}
		if opts.DiscardSpanPayload && isSpanRecord(rec.Type) {
			if sid, err := requiredString(rec.Fields, "spanId"); err == nil {
				tid, _ := requiredString(rec.Fields, "traceId")
				ti, _ := trail.ParseTraceID(tid)
				si, _ := trail.ParseSpanID(sid)
				s := c.Traces[ti].Spans[si]
				if m := opts.Retention; m != nil {
					m.RetainedAttributes -= len(s.Attributes)
					m.RetainedEvents -= len(s.Events)
				}
				s.Attributes = nil
				s.Events = nil
			}
		}
	}
	c.TailCondition = d.Tail
	if c.CaptureStart == nil {
		return c, &FormatError{d.line, "capture_start", fmt.Errorf("missing capture_start")}
	}
	c.resolve()
	if opts.OnTrace != nil {
		ids := make([]trail.TraceID, 0, len(c.Traces))
		for id := range c.Traces {
			ids = append(ids, id)
		}
		slices.SortFunc(ids, func(a, b trail.TraceID) int { return strings.Compare(a.String(), b.String()) })
		for _, id := range ids {
			t := c.Traces[id]
			if !t.CompletionObserved {
				if err := opts.OnTrace(t); err != nil {
					return c, err
				}
			}
		}
	}
	if opts.Strict && len(c.Diagnostics) > 0 {
		return c, fmt.Errorf("trail: strict replay rejected %d diagnostics", len(c.Diagnostics))
	}
	return c, nil
}

func (c *MachineCapture) apply(r Record) error {
	f := r.Fields
	wall := time.Unix(0, r.WallNanos).UTC()
	if r.Type == "capture_start" {
		if c.CaptureStart != nil {
			return fmt.Errorf("duplicate capture_start")
		}
		c.CaptureStart = &CaptureStartRecord{Seq: r.Seq, WallTime: wall}
		return nil
	}
	if r.Type == "loss_summary" {
		return c.applyLoss(r, wall)
	}
	tid, _ := requiredString(f, "traceId")
	traceID, _ := trail.ParseTraceID(tid)
	rid, _ := requiredString(f, "rootSpanId")
	rootID, _ := trail.ParseSpanID(rid)
	if r.Type == "span_start" {
		sid, _ := requiredString(f, "spanId")
		spanID, _ := trail.ParseSpanID(sid)
		if spanID != rootID {
			if _, ok := f["parentSpanId"]; !ok {
				return fmt.Errorf("non-root span has no parent")
			}
		}
	}
	t := c.Traces[traceID]
	if t == nil {
		if r.Type != "span_start" {
			return fmt.Errorf("%s without start", r.Type)
		}
		t = &MachineTrace{TraceID: traceID, RootSpanID: rootID, Status: TraceStatusIncomplete, Spans: map[trail.SpanID]*MachineSpan{}}
		c.Traces[traceID] = t
		c.TotalTraces++
	}
	if t.RootSpanID != rootID {
		return fmt.Errorf("inconsistent root identity")
	}
	if t.CompletionObserved {
		return fmt.Errorf("record after trace_end")
	}
	if r.Type == "trace_end" {
		root := t.Spans[rootID]
		if root == nil || !root.Ended {
			return fmt.Errorf("trace_end without ended root")
		}
		for _, s := range t.Spans {
			if !s.Ended {
				return fmt.Errorf("trace_end with live spans")
			}
		}
		if r.ElapsedNanos < root.StartElapsedNanos {
			return fmt.Errorf("negative trace lifetime")
		}
		duration := strconv.FormatInt(r.ElapsedNanos-root.StartElapsedNanos, 10)
		end := wall.Format(time.RFC3339Nano)
		t.TraceLifetimeNanos = &duration
		t.WallEnd = &end
		t.CompletionObserved = true
		t.Status = TraceStatusCompleted
		c.CompletedTraces++
		return nil
	}
	sid, _ := requiredString(f, "spanId")
	spanID, _ := trail.ParseSpanID(sid)
	s := t.Spans[spanID]
	if r.Type == "span_start" {
		if s != nil {
			return fmt.Errorf("duplicate span_start")
		}
		name, _ := requiredString(f, "name")
		scope, _ := requiredString(f, "scope")
		s = &MachineSpan{TraceID: traceID, RootSpanID: rootID, SpanID: spanID, Name: name, Scope: scope, WallStart: wall.Format(time.RFC3339Nano), StartSeq: r.Seq, StartElapsedNanos: r.ElapsedNanos, DroppedAttrs: "0", DroppedEvents: "0", DroppedStatus: "0", Attributes: []MachineAttribute{}, Events: []MachineEvent{}, Children: []trail.SpanID{}}
		if p, ok := f["parentSpanId"]; ok {
			var text string
			_ = json.Unmarshal(p, &text)
			s.ParentSpanID, _ = trail.ParseSpanID(text)
		}
		if !s.ParentSpanID.IsValid() && spanID != rootID {
			return fmt.Errorf("non-root span has no parent")
		}
		t.Spans[spanID] = s
		c.TotalSpans++
		return nil
	}
	if s == nil {
		return fmt.Errorf("%s without start", r.Type)
	}
	if s.Ended {
		return fmt.Errorf("%s after span_end", r.Type)
	}
	switch r.Type {
	case "span_update":
		if raw, ok := f["attributes"]; ok {
			attrs, _ := decodeAttributes(raw)
			for _, a := range attrs {
				i := slices.IndexFunc(s.Attributes, func(old MachineAttribute) bool { return old.Key == a.Key })
				if i < 0 {
					s.Attributes = append(s.Attributes, a)
				} else {
					s.Attributes[i] = a
				}
			}
			slices.SortFunc(s.Attributes, func(a, b MachineAttribute) int { return strings.Compare(a.Key, b.Key) })
		}
		if raw, ok := f["status"]; ok {
			var status struct {
				Code        MachineStatusCode `json:"code"`
				Description string            `json:"description"`
			}
			_ = json.Unmarshal(raw, &status)
			if status.Code == StatusUnset {
				s.Status = nil
			} else {
				s.Status = &MachineStatus{Code: status.Code, Description: status.Description}
			}
		}
	case "event":
		c.TotalEvents++
		name, _ := requiredString(f, "name")
		attrs := []MachineAttribute{}
		if raw, ok := f["attributes"]; ok {
			attrs, _ = decodeAttributes(raw)
		}
		s.Events = append(s.Events, MachineEvent{Seq: r.Seq, Name: name, WallTime: wall, ElapsedNanos: r.ElapsedNanos, Attributes: attrs})
	case "span_end":
		c.EndedSpans++
		duration, _ := requiredString(f, "durationNano")
		end := wall.Format(time.RFC3339Nano)
		s.DurationNanos = &duration
		s.WallEnd = &end
		s.Ended = true
		s.DroppedAttrs, _ = requiredString(f, "droppedAttributes")
		s.DroppedEvents, _ = requiredString(f, "droppedEvents")
		s.DroppedStatus, _ = requiredString(f, "droppedStatusUpdates")
		c.addDrops(s)
		switch {
		case s.Status == nil:
			c.EndedUnset++
		case s.Status.Code == StatusOK:
			c.EndedOK++
		case s.Status.Code == StatusError:
			c.EndedError++
		}
		n, _ := strconv.ParseInt(duration, 10, 64)
		c.DurationTotal.Add(&c.DurationTotal, big.NewInt(n))
		if c.DurationMin == nil || n < *c.DurationMin {
			v := n
			c.DurationMin = &v
		}
		if c.DurationMax == nil || n > *c.DurationMax {
			v := n
			c.DurationMax = &v
		}
	}
	return nil
}

func isSpanRecord(kind string) bool {
	switch kind {
	case "span_start", "span_update", "span_end", "event":
		return true
	default:
		return false
	}
}

func (c *MachineCapture) applyLoss(r Record, wall time.Time) error {
	f := r.Fields
	n := func(k string) uint64 { v, _ := quotedUint(f, k); return v }
	s := &MachineLossSummary{Seq: r.Seq, WallTime: wall, ElapsedNanos: r.ElapsedNanos, RejectedStarts: n("rejectedStarts"), DroppedAttributes: n("droppedAttributes"), DroppedEvents: n("droppedEvents"), DroppedStatusUpdates: n("droppedStatusUpdates"), UnendedSpans: n("unendedSpans"), UnendedDroppedAttributes: n("unendedDroppedAttributes"), UnendedDroppedEvents: n("unendedDroppedEvents"), UnendedDroppedStatusUpdates: n("unendedDroppedStatusUpdates")}
	if old := c.LossAccounting.Summary; old != nil {
		if s.RejectedStarts < old.RejectedStarts || s.DroppedAttributes < old.DroppedAttributes || s.DroppedEvents < old.DroppedEvents || s.DroppedStatusUpdates < old.DroppedStatusUpdates {
			return fmt.Errorf("regressing cumulative loss summary")
		}
	}
	c.LossAccounting.Summary = s
	return nil
}

func (c *MachineCapture) resolve() {
	for _, t := range c.Traces {
		c.resolveTrace(t)
	}
	slices.SortFunc(c.Diagnostics, func(a, b MachineDiagnostic) int {
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.SpanID.String(), b.SpanID.String())
	})
}

func (c *MachineCapture) addDrops(s *MachineSpan) {
	a, _ := strconv.ParseUint(s.DroppedAttrs, 10, 64)
	e, _ := strconv.ParseUint(s.DroppedEvents, 10, 64)
	status, _ := strconv.ParseUint(s.DroppedStatus, 10, 64)
	d := &c.LossAccounting.DerivedPerSpan
	if ^uint64(0)-d.DroppedAttributes < a || ^uint64(0)-d.DroppedEvents < e || ^uint64(0)-d.DroppedStatus < status {
		c.Diagnostics = append(c.Diagnostics, MachineDiagnostic{Type: "derived_loss_overflow", Message: "per-span totals exceed uint64"})
	} else {
		d.DroppedAttributes += a
		d.DroppedEvents += e
		d.DroppedStatus += status
	}
	if a != 0 || e != 0 || status != 0 {
		d.SpansWithDrops++
	}
}

func (c *MachineCapture) resolveTrace(t *MachineTrace) {
	if t.resolved {
		return
	}
	t.resolved = true
	t.RootSpan = t.Spans[t.RootSpanID]
	if t.RootSpan == nil {
		t.Status = TraceStatusUnresolved
	} else {
		t.WallStart = t.RootSpan.WallStart
		t.RootSpanDurationNanos = t.RootSpan.DurationNanos
	}
	spans := make([]*MachineSpan, 0, len(t.Spans))
	for _, s := range t.Spans {
		spans = append(spans, s)
	}
	slices.SortFunc(spans, func(a, b *MachineSpan) int {
		if a.StartSeq < b.StartSeq {
			return -1
		}
		if a.StartSeq > b.StartSeq {
			return 1
		}
		return strings.Compare(a.SpanID.String(), b.SpanID.String())
	})
	for _, s := range spans {
		if !s.Ended {
			t.UnendedSpans++
			c.LossAccounting.LiveSpans++
		}
		if s.ParentSpanID.IsValid() {
			p := t.Spans[s.ParentSpanID]
			if p == nil || p.StartSeq >= s.StartSeq {
				s.UnresolvedParent = true
				d := MachineDiagnostic{Type: "unresolved_parent", TraceID: t.TraceID, SpanID: s.SpanID, ParentSpanID: s.ParentSpanID, Message: "parent missing or started after child"}
				c.Diagnostics = append(c.Diagnostics, d)
				t.Diagnostics = append(t.Diagnostics, d)
				s.Diagnostics = append(s.Diagnostics, d)
			} else {
				s.ParentResolved = true
				p.Children = append(p.Children, s.SpanID)
			}
		}
	}
	for _, s := range t.Spans {
		slices.SortFunc(s.Children, func(a, b trail.SpanID) int {
			x, y := t.Spans[a], t.Spans[b]
			if x.StartElapsedNanos < y.StartElapsedNanos {
				return -1
			}
			if x.StartElapsedNanos > y.StartElapsedNanos {
				return 1
			}
			return strings.Compare(a.String(), b.String())
		})
	}
}
