package replay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"

	"go.lostcrafters.com/trail"
)

// FormatError identifies a journal line that cannot be decoded safely.
type FormatError struct {
	Line  int
	Field string
	Err   error
}

func (e *FormatError) Error() string {
	return fmt.Sprintf("trail: journal line %d, %s: %v", e.Line, e.Field, e.Err)
}

func (e *FormatError) Unwrap() error { return e.Err }

// Record is a validated journal object awaiting lifecycle reconstruction.
// Unknown additive fields are retained without changing replay semantics.
type Record struct {
	Line         int
	Type         string
	Seq          uint64
	WallNanos    int64
	ElapsedNanos int64
	Fields       map[string]json.RawMessage
}

// Decoder reads journal lines with an explicit reader limit, independent of
// writer limits. It never sorts records by timestamps.
type Decoder struct {
	r            *bufio.Reader
	line         int
	maxLineBytes int
	Tail         *TailCondition
}

// NewDecoder accepts records up to maxLineBytes; zero selects 1 MiB.
func NewDecoder(r io.Reader, maxLineBytes int) *Decoder {
	if maxLineBytes <= 0 {
		maxLineBytes = 1 << 20
	}
	return &Decoder{r: bufio.NewReader(r), maxLineBytes: maxLineBytes}
}

func (d *Decoder) readLine() ([]byte, error) {
	var line []byte
	for {
		part, err := d.r.ReadSlice('\n')
		if len(line)+len(part) > d.maxLineBytes {
			return nil, &FormatError{Line: d.line + 1, Field: "line", Err: fmt.Errorf("record exceeds %d bytes", d.maxLineBytes)}
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(line) > 0 {
			d.line++
		}
		return line, err
	}
}

// Header must be called once before Next.
func (d *Decoder) Header() (JournalHeader, error) {
	line, err := d.readLine()
	if err != nil && !errors.Is(err, io.EOF) {
		return JournalHeader{}, err
	}
	var h struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(line, &h); err != nil {
		return JournalHeader{}, &FormatError{d.line, "header", err}
	}
	if h.Format != "trail" || h.Version != 1 {
		return JournalHeader{}, &FormatError{d.line, "header", fmt.Errorf("unsupported format %q version %d", h.Format, h.Version)}
	}
	return JournalHeader{Format: h.Format, Version: h.Version}, nil
}

// Next accepts a complete final record without LF. Only an unexpected EOF
// inside JSON is recoverable as a torn tail; other malformed JSON is an error.
func (d *Decoder) Next() (Record, error) {
	line, readErr := d.readLine()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return Record{}, readErr
	}
	if len(line) == 0 && errors.Is(readErr, io.EOF) {
		return Record{}, io.EOF
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		if errors.Is(readErr, io.EOF) && bytes.Contains([]byte(err.Error()), []byte("unexpected end of JSON input")) {
			d.Tail = &TailCondition{Type: "torn_tail", Line: d.line, Message: "incomplete JSON at EOF"}
			return Record{}, io.EOF
		}
		return Record{}, &FormatError{d.line, "JSON", err}
	}
	r := Record{Line: d.line, Fields: fields}
	if err := json.Unmarshal(fields["type"], &r.Type); err != nil {
		return Record{}, &FormatError{d.line, "type", err}
	}
	var err error
	if r.Seq, err = quotedUint(fields, "seq"); err != nil || r.Seq == 0 {
		return Record{}, &FormatError{d.line, "seq", fmt.Errorf("expected positive quoted uint64")}
	}
	if r.WallNanos, err = quotedInt(fields, "timeUnixNano"); err != nil {
		return Record{}, &FormatError{d.line, "timeUnixNano", err}
	}
	if r.ElapsedNanos, err = quotedInt(fields, "elapsedNano"); err != nil || r.ElapsedNanos < 0 {
		return Record{}, &FormatError{d.line, "elapsedNano", fmt.Errorf("expected nonnegative quoted int64")}
	}
	switch r.Type {
	case "capture_start", "span_start", "span_update", "event", "span_end", "trace_end", "loss_summary":
	default:
		return Record{}, &FormatError{d.line, "type", fmt.Errorf("unsupported record %q", r.Type)}
	}
	if err := validateRecord(r); err != nil {
		return Record{}, &FormatError{d.line, "record", err}
	}
	return r, nil
}

func requiredString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok || string(raw) == "null" {
		return "", fmt.Errorf("missing %s", key)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return s, nil
}

