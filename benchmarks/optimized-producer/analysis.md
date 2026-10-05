# Producer optimization analysis and stage evidence

Control: committed `benchmarks/baseline/core.txt` and `file.txt`, artifact commit
`269fddf`; measured source `3e9e4cf3436a84c16d13b4e477422bfd9f9edded`.
Baseline files are not modified. Environment remains Go 1.27.1, Windows/amd64,
Ryzen 5800X3D, default GOMAXPROCS=16. Async queue remains 2048 records / 4 MiB,
256-operation no-loss windows. File encoding/batching are outside this pass.

Benchstat is built outside the repository with mise's Go:

```powershell
# Replace <profile-dir> with an external directory of your choice.
New-Item -ItemType Directory -Force '<profile-dir>' | Out-Null
$env:GOBIN=(Resolve-Path '<profile-dir>').Path
mise exec -- go install golang.org/x/perf/cmd/benchstat@v0.0.0-20260929162123-406019bb8b68
```

Timing, profiles, builds and correctness/race runs are serialized. Candidate
outputs are raw and preserve Go's CPU-header trailing whitespace. Full-rate
profiles are diagnostics, not timing evidence. Benchstat's significance applies
to measured samples, not a guarantee against machine-state/Defender variation
between control and candidate capture. File async allocations include only
concurrent writer work within timed producer windows, not all encoder allocations.

## Candidate A: no-option configuration

Move opaque option invocation into `resolveStartOptions`; only call it for
nonempty option slices. Start and AddEvent still use one shared body, retain
nil-option skipping and execute supplied options in order. No locking changes.

```powershell
mise exec -- go test . -run '^$' -bench '^Benchmark(AsyncEnabled|Enabled)/memory/(StartEnd|AddEvent)$' -benchmem -benchtime=1s -count=10
```

Output: `candidate-a.txt`; control comparison: `candidate-a-benchstat.txt`.

A removes **32 B and one allocation** in all four workloads. Versus baseline,
async StartEnd 1040 → 985.6 ns (-5.23%, p<0.001), async AddEvent 213.15 → 189.15
ns (-11.26%, p<0.001), sync StartEnd 497.65 → 472.6 ns (-5.03%, p<0.001).
Sync AddEvent's first run is not significant (90.865 → 91.785 ns, p=0.684).
A second isolated run (`candidate-a-confirm.txt`) also is not significant
(91.405 ns, p=0.985). Keep for verified allocation savings and the other three
latency wins; do not claim a sync AddEvent speedup from A alone.

Compiler: the helper inlines (cost 77); the cfg escape remains **inside the
nonempty-options branch**, not on the empty path. State locking/defers unchanged.
Option order/nil/empty options checked with both processors; fmt/check/race pass.

## Candidate B: resolved attribute ownership

Prototype transfers freshly resolved producer slices through a private async
path. Public Process remains defensive. Both scalar strings and nested elements
still clone backing bytes. Only exact-capacity slices transfer; deduplicated/
dropped batches compact rather than retain spare uncharged backing storage.
The byte formula, reservation accounting and budget check before copying stay
unchanged. Tests cover nested caller mutation, initial attributes/updates/events,
8 MiB substring backing separation, and post-dedupe capacity compaction.

```powershell
mise exec -- go test . -run '^$' -bench '^Benchmark(AsyncEnabled|Enabled)/memory/SetAttributes$' -benchmem -benchtime=1s -count=10
```

`candidate-b.txt` shows **624 → 432 B**, **7 → 6 allocations** for async; sync
remains **304 B / 2 allocations**. Initial latencies slowed in both processors,
with an abrupt async timing shift after the first sample. Save the result rather
than discard it; temporarily restore A-only production code and capture
`candidate-b-control-a.txt` to distinguish overhead from environmental drift.

The contemporary A-only control is also slower than the historical control:
async 698.15 ns, sync 279.85 ns. B is **638.85 ns (-8.49%, p=0.001)** versus
that control; sync **276.9 ns (p=0.436)**, no significant regression. Keep B.
Historical-control comparison is retained, including the regression: changing
machine conditions cannot be made to disappear by selecting favorable samples.
Full checks/race pass. B's escape report still has original + cloned Record boxes;
the resolution batch now supplies ownership, and strings remain cloned.

## Candidate C: typed handoff, unchanged FIFO representation

