// Package query matches reconstructed spans with explicit AND predicates.
package query

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"go.lostcrafters.com/trail/internal/replay"
)

// Filter holds command-line span predicates.
type Filter struct {
	Status, Scope, Name, TraceID, SpanID string
	Incomplete                           bool
	MinDuration, Since, Until            *int64
	Attributes                           []string
}

// Validate rejects malformed filters before reading a capture.
func (f Filter) Validate() error {
	if f.Status != "" && f.Status != "unset" && f.Status != "ok" && f.Status != "error" {
		return fmt.Errorf("invalid status")
	}
	for _, expr := range f.Attributes {
		_, _, _, err := parseAttribute(expr)
		if err != nil {
			return err
		}
	}
	return nil
}

func parseAttribute(expr string) (string, string, any, error) {
	k, v, ok := strings.Cut(expr, "=")
	if !ok || k == "" {
		return "", "", nil, fmt.Errorf("attribute requires key=type:value")
	}
	t, s, ok := strings.Cut(v, ":")
	if !ok {
		return "", "", nil, fmt.Errorf("attribute requires explicit type")
	}
	var value any
	var err error
	switch t {
	case "str":
		value = s
	case "int":
		value, err = strconv.ParseInt(s, 10, 64)
	case "uint":
		value, err = strconv.ParseUint(s, 10, 64)
	case "duration":
		value, err = time.ParseDuration(s)
	case "float":
		value, err = strconv.ParseFloat(s, 64)
		if err == nil && (math.IsNaN(value.(float64)) || math.IsInf(value.(float64), 0)) {
			err = fmt.Errorf("float must be finite")
		}
	case "bool":
		value, err = strconv.ParseBool(s)
	default:
		err = fmt.Errorf("unsupported attribute type %q", t)
	}
	return k, t, value, err
}

// Match tests final observed span state, excluding unknown durations from thresholds.
func (f Filter) Match(s *replay.MachineSpan) bool {
	status := "unset"
	if s.Status != nil {
		status = string(s.Status.Code)
	}
	if f.Status != "" && status != f.Status || f.Scope != "" && !strings.HasPrefix(s.Scope, f.Scope) || f.Name != "" && !strings.Contains(strings.ToLower(s.Name), strings.ToLower(f.Name)) || f.TraceID != "" && s.TraceID.String() != f.TraceID || f.SpanID != "" && s.SpanID.String() != f.SpanID || f.Incomplete && s.Ended {
		return false
	}
	if f.Since != nil && s.StartElapsedNanos < *f.Since || f.Until != nil && s.StartElapsedNanos > *f.Until {
		return false
	}
	if f.MinDuration != nil {
		if s.DurationNanos == nil {
			return false
		}
		n, _ := strconv.ParseInt(*s.DurationNanos, 10, 64)
		if n < *f.MinDuration {
			return false
		}
	}
	for _, expr := range f.Attributes {
		k, t, v, err := parseAttribute(expr)
		if err != nil {
			return false
		}
		matched := false
		for _, a := range s.Attributes {
			if a.Key != k {
				continue
			}
			switch t {
			case "str":
				matched = a.Type == replay.KindString && a.StringValue == v.(string)
			case "int":
				matched = a.Type == replay.KindInt64 && a.Int64Value == v.(int64)
			case "uint":
				matched = a.Type == replay.KindUint64 && a.Uint64Value == v.(uint64)
			case "duration":
				matched = a.Type == replay.KindDuration && a.DurationValue == int64(v.(time.Duration))
			case "float":
				matched = a.Type == replay.KindFloat64 && a.Float64Value == v.(float64)
			case "bool":
				matched = a.Type == replay.KindBool && a.BoolValue == v.(bool)
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
