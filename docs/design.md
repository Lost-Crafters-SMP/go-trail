# Trail: research and design proposal

**Positioning:** Lightweight local-first tracing for Go applications.

**Status:** research/design only, reviewed against the sources below on
2026-10-05. Nothing in the proposed API or file format is implemented. This is a
recommendation for review, not a public API or format specification. No runtime
dependencies are selected or added by this document.

## 1. Recommendation at a glance

- Keep an OTel-familiar span API, but use an explicitly owned provider, local
  context propagation, and one sink. Add an optional no-op-by-default global
  convenience layer over the same provider, not a global SDK or collector.
- Recommend a **versioned Trail JSONL journal**: span starts, updates, events,
  and ends are separate records. This recovers unfinished operations after a
  crash and does not accumulate an entire trace or event history in memory.
- Crash/hang usefulness is a first-class acceptance requirement: capture evidence
  as work happens, not only when spans End. A killed process need not run cleanup
  for its already-written starts/events to be useful.
- Reuse OTel's conceptual model and ID widths. Preserve a documented mapping to
  OTLP, but **do not label the journal OTLP JSON or OTel compliant**. An actual
  OTLP conversion/export mode is deferred.
- Introduce **Tracer -> Processor -> Sink** now. Processor choice is provider
  configuration, not a span semantic. SyncProcessor is the v0.1 implementation;
  AsyncProcessor is a first-class future option with bounded buffering.
- Keep a three-method record sink shared by both processors. The processor owns
  delivery/barriers and its sink; the file sink owns serialization/file cleanup.
  No async queue, batching framework, or rotation implementation yet.
- Support multiple concurrent root traces per provider/file. A root is not a
  process, and ending it does not end its active descendants.
- Make trace completion explicit internally. Reject ended-span parenting by
  starting a new root; this deliberate OTel API difference makes eventual
  segment closure well-defined without inventing a public trace-session API.
- Target zero allocations for the bare disabled Start/End path; measure option
  construction separately. No performance results exist yet.

The major tradeoff is interoperability versus incomplete-span recovery. If
direct OTLP file ingestion is more important than recording unfinished spans,
choose the completed-span OTLP JSONL alternative in section 3 **before** freezing
the journal schema. Do not implement both formats in the first milestone.

## 2. Goals, boundaries, and terminology

The default workflow is:

```text
application -> Trail instrumentation -> local artifact -> later inspection/share
```

Trail records application-defined operations for debugging and performance
analysis. It should work for CLI tools, daemons, desktop programs, test harnesses,
build tools, and agents without a different core API. Applications choose what a
root operation means and when to start new roots. Long-lived operations and
overlapping roots are ordinary cases, not special CLI modes.

Non-goals: OTel implementation, monitoring platform, metrics, log framework,
automatic instrumentation, semantic-convention catalog, remote exporters,
collector integration at runtime, distributed propagation, viewer, rotation,
retention, or compression in v0.1.

| Term | Meaning |
| --- | --- |
| Provider | Owner of configuration, admission state, ID/clock sources, processor, and lifecycle |
| Tracer | Cheap named instrumentation scope obtained from a provider |
| Trace | One root and its locally admitted descendants, sharing a TraceID |
| Span | One named operation with zero/one parent and a start/end lifecycle |
| Span context | Immutable identifiers for correlation; not application data |
| Event | Explicit point-in-time diagnostic within a span |
| Capture | One provider's output lifetime; may contain many traces |
| Processor | Delivery/admission boundary between tracing and a sink; owns processing lifecycle |
| Sink | Recipient of typed records; owns output encoding and cleanup |
| Segment | Future rotation output unit; may contain several traces |
| Sealed | No longer accepts new roots; does **not** mean write-closed |
| Draining | Not current for new roots, but still writable for assigned traces |
| Closed | No more writes possible; only then eligible for retention/compression |

The proposed runtime completion predicate is exactly:

```text
trace_complete = root_has_ended AND live_admitted_span_count == 0
```

Count the root itself while it is live, so zero live spans implies an ended root
in a well-formed execution; retain both conditions explicitly in the contract.
No ended span can admit another child into that trace. Submit trace_end only after
all admitted span-end records have been submitted, under the lifecycle/admission
gate. That is logical completion, not delivery or durability. The processor
delivers the marker after preceding retained records; a failed output does not
become a persisted completion claim. This predicate is identical for both
processors. Root duration
and the lifetime of all trace activity can therefore differ. A file is not
required to contain exactly one trace.

## 3. Format research and decision

### Current standards versus implementations

The OTLP specification inspected is **1.11.0**. It marks traces, metrics, and logs
Stable; profiles are Development [S1]. The protobuf `TracesData` message explicitly
supports persistent storage or embedding outside the OTLP transport [S3]. Reusing
it does not require running an OTel SDK or implementing HTTP/gRPC.

OTLP JSON encoding is specified within stable OTLP [S1], with important
differences from ordinary ProtoJSON:

- IDs are hexadecimal, **not base64**; emit lowercase 32/16-character strings.
- JSON field names are lowerCamelCase, not protobuf snake_case names.
- Enum values must be **numbers**, not enum-name strings.
- 64-bit integer values, including nanosecond timestamps, are decimal strings.
- OTLP JSON receivers must ignore unknown message fields; generic ProtoJSON
  parsers need the corresponding unknown-field option [S1, S12].

The **OpenTelemetry Protocol File Exporter specification is Development**, and
calls itself a placeholder. Its current serialization recommendation is UTF-8
JSONL, one top-level `TracesData` object per line for trace files; signals must not
be mixed. It guarantees neither file order nor monotonic timestamps [S2]. It is
not a stable, universal file-exporter interoperability contract.

Collector `fileexporter` and `otlpjsonfilereceiver` document **alpha** trace
stability at the inspected revisions [S4, S5]. The exporter supports JSON lines,
and the receiver documents reading that serialization. The exporter also warns
that exact field names are not guaranteed stable. These are useful precedents,
not a reason to copy its rotation or operational contract. No Collector binary
or viewer import has been tested as part of this design pass.

### Alternatives

| Candidate | Benefits | Costs/limitations for Trail | Decision |
| --- | --- | --- | --- |
| A. OTLP model + OTLP JSONL | Stable span vocabulary; documented file ingestion path; generic JSON tooling; one small `TracesData` batch per ended span is appendable | Nested envelope repeated; no native start/update journal; active spans and unexported events disappear on crash; uint64/duration need mapping; file spec still Development | Strong alternative if direct ingestion wins; not the proposed native capture |
| B. Versioned Trail JSONL journal | Starts/events survive before End; natural append-only replay; native unsigned/duration values; bounds independent of trace/event-history length | Own reader/replay contract; no direct existing-viewer import; converter needed for OTLP/Chrome | Recommended v0.1 capture |
| C. Chrome Trace Event JSON | Mature ecosystem; Perfetto directly imports familiar duration/instant events [S6] | Track/thread-oriented rather than trace-tree oriented; ordinary duration events must nest on a track; async lanes/flows needed for arbitrary overlap; usual array/container needs finalization or recovery | Future conversion target, not authoritative storage |
| D. Perfetto TrackEvent protobuf | Packet streaming, nanosecond timestamps, custom tracks/flows, mature UI/SQL analysis [S7] | Binary tooling and schema/descriptor burden; more concepts than local spans need; mapping asynchronous spans still required | Future conversion target; unjustified first dependency stack |

An array can be emitted incrementally; Chrome JSON does **not** inherently require
holding the whole capture in memory. Its disadvantages here are container
finalization/recovery and the mismatch between thread-nested slices and Go's
overlapping, cross-goroutine spans. A newline per event inside an unclosed array
is not JSONL. Do not rely on a viewer accepting a truncated array.

OTLP JSONL also does **not** require a giant trace object: one completed span in
one `TracesData` line is valid in shape. However, a single-span object without the
`resourceSpans -> scopeSpans -> spans` envelope is not the file representation
described in [S2]. A fake span with `endTimeUnixNano = 0`, or repeated snapshots
of a span as it changes, is not a verified OTLP lifecycle journal contract.

### What the recommendation actually guarantees

Trail's proposed runtime model maps names, IDs, parents, scope, attributes,
events, timestamps, and status to the stable OTLP span model. Its **journal is a
custom Trail format**, not an OTLP payload with a cosmetic envelope. Merely
removing its header will not make it OTLP. Replay and type conversion are needed.

Keep future OTLP JSON output separate: each line must then be genuine
`TracesData`, using the OTLP encoding rules above. Do not insert a Trail header or
Trail start/update records into that stream. Do not advertise compliance with
the OTel API, SDK, exporter configuration, transport, or evolving file-exporter
specification.

The semantic alignment boundary is specific, not merely "OTel-like":

| Concept | Intentionally aligned | Trail-specific or deferred |
| --- | --- | --- |
| Identity/tree | 16-byte TraceID, 8-byte SpanID, shared trace ID, one parent per span, absent root parent | Provider-local parenting; no remote/W3C propagation or links |
| Scope/name | Named instrumentation scope and named operation | No discovered resource/service attributes; immutable names in v0.1 |
| Attributes/events | Unique typed keys; named, timestamped events with attributes | uint64/duration tags, streamed updates, explicit local limits; no full AnyValue object model |
| Status/error | Unset/OK/Error categories; RecordError separate from status | Last-write status policy; generic `error` event, not automatic OTel exception conventions |
| Timing | Nanosecond timestamps and start/end intervals | Explicit monotonic duration and capture-relative elapsed axis |
| Storage/lifetime | Reconstructable spans can populate OTLP Span fields | Versioned journal header, mutation records, sequence, trace_end, segment routing; none are OTLP records |

A future converter maps reconstructed spans into scopeSpans, uses an empty or
explicitly supplied resource, emits kind INTERNAL (`1`) for the initial API, maps
Unset/OK/Error to OTLP codes `0/1/2`, and carries unique attributes, events, and
drop counts. It normalizes exported timestamps using capture origin plus elapsed
offsets to preserve durations rather than exporting misleading wall-clock jumps.
Trace-end/sequence/header records do not become fake spans. Unfinished spans are
reported separately or omitted with an explicit conversion report, not assigned
invented ends. Unsigned/duration type loss is specified in section 7. This is a
future mapping contract to test, not an implemented converter.

