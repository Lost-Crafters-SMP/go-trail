# Async file drain experiments

Control source: signed `c1017681025ca2741e46caee50e2a9ad2262fbe5` (same tree as
the pre-signing `4e1f38cba15e0189fced700a728f3ef01046b5ab`). No prior artifacts
are overwritten. See `environment.json` for machine/toolchain/options.

## Methodology

Two separate views, ten fixed 100000-operation samples each:

- Existing `BenchmarkAsyncEnabled/file`: unchanged producer timer and
  `drain-ns/op` checkpoint waits; B/op includes only writer activity while timed.
- New `BenchmarkFileDrain`: same operations, queue 2048 records / 4 MiB,
  256-operation no-loss windows. Timer/allocations include production **and all
  drains**, plus each cumulative-summary barrier. `ns/op` is end-to-end;
  `drain-ns/op` is only time spent waiting in Flush per operation. Mean
  `flush-ns/window` includes delivery of up to 256 operations, not just File.Sync.
  Final `shutdown-ns` includes span End and final summary/drain cleanup timing
  outside the allocation timer. It is one sample per benchmark repetition, not
  a calibrated steady-state shutdown benchmark. File.Sync is disabled throughout.

Fixed iterations avoid the producer-only timer choosing millions of operations
while excluding I/O waits. They differ from earlier 1s captures; compare these
stage controls with each other, not as a new ten-sample historical 1s comparison.
CPU and full-rate allocation profiles are separate diagnostic runs, never timing
evidence. Stage timing, profiles and checks run serially.

```powershell
# Replace placeholders with external directories/executable locations.
./benchmarks/file-drain/capture-stage.ps1 -Stage control -Local '<profile-dir>' -Benchstat '<profile-dir>/benchstat.exe'
./benchmarks/file-drain/capture-stage.ps1 -Stage reuse -Local '<profile-dir>' -Benchstat '<profile-dir>/benchstat.exe'
```

Benchstat: `golang.org/x/perf/cmd/benchstat` at
`v0.0.0-20260929162123-406019bb8b68`, the same binary as the producer pass.
Profile reports normalize paths to `<repo>`, `<temp>`, `<cache>` and
`<profile-dir>`; numerical data are not edited. Binary profiles stay outside Git.

## Initial architecture findings

One serialized writer removes one FIFO entry, invokes WriteRecord, then releases
its ordinary credit/charged bytes. A barrier can contain a loss-summary record;
it is written before Flush. Lifecycle credits already converted to queued end
entries remain occupied until writer consumption. A failed write latches the
processor error; subsequent accepted entries are discarded without further
writes, and barriers report the sticky failure. Cancellation does not interrupt
an in-flight sink write and Shutdown remains resumable.

File WriteRecord currently encodes from nil before taking its mutex; writeAll
uses os.File.Write and loops only on a positive short write without an error.
No user-space byte buffer spans calls. On a normal complete write it makes one
os.File.Write per record, plus one header write per file. This is **source-derived**
initial evidence, not measured kernel syscall counts. OS write implementation
may retry internally; CPU stacks alone cannot count kernel calls.

The current file encoder validates record kind/IDs/seq/update content, but has
**no independent maximum encoded-record-size rejection**. Async admission has
payload bounds. Preserve existing validation rather than inventing a new sink
rejection policy; clamp scratch retention even for oversized direct sink callers.

## Stage 1: encoder scratch reuse (kept)

Sink-owned scratch, lazily pre-sized to 1024 bytes, is reused under its mutex.
Capacity above 64 KiB is discarded after the record; Shutdown releases scratch.
Encoding/validation still precedes output, JSONL bytes are identical, and there
is no buffering across WriteRecord calls. The sink's narrow file interface adds
a test seam for actual os.File.Write counts and failure injection; it does not
change public APIs or producer code. No sync.Pool or additional goroutines.

| Operation | End-to-end ns/op control → reuse | Drain ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| StartEnd | 11448.5 → 10556.5 | 10231.5 → 9381 | 2486 → 529 | 21 → 8 |
| AddEvent | 3804 → 3273 | 3602 → 3164.5 | 877 → 129 | 7 → 2 |
| SetAttributes | 4261.5 → 3510 | 3672 → 3108 | 1317 → 321 | 10 → 5 |

