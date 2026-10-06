// Package replay provides Trail journal decoding and reconstruction.
package replay

import (
	"math/big"
	"strconv"
	"time"

	"go.lostcrafters.com/trail"
)

// AttributeKind identifies the typed value of an attribute.
type AttributeKind uint8

// Attribute kinds preserve journal type discriminators.
const (
	KindInvalid AttributeKind = iota
	KindString
	KindBool
	KindInt64
	KindUint64
	KindFloat64
	KindDuration
	KindStrings
)

// MachineAttribute is a typed attribute in the reconstructed model.
type MachineAttribute struct {
	Key  string
	Type AttributeKind
	// Exactly one of the following is set based on Type.
	StringValue   string
	Int64Value    int64
	Uint64Value   uint64
	Float64Value  float64
	BoolValue     bool
	DurationValue int64
	StringsValue  []string
}

// MachineEvent is a timestamped event on a span.
type MachineEvent struct {
	Seq          uint64
	Name         string
	WallTime     time.Time
	ElapsedNanos int64
	Attributes   []MachineAttribute
}

// MachineDiagnostic represents a replay/lifecycle diagnostic.
type MachineDiagnostic struct {
	Type         string
	TraceID      trail.TraceID
	SpanID       trail.SpanID
	ParentSpanID trail.SpanID
	Line         int
	Message      string
}

// MachineLossSummary is a loss summary snapshot.
type MachineLossSummary struct {
	Seq                         uint64
	WallTime                    time.Time
	ElapsedNanos                int64
	RejectedStarts              uint64
	DroppedAttributes           uint64
	DroppedEvents               uint64
	DroppedStatusUpdates        uint64
	UnendedSpans                uint64
	UnendedDroppedAttributes    uint64
	UnendedDroppedEvents        uint64
	UnendedDroppedStatusUpdates uint64
}

// MachineDerivedLoss represents per-span derived loss totals.
type MachineDerivedLoss struct {
	DroppedAttributes uint64
	DroppedEvents     uint64
	DroppedStatus     uint64
	SpansWithDrops    int
}

// MachineLossAccounting tracks loss accounting state.
type MachineLossAccounting struct {
	Summary        *MachineLossSummary
	DerivedPerSpan MachineDerivedLoss
	LiveSpans      int
}

// TraceStatus represents the completion state of a trace.
type TraceStatus int

// Trace lifecycle states distinguish completion from unresolved roots.
const (
	TraceStatusCompleted TraceStatus = iota
	TraceStatusIncomplete
	TraceStatusUnresolved
)

// MachineSpan represents a reconstructed span.
type MachineSpan struct {
	TraceID           trail.TraceID
	RootSpanID        trail.SpanID
	StartSeq          uint64
	StartElapsedNanos int64
	SpanID            trail.SpanID
	ParentSpanID      trail.SpanID
	ParentResolved    bool
	UnresolvedParent  bool
	Name              string
	Scope             string
	WallStart         string // RFC3339
	WallEnd           *string
	DurationNanos     *string
	Ended             bool
	Attributes        []MachineAttribute
	Status            *MachineStatus
	Events            []MachineEvent
	DroppedAttrs      string
	DroppedEvents     string
	DroppedStatus     string
	Children          []trail.SpanID
	Diagnostics       []MachineDiagnostic
}

// MachineTrace represents a reconstructed trace.
type MachineTrace struct {
	resolved              bool
	CompletionObserved    bool
	TraceID               trail.TraceID
	RootSpanID            trail.SpanID
	RootSpanDurationNanos *string
	TraceLifetimeNanos    *string
	WallStart             string
	WallEnd               *string
	UnendedSpans          int
	Status                TraceStatus
	Spans                 map[trail.SpanID]*MachineSpan
	RootSpan              *MachineSpan
	Diagnostics           []MachineDiagnostic
}

// MachineCapture represents a fully reconstructed capture.
type MachineCapture struct {
	EndedOK                uint64
	EndedError             uint64
	EndedUnset             uint64
	DurationTotal          big.Int
	DurationMin            *int64
	DurationMax            *int64
	TotalTraces            uint64
	CompletedTraces        uint64
	TotalSpans             uint64
	EndedSpans             uint64
	TotalEvents            uint64
	LastRecordSeq          uint64
	LastRecordElapsedNanos int64
	Path                   string
	Header                 JournalHeader
	CaptureStart           *CaptureStartRecord
	Traces                 map[trail.TraceID]*MachineTrace
	LossAccounting         MachineLossAccounting
	Diagnostics            []MachineDiagnostic
	TailCondition          *TailCondition
	ObservedElapsedNanos   int64
}

// JournalHeader identifies the native format version.
type JournalHeader struct {
	Format  string
	Version int
}

// CaptureStartRecord anchors the capture clock.
type CaptureStartRecord struct {
	Seq          uint64
	WallTime     time.Time
	ElapsedNanos int64
}

// TailCondition describes recoverable EOF damage.
type TailCondition struct {
	Type    string
	Line    int
	Message string
}

// MachineStatusCode represents status codes.
type MachineStatusCode string

// Status codes mirror the journal status strings.
const (
	StatusUnset MachineStatusCode = "unset"
	StatusOK    MachineStatusCode = "ok"
	StatusError MachineStatusCode = "error"
)

// MachineStatus is the reconstructed status.
type MachineStatus struct {
	Code        MachineStatusCode
	Description string
}

// AttributeKindString returns the string representation of an attribute kind.
func AttributeKindString(k AttributeKind) string {
	switch k {
	case KindString:
		return "string"
	case KindBool:
		return "bool"
	case KindInt64:
		return "int64"
	case KindUint64:
		return "uint64"
	case KindFloat64:
		return "float64"
	case KindDuration:
		return "duration"
	case KindStrings:
		return "strings"
	default:
		return "invalid"
	}
}

// MachineAttributeValue returns the canonical machine value for an attribute.
func MachineAttributeValue(a MachineAttribute) any {
	switch a.Type {
	case KindString:
		return a.StringValue
	case KindInt64:
		return strconv.FormatInt(a.Int64Value, 10)
	case KindUint64:
		return strconv.FormatUint(a.Uint64Value, 10)
	case KindDuration:
		return strconv.FormatInt(a.DurationValue, 10)
	case KindFloat64:
		return a.Float64Value
	case KindBool:
		return a.BoolValue
	case KindStrings:
		return a.StringsValue
	default:
		panic("invalid attribute kind: " + AttributeKindString(a.Type))
	}
}