Interoperability expectations:

- Native journal: editors, line tools, and `jq` can inspect it immediately.
- OTLP JSONL after conversion: Collector file ingestion is a documented path,
  but must be verified against a pinned release and representative fixtures.
- Perfetto/Chrome: the inspected Perfetto documentation establishes Chrome and
  native Perfetto imports, not direct Trail/OTLP JSONL imports [S6, S7].
- A backend accepting OTLP requests is not evidence that its UI imports files.
- OTel Go's `stdouttrace` serializes SDK span stubs, not `TracesData`; its JSON
  must not be mistaken for OTLP file JSON [S9].

## 4. API and ownership proposal

All signatures and examples below are illustrative, **not available Go APIs**.
Prefer concrete handles with private state instead of extensible Tracer/Span
interfaces. Processor and Sink are the pipeline extension boundaries, not
alternative Tracer/Span implementations. Value copies of Tracer
and Span share their underlying state; copying a span cannot duplicate End.

```go
// Proposed root-package surface, not an implementation.
func NewProvider(processor Processor, opts ...ProviderOption) (*Provider, error)
func NewSyncProcessor(sink Sink) (*SyncProcessor, error)

// Future configuration alternative; not part of the first implementation:
// func NewAsyncProcessor(sink Sink, opts ...AsyncOption) (*AsyncProcessor, error)

func (p *Provider) Tracer(scope string) Tracer
func (p *Provider) Flush(ctx context.Context) error
func (p *Provider) Shutdown(ctx context.Context) error

func (t Tracer) Enabled() bool
func (t Tracer) Start(ctx context.Context, name string,
    opts ...StartOption) (context.Context, Span)

func (s Span) End()
func (s Span) IsRecording() bool
func (s Span) SpanContext() SpanContext
func (s Span) SetAttributes(attrs ...Attribute)
func (s Span) AddEvent(name string, opts ...EventOption)
func (s Span) RecordError(err error, opts ...EventOption)
func (s Span) SetStatus(code StatusCode, description string)

func SpanFromContext(ctx context.Context) Span
func SpanContextFromContext(ctx context.Context) SpanContext
func ContextWithSpan(ctx context.Context, span Span) context.Context

// WithAttributes can be accepted as both a StartOption and EventOption.
// WithNewRoot is a StartOption; explicit timestamp options are deferred.
```

The root package exposes common span, ID, attribute, event-option, and status
functionality directly. A named scope is a component/instrumentation name, not
an implicit `service.name`. Tracer handles need not be cached in an unbounded
provider map. Empty scope can mean unknown; empty span/event names are normalized
to a documented placeholder with a diagnostic rather than panicking.

Application wiring might eventually look like:

```go
// Proposed usage inside an application function returning error.
sink, err := file.Open("capture.trail.jsonl")
if err != nil {
    return err // application may explicitly choose to continue without tracing
}
processor, err := trail.NewSyncProcessor(sink)
if err != nil {
    // Processor construction did not transfer sink ownership.
    return errors.Join(err, sink.Shutdown(context.Background()))
}
provider, err := trail.NewProvider(processor)
if err != nil {
    // Provider construction did not transfer processor ownership.
    return errors.Join(err, processor.Shutdown(context.Background()))
}
tracer := provider.Tracer("example/resolver")

workErr := func() error {
    ctx, span := tracer.Start(ctx, "resolver.reconcile",
        trail.WithAttributes(
            trail.String("provider", "modrinth"),
            trail.Int("candidates", 12),
        ),
    )
    defer span.End()
    // Pass ctx to child work and wait for any owned goroutines before returning.
    if err := reconcile(ctx); err != nil {
        span.RecordError(err)
        span.SetStatus(trail.StatusError, "lookup failed")
        return err
    }
    return nil
}()

// Use a fresh cleanup context, not an already-canceled work context.
cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
return errors.Join(workErr, provider.Shutdown(cleanupCtx))
```

No environment configuration, implicit enablement, or application-exit hooks.
The optional default-provider convenience layer below does not own lifecycle.
Libraries receive a Tracer from their host. A provider
does not own application goroutines. It owns exactly one processor, exclusively;
the processor owns its sink. Sharing a processor between providers or a sink
between processors is unsupported in v0.1. A file can still serve arbitrarily
many traces during that provider's lifetime. The tracer/provider never invokes
Sink.WriteRecord, Sink.Flush, or Sink.Shutdown directly.

Successful NewProvider validates options and submits capture initialization through
the processor, then assumes processor ownership. Successful processor construction
transfers sink ownership to it; a failed constructor leaves ownership with its
caller. Initialization acceptance is not a promise of disk durability or detection
of every later asynchronous write failure. A nil processor and zero-value
Provider/Tracer/Span are disabled, safe handles. Do not pass interfaces containing
typed-nil processors or sinks; a real processor requires a valid sink.
Setup failures return errors; they must not manufacture a seemingly enabled
provider. Replacing a processor/sink or runtime enablement is out of scope initially.

### Optional process-global Provider convenience

Explicit ownership remains primary. The v0.1 global surface is one registration
function and one named-tracer lookup:

```go
func SetDefaultProvider(p *Provider)
func GetTracer(scope string) Tracer
```

The globally registered object is a Provider; Tracers remain products of a
Provider. GetTracer loads the Provider registered at the time of the call and
returns its Tracer(scope), using the same scope naming rules as explicit usage.
There is no separately stored global Tracer, default-provider getter,
previous-provider return value, or reset/restore helper. Do not call this NewTracer:
the tracer is resolved through a Provider, not constructed independently of one.

Do not add package-level Start in v0.1. It would lose or implicitly invent the
instrumentation scope, or require both scope and span name with little advantage
over `trail.GetTracer(scope).Start(ctx, name)`. Do not infer scope from runtime
caller information or introduce an anonymous/default tracer.

Before configuration, the internal default resolves to a safe no-op Provider.
Passing nil selects that same no-op default using the existing setter, not a
separate reset API. This state creates no files, processors, workers, context
wrappers, or records. Preserve the allocation-free target for bare disabled
Start/End. GetTracer before configuration returns a non-recording tracer without
initializing output. Target zero allocations for bare GetTracer(scope).Start/End
as well as cached disabled tracer Start/End; measure both rather than assuming
the atomic lookup is free.

SetDefaultProvider atomically replaces a non-owning provider reference. GetTracer
is concurrency-safe and reads one complete old or new reference at a single
linearization point, then delegates to that Provider. Replacement affects future
GetTracer calls only; it does not mutate or rebind any already-created Provider,
Tracer, Span, or trace. Returned tracers retain their original Provider, including
no-op tracers obtained before configuration. Configure before obtaining long-lived
application tracers; no per-Start default lookup or forwarding tracer is involved.
OTel Go's preconfiguration tracer forwarding
behavior [S21] is research context, not a behavior Trail adopts.

Registration transfers no lifecycle ownership and performs no Flush, Shutdown,
file creation, automatic configuration, processor startup, environment lookup,
exit hook registration, or other hidden work. Installing a closing/closed provider
does not reopen it. Replacing or clearing the registration never closes the old
provider or waits for its users. The constructing caller must coordinate those
users and explicitly Flush/Shutdown its provider. Normal provider shutdown rules
still govern any tracers that retain it.

The registered provider uses exactly the same Tracer -> Processor -> Sink pipeline
as an explicitly held provider. Registration cannot bypass Processor and has no
processor- or sink-specific behavior.

#### Small application: register and resolve a named tracer

After explicitly constructing sink, processor, and provider as shown above:

```go
trail.SetDefaultProvider(provider) // Optional registration; no ownership transfer.
tracer := trail.GetTracer("myapp") // Explicit equivalent: provider.Tracer("myapp").
ctx, span := tracer.Start(ctx, "command.run")
workErr := runCommand(ctx)
span.End()

// Application teardown after its tracing users have stopped.
cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
flushErr := provider.Flush(cleanupCtx)
shutdownErr := provider.Shutdown(cleanupCtx)
return errors.Join(workErr, flushErr, shutdownErr)
```

Flush is optional before Shutdown because Shutdown already drains/flushes. The
example calls both to show their explicit owner. Registration is optional:
applications can instead use provider.Tracer("myapp") with no global dependency.

#### Reusable library: accept an explicit tracer

```go
type Builder struct { tracer trail.Tracer }

func NewBuilder(tracer trail.Tracer) *Builder {
    return &Builder{tracer: tracer}
}

func (b *Builder) Build(ctx context.Context) error {
    ctx, span := b.tracer.Start(ctx, "build")
    defer span.End()
    return b.build(ctx)
}

builder := NewBuilder(provider.Tracer("example.org/buildlib"))
```

Libraries generally must not mutate the process default or shut down a host-owned
provider. A zero Tracer permits safe opt-out without global dependencies.

#### Tests: prefer explicit ownership

Tests should construct explicit providers/tracers and may run in parallel without
touching global registration. Tests specifically exercising SetDefaultProvider
must serialize or run in isolated subprocesses. Atomics prevent data races, not
semantic interference between competing setters. GetTracer returns a tracer,
not the registered provider; there is no provider getter or previous-provider
return for discovering/restoring an unknown prior default.
If a controlled test fixture needs to reinstall a known provider, it retains that
handle itself and uses SetDefaultProvider again; it cannot assume isolation from
uncoordinated tests or application goroutines.

The v0.1 global acceptance tests must verify safe no-op GetTracer before
configuration and after SetDefaultProvider(nil), explicit/global scope equivalence,
and old/new tracer snapshots across replacement. Existing tracers and active spans
must retain their provider; preconfiguration tracers must remain no-op. Verify
concurrent registration/lookup under the race detector, no separately stored
global tracer, and no implicit lifecycle or sink calls during registration/lookup.
Run processor-independent cases against SyncProcessor first and AsyncProcessor
later, without adding public testing conveniences.

## 5. Context and span lifecycle

