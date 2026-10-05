package trail_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/file"
)

// BenchmarkAsyncEnabled uses no-loss windows, not saturated rejection loops.
// Producer timing excludes checkpoint/drain waits. Go allocation measurements
// are process-wide and can include background writer allocations while timed.
func BenchmarkAsyncEnabled(b *testing.B) {
	for _, sinkName := range []string{"memory", "file"} {
		b.Run(sinkName, func(b *testing.B) {
			for _, operation := range []string{"StartEnd", "AddEvent", "SetAttributes"} {
				b.Run(operation, func(b *testing.B) {
					var sink trail.Sink = discardSink{}
					if sinkName == "file" {
						opened, err := file.Open(filepath.Join(b.TempDir(), "async.jsonl"))
						if err != nil {
							b.Fatal(err)
						}
						sink = opened
					}
					processor, err := trail.NewAsyncProcessor(sink, trail.WithMaxQueuedRecords(2048), trail.WithMaxQueuedBytes(4<<20))
					if err != nil {
						b.Fatal(err)
					}
					provider, err := trail.NewProvider(processor)
					if err != nil {
						_ = processor.Shutdown(context.Background())
						b.Fatal(err)
					}
					tracer := provider.Tracer("benchmark")
					ctx := context.Background()
					var span trail.Span
					if operation != "StartEnd" {
						_, span = tracer.Start(ctx, "work")
					}
					b.ReportAllocs()
					b.ResetTimer()
					b.StopTimer()
					var drain time.Duration
					for offset := 0; offset < b.N; {
						window := min(256, b.N-offset)
						b.StartTimer()
						for range window {
							switch operation {
							case "StartEnd":
								_, root := tracer.Start(ctx, "work")
								if !root.IsRecording() {
									b.Fatal("benchmark admission rejected")
								}
								root.End()
							case "AddEvent":
								span.AddEvent("checkpoint")
							case "SetAttributes":
								span.SetAttributes(trail.String("mode", "fast"), trail.Int("count", 12))
							}
						}
						b.StopTimer()
						start := time.Now()
						if err := provider.Flush(ctx); err != nil {
							b.Fatal(err)
						}
						drain += time.Since(start)
						offset += window
					}
					span.End()
					if err := provider.Shutdown(ctx); err != nil {
						b.Fatal(err)
					}
					b.ReportMetric(float64(drain.Nanoseconds())/float64(b.N), "drain-ns/op")
				})
			}
		})
	}
}
