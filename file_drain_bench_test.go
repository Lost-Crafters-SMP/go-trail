package trail_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/file"
)

// BenchmarkFileDrain uses the existing 256-operation no-loss workloads, but
// includes writer allocations and checkpoint waits in the measured interval.
func BenchmarkFileDrain(b *testing.B) {
	for _, operation := range []string{"StartEnd", "AddEvent", "SetAttributes"} {
		b.Run(operation, func(b *testing.B) {
			sink, err := file.Open(filepath.Join(b.TempDir(), "drain.jsonl"))
			if err != nil {
				b.Fatal(err)
			}
			batchBytes := 0
			if value := os.Getenv("TRAIL_BENCH_BATCH_BYTES"); value != "" {
				batchBytes, err = strconv.Atoi(value)
				if err != nil {
					b.Fatal(err)
				}
			}
			processor, err := trail.NewAsyncProcessor(sink, trail.WithMaxQueuedRecords(2048), trail.WithMaxQueuedBytes(4<<20), trail.WithMaxBatchBytes(batchBytes))
			if err != nil {
				b.Fatal(err)
			}
			provider, err := trail.NewProvider(processor)
			if err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			tracer := provider.Tracer("benchmark")
			var span trail.Span
			if operation != "StartEnd" {
				_, span = tracer.Start(ctx, "work")
			}
			if err := provider.Flush(ctx); err != nil {
				b.Fatal(err)
			}
			var flush time.Duration
			b.ReportAllocs()
			b.ResetTimer()
			for offset := 0; offset < b.N; {
				window := min(256, b.N-offset)
				for range window {
					switch operation {
					case "StartEnd":
						_, root := tracer.Start(ctx, "work")
						if !root.IsRecording() {
							b.Fatal("rejected start")
						}
						root.End()
					case "AddEvent":
						span.AddEvent("checkpoint")
					case "SetAttributes":
						span.SetAttributes(trail.String("mode", "fast"), trail.Int("count", 12))
					}
				}
				start := time.Now()
				if err := provider.Flush(ctx); err != nil {
					b.Fatal(err)
				}
				flush += time.Since(start)
				offset += window
			}
			b.StopTimer()
			span.End()
			start := time.Now()
			if err := provider.Shutdown(ctx); err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(flush.Nanoseconds())/float64(b.N), "drain-ns/op")
			b.ReportMetric(float64(flush.Nanoseconds())/float64((b.N+255)/256), "flush-ns/window")
			b.ReportMetric(float64(time.Since(start).Nanoseconds()), "shutdown-ns")
		})
	}
}