Use an unexported typed context key and standard `context.Context`. Start returns
a derived context carrying the new span; it does not mutate the parent context.
No goroutine-local storage or goroutine-ID extraction. Context cancellation does
not End spans or discard their completion records; defer/lifecycle ownership is
explicit. Require a non-nil context, as ordinary Go context APIs do.

Parent rules:

1. An active span from the **same provider** is a parent; inherit TraceID.
2. No parent, WithNewRoot, an ended parent, or a foreign-provider span creates a
   new local root and a new TraceID. A foreign parent must not cause one trace to
   be silently split across sink owners. IDs alone do not confer parenting rights.
3. Root admission and child admission happen atomically with lifecycle checks.
   A child Start racing parent End either joins while the parent is active or
   becomes a new root; this is deliberately not timing-independent.
4. End does not End children. Children already admitted may outlive their root
   and create more children while **they** are active.
5. Once an individual span ends, it cannot admit new children. Reusing its context
   for later independent work creates a new trace, rather than reopening a closed
   trace/segment. Keep a live coordinating span when ongoing work belongs together.

Rule 5 differs from OTel, which permits ended-span parenting [S8]. Preserving
that behavior would require a separate public trace close/lease operation or
retaining routing state indefinitely. Neither is recommended for v0.1. This
tradeoff needs review before API freeze, not an undocumented implementation hack.

Start captures start time and IDs, reserves active capacity, and submits a
`span_start` record. End has one winning state transition: capture end time,
submit `span_end`, release the active reference, then submit `trace_end` if the trace
is quiescent. Repeated End and mutations after End are no-ops. IDs stay available
after End. Mutations racing End either linearize before it and are recorded, or
lose and do nothing. Trace completion follows the final admitted span, not
necessarily the root.

If admission fails because of a limit, processor capacity, or broken output,
return a non-recording span
and the original context. The caller's existing recording ancestor remains in
that context; later children may attach there, so dropped instrumentation can
coarsen the tree. The returned span's methods must not mutate that ancestor.

## 6. Core data model, IDs, and clocks

The logical model is independent of encoding:

| Entity | Essential data |
| --- | --- |
| Capture | Clock origin, provider lifetime, file format version at serialization |
| Trace | TraceID, root SpanID, active-span count; no collection of completed spans |
| Span | TraceID, SpanID, ParentSpanID, scope, name, start/end, elapsed duration, attributes, events, status |
| Event | Name, timestamp, elapsed offset, typed attributes |
| Record | Typed lifecycle/update payload; trace/root IDs for routing; capture-relative elapsed time and sequence |

The runtime need not retain the full logical span model: a journal streams events
and updates immediately. A span retains IDs, timing, lifecycle state, bounded
attribute-key bookkeeping, and drop counts, not an event list or completed
children. Replay reconstructs the logical model later.

Propose `TraceID [16]byte`, `SpanID [8]byte`, and an immutable-by-value SpanContext
containing both. Provide IsValid, String, and strict parse helpers. All-zero IDs
are invalid; root ParentSpanID is absent on the wire, not a valid zero parent.
String/JSON forms are fixed-width lowercase hex; parsing can accept either case
but must reject wrong lengths, malformed hex, and zero identifiers where valid
IDs are required. No `traceparent`, baggage, TraceState, remote bit, or public
sampling flags are necessary for local propagation.

Use the standard cryptographic random source for IDs, retrying a generated zero
value. Do not use wall time, PID, `math/rand`, or sequential production IDs.
Test injection should accept a small ID generator with separate root-trace and
span generation methods, returning errors for deterministic failure tests.
Serialize calls unless the injected source is documented concurrency-safe. Invalid
test-generated IDs fail admission with a diagnostic. Trail's ID handling does
not replace Go's own policy for failure of its OS cryptographic random source.

Collisions are probabilistic, not impossible: birthday approximation is
`n*(n-1)/(2*2^bits)`. TraceIDs have 128 bits; SpanIDs need uniqueness only within
their trace. One million spans in one trace have roughly a 2.7e-8 SpanID collision
probability. No unbounded set of historical span IDs is proposed. IDs are not
security credentials; merged captures still require collision/duplicate checks.

Use `time.Now` with its monotonic component for durations and capture-relative
offsets. Record wall-clock Unix nanoseconds for correlation as well. JSON cannot
preserve Go's monotonic clock reading directly. Capture initialization stores a
wall-clock origin; subsequent records store elapsed nanoseconds from that origin.
For analysis/conversion, `originWall + elapsed` gives a consistent local timeline
even if the system clock steps. Actual wall timestamps can reveal that step.
This does not synchronize different machines/captures or imply nanosecond clock
accuracy [S20]. Preserve raw in-process time readings for measurement: calling
UTC/Local/In, Round/Truncate, or serializing/reparsing them can strip their
monotonic component. Formatting for display must operate on a copy, not replace
the stored measurement source. Monotonic clocks may exclude time spent asleep on
some systems; document elapsed timing as the Go/OS monotonic interval, not a
promise about suspend-inclusive wall time.

The wire representation keeps three distinct quantities:

- `timeUnixNano`: the actual wall-clock reading, for external correlation.
- `elapsedNano`: elapsed from this capture's origin, for relative placement.
- `durationNano` on span_end: explicitly measured span duration, authoritative
  for performance. It equals end elapsed minus start elapsed with a valid clock.

Readers must never use end wall minus start wall as a fallback for a missing
duration. Missing End means duration unknown. Example: a span can start at wall
12:00:00, end at wall 11:59:59 after a clock correction, and still have a measured
duration of 200 ms. Display the wall discontinuity without a negative duration.
Use integer arithmetic with checked range conversions; do not hide overflow or
invalid test-clock readings by clamping to a plausible measurement.

For deterministic tests, a single `func() time.Time` is insufficient to simulate
independent wall steps and elapsed progress: time.Unix/Parse produce wall-only
values. Recommend a small injected reading function returning **wall time and a
monotonic-relative tick together**, not a scheduling/timer framework. Production
readings derive ticks from raw time.Now().Sub(origin); a fake can move wall time
backward while ticks advance. Serialize injected source calls and require a
consistent tick domain. Normalize to capture-relative offsets and subtract ticks
for durations; detect regressions in the supplied clock. Public naming of this
test seam remains provisional. Concurrent scheduler order is not deterministic.

## 7. Attributes, events, status, and logging

The typed attribute model, bounded span/event attribute batches, streamed
events, RecordError, last-write status, and the per-span drop counts
serialized on span_end are implemented for v0.1, including the active-span
capacity limit with release on every End path. Flush/Shutdown return output
and admission-loss errors. Capture-scoped cumulative `loss_summary` records
checkpoint rejected admissions and bounded-data drops at both Flush and Shutdown;
[the accepted decision](loss-summary-recommendation.md) specifies the schema,
zero-snapshot behavior, and replacement replay contract.

### Typed attributes

Use a closed, small Attribute/Value representation, not `map[string]any` or a
general-purpose JSON object model. Evaluate the following constructors:

| Proposed constructor | Native value | OTLP mapping after replay |
| --- | --- | --- |
| String | string | `stringValue` |
| Bool | bool | `boolValue` |
| Int / Int64 | signed int64 | `intValue` decimal string |
| Uint64 | unsigned uint64 | `intValue` if <= MaxInt64; otherwise decimal `stringValue`, with documented type loss |
| Float64 | finite float64 | `doubleValue` number |
| Duration | signed nanoseconds | `intValue` nanoseconds; unit/type not intrinsic to OTLP |
| Strings | homogeneous string slice | `arrayValue.values` of `stringValue` |

Unsigned integers and durations are useful locally, but OTLP AnyValue has no
unsigned-integer or duration alternative [S3]. Never overflow, cast uint64 to a
negative number, or convert a large integer to a lossy float. A future converter
must report type-loss mappings, not promise lossless round trips. Additional
homogeneous bool/int/float arrays can wait for actual demand; omit nested maps,
heterogeneous arrays, bytes, reflection, and arbitrary Stringer evaluation in v0.1.

Attribute keys are unique within a final span/event. Last accepted update wins;
duplicates in one input batch are resolved left-to-right before emission. Empty
keys and invalid values are dropped with counters. Reject NaN/Inf in the native
finite-float model rather than letting `encoding/json` fail a whole record.
No automatic semantic names, units, environment/resource discovery, or central
vocabulary. Duration keys should describe their unit to consumers where useful.

Caller slices must be stable during the call; Trail copies bounded slice data
before retaining it or passing it to an asynchronous extension. Mutation after
the method returns cannot change captured output. Attribute helpers can be
borrowed-value builders with copying deferred until recording, so disabled calls
do not unnecessarily clone arrays. Document that distinction explicitly.

### Bounds, not whole-trace accumulation

Initial **tuning proposals**, to validate before release: 4,096 concurrent active
spans/provider, 64 unique keys/span, 32 keys/event, 256-byte keys/names, 4-KiB
string values, 64 elements/array, and 64-KiB encoded mutation/event records.
Truncate strings only on UTF-8 boundaries; copy retained truncated keys so small
substrings do not keep enormous backing strings alive. Invalid UTF-8 normalization
must be documented and counted, not an accidental encoder behavior.

Stream events rather than cap-and-retain an event list. Output size can grow
with activity; memory must not. Existing-key updates are still permitted at the
key-count limit. Oversized update/event records are dropped whole, incrementing
drop counters; essential start/end/control records have separately bounded
payloads and cannot be dropped solely because an attribute batch is too large.
Initial attributes can be emitted as a bounded update following span_start.
Reserve capacity before emitting an accepted start; release it on every End
path, including output failure. No default unlimited queue or active registry.

These limits bound library-owned retained memory, not caller allocations or
unbounded concurrency of callers constructing input. A provider holds only active
trace bookkeeping; an ended span retained in a user context still costs a small
handle. Flush/Shutdown report admission loss as well as I/O errors so incomplete
captures do not appear healthy.

### Events and errors

AddEvent records a name, current timestamp/elapsed offset, and attributes such as
`cache.miss`, `retry.scheduled`, `request.sent`, or `candidate.rejected`. It has no
severity/filtering subsystem, formatting language, or independent logging stream.

