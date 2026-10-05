package trail

import (
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Bounds for recorded data. These bound library-owned retained memory and
// encoded record sizes, not caller allocations; they are tuning proposals
// from docs/design.md, validated before release.
const (
	maxActiveSpans        = 4096
	maxAttributesPerSpan  = 64
	maxAttributesPerEvent = 32
	maxKeyBytes           = 256
	maxNameBytes          = 256
	maxStringValueBytes   = 4096
	maxStringsElements    = 64
	maxEncodedRecordBytes = 64 << 10
)

// An AttributeKind identifies the typed value of an Attribute.
type AttributeKind uint8

// Attribute kinds. KindInvalid marks values constructors reject, such as
// non-finite floats; invalid attributes are dropped with counters when
// recorded.
const (
	KindInvalid AttributeKind = iota
	KindString
	KindBool
	KindInt64
	KindUint64
	KindFloat64
	KindDuration
	KindStrings
)

// An Attribute is a typed key/value pair recorded on spans and events.
// Attributes are borrowed-value builders: the Strings constructor does not
// copy its slice, and recording copies bounded data before emitting it.
type Attribute struct {
	key      string
	kind     AttributeKind
	str      string
	boolean  bool
	int64v   int64
	uint64v  uint64
	float64v float64
	strs     []string
}

// Key returns the attribute key.
func (a Attribute) Key() string {
	return a.key
}

// Kind returns the attribute's value kind.
func (a Attribute) Kind() AttributeKind {
	return a.kind
}

// String returns the value of a string attribute, or "" for other kinds.
func (a Attribute) String() string {
	return a.str
}

// Bool returns the value of a bool attribute, or false for other kinds.
func (a Attribute) Bool() bool {
	return a.boolean
}

// Int64 returns the value of an int64 attribute, or 0 for other kinds.
func (a Attribute) Int64() int64 {
	return a.int64v
}

// Uint64 returns the value of a uint64 attribute, or 0 for other kinds.
func (a Attribute) Uint64() uint64 {
	return a.uint64v
}

// Float64 returns the value of a float64 attribute, or 0 for other kinds.
func (a Attribute) Float64() float64 {
	return a.float64v
}

// Duration returns the value of a duration attribute, or 0 for other kinds.
func (a Attribute) Duration() time.Duration {
	return time.Duration(a.int64v)
}

// Strings returns the value of a string-slice attribute, or nil for other
// kinds. The returned slice must not be modified.
func (a Attribute) Strings() []string {
	return a.strs
}

// String returns a string attribute. Empty keys and empty values are
// recorded as-is; bounds apply when the attribute is recorded.
func String(key, value string) Attribute {
	return Attribute{key: key, kind: KindString, str: value}
}

// Bool returns a bool attribute.
func Bool(key string, value bool) Attribute {
	return Attribute{key: key, kind: KindBool, boolean: value}
}

// Int returns an int attribute stored as int64.
func Int(key string, value int) Attribute {
	return Attribute{key: key, kind: KindInt64, int64v: int64(value)}
}

// Int64 returns an int64 attribute.
func Int64(key string, value int64) Attribute {
	return Attribute{key: key, kind: KindInt64, int64v: value}
}

// Uint64 returns a uint64 attribute. Values above MaxInt64 still serialize
// losslessly in the native journal; conversion to integer-limited formats
// is a documented loss.
func Uint64(key string, value uint64) Attribute {
	return Attribute{key: key, kind: KindUint64, uint64v: value}
}

// Float64 returns a float64 attribute. Non-finite values construct an
// invalid attribute that is dropped with a counter when recorded.
func Float64(key string, value float64) Attribute {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return Attribute{kind: KindInvalid}
	}
	return Attribute{key: key, kind: KindFloat64, float64v: value}
}

// Duration returns a duration attribute stored as nanoseconds.
func Duration(key string, value time.Duration) Attribute {
	return Attribute{key: key, kind: KindDuration, int64v: int64(value)}
}

// Strings returns a string-slice attribute. The values slice is borrowed,
// not copied; recording copies bounded data before emission.
func Strings(key string, values []string) Attribute {
	return Attribute{key: key, kind: KindStrings, strs: values}
}

