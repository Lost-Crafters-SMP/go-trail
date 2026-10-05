# AsyncProcessor contract

Design closed before implementation. Processor selection is explicit; SyncProcessor
remains supported. No Provider/Tracer/Span public signatures change.

## Configuration and ownership

`NewAsyncProcessor(sink Sink, opts ...AsyncOption) (*AsyncProcessor, error)` owns
the sink and starts one writer on success. `WithMaxQueuedRecords(int)` defaults to
16384; `WithMaxQueuedBytes(int)` defaults to 16 MiB. Minimums are six records and
2048 charged bytes. Constructor failure leaves ownership with the caller. A
processor serves one provider/capture; direct Processor use is also possible but
must supply valid lifecycle ordering. Every successful constructor needs Shutdown,
including when subsequent Provider construction fails.

One FIFO preserves retained acceptance order. Queue limits include queued and
in-flight entries **and promises for not-yet-submitted lifecycle records**. Two
record credits and 512 bytes are permanently withheld for one checkpoint/control
operation. There are no secondary record/retry/callback queues, timers, batching,
or synchronous overflow fallback.

Byte accounting is conservative owned-payload accounting, not encoded JSON bytes:
256 bytes per record, 128 bytes per attribute, 16 bytes per string-array element,
plus all copied string bytes. Structural ring/map/channel overhead is separately
bounded by the record limit, not charged as payload. Per-record owned charge is
at most 64 KiB; normal input bounds still apply. Copies own attribute slices,
string-array slices/elements, status objects/descriptions, names/scopes, and keys/
values. No caller-owned mutable payload survives successful Process return.

## Reservations and overflow

Accepting span_start atomically reserves its own charge, one 256-byte span_end
credit, and, for a root, one 256-byte trace_end credit. Reject the whole admission
with `ErrQueueFull` if either budget is insufficient, before exposing a recording
Span/context. Bounded identity maps track promises; no per-span goroutines exist.
End/trace_end convert promises into FIFO entries without requiring free ordinary
capacity. Entry credits remain charged until WriteRecord returns (not at logical
End or dequeue). Unsubmitted promises remain until submission or closing; closing
releases promises for unfinished work without emitting invented Ends.

Events and attribute/status updates may be rejected whole with `ErrQueueFull`.
Provider handles this as nonterminal bounded loss, not a pipeline failure/callback:
one dropped event per rejected Event; one dropped attribute per attribute in a
rejected SpanUpdate; one dropped status update if that update contains status.
Initial attribute-update rejection follows the same policy. No loss is silent.
Rejected starts increment the existing cumulative admission counter and produce
IncompleteError at barriers. Retained records are never evicted/reordered.

The approved shared schema extension adds `droppedStatusUpdates` on span_end and
loss_summary, and `unendedDroppedStatusUpdates` on loss_summary. Sync writes these
as zero unless an explicit bounded status drop occurs. Existing attribute/event
semantics and cumulative replacement replay remain unchanged. Loss totals overlap
per-span Ends; never add them. A trace_end claims quiescence, not lossless fidelity.
Direct Processor callers receive rejection errors and own capture-loss accounting;
the automatic per-span/capture counters belong to Provider.

## Barriers and cancellation

Provider submits a fresh loss snapshot and its Flush marker atomically under its
admission gate. It waits for the bounded control permit **outside** that gate, and
releases the gate before waiting for delivery. Subsequent records go behind the
marker and cannot delay its high-water mark. Only the writer invokes Sink.Flush.
Direct Processor.Flush inserts a marker, not a fabricated provider snapshot.
Control-operation waiters do not retain copies of queued record data.

Flush/Shutdown may wait; ordinary Process never waits for I/O, space, or writer
progress. Short mutex/copy work is permitted. Context cancellation before enqueue
emits nothing; after enqueue it cancels the caller's wait, not accepted records.
An enqueued marker may skip canceled Sink.Flush, but earlier accepted records and
the checkpoint still drain. At most one marker is outstanding; a new caller can
wait for its permit with its own context. Canceled Flush does not poison output.

Provider Shutdown closes admission first and checkpoints final loss before drain/
flush/closure. Processor Shutdown also stops Process, even on canceled entry. It
does not wait for application spans to End. Cancellation leaves closing resumable;
after final-summary acceptance retries enqueue cleanup only, not a duplicate final
summary. Accepted FIFO entries continue to drain while closing. A fresh Shutdown
finishes sink cleanup and joins the writer. Closed operations return terminal
results without new records. Sink calls never overlap or run under acceptance
locks. An in-flight WriteRecord cannot be context-canceled by this interface:
the writer remains owned until the sink returns and shutdown completes. A custom
sink returning cancellation from cleanup must remain resumable, not already closed.

## Failures and callbacks

First non-cancellation write/flush error is sticky. The writer stops invoking
WriteRecord after failure, releases failed/undeliverable entries and credits,
and remains available for barriers/cleanup. Process no longer reports successful
deliverability. Barriers return the sticky error; Shutdown still attempts sink
closure and joins cleanup errors. Failed output is explicitly uncertain, not an
overflow drop attributed to caller data. No uncertain write retry occurs.

Provider WithErrorHandler handles background errors even without another tracing
call. One bounded, once-launched terminal-notification goroutine bridges the writer
to the existing provider latch, so a callback may reenter Flush/Shutdown without
waiting on itself. It holds no writer/admission locks and there is no callback
queue. Immediate acceptance failures still return to Provider for reporting after
unlock; races claim the same sticky notification once. Additional cleanup errors
are reported by the existing Shutdown path. No stderr or panic recovery is added.
Shutdown joins the writer, not arbitrary host callback code; as with current
handlers, the host must ensure its callback returns. Waiting for callbacks would
deadlock callback reentry into Shutdown.

## Completion, crash window, and verification

Logical trace completion, processor acceptance, RAM retention, sink delivery,
Flush completion, and sink durability are distinct. Accepted RAM records can be
lost on crash before delivery. The writer drains starts/events continuously, not
at trace completion. Sync narrows this crash window; Sync-on-Flush remains the
sink's opt-in durability policy. No deterministic inter-goroutine schedule is
promised. Admission-time routing remains the required seam for future rotation;
rotation itself is out of scope.

Run shared conformance for both processors after repairing any fake-sink access
that races a background writer. Add stalled/failing-sink tests for both budgets,
reservations, drops/checkpoints, copying, barrier high-water marks, cancellation,
reentry, and joined worker exit. Benchmarks use bounded no-loss producer windows
with drains outside the producer timer, and measure drain separately; do not
present fast rejection/no-op loops as accepted-record throughput.
