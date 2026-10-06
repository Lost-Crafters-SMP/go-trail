package replay

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDecoderTail(t *testing.T) {
	const header = "{\"format\":\"trail\",\"version\":1}\n"
	const record = `{"type":"capture_start","seq":"1","timeUnixNano":"0","elapsedNano":"0"}`
	for _, tc := range []struct {
		name, suffix string
		tail, bad    bool
	}{
		{"terminated", record + "\n", false, false},
		{"complete EOF", record, false, false},
		{"torn EOF", record + "\n" + `{"type":`, true, false},
		{"malformed interior", "not JSON\n", false, true},
		{"malformed EOF", "not JSON", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDecoder(strings.NewReader(header+tc.suffix), 0)
			if _, err := d.Header(); err != nil {
				t.Fatal(err)
			}
			_, err := d.Next()
			if tc.bad {
				var fe *FormatError
				if !errors.As(err, &fe) {
					t.Fatalf("expected format error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.Next(); !errors.Is(err, io.EOF) {
				t.Fatalf("expected EOF, got %v", err)
			}
			if (d.Tail != nil) != tc.tail {
				t.Fatalf("tail=%v", d.Tail)
			}
		})
	}
}

func TestDecoderHeaderAndLimit(t *testing.T) {
	for _, input := range []string{`{"format":"trail","version":2}`, `{"format":"other","version":1}`, `{}`} {
		if _, err := NewDecoder(strings.NewReader(input), 0).Header(); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if _, err := NewDecoder(strings.NewReader(strings.Repeat("x", 100)), 10).Header(); err == nil {
		t.Fatal("accepted oversized header")
	}
}

func TestMalformedRequiredFields(t *testing.T) {
	for _, input := range []string{
		fixture(`{"type":"capture_start","seq":"2","elapsedNano":"0"}`),
		fixture(strings.Replace(startLine(2, testRoot, "", 0), testTrace, "invalid", 1)),
		fixture(strings.Replace(startLine(2, testRoot, "", 0), testRoot, "invalid", 1)),
		fixture(strings.Replace(startLine(2, testRoot, "", 0), `,"name":"operation"`, "", 1)),
	} {
		for _, opts := range []Options{{}, {Strict: true}, {BestEffort: true}} {
			c, err := Read(strings.NewReader(input), "test", opts)
			if err == nil && (c == nil || len(c.Diagnostics) == 0) {
				t.Fatalf("damage hidden: %s", input)
			}
		}
	}
}