RecordError(nil) is a no-op. A non-nil error records one `error` event with the
explicitly requested `error.message` derived from `err.Error()`, plus supplied
attributes. Do not retain the error object, walk causes automatically, capture a
stack, or record a panic automatically. Do not call user error/string formatting
code while holding internal locks. RecordError does **not** set status; retries
and handled errors may not make an operation fail. This follows the familiar OTel
Go separation [S10], without importing its exception convention catalog.

StatusUnset is default; StatusOK and StatusError are explicit. Recommend the last
accepted SetStatus call wins, including a reset to Unset. Description is meaningful
only for Error and is cleared for other codes. This intentionally differs from
OTel's `OK > Error > Unset` precedence [S8]; no hidden sticky-success behavior.
Unknown codes are ignored with a diagnostic. Status is never inferred at End.

### Coexistence with slog

Expose SpanContextFromContext and ID formatting so applications can attach
`trace_id`/`span_id` through their own `slog.Handler` or log calls. `log/slog` is
standard-library code, but the tracing core need not import or configure it.
No logger ownership, automatic event-to-log conversion, or slog subpackage yet.

## 8. Processor, sink, and concurrency contracts

The mandatory architecture is:

```text
Tracer / Span -> provider lifecycle bookkeeping -> Processor -> Sink

v0.1:   SyncProcessor  -> Sink
future: AsyncProcessor -> bounded queue -> one background writer -> same Sink
```

Proposed minimum boundaries (names remain provisional):

```go
type Processor interface {
    Process(record Record) error
    Flush(ctx context.Context) error
    Shutdown(ctx context.Context) error
}

type Sink interface {
    WriteRecord(record Record) error
    Flush(ctx context.Context) error
    Shutdown(ctx context.Context) error
}
```

Processor.Process reports **acceptance/rejection**, not a universal disk-write
guarantee. Span methods use it internally and keep the same void mutation/End
signatures regardless of processor. The provider submits its own lifecycle/control
records through this boundary too; it cannot bypass processing for Flush or End.
Processor.Flush guarantees delivery of its preceding accepted records followed by
Sink.Flush. Processor.Shutdown drains accepted records, flushes, and shuts down
the owned sink. The sink interface contains no queue or processor-mode concepts.

Record is a closed typed union of capture_start, span_start, span_update, event,
span_end, trace_end, and bounded loss-summary control records, with safe accessors.
Use the **same record types, encoding, timestamps, IDs, and replay rules** for both
processors. A no-overflow AsyncProcessor run must produce the same logical
journal as SyncProcessor for the same ordered input. Delivery timing and possible
loss differ, not the span/trace semantics. Loss fields/records are common to both:
SyncProcessor must also report input-limit loss and failed admissions. Their exact
schema is part of the journal-freeze gate, not an implemented async format.

Generate IDs and sample wall/elapsed times in instrumentation, not on background
dequeue. Processor delay must not extend span duration or change event timestamps.
Processors preserve retained input order; do not batch-reorder lifecycle records.
File serialization version belongs to the file sink. A test sink can copy records;
a future ended-span exporter can replay per-span state internally, with explicit
bounded-state requirements rather than an SDK hidden inside the core.

Why not io.Writer alone? It has no ownership/barrier semantics. Why not OTel's
batch SpanExporter? Ended-span batches do not carry early records or root admission
for trace-local rotation [S11]. Why not a processor/sampler/transform chain? One
processing boundary meets the delivery requirement; middleware registries and
arbitrary processor composition are unnecessary in v0.1.

### Shared lifecycle and concurrency

- Provider, Tracer, and Span methods are safe for normal concurrent use.
- A span mutex linearizes its own updates/End. IDs are immutable; Enabled and
  IsRecording are advisory. Processor selection is configuration at construction,
  never a SpanOption or a different completion predicate.
- A short provider admission gate serializes Process submissions, assigns
  capture-wide sequences, and coordinates lifecycle/barrier insertion. Admission,
  span-end/count release, and resulting trace_end form one lifecycle transaction.
  Shutdown cannot interleave halfway through it.
- The processor, not the tracer, serializes sink calls. SyncProcessor does so on
  the submitting path; AsyncProcessor uses its sole writer. Sink.WriteRecord,
  Flush, and Shutdown must never overlap for the same sink.
- Runtime trace completion remains `root_ended && live_span_count == 0` even if
  its records are still queued. **Delivered completion** requires the ordered
  trace_end to reach the sink; **durability** depends on sink Flush/Sync policy.
  Future segment closure must wait for delivered completion, not runtime counts.
- Lock order is span state -> provider admission -> processor acceptance. Neither
  async dequeue nor sink calls may acquire span locks or require the provider
  admission gate. Establish this before implementation and test races explicitly.
- Processor.Process may borrow immutable record payloads only until it returns.
  An async/retaining processor must take an owned bounded copy before returning;
  caller mutation cannot affect queued data. Sink payload borrowing lasts through
  WriteRecord. Pools cannot reclaim data before that call finishes.
- Error callbacks run outside locks held by their invoking path. No callbacks
  from inside Process while the provider gate is held; immediate errors return to
  the provider for reporting after unlock. Background failures are latched and
  reported outside writer locks through the configured pipeline error handler.
  Processors/sinks must not reenter their owning tracing pipeline.

The logical admission gate is not a disk-I/O requirement of the tracing API.
Under SyncProcessor it naturally stays occupied during synchronous delivery;
under AsyncProcessor it performs bounded acceptance/copying without waiting for
queue space or sink I/O. Ordinary short mutex contention is possible: "non-blocking
normal instrumentation" means no sink/queue-capacity waits, not a lock-free or
hard-real-time promise. Same-span partial ordering is preserved, but no stable
sibling, scheduler, or global wall-timestamp ordering is promised.

### Initial SyncProcessor

Implement only SyncProcessor in the first milestone. Process validates the record,
invokes Sink.WriteRecord in order, latches failures, and returns after the attempt.
No worker or queue. Flush/Shutdown delegate in order after preceding calls complete.
This makes lifecycle, journal, crash-tail recovery, error propagation, and resource
ownership testable without queue mechanics.

Start/Event/End **may** block on disk when this processor is configured. End's
public contract is still a logical-end and processor-submission attempt, not
"bytes written before return". Sync delivery is an implementation-specific stronger
property, not a guarantee applications may assume for every processor. Use Flush
or Shutdown for processor-independent delivery guarantees. OTel's nonblocking-End
expectation is a useful target for the future async mode, not a promise about sync
configuration [S8].

### Future AsyncProcessor: first-class option, not an implementation now

Required architecture: finite record **and byte** queue limits, one owned writer,
non-blocking normal admission, explicit rejection/drop accounting, a Flush barrier,
and draining Shutdown. Do not silently fall back to synchronous writes on overflow.
No unbounded secondary error, completion, retry, or callback queue.

Not every record may be dropped interchangeably. Losing an event/update is a
visible fidelity loss; losing an admitted span's End or a trace_end can make
rotation drain forever or imply a false lifecycle. Reserve bounded capacity for
essential lifecycle records **when accepting a span start**, including its End,
root trace_end/control obligations, and bounded loss summaries. Reject new span
admission if those credits cannot be reserved. Return start rejection to the
provider before a recording span/context is exposed. Existing-key/event/status
updates may be dropped with counters; retained records cannot change order.

Reservations must cover maximum encoded/owned lifecycle payloads and remain held
until the writer consumes the reserved records, not merely until runtime End.
Otherwise a stalled writer plus repeated Start/End can exhaust all completion
space despite a low live-span count. Any reserved control lane must itself be
bounded. Queue overflow/drop counts belong to the common loss model, including
attribute/status-update losses and rejected starts, not async-only schema fields.
Loss summaries are ordered control records; they must not allow trace_end to
masquerade as a lossless capture. Total sink failure may prevent persisting those
summaries, so sticky errors remain necessary.

This credit/rejection policy addresses the unavoidable tradeoff: a finite-memory,
non-waiting processor cannot accept every record while its writer is permanently
stalled. It preserves lifecycle for **accepted** spans instead of promising lossless
nonblocking tracing under arbitrary load. Exact reservation sizes and overflow
selection remain an AsyncProcessor design gate, not extra v0.1 machinery.

Flush establishes an admission high-water mark and waits for all retained records
through it to be consumed, then performs Sink.Flush on the writer. Concurrent
later records are outside that barrier. Barrier insertion is a lifecycle operation
and may wait with its context; normal span methods do not wait for queue capacity.
Shutdown stops admission, drains accepted queued/control records, flushes, and
closes the sink through the same writer. It does not wait for unended application
spans or invent their End records. Cancellation leaves resumable closing state;
it cannot abandon the owned writer, discard its queue silently, or close the sink
under an in-progress write. Joining/draining is distinct from sink power-loss
durability.

Async crash usefulness is necessarily weaker in timeliness: only delivered journal
records survive, and the bounded pending queue may be lost on kill/crash. Starts
and events must be continuously drained **before spans end**, never deferred until
trace completion. No claim that every call accepted into RAM is crash durable.
Apps needing the narrower loss window can choose SyncProcessor without changing
instrumentation, or call Flush at intentional checkpoints.

### Shared processor conformance suite

Design one ordered-input harness parameterized by a processor factory and a
recording/failing sink. Test identical no-loss journal records; parent/child and
unfinished-span reconstruction; immutable queued payloads; duplicate End;
root-before-child End; trace completion; error latching; Flush high-water marks;
no overlapping sink calls; and shutdown ownership, cancellation, retry, and close.
Assertions observe the sink **after Flush/Shutdown**, not immediately after End.
SyncProcessor runs it first; AsyncProcessor must pass it later unchanged.

Add processor-specific tests separately: immediate sync sink-error returns, and
future bounded queue/byte credits, stalled writer overflow, loss summaries,
background failure callbacks, nonwaiting instrumentation, barrier progress under
full queues, and no goroutine leaks. Shared semantic tests must not assert that
Process/End blocks, that the sink has already seen a record on return, or that
concurrent producer scheduling is deterministic.

