package trail

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// ErrQueueFull is a nonterminal rejection: a bounded async budget is exhausted.
// Provider counts rejected admissions or dropped payloads without notifying its
// error handler. Direct Process callers must account for their own rejected data.
var ErrQueueFull = errors.New("trail: async queue capacity exhausted")

// AsyncOption configures an explicitly selected AsyncProcessor.
type AsyncOption func(*asyncConfig)

type asyncConfig struct {
	records    int
	bytes      int
	batchBytes int
}

// WithMaxQueuedRecords sets the total entry/obligation budget, including two
// checkpoint credits and the in-flight record. The minimum is six.
func WithMaxQueuedRecords(n int) AsyncOption {
	return func(c *asyncConfig) { c.records = n }
}

// WithMaxQueuedBytes sets the conservative owned-payload/obligation byte budget,
// including 512 checkpoint bytes. Structural overhead is separately bounded by
// the record limit. The minimum is 2048; see docs/async-processor.md.
func WithMaxQueuedBytes(n int) AsyncOption {
	return func(c *asyncConfig) { c.bytes = n }
}

// WithMaxBatchBytes enables FIFO grouping for a BatchSink. n bounds conservative
// charged payload bytes in each group; oversized individual records are delivered
// alone. Zero disables batching (the default); positive limits are 256..65536.
// Only already available entries are grouped; no timer/capacity wait is added.
// Credits stay occupied until the complete sink call returns. A sink without
// BatchSink support retains the ordinary single-record path.
func WithMaxBatchBytes(n int) AsyncOption {
	return func(c *asyncConfig) { c.batchBytes = n }
}

const asyncRecordCharge = 256

type asyncSpanKey struct {
	trace TraceID
	span  SpanID
}

type asyncEntry struct {
	record  Record
	bytes   int
	barrier *asyncBarrier
}

type asyncBarrier struct {
	ctx      context.Context
	shutdown bool
	done     chan struct{}
	err      error // published by closing done
}

// AsyncProcessor retains owned bounded records and delivers them in order with
// one writer. Process never waits for sink I/O, free space, or writer progress.
// Accepted starts reserve their future End (and root trace-end) capacity.
// Successful construction owns sink; callers must eventually call Shutdown.
// The zero value is not initialized. See docs/async-processor.md for accounting,
// cancellation, direct-use lifecycle requirements, and crash-window semantics.
type AsyncProcessor struct {
	mu          sync.Mutex
	sink        Sink
	queue       []asyncEntry
	head        int
	count       int
	recordLimit int
	byteLimit   int
	usedRecords int
	usedBytes   int
	ends        map[asyncSpanKey]struct{}
	traces      map[TraceID]struct{}
	wake        chan struct{}
	control     chan struct{}
	done        chan struct{}
	closing     bool
	closed      bool
	err         error
	termErr     error
	provider    *providerState
	batchSink   BatchSink
	batchBytes  int
}

// NewAsyncProcessor constructs an explicit async pipeline. Defaults are 16384
// record credits and 16 MiB charged bytes. Failure leaves sink ownership with
// the caller; success starts one owned writer and takes sink ownership.
func NewAsyncProcessor(sink Sink, opts ...AsyncOption) (*AsyncProcessor, error) {
	if sink == nil {
		return nil, errors.New("trail: AsyncProcessor requires a non-nil sink")
	}
	value := reflect.ValueOf(sink)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil, errors.New("trail: AsyncProcessor requires a non-nil sink")
		}
	}
	cfg := asyncConfig{records: 16384, bytes: 16 << 20}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.records < 6 || cfg.bytes < 2048 {
		return nil, errors.New("trail: async limits require at least 6 records and 2048 bytes")
	}
	if cfg.batchBytes != 0 && (cfg.batchBytes < asyncRecordCharge || cfg.batchBytes > 64<<10) {
		return nil, errors.New("trail: batch bytes require zero or 256..65536")
	}
	p := &AsyncProcessor{
		sink: sink, queue: make([]asyncEntry, cfg.records),
		recordLimit: cfg.records - 2, byteLimit: cfg.bytes - 2*asyncRecordCharge,
		ends: make(map[asyncSpanKey]struct{}), traces: make(map[TraceID]struct{}),
		wake: make(chan struct{}, 1), control: make(chan struct{}, 1), done: make(chan struct{}),
	}
	if cfg.batchBytes > 0 {
		p.batchSink, _ = sink.(BatchSink)
		p.batchBytes = cfg.batchBytes
	}
	p.control <- struct{}{}
	go p.run()
	return p, nil
}

