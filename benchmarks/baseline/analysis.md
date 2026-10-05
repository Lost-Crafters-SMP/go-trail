# Allocation attribution and optimization shortlist

This is analysis of the saved revision, not an optimization proposal accepted for
implementation. Full-rate focused memory profiles include setup, calibration,
checkpoints, and teardown even when benchmark timers exclude them. Use the
operation-sized line counts, not total profile bytes divided by benchmark N.
Timing runs use the default sampling rate; diagnostic runs use rate 1.

## Verified hot-path allocations

The memory profiles ran 100000 iterations plus the benchmark's one-iteration
calibration. Each listed per-operation non-tiny site has **100001** flat
allocations. Saved `*-alloc_objects.txt`, `*-alloc_space.txt`, annotated
`*-lines.txt`, and `escape-hot-path.txt` independently locate these sites.
Byte sizes below are amd64 allocation-class sizes confirmed by alloc_space,
not portable struct-size promises.

### Start/End

| Source | Sync count / bytes per operation | Async difference | Classification |
| --- | ---: | --- | --- |
| provider.go:310 `startConfig` | 1 / 32 | Same | Option configuration, even with no options |
| provider.go:396 SpanStart interface box | 1 / 112 | Same | Record/interface escape |
| provider.go:404 `traceState` | 1 / 48 | Same | Root trace lifecycle |
| provider.go:410 `spanState` | 1 / 128 | Same | Span lifecycle |
| context.WithValue via context.go:15 | 1 / 48 | Same | Propagation context lifecycle |
| span.go:135 SpanEnd interface box | 1 / 112 | Same | Record/interface escape |
| span.go:157 TraceEnd interface box | 1 / 64 | Same | Record/interface escape |
| async_processor.go:489 Name/Scope cloning | 0 | 2 small string allocations / 16 combined bytes | Ownership/backing isolation |
| async_processor.go:490 returned SpanStart box | 0 | 1 / 112 | Cloned Record/interface escape |

Sync totals **7 / 544 B**; async totals **10 / 672 B**. The extra three are
**Name clone + Scope clone + one new SpanStart interface box**, not one clone
for each of SpanStart/SpanEnd/TraceEnd. That lifecycle-three-box hypothesis is
**disproved**: End and TraceEnd go through cloneAsyncRecord's default branch,
forwarding their original interfaces. Their allocation stacks are unchanged.
Reservation maps/ring are setup/amortized costs, not three allocations per
Start/End. `startConfig` escapes through the opaque option-function call, even
though this benchmark's option slice is empty.

### AddEvent

Sync's **2 / 144 B** come from `span.go:74` option configuration (**32 B**) and
`span.go:235` original Event interface box (**112 B**). Async's **4 / 272 B** add
the event-name clone at `async_processor.go:500` (**16 B** allocation class)
and returned Event box at line 502 (**112 B**).

There is **no attribute/event backing-slice heap allocation in this workload**:
the event has zero attributes. The zero-capacity batch at attributes.go:262 does
not allocate storage; the dedupe map at line 263 does not escape and produces no
profiled heap allocation here. cloneAsyncAttributes returns nil for empty input.
With attributes, the resolver creates a batch; nested `Strings` adds the
boundAttribute copy and async adds its own copies. Those paths are not exercised
by this benchmark and are not claimed as measured costs.

### SetAttributes

Sync's **2 / 304 B** are the two-element resolution batch at
`attributes.go:226` (**192 B**) and original SpanUpdate box at `span.go:192`
(**112 B**). Async's **7 / 624 B** add a second attribute batch at
`async_processor.go:470` (**192 B**), two key-string clones at line 472 and the
`"fast"` value clone at line 473 (**16 combined B**), plus the returned
SpanUpdate box at line 498 (**112 B**). No status pointer is present in this
workload; its copy is an additional allocation only when supplied.

