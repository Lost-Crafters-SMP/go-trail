package replay

// RetentionStats is optional internal instrumentation, not a machine-schema
// field. Counts exclude decoder buffers, diagnostics, strings and map overhead.
type RetentionStats struct {
	ReconstructedSpans                                uint64
	RetainedSpans, RetainedAttributes, RetainedEvents int
	PeakSpans, PeakTraces, PeakAttributes, PeakEvents int
	CompletionIDs                                     int
}