func validateRecord(r Record) error {
	f := r.Fields
	if r.Type == "capture_start" {
		if r.Seq != 1 || r.ElapsedNanos != 0 {
			return fmt.Errorf("capture_start must have seq 1 and elapsed 0")
		}
		return nil
	}
	if r.Type == "loss_summary" {
		keys := []string{"rejectedStarts", "droppedAttributes", "droppedEvents", "droppedStatusUpdates", "unendedSpans", "unendedDroppedAttributes", "unendedDroppedEvents", "unendedDroppedStatusUpdates"}
		v := make([]uint64, len(keys))
		for i, k := range keys {
			n, err := quotedUint(f, k)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			v[i] = n
		}
		if v[5] > v[1] || v[6] > v[2] || v[7] > v[3] || (v[4] == 0 && (v[5] != 0 || v[6] != 0 || v[7] != 0)) {
			return fmt.Errorf("invalid live loss subtotals")
		}
		return nil
	}
	tid, err := requiredString(f, "traceId")
	if err != nil {
		return err
	}
	if _, err = trail.ParseTraceID(tid); err != nil {
		return err
	}
	root, err := requiredString(f, "rootSpanId")
	if err != nil {
		return err
	}
	if _, err = trail.ParseSpanID(root); err != nil {
		return err
	}
	if r.Type == "trace_end" {
		return nil
	}
	sid, err := requiredString(f, "spanId")
	if err != nil {
		return err
	}
	if _, err = trail.ParseSpanID(sid); err != nil {
		return err
	}
	switch r.Type {
	case "span_start":
		if _, err = requiredString(f, "scope"); err != nil {
			return err
		}
		if _, err = requiredString(f, "name"); err != nil {
			return err
		}
		if _, ok := f["parentSpanId"]; ok {
			p, e := requiredString(f, "parentSpanId")
			if e != nil {
				return e
			}
			if _, e = trail.ParseSpanID(p); e != nil {
				return e
			}
		}
	case "span_end":
		n, e := quotedInt(f, "durationNano")
		if e != nil || n < 0 {
			return fmt.Errorf("invalid durationNano")
		}
		for _, k := range []string{"droppedAttributes", "droppedEvents", "droppedStatusUpdates"} {
			if _, e = quotedUint(f, k); e != nil {
				return fmt.Errorf("%s: %w", k, e)
			}
		}
	case "event":
		n, e := requiredString(f, "name")
		if e != nil || n == "" {
			return fmt.Errorf("event requires name")
		}
	case "span_update":
		if _, ok := f["attributes"]; !ok {
			if _, ok = f["status"]; !ok {
				return fmt.Errorf("empty span_update")
			}
		}
	}
	if raw, ok := f["attributes"]; ok {
		if _, err = decodeAttributes(raw); err != nil {
			return err
		}
	}
	if raw, ok := f["status"]; ok {
		var status map[string]json.RawMessage
		if err = json.Unmarshal(raw, &status); err != nil {
			return err
		}
		code, e := requiredString(status, "code")
		if e != nil {
			return e
		}
		if code != "unset" && code != "ok" && code != "error" {
			return fmt.Errorf("invalid status %q", code)
		}
		if _, ok = status["description"]; ok {
			if _, e = requiredString(status, "description"); e != nil {
				return e
			}
		}
	}
	return nil
}

func decodeAttributes(raw json.RawMessage) ([]MachineAttribute, error) {
	if string(raw) == "null" {
		return nil, fmt.Errorf("null attributes")
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	out := make([]MachineAttribute, 0, len(entries))
	seen := map[string]bool{}
	for _, f := range entries {
		key, err := requiredString(f, "key")
		if err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate attribute key %q", key)
		}
		seen[key] = true
		kind, err := requiredString(f, "type")
		if err != nil {
			return nil, err
		}
		a := MachineAttribute{Key: key}
		v, ok := f["value"]
		if !ok || string(v) == "null" {
			return nil, fmt.Errorf("missing attribute value")
		}
		switch kind {
		case "string":
			a.Type = KindString
			a.StringValue, err = requiredString(f, "value")
		case "int64":
			a.Type = KindInt64
			a.Int64Value, err = quotedInt(f, "value")
		case "uint64":
			a.Type = KindUint64
			a.Uint64Value, err = quotedUint(f, "value")
		case "duration":
			a.Type = KindDuration
			a.DurationValue, err = quotedInt(f, "value")
		case "bool":
			a.Type = KindBool
			err = json.Unmarshal(v, &a.BoolValue)
		case "float64":
			a.Type = KindFloat64
			err = json.Unmarshal(v, &a.Float64Value)
			if math.IsInf(a.Float64Value, 0) || math.IsNaN(a.Float64Value) {
				err = fmt.Errorf("non-finite float")
			}
		case "strings":
			a.Type = KindStrings
			err = json.Unmarshal(v, &a.StringsValue)
		default:
			err = fmt.Errorf("unsupported attribute type %q", kind)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func quotedInt(fields map[string]json.RawMessage, key string) (int64, error) {
	var s string
	if err := json.Unmarshal(fields[key], &s); err != nil {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

func quotedUint(fields map[string]json.RawMessage, key string) (uint64, error) {
	var s string
	if err := json.Unmarshal(fields[key], &s); err != nil {
		return 0, err
	}
	return strconv.ParseUint(s, 10, 64)
}
