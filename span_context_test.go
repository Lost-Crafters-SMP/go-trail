package trail

import "testing"

func TestNewSpanContext(t *testing.T) {
	traceID := TraceID{0x01}
	spanID := SpanID{0x02}
	sc := NewSpanContext(traceID, spanID)
	if got := sc.TraceID(); got != traceID {
		t.Fatalf("TraceID() = %v, want %v", got, traceID)
	}
	if got := sc.SpanID(); got != spanID {
		t.Fatalf("SpanID() = %v, want %v", got, spanID)
	}
}

func TestSpanContextIsValid(t *testing.T) {
	traceID := TraceID{0x01}
	spanID := SpanID{0x02}
	tests := []struct {
		name string
		sc   SpanContext
		want bool
	}{
		{name: "zero value", sc: SpanContext{}, want: false},
		{name: "zero trace", sc: NewSpanContext(TraceID{}, spanID), want: false},
		{name: "zero span", sc: NewSpanContext(traceID, SpanID{}), want: false},
		{name: "both valid", sc: NewSpanContext(traceID, spanID), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sc.IsValid(); got != tt.want {
				t.Fatalf("IsValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSpanContextString(t *testing.T) {
	sc := NewSpanContext(TraceID{0xde, 0xad}, SpanID{0xbe, 0xef})
	if got, want := sc.String(), "dead0000000000000000000000000000:beef000000000000"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
