package render

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
)

func objectArray(v any) []Object { a, _ := v.([]Object); return a }

func writeDocumentItems(w io.Writer, d Object) error {
	emit := func(kind string, item Object) error {
		o := maps.Clone(item)
		o["schema_version"] = "trail.cli.v1"
		o["type"] = kind
		return json.NewEncoder(w).Encode(o)
	}
	if _, ok := d["type"]; ok {
		return json.NewEncoder(w).Encode(d)
	}
	for _, key := range []string{"span", "trace"} {
		if o, ok := d[key].(Object); ok {
			return emit(key, o)
		}
	}
	for _, key := range []string{"spans", "traces"} {
		for _, o := range objectArray(d[key]) {
			if err := emit(key[:len(key)-1], o); err != nil {
				return err
			}
		}
	}
	if c, ok := d["capture"].(Object); ok {
		return emit("capture_summary", c)
	}
	if diagnostics, ok := d["diagnostics"]; ok {
		return emit("query_summary", Object{"diagnostics": diagnostics})
	}
	return nil
}

func writeCompact(w io.Writer, d Object) error {
	span := func(s Object) error {
		duration := "?"
		if v, ok := s["duration_nanos"].(string); ok {
			duration = v
		}
		_, err := fmt.Fprintf(w, "SPAN\t%s\t%s\t%s\t%q\t%s\t%v\n", s["trace_id"], s["span_id"], s["parent_span_id"], s["name"], duration, s["ended"])
		return err
	}
	trace := func(t Object) error {
		for _, s := range objectArray(t["spans"]) {
			if err := span(s); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(w, "TRACE\t%s\t%s\n", t["trace_id"], t["trace_status"])
		return err
	}
	if s, ok := d["span"].(Object); ok {
		return span(s)
	}
	if t, ok := d["trace"].(Object); ok {
		return trace(t)
	}
	for _, s := range objectArray(d["spans"]) {
		if err := span(s); err != nil {
			return err
		}
	}
	for _, t := range objectArray(d["traces"]) {
		if err := trace(t); err != nil {
			return err
		}
	}
	if c, ok := d["capture"].(Object); ok {
		for _, t := range objectArray(c["traces"]) {
			if err := trace(t); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(w, "CAPTURE\t%q\t%s\t%s\t%s\n", c["path"], c["traces_total"], c["spans_total"], c["events_total"])
		return err
	}
	return nil
}
