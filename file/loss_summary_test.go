package file_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/file"
)

// decodeLossSummary is a test-only reference decoder for the v1 checkpoint.
// Required fields are checked separately so missing counters cannot mean zero.
func decodeLossSummary(line string) (trail.LossSummary, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		return trail.LossSummary{}, err
	}
	var kind string
	if err := json.Unmarshal(fields["type"], &kind); err != nil || kind != "loss_summary" {
		return trail.LossSummary{}, errors.New("not a loss_summary")
	}
	var wire struct {
		Seq                      uint64 `json:"seq,string"`
		Wall                     int64  `json:"timeUnixNano,string"`
		Elapsed                  int64  `json:"elapsedNano,string"`
		RejectedStarts           uint64 `json:"rejectedStarts,string"`
		DroppedAttributes        uint64 `json:"droppedAttributes,string"`
		DroppedEvents            uint64 `json:"droppedEvents,string"`
		UnendedSpans             uint64 `json:"unendedSpans,string"`
		UnendedDroppedAttributes uint64 `json:"unendedDroppedAttributes,string"`
		UnendedDroppedEvents     uint64 `json:"unendedDroppedEvents,string"`
	}
	for _, key := range []string{"seq", "timeUnixNano", "elapsedNano", "rejectedStarts", "droppedAttributes", "droppedEvents", "unendedSpans", "unendedDroppedAttributes", "unendedDroppedEvents"} {
		value, ok := fields[key]
		if !ok || string(value) == "null" {
			return trail.LossSummary{}, errors.New("missing required summary field: " + key)
		}
	}
	if err := json.Unmarshal([]byte(line), &wire); err != nil {
		return trail.LossSummary{}, err
	}
	if wire.Seq == 0 || wire.UnendedDroppedAttributes > wire.DroppedAttributes || wire.UnendedDroppedEvents > wire.DroppedEvents ||
		(wire.UnendedSpans == 0 && (wire.UnendedDroppedAttributes != 0 || wire.UnendedDroppedEvents != 0)) {
		return trail.LossSummary{}, errors.New("invalid summary counters or sequence")
	}
	return trail.LossSummary{
		Seq: wire.Seq, Wall: time.Unix(0, wire.Wall), Elapsed: time.Duration(wire.Elapsed),
		RejectedStarts: wire.RejectedStarts, DroppedAttributes: wire.DroppedAttributes, DroppedEvents: wire.DroppedEvents,
		UnendedSpans: wire.UnendedSpans, UnendedDroppedAttributes: wire.UnendedDroppedAttributes, UnendedDroppedEvents: wire.UnendedDroppedEvents,
	}, nil
}

func TestLossSummaryEncodingAndDecoding(t *testing.T) {
	path := tempJournal(t)
	sink, err := file.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := trail.LossSummary{
		Seq: 7, Wall: time.Unix(0, -22), Elapsed: 33,
		RejectedStarts: math.MaxUint64, DroppedAttributes: math.MaxUint64, DroppedEvents: 4,
		UnendedSpans: 1, UnendedDroppedAttributes: 2, UnendedDroppedEvents: 3,
	}
	if err := sink.WriteRecord(want); err != nil {
		t.Fatal(err)
	}
	if err := sink.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	line := readLines(t, path)[1]
	got, err := decodeLossSummary(line)
	if err != nil || got != want {
		t.Fatalf("decoded = %+v, %v; want %+v", got, err, want)
	}
	for _, key := range []string{"traceId", "spanId", "rootSpanId", "durationNano", "reason"} {
		if strings.Contains(line, `"`+key+`"`) {
			t.Fatalf("capture-scoped summary carries %s", key)
		}
	}
}

const firstLossFixture = `{"type":"loss_summary","seq":"3","timeUnixNano":"0","elapsedNano":"10","rejectedStarts":"1","droppedAttributes":"2","droppedEvents":"1","unendedSpans":"1","unendedDroppedAttributes":"2","unendedDroppedEvents":"1"}`
const finalLossFixture = `{"type":"loss_summary","seq":"5","timeUnixNano":"0","elapsedNano":"20","rejectedStarts":"2","droppedAttributes":"3","droppedEvents":"1","unendedSpans":"0","unendedDroppedAttributes":"0","unendedDroppedEvents":"0"}`

