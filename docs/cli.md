# Trail CLI

Build with `mise exec -- go build ./cmd/trail`, or install with
`go install go.lostcrafters.com/trail/cmd/trail@latest` after publication.
The CLI reads explicit native Trail journal files. It does not accept stdin,
expand globs, discover captures, provide a TUI/query DSL, or expose replay as a
public Go API. Quote paths containing spaces. Treat capture contents as sensitive.

## Commands

```text
trail inspect <file> [flags]
trail trace <file> <trace-id> [flags]
trail span <file> <span-id> [flags]
trail query <file> [flags]
trail stats <file> [flags]
trail export <file> [flags]
```

Parsing and generated help use `urfave/cli/v3`. Run `trail --help`,
`trail inspect --help`, or `trail help trace` without a capture file.
Command flags may appear before, between, or after positional arguments; use `--`
to terminate flag parsing for a path that begins with a dash. IDs must be full-length
hexadecimal: 32 characters for traces, 16 for spans. Uppercase input is accepted.

```sh
trail inspect capture.trail.jsonl --top 5
trail trace capture.trail.jsonl 11111111111111111111111111111111 --max-depth 4
trail query capture.trail.jsonl --status error --scope myapp --format ndjson
trail query capture.trail.jsonl --min-duration 25ms --attribute retries=int:2 --top 10
trail query capture.trail.jsonl --attribute enabled=bool:false --attribute label=str:worker
trail stats capture.trail.jsonl --format json
trail export capture.trail.jsonl --format toon
```

Common flags: `--format text|compact|json|ndjson|toon` (default text),
`--best-effort`, `--strict`, `--fail-on-error`. Text accepts
`--color never|auto|always` (default never) and `--local-time`. Auto color is
enabled only for a character-device output file. Machine formats never emit ANSI
or convert wall times to local time. Unsupported flag/command/format combinations
fail rather than silently ignoring presentation settings.

Inspect accepts `--top` (default 10 displayed summaries), `--since`, `--until`.
Time bounds are inclusive Go durations measured from capture start, e.g. `2s`.
They restrict slowest-trace, error-span, and incomplete-trace **summaries**;
capture totals, loss checkpoints and observed elapsed remain whole-file values.
Text labels these separately as `CAPTURE TOTALS (whole file)` and `DETAIL WINDOW`.
Machine inspect output adds `detail_window` with explicit whole-capture totals
semantics and optional exact `since_nanos` / `until_nanos` bounds.
Slowest traces require an authoritative trace lifetime. Unresolved roots cannot
be placed in a root-start time window. Default summaries are bounded, while the
machine diagnostics list is complete.

Text trace accepts `--max-depth` (default 10; root depth 0), `--max-spans`
(default 500), `--no-events`, and `--sort start|duration`. Limits truncate only
presentation, never replay validation or machine output. Concurrent siblings are
not represented as sequential execution. Status markers are Trail's ERR, OK or
unset `-`; `UNENDED` identifies missing ends. Orphans preserve the recorded parent.

Stats reports whole-capture counts, ended-span status counts, authoritative ended
duration count/min/max/exact total, and separately labelled loss sources. It does
not infer application-specific success, HTTP status, or event severity.

## Query predicates and ordering

`--mode spans|traces` defaults to spans. All predicates use AND:

| Flag | Meaning |
| --- | --- |
| `--status unset|ok|error` | Last observed explicit Trail status; no status is unset |
| `--scope PREFIX` | Case-sensitive scope prefix |
| `--name TEXT` | Case-insensitive name substring |
| `--min-duration DURATION` | Inclusive authoritative span-duration threshold; unknown is excluded |
| `--attribute KEY=TYPE:VALUE` | Repeatable typed equality |
| `--incomplete` | Span has no accepted end; evaluated at EOF |
| `--trace-id ID`, `--span-id ID` | Full-ID equality |
| `--since DURATION`, `--until DURATION` | Inclusive span-start elapsed bounds |
| `--top N` | Longest N matching spans or matching traces; unknown duration ranks last |
| `--sort start|duration` | Global result ordering; duration is descending |

Attribute prefixes are `int:`, `uint:`, `duration:`, `float:`, `bool:`, `str:`.
Durations use Go syntax. Float predicates must be finite. Types are significant:
`count=int:1` does not match a string or uint attribute. String-array predicates
are not provided in v1. Status/attributes use the final observed span state.
Trace-mode predicates select a trace if at least one of its spans matches; they
do not reinterpret `--min-duration` as a trace-lifetime predicate.

Buffered span results default to start order, then ID for ties. Trace results
default to ID order. Top results rank by span duration or trace lifetime, then ID.
Incremental span NDJSON instead follows trace completion order, with start/sequence
order inside each trace. An explicit `--sort start` or `--sort duration` buffers
results until EOF. `--top` keeps bounded selected result state until EOF.

