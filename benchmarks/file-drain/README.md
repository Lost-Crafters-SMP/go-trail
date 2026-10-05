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
| StartEnd | 11448.5 → 10556 | 10231.5 → 9381 | 2486 → 529 | 21 → 8 |
| AddEvent | 3804 → 3273 | 3602 → 3164.5 | 877 → 129 | 7 → 2 |
| SetAttributes | 4261.5 → 3510 | 3671.5 → 3108 | 1317 → 321 | 10 → 5 |

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
