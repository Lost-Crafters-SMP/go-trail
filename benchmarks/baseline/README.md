# Pre-optimization baseline

Measured source revision: **`3e9e4cf3436a84c16d13b4e477422bfd9f9edded`**.
The baseline-artifacts commit is intentionally subsequent to this source revision.
No production code or benchmark workloads changed in this capture.

## Timing summary

Medians of ten samples; allocations are per timed operation. Full variation is
preserved in raw outputs, rather than rounded away in the committed raw data.

| Sink | Operation | Sync ns/op | Sync B/op | Sync allocs/op | Async producer ns/op | Async B/op | Async allocs/op |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| memory | StartEnd | 497.65 | 544 | 7 | 1040.00 | 672 | 10 |
| memory | AddEvent | 90.87 | 144 | 2 | 213.15 | 272 | 4 |
| memory | SetAttributes | 230.00 | 304 | 2 | 469.10 | 624 | 7 |
| file | StartEnd | 10716.50 | 2497 | 20 | 902.00 | 796 | 10 (range 10–11) |
| file | AddEvent | 3886.00 | 888 | 7 | 226.40 | 295 | 4 |
| file | SetAttributes | 4419.50 | 1296 | 7 | 669.60 | 714.5 | 7 |

Async median drain ns/op: memory StartEnd **14.275**, AddEvent **12.34**,
SetAttributes **12.73**; file StartEnd **9752**, AddEvent **3535**, SetAttributes
**3711**. File async B/op varies with writer scheduling: respectively **794–824**,
**290–302**, **695–716**. Median bytes above may be fractional because they are
medians of Go's rounded per-sample outputs, not individual allocation sizes.

File async producer ns/op ranges are **874.8–1475**, **169.5–291.8**, and
**409.3–741.6**. This is substantial within-run variability; do not use the
medians alone to declare a small optimization significant. The user observed
Defender real-time scanning activity during the run. Scanning is a suspected
environmental influence, not measured causal attribution; no exclusions or
protection settings were changed. Future comparisons should keep those settings
and storage location comparable. File capture took **768.355s wall-clock**:
the 1s async producer timer excludes much longer drain waits.

## Findings and candidates

Verified by focused pprof allocation stacks/bytes **and** compiler diagnostics:

- **Sync StartEnd (7 allocations):** option config, span state, trace state,
  propagation context, and original SpanStart/SpanEnd/TraceEnd interface boxes.
- **Async StartEnd (+3):** Name clone, Scope clone, and **one** returned SpanStart
  interface box. The hypothesis that Start/End/TraceEnd each gets a new retained
  clone is **disproved**; the latter two reuse their original interfaces.
- **AddEvent:** Sync config + Event box; async adds name backing + cloned Event
  box. This attribute-free workload has no attribute-slice/map heap allocation.
- **SetAttributes:** Sync resolution batch + SpanUpdate box; async adds a batch
  copy, two key/one value string clones and a cloned SpanUpdate box. Small dedupe
  maps do not escape/allocate here; persistent span keys allocate at initial
  insertion, not on every repeated update.
- **File:** repeated encoder buffer growth dominates added allocation bytes;
  file CPU stacks are dominated by Windows output calls. Async file producer
  allocation counts exclude some writer work, not equivalent total allocation.
- **Surprise:** tiny string suballocations share 16-byte blocks; pprof objects
  need not equal benchmark allocs/op, even with full-rate profiling. Escapes also
  do not imply mallocs for zero-size context keys/empty slices.

Producer candidates, in expected-value order: (1) internal ownership handoff to
avoid duplicate attribute copies (medium/high risk), (2) no-option configuration
fast path (lower risk; 1 allocation opportunity), (3) reducing changed-record
queue reboxing (medium/high risk), (4) small-batch linear dedupe (conditional CPU
win, **not** a measured small-map heap win), (5) removing string clones only
where bounded backing ownership is proved (high semantic risk if generalized).
Drain candidates: bounded encoder-buffer reuse/pre-sizing first (medium risk),
writer-side output batching second (high ordering/crash-visibility/API risk).
No evidence justifies a lock-free redesign. See [detailed attribution, ownership
classification and risk analysis](analysis.md).

## Environment and reproduction

