# AsyncProcessor enabled benchmarks

Windows/amd64, AMD Ryzen 7 5800X3D, pinned Go 1.27.1, benchmark suffix `-16`.
Three 200 ms runs, executed separately from checks/race tests. No Sync-on-Flush.
Sink choices: real exclusive-create unbuffered file sink, or in-memory discard
sink with no encoding/retention. Setup/final Shutdown excluded. Command:

```sh
mise exec -- go test -run="^$" -bench="Benchmark(AsyncEnabled|Enabled)$" -benchmem -benchtime=200ms -count=3 .
```

Async uses 2048 record credits, 4 MiB charged bytes, and windows of at most 256
operations. Each window is followed by Provider.Flush outside producer timing,
ensuring no saturation/no-op throughput shortcut. `drain-ns/op` divides the
measured checkpoint/delivery/Flush wait by operation count; it is additional to
producer time, not an independent sink-throughput estimate. The writer can run
concurrently with the producer. Go's allocation metrics are process-wide and
can include concurrent writer allocations during producer windows, particularly
for file output; they are not a precise producer-only heap profile. Draining
allocations outside those windows are excluded. Values below are measured ranges,
not guarantees; compare against the contemporaneous Sync baseline, not only the
earlier measurements in design.md.

| Sink / operation | Async producer ns/op | B/op | allocs/op | Async drain ns/op | Sync ns/op | Sync B/op / allocs |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| memory Start/End | 1021–1033 | 672 | 10 | 15.69–17.23 | 579.2–640.1 | 544 / 7 |
| memory AddEvent | 213.3–223.0 | 272 | 4 | 8.013–10.55 | 140.4–151.8 | 144 / 2 |
| memory SetAttributes | 463.6–466.6 | 624 | 7 | 4.572–12.46 | 311.7–369.0 | 304 / 2 |
| file Start/End | 1395–1445 | 816–822 | 10 | 10311–11231 | 11215–13642 | 2497 / 20 |
| file AddEvent | 286.3–291.8 | 302 | 4 | 3574–3664 | 3579–3824 | 888 / 7 |
| file SetAttributes | 668.5–678.5 | 714–716 | 7 | 3627–3734 | 4204–4369 | 1296 / 7 |

Async reduces file-path producer latency roughly 6–13x here, but increases
in-memory producer cost through owned copies/reservation accounting and writer
synchronization. Total drain work is not eliminated. Scheduling, background
activity, and filesystem conditions vary between runs; no premature pooling,
batching, or queue optimization is justified by this initial result.
