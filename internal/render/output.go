package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	toon "github.com/toon-format/toon-go"
)

// Write serializes one reconstructed document, never journal records.
func Write(w io.Writer, format string, document Object) error {
	return WriteWithOptions(w, format, document, TextOptions{MaxDepth: 10, MaxSpans: 500, Sort: "start", Color: "never"})
}

// TextOptions controls human presentation without altering machine output.
type TextOptions struct {
	MaxDepth, MaxSpans  int
	NoEvents, LocalTime bool
	Sort, Color         string
}

// WriteWithOptions preserves the shared machine schema across serializations.
func WriteWithOptions(w io.Writer, format string, document Object, opts TextOptions) error {
	if format == "text" && opts.Color == "always" {
		var buffer bytes.Buffer
		opts.Color = "never"
		if err := WriteWithOptions(&buffer, format, document, opts); err != nil {
			return err
		}
		_, err := fmt.Fprintf(w, "\x1b[36m%s\x1b[0m", buffer.String())
		return err
	}
	document["schema_version"] = "trail.cli.v1"
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(document)
	case "ndjson":
		return writeDocumentItems(w, document)
	case "toon":
		data, err := toon.Marshal(document)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	case "compact":
		return writeCompact(w, document)
	case "text":
		if trace, ok := document["trace"].(Object); ok {
			return writeTrace(w, trace, opts)
		}
		if s, ok := document["span"].(Object); ok {
			return writeSpan(w, s, opts)
		}
		if c, ok := document["capture"].(Object); ok {
			if err := writeOverview(w, c, opts); err != nil {
				return err
			}
			for _, t := range objectArray(c["traces"]) {
				if err := writeTrace(w, t, opts); err != nil {
					return err
				}
			}
			return nil
		}
		if spans, ok := document["spans"].([]Object); ok {
			for _, s := range spans {
				if err := writeSpan(w, s, opts); err != nil {
					return err
				}
			}
			return nil
		}
		if traces, ok := document["traces"].([]Object); ok {
			for _, t := range traces {
				if err := writeTrace(w, t, opts); err != nil {
					return err
				}
			}
			return nil
		}
		data, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func writeTrace(w io.Writer, t Object, opts TextOptions) error {
	if _, err := fmt.Fprintf(w, "TRACE %s  %s  lifetime=%s  root-duration=%s\n", t["trace_id"], t["trace_status"], humanDuration(t["trace_lifetime_nanos"]), humanDuration(t["root_span_duration_nanos"])); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  started: %s (siblings may overlap)\n", humanWall(t["wall_start"], opts)); err != nil {
		return err
	}
	spans := t["spans"].([]Object)
	byID := map[string]Object{}
	for _, s := range spans {
		byID[s["span_id"].(string)] = s
	}
	var visit func(Object, int) error
	count, hidden := 0, 0
	visit = func(s Object, depth int) error {
		if depth > opts.MaxDepth || count >= opts.MaxSpans {
			hidden++
			return nil
		}
		count++
		state := "-"
		if status, ok := s["status"].(Object); ok {
			switch status["code"] {
			case "ok":
				state = "OK"
			case "error":
				state = "ERR"
			}
		}
		live := ""
		if !s["ended"].(bool) {
			live = " UNENDED"
		}
		if _, err := fmt.Fprintf(w, "%s%s  %s  %s%s\n", strings.Repeat("  ", depth), strconv.Quote(s["name"].(string)), humanDuration(s["duration_nanos"]), state, live); err != nil {
			return err
		}
		for _, e := range s["events"].([]Object) {
			if opts.NoEvents {
				break
			}
			if _, err := fmt.Fprintf(w, "%s  event %q elapsed=%s\n", strings.Repeat("  ", depth), e["name"], humanDuration(e["elapsed_nanos"])); err != nil {
				return err
			}
			if err := writeAttributes(w, objectArray(e["attributes"]), strings.Repeat("  ", depth)+"    "); err != nil {
				return err
			}
		}
		children := slices.Clone(s["children"].([]string))
		if opts.Sort == "duration" {
			slices.SortStableFunc(children, func(a, b string) int {
				an, _ := strconv.ParseInt(fmt.Sprint(byID[a]["duration_nanos"]), 10, 64)
				bn, _ := strconv.ParseInt(fmt.Sprint(byID[b]["duration_nanos"]), 10, 64)
				if an > bn {
					return -1
				}
				if an < bn {
					return 1
				}
				return strings.Compare(a, b)
			})
		}
		for _, id := range children {
			if child := byID[id]; child != nil {
				if err := visit(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if root := byID[t["root_span_id"].(string)]; root != nil {
		if err := visit(root, 0); err != nil {
			return err
		}
	}
	for _, s := range spans {
		if s["unresolved_parent"].(bool) {
			if _, err := fmt.Fprintln(w, "ORPHAN parent="+s["parent_span_id"].(string)); err != nil {
				return err
			}
			if err := visit(s, 0); err != nil {
				return err
			}
		}
	}
	if hidden > 0 {
		if _, err := fmt.Fprintf(w, "... truncated (%d subtrees omitted)\n", hidden); err != nil {
			return err
		}
	}
	return writeDiagnostics(w, t["diagnostics"])
}

func writeOverview(w io.Writer, c Object, opts TextOptions) error {
	loss := c["loss_accounting"].(Object)
	state := "unknown"
	if _, ok := loss["summary"]; ok {
		state = "known (checkpoint snapshot)"
	}
	_, err := fmt.Fprintf(w, "CAPTURE %q\nCAPTURE TOTALS (whole file)\n  journal: trail version %v\n  started: %s\n  observed elapsed: %s (not capture end)\n  traces: %s, completed: %s\n  spans: %s, ended: %s, unended: %s\n  events: %s\n  loss accounting: %s\n", c["path"], c["header"].(Object)["version"], humanWall(c["capture_start_wall"], opts), humanDuration(c["observed_elapsed_nanos"]), c["traces_total"], c["traces_completed"], c["spans_total"], c["spans_ended"], c["spans_unended"], c["events_total"], state)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "  incomplete: %s, unresolved: %s\n", c["traces_incomplete"], c["traces_unresolved"]); err != nil {
		return err
	}
	if counts, ok := c["ended_span_status_counts"].(Object); ok {
		if _, err = fmt.Fprintf(w, "  ended status: OK=%s ERR=%s unset=%s\n", counts["ok"], counts["error"], counts["unset"]); err != nil {
			return err
		}
	}
	if duration, ok := c["ended_span_durations"].(Object); ok {
		if _, err = fmt.Fprintf(w, "  ended durations: count=%s min=%s max=%s total=%s ns\n", duration["count"], humanDuration(duration["min_nanos"]), humanDuration(duration["max_nanos"]), duration["total_nanos"]); err != nil {
			return err
		}
	}
	if errors, ok := c["error_spans"].([]Object); ok {
		if window, ok := c["detail_window"].(Object); ok {
			since, until := "start", "unbounded"
			if v, ok := window["since_nanos"]; ok {
				since = humanDuration(v)
			}
			if v, ok := window["until_nanos"]; ok {
				until = humanDuration(v)
			}
			if _, err := fmt.Fprintf(w, "DETAIL WINDOW %s..%s (totals above remain whole file)\n", since, until); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintln(w, "DETAIL SUMMARIES (whole file)"); err != nil {
				return err
			}
		}
		for _, s := range errors {
			if _, err = fmt.Fprintf(w, "  ERR %s %q: %q\n", s["span_id"], s["name"], s["description"]); err != nil {
				return err
			}
		}
	}
	if slow, ok := c["slowest_traces"].([]Object); ok && len(slow) > 0 {
		if _, err = fmt.Fprintln(w, "SLOWEST COMPLETED TRACES"); err != nil {
			return err
		}
		for _, t := range slow {
			if _, err = fmt.Fprintf(w, "  %s  %s  %q\n", t["trace_id"], humanDuration(t["trace_lifetime_nanos"]), t["root_name"]); err != nil {
				return err
			}
		}
	}
	if unfinished, ok := c["incomplete_traces"].([]Object); ok && len(unfinished) > 0 {
		if _, err = fmt.Fprintln(w, "INCOMPLETE / UNRESOLVED TRACES"); err != nil {
			return err
		}
		for _, t := range unfinished {
			if _, err = fmt.Fprintf(w, "  %s  %s  unended=%s\n", t["trace_id"], t["trace_status"], t["unended_spans"]); err != nil {
				return err
			}
		}
	}
	derived := loss["derived_per_span_losses"].(Object)
	if _, err = fmt.Fprintf(w, "  derived ended-span drops: attributes=%s events=%s status=%s (not added to summary)\n", derived["dropped_attributes"], derived["dropped_events"], derived["dropped_status_updates"]); err != nil {
		return err
	}
	if summary, ok := loss["summary"].(Object); ok {
		if _, err = fmt.Fprintf(w, "  latest loss summary seq=%s: rejected starts=%s dropped attributes=%s events=%s status=%s\n", summary["seq"], summary["rejected_starts"], summary["dropped_attributes"], summary["dropped_events"], summary["dropped_status_updates"]); err != nil {
			return err
		}
	}
	diagnostics := c["diagnostics"].(Object)
	if err := writeDiagnostics(w, diagnostics["replay_diagnostics"]); err != nil {
		return err
	}
	for _, tail := range objectArray(diagnostics["tail_conditions"]) {
		if _, err := fmt.Fprintf(w, "  TORN TAIL line %s: %s\n", tail["line"], tail["message"]); err != nil {
			return err
		}
	}
	return nil
}

func writeSpan(w io.Writer, s Object, opts TextOptions) error {
	parent := s["parent_span_id"].(string)
	if parent == "" {
		parent = "none (root)"
	}
	if _, err := fmt.Fprintf(w, "SPAN %s\n  trace: %s\n  root: %s\n  parent: %s (resolved=%v)\n  name: %q\n  scope: %q\n  started: %s\n  ended: %v\n  duration: %s\n", s["span_id"], s["trace_id"], s["root_span_id"], parent, s["parent_resolved"], s["name"], s["scope"], humanWall(s["wall_start"], opts), s["ended"], humanDuration(s["duration_nanos"])); err != nil {
		return err
	}
	if wall, ok := s["wall_end"]; ok {
		if _, err := fmt.Fprintf(w, "  wall end: %s\n", humanWall(wall, opts)); err != nil {
			return err
		}
	}
	status, description := "unset", ""
	if v, ok := s["status"].(Object); ok {
		status = v["code"].(string)
		description = v["description"].(string)
	}
	if description != "" {
		description = " " + strconv.Quote(description)
	}
	if _, err := fmt.Fprintf(w, "  status: %s%s\n", status, description); err != nil {
		return err
	}
	if err := writeAttributes(w, objectArray(s["attributes"]), "  "); err != nil {
		return err
	}
	for _, event := range objectArray(s["events"]) {
		if _, err := fmt.Fprintf(w, "  event %q at %s elapsed=%s\n", event["name"], humanWall(event["wall_time"], opts), humanDuration(event["elapsed_nanos"])); err != nil {
			return err
		}
		if err := writeAttributes(w, objectArray(event["attributes"]), "    "); err != nil {
			return err
		}
	}
	if s["ended"].(bool) {
		if _, err := fmt.Fprintf(w, "  drops: attributes=%s events=%s status=%s\n", s["dropped_attrs"], s["dropped_events"], s["dropped_status"]); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(w, "  drops: unknown (span unended)"); err != nil {
			return err
		}
	}
	children := s["children"].([]string)
	childText := ""
	if len(children) > 0 {
		childText = " " + strings.Join(children, ", ")
	}
	if _, err := fmt.Fprintf(w, "  children (%d):%s\n", len(children), childText); err != nil {
		return err
	}
	return writeDiagnostics(w, s["diagnostics"])
}

func writeAttributes(w io.Writer, attrs []Object, indent string) error {
	for _, a := range attrs {
		var value string
		switch a["type"] {
		case "string":
			value = strconv.Quote(a["value"].(string))
		case "duration":
			value = humanDuration(a["value"])
		case "strings":
			values := a["value"].([]string)
			quoted := make([]string, 0, len(values))
			for _, v := range values {
				quoted = append(quoted, strconv.Quote(v))
			}
			value = "[" + strings.Join(quoted, ", ") + "]"
		default:
			value = fmt.Sprint(a["value"])
		}
		if _, err := fmt.Fprintf(w, "%sattribute %q (%s): %s\n", indent, a["key"], a["type"], value); err != nil {
			return err
		}
	}
	return nil
}

func writeDiagnostics(w io.Writer, v any) error {
	ds := objectArray(v)
	if len(ds) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "DIAGNOSTICS"); err != nil {
		return err
	}
	for _, d := range ds {
		if _, err := fmt.Fprintf(w, "  %s line %s: %s\n", d["type"], d["line"], d["message"]); err != nil {
			return err
		}
	}
	return nil
}

func humanWall(v any, opts TextOptions) string {
	if v == nil {
		return "unknown"
	}
	s := v.(string)
	if s == "" {
		return "unknown"
	}
	if opts.LocalTime {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.Local().Format(time.RFC3339Nano)
		}
	}
	return s
}

func humanDuration(v any) string {
	if v == nil {
		return "unknown"
	}
	n, err := strconv.ParseInt(v.(string), 10, 64)
	if err != nil {
		return "unknown"
	}
	return time.Duration(n).String()
}