2026-10-05; Go 1.27.1 via mise; Windows/amd64 (OS build 10.0.26200);
AMD Ryzen 7 5800X3D, 16 logical processors. GOMAXPROCS unset/default;
all measured benchmark names have suffix `-16`. See `environment.json`.
Run timing, profiling and checks **serially**, with other heavy workloads idle.
No pinned benchstat dependency is added; raw ten-sample files support later
`benchstat old/core.txt new/core.txt` (and the equivalent file comparison).

Exact timing commands, from repository root:

```powershell
mise exec -- go test . -run '^$' -bench '^Benchmark(AsyncEnabled|Enabled)/memory/(StartEnd|AddEvent|SetAttributes)$' -benchmem -benchtime=1s -count=10
mise exec -- go test . -run '^$' -bench '^Benchmark(AsyncEnabled|Enabled)/file/(StartEnd|AddEvent|SetAttributes)$' -benchmem -benchtime=1s -count=10
```

`capture-timings.ps1` saves these unedited outputs to `core.txt` and `file.txt`.
Raw run files intentionally retain Go's CPU-header trailing spaces; ordinary
`git diff --check` flags those emitted lines. Do not trim/hand-edit raw output.
Both sinks' existing benchmarks live in the **root package**, not `./file`.
Memory sink is discard/no encoding, not a retained in-memory journal. File sink
is real exclusive-create/unbuffered JSONL, default Sync-on-Flush **off**.
Workloads are root `Start("work")`/End in scope `"benchmark"`, attribute-free
`AddEvent("checkpoint")`, and `SetAttributes(String("mode", "fast"),
Int("count", 12))` repeatedly on one live span. They do not measure event
attributes, nested string arrays, status updates or newly introduced span keys
on every operation. File journals use Go's benchmark TempDir (see environment).

Async benchmark queue: **2048 records, 4 MiB charged bytes**, windows at most
256 operations, Provider.Flush between windows outside the producer timer.
Control reserves two credits/512 bytes; ordinary budgets include in-flight
records and promised Ends. Constructor defaults (16384 / 16 MiB) are not used
here. Setup/final Shutdown excluded. Async ns/op measures producer windows;
`drain-ns/op` is additional checkpoint/drain wait per producer operation, not
isolated total sink throughput. B/op/allocs/op are process-wide allocations
during timer windows and can include concurrent writer work. File async values
therefore omit some encoding allocation after StopTimer; do not interpret them
as total end-to-end allocation savings. Full profiles capture that omitted work.

## Saved diagnostics

Saved profile paths are normalized to neutral placeholders; measurement values
and source line numbers are unchanged.

```powershell
./benchmarks/baseline/capture-profiles.ps1 -Local '<profile-dir>'
./benchmarks/baseline/capture-escapes.ps1 -Local '<profile-dir>'
```

Profiles: each sync/async memory StartEnd, AddEvent and SetAttributes separately,
100000 iterations, `-memprofilerate=1`, `-memprofile=<profile-dir>/<case>.pprof`,
`-o <profile-dir>/<case>.test.exe`; otherwise same focused `-run '^$' -bench '^...$'`.
`go tool pprof -top -alloc_objects -nodecount=30 -lines <exe> <profile>` and
`-top -alloc_space` are saved separately, with annotated `-list` hot functions.
Additional sync/async file StartEnd allocation profiles cover encoding. Separate
3s file StartEnd CPU captures use `-cpuprofile` and normal allocation sampling,
not expensive full-rate allocation instrumentation. Exact expanded workload
selection and report commands are in `capture-profiles.ps1`.

Binaries/full escape log stay in
`<profile-dir>`, outside Git; supply an external directory through
the scripts' required `-Local` argument (replace the placeholder before running). Text reports are sufficient for
source attribution; rerun at the source revision for new binary profiles.
Escape command: `mise exec -- go build '-gcflags=go.lostcrafters.com/trail=-m=2' .`.
`escape-hot-path.txt` retains only hot source ranges' inlining, escape and
parameter-leak diagnostics, not the full flow log or dependencies' compiler logs.

Profile-run ns/op are **not** the timing baseline: full-rate profiling perturbs
allocation speed, and profiles include benchmark calibration/setup/checkpoints.
See [allocation attribution and ranked candidates](analysis.md). No optimization
has been implemented; this baseline is the stopping point.
