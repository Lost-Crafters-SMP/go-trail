// Package render projects reconstructed state into CLI output formats.
package render

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"go.lostcrafters.com/trail/internal/replay"
)

// Object is the shared JSON/TOON projection. Exact integers are strings;
// optional keys are added only when their source is authoritative.
type Object map[string]any

func integer(n int64) string   { return strconv.FormatInt(n, 10) }
func unsigned(n uint64) string { return strconv.FormatUint(n, 10) }

func attributes(attrs []replay.MachineAttribute) []Object {
	out := make([]Object, 0, len(attrs))
	for _, a := range attrs {
		v := replay.MachineAttributeValue(a)
		if a.Type == replay.KindStrings && a.StringsValue == nil {
			v = []string{}
		}
		out = append(out, Object{"key": a.Key, "type": replay.AttributeKindString(a.Type), "value": v})
	}
	slices.SortFunc(out, func(a, b Object) int { return strings.Compare(a["key"].(string), b["key"].(string)) })
	return out
}

// Span projects a span without losing attribute types or missing durations.
func Span(s *replay.MachineSpan) Object {
	parent := ""
	if s.ParentSpanID.IsValid() {
		parent = s.ParentSpanID.String()
	}
	o := Object{"span_id": s.SpanID.String(), "trace_id": s.TraceID.String(), "root_span_id": s.RootSpanID.String(), "parent_span_id": parent, "parent_resolved": s.ParentResolved, "unresolved_parent": s.UnresolvedParent, "name": s.Name, "scope": s.Scope, "wall_start": s.WallStart, "start_elapsed_nanos": integer(s.StartElapsedNanos), "ended": s.Ended, "attributes": attributes(s.Attributes), "dropped_attrs": s.DroppedAttrs, "dropped_events": s.DroppedEvents, "dropped_status": s.DroppedStatus}
	if !s.Ended {
		delete(o, "dropped_attrs")
		delete(o, "dropped_events")
		delete(o, "dropped_status")
	}
	o["diagnostics"] = Diagnostics(s.Diagnostics)
	if s.DurationNanos != nil {
		o["duration_nanos"] = *s.DurationNanos
	}
	if s.WallEnd != nil {
		o["wall_end"] = *s.WallEnd
	}
	if s.Status != nil {
		o["status"] = Object{"code": string(s.Status.Code), "description": s.Status.Description}
	}
	events := make([]Object, 0, len(s.Events))
	for _, e := range s.Events {
		events = append(events, Object{"seq": unsigned(e.Seq), "name": e.Name, "wall_time": e.WallTime.Format(time.RFC3339Nano), "elapsed_nanos": integer(e.ElapsedNanos), "attributes": attributes(e.Attributes)})
	}
	o["events"] = events
	children := make([]string, 0, len(s.Children))
	for _, id := range s.Children {
		children = append(children, id.String())
	}
	o["children"] = children
	return o
}

// OrderedSpans returns deterministic start-time order with sequence tie breaks.
func OrderedSpans(t *replay.MachineTrace) []*replay.MachineSpan {
	out := make([]*replay.MachineSpan, 0, len(t.Spans))
	for _, s := range t.Spans {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b *replay.MachineSpan) int {
		if a.StartElapsedNanos < b.StartElapsedNanos {
			return -1
		}
		if a.StartElapsedNanos > b.StartElapsedNanos {
			return 1
		}
		if a.StartSeq < b.StartSeq {
			return -1
		}
		if a.StartSeq > b.StartSeq {
			return 1
		}
		return strings.Compare(a.SpanID.String(), b.SpanID.String())
	})
	return out
}

// Trace projects completion independently from the root's duration.
func Trace(t *replay.MachineTrace) Object {
	state := []string{"completed", "incomplete", "unresolved"}[t.Status]
	o := Object{"trace_id": t.TraceID.String(), "root_span_id": t.RootSpanID.String(), "trace_status": state, "unended_spans": integer(int64(t.UnendedSpans)), "wall_start": t.WallStart}
	if t.RootSpanDurationNanos != nil {
		o["root_span_duration_nanos"] = *t.RootSpanDurationNanos
	}
	if t.TraceLifetimeNanos != nil {
		o["trace_lifetime_nanos"] = *t.TraceLifetimeNanos
	}
	if t.WallEnd != nil {
		o["wall_end"] = *t.WallEnd
	}
	spans := make([]Object, 0, len(t.Spans))
	for _, s := range OrderedSpans(t) {
		spans = append(spans, Span(s))
	}
	o["spans"] = spans
	o["diagnostics"] = Diagnostics(t.Diagnostics)
	return o
}