## 9. Native local file proposal

### Naming, opening, and ownership

Use caller-specified paths, conventionally `capture.trail.jsonl`. Do not derive
names from usernames, paths, URLs, or process arguments. No "one invocation,
one file" rule in the core.

Propose `file.Open(path, ...Option) (*file.Sink, error)` in
`go.lostcrafters.com/trail/file`. Default to exclusive new-file creation (no
overwrite), restrictive permissions such as 0600 on Unix, and no automatic
directory creation. Permissions on Windows require host ACL policy; Unix mode
bits alone are not a cross-platform privacy guarantee. Report setup/header-write
errors and close a newly opened descriptor on failed setup. This opening
behavior, the versioned header, unbuffered complete-line writes, stream stop
after uncertain writes, `WithSyncOnFlush`, and the shutdown cleanup behavior
are implemented in v0.1; resume/append mode remains deferred.

The file is append-only **during this capture**. Resuming/appending to an existing
capture is deferred; this is different from streaming append writes. Never
silently truncate an old artifact. A future append mode must validate the header,
capture boundary, and final line before writing, with an explicit repair policy.

One provider/processor/sink pipeline may record many concurrent traces. Independent sinks or
processes writing the same file are unsupported. In-process serialization is
required even with append flags: a logical line may involve partial/multiple OS
writes, and `O_APPEND` is not a portable multiwriter record-atomicity guarantee.
Do not support external copytruncate/logrotate against a live capture.

### JSONL shape and replay

Recommend UTF-8, no BOM, compact object per LF-terminated line, no blank lines,
and no surrounding array. Put an explicitly versioned Trail header first;
capture_start then anchors the provider's clock. All other records carry sequence,
capture-relative elapsed nanoseconds, and actual wall timestamp; span records
carry TraceID, SpanID, and RootSpanID so routing does not require tree reconstruction.
ParentSpanID belongs to span_start; it is absent for roots. Record integer/time
values as decimal strings to avoid JavaScript precision loss.

The following is a **draft synthetic fixture**, not the finalized schema. The
root ends while its child remains active; the trace completes only after the
child. `span_update` may contain attributes, status, or both.

Implementation status: the header and all records below are implemented for
the still-unreleased version 1 by `go.lostcrafters.com/trail/file`, including
decimal-string numerics, hex identifiers, absent `parentSpanId` for roots,
typed updates/events, per-span drops, and capture-scoped loss snapshots.
Adding the accepted loss record completes that gate without a version bump;
real-capture schema-freeze review remains required before declaring v1 stable.

```jsonl
{"format":"trail","version":1}
{"type":"capture_start","seq":"1","timeUnixNano":"1700000000000000000","elapsedNano":"0"}
{"type":"span_start","seq":"2","timeUnixNano":"1700000000000000000","elapsedNano":"0","traceId":"11111111111111111111111111111111","spanId":"2222222222222222","rootSpanId":"2222222222222222","scope":"example/resolver","name":"resolver.reconcile"}
{"type":"span_start","seq":"3","timeUnixNano":"1700000000000000010","elapsedNano":"10","traceId":"11111111111111111111111111111111","spanId":"3333333333333333","rootSpanId":"2222222222222222","parentSpanId":"2222222222222222","scope":"example/resolver","name":"provider.lookup"}
{"type":"event","seq":"4","timeUnixNano":"1700000000000000015","elapsedNano":"15","traceId":"11111111111111111111111111111111","spanId":"3333333333333333","rootSpanId":"2222222222222222","name":"cache.miss","attributes":[{"key":"candidates","type":"int64","value":"12"}]}
{"type":"span_end","seq":"5","timeUnixNano":"1700000000000000020","elapsedNano":"20","traceId":"11111111111111111111111111111111","spanId":"2222222222222222","rootSpanId":"2222222222222222","durationNano":"20","droppedAttributes":"0","droppedEvents":"0"}
{"type":"span_update","seq":"6","timeUnixNano":"1700000000000000022","elapsedNano":"22","traceId":"11111111111111111111111111111111","spanId":"3333333333333333","rootSpanId":"2222222222222222","status":{"code":"error","description":"lookup failed"}}
{"type":"span_end","seq":"7","timeUnixNano":"1700000000000000030","elapsedNano":"30","traceId":"11111111111111111111111111111111","spanId":"3333333333333333","rootSpanId":"2222222222222222","durationNano":"20","droppedAttributes":"0","droppedEvents":"0"}
{"type":"trace_end","seq":"8","timeUnixNano":"1700000000000000030","elapsedNano":"30","traceId":"11111111111111111111111111111111","rootSpanId":"2222222222222222"}
{"type":"loss_summary","seq":"9","timeUnixNano":"1700000000000000040","elapsedNano":"40","rejectedStarts":"0","droppedAttributes":"0","droppedEvents":"0","unendedSpans":"0","unendedDroppedAttributes":"0","unendedDroppedEvents":"0"}
```

Draft native attribute encoding: key/type/value entries, with type names
`string`, `bool`, `int64`, `uint64`, `float64`, `duration`, and `strings`. Signed,
unsigned, and duration values use decimal strings; duration units are nanoseconds.
String arrays preserve element order. Span updates overwrite existing keys;
events append in record order; status updates replace previous status. Type tags
are necessary because a string containing digits is not an integer attribute.
Sort attribute keys within a record for stable serial fixtures; do not sort events
or records behind the caller's back. Drop counts on End are totals for that span.

Use sequence numbers to detect possible missing records in an intact, single-file
capture; consumers must not infer successful delivery from contiguous numbers
alone. A future per-segment or filtered projection is intentionally sparse in the
capture-wide sequence and must declare that scope; gaps there are not by themselves
evidence of loss. Records rejected before emission may only appear in drop counts
or Flush/Shutdown errors. A successfully read trace_end is
an admission-completion marker, not proof that the host operation succeeded or
that no data was dropped.

### Minimum reconstruction contract

A reader processes complete records in file/sequence order, not after sorting
them by wall timestamp. The serialization sequence is local to the provider;
it records whichever concurrent operation won the admission gate, not a cross-goroutine
semantic total order promised by instrumentation.

1. **span_start:** establish identity, root identity, optional parent, name, scope,
   and start wall/elapsed times. Mark the span live immediately. A missing End
   does not prevent reconstructing its place in the tree or inspecting its events.
2. **span_update:** apply unique attribute-key overwrites and explicit status
   replacement to that span. Status defaults to Unset; no End is required to see
   a delivered error/status update in a hung or killed program.
3. **event:** append the event to its identified span in accepted record order.
   RecordError is an ordinary typed event under these rules. Keep event elapsed
   times for placement; same timestamp does not imply simultaneous execution.
4. **span_end:** close that span once and use durationNano, not wall subtraction.
   Do not close its children. Reject/flag updates or duplicate Ends after it.
5. **trace_end:** record a completion claim only after the root ended and every
   observed live span ended. Missing starts, unexplained gaps, invalid lifecycles,
   or reported losses qualify that claim; distinguish completion from fidelity.
6. **loss_summary:** replace the capture's previous loss snapshot with the latest
   successfully read valid summary. Required quoted uint64 counters are
   `rejectedStarts`, `droppedAttributes`, `droppedEvents`, `unendedSpans`,
   `unendedDroppedAttributes`, and `unendedDroppedEvents`. The first three are
   lifetime totals; the last three describe logically live spans at that sequence.
   Never sum snapshots or add totals to overlapping span_end counts. Live subtotals
   can decrease at logical End (even if its record fails delivery); lifetime totals
   must not decrease. Missing summaries mean unknown accounting, not zero. A torn
   final summary does not replace a previous valid snapshot. Missing/null fields,
   out-of-range counters, subtotals exceeding totals, nonzero live drops with zero
   live spans, or regressing totals are corruption diagnostics. The summary has
   no IDs and is neither completion nor durability evidence. Totals are current
   through its sequence only; later records do not retroactively update it.
7. **EOF/live tail:** starts without Ends remain unfinished; traces without
   trace_end are open or completion-unconfirmed. If all span Ends are present but
   the final trace_end was torn, distinguish inferred quiescence from a recorded
   completion marker. EOF does not reveal when or why a process stopped.

Required partial order: parent start before admitted child start; span start
before its accepted updates/events/End; every admitted End before trace_end.
There is no sibling ordering, global timestamp monotonicity, CPU-execution order,
or parent-before-child End requirement. Nonmonotonic capture-relative timestamps
across unrelated records are possible when an operation samples time before
waiting for the admission gate; preserve that measurement rather than rewrite it
to make file order look chronological.

Readers can incrementally expose started spans and emit finalized spans while
releasing their detailed state. Completed parent metadata may be retained only
as needed for still-live descendants or placed in an external index. A whole
hierarchy/event history may grow with the artifact, so a future analyser must
choose streaming callbacks, disk indexing, or explicit memory quotas rather than
claiming constant memory for arbitrary full-tree analysis. Reader quotas must be
independent of trusted writer defaults; a shared artifact is untrusted input.

### Buffering, durability, and crash recovery

Default file-sink behavior: serialize a whole bounded line first, write without a
user-space buffered writer, and do **not** fsync every record. Pre-encoding prevents
serialization failure from emitting half a JSON value, but OS writes can still
partially succeed. Handle short writes, and permanently stop this file stream
after an uncertain/partial write so later data cannot be appended to a torn line.
No automatic retry that could duplicate a record.

The file sink is identical under both processors: SyncProcessor invokes it on
the submitting path; AsyncProcessor would invoke it on its writer. The sink owns
no worker/queue. This bounds transient encoding memory to a record, not a trace.
A sink-local buffer would
reduce syscalls but expand the loss window and require a periodic flush worker;
defer that tradeoff until benchmarks justify it. OS page-cache buffering remains.

Processor Flush is a delivery barrier. File Flush applies to already delivered
records and makes data visible through
the underlying write path, not power-loss durable. Offer an explicit
`WithSyncOnFlush(true)` option to call `File.Sync` during Flush and final Shutdown;
do not confuse Close with Sync. Sync is subject to OS/filesystem guarantees and
does not by itself promise crash-durable directory entry creation on every system.