The two-entry index map at `attributes.go:227` **does not escape and has no
heap allocation in this profile**. The persistent span key map *does* escape
at line 245: annotated profiles show two creations across calibration and main
run, plus initial backing storage at key insertion, not a new map per update.
Repeated `mode`/`count` updates do not introduce new keys. Larger dedupe maps
can still allocate backing tables despite the map header not escaping; these
profiles do not establish a large-batch baseline. Caller variadic backing storage
is not a hot heap-allocation site in this scalar workload.

### Tiny allocator: why pprof object counts differ

At full rate the async StartEnd profile shows roughly **900000** hot heap objects,
not the benchmark's **1000000** allocation calls. SetAttributes similarly shows
roughly **500000**, not **700000**. This is **not missing End/TraceEnd copying**:
multiple tiny no-pointer string allocations fit into one 16-byte tiny block.
The heap profile records blocks; MemStats.Mallocs (used by benchmark allocation
counts) additionally counts tiny suballocations. Confirmed against pinned Go's
`runtime/malloc.go:1271–1278` (existing-block early return) and
`runtime/mstats.go:424–431` (tiny suballocation count addition). Compiler
diagnostics show both SpanStart clone byte allocations escaping at line 489 and
the key/value clone byte allocations at lines 472–473. Do not equate pprof
alloc_objects totals directly with benchmark allocs/op for these tiny strings.

### File encoding and CPU observations

Sync file StartEnd's profile attributes about **1.30 million allocation objects /
186.16 MiB** to Sink.WriteRecord/encoding over 100001 StartEnd operations, on top
of the core allocations. This agrees with approximately **13 extra allocations /
1953 B** per operation (three records), with small variations from sequence
decimal widths. Major flat-byte sites are appendIDString growth (**73.24 MiB**),
SpanEnd counter-field append at encode.go:124 (**48.78 MiB**), and internal
strconv append growth (**46.59 MiB**). These are growing output-buffer allocations,
not evidence that each ID/number conversion intrinsically allocates a string.
The async file profile includes the same encoder work on the writer plus
checkpoint encoding, whereas its producer-only B/op does not include all of it.

Separate file CPU profiles put **80–85% flat** at Windows `runtime.cgocall`, with
writeFile/FD.execIO stacks accounting for roughly **79–85% cumulative**. This is
the Windows syscall path, not a new cgo library dependency. It supports exploring
fewer output calls, but does not isolate Defender, disk, kernel or lock contention
as the cause. CPU totals can exceed wall duration with multiple profiled threads;
these reports are diagnostic, not a substitute for throughput measurements.

## Escape/inlining interpretation

- startConfig, span/trace state and provider-to-Processor SpanStart/SpanEnd/
  TraceEnd/Event/SpanUpdate boxes escape as seen in profiles.
- Async Process leaks its record into retained storage; modified clone branches'
  concrete `r` values escape at return (490/498/502). Attribute and nested string
  slices, status pointee and cloned string bytes escape; value-only default
  records reuse the original interface.
- Resolution batches/persistent key maps escape; small index map headers do not.
- Public End/SetAttributes wrappers inline, but hot state methods/Process do not
  inline because of defer; resolvers and clone helpers exceed the cost budget.
  This is a diagnostic observation, not authorization to remove defer/locking.
- Escape logs are conservative: an escaped zero-sized spanKey does not imply an
  extra malloc; only context.WithValue is a per-operation context heap site here.
  Likewise zero-length slices and empty strings do not allocate backing data.
  Error-path escapes in the report are not successful-operation allocations.

## Ownership review: `cloneAsyncRecord`

All records carry value IDs, sequence/counters, durations and `time.Time`.
The latter refers to a shared immutable `time.Location`, not caller-mutable
payload. These fields need no deep copy.

| Record | Reference-bearing payload | Required isolation |
| --- | --- | --- |
| CaptureStart, SpanEnd, TraceEnd, LossSummary | Only time location | Retain existing concrete value/interface; no deep copy |
| SpanStart | Name, Scope strings | Strings immutable; cloned backing bytes currently guarantee bounded retention |
| Event | Name, Attributes | Copy attribute backing slice; copy nested string-array backing slices |
| SpanUpdate | Attributes, optional *SpanStatus | Copy attribute slice, nested string-array slices, and pointed-to status value |