func (p *AsyncProcessor) bind(ps *providerState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != nil || p.closing {
		return errors.New("trail: AsyncProcessor already bound or closing")
	}
	p.provider = ps
	return nil
}

// Process borrows record only until return. Success owns a bounded deep copy.
// End/trace-end submissions must match a previously accepted start reservation.
func (p *AsyncProcessor) Process(record Record) error {
	size, err := asyncRecordSize(record)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admitLocked(record, size); err != nil {
		return err
	}
	p.enqueueLocked(asyncEntry{record: cloneAsyncRecord(record), bytes: size})
	return nil
}

// admitLocked is shared by public borrowed records and the private typed
// producer handoff. It checks and commits exactly the same lifecycle credits;
// callers keep the lock through ownership preparation and FIFO insertion.
func (p *AsyncProcessor) admitLocked(record Record, size int) error {
	if p.closed {
		return ErrProcessorShutdown
	}
	if p.err != nil {
		return p.err
	}
	if p.closing {
		return ErrProcessorShutdown
	}
	credits, bytes := 1, size
	var endKey asyncSpanKey
	switch r := record.(type) {
	case SpanStart:
		endKey = asyncSpanKey{r.TraceID, r.SpanID}
		if _, exists := p.ends[endKey]; exists {
			return errors.New("trail: duplicate async span start")
		}
		credits++
		bytes += asyncRecordCharge
		if !r.ParentSpanID.IsValid() {
			if _, exists := p.traces[r.TraceID]; exists {
				return errors.New("trail: duplicate async trace start")
			}
			credits++
			bytes += asyncRecordCharge
		} else if _, exists := p.traces[r.TraceID]; !exists {
			return errors.New("trail: async child without admitted trace")
		}
	case SpanEnd:
		endKey = asyncSpanKey{r.TraceID, r.SpanID}
		if _, exists := p.ends[endKey]; !exists {
			return errors.New("trail: async span end without reservation")
		}
		credits, bytes = 0, 0
	case TraceEnd:
		if _, exists := p.traces[r.TraceID]; !exists {
			return errors.New("trail: async trace end without reservation")
		}
		credits, bytes = 0, 0
	}
	if credits > p.recordLimit-p.usedRecords || bytes > p.byteLimit-p.usedBytes {
		return ErrQueueFull
	}
	switch r := record.(type) {
	case SpanStart:
		p.ends[endKey] = struct{}{}
		if !r.ParentSpanID.IsValid() {
			p.traces[r.TraceID] = struct{}{}
		}
	case SpanEnd:
		delete(p.ends, endKey)
	case TraceEnd:
		delete(p.traces, r.TraceID)
	}
	p.usedRecords += credits
	p.usedBytes += bytes
	return nil
}

func (p *AsyncProcessor) enqueueLocked(e asyncEntry) {
	p.queue[(p.head+p.count)%len(p.queue)] = e
	p.count++
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// acquireControl bounds outstanding markers even if their callers cancel.
func (p *AsyncProcessor) acquireControl(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return ErrProcessorShutdown
	case <-p.control:
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			p.control <- struct{}{}
			return ErrProcessorShutdown
		}
		return nil
	}
}

// checkpoint is called with a control permit and, for provider snapshots, its
// admission gate. Snapshot and barrier are a single FIFO transaction.
func (p *AsyncProcessor) checkpoint(ctx context.Context, summary *LossSummary, shutdown bool) *asyncBarrier {
	b := &asyncBarrier{ctx: ctx, shutdown: shutdown, done: make(chan struct{})}
	p.mu.Lock()
	if p.closed {
		b.err = p.termErr
		close(b.done)
		p.control <- struct{}{}
	} else {
		var record Record
		if summary != nil {
			record = *summary
		}
		p.enqueueLocked(asyncEntry{record: record, barrier: b})
	}
	p.mu.Unlock()
	return b
}

