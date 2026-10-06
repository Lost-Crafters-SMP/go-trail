package render

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"testing"

	toon "github.com/toon-format/toon-go"

	"go.lostcrafters.com/trail/internal/replay"
)

func TestScalarFidelity(t *testing.T) {
	attrs := []replay.MachineAttribute{{Key: "min", Type: replay.KindInt64, Int64Value: math.MinInt64}, {Key: "max", Type: replay.KindUint64, Uint64Value: math.MaxUint64}, {Key: "duration", Type: replay.KindDuration, DurationValue: 842000000000}, {Key: "float", Type: replay.KindFloat64, Float64Value: 2.5}, {Key: "bool", Type: replay.KindBool}, {Key: "string", Type: replay.KindString}, {Key: "empty", Type: replay.KindStrings}, {Key: "list", Type: replay.KindStrings, StringsValue: []string{"a", "b"}}}
	attrs = append(attrs, replay.MachineAttribute{Key: "int_max", Type: replay.KindInt64, Int64Value: math.MaxInt64}, replay.MachineAttribute{Key: "true", Type: replay.KindBool, BoolValue: true}, replay.MachineAttribute{Key: "text", Type: replay.KindString, StringValue: "quoted \"value\"\nUnicode λ"})
	doc := Object{"attributes": attributes(attrs)}
	var j, tbuf bytes.Buffer
	if err := Write(&j, "json", doc); err != nil {
		t.Fatal(err)
	}
	if err := Write(&tbuf, "toon", doc); err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(j.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	other, err := toon.Decode(tbuf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, other) {
		t.Fatalf("JSON=%#v TOON=%#v", decoded, other)
	}
	values := map[string]any{}
	for _, a := range decoded.(map[string]any)["attributes"].([]any) {
		entry := a.(map[string]any)
		values[entry["key"].(string)] = entry["value"]
	}
	if values["min"] != "-9223372036854775808" || values["max"] != "18446744073709551615" || values["duration"] != "842000000000" || values["bool"] != false || values["float"] != 2.5 {
		t.Fatalf("values=%v", values)
	}
}

func TestLogicalMachineEquivalence(t *testing.T) {
	unknown := &replay.MachineSpan{WallStart: "1970-01-01T00:00:00Z"}
	zero := "0"
	wall := "1970-01-01T00:00:00Z"
	known := &replay.MachineSpan{WallStart: wall, WallEnd: &wall, DurationNanos: &zero, Ended: true, Status: &replay.MachineStatus{Code: replay.StatusOK}}
	for _, tc := range []struct {
		name string
		doc  Object
	}{
		{"unknown", Object{"span": Span(unknown)}}, {"zero", Object{"span": Span(known)}},
		{"inspect", serializationSample("inspect", 0, 0, 0)},
		{"trace", benchmarkDocument(5, 2, 1)},
		{"loss", serializationSample("diagnosticsLossHeavy", 0, 0, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var j, tbuf bytes.Buffer
			if err := Write(&j, "json", tc.doc); err != nil {
				t.Fatal(err)
			}
			if err := Write(&tbuf, "toon", tc.doc); err != nil {
				t.Fatal(err)
			}
			var left any
			if err := json.Unmarshal(j.Bytes(), &left); err != nil {
				t.Fatal(err)
			}
			right, err := toon.Decode(tbuf.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(left, right) {
				t.Fatalf("logical mismatch\nJSON:%s\nTOON:%s", j.String(), tbuf.String())
			}
		})
	}
	for _, key := range []string{"duration_nanos", "wall_end", "status", "dropped_attrs"} {
		if _, ok := Span(unknown)[key]; ok {
			t.Fatalf("unknown %s present", key)
		}
	}
}

func TestDurationPresence(t *testing.T) {
	s := &replay.MachineSpan{}
	if _, ok := Span(s)["duration_nanos"]; ok {
		t.Fatal("invented duration")
	}
	zero := "0"
	s.DurationNanos = &zero
	s.Ended = true
	if Span(s)["duration_nanos"] != "0" {
		t.Fatal("lost zero")
	}
}
