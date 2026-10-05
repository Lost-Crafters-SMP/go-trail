// Package file writes Trail capture journals as versioned JSONL files.
//
// Open creates a new file exclusively; records are appended as complete
// LF-terminated JSON lines with no user-space buffering, so successfully
// written records are visible through the ordinary write path and a crash
// leaves at most a torn final line. The format is Trail's native journal,
// not OTLP JSON; see docs/design.md for the replay contract.
package file

import (
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"go.lostcrafters.com/trail"
)

// journalVersion is the Trail journal format version this sink writes.
const journalVersion = 1

// headerLine is the first line of every journal this sink writes.
var headerLine = `{"format":"trail","version":` + strconv.Itoa(journalVersion) + "}\n"

// encodeRecord renders one record as a complete JSON line without the
// trailing newline.
func encodeRecord(record trail.Record) ([]byte, error) {
	var b []byte
	switch r := record.(type) {
	case trail.CaptureStart:
		if err := validateCaptureStart(r); err != nil {
			return nil, err
		}
		b = append(b, `{"type":"capture_start","seq":`...)
		b = appendUintString(b, r.Seq)
		b = append(b, `,"timeUnixNano":`...)
		b = appendIntString(b, r.Wall.UnixNano())
		b = append(b, `,"elapsedNano":"0"}`...)

	case trail.SpanStart:
		if err := validateSpanIDs("span_start", r.TraceID, r.SpanID, r.RootSpanID); err != nil {
			return nil, err
		}
		if r.Seq == 0 {
			return nil, errField("span_start", "seq")
		}
		b = append(b, `{"type":"span_start","seq":`...)
		b = appendUintString(b, r.Seq)
		b = append(b, `,"timeUnixNano":`...)
		b = appendIntString(b, r.Wall.UnixNano())
		b = append(b, `,"elapsedNano":`...)
		b = appendDurationString(b, r.Elapsed)
		b = append(b, `,"traceId":`...)
		b = appendIDString(b, r.TraceID[:])
		b = append(b, `,"spanId":`...)
		b = appendIDString(b, r.SpanID[:])
		b = append(b, `,"rootSpanId":`...)
		b = appendIDString(b, r.RootSpanID[:])
		if r.ParentSpanID.IsValid() {
			b = append(b, `,"parentSpanId":`...)
			b = appendIDString(b, r.ParentSpanID[:])
		}
		b = append(b, `,"scope":`...)
		b = appendJSONString(b, r.Scope)
		b = append(b, `,"name":`...)
		b = appendJSONString(b, r.Name)
		b = append(b, '}')

	case trail.SpanEnd:
		if err := validateSpanIDs("span_end", r.TraceID, r.SpanID, r.RootSpanID); err != nil {
			return nil, err
		}
		if r.Seq == 0 {
			return nil, errField("span_end", "seq")
		}
		b = append(b, `{"type":"span_end","seq":`...)
		b = appendUintString(b, r.Seq)
		b = append(b, `,"timeUnixNano":`...)
		b = appendIntString(b, r.Wall.UnixNano())
		b = append(b, `,"elapsedNano":`...)
		b = appendDurationString(b, r.Elapsed)
		b = append(b, `,"traceId":`...)
		b = appendIDString(b, r.TraceID[:])
		b = append(b, `,"spanId":`...)
		b = appendIDString(b, r.SpanID[:])
		b = append(b, `,"rootSpanId":`...)
		b = appendIDString(b, r.RootSpanID[:])
		b = append(b, `,"durationNano":`...)
		b = appendDurationString(b, r.Duration)
		b = append(b, `,"droppedAttributes":`...)
		b = appendUintString(b, r.DroppedAttributes)
		b = append(b, `,"droppedEvents":`...)
		b = appendUintString(b, r.DroppedEvents)
		b = append(b, '}')

	case trail.SpanUpdate:
		if err := validateSpanIDs("span_update", r.TraceID, r.SpanID, r.RootSpanID); err != nil {
			return nil, err
		}
		if r.Seq == 0 {
			return nil, errField("span_update", "seq")
		}
		if len(r.Attributes) == 0 && r.Status == nil {
			return nil, errField("span_update", "attributes or status")
		}
		b = append(b, `{"type":"span_update","seq":`...)
		b = appendUintString(b, r.Seq)
		b = append(b, `,"timeUnixNano":`...)
		b = appendIntString(b, r.Wall.UnixNano())
		b = append(b, `,"elapsedNano":`...)
		b = appendDurationString(b, r.Elapsed)
		b = append(b, `,"traceId":`...)
		b = appendIDString(b, r.TraceID[:])
		b = append(b, `,"spanId":`...)
		b = appendIDString(b, r.SpanID[:])
		b = append(b, `,"rootSpanId":`...)
		b = appendIDString(b, r.RootSpanID[:])
		if len(r.Attributes) > 0 {
			b = appendAttributes(b, r.Attributes)
		}
		if r.Status != nil {
			b = appendStatus(b, r.Status)
		}
		b = append(b, '}')

	case trail.Event:
		if err := validateSpanIDs("event", r.TraceID, r.SpanID, r.RootSpanID); err != nil {
			return nil, err
		}
		if r.Seq == 0 {
			return nil, errField("event", "seq")
		}
		if r.Name == "" {
			return nil, errField("event", "name")
		}
		b = append(b, `{"type":"event","seq":`...)
		b = appendUintString(b, r.Seq)
		b = append(b, `,"timeUnixNano":`...)
		b = appendIntString(b, r.Wall.UnixNano())
		b = append(b, `,"elapsedNano":`...)
		b = appendDurationString(b, r.Elapsed)
		b = append(b, `,"traceId":`...)
		b = appendIDString(b, r.TraceID[:])
		b = append(b, `,"spanId":`...)
		b = appendIDString(b, r.SpanID[:])
		b = append(b, `,"rootSpanId":`...)
		b = appendIDString(b, r.RootSpanID[:])
		b = append(b, `,"name":`...)
		b = appendJSONString(b, r.Name)
		if len(r.Attributes) > 0 {
			b = appendAttributes(b, r.Attributes)
		}
		b = append(b, '}')

	case trail.TraceEnd:
		if err := validateSpanIDs("trace_end", r.TraceID, r.RootSpanID, r.RootSpanID); err != nil {
			return nil, err
		}
		if r.Seq == 0 {
			return nil, errField("trace_end", "seq")
		}
		b = append(b, `{"type":"trace_end","seq":`...)
		b = appendUintString(b, r.Seq)
		b = append(b, `,"timeUnixNano":`...)
		b = appendIntString(b, r.Wall.UnixNano())
		b = append(b, `,"elapsedNano":`...)
		b = appendDurationString(b, r.Elapsed)
		b = append(b, `,"traceId":`...)
		b = appendIDString(b, r.TraceID[:])
		b = append(b, `,"rootSpanId":`...)
		b = appendIDString(b, r.RootSpanID[:])
		b = append(b, '}')

	default:
		return nil, errUnsupported(record)
	}
	return b, nil
}