Benchstat: total time -7.79% (p=0.029), -13.96% and -17.63% (p<0.001);
drain -8.31% (p=0.004), -12.15% and -15.36% (p<0.001). All allocation/byte
reductions p<0.001. Flush window medians: 2.617 → 2.399 ms, 921.16 → 809.43 us,
939.06 → 794.90 us, same significance as drain. The initial final-Shutdown
samples often read **zero** at this machine's clock resolution and cannot support
a latency claim; retain these raw samples rather than replacing zeros.

Profiles independently confirm the per-record append/growth allocation sites
disappear: remaining full-rate allocations are producer record/payload/state
sites and setup/checkpoints. CPU writeFile/execIO share remains approximately
93% / 100% / 91.43% for StartEnd / AddEvent / SetAttributes after reuse (short
profiles, 10ms sampling; attribution is not a causal isolation of OS costs).
This justifies a separate bounded-write batching experiment. Seven representative
records measured **seven os.File.Write calls, 1622 bytes, 231.71 bytes/write**
in `reuse-write-count.txt`. Normal initial-path count was source-derived; this
new test observes actual calls, not the number of internal kernel retries.

Correctness: equivalence for all record types/escaping/nested attrs/status,
oversized record retention clamp, unsupported/field validation-before-output,
concurrent standalone sink calls, short-progress writes, zero-progress writes,
partial/immediate failures, failure latching/no retry. Focused race tests repeated
ten times. Initial check exposed test-only error-comparison lint warnings, fixed
using errors.Is; full checks/race rerun before commit.

## Stage 2: optional bounded batching (kept, opt-in)

`BatchSink.WriteRecords` is an optional capability. `WithMaxBatchBytes(n)` bounds
the async FIFO group's conservative charged bytes; default zero disables it.
Generic sinks and SyncProcessor retain WriteRecord. The writer only groups
currently available entries, never waits to fill, never crosses a barrier, and
keeps all credits/bytes occupied until output returns. File output chunks have a
separate hard 64 KiB bound; one longer line travels alone. Nothing remains pending
in the sink when a call returns. Details: `docs/file-batching.md`.

Targets **3072 / 12288 / 49152** charged bytes were chosen to represent about
12 / 48 / 192 minimum-charge records, or fewer attribute-heavy ones. Actual
unbatched averages are approximately 234 / 217 / 308 encoded bytes per record
for StartEnd / AddEvent / SetAttributes. Escaping and field overhead can make
encoded and charged sizes differ; both limits are explicit, not a record-count
only batching policy. No target is enabled by default.

### Ten-sample medians by target

| Charged byte target | Operation | End-to-end ns/op | Drain ns/op | Mean Flush/window, ns | B/op | allocs/op |
| ---: | --- | ---: | ---: | ---: | ---: | ---: |
| 0 (reuse control) | StartEnd | 10556.5 | 9381 | 2399280.5 | 529 | 8 |
| 0 | AddEvent | 3273 | 3164.5 | 809429.5 | 129 | 2 |
| 0 | SetAttributes | 3510 | 3108 | 794901 | 321 | 5 |
| 3072 | StartEnd | 2415 | 1612 | 412384.5 | 530 | 8 |
| 3072 | AddEvent | 855.85 | 712.35 | 182185.5 | 129 | 2 |
| 3072 | SetAttributes | 1399 | 811.55 | 207562 | 321 | 5 |
| 12288 | StartEnd | 1334 | 525.55 | 134411 | 530 | 8 |
| 12288 | AddEvent | 426 | 302.6 | 77395.5 | 129 | 2 |
| 12288 | SetAttributes | 691.75 | 337.4 | 86300 | 321 | 5 |
| 49152 | StartEnd | 1026 | 189.7 | 48517.5 | 530 | 8 |
| 49152 | AddEvent | 328 | 183.5 | 46927 | 129 | 2 |
| 49152 | SetAttributes | 529.55 | 189.4 | 48438 | 321 | 5 |

