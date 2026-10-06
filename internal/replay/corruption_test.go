package replay

import (
	"fmt"
	"strings"
	"testing"
)

func TestLifecycleCorruptionPolicies(t *testing.T) {
	update := fmt.Sprintf(`{"type":"span_update","seq":"4","timeUnixNano":"0","elapsedNano":"1","traceId":%q,"spanId":%q,"rootSpanId":%q,"status":{"code":"error"}}`, testTrace, testRoot, testRoot)
	event := fmt.Sprintf(`{"type":"event","seq":"4","timeUnixNano":"0","elapsedNano":"1","traceId":%q,"spanId":%q,"rootSpanId":%q,"name":"checkpoint"}`, testTrace, testRoot, testRoot)
	completion := fmt.Sprintf(`{"type":"trace_end","seq":"3","timeUnixNano":"0","elapsedNano":"1","traceId":%q,"rootSpanId":%q}`, testTrace, testRoot)
	for _, tc := range []struct{ name, input string }{
		{"end without start", fixture(endLine(2, testRoot, 1, 1))},
		{"update after end", fixture(startLine(2, testRoot, "", 0), endLine(3, testRoot, 1, 1), update)},
		{"event after end", fixture(startLine(2, testRoot, "", 0), endLine(3, testRoot, 1, 1), event)},
		{"completion with live root", fixture(startLine(2, testRoot, "", 0), completion)},
		{"inconsistent root", fixture(startLine(2, testRoot, "", 0), strings.Replace(startLine(3, testChild, testRoot, 0), `"rootSpanId":"`+testRoot+`"`, `"rootSpanId":"4444444444444444"`, 1))},
		{"interior malformed JSON", fixture(startLine(2, testRoot, "", 0), "broken", endLine(3, testRoot, 1, 1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Read(strings.NewReader(tc.input), "test", Options{}); err == nil {
				t.Fatal("default accepted corruption")
			}
			c, err := Read(strings.NewReader(tc.input), "test", Options{BestEffort: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Diagnostics) == 0 {
				t.Fatal("recovery hid damage")
			}
			if _, err := Read(strings.NewReader(tc.input), "test", Options{BestEffort: true, Strict: true}); err == nil {
				t.Fatal("strict accepted damage")
			}
		})
	}
}

func TestZeroAndHugeLoss(t *testing.T) {
	for _, value := range []string{"0", "18446744073709551615"} {
		t.Run(value, func(t *testing.T) {
			input := fixture(fmt.Sprintf(`{"type":"loss_summary","seq":"2","timeUnixNano":"0","elapsedNano":"0","rejectedStarts":%q,"droppedAttributes":%q,"droppedEvents":"0","droppedStatusUpdates":"0","unendedSpans":"0","unendedDroppedAttributes":"0","unendedDroppedEvents":"0","unendedDroppedStatusUpdates":"0"}`, value, value))
			c, err := Read(strings.NewReader(input), "test", Options{Strict: true})
			if err != nil {
				t.Fatal(err)
			}
			if c.LossAccounting.Summary == nil || fmt.Sprint(c.LossAccounting.Summary.RejectedStarts) != value {
				t.Fatal("lost exact recorded summary")
			}
		})
	}
}

func TestUnendedLifecyclePolicies(t *testing.T) {
	for _, input := range []string{fixture(startLine(2, testRoot, "", 0)), fixture(startLine(2, testRoot, "", 0), startLine(3, testChild, testRoot, 0), endLine(4, testRoot, 1, 1))} {
		for _, opts := range []Options{{}, {Strict: true}, {BestEffort: true}} {
			c, err := Read(strings.NewReader(input), "test", opts)
			if err != nil {
				t.Fatal(err)
			}
			if c.LossAccounting.LiveSpans != 1 {
				t.Fatalf("live spans=%d", c.LossAccounting.LiveSpans)
			}
		}
	}
	input := fixture(startLine(2, testChild, testRoot, 0))
	if _, err := Read(strings.NewReader(input), "test", Options{Strict: true}); err == nil {
		t.Fatal("strict accepted unresolved parent")
	}
}

func TestLossReplacement(t *testing.T) {
	summary := func(seq, total, live int) string {
		return fmt.Sprintf(`{"type":"loss_summary","seq":"%d","timeUnixNano":"0","elapsedNano":"1","rejectedStarts":"0","droppedAttributes":"%d","droppedEvents":"0","droppedStatusUpdates":"0","unendedSpans":"%d","unendedDroppedAttributes":"0","unendedDroppedEvents":"0","unendedDroppedStatusUpdates":"0"}`, seq, total, live)
	}
	input := fixture(summary(2, 1, 3), summary(3, 2, 0))
	c, err := Read(strings.NewReader(input), "test", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c.LossAccounting.Summary == nil || c.LossAccounting.Summary.DroppedAttributes != 2 || c.LossAccounting.Summary.UnendedSpans != 0 {
		t.Fatal("summaries not replaced")
	}
	input = fixture(summary(2, 2, 0), summary(3, 1, 0))
	if _, err := Read(strings.NewReader(input), "test", Options{}); err == nil {
		t.Fatal("accepted regressing lifetime total")
	}
	c, err = Read(strings.NewReader(input), "test", Options{BestEffort: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.LossAccounting.Summary.DroppedAttributes != 2 {
		t.Fatal("invalid summary replaced last valid summary")
	}
}