## Formats and schema

JSON is the canonical structured baseline. JSON and TOON use a shared logical
projection with `schema_version: "trail.cli.v1"`. TOON changes serialization, not
the native journal or replay semantics. Text is for people; compact is a deliberately
lossy tab-separated shell summary (`SPAN`, `TRACE`, `CAPTURE` lines). Compact quotes
names/paths and uses `?` for unknown duration. It omits attributes, events, detailed
loss accounting and diagnostics; use machine formats for complete structured data.

Exact int64/uint64 values, sequence numbers, nanoseconds and counts are decimal
**strings**, even when small. Journal header version remains a JSON number.
Typed attributes preserve their discriminator: string, bool, float64 and string
arrays keep their native shapes; integer and duration attribute values are strings.
Empty arrays are arrays, not null. Attributes are sorted by key. Journal sequence,
not wall time or map iteration order, determines reconstruction.

Optional status, wall end and authoritative duration keys are omitted when unknown.
An absent duration is not zero; `"0"` means an authoritative zero. Drop counters on
unended spans are omitted because no final per-span accounting was recorded.

Observed elapsed is the largest structurally validated record elapsed value,
**not capture end**. A decoded record rejected for lifecycle damage under
best-effort still contributes its observed timestamp and sequence position.
Trace lifetime is `trace_end.elapsedNano - root span_start.elapsedNano`; it exists
only with a valid trace end. Root span duration is a separate authoritative field.
The root can end before its children. Missing parents are not rewritten as roots.

### NDJSON

Every line is a complete JSON object with a `type` and schema version; no outer array.

* Export emits `span` objects with embedded events, once each; then `trace` metadata
  without a duplicate spans array. Completed traces are emitted at accepted
  `trace_end`; remaining traces are emitted at EOF in trace-ID order.
* Export ends with `capture_summary`: counts, latest valid loss checkpoint,
  derived losses, replay diagnostics, and any torn-tail condition. Intermediate
  cumulative loss checkpoints are validated but not emitted as duplicate snapshots.
* Incremental span queries emit matching `span` objects, then `query_summary` with
  match count and replay/tail diagnostics. Nonmatching spans are not emitted.
* Buffered span/trace queries emit one result per line, then `query_summary` with
  diagnostics. Single trace/span commands emit one object. Inspect/stats emit one
  `capture_summary` at EOF.

Events are embedded **only**, never also emitted separately. Trace-query objects
contain their spans intentionally: each matching trace is one query result.
Diagnostics/tail information is embedded in summaries rather than emitted a second
time as independent items. Consumers must check the process exit status: a later
fatal/strict error can follow already-written records. There is no transactional
rollback of stdout.

Span output waits for trace completion because the canonical span includes child
IDs and parent resolution: a child can be recorded after its parent ends. Emitting
that canonical object at span end would falsely imply final ancestry. Incomplete
span predicates require EOF. This is incremental **trace-finalized** output, not
an eager span-end notification stream.

## Retention and limits

| Command/mode | Retention |
| --- | --- |
| inspect | Aggregates, bounded top/error/incomplete summaries; attribute/event payloads discarded after each validated record; trace lifecycle metadata until trace end |
| stats | Aggregates; attribute/event payloads discarded after each validated record; trace lifecycle metadata until trace end |
| query spans, NDJSON, no top/explicit sort | Trace-finalized incremental emission; completed trace detail released |
| query spans, other cases | Matching projections retained; top-N bounded when requested; nonmatching completed detail released |
| query traces | Matching trace content retained; nonmatches released; top-N bounded when requested |
| trace | Two-pass validation/targeted reconstruction; only target trace payload in second pass |
| span | Two-pass; target span payload plus target-trace ancestry/lifecycle metadata |
| export NDJSON | Trace-finalized incremental emission; completed detail released |
| export compact | Trace-finalized incremental emission; completed detail released |
| export JSON / TOON / text | Full reconstruction, O(total capture content) |

**Incremental does not mean constant-memory.** Exact detection of records after a
released trace's end requires one completion-ID tombstone per completed trace.
Active/incomplete traces require per-span lifecycle/ancestry metadata, and formats
that emit full spans keep their payload until trace completion. Diagnostics also
remain available until EOF. Thus a single long-lived trace, many never-completed
traces, or arbitrarily many diagnostics can grow memory. The CLI does not hide
these costs or claim a fixed memory bound. JSON/TOON full exports explicitly retain
the complete reconstructed document. Targeted commands require a stable file
between passes; a streamed SHA-256 byte comparison rejects changes rather than
returning a mixed snapshot, without retaining either file's bytes.

