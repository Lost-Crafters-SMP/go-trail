package trail

import "context"

// A Processor delivers records to a sink. It is the pipeline boundary
// between tracing code and output: tracers and spans never write to sinks
// directly.
//
// Process reports acceptance of a record, not a durability guarantee.
// Processor implementations must be safe for concurrent use and must
// serialize their sink calls so that WriteRecord, Flush, and Shutdown never
// overlap for one sink.
type Processor interface {
	Process(record Record) error
	Flush(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

// A Sink receives records from a processor and owns their encoding and
// cleanup. Sinks must be safe for the serialized calling pattern their
// processor provides; a sink owns no queue or processor-mode concepts.
type Sink interface {
	WriteRecord(record Record) error
	Flush(ctx context.Context) error
	Shutdown(ctx context.Context) error
}
