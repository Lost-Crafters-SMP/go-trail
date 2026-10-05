package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"go.lostcrafters.com/trail"
)

// ErrSinkShutdown is returned by Sink operations after Shutdown has
// completed.
var ErrSinkShutdown = errors.New("trail/file: sink is shut down")

// Option configures a Sink. Options can only be created by this package.
type Option func(*sinkConfig)

// sinkConfig collects Option values.
type sinkConfig struct {
	syncOnFlush bool
}

// WithSyncOnFlush controls whether Flush and the final Shutdown call
// File.Sync, asking the OS for power-loss durability where it supports it.
// The default relies on the ordinary unbuffered write path only.
func WithSyncOnFlush(sync bool) Option {
	return func(c *sinkConfig) { c.syncOnFlush = sync }
}

// A Sink appends Trail records to one exclusively created file as complete
// JSONL lines. It implements trail.Sink. Writes are unbuffered and ordered
// by the calling processor; the first failure permanently stops the stream
// so a torn line is never extended.
type Sink struct {
	path string
	f    sinkFile

	mu          sync.Mutex
	syncOnFlush bool
	err         error
	termErr     error
	closed      bool
	scratch     []byte
}

// sinkFile is the file owner's narrow boundary, also allowing precise write
// counting and partial-write failure injection without changing public APIs.
type sinkFile interface {
	io.Writer
	Sync() error
	Close() error
}

const initialScratchBytes = 1024
const maxRetainedScratchBytes = 64 << 10

// Open creates a new file at path and writes the journal header. Creation
// is exclusive: an existing file is never overwritten or truncated. No
// parent directories are created. A non-nil error closes the descriptor and
// leaves nothing to clean up.
func Open(path string, opts ...Option) (*Sink, error) {
	var cfg sinkConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("trail/file: create %s: %w", path, err)
	}
	if _, err := writeAll(f, []byte(headerLine)); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("trail/file: write header: %w", err)
	}
	return &Sink{path: path, f: f, syncOnFlush: cfg.syncOnFlush}, nil
}

// WriteRecord encodes record and appends it as one complete line. Encoding
// happens before any bytes are written, so a malformed record cannot emit a
// partial line. The first write failure latches and permanently stops this
// stream; later calls return the same error.
func (s *Sink) WriteRecord(record trail.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scratch == nil {
		s.scratch = make([]byte, 0, initialScratchBytes)
	}
	line, err := encodeRecordInto(s.scratch[:0], record)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if cap(line) <= maxRetainedScratchBytes {
		s.scratch = line[:0]
	} else {
		// Large direct callers may encode successfully without pinning their
		// transient output allocation for the remainder of the sink lifetime.
		s.scratch = nil
	}
	if s.closed {
		return ErrSinkShutdown
	}
	if s.err != nil {
		return s.err
	}
	if _, err := writeAll(s.f, line); err != nil {
		s.err = fmt.Errorf("trail/file: write %s: %w", s.path, err)
		return s.err
	}
	return nil
}

// Flush makes already written records visible through the underlying write
// path. Because writes are unbuffered this reports errors rather than
// moving bytes; with WithSyncOnFlush it also calls File.Sync.
func (s *Sink) Flush(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSinkShutdown
	}
	if s.err != nil {
		return s.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.syncOnFlush {
		if err := s.f.Sync(); err != nil {
			s.err = fmt.Errorf("trail/file: sync %s: %w", s.path, err)
			return s.err
		}
	}
	return nil
}

// Shutdown closes the file. With WithSyncOnFlush it syncs first. Cleanup is
// attempted even after write or sync failures, errors are combined, and
// repeated calls return the same terminal result. A canceled context leaves
// the sink open so a later call can finish it.
func (s *Sink) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.termErr
	}
	var errs []error
	if s.err == nil && s.syncOnFlush {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.f.Sync(); err != nil {
			s.err = fmt.Errorf("trail/file: sync %s: %w", s.path, err)
			errs = append(errs, s.err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.f.Close(); err != nil {
		errs = append(errs, fmt.Errorf("trail/file: close %s: %w", s.path, err))
	}
	if s.err != nil {
		errs = append(errs, s.err)
	}
	s.closed = true
	s.scratch = nil
	s.termErr = errors.Join(errs...)
	return s.termErr
}

// writeAll writes all of buf, treating a short write as an error because a
// partial line must stop the stream.
func writeAll(f io.Writer, buf []byte) (int, error) {
	written := 0
	for written < len(buf) {
		n, err := f.Write(buf[written:])
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, fmt.Errorf("short write: %d of %d bytes", written, len(buf))
		}
	}
	return written, nil
}
