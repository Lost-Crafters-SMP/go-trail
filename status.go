package trail

// A StatusCode is the outcome of a span as set explicitly by the
// application. Status is never inferred at End.
type StatusCode int

// Status codes. Unset is the default; OK and Error are explicit. The
// numeric values align with the OpenTelemetry status codes Trail's
// semantics borrow, though precedence rules differ: the last accepted
// SetStatus call wins, including a reset to Unset.
const (
	StatusUnset StatusCode = 0
	StatusOK    StatusCode = 1
	StatusError StatusCode = 2
)

// String returns the wire name of the code.
func (c StatusCode) String() string {
	switch c {
	case StatusUnset:
		return "unset"
	case StatusOK:
		return "ok"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// A SpanStatus is an explicit status update recorded on a span.
type SpanStatus struct {
	Code        StatusCode
	Description string
}
