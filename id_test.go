package trail

import "testing"

func TestTraceIDIsValid(t *testing.T) {
	tests := []struct {
		name string
		id   TraceID
		want bool
	}{
		{name: "zero", id: TraceID{}, want: false},
		{name: "nonzero first byte", id: TraceID{0x01}, want: true},
		{name: "nonzero last byte", id: TraceID{15: 0xff}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.IsValid(); got != tt.want {
				t.Fatalf("IsValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTraceIDString(t *testing.T) {
	id := TraceID{0xde, 0xad, 0xbe, 0xef}
	if got, want := id.String(), "deadbeef000000000000000000000000"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestParseTraceID(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    TraceID
		wantErr bool
	}{
		{name: "lowercase", in: "deadbeef000000000000000000000042", want: TraceID{0xde, 0xad, 0xbe, 0xef, 15: 0x42}},
		{name: "uppercase", in: "DEADBEEF000000000000000000000042", want: TraceID{0xde, 0xad, 0xbe, 0xef, 15: 0x42}},
		{name: "mixed case", in: "DeAdBeEf000000000000000000000042", want: TraceID{0xde, 0xad, 0xbe, 0xef, 15: 0x42}},
		{name: "empty", in: "", wantErr: true},
		{name: "too short", in: "deadbeef", wantErr: true},
		{name: "too long", in: "deadbeef00000000000000000000000000", wantErr: true},
		{name: "malformed digits", in: "deadbeef0000000000000000000000zz", wantErr: true},
		{name: "zero", in: "00000000000000000000000000000000", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTraceID(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseTraceID(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTraceID(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseTraceID(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseTraceIDRoundTrip(t *testing.T) {
	id := TraceID{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10}
	parsed, err := ParseTraceID(id.String())
	if err != nil {
		t.Fatalf("ParseTraceID(String()) unexpected error: %v", err)
	}
	if parsed != id {
		t.Fatalf("round trip = %v, want %v", parsed, id)
	}
}

func TestSpanIDIsValid(t *testing.T) {
	tests := []struct {
		name string
		id   SpanID
		want bool
	}{
		{name: "zero", id: SpanID{}, want: false},
		{name: "nonzero first byte", id: SpanID{0x01}, want: true},
		{name: "nonzero last byte", id: SpanID{7: 0xff}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.IsValid(); got != tt.want {
				t.Fatalf("IsValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSpanIDString(t *testing.T) {
	id := SpanID{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}
	if got, want := id.String(), "123456789abcdef0"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestParseSpanID(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    SpanID
		wantErr bool
	}{
		{name: "lowercase", in: "123456789abcdef0", want: SpanID{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}},
		{name: "uppercase", in: "123456789ABCDEF0", want: SpanID{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}},
		{name: "empty", in: "", wantErr: true},
		{name: "too short", in: "1234", wantErr: true},
		{name: "too long", in: "123456789abcdef00", wantErr: true},
		{name: "malformed digits", in: "123456789abcdefg", wantErr: true},
		{name: "zero", in: "0000000000000000", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSpanID(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseSpanID(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSpanID(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseSpanID(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseSpanIDRoundTrip(t *testing.T) {
	id := SpanID{0x00, 0xff, 0x11, 0xee, 0x22, 0xdd, 0x33, 0xcc}
	parsed, err := ParseSpanID(id.String())
	if err != nil {
		t.Fatalf("ParseSpanID(String()) unexpected error: %v", err)
	}
	if parsed != id {
		t.Fatalf("round trip = %v, want %v", parsed, id)
	}
}