Every target's total/drain/Flush gain over reuse is significant (p<0.001), not
merely a producer timing improvement. For 49152, total -90.28% / -89.98% /
-84.91%; drain -97.98% / -94.20% / -93.91%. Operation throughput from total
medians: **0.0947 → 0.9747**, **0.3055 → 3.0488**, **0.2849 → 1.8884** million
operations/s (StartEnd comprises three records). These are checkpointed workloads,
not a guarantee of disk bandwidth, durability or sustained multi-producer rates.
The extra 1 B/op in StartEnd amortizes bounded batch backing over the workload;
there is no new per-record allocation. Full-rate profiles confirm no revived
encoder-growth sites; optional batch backing is allocated once per sink.

### Actual output-call observations

`TestObservedAsyncFileWrites` wraps the actual os.File and counts Write invocations
after exclusive-open/header creation, with the same queue/window/workloads.
Five separate runs per target/operation, 16384 operations each, include setup,
summaries and shutdown; the count denominator is actual emitted journal lines
excluding the header. These are **measured os.File.Write calls**, not claimed
counts of internal kernel retries. Values below are medians from the raw
`batch-write-counts.txt`; scheduling changes available grouping.

| Target | Operation | writes/record | bytes/write | bytes/record |
| ---: | --- | ---: | ---: | ---: |
| 0 | StartEnd | 1 | 234.06 | 234.06 |
| 0 | AddEvent | 1 | 217.38 | 217.38 |
| 0 | SetAttributes | 1 | 307.98 | 307.98 |
| 3072 | StartEnd | 0.092446 | 2526.04 | 233.49 |
| 3072 | AddEvent | 0.097368 | 2225.87 | 216.77 |
| 3072 | SetAttributes | 0.206285 | 1491.46 | 307.67 |
| 12288 | StartEnd | 0.025539 | 9135.30 | 233.26 |
| 12288 | AddEvent | 0.027594 | 7833.53 | 216.42 |
| 12288 | SetAttributes | 0.051115 | 6009.93 | 307.20 |
| 49152 | StartEnd | 0.020582 | 11329.53 | 233.18 |
| 49152 | AddEvent | 0.015803 | 13701.63 | 216.52 |
| 49152 | SetAttributes | 0.035130 | 8744.53 | 307.11 |

49 KiB target reduces writes/record **97.94% / 98.42% / 96.49%** versus the
unbatched path. Encoded-byte averages vary slightly because actual seq/wall/
elapsed/duration numeric widths vary; format equivalence uses identical records
in correctness tests rather than different-time benchmark streams.

### Failure/latency decisions

Keep batching **explicitly opt-in**. The largest tested target wins these no-loss
window workloads and improves measured Flush latency, but does not establish
tail latency under sustained producer contention. A failed batch has uncertain
prefix delivery; there is deliberately no per-record success/retry API. A write
error or zero progress stops all later output, valid prefix lines survive a torn
tail, and a canceled checkpoint does not release in-flight lifecycle credits.
Positive short/no-error writes preserve the old suffix-progress loop. No timers,
pending-across-call bytes, retries after error or extra output workers are added.

Batching retains an additional **64 KiB** byte buffer per file sink and at most
**4096 bytes** of Record-interface grouping storage per processor at the maximum
allowed target (256 entries * 16 bytes on amd64). Credit/payload budgets remain
unchanged and include all in-flight group records. Generic fallback/default
allocations remain identical. A later disabled-default run has StartEnd/AddEvent
total changes not significant, and SetAttributes **+3.59% total** (p=0.004), while
its drain **-4.01%** (p=0.023). Save this mixed environmental/layout result rather
than assert the default path always gets faster. Producer resolution/ownership
logic is untouched.

Reproduction (run serially; environment variable is benchmark-only):

