package trail

import "time"

// A Record is one entry submitted through a Processor to a Sink. The set of
// record types is closed: unexported implementations prevent arbitrary user
// records. Records are values; retaining one beyond a Process or WriteRecord
// call requires copying any reference-bearing payload.
type Record interface {
	record()
}

// CaptureStart is the first record of a capture. It anchors the capture's
// clock origin; its elapsed offset is zero by definition.
type CaptureStart struct {
	Seq  uint64
	Wall time.Time
}

// SpanStart reports that a span was admitted into a trace. ParentSpanID is
// the zero value for roots.
type SpanStart struct {
	Seq          uint64
	Wall         time.Time
	Elapsed      time.Duration
	TraceID      TraceID
	SpanID       SpanID
	ParentSpanID SpanID
	RootSpanID   SpanID
	Scope        string
	Name         string
}

// SpanEnd reports that a span ended. Duration is measured from the monotonic
// clock and is authoritative; it is not derived from wall timestamps.
type SpanEnd struct {
	Seq        uint64
	Wall       time.Time
	Elapsed    time.Duration
	TraceID    TraceID
	SpanID     SpanID
	RootSpanID SpanID
	Duration   time.Duration
}

// TraceEnd claims that a trace completed: its root ended and no admitted
// span of the trace remained live.
type TraceEnd struct {
	Seq        uint64
	Wall       time.Time
	Elapsed    time.Duration
	TraceID    TraceID
	RootSpanID SpanID
}

func (CaptureStart) record() {}
func (SpanStart) record()    {}
func (SpanEnd) record()      {}
func (TraceEnd) record()     {}
