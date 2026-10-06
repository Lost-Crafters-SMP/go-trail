package render

import (
	"encoding/json"
	"io"

	"go.lostcrafters.com/trail/internal/replay"
)

// WriteTraceItems emits reconstructed spans with embedded events, followed by
// trace metadata. Events are not duplicated as separate NDJSON objects.
func WriteTraceItems(w io.Writer, t *replay.MachineTrace) error {
	enc := json.NewEncoder(w)
	for _, s := range OrderedSpans(t) {
		o := Span(s)
		o["type"] = "span"
		o["schema_version"] = "trail.cli.v1"
		if err := enc.Encode(o); err != nil {
			return err
		}
	}
	o := Trace(t)
	delete(o, "spans")
	o["type"] = "trace"
	o["schema_version"] = "trail.cli.v1"
	return enc.Encode(o)
}