```powershell
./benchmarks/file-drain/capture-stage.ps1 -Stage batch3072 -BatchBytes 3072 -Local '<profile-dir>' -Benchstat '<profile-dir>/benchstat.exe'
./benchmarks/file-drain/capture-stage.ps1 -Stage batch12288 -BatchBytes 12288 -Local '<profile-dir>' -Benchstat '<profile-dir>/benchstat.exe'
./benchmarks/file-drain/capture-stage.ps1 -Stage batch49152 -BatchBytes 49152 -Local '<profile-dir>' -Benchstat '<profile-dir>/benchstat.exe'
mise exec -- go test ./file -run '^TestObservedAsyncFileWrites$' -count=5 -v
./benchmarks/file-drain/capture-shutdown.ps1 -Benchstat '<profile-dir>/benchstat.exe'
./benchmarks/file-drain/capture-long-cpu.ps1 -Local '<profile-dir>'
```

`capture-long-cpu.ps1` uses saved stage binaries at one million operations per
case, giving better attribution than the short 100000-operation batch profiles.
Shutdown's dedicated repeated benchmark holds the initial capture behind a gate,
pre-admits exactly 256 events + root lifecycle records, then times release/drain/
final-summary/close. Setup/production/open are outside its timer. Ten repetitions
of 256 fresh pipelines; unlike one-off final cleanup, this provides nonzero mean
latencies. It compares disabled batching and targets in the **same implementation**,
not an extra original nil-encoder Shutdown control.

| Target | Pending-work Shutdown ns/call | Change vs disabled | p | B/call | allocs/call |
| ---: | ---: | ---: | --- | ---: | ---: |
| 0 | 1131534 | control | — | 1370 | 4 |
| 3072 | 471519 | -58.33% | 0.002 | 66952 | 5 |
| 12288 | 379395.5 | -66.47% | <0.001 | 66955 | 5 |
| 49152 | 371044 | -67.21% | 0.002 | 66963.5 | 5 |

Fresh-sink shutdown **does allocate 64 KiB once** for batch output inside the
timed interval, rather than amortizing it as the long-run workload does. This is
an explicit memory tradeoff, not a zero-allocation shutdown claim. No shutdown
latency regression was detected versus disabled batching. The 49152 target is
not proved faster than 12288 for Shutdown; their means have overlapping variation.

### Longer CPU attribution

One-million-operation frozen-stage-binary captures (separate from timing) show
Windows `syscall.writeFile` cumulative share drop as follows:

| Operation | Reuse only | Batch 49152 | encodeRecordInto cumulative, reuse → batch |
| --- | ---: | ---: | ---: |
| StartEnd | 86.64% | 23.56% | 0.61% → 9.20% |
| AddEvent | 92.65% | 53.66% | 0.64% → 17.07% |
| SetAttributes | 83.46% | 43.66% | below top cumulative listing → 8.45% |

Mutex/serialization/queue work was not a leading pre-reuse CPU hotspot, and
allocation profiles attributed large growth costs to append/ID/number paths.
After batching, producer/encoding/GC/locking work naturally accounts for more
of a much shorter run. `runtime.cgocall` also includes non-write OS calls such
as random-ID generation; its total is **not** substituted for writeFile counts.
The batch captures still contain only 41 / 71 / 174 sampled 10ms CPU units, so
shares are approximate, not enough to attribute remaining costs to Defender,
disk, kernel or scheduler individually. All flat/cumulative text reports and
raw profile runs are saved; binary profiles remain external.

### Final correctness verification

Focused file/batch/async/conformance tests repeated under race, plus fmt/full
check/full race, cover exact output/FIFO, exact 64 KiB boundary, oversized line,
long-plus-small groups, standalone concurrent WriteRecord/WriteRecords/Flush,
validation-before-output, short/zero/partial/immediate failures, terminal failure
latching/no subsequent output, cancellation after a checkpoint is queued while
a batch is stalled, resumable Shutdown, occupied in-flight credits, drop/loss
accounting, generic fallback and shared batch conformance. A gated batch failure
also verifies Flush/Shutdown propagate the original error and no later calls
extend the stream. Torn-prefix tests preserve complete prior JSONL lines.

Both experiments are kept as separate signed commits. No encoder schema,
producer ownership/resolution, queue limits, security configuration or unrelated
tooling changes. Further default enablement or tail-latency guarantees need
real workload/concurrent saturation evidence; this pass stops here.
