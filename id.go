package trail

import (
	"encoding/hex"
	"fmt"
)

// TraceID uniquely identifies a trace within a capture.
//
// The zero value is invalid. Identifiers are formatted as fixed-width
// lowercase hexadecimal strings.
type TraceID [16]byte

// IsValid reports whether the identifier is non-zero.
func (id TraceID) IsValid() bool {
	return id != TraceID{}
}

// String returns the identifier as 32 lowercase hexadecimal digits.
func (id TraceID) String() string {
	return hex.EncodeToString(id[:])
}

// ParseTraceID parses a 32-digit hexadecimal trace identifier. Uppercase and
// lowercase digits are accepted. It rejects wrong lengths, malformed digits,
// and the zero identifier.
func ParseTraceID(s string) (TraceID, error) {
	var id TraceID
	if err := parseHexID(id[:], s, "trace"); err != nil {
		return TraceID{}, err
	}
	if !id.IsValid() {
		return TraceID{}, fmt.Errorf("trail: trace ID must not be zero")
	}
	return id, nil
}

// SpanID uniquely identifies a span within its trace.
//
// The zero value is invalid. Identifiers are formatted as fixed-width
// lowercase hexadecimal strings.
type SpanID [8]byte

// IsValid reports whether the identifier is non-zero.
func (id SpanID) IsValid() bool {
	return id != SpanID{}
}

// String returns the identifier as 16 lowercase hexadecimal digits.
func (id SpanID) String() string {
	return hex.EncodeToString(id[:])
}

// ParseSpanID parses a 16-digit hexadecimal span identifier. Uppercase and
// lowercase digits are accepted. It rejects wrong lengths, malformed digits,
// and the zero identifier.
func ParseSpanID(s string) (SpanID, error) {
	var id SpanID
	if err := parseHexID(id[:], s, "span"); err != nil {
		return SpanID{}, err
	}
	if !id.IsValid() {
		return SpanID{}, fmt.Errorf("trail: span ID must not be zero")
	}
	return id, nil
}

// parseHexID decodes the fixed-width hexadecimal form of an identifier into
// dst, describing the identifier kind in errors.
func parseHexID(dst []byte, s string, kind string) error {
	want := hex.EncodedLen(len(dst))
	if len(s) != want {
		return fmt.Errorf("trail: %s ID must be %d hexadecimal characters, got %d characters", kind, want, len(s))
	}
	if _, err := hex.Decode(dst, []byte(s)); err != nil {
		return fmt.Errorf("trail: malformed %s ID %q: %w", kind, s, err)
	}
	return nil
}