Completion tombstones use a set of full 16-byte trace IDs, not reconstructed spans
or an approximate filter. Their key bytes alone are 1,600,000 for 100k traces and
8,000,000 for 500k traces, **excluding Go map overhead**. The boolean value was
removed, but arbitrary trace IDs cannot be dropped or shortened without weakening
exact late-record detection. Thus released paths have O(total completed trace IDs)
metadata plus O(active/incomplete trace history), not O(total completed payload).
Even aggregate commands retain ended spans' ancestry, start order, names, statuses
and root timing inside a still-active trace. Diagnostics are a separate unbounded
input-dependent cost. The tombstone set is released when replay returns.

## Loss, corruption and exit status

Missing loss summary means **unknown**, not known zero. A recorded all-zero summary
is known zero. The latest valid cumulative summary replaces earlier checkpoints;
lifetime totals cannot regress, while live subtotals may decrease. Invalid summaries
do not replace the last valid summary under best-effort recovery. Derived ended-span
losses are shown separately and never added to checkpoint totals.

Default replay rejects malformed interior records and illegal lifecycle operations.
Unresolved parents and sequence gaps are diagnostics, not silently repaired state.
`--best-effort` skips eligible bad records with visible diagnostics; it cannot recover
an unsupported/bad header, missing initial capture start, non-increasing sequence,
I/O errors, or an oversized line. Lines are limited to 1 MiB.
`--strict` rejects replay diagnostics, including those generated during best-effort.
Query and targeted commands, and compact output, also report replay/tail warnings
on stderr so recovery is visible even when a query has no results or lossy output
omits diagnostic fields. Machine stdout remains ANSI-free.

A complete final record without a newline is accepted. Incomplete JSON at EOF is
a recoverable torn tail, recorded separately; other malformed final JSON is an
error. Strict rejects replay diagnostics, not the separately classified torn tail.

| Exit | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Usage / invalid flags, IDs or predicates |
| 2 | Bad/unreadable capture, strict rejection or output error |
| 3 | Trace/span not found |
| 4 | No query matches |
| 5 | `--fail-on-error` and recorded Trail Error status |

## TOON measurements

TOON is retained for readable structure and measured savings on some homogeneous
attribute/diagnostic tables, **not** as a universal smaller/faster format. Nested
arrays often prevent tabular encoding. No tokenizer was measured; there are no
token-count or agent-efficiency claims.

Representative synthetic projections, Windows/amd64, Ryzen 7 5800X3D, pinned tools,
200 ms benchmark calibration. Bytes include the terminating newline. Timing and
allocation measurements are indicative, not performance guarantees.

| Sample | JSON bytes | TOON bytes | TOON delta | JSON ns/op | TOON ns/op | JSON allocs/op | TOON allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| inspect | 705 | 745 | +5.67% | 7,996 | 19,267 | 87 | 206 |
| trace 5 | 4,173 | 4,729 | +13.32% | 40,938 | 108,382 | 405 | 1,028 |
| trace 50 | 39,273 | 44,754 | +13.96% | 394,500 | 1,139,897 | 3,780 | 9,420 |
| trace 500 | 390,273 | 444,654 | +13.93% | 3,946,013 | 13,546,590 | 37,534 | 92,987 |
| query 100 | 52,309 | 57,564 | +10.05% | 498,166 | 2,173,669 | 4,608 | 10,996 |
| query 1000 | 523,009 | 574,762 | +9.90% | 5,255,426 | 14,926,407 | 46,010 | 108,810 |
| attribute-heavy | 38,513 | 32,332 | -16.05% | 389,902 | 1,031,655 | 4,150 | 8,907 |
| event-heavy | 56,373 | 70,762 | +25.52% | 619,702 | 2,112,180 | 5,790 | 14,391 |
| diagnostics/loss-heavy | 2,779 | 2,411 | -13.24% | 26,609 | 50,860 | 272 | 649 |

Reproduce: `mise exec -- go test ./internal/render -run '^$' -bench '^BenchmarkSerialization$' -benchmem`.
Readable TOON/text fixtures in `internal/render/testdata`, canonical fingerprints,
and shape tests use actual `toon-go` output;
JSON/TOON logical-equivalence tests cover exact extremes and optional presence.
Staged CLI tests prove two rounds of NDJSON output before EOF. The standard suite
tests 100,000 completed spans; set `TRAIL_LARGE_500K=1` to run the extended 500,000-span
release test. Set `TRAIL_COMMAND_LARGE=1` for command-level retention instrumentation:
500k spans for aggregate/released/query/targeted paths, and 10k for full JSON/TOON
exports. Full exports deliberately use a smaller fixture to avoid conflating model
retention with an impractical encoder memory stress test. Instrumentation counts
span/attribute/event objects and completion IDs, not OS peak RSS or map overhead.
The suite also tests 2,048 ended children in one still-active trace and bounded
top-10 selection across 100k candidate results. These tests verify detail release,
not constant-memory retention. Regenerate readable fixtures only deliberately with
`TRAIL_UPDATE_GOLDENS=1`; ordinary tests compare checked-in bytes without rewriting.
