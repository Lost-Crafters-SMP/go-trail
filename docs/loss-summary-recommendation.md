# Loss-summary recommendation (review required)

**Recommend one capture-scoped, cumulative summary at Shutdown for v0.1.**
This is a proposal only: no record type, encoding, or journal version changes
are implemented by this recommendation.

## Shutdown only versus Flush + Shutdown

- **Shutdown only (recommended):** close admission, snapshot loss counters, submit
  a summary before processor cleanup/drain, then shut down. Emit only if loss or
  unfinished work exists. This is a simple final accounting point and does not
  make repeated Flush grow the journal or require periodic reporting machinery.
  Cancellation before submission must permit retry without duplicate submission.
- **Flush + Shutdown:** gives earlier visibility for long-lived processes and
  better crash evidence, but requires snapshot/barrier ordering and duplicate
  suppression. Keep this as a future extension using the same cumulative model,
  not deltas. A process killed before Shutdown will not have the recommended
  final summary; lifecycle records remain its primary crash evidence.

## Contents and scope

Use **capture scope**, associated with the preceding `capture_start` boundary.
Rejected roots have no admitted trace, so a trace-scoped record cannot account
for them honestly. Do not invent trace/span IDs for rejected work. A future
multi-capture or rotating stream must review explicit capture identity separately.

Proposed semantic counters (field names and encoding are not frozen):

- Cumulative **rejected starts**, counted once per failed admission attempt;
  bounded reason categories such as active-span capacity, ID generation, or
  processor rejection. Exclude Starts after Shutdown and deliberate no-op use.
  Include rejected children; failed admission preserves their parent's context
  but still represents missing detail. Existing code counts capacity rejection
  only; other categories would need implementation and tests.
- Cumulative **dropped attributes/events for the entire capture**, including
  both ended and unfinished spans. These are authoritative aggregate totals,
  not additions to per-span `span_end` counts. Avoid double-counting the same
  failed admission or oversized batch through multiple categories.
- A snapshot **unfinished-span count** and **unfinished-span attribute/event
  drops**. They preserve losses whose `span_end` will never be written, without
  synthesizing Ends, durations, or trace completion. Do not emit an unbounded
  list of live spans; their existing starts identify unfinished work during replay.

Maintain bounded provider-level totals and live-span drop subtotals as mutations
occur; release the live subtotals when a span logically ends. No full live-span
registry is required. Logical End does not prove sink delivery: missing End
records still require sequence/replay diagnostics and returned output errors.

## Replay if multiple summaries eventually exist

Each record is a **replacement cumulative snapshot**, ordered by capture sequence.
The latest valid summary in the capture supersedes earlier snapshots; never sum
summary records, and never add their totals to per-span End counters. Lifetime
totals must not decrease; unfinished-work subtotals are point-in-time gauges and
may decrease after work ends. A regressing lifetime total is a diagnostic, not
an invitation to guess a delta. Missing summaries mean accounting is unknown,
not zero. A summary is not a `trace_end` or a clean-shutdown/durability claim.

Summary submission itself can fail, especially on an already broken stream.
Do not retry uncertain writes or promise a persisted loss report: callbacks and
Flush/Shutdown return values remain authoritative for output failures.

**Review requested:** approve Shutdown-only cumulative capture scope and these
counter semantics before specifying required fields or changing the journal.