Readers should preserve all validated earlier records. On an offline artifact,
a final non-newline record may be accepted if it is complete valid JSON; a torn
final record is reported and ignored. While tailing a live file, retain that tail
until a newline arrives rather than parsing a prefix. Malformed **interior** lines,
bad headers, missing required fields, or duplicate/end-before-start lifecycle
records are corruption warnings/errors, not silently skipped clean data. A future
salvage mode must visibly mark uncertainty. Limit reader record sizes and avoid
the default 64-KiB `bufio.Scanner` token limit as an accidental schema constraint.

On process crash, successfully written starts and events can identify unfinished
operations. Ended children may have an open/missing root. Missing End/trace_end
means incomplete/unknown, not zero duration, OK, or fabricated termination at EOF.
No guarantee exists for SIGKILL, disk failure, power loss, or records not yet
written. An unflushed completed-span OTLP exporter would recover less information
in this particular case; this is the main reason to choose the journal.

A hang must be diagnosable by reading/tailing the growing artifact without Flush,
Shutdown, or an ended root. Abrupt kill/early exit must leave previous complete
records usable. This is **crash usefulness**, not unconditional crash durability:
a blocked write, unwritten operation, or power-loss page-cache loss cannot be
recovered. Do not add periodic heartbeat events or infer host liveness merely
because a capture stops growing.

## 10. Flush, shutdown, failure, and process exit

| Operation | Proposed guarantee |
| --- | --- |
| End | Winning call ends recording and submits its completion record to Processor before returning; no universal sink-delivery/durability guarantee and no implicit child join |
| Flush(ctx) | Submits a cumulative capture loss snapshot under the admission gate before the processor delivery/Sink.Flush barrier; returns sticky output/loss errors; does not end active spans or close output |
| Shutdown(ctx) | Closes provider admission, submits the final cumulative loss snapshot before processor closure, reports remaining active spans as incomplete, drains/flushes/closes; never waits for forgotten spans to End |

Normal applications stop/join their work and End spans **before** Shutdown. A
shutdown does not synthesize successful Ends or fake durations for live work.
If active spans remain, return an identifiable unended-span error/count, leave
their starts without ends in the journal, and make subsequent span methods no-ops.
No full registry/snapshot of all live spans is required to report a count.

Snapshots are emitted even with zero losses, and each explicit Flush writes a
replacement even if counters are unchanged. There are no automatic checkpoints.
Rejected capacity, ID-generation, and processor admission failures count once per
Start attempt; disabled/post-shutdown Starts do not count. Bounded attribute/event
drop totals include initial, ended, and unfinished spans. Per-span End counts are
preserved, not added to these aggregate totals. A canceled entry emits nothing;
Shutdown canceled after final submission resumes cleanup without resubmission.
Summary acceptance/delivery failures use existing sticky errors and authoritative
Flush/Shutdown results; cleanup still runs. See the accepted loss-summary decision
for the full field, failure, and replay contract and test-only decoding fixtures.

Lifecycle state is running -> closing -> closed, not reversible. Concurrent calls
coordinate one cleanup attempt; successful repeated Shutdown returns the same
terminal result. If cancellation prevents cleanup from completing, remain closing
and allow a later Shutdown with a fresh context to finish, without reopening
admission. If closing a file actually succeeds, report closed even if its preceding
flush failed; retries must not write to a closed file. Sink cleanup should still
attempt Close after write/flush/Sync failure and combine errors.

Flush/Shutdown check cancellation while waiting to enter barriers and between
stages. Ordinary regular-file Write/Sync/Close are not context-cancelable system
calls; an already executing call can exceed the context deadline. Document this
limitation instead of claiming a hard shutdown timeout or spawning an abandoned
goroutine for every write. Custom sinks must document their own cancellation
capabilities. Async barrier waits can honor context cancellation without claiming
an in-flight file syscall stopped. Application work contexts are never used to
cancel normal End submissions. Canceling a Flush wait must not silently cancel
the queued records it was meant to observe.

Failure policy:

| Failure | Behavior |
| --- | --- |
| File open/create/header or provider initialization | Returned setup error; caller explicitly decides whether tracing is optional |
| ID generation/configuration invalidity | Setup error where possible; runtime admission failure with diagnostic, never invalid recorded IDs |
| Invalid/oversized attribute or event | Drop according to bounds, count loss, preserve lifecycle records |
| Future async queue/byte capacity exhausted | Reject new admissions or drop eligible updates/events without waiting; preserve reserved lifecycle records and report loss |
| Encode, short write, disk full, write failure | Latch first operational error; stop output rather than corrupt/retry the stream; release normal span bookkeeping |
| Flush/Sync/Close failure | Return error, retain earlier failure context, still attempt owned-resource cleanup |
| Unended spans/admission loss | Report incomplete capture in Flush/Shutdown results as applicable |

Propose a pipeline-local optional `WithErrorHandler(func(error))`, not global
logging. Wire a shared diagnostic reporter at provider/processor construction so
immediate Process errors and future background writer errors reach the same
policy without adding callback machinery to Span APIs. SyncProcessor returns
per-call errors for the provider to report after unlocking; AsyncProcessor must
also report failures that occur when no further instrumentation call is made.
Implemented v0.1 policy: `WithErrorHandler(func(error))` is an optional Provider
option, with **no implicit stderr output** (nil means no notifications). Notify
the first latched failure once and additional terminal cleanup failures once;
repeated Process/Flush/Shutdown failures do not storm. Context cancellation and
ordinary bounded-data drops are not terminal notifications. The first failure
remains sticky, including ID-generation errors, and Flush/Shutdown remain the
authoritative returned-error path regardless of whether a callback is configured.

Callbacks run synchronously after admission and processor/sink locks are released.
Pending notifications are claimed before invoking user code, allowing callback
reentry into Start, Flush, or Shutdown without deadlock or duplicate reporting.
Handlers must be concurrency-safe: independent operations may invoke callbacks
concurrently; notification order across goroutines is not promised. There is no
worker, callback lock, automatic tracing of the handler, or panic recovery for
host callback code. Future AsyncProcessor background-failure wiring remains a
separate implementation gate.

End/AddEvent/SetAttributes retain defer-friendly void signatures; strict
applications handle setup, callbacks, and final errors themselves. Trail never
calls os.Exit, panics because of a sink error, or installs signal handlers. A
custom sink/callback panic is host-supplied code, not a promised sandbox.
`os.Exit` bypasses defers: a CLI should run work and Shutdown in a function that
returns before choosing its exit code. Hosts handle signals and use fresh bounded
cleanup contexts. Normal deferred End during panic does not automatically capture
the panic value or a stack trace.

## 11. Privacy and redaction

Only caller-supplied span names, scope names, attributes, events, statuses, and
explicit RecordError messages are recorded beyond diagnostic IDs/timing/format
metadata. No automatic environment variables, request headers, authorization,
tokens, local filesystem paths, usernames, command arguments, URLs, stack traces,
hostname, resource detectors, or build-path discovery.

Explicit error recording can leak paths, URLs, or secrets in err.Error(). Names,
scope labels, status descriptions, and output filenames can also be sensitive;
redacting attribute keys alone is not sufficient. Hosts should pass classified
safe labels/errors, review artifacts before bug-report sharing, and control file
access/retention. IDs allow cross-record correlation and are not anonymization.

Do not add a core redaction callback in v0.1. A host can sanitize at instrumentation
boundaries or later supply a sink decorator; a decorator must cover every payload
field, apply before bytes reach a buffer/file, and preserve required IDs/lifecycle.
That is a documented extension point, not a claim that v0.1 ships redaction.

## 12. Disabled performance and deterministic testing

Disabled Start returns the original context and a zero/non-recording Span. Do not
call time.Now, generate IDs, allocate a context wrapper/span state, apply options,
take a sink lock, format an error, or construct a record. Disabled mutations do
nothing. SpanFromContext may still expose an existing enabled ancestor when
disabled instrumentation has preserved the context; use the returned span to
annotate the operation just started.

**Acceptance targets, not measured promises:**

- Bare disabled Start+End and context lookup: **0 allocs/op, 0 B/op**. Measured
  on the reference machine (Ryzen 7 5800X3D, Windows, Go 1.27.1): bare
  disabled Start+End ≈ 2.2 ns/op, global GetTracer().Start/End ≈ 3.5–4.3 ns/op,
  and disabled span mutations without options ≈ 3.7–4.2 ns/op, all at zero
  allocations. Constructing option values such as WithAttributes on a disabled
  call costs one small constant allocation for the option closure and argument
  slice; copying attribute data is still deferred until recording.
- Scalar attribute constructors: no heap allocation by themselves. For the
  common static-string/scalar WithAttributes call, aim for 0 allocs/op end-to-end;
  verify compiler escapes before committing to that guarantee.
- Strings/option variadics can still allocate in caller code. Function arguments
  are evaluated before Start; expensive computation is not magically deferred.
- Disabled span operations must not call err.Error(). Disabled Starts must leave
  cancel/deadline behavior and context identity unchanged.
- Aim for bare disabled Start+End <= 50 ns/op on a recorded reference amd64
  machine/toolchain, and publish arm64 comparisons. This is a benchmark budget,
  not a platform-independent service guarantee.

Use Tracer.Enabled before expensive initial attributes and Span.IsRecording
before expensive event attributes. Those checks are advisory across End/Shutdown,
not reservations. Avoid closure-heavy option builders and public lazy-value APIs
until profiling proves they are needed. No-op mode must create no output file or
worker: the host decides whether to construct a file sink at all.

### Enabled-path baseline

Measured on Windows/amd64, AMD Ryzen 7 5800X3D (8 cores, benchmark suffix `-16`),
Go 1.27.1 resolved through mise, with no concurrent verification process:

```text
mise exec -- go test -run="^$" -bench="BenchmarkEnabled" -benchmem -benchtime=200ms -count=3 .
```

Ranges across three runs (not platform-independent budgets):

