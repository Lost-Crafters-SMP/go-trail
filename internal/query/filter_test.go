package query

import (
	"testing"

	"go.lostcrafters.com/trail/internal/replay"
)

func TestAttributePredicates(t *testing.T) {
	s := &replay.MachineSpan{Attributes: []replay.MachineAttribute{{Key: "count", Type: replay.KindInt64, Int64Value: 1}, {Key: "enabled", Type: replay.KindBool}}}
	f := Filter{Attributes: []string{"count=int:1", "enabled=bool:false"}}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	if !f.Match(s) {
		t.Fatal("AND predicate rejected match")
	}
	f.Attributes = append(f.Attributes, "count=str:1")
	if f.Match(s) {
		t.Fatal("ignored attribute type")
	}
	for _, bad := range []string{"key", "key=1", "key=unknown:x", "key=int:bad"} {
		if err := (Filter{Attributes: []string{bad}}).Validate(); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestUnknownDurationExcluded(t *testing.T) {
	zero := int64(0)
	f := Filter{MinDuration: &zero}
	s := &replay.MachineSpan{}
	if f.Match(s) {
		t.Fatal("unknown duration treated as zero")
	}
	duration := "0"
	s.DurationNanos = &duration
	if !f.Match(s) {
		t.Fatal("authoritative zero excluded")
	}
}