Attributes' keys/scalar string values/string-array elements and status descriptions
are immutable strings; numeric/bool fields are values. A string-array's elements
can be replaced through a shared mutable backing slice, so the slice must be
copied even though its strings cannot be mutated through safe Go.
`boundAttribute` already creates a bounded nested string slice at resolution;
async ownership creates a second nested slice. The current AddEvent workload
has no attributes, and SetAttributes uses only scalar String/Int, so these
nested-slice paths are inspected source behavior, not measured profile costs.

Cloning every string is unnecessary **for mutation isolation alone**, including
literal names/keys in these benchmarks. However, retaining a short substring can
pin arbitrarily large backing storage. The documented bounded-owned-bytes
contract means removing all `strings.Clone` calls is not a safe mechanical win.
Any future producer-owned fast path must preserve bounded retention, public
Process ownership, and charge accounting. No unsafe-string mutation is promised.

The async queue holds `Record` interfaces. Boxing the changed concrete SpanStart,
Event, and SpanUpdate at clone return escapes; the value-only default branch
returns the original interface and does **not** box End/TraceEnd again. Interface
use does not universally require heap allocation: these particular dynamic
Processor calls and queue retention do; devirtualized/nonretained calls can differ.

## Candidate ranking

### Producer latency / allocation

1. **Avoid redundant ownership work via an internal owned-record path** (high
   value, medium/high risk). Resolution already makes a fresh attribute batch;
   async copies it again. Transfer only proven-owned, bounded payloads internally;
   keep public Process defensive. Race/ownership and queue-rejection tests required.
2. **No-option configuration fast path** (medium value, low/medium risk).
   Profiles confirm Start/AddEvent's configuration allocation even with no
   options. An empty-option path could avoid this one allocation without
   changing supplied-option semantics; preserve no-op behavior and normal timing.
3. **Reduce queue Record/interface reboxing** (medium/high value, medium/high
   risk). A tagged concrete queue representation or internal typed handoff could
   avoid cloned record boxes, but increases entry size and maintenance cost.
   It cannot automatically eliminate the original provider-to-Processor box.
4. **Small-batch linear dedupe instead of a map** (conditional value, low/medium
   risk). The measured two-attribute index map is already non-heap; expected
   wins here are CPU work, not removing a per-operation map allocation. Larger
   batches need separate evidence. Preserve last-wins, sorting, persistent
   key limits and bounded worst-case work; use map fallback for larger batches.
5. **Avoid unnecessary cloneAsyncRecord deep copies** (conditional value,
   medium/high risk). Value-only records already avoid copies. Strings are
   immutable but cloning enforces bounded backing ownership; remove only with
   proof of independently owned bounded backing. Status/nested mutable slices
   still need copying. Do not blanket-delete string copies.

### Drain / file throughput

1. **Reuse/pre-size encoder buffers** (high allocation value, medium risk).
   encodeRecord starts from nil for every record, repeatedly growing append
   storage. A sink-owned bounded buffer could reduce this; preserve standalone
   Sink concurrency, validation-before-write and oversized-record limits. Current
   encoding happens before its mutex, so reuse needs deliberate serialization.
2. **Batch writer-side output** (potentially high syscall value, high contract
   risk). File writes are one record at a time. Batching must preserve FIFO,
   Flush high-water marks, reservations through consumption, bounded bytes,
   torn-tail/first-failure behavior, and crash visibility. Generic Sink has only
   WriteRecord, so benefits require an explicit compatible sink capability, not
   just draining several queue entries in a loop.

No evidence here warrants lock-free queues. Discard-sink async cost includes
ownership, admission/reservation work and mutex scheduling; CPU/concurrency
profiles would be needed to isolate lock contention before redesigning locks.