| SyncProcessor sink / operation | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| In-memory discard / root Start + End | 491–505 | 512 | 7 |
| In-memory discard / AddEvent | 95–115 | 144 | 2 |
| In-memory discard / SetAttributes | 227–249 | 304 | 2 |
| Real file / root Start + End | 11,803–13,447 | 2,465 | 20 |
| Real file / AddEvent | 3,621–3,689 | 888 | 7 |
| Real file / SetAttributes | 4,027–4,233 | 1,296 | 7 |

The in-memory sink consumes without retaining or encoding, isolating core,
ID/clock, admission, and SyncProcessor overhead. The real sink uses a fresh
temporary file, exclusive creation, unbuffered JSONL writes, and default
Sync-on-Flush **off**. File results include encoding and OS writes/page-cache
effects, not power-loss durability, sustained-storage throughput, or isolated
encoder cost; disk and host load can change these numbers substantially.
Setup/capture header and cleanup are outside timing. Each root Start/End emits
three lifecycle records; AddEvent emits one unadorned event on an already live
span; SetAttributes updates two existing scalar keys on a live span. Benchmark
calibration warms those keys; this is steady-state update cost, not first-key
insertion. Handler configuration is nil, with no error-path work in timing.

Implementation tests cover: zero handles, root/child IDs, canceled
contexts, copied spans, duplicate End, simultaneous End/update/child Start,
foreign/ended parents, root-before-child completion, multiple roots, limits,
slice mutation, non-finite floats, deterministic clocks/IDs, short/error writes,
callback reentrancy rules, Flush barriers, canceled/retried Shutdown, and crash
tails. Add race-detector runs and allocation benchmarks. Use sorted attribute
fixtures; test concurrent partial-order constraints instead of demanding one
byte-for-byte goroutine interleaving. Coverage continues to evolve with the
remaining design gates; passing checks are not a claim of exhaustive coverage.

## 13. Future trace-local rotation: constraints only

Required invariant:

```text
root A admitted -> bind trace A to segment 1
segment 1 crosses rotate_size
root B admitted -> bind trace B to segment 2
A's descendants/updates/events/ends -> still segment 1
last admitted span of A ends -> trace_end -> segment 1 may finish draining
```

The pipeline must bind a trace when its root span_start is **admitted**, before
returning a recording root or accepting its children. SyncProcessor can naturally
do this during its root submission. A future AsyncProcessor must not defer binding
to dequeue time: rotation could happen while the root record waits in RAM.
Every later record includes TraceID/RootSpanID; queued records retain the already
chosen route. Routing tokens/generations are processing metadata, not processor-
specific journal fields or SpanOptions.

A future rotating sink/pipeline needs a cheap metadata-only root-binding seam
at processor admission, separate from delayed file writes. Its exact optional
capability/configuration is not implemented or frozen here; preserve the common
three-method sink for ordinary sync/async delivery rather than forcing a rotating
factory into v0.1. Admission binding must not require synchronous filesystem I/O
in async mode: prepare segments on the writer, and reject a root or explicitly
defer rotation with a diagnostic if the next segment is not ready. Do not silently
choose a different segment later. Approximate-size reservations must account for
queued bytes at admission, not just bytes already written.

Release the trace route only after trace_end and its preceding retained records
have been **delivered**. Runtime completion or queued trace_end alone cannot close
a segment. This separates logical trace completion from processor draining and
keeps admission/rotation races out of Tracer/Span APIs. Settle the binding seam
before implementing either async rotation combination; neither is in v0.1.

Threshold evaluation affects **new roots only**. Use `rotate_size`, not `max_size`:
count approximately encoded bytes, including buffered/reserved bytes if buffering
is later introduced. On the next root admission after crossing the target, make
a fresh segment current. Existing traces can overrun the target arbitrarily;
file size is not capped. A single long-lived trace may keep writing its original
segment indefinitely. If no new root starts, crossing the threshold need not
create an empty file.

Future segment state must separate current/sealed-draining/fully-closed:

| State | New roots | Already pinned trace records | Compression/retention deletion |
| --- | --- | --- | --- |
| Current | Accepted | Accepted | Forbidden |
| Sealed / non-current, draining | Rejected for this segment | Accepted | Forbidden |
| Fully closed | Rejected | Impossible | Eligible under policy |

Here "sealed" means sealed to **new root assignments**, not sealed to all writes;
prefer the explicit term sealed-draining in APIs/state names to avoid ambiguity.
Track active assigned traces **and** in-flight/pending writes before closing a
non-current segment.
Ending the root alone is insufficient. Closed segments only may be compressed,
deleted, or counted as reclaimable retention candidates. Stable generation-based
filenames and explicit state must work on Windows too; do not rely on Unix
rename/unlink behavior for open files. Repeat format and capture clock metadata
in each independently readable segment, identify its generation and sparse
capture-sequence scope, and define metadata deduplication when segments are merged.

Age rotation, retention count/age, compression, generation numbers, restart
recovery, and safe cleanup remain unimplemented. Many long-lived traces can pin
many draining files. A future descriptor/segment cap needs an explicit policy,
such as declining new capture or pausing rotation with a diagnostic; it must not
silently reroute descendants, delete draining files, or promise a hard disk cap.
Do not use an off-the-shelf size-rotating io.Writer that splits active traces.

## 14. Future analysis and runtime profiling

The journal supplies IDs, parents, monotonic-relative intervals, events, status,
and incompleteness/loss information. It does not precompute indexes or summaries.
Future `trail view` / `trail summary` commands are concepts, not present commands.

Possible replay/analysis:

- Root-operation duration versus full observed trace extent, explicitly distinct.
- Slowest spans and hierarchy, including missing parents and unfinished spans.
- Self-time as parent interval minus the **union of clipped child intervals**,
  not parent duration minus the sum of overlapping child durations.
- Event timeline, errors versus final status, overlapping sibling work.
- Lower-bound/unknown results for partial capture rather than invented totals.

Elapsed wall time is not CPU time. A large self-time interval does not prove a
goroutine was blocked; wait-heavy work requires explicit application spans/events
or correlation with runtime profiling. Starting/ending on different goroutines
is supported, but Trail does not automatically know threads or goroutine IDs.
Chrome/Perfetto conversion must use async lanes or separate logical tracks and
flows for arbitrary overlap, not put every span on one fictitious nested thread
[S6, S7]. OTLP conversion must reconstruct per-span updates/events, potentially
requiring an offline index or bounded active-span state; streaming capture does
not imply every whole-trace analysis is constant-memory.

Trail complements `runtime/trace` (scheduler, GC, goroutine blocking, tasks and
regions), CPU pprof, heap profiles, block profiles, and mutex profiles [S13, S14].
Go runtime regions must begin/end in the same goroutine, unlike proposed Trail
spans. Applications may enable both independently. No runtime/trace bridge or
profiling controls are needed in the core, and shared contexts can carry both
systems' private keys without conflating their lifetimes.

## 15. Precedents, dependencies, and package layout

### Lessons, not implementations to clone

| Precedent | Keep | Deliberately omit/change |
| --- | --- | --- |
| OTel Go API/SDK [S8, S10, S11] | Context/Start/End ergonomics, scope labels, typed attributes, explicit RecordError/status, processing boundary, concurrency and ownership discipline | Global setup, composable processor/sampler stack, resource detection, propagation, links/kinds for v0.1, sticky OK status, ended-parent continuation; nonblocking End depends on processor choice |
| OTLP traces [S1, S3] | ID widths, tree/span/event/status concepts, precision-safe encoding lessons | Transport/retry/backpressure architecture and compliance claims |
| Go runtime/trace [S13] | Local artifact and defer-friendly task annotation, explicit completion | Runtime event capture, process-global recorder, goroutine-nested region restrictions |
| Chrome / Perfetto [S6, S7] | Existing local analysis targets, streamed packets, explicit asynchronous tracks | Core dependence on thread lanes, native binary schema/clock/interning complexity |
| Reflow localtrace [S15] | Real Go local span/event/context precedent; separation of tracer and local output | Application-specific kinds/IDs, accumulated event array rewritten on Flush, suppressed write errors and unused distributed methods |
| fgtrace [S16] | Demonstrates useful local Perfetto-readable artifacts | Experimental sampled whole-goroutine stacks, stop-the-world cost, automatic capture; it is not an explicit span library |
| pkg/profile [S17] | Simple locally owned capture and explicit Stop-before-exit ergonomics | Profiling runtime controls and process shutdown hooks in a tracing library |

These sources establish useful design precedents, not a claim that any lightweight
library is a current drop-in Trail implementation or has measured Trail-like
overhead. Avoid expanding the project into a general profiling abstraction.

### Dependency evaluation

Prefer standard-library runtime code: context, time, crypto/rand, encoding/hex,
encoding/json, sync/atomic, errors, io, and os inside the file package. No tracing
SDK, Collector pdata, gRPC, rotating writer, or semantic conventions dependency.