// Diagnostics projects contextual replay diagnostics without inventing IDs.
func Diagnostics(ds []replay.MachineDiagnostic) []Object {
	out := make([]Object, 0, len(ds))
	for _, d := range ds {
		o := Object{"type": d.Type, "line": integer(int64(d.Line)), "message": d.Message}
		if d.TraceID.IsValid() {
			o["trace_id"] = d.TraceID.String()
		}
		if d.SpanID.IsValid() {
			o["span_id"] = d.SpanID.String()
		}
		if d.ParentSpanID.IsValid() {
			o["parent_span_id"] = d.ParentSpanID.String()
		}
		out = append(out, o)
	}
	return out
}

// Capture projects the complete document with separately labelled loss sources.
func Capture(c *replay.MachineCapture, details bool) Object {
	o := Object{"path": c.Path, "header": Object{"format": c.Header.Format, "version": c.Header.Version}, "observed_elapsed_nanos": integer(c.ObservedElapsedNanos), "last_record_seq": unsigned(c.LastRecordSeq), "last_record_elapsed_nanos": integer(c.LastRecordElapsedNanos)}
	if c.CaptureStart != nil {
		o["capture_start_wall"] = c.CaptureStart.WallTime.Format(time.RFC3339Nano)
	}
	traces := make([]*replay.MachineTrace, 0, len(c.Traces))
	for _, t := range c.Traces {
		traces = append(traces, t)
	}
	slices.SortFunc(traces, func(a, b *replay.MachineTrace) int { return strings.Compare(a.TraceID.String(), b.TraceID.String()) })
	docs := make([]Object, 0, len(traces))
	for _, t := range traces {
		if details {
			docs = append(docs, Trace(t))
		}
	}
	o["traces_total"] = unsigned(c.TotalTraces)
	o["traces_completed"] = unsigned(c.CompletedTraces)
	var unresolved uint64
	unfinished := make([]Object, 0)
	for _, t := range traces {
		if t.Status != replay.TraceStatusCompleted {
			if t.Status == replay.TraceStatusUnresolved {
				unresolved++
			}
			unfinished = append(unfinished, Object{"trace_id": t.TraceID.String(), "trace_status": []string{"completed", "incomplete", "unresolved"}[t.Status], "unended_spans": integer(int64(t.UnendedSpans))})
		}
	}
	o["incomplete_traces"] = unfinished
	o["traces_unresolved"] = unsigned(unresolved)
	o["traces_incomplete"] = unsigned(c.TotalTraces - c.CompletedTraces - unresolved)
	o["spans_total"] = unsigned(c.TotalSpans)
	o["spans_ended"] = unsigned(c.EndedSpans)
	o["spans_unended"] = unsigned(c.TotalSpans - c.EndedSpans)
	o["events_total"] = unsigned(c.TotalEvents)
	o["ended_span_status_counts"] = Object{"ok": unsigned(c.EndedOK), "error": unsigned(c.EndedError), "unset": unsigned(c.EndedUnset)}
	if c.DurationMin != nil {
		o["ended_span_durations"] = Object{"count": unsigned(c.EndedSpans), "total_nanos": c.DurationTotal.String(), "min_nanos": integer(*c.DurationMin), "max_nanos": integer(*c.DurationMax)}
	}
	if details {
		o["traces"] = docs
	}
	loss := Object{"live_spans": integer(int64(c.LossAccounting.LiveSpans))}
	d := c.LossAccounting.DerivedPerSpan
	loss["derived_per_span_losses"] = Object{"dropped_attributes": unsigned(d.DroppedAttributes), "dropped_events": unsigned(d.DroppedEvents), "dropped_status_updates": unsigned(d.DroppedStatus), "spans_with_drops": integer(int64(d.SpansWithDrops))}
	if s := c.LossAccounting.Summary; s != nil {
		loss["summary"] = Object{"seq": unsigned(s.Seq), "elapsed_nanos": integer(s.ElapsedNanos), "rejected_starts": unsigned(s.RejectedStarts), "dropped_attributes": unsigned(s.DroppedAttributes), "dropped_events": unsigned(s.DroppedEvents), "dropped_status_updates": unsigned(s.DroppedStatusUpdates), "unended_spans": unsigned(s.UnendedSpans), "unended_dropped_attributes": unsigned(s.UnendedDroppedAttributes), "unended_dropped_events": unsigned(s.UnendedDroppedEvents), "unended_dropped_status_updates": unsigned(s.UnendedDroppedStatusUpdates)}
	}
	o["loss_accounting"] = loss
	diagnostics := Diagnostics(c.Diagnostics)
	o["diagnostics"] = Object{"replay_diagnostics": diagnostics}
	if c.TailCondition != nil {
		o["diagnostics"].(Object)["tail_conditions"] = []Object{{"type": c.TailCondition.Type, "line": integer(int64(c.TailCondition.Line)), "message": c.TailCondition.Message}}
	}
	return o
}