Reject enlarging the ring to a tagged union of record payloads: it multiplies
entry size and still needs an interface for generic Sink delivery. Instead
prototype three small typed provider handoffs and one generic `processFresh`.
Shared `admitLocked` keeps lifecycle/byte/record decisions identical. It checks
before payload preparation; the mutex stays held until FIFO insertion. Concrete
payloads become owned before the one queued Record conversion. Public Process
continues cloning borrowed data. No queue entry growth, sink/API changes, unsafe
interface mutation, per-span workers, capacity waits or lock/defer removals.

The prototype runs the complete matching memory command, count=10, as
`candidate-c.txt`; compare with the historical baseline and candidate B.

Keep C. A complete B-only rerun (`candidate-c-control-b.txt`) confirms, versus C:
async StartEnd **1433 → 1430.5 ns**, no significant change (p=0.739);
AddEvent **288.65 → 256.8 ns**, -11.03% (p<0.001);
SetAttributes **638.7 → 597.05 ns**, -6.52% (p<0.001).
No sync regressions against that control: StartEnd p=0.218, SetAttributes p=0.247;
sync AddEvent is faster (p<0.001), but its code change does not remove another
allocation and environment/layout effects cannot be separated by that result.
All three async operations remove **112 B / one allocation** relative to B.
Earlier historical-control latency regressions remain saved transparently.

Compiler confirms both temporary record-interface conversions (size/admission)
**do not escape**, while only `own(record)` at FIFO insertion escapes. The
generic implementation retains its defer/mutex; thin concrete instantiation
wrappers inline. Public Process retains its defensive clone path. fmt/check/race
pass, including a test proving full-queue rejection never invokes ownership work.

## Candidate D and excluded work

Selective string-copy reduction is evaluated but **not implemented**. Fresh
resolution slices do not prove their short scalar strings have independent
backing. Truncated strings may already be owned, but distinguishing them needs
extra per-field provenance and a separate truncated-payload benchmark; common
baseline payloads are borrowed. All required string clones remain. Explicit
8 MiB substring tests ensure internal records do not pin caller backing.
No dedupe rewrite, lock-free structure, file encoder reuse or output batching.
Owning scope once at Tracer construction was also considered, but would shift
allocation to enabled handle/global lookup and needs separate usage-pattern
benchmarks. It is not silently added as an unmeasured optimization.

## Final reproduction

```powershell
./benchmarks/optimized-producer/capture-profiles.ps1 -Local '<profile-dir>'
./benchmarks/optimized-producer/capture-escapes.ps1 -Candidate final -Local '<profile-dir>'
./benchmarks/optimized-producer/capture-final.ps1 -Benchstat '<profile-dir>/benchstat.exe'
```

The final script runs the **same** memory/file expressions, `-benchmem
-benchtime=1s -count=10`, as the baseline. It saves `core.txt`, `file.txt` and
their `*-benchstat.txt` comparisons without modifying baseline artifacts.
Profile binaries/full compiler logs stay outside Git in the approved local temp
directory; readable text reports and exact commands are committed here.

## Final comparison with committed control

Medians of ten samples. `p<0.001` means benchstat prints rounded `p=0.000`.
Positive ns delta is slower. Allocation/byte changes are significant (p<0.001)
where nonzero; unchanged counts are identical (p=1). No samples were discarded.

| Memory operation | Before ns/op | After ns/op | Delta / p | Before → after B/op | Before → after allocs/op |
| --- | ---: | ---: | --- | ---: | ---: |
| Sync StartEnd | 497.65 | 517.30 | +3.95% / <0.001 | 544 → 512 | 7 → 6 |
| Async StartEnd | 1040.00 | 1387.00 | +33.37% / 0.002 | 672 → 528 | 10 → 8 |
| Sync AddEvent | 90.865 | 80.80 | -11.08% / <0.001 | 144 → 112 | 2 → 1 |
| Async AddEvent | 213.15 | 247.80 | +16.26% / <0.001 | 272 → 128 | 4 → 2 |
| Sync SetAttributes | 230.00 | 235.95 | +2.59% / 0.160 (not significant) | 304 → 304 | 2 → 2 |
| Async SetAttributes | 469.10 | 591.65 | +26.12% / <0.001 | 624 → 320 | 7 → 5 |

