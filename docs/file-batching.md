# Optional bounded async file batching

Default processors are unchanged. To opt in, select `NewAsyncProcessor` with
`WithMaxBatchBytes(n)` and a sink implementing `BatchSink`. `file.Sink` implements
the capability; generic sinks fall back to WriteRecord, and SyncProcessor never
uses it. Zero disables batching. Positive targets are 256..65536 conservative
charged payload bytes, not a claim about exact encoded JSON size.

The async writer groups only currently available ordinary FIFO entries up to
that target. A single larger charged record travels alone. Group structure is
bounded too: a reusable Record slice is at most min(ordinary queue limit,
target/256) elements. Records are removed from the ring but their credits and
charged bytes remain occupied until the **entire** output call returns. There
is no change to producer admission, ownership copying, lifecycle reservations,
overflow/loss counters, ordinary/checkpoint budgets or error notification.

Groups never cross a barrier. Its loss-summary record still uses WriteRecord
before Flush/Shutdown. The writer does not wait for another record to fill a
group; a small/isolated group is written immediately. No timer or second worker
is involved. Sink calls remain serialized.

## File output bounds and visibility

WriteRecords encodes one validated line at a time into the same bounded-retention
scratch used by WriteRecord, and copies complete lines into a separate 64 KiB
output buffer. It writes before adding a line that exceeds remaining capacity;
an exact boundary fills the chunk. A line larger than 64 KiB is written alone
after any preceding chunk, without retaining its oversized scratch backing.
Many small records after a large one are handled normally. Direct file callers
remain responsible for the existing record/payload validation contract; this
does not add a maximum-size rejection to the formerly unbounded direct encoder.

The output buffer is empty whenever WriteRecords returns; there is no pending
user-space data across calls. Thus standalone Flush/Shutdown have no new bytes
to force, and the async high-water barrier already ensures completion of every
preceding group. WithSyncOnFlush still issues File.Sync at Flush/final Shutdown.
No durability guarantee changes: ordinary successful writes mean OS visibility,
not power-loss persistence. At most 64 KiB each of scratch and batch backing is
retained per sink; Shutdown releases both. No sync.Pool is needed.

## Failure and cancellation

- Positive short writes without error retain existing writeAll behavior: attempt
  only the remaining suffix. Zero progress stops with an error. Any write error,
  including after a positive prefix, latches immediately; **no retry after error**.
- An attempted group can have an uncertain delivered prefix. The processor
  marks the group consumed only after the call returns, latches the first terminal
  error and never submits later records. Credits for that failed group are then
  released; already accepted remaining records are discarded as on the old path.
  There is no per-record success result or automatic recovery/replay.
- A complete prefix remains valid JSONL; a partial final record is a torn tail.
  A crash can lose the unwritten suffix of a group just as it can lose queued
  records. No bytes after a torn write are appended.
- Encoding/field/unsupported errors do not emit malformed record bytes. A batch
  encoding error can leave an earlier prefix delivered or an unflushed local
  prefix discarded; it returns an error, not a partial success. Public WriteRecord
  retains its original field-error precedence and non-latching validation errors.
- Flush/Shutdown cancellation cannot interrupt an in-flight write. Credits remain
  occupied. A canceled checkpoint never passes later entries, and a later call
  can resume Shutdown after the output call completes. The existing bounded
  checkpoint permit, once-only notification and high-water semantics are unchanged.

Opt-in batching trades a bounded group of uncertain completion and somewhat longer
short queue-lock sections for fewer file output calls. Evidence and target choices
are in `benchmarks/file-drain/README.md`; enabling it is not an automatic default.
