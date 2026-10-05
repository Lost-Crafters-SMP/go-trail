package trail_test

import (
	"context"
	"path/filepath"
	"testing"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/file"
)

// discardSink consumes without retaining records or encoding. Together with
// SyncProcessor this isolates core/admission/processor cost, not JSON cost.
type discardSink struct{}

func (discardSink) WriteRecord(_ trail.Record) error { return nil }
func (discardSink) Flush(_ context.Context) error    { return nil }
func (discardSink) Shutdown(_ context.Context) error { return nil }

func BenchmarkEnabled(b *testing.B) {
	for _, sinkName := range []string{"memory", "file"} {
		b.Run(sinkName, func(b *testing.B) {
			for _, operation := range []string{"StartEnd", "AddEvent", "SetAttributes"} {
				b.Run(operation, func(b *testing.B) {
					var sink trail.Sink = discardSink{}
					if sinkName == "file" {
						opened, err := file.Open(filepath.Join(b.TempDir(), "benchmark.jsonl"))
						if err != nil {
							b.Fatal(err)
						}
						sink = opened
					}
					processor, err := trail.NewSyncProcessor(sink)
					if err != nil {
						b.Fatal(err)
					}
					provider, err := trail.NewProvider(processor)
					if err != nil {
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
					for range b.N {
						switch operation {
						case "StartEnd":
							_, root := tracer.Start(ctx, "work")
							root.End()
						case "AddEvent":
							span.AddEvent("checkpoint")
						case "SetAttributes":
							span.SetAttributes(trail.String("mode", "fast"), trail.Int("count", 12))
						}
					}
					b.StopTimer()
					span.End()
					if err := provider.Shutdown(ctx); err != nil {
						b.Fatal(err)
					}
				})
			}
		})
	}
}
