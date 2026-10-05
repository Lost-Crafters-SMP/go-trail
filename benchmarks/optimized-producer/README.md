# Optimized producer evidence

Control: baseline artifact commit `269fddf`, measured source
`3e9e4cf3436a84c16d13b4e477422bfd9f9edded`. Baseline artifacts are untouched.
Go 1.27.1 via mise; Windows/amd64 build 26200; Ryzen 5800X3D; default
GOMAXPROCS=16. Async benchmark: 2048 records / 4 MiB, no-loss windows of 256.
Sink implementations, encoder, queue size, bounds and charging are unchanged.

## Decisions

| Candidate | Outcome | Evidence |
| --- | --- | --- |
| A: no-option config | Kept | -32 B / one allocation for Start/AddEvent; initial three latency wins; sync AddEvent initially not significant |
| B: resolved slice transfer | Kept | Async SetAttributes -192 B / one allocation; contemporary control -8.49% ns (p=0.001); no significant sync regression |
| C: typed record handoff | Kept | -112 B / one allocation for all three async cases; paired AddEvent -11.03%, attributes -6.52% (p<0.001); no significant StartEnd or sync regression |
| Tagged queue alternative | Not implemented | Larger entries and generic Sink still requires Record delivery; typed handoff avoids this complexity |
| D: selective string copies | Evaluated, deferred | Fresh slices do not prove string backing ownership; provenance/caching needs separate evidence |

B/C were temporarily removed for contemporary control captures, then restored.
No dedupe rewrite, lock-free queue, file buffer reuse or batching was attempted.

## Final ten-sample medians

Positive ns delta is slower. Bytes and allocations are per timed operation;
async ns/op excludes drain waits. File async bytes exclude some writer work.

| Sink / processor / operation | Before → after ns/op | Delta | p | Before → after B/op | Before → after allocs/op |
| --- | ---: | ---: | --- | ---: | ---: |
| Memory Sync StartEnd | 497.65 → 517.30 | +3.95% | <0.001 | 544 → 512 | 7 → 6 |
| Memory Async StartEnd | 1040 → 1387 | +33.37% | 0.002 | 672 → 528 | 10 → 8 |
| Memory Sync AddEvent | 90.865 → 80.80 | -11.08% | <0.001 | 144 → 112 | 2 → 1 |
| Memory Async AddEvent | 213.15 → 247.80 | +16.26% | <0.001 | 272 → 128 | 4 → 2 |
| Memory Sync SetAttributes | 230 → 235.95 | +2.59% | 0.160 | 304 → 304 | 2 → 2 |
| Memory Async SetAttributes | 469.10 → 591.65 | +26.12% | <0.001 | 624 → 320 | 7 → 5 |
| File Sync StartEnd | 10716.50 → 10633 | -0.78% | 0.165 | 2497 → 2465 | 20 → 19 |
| File Async StartEnd | 902 → 826.65 | -8.35% | <0.001 | 796 → 641 | 10 → 8 |
| File Sync AddEvent | 3886 → 3443 | -11.40% | <0.001 | 888 → 856 | 7 → 6 |
| File Async AddEvent | 226.40 → 138.10 | -39.00% | <0.001 | 295 → 141 | 4 → 2 |
| File Sync SetAttributes | 4419.50 → 3994.50 | -9.62% | <0.001 | 1296 → 1296 | 7 → 7 |
| File Async SetAttributes | 669.60 → 337.80 | -49.55% | <0.001 | 714.5 → 383 | 7 → 5 |

**Mixed latency result, not a universal speedup.** Historical memory comparisons
have significant regressions. Contemporary controls were also slower than the
historical capture and support B/C's stepwise gains, but do not isolate the cause
of the historical shift. Defender was observed in the baseline; no security,
power-plan, affinity or GOMAXPROCS settings were changed. Keep for deterministic
allocation/byte savings plus measured stepwise/file gains; obtain a controlled
latency study before making broader speed claims. All samples are retained.

## Attribution and safety

Profiles confirm config allocation disappears on empty-option paths; optional
configuration still escapes when supplied. Async now boxes an owned concrete
record once at enqueue, not once before and again after cloning. Temporary size/
admission interface views do not escape. SetAttributes retains its fresh 192 B
batch, 112 B box and 16 combined bytes of tiny strings; the second batch is gone.
All string clones remain; direct public Process still copies defensively.
No locks/defers removed and ring entries remain unchanged. Transfers require
tight backing capacity, with compaction otherwise; nested mutation and 8 MiB
substring tests verify ownership. See [full attribution and stage analysis](analysis.md).

## Reproduce

```powershell
mise exec -- go test . -run '^$' -bench '^Benchmark(AsyncEnabled|Enabled)/memory/(StartEnd|AddEvent|SetAttributes)$' -benchmem -benchtime=1s -count=10
mise exec -- go test . -run '^$' -bench '^Benchmark(AsyncEnabled|Enabled)/file/(StartEnd|AddEvent|SetAttributes)$' -benchmem -benchtime=1s -count=10
```

`capture-final.ps1` saves `core.txt`, `file.txt` and benchstat comparisons against
baseline. Benchstat pin/install command, individual stage commands/outputs,
drain medians, confidence/significance and compiler findings are in `analysis.md`.
`capture-profiles.ps1` saves six focused full-rate text profile reports;
`capture-escapes.ps1` saves filtered diagnostics. Binaries/full compiler logs stay
in the approved local temp directory, outside Git. Run capture and checks serially.
Raw run files intentionally retain Go's CPU-header trailing whitespace.

Verification: repeated focused race/conformance/option/ownership tests, plus
`mise run fmt`, `mise run check` and `mise run test:race` after each kept stage.
Existing queue/reservation/control/barrier/cancellation/loss/cleanup cases pass.
Further producer allocation work may be worthwhile, but should follow controlled
latency/concurrency profiles, not speculative dedupe, pooling or lock-free work.

Saved profile paths are normalized; measurement values and line numbers remain
unchanged. Profile/escape scripts require `-Local` pointing to an external
directory. Final capture defaults to benchstat on PATH, or accepts `-Benchstat`
with its executable location. Replace documented placeholders before running.
