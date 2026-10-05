package trail

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestAttributeConstructorsAndAccessors(t *testing.T) {
	tests := []struct {
		name  string
		attr  Attribute
		kind  AttributeKind
		check func(t *testing.T, a Attribute)
	}{
		{
			name: "string", attr: String("k", "v"), kind: KindString,
			check: func(t *testing.T, a Attribute) {
				if a.String() != "v" {
					t.Fatalf("String() = %q", a.String())
				}
			},
		},
		{
			name: "bool", attr: Bool("k", true), kind: KindBool,
			check: func(t *testing.T, a Attribute) {
				if !a.Bool() {
					t.Fatal("Bool() = false")
				}
			},
		},
		{
			name: "int", attr: Int("k", -3), kind: KindInt64,
			check: func(t *testing.T, a Attribute) {
				if a.Int64() != -3 {
					t.Fatalf("Int64() = %d", a.Int64())
				}
			},
		},
		{
			name: "int64", attr: Int64("k", -4), kind: KindInt64,
			check: func(t *testing.T, a Attribute) {
				if a.Int64() != -4 {
					t.Fatalf("Int64() = %d", a.Int64())
				}
			},
		},
		{
			name: "uint64", attr: Uint64("k", 1<<63), kind: KindUint64,
			check: func(t *testing.T, a Attribute) {
				if a.Uint64() != 1<<63 {
					t.Fatalf("Uint64() = %d", a.Uint64())
				}
			},
		},
		{
			name: "float64", attr: Float64("k", 2.5), kind: KindFloat64,
			check: func(t *testing.T, a Attribute) {
				if a.Float64() != 2.5 {
					t.Fatalf("Float64() = %v", a.Float64())
				}
			},
		},
		{
			name: "duration", attr: Duration("k", 1500*time.Nanosecond), kind: KindDuration,
			check: func(t *testing.T, a Attribute) {
				if a.Duration() != 1500*time.Nanosecond {
					t.Fatalf("Duration() = %v", a.Duration())
				}
			},
		},
		{
			name: "strings", attr: Strings("k", []string{"a", "b"}), kind: KindStrings,
			check: func(t *testing.T, a Attribute) {
				if got := a.Strings(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
					t.Fatalf("Strings() = %v", got)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.attr.Kind() != tt.kind {
				t.Fatalf("Kind() = %v, want %v", tt.attr.Kind(), tt.kind)
			}
			if tt.attr.Key() != "k" {
				t.Fatalf("Key() = %q", tt.attr.Key())
			}
			tt.check(t, tt.attr)
		})
	}
}

func TestNonFiniteFloatsAreInvalid(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		attr := Float64("k", value)
		if attr.Kind() != KindInvalid {
			t.Fatalf("Float64(%v) kind = %v, want KindInvalid", value, attr.Kind())
		}
	}
}

func TestTruncateUTF8(t *testing.T) {
	ascii := strings.Repeat("a", 300)
	if got := truncateUTF8(ascii, maxKeyBytes); len(got) != maxKeyBytes {
		t.Fatalf("ascii truncation len = %d, want %d", len(got), maxKeyBytes)
	}
	multibyte := strings.Repeat("é", 200) // 2 bytes each, 400 total
	got := truncateUTF8(multibyte, 255)
	if len(got) != 254 { // 127 two-byte runes, cut back one byte to a boundary
		t.Fatalf("multibyte truncation len = %d, want 254", len(got))
	}
	for i := 0; i < len(got); i += 2 {
		if got[i] != 0xc3 {
			t.Fatalf("truncated string is not valid UTF-8 at %d", i)
		}
	}
}