| File operation | Before ns/op | After ns/op | Delta / p | Before → after B/op | Before → after allocs/op |
| --- | ---: | ---: | --- | ---: | ---: |
| Sync StartEnd | 10716.50 | 10633.00 | -0.78% / 0.165 (not significant) | 2497 → 2465 | 20 → 19 |
| Async StartEnd | 902.00 | 826.65 | -8.35% / <0.001 | 796 → 641 | 10 → 8 |
| Sync AddEvent | 3886.00 | 3443.00 | -11.40% / <0.001 | 888 → 856 | 7 → 6 |
| Async AddEvent | 226.40 | 138.10 | -39.00% / <0.001 | 295 → 141 | 4 → 2 |
| Sync SetAttributes | 4419.50 | 3994.50 | -9.62% / <0.001 | 1296 → 1296 | 7 → 7 |
| Async SetAttributes | 669.60 | 337.80 | -49.55% / <0.001 | 714.5 → 383 | 7 → 5 |

Async median drain ns/op: memory **14.275 → 18.715**, **12.34 → 16.89**,
**12.73 → 18.095**; file **9752 → 9579.5**, **3535 → 3399.5**,
**3711 → 3527**, in StartEnd/AddEvent/SetAttributes order. File capture took
**874.092s wall-clock**, because its 1s timer excludes async drain waits.

**Do not claim a universal latency improvement.** The final historical memory
comparison has significant regressions, including async and sync StartEnd.
Contemporary A/B controls were also slower than the historical capture; allocation
savings are deterministic, B/C's paired latency gains are supported, and file
producer gains are measured, but the cause of the historical timing shift is
not isolated. Defender activity was observed during the baseline; no security,
affinity, power-plan or GOMAXPROCS settings were changed here. Benchstat cannot
remove environmental/scheduling/layout confounding. Keep A/B/C for the verified
resource reductions and stepwise gains, with this limitation visible. A controlled
latency study is the next step before promising speed improvements across sinks.

Correctness: focused async/conformance/option tests ran repeatedly under race;
full `mise run fmt`, `mise run check`, and `mise run test:race` passed after
each kept stage. New tests cover caller/nested mutation, large substring backing,
capacity compaction and ownership skipped on rejection. Existing reservations,
bounded control lane, high-water marks, shutdown resume, loss, error reentry,
worker termination and serialized sink cases remain in the suite.

## Updated allocation attribution

Full-rate profiles use 100000 iterations plus the one-iteration calibration;
these are **not** latency measurements. The six `final-*-alloc_objects.txt` and
`final-*-alloc_space.txt` reports confirm the new sites, not just counts inferred
from benchmark output. Tiny string suballocations still share blocks, so heap
object totals differ from benchmark allocation calls as explained in baseline.

| Memory operation | Before B/op / allocs | After B/op / allocs | Remaining allocations |
| --- | ---: | ---: | --- |
| Sync StartEnd | 544 / 7 | 512 / 6 | Span/trace/context state + three Record boxes |
| Async StartEnd | 672 / 10 | 528 / 8 | Same lifecycle/record costs + Name/Scope clones |
| Sync AddEvent | 144 / 2 | 112 / 1 | One Event box |
| Async AddEvent | 272 / 4 | 128 / 2 | One Event box + event-name clone |
| Sync SetAttributes | 304 / 2 | 304 / 2 | Resolution batch + one update box |
| Async SetAttributes | 624 / 7 | 320 / 5 | Resolution batch + one update box + two key/one value clones |

No-option config sites disappear from Start/AddEvent profiles. Async Record
boxing is now at `owned_record.go:19`, once per start/event/attribute update;
End/TraceEnd retain their original single boxes. For SetAttributes, the original
resolution batch is 192 B, queue box 112 B, tiny string backing 16 combined B.
The old extra 192 B batch and 112 B cloned-box sites disappear. This is an
end-to-end allocation removal, not moving boxing to the writer. Mutable nested
slice transfer is source/test verified; scalar benchmarks do not exercise it.

Compiler reports borrowed interface views at processFresh size/admission as
non-escaping; only FIFO insertion escapes. Opaque option cfg still escapes when
options exist. Public Process remains defensive and can still create cloned
boxes for direct callers. Lock/defer structure remains, regardless of inlining.
The small dedupe index map remains non-heap; no map-allocation win is claimed.