// replayLossSummary validates the capture's snapshot progression and replaces
// rather than adds counters. An invalid line never overwrites a valid prefix.
func replayLossSummary(latest **trail.LossSummary, line string) error {
	summary, err := decodeLossSummary(line)
	if err != nil {
		return err
	}
	if prev := *latest; prev != nil {
		if summary.Seq <= prev.Seq || summary.RejectedStarts < prev.RejectedStarts ||
			summary.DroppedAttributes < prev.DroppedAttributes || summary.DroppedEvents < prev.DroppedEvents {
			return errors.New("regressing summary sequence or lifetime totals")
		}
	}
	*latest = &summary
	return nil
}

func TestLossSummaryReplayReplacesAndPreservesValidPrefix(t *testing.T) {
	// The ordinary End counters overlap capture totals; neither they nor
	// previous snapshots are added to the replacement capture totals.
	lines := []string{
		firstLossFixture,
		`{"type":"span_end","seq":"4","droppedAttributes":"3","droppedEvents":"1"}`,
		finalLossFixture,
		`{"type":"loss_summary","seq":"6"`, // torn final line
	}
	var latest *trail.LossSummary
	for i, line := range lines {
		if strings.Contains(line, `"type":"span_end"`) {
			continue
		}
		err := replayLossSummary(&latest, line)
		if err != nil {
			if i != len(lines)-1 {
				t.Fatal(err)
			}
			break // reported torn tail does not replace the last valid snapshot
		}
	}
	if latest == nil || latest.Seq != 5 || latest.RejectedStarts != 2 || latest.DroppedAttributes != 3 || latest.DroppedEvents != 1 || latest.UnendedSpans != 0 {
		t.Fatalf("replacement replay = %+v", latest)
	}
}

func TestLossSummaryReplayDiagnosesRegression(t *testing.T) {
	for _, line := range []string{
		strings.Replace(finalLossFixture, `"seq":"5"`, `"seq":"3"`, 1),
		strings.Replace(finalLossFixture, `"rejectedStarts":"2"`, `"rejectedStarts":"0"`, 1),
		strings.Replace(finalLossFixture, `"droppedAttributes":"3"`, `"droppedAttributes":"1"`, 1),
		strings.Replace(finalLossFixture, `"droppedEvents":"1"`, `"droppedEvents":"0"`, 1),
	} {
		var latest *trail.LossSummary
		if err := replayLossSummary(&latest, firstLossFixture); err != nil {
			t.Fatal(err)
		}
		if err := replayLossSummary(&latest, line); err == nil || latest.Seq != 3 {
			t.Fatalf("regression replaced valid prefix: %+v, %v", latest, err)
		}
	}
}

func TestLossSummaryDecoderRejectsInvalidFields(t *testing.T) {
	for name, line := range map[string]string{
		"missing":          strings.Replace(firstLossFixture, `,"rejectedStarts":"1"`, "", 1),
		"null":             strings.Replace(firstLossFixture, `"rejectedStarts":"1"`, `"rejectedStarts":null`, 1),
		"number":           strings.Replace(firstLossFixture, `"rejectedStarts":"1"`, `"rejectedStarts":1`, 1),
		"negative":         strings.Replace(firstLossFixture, `"rejectedStarts":"1"`, `"rejectedStarts":"-1"`, 1),
		"overflow":         strings.Replace(firstLossFixture, `"rejectedStarts":"1"`, `"rejectedStarts":"18446744073709551616"`, 1),
		"zero seq":         strings.Replace(firstLossFixture, `"seq":"3"`, `"seq":"0"`, 1),
		"invalid subtotal": strings.Replace(firstLossFixture, `"unendedDroppedAttributes":"2"`, `"unendedDroppedAttributes":"3"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeLossSummary(line); err == nil {
				t.Fatal("invalid summary accepted")
			}
		})
	}
}

func TestLossSummaryEncoderRejectsInvalidFields(t *testing.T) {
	for _, record := range []trail.LossSummary{
		{},
		{Seq: 1, DroppedAttributes: 1, UnendedDroppedAttributes: 2, UnendedSpans: 1},
		{Seq: 1, DroppedEvents: 1, UnendedDroppedEvents: 1},
	} {
		sink, err := file.Open(tempJournal(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := sink.WriteRecord(record); err == nil {
			t.Fatalf("invalid summary accepted: %+v", record)
		}
		_ = sink.Shutdown(context.Background())
	}
}
