package trail

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrProcessorShutdown is returned by Processor operations after Shutdown
// has completed.
var ErrProcessorShutdown = errors.New("trail: processor is shut down")

// SyncProcessor delivers each accepted record to its sink on the submitting
// path, in order, without a queue or background writer. Callers of Process
// observe sink latency; Flush and Shutdown are barriers over preceding calls.
//
// The first output failure is latched: later Process calls return the same
// error without touching the sink again, so a damaged stream is not extended.
// Shutdown still attempts sink cleanup and combines errors.
type SyncProcessor struct {
	mu      sync.Mutex
	sink    Sink
	err     error
	termErr error
	closed  bool
}

// NewSyncProcessor returns a processor that delivers to sink. On success it
// owns sink; a non-nil error leaves ownership with the caller. Passing a nil
// sink, including an interface holding a typed nil, is invalid.
func NewSyncProcessor(sink Sink) (*SyncProcessor, error) {
	if sink == nil {
		return nil, errors.New("trail: SyncProcessor requires a non-nil sink")
	}
	return &SyncProcessor{sink: sink}, nil
}

// Process writes record to the sink synchronously and returns the result.
// After the first write error, it returns the latched error without writing.
// It returns ErrProcessorShutdown once Shutdown has completed.
func (p *SyncProcessor) Process(record Record) error {
	if record == nil {
		return errors.New("trail: Process requires a non-nil record")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrProcessorShutdown
	}
	if p.err != nil {
		return p.err
	}
	if err := p.sink.WriteRecord(record); err != nil {
		p.err = fmt.Errorf("trail: write record: %w", err)
		return p.err
	}
	return nil
}

// Flush calls the sink's Flush, which makes preceding delivered records
// visible. A latched write failure is returned without calling the sink.
func (p *SyncProcessor) Flush(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrProcessorShutdown
	}
	if p.err != nil {
		return p.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.sink.Flush(ctx); err != nil {
		p.err = fmt.Errorf("trail: flush: %w", err)
		return p.err
	}
	return nil
}

// Shutdown flushes and closes the owned sink, then remains closed. It drains
// nothing because delivery is synchronous. Cleanup is attempted even after
// write or flush failures, and errors are combined. Repeated calls return
// the same terminal result. A canceled context leaves shutdown resumable:
// the processor is not closed and a later call can finish it.
func (p *SyncProcessor) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.termErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var errs []error
	if p.err == nil {
		if err := p.sink.Flush(ctx); err != nil {
			p.err = fmt.Errorf("trail: flush: %w", err)
			errs = append(errs, p.err)
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(append(errs, err)...)
	}
	if err := p.sink.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("trail: shut down sink: %w", err))
	}
	if p.err != nil {
		errs = append(errs, p.err)
	}
	p.termErr = errors.Join(errs...)
	p.closed = true
	return p.termErr
}
