package trail_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/file"
)

// gateFileSink holds the initial capture so the shutdown benchmark begins with
// exactly 256 queued events plus lifecycle/control records, not a drained file.
type gateFileSink struct {
	*file.Sink
	gate <-chan struct{}
}

func (s gateFileSink) WriteRecord(r trail.Record) error    { <-s.gate; return s.Sink.WriteRecord(r) }
func (s gateFileSink) WriteRecords(r []trail.Record) error { <-s.gate; return s.Sink.WriteRecords(r) }

func BenchmarkFileDrainShutdown(b *testing.B) {
	batchBytes := 0
	if value := os.Getenv("TRAIL_BENCH_BATCH_BYTES"); value != "" {
		var err error
		batchBytes, err = strconv.Atoi(value)
		if err != nil {
			b.Fatal(err)
		}
	}
	ctx := context.Background()
	dir := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()
	b.StopTimer()
	for i := range b.N {
		sink, err := file.Open(filepath.Join(dir, strconv.Itoa(i)+".jsonl"))
		if err != nil {
			b.Fatal(err)
		}
		gate := make(chan struct{})
		ap, err := trail.NewAsyncProcessor(gateFileSink{sink, gate}, trail.WithMaxQueuedRecords(2048), trail.WithMaxQueuedBytes(4<<20), trail.WithMaxBatchBytes(batchBytes))
		if err != nil {
			b.Fatal(err)
		}
		p, err := trail.NewProvider(ap)
		if err != nil {
			b.Fatal(err)
		}
		_, span := p.Tracer("benchmark").Start(ctx, "work")
		for range 256 {
			span.AddEvent("checkpoint")
		}
		span.End()
		b.StartTimer()
		close(gate)
		if err := p.Shutdown(ctx); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
	}
}