func waitAsyncBarrier(ctx context.Context, b *asyncBarrier) error {
	select {
	case <-b.done:
		return b.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Flush establishes an acceptance high-water mark, then waits for delivery and
// Sink.Flush. Canceling the wait never discards accepted records.
func (p *AsyncProcessor) Flush(ctx context.Context) error {
	if err := p.acquireControl(ctx); err != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.closed {
			return ErrProcessorShutdown
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		p.control <- struct{}{}
		return err
	}
	return waitAsyncBarrier(ctx, p.checkpoint(ctx, nil, false))
}

func (p *AsyncProcessor) stopAdmission() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closing {
		p.closing = true
		promises := len(p.ends) + len(p.traces)
		p.usedRecords -= promises
		p.usedBytes -= promises * asyncRecordCharge
		clear(p.ends)
		clear(p.traces)
	}
}

// Shutdown stops acceptance, drains accepted entries, and flushes/closes on the
// writer. Cancellation leaves closing resumable; a successful cleanup joins the
// writer. It cannot interrupt an in-flight WriteRecord.
func (p *AsyncProcessor) Shutdown(ctx context.Context) error {
	p.stopAdmission()
	if err := p.acquireControl(ctx); err != nil {
		p.mu.Lock()
		closed, terminal := p.closed, p.termErr
		p.mu.Unlock()
		if closed {
			<-p.done
			return terminal
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		p.control <- struct{}{}
		return err
	}
	err := waitAsyncBarrier(ctx, p.checkpoint(ctx, nil, true))
	p.mu.Lock()
	closed, terminal := p.closed, p.termErr
	p.mu.Unlock()
	if closed {
		<-p.done
		return terminal
	}
	return err
}

func (p *AsyncProcessor) latchLocked(err error) {
	if p.err == nil && err != nil {
		p.err = err
		if p.provider != nil && p.provider.errorHandler != nil {
			// At most one terminal-notification goroutine. Never make the writer
			// wait for host code or for a callback's reentrant Shutdown.
			go p.provider.backgroundError(err)
		}
	}
}

func (p *AsyncProcessor) run() {
	defer close(p.done)
	var records []Record
	if p.batchSink != nil {
		records = make([]Record, 0, min(p.recordLimit, p.batchBytes/asyncRecordCharge))
	}
	for {
		p.mu.Lock()
		if p.count == 0 {
			p.mu.Unlock()
			<-p.wake
			continue
		}
		e := p.queue[p.head]
		p.queue[p.head] = asyncEntry{}
		p.head = (p.head + 1) % len(p.queue)
		p.count--
		sticky := p.err
		consumed, bytes := 1, e.bytes
		if p.batchSink != nil && sticky == nil && e.barrier == nil {
			records = append(records, e.record)
			for p.count > 0 {
				next := p.queue[p.head]
				if next.barrier != nil || next.bytes > p.batchBytes-bytes {
					break
				}
				records = append(records, next.record)
				bytes += next.bytes
				consumed++
				p.queue[p.head] = asyncEntry{}
				p.head = (p.head + 1) % len(p.queue)
				p.count--
			}
		}
		p.mu.Unlock()
		if e.record != nil && sticky == nil {
			var err error
			if len(records) > 1 {
				err = p.batchSink.WriteRecords(records)
			} else {
				err = p.sink.WriteRecord(e.record)
			}
			if err != nil {
				p.mu.Lock()
				p.latchLocked(fmt.Errorf("trail: write record: %w", err))
				p.mu.Unlock()
			}
		}
		clear(records)
		records = records[:0]
		if e.barrier == nil {
			p.mu.Lock()
			p.usedRecords -= consumed
			p.usedBytes -= bytes
			p.mu.Unlock()
			continue
		}
		if p.finishBarrier(e.barrier) {
			return
		}
	}
}

func (p *AsyncProcessor) finishBarrier(b *asyncBarrier) bool {
	p.mu.Lock()
	sticky := p.err
	p.mu.Unlock()
	var errs []error
	canceled := false
	if sticky == nil && b.ctx.Err() == nil {
		if err := p.sink.Flush(b.ctx); err != nil {
			if isCancellation(err) {
				canceled = true
				errs = append(errs, err)
			} else {
				p.mu.Lock()
				p.latchLocked(fmt.Errorf("trail: flush: %w", err))
				p.mu.Unlock()
			}
		}
	}
	closed := false
	if err := b.ctx.Err(); err != nil {
		errs = append(errs, err)
	} else if b.shutdown && !canceled {
		err := p.sink.Shutdown(b.ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("trail: shut down sink: %w", err))
		}
		closed = !isCancellation(err)
	}
	p.mu.Lock()
	if p.err != nil {
		errs = append(errs, p.err)
	}
	b.err = errors.Join(errs...)
	if closed {
		p.closed = true
		p.termErr = b.err
	}
	close(b.done)
	p.control <- struct{}{}
	p.mu.Unlock()
	return closed
}

// asyncRecordSize bounds the owned copy before allocating it. Conservative
// charges cover record/attribute headers; string bytes are charged separately.
func asyncRecordSize(record Record) (int, error) {
	size := asyncRecordCharge
	var attrs []Attribute
	switch r := record.(type) {
	case CaptureStart, SpanEnd, TraceEnd, LossSummary:
	case SpanStart:
		if len(r.Name) > maxNameBytes || len(r.Scope) > maxNameBytes {
			return 0, errors.New("trail: oversized async span start")
		}
		size += len(r.Name) + len(r.Scope)
	case SpanUpdate:
		attrs = r.Attributes
		if r.Status != nil {
			if len(r.Status.Description) > maxStringValueBytes {
				return 0, errors.New("trail: oversized async status")
			}
			size += len(r.Status.Description)
		}
	case Event:
		attrs = r.Attributes
		if len(r.Name) > maxNameBytes {
			return 0, errors.New("trail: oversized async event name")
		}
		size += len(r.Name)
	default:
		return 0, errors.New("trail: unsupported async record")
	}
	if len(attrs) > maxAttributesPerSpan {
		return 0, errors.New("trail: oversized async attributes")
	}
	for _, a := range attrs {
		if len(a.strs) > maxStringsElements {
			return 0, errors.New("trail: oversized async string array")
		}
		if len(a.key) > maxKeyBytes || len(a.str) > maxStringValueBytes {
			return 0, errors.New("trail: oversized async attribute")
		}
		size += 128 + len(a.key) + len(a.str) + 16*len(a.strs)
		for _, v := range a.strs {
			if len(v) > maxStringValueBytes {
				return 0, errors.New("trail: oversized async string element")
			}
			size += len(v)
		}
		if size > maxEncodedRecordBytes {
			return 0, ErrQueueFull
		}
	}
	if size > maxEncodedRecordBytes {
		return 0, ErrQueueFull
	}
	return size, nil
}

func cloneAsyncAttributes(attrs []Attribute, resolved bool) []Attribute {
	if len(attrs) == 0 {
		return nil
	}
	owned := attrs
	// Charging uses length. A partially filled resolution batch may have spare
	// capacity after deduplication/drops, so compact it rather than retain more
	// backing storage than the ordinary byte budget accounts for.
	if !resolved || cap(attrs) != len(attrs) {
		owned = make([]Attribute, len(attrs))
	}
	for i, a := range attrs {
		a.key = strings.Clone(a.key)
		a.str = strings.Clone(a.str)
		if a.strs != nil {
			values := a.strs
			if !resolved || cap(values) != len(values) {
				values = make([]string, len(a.strs))
			}
			for j, v := range a.strs {
				values[j] = strings.Clone(v)
			}
			a.strs = values
		}
		owned[i] = a
	}
	return owned
}

func cloneAsyncRecord(record Record) Record {
	switch r := record.(type) {
	case SpanStart:
		r.Name, r.Scope = strings.Clone(r.Name), strings.Clone(r.Scope)
		return r
	case SpanUpdate:
		r.Attributes = cloneAsyncAttributes(r.Attributes, false)
		if r.Status != nil {
			status := *r.Status
			status.Description = strings.Clone(status.Description)
			r.Status = &status
		}
		return r
	case Event:
		r.Name = strings.Clone(r.Name)
		r.Attributes = cloneAsyncAttributes(r.Attributes, false)
		return r
	default:
		return record
	}
}