// truncateUTF8 cuts s to at most limit bytes on a UTF-8 boundary and copies
// the retained prefix so it does not pin a larger backing string.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.Clone(s[:cut])
}

// normalizeKey bounds an attribute key. It reports false for empty keys,
// which cannot be recorded.
func normalizeKey(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	return truncateUTF8(key, maxKeyBytes), true
}

// boundAttribute returns a copy of a with its key and string values bounded.
// The callers have already rejected invalid kinds and empty keys.
func boundAttribute(a Attribute) Attribute {
	a.key = truncateUTF8(a.key, maxKeyBytes)
	switch a.kind {
	case KindString:
		a.str = truncateUTF8(a.str, maxStringValueBytes)
	case KindStrings:
		if len(a.strs) > maxStringsElements {
			a.strs = a.strs[:maxStringsElements]
		}
		bounded := make([]string, len(a.strs))
		for i, v := range a.strs {
			bounded[i] = truncateUTF8(v, maxStringValueBytes)
		}
		a.strs = bounded
	}
	return a
}

// estimateAttributeBytes estimates the encoded size of an attribute
// conservatively so oversized records can be dropped before emission.
func estimateAttributeBytes(a Attribute) int {
	size := len(a.key) + 48
	switch a.kind {
	case KindString:
		size += len(a.str)
	case KindStrings:
		for _, v := range a.strs {
			size += len(v) + 8
		}
	default:
		size += 32
	}
	return size
}

// oversizedRecord reports whether a record with the given name and resolved
// attributes would exceed the encoded-record bound and must be dropped
// whole.
func oversizedRecord(name string, attrs []Attribute) bool {
	size := len(name) + 160
	for _, a := range attrs {
		size += estimateAttributeBytes(a)
	}
	return size > maxEncodedRecordBytes
}

// resolveSpanAttributes bounds and dedupes a span-bound attribute batch
// left-to-right against the span's recorded keys, enforcing the per-span
// key limit while permitting updates to existing keys. It returns the
// possibly newly allocated keys map, the accepted attributes sorted by key,
// and a drop count for invalid and over-limit pairs.
func resolveSpanAttributes(keys map[string]struct{}, attrs []Attribute) (map[string]struct{}, []Attribute, int) {
	batch := make([]Attribute, 0, min(len(attrs), maxAttributesPerSpan))
	index := make(map[string]int, len(attrs))
	dropped := 0
	for _, attr := range attrs {
		key, ok := normalizeKey(attr.key)
		if !ok || attr.kind == KindInvalid {
			dropped++
			continue
		}
		attr = boundAttribute(attr)
		if i, exists := index[key]; exists {
			batch[i] = attr // later duplicate wins
			continue
		}
		if _, onSpan := keys[key]; !onSpan && len(keys) >= maxAttributesPerSpan {
			dropped++
			continue
		}
		if keys == nil {
			keys = make(map[string]struct{})
		}
		keys[key] = struct{}{}
		index[key] = len(batch)
		batch = append(batch, attr)
	}
	slices.SortFunc(batch, func(a, b Attribute) int {
		return strings.Compare(a.key, b.key)
	})
	return keys, batch, dropped
}

// resolveEventAttributes bounds and dedupes an event-bound attribute batch
// left-to-right with a per-event key limit. It returns the accepted
// attributes sorted by key and a drop count for invalid and over-limit
// pairs.
func resolveEventAttributes(attrs []Attribute) ([]Attribute, int) {
	batch := make([]Attribute, 0, min(len(attrs), maxAttributesPerEvent))
	index := make(map[string]int, len(attrs))
	dropped := 0
	for _, attr := range attrs {
		key, ok := normalizeKey(attr.key)
		if !ok || attr.kind == KindInvalid {
			dropped++
			continue
		}
		attr = boundAttribute(attr)
		if i, exists := index[key]; exists {
			batch[i] = attr // later duplicate wins
			continue
		}
		if len(batch) >= maxAttributesPerEvent {
			dropped++
			continue
		}
		index[key] = len(batch)
		batch = append(batch, attr)
	}
	slices.SortFunc(batch, func(a, b Attribute) int {
		return strings.Compare(a.key, b.key)
	})
	return batch, dropped
}
