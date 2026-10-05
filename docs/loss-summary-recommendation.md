# Loss-summary decision (accepted and implemented)

**Use capture-scoped cumulative replacement snapshots at both Flush and Shutdown.**
This replaces the original Shutdown-only recommendation: explicit Flush checkpoints
preserve current loss evidence even if a process hangs or crashes before Shutdown.
There are no timers, periodic emissions, automatic checkpoints, or per-drop records.

## Emission and failure contract

- Every enabled Provider Flush submits `loss_summary` under the admission gate
  before invoking the processor's Flush barrier. Successful Flush includes delivery
  of that summary under the processor contract, plus the sink's normal flush policy.
- Shutdown closes admission, submits the final snapshot before processor drain/flush/
  closure, and does not invent span Ends. Repeated completed Shutdown (or Flush after
  closure) returns the terminal result without emitting another record.
- A context canceled before submission emits nothing. If Shutdown cleanup is
  canceled after submission, retry resumes cleanup without resubmitting the final
  snapshot, even if the first attempt failed. Uncertain writes are never retried.
- Emit even all-zero snapshots, including an unchanged snapshot on repeated Flush.
  This avoids suppression state and permits an explicit known-zero checkpoint.
  Absence of a summary means **unknown accounting**, not zero losses.
- Submission errors are latched and returned through Flush/Shutdown. Cleanup still
  runs, and existing error-handler notification rules apply. A summary cannot
  promise persistence on a broken stream; returned errors remain authoritative.

## Journal-v1 schema

`loss_summary` belongs to the preceding `capture_start` in the single-capture file.
It carries no trace/span/root IDs, duration, completion flag, or emission-reason
field. Required fields are `type`, `seq`, `timeUnixNano`, `elapsedNano`, and all six
counters below; zero counters are present. Sequence and counters are quoted uint64
decimal strings. Wall timestamp and monotonic-derived elapsed nanoseconds are
quoted int64 decimal strings, following the ordinary record encoding.

| Field | Meaning |
| --- | --- |
| `rejectedStarts` | Cumulative failed admission attempts: active-span capacity, span/trace ID generation failure, or processor rejection of span_start; counted once per attempt, including children |
| `droppedAttributes` | Cumulative bounded-data attribute drops across all admitted spans, including initial attributes and event attributes |
| `droppedEvents` | Cumulative oversized event drops across all admitted spans |
| `unendedSpans` | Point-in-time count of logically live admitted spans |
| `unendedDroppedAttributes` | Attribute-drop subtotal on those logically live spans |
| `unendedDroppedEvents` | Event-drop subtotal on those logically live spans |

Starts on disabled providers or after admission closes are intentional no-ops,
not rejections. Rejected attempts have no admitted identity; reason categories
are deliberately omitted for the smallest clean contract. Valid truncation and
ignored invalid status codes retain their existing semantics and are not newly
classified as loss. Pipeline errors are not attributed as attribute/event drops;
they remain sticky output errors and sequence/reconstruction diagnostics, not a
fabricated count of missing delivered records. No other existing numeric
capture-loss counter is omitted.

All drops count once in capture lifetime totals. Existing per-span `span_end`
counts remain unchanged and overlap those totals. Live subtotals are released at
logical End, even if End delivery fails; the lifetime totals still include its
drops. Provider accounting uses bounded scalar counters, not a live-span registry.
Unended subtotals must not exceed lifetime totals and must be zero when
`unendedSpans` is zero. No unbounded lists of span IDs are emitted.

## Decoding and replay

Process complete validated records in file/sequence order. Within the capture,
**the latest successfully read valid cumulative summary replaces prior summaries**.
Never sum snapshots and never add their totals to span_end counters. Lifetime
totals must not decrease; live-span counts/subtotals may decrease after Ends. A
regressing lifetime total is a corruption diagnostic, not a delta to reconstruct.
Counters are authoritative through the snapshot's sequence, not necessarily EOF;
later ordinary records can contain work not included in the last snapshot.

Require every field and validate integer ranges and live-subtotal invariants;
missing/null fields do not default to zero. Preserve the last valid snapshot if
the final line is torn/invalid and report the tail diagnostic. Invalid interior
records remain corruption errors, not silently skipped data. Unknown fields may
be ignored under the normal v1 compatibility contract.

A summary is not `trace_end`, proof of clean shutdown, proof of sink delivery of
every End, or a durability claim. Lifecycle replay still identifies unfinished
spans from starts without Ends. A future multi-capture/rotating stream must review
explicit capture identity separately. This completes the loss-record gate within
the still-unreleased journal v1; no version bump is made. Real-capture freeze review
and limits/truncation tuning remain outstanding.

The library still exposes no production reader API. Wire decoding/range validation
and replacement replay are pinned by test-only reference decoding and fixtures;
a general reader/viewer is intentionally outside this change.