func TestResolveSpanAttributesDedupAndLimit(t *testing.T) {
	keys := make(map[string]struct{})
	for i := range maxAttributesPerSpan {
		keys[string(rune('a'+i))] = struct{}{}
	}

	// A new key at the limit drops; an existing key updates.
	keys, resolved, dropped := resolveSpanAttributes(keys, []Attribute{
		String("new", "value"),
		String("a", "updated"),
	})
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if len(resolved) != 1 || resolved[0].Key() != "a" || resolved[0].String() != "updated" {
		t.Fatalf("resolved = %+v, want only the existing-key update", resolved)
	}
	if len(keys) != maxAttributesPerSpan {
		t.Fatalf("keys = %d, want %d with no new key admitted", len(keys), maxAttributesPerSpan)
	}

	// Left-to-right: later duplicates win, empty keys and invalid values drop.
	_, resolved, dropped = resolveSpanAttributes(map[string]struct{}{}, []Attribute{
		String("k", "first"),
		String("k", "second"),
		String("", "no key"),
		Float64("f", math.NaN()),
	})
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
	if len(resolved) != 1 || resolved[0].String() != "second" {
		t.Fatalf("resolved = %+v, want later duplicate", resolved)
	}
}

func TestResolveSpanAttributesSortsAndBounds(t *testing.T) {
	long := strings.Repeat("x", maxStringValueBytes+100)
	_, resolved, dropped := resolveSpanAttributes(map[string]struct{}{}, []Attribute{
		String("b", "v"),
		String("a", long),
		Strings("c", []string{long, long, long}),
	})
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}
	if len(resolved) != 3 || resolved[0].Key() != "a" || resolved[1].Key() != "b" || resolved[2].Key() != "c" {
		t.Fatalf("resolved order = %v", keysOf(resolved))
	}
	for _, a := range resolved {
		if a.Kind() == KindString && len(a.String()) > maxStringValueBytes {
			t.Fatalf("string value not bounded: %d bytes", len(a.String()))
		}
		for _, v := range a.Strings() {
			if len(v) > maxStringValueBytes {
				t.Fatalf("strings element not bounded: %d bytes", len(v))
			}
		}
	}
}

func keysOf(attrs []Attribute) []string {
	keys := make([]string, 0, len(attrs))
	for _, a := range attrs {
		keys = append(keys, a.Key())
	}
	return keys
}

func TestResolveEventAttributesLimit(t *testing.T) {
	attrs := make([]Attribute, 0, maxAttributesPerEvent+2)
	for i := range maxAttributesPerEvent + 2 {
		attrs = append(attrs, Int(string(rune('a'+i%26))+string(rune('a'+i/26)), i))
	}
	resolved, dropped := resolveEventAttributes(attrs)
	if len(resolved) != maxAttributesPerEvent {
		t.Fatalf("resolved = %d, want %d", len(resolved), maxAttributesPerEvent)
	}
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
}

func TestOversizedRecordDroppedWhole(t *testing.T) {
	// Many near-limit string values exceed the encoded record bound.
	attrs := make([]Attribute, 0, maxAttributesPerSpan)
	for i := range maxAttributesPerSpan {
		attrs = append(attrs, String(string(rune('a'+i/26))+string(rune('a'+i%26)), strings.Repeat("v", maxStringValueBytes)))
	}
	if len(attrs) != maxAttributesPerSpan {
		t.Fatalf("setup built %d attrs", len(attrs))
	}
	if !oversizedRecord("", attrs) {
		t.Fatal("maximal attribute batch not detected as oversized")
	}
	small := []Attribute{String("k", "v")}
	if oversizedRecord("name", small) {
		t.Fatal("small batch detected as oversized")
	}
}

func TestStringsSliceCopiedOnRecord(t *testing.T) {
	values := []string{"original"}
	attr := Strings("k", values)
	values[0] = "mutated"
	// The constructor borrows; mutation before recording is visible by
	// design. Bounding copies at record time is what protects output.
	if attr.Strings()[0] != "mutated" {
		t.Fatal("borrowed slice semantics changed")
	}
	bounded := boundAttribute(attr)
	bounded.Strings()[0] = "mutated again"
	if attr.Strings()[0] != "mutated" {
		t.Fatal("boundAttribute did not copy the slice")
	}
}

func TestStringsElementsTruncatedToBound(t *testing.T) {
	values := make([]string, maxStringsElements+10)
	for i := range values {
		values[i] = "v"
	}
	bounded := boundAttribute(Strings("k", values))
	if got := len(bounded.Strings()); got != maxStringsElements {
		t.Fatalf("elements = %d, want %d", got, maxStringsElements)
	}
}