func validateCaptureStart(r trail.CaptureStart) error {
	if r.Seq == 0 {
		return errField("capture_start", "seq")
	}
	return nil
}

func validateSpanIDs(kind string, traceID trail.TraceID, spanID, rootID trail.SpanID) error {
	if !traceID.IsValid() {
		return errField(kind, "traceId")
	}
	if !spanID.IsValid() {
		return errField(kind, "spanId")
	}
	if !rootID.IsValid() {
		return errField(kind, "rootSpanId")
	}
	return nil
}

func errField(kind, field string) error {
	return &FieldError{Record: kind, Field: field}
}

func errUnsupported(record trail.Record) error {
	return &UnsupportedRecordError{Record: record}
}

// appendAttributes appends the attributes array with typed key/value
// entries. Signed, unsigned, and duration values are decimal strings.
func appendAttributes(b []byte, attrs []trail.Attribute) []byte {
	b = append(b, `,"attributes":[`...)
	for i, a := range attrs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"key":`...)
		b = appendJSONString(b, a.Key())
		switch a.Kind() {
		case trail.KindString:
			b = append(b, `,"type":"string","value":`...)
			b = appendJSONString(b, a.String())
		case trail.KindBool:
			b = append(b, `,"type":"bool","value":`...)
			b = append(b, strconv.FormatBool(a.Bool())...)
		case trail.KindInt64:
			b = append(b, `,"type":"int64","value":`...)
			b = appendIntString(b, a.Int64())
		case trail.KindUint64:
			b = append(b, `,"type":"uint64","value":`...)
			b = appendUintString(b, a.Uint64())
		case trail.KindFloat64:
			b = append(b, `,"type":"float64","value":`...)
			b = strconv.AppendFloat(b, a.Float64(), 'g', -1, 64)
		case trail.KindDuration:
			b = append(b, `,"type":"duration","value":`...)
			b = appendDurationString(b, a.Duration())
		case trail.KindStrings:
			b = append(b, `,"type":"strings","value":[`...)
			for j, v := range a.Strings() {
				if j > 0 {
					b = append(b, ',')
				}
				b = appendJSONString(b, v)
			}
			b = append(b, ']')
		default:
			b = append(b, `,"type":"invalid"}`...)
			continue
		}
		b = append(b, '}')
	}
	return append(b, ']')
}

// appendStatus appends a status object with its wire code name and, when
// present, its description.
func appendStatus(b []byte, status *trail.SpanStatus) []byte {
	b = append(b, `,"status":{"code":`...)
	b = appendJSONString(b, status.Code.String())
	if status.Description != "" {
		b = append(b, `,"description":`...)
		b = appendJSONString(b, status.Description)
	}
	return append(b, '}')
}

// FieldError reports a record missing a required field or carrying an
// invalid value for one.
type FieldError struct {
	Record string
	Field  string
}

func (e *FieldError) Error() string {
	return "trail/file: invalid " + e.Record + " record: bad or missing " + e.Field
}

// UnsupportedRecordError reports a record type this sink cannot encode.
type UnsupportedRecordError struct {
	Record trail.Record
}

func (e *UnsupportedRecordError) Error() string {
	return "trail/file: unsupported record type " + typeName(e.Record)
}

func typeName(record trail.Record) string {
	return fmt.Sprintf("%T", record)
}

// appendUintString appends v as a quoted decimal string. Large integers are
// serialized as strings to avoid JavaScript precision loss.
func appendUintString(b []byte, v uint64) []byte {
	b = append(b, '"')
	b = strconv.AppendUint(b, v, 10)
	return append(b, '"')
}

// appendIntString appends v as a quoted signed decimal string.
func appendIntString(b []byte, v int64) []byte {
	b = append(b, '"')
	b = strconv.AppendInt(b, v, 10)
	return append(b, '"')
}

// appendDurationString appends d as a quoted nanosecond decimal string.
func appendDurationString(b []byte, d time.Duration) []byte {
	return appendIntString(b, int64(d))
}

// appendJSONString appends s as a JSON string. It escapes the required
// control characters and quotes, passes valid UTF-8 through, and replaces
// invalid UTF-8 bytes with U+FFFD like encoding/json.
func appendJSONString(b []byte, s string) []byte {
	const hexDigits = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"':
				b = append(b, '\\', '"')
			case c == '\\':
				b = append(b, '\\', '\\')
			case c == '\n':
				b = append(b, '\\', 'n')
			case c == '\r':
				b = append(b, '\\', 'r')
			case c == '\t':
				b = append(b, '\\', 't')
			case c < 0x20:
				b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0x0f])
			default:
				b = append(b, c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, `�`...)
		} else {
			b = append(b, s[i:i+size]...)
		}
		i += size
	}
	return append(b, '"')
}

// appendIDString appends an identifier as a quoted lowercase hexadecimal
// string.
func appendIDString(b []byte, id []byte) []byte {
	const hexDigits = "0123456789abcdef"
	b = append(b, '"')
	for _, c := range id {
		b = append(b, hexDigits[c>>4], hexDigits[c&0x0f])
	}
	return append(b, '"')
}