`go.opentelemetry.io/proto/otlp` is stable-model generated code, but its inspected
module manifest includes protobuf, gRPC, grpc-gateway, and indirect x/*/genproto
requirements [S18]. That **module graph is not the same as linked binary weight**:
importing just trace/common/resource model packages primarily uses protobuf and
descriptors, not automatically the collector-service gRPC implementation. Adding
collector-service packages can pull in substantially more runtime code. Exact
binary-size, allocation, and build-time deltas must be measured on a small host
before adding it; no invented MB estimates here.

Ordinary protojson defaults encode bytes as base64 and enum names as strings;
OTLP needs adaptations. Ordinary encoding/json on generated structs is also not
an OTLP encoder (inspect their snake_case tags and byte fields) [S1, S12, S19].
Reusing generated messages avoids schema transcription but does not remove
encoding-rule work. Collector pdata includes purpose-built OTLP JSON behavior,
but is a larger abstraction/dependency choice than this library needs.

Recommendation: **no external production dependencies for v0.1**. If an OTLP
converter later becomes worthwhile, first compare a tiny explicit supported-field
JSON mapper against generated model packages plus an OTLP-aware adapter. Prefer
generated models for broad OTLP support; prefer a documented limited mapper only
with pinned cross-decoder fixtures and range/encoding tests. A test-only external
conformance module/tool can validate compatibility without making the tracing
core import an SDK. Do not copy upstream generated code or internal packages
into Trail for convenience.

### Initial layout proposal

```text
go.lostcrafters.com/trail     common API, context, IDs, typed records, provider/processor
go.lostcrafters.com/trail/file local journal serialization and file ownership
internal/...                 only proven shared implementation details
docs/design.md              this proposal
```

No separate api/sdk/trace/attribute/status modules, exporter registry, public
internal runtime package, viewer command, or rotating package yet. Tests can define
small fake sinks without shipping a production memory package. Keep IO out of the
root API's ownership assumptions, even if concrete record types live there.

## 16. Compatibility and smallest useful v0.1

Go API and file format version independently. v0.x APIs may change with explicit
release notes; stabilize the common surface before v1. Concrete handles avoid
forcing third-party tracer/span implementations to absorb interface additions.
Treat Processor, Sink, and Record as real extension contracts once released;
processor configuration may evolve without changing Tracer/Span methods. Adding lifecycle
record types may affect custom sinks and is not automatically harmless.

The native header carries an integer format version distinct from module semver.
Within a version, permit documented additive optional fields; readers ignore
unknown fields but must not silently ignore unknown lifecycle record types that
could change completeness. Reject unsupported major format versions. Do not
repurpose a field, type tag, or record meaning within a version. Formal JSON
schema, required fields, replay rules, limits, fixtures, and compatibility tests
must be written **before** declaring the first format stable. Draft fixtures in
this document impose no pre-release migration obligations.

Future actual OTLP output should guarantee a named supported field/type mapping,
encoding rules, pinned reference schema/tested decoders, and recorded losses. Its
payload compatibility is separate from Trail's journal version; API familiarity
does not make Trail an OTel API implementation.

Recommended v0.1 acceptance slice, now implemented:

1. Explicit provider/tracer and safe disabled handles; optional SetDefaultProvider
   registration and GetTracer(scope) lookup, with snapshot provider binding and
   explicit lifecycle ownership; no package-level Start;
   local context and root/child
   Start/End, WithNewRoot, typed IDs and deterministic injection.
2. Bounded typed attributes, events, independent error/status recording.
3. Explicit Processor boundary with only SyncProcessor implemented, plus the
   shared sink contract and exclusive-create JSONL file sink; multiple concurrent
   traces, early records, incomplete-span recovery semantics.
4. Clear error reporting, Flush/Shutdown barriers, optional Sync-on-Flush, safe
   resource cleanup, explicit unended-span reporting via IncompleteError, and
   cumulative capture loss snapshots at explicit Flush/Shutdown checkpoints.
5. A processor-parameterized semantic conformance suite, concurrency/failure
   tests, race runs, and disabled allocation benchmarks. Journals are readable
   with generic JSON tooling; no dedicated viewer is required.

Exclude rotation, retention, compression, resume-existing-file mode, async
workers, remote export, distributed propagation, OTLP converter, automatic slog
integration, sampling, metrics, and monitoring. The architecture documents their
constraints. Processor is an explicit architectural boundary; async queue,
worker, reservation machinery, and rotation capabilities are not implemented.

For the repository, use `mise run fmt` then `mise run check`; `mise run
test:race` adds race-detector runs and `go test -bench` runs the disabled-path
allocation benchmarks.

## 17. Open review questions and implementation gates

1. **Journal schema freeze:** the clarified direction favors early crash-useful
   records and semantic OTLP alignment, not completed-span-only storage. The v1
   header, record types, decimal-string numerics, typed attribute values, and
   status objects and Flush/Shutdown loss-summary records are implemented as the
   design fixture specifies; a freeze review against real captures remains before
   calling the schema stable. Direct OTLP JSONL remains a researched alternative,
   not the selected native format.
2. **Ended-parent boundary:** accept new-root behavior, or require OTel-like late
   parenting? The latter needs an explicit trace lease/close concept before
   promising both safe draining closure and indefinitely reusable contexts.
3. **Processor delivery budget:** the enabled SyncProcessor baseline is recorded
   in section 12; validate real workloads before tuning it. AsyncProcessor remains
   a first-class configuration option;
   before implementing it, settle byte/record reservations, eligible drops,
   control barriers, error notification, and writer lifetime under cancellation.
4. **Public pipeline surface:** settle Processor/Record names, ownership, shared
   diagnostic wiring, and sink evolution rules using the conformance suite. Keep
   root-admission routing separate from dequeue for future rotation; avoid a
   generic plugin system or extra Tracer/Span methods.
5. **Limits and diagnostics:** validate the proposed byte/count limits and
   truncation behavior. The default-policy gate is closed: optional
   WithErrorHandler plus authoritative Flush/Shutdown errors, no implicit stderr.
   Loss-summary schema is implemented with cumulative replacement checkpoints;
   see the linked accepted decision. Real-capture limits tuning remains open.
6. **Unsigned/duration conversion:** confirm whether future OTLP conversion
   should use the specified type-loss mapping or require explicit user policy.
7. **Durability defaults:** implemented as recommended: no per-record Sync and
   opt-in Sync-on-Flush; verify expectations for shared bug-report artifacts
   and directory durability with real workloads.
8. **Minimum Go version:** scaffold pins Go 1.27.1 with go.mod 1.27.0; this design
   does not justify raising or lowering that minimum. Revisit intentionally for
   library consumers rather than changing toolchain config during research.

Before implementation: settle 1/2, write a minimal replay schema and invariants,
then use deterministic fixtures/fake sinks to exercise the ownership and race
contracts. Do not use those exercises as a reason to implement rotation or a viewer.

## Sources

Primary sources inspected 2026-10-05. Tagged OTLP schema links are pinned;
`main`/`master` links are research snapshots and may change. No binary
interoperability or performance measurements were performed in this pass.

- **S1:** [OTLP specification 1.11.0, stability and JSON encoding](https://opentelemetry.io/docs/specs/otlp/).
- **S2:** [OpenTelemetry Protocol File Exporter, Development status and JSONL representation](https://opentelemetry.io/docs/specs/otel/protocol/file-exporter/).
- **S3:** [OTLP v1.11.0 trace.proto](https://github.com/open-telemetry/opentelemetry-proto/blob/v1.11.0/opentelemetry/proto/trace/v1/trace.proto) and [common.proto / AnyValue](https://github.com/open-telemetry/opentelemetry-proto/blob/v1.11.0/opentelemetry/proto/common/v1/common.proto).
- **S4:** [Collector fileexporter README: stability, JSON lines, rotation, append, buffering](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/exporter/fileexporter/README.md).
- **S5:** [Collector OTLP JSON File Receiver README: stability and input contract](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/receiver/otlpjsonfilereceiver/README.md).
- **S6:** [Perfetto external trace formats, Chrome support and nesting limitations](https://perfetto.dev/docs/getting-started/other-formats) and [original Chrome Trace Event format](https://docs.google.com/document/d/1CvAClvFfyA5R-PhYUmn5OOQtYMH4h6I0nSsKchNAySU/preview) (linked by Perfetto; conclusions here use the inspected Perfetto documentation).
- **S7:** [Perfetto programmatic conversion, async tracks and flows](https://perfetto.dev/docs/getting-started/converting) and [synthetic TrackEvent reference, packet streaming](https://perfetto.dev/docs/reference/synthetic-track-event).
- **S8:** [OTel tracing API specification: contexts, End, status, concurrency, no-op](https://opentelemetry.io/docs/specs/otel/trace/api/).
- **S9:** [OTel Go stdouttrace implementation: JSON-encoded SDK span stubs](https://github.com/open-telemetry/opentelemetry-go/blob/main/exporters/stdout/stdouttrace/trace.go).
- **S10:** [OTel Go Span API](https://github.com/open-telemetry/opentelemetry-go/blob/main/trace/span.go), [Tracer API](https://github.com/open-telemetry/opentelemetry-go/blob/main/trace/tracer.go), [no-op implementation](https://github.com/open-telemetry/opentelemetry-go/blob/main/trace/noop/noop.go), and [SDK provider lifecycle/options](https://github.com/open-telemetry/opentelemetry-go/blob/main/sdk/trace/provider.go).
- **S11:** [OTel Go SDK SpanExporter interface](https://github.com/open-telemetry/opentelemetry-go/blob/main/sdk/trace/span_exporter.go).
- **S12:** [ProtoJSON representation and compatibility](https://protobuf.dev/programming-guides/json/) and [JSON Lines framing](https://jsonlines.org/).
- **S13:** [Go runtime/trace: activities, tasks, regions and lifecycle](https://pkg.go.dev/runtime/trace).
- **S14:** [Go diagnostics overview: profiling and execution tracing](https://go.dev/doc/diagnostics).
- **S15:** [Reflow localtrace source](https://github.com/grailbio/reflow/blob/master/trace/localtrace/localtracer.go) and [context/span event API](https://github.com/grailbio/reflow/blob/master/trace/trace.go).
- **S16:** [fgtrace README: experimental goroutine sampling and local Chrome/Perfetto output](https://github.com/felixge/fgtrace/blob/main/README.md).
- **S17:** [pkg/profile README: local profiling Start/Stop ownership](https://github.com/pkg/profile/blob/master/README.md).
- **S18:** [OTLP generated Go module requirements](https://github.com/open-telemetry/opentelemetry-proto-go/blob/main/otlp/go.mod).
- **S19:** [OTLP generated Go trace messages, imports and JSON tags](https://github.com/open-telemetry/opentelemetry-proto-go/blob/main/otlp/trace/v1/trace.pb.go).
- **S20:** [Go time monotonic-clock contract, serialization and suspend caveats](https://pkg.go.dev/time#hdr-Monotonic_Clocks), inspected in [Go 1.27.1 time source](https://github.com/golang/go/blob/go1.27.1/src/time/time.go).
- **S21:** [OTel Go global tracer forwarding implementation and preconfiguration behavior](https://github.com/open-telemetry/opentelemetry-go/blob/main/internal/global/trace.go).
