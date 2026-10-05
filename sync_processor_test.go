package trail

import (
	"context"
	"errors"
	"testing"
)

// recordingSink captures records and can be configured to fail.
type recordingSink struct {
	records []Record

	writeErr   error
	flushErr   error
	closeErr   error
	writeCalls int
	flushCalls int
	closeCalls int
}

func (s *recordingSink) WriteRecord(record Record) error {
	s.writeCalls++
	if s.writeErr != nil {
		return s.writeErr
	}
	s.records = append(s.records, record)
	return nil
}

func (s *recordingSink) Flush(_ context.Context) error {
	s.flushCalls++
	if s.flushErr != nil {
		return s.flushErr
	}
	return nil
}

func (s *recordingSink) Shutdown(_ context.Context) error {
	s.closeCalls++
	if s.closeErr != nil {
		return s.closeErr
	}
	return nil
}

func TestNewSyncProcessorNilSink(t *testing.T) {
	if _, err := NewSyncProcessor(nil); err == nil {
		t.Fatal("NewSyncProcessor(nil) succeeded, want error")
	}
}

func TestSyncProcessorProcessOrder(t *testing.T) {
	sink := &recordingSink{}
	processor, err := NewSyncProcessor(sink)
	if err != nil {
		t.Fatalf("NewSyncProcessor unexpected error: %v", err)
	}
	first := CaptureStart{Seq: 1}
	second := SpanStart{Seq: 2, Name: "op"}
	if err := processor.Process(first); err != nil {
		t.Fatalf("Process(first) unexpected error: %v", err)
	}
	if err := processor.Process(second); err != nil {
		t.Fatalf("Process(second) unexpected error: %v", err)
	}
	if len(sink.records) != 2 {
		t.Fatalf("sink recorded %d records, want 2", len(sink.records))
	}
	if sink.records[0] != first || sink.records[1] != second {
		t.Fatalf("records out of order: %v", sink.records)
	}
}

func TestSyncProcessorNilRecord(t *testing.T) {
	sink := &recordingSink{}
	processor, _ := NewSyncProcessor(sink)
	if err := processor.Process(nil); err == nil {
		t.Fatal("Process(nil) succeeded, want error")
	}
	if sink.writeCalls != 0 {
		t.Fatalf("sink wrote %d records after nil record, want 0", sink.writeCalls)
	}
}

func TestSyncProcessorLatchesFirstWriteError(t *testing.T) {
	writeErr := errors.New("disk full")
	sink := &recordingSink{writeErr: writeErr}
	processor, _ := NewSyncProcessor(sink)
	if err := processor.Process(CaptureStart{}); !errors.Is(err, writeErr) {
		t.Fatalf("Process error = %v, want wrapped %v", err, writeErr)
	}
	// The latched error must stop output: a later record must not reach the
	// sink and must return the same error.
	sink.writeErr = nil
	if err := processor.Process(SpanStart{}); !errors.Is(err, writeErr) {
		t.Fatalf("second Process error = %v, want latched %v", err, writeErr)
	}
	if sink.writeCalls != 1 {
		t.Fatalf("sink write calls = %d, want 1", sink.writeCalls)
	}
}

func TestSyncProcessorFlush(t *testing.T) {
	t.Run("calls sink flush", func(t *testing.T) {
		sink := &recordingSink{}
		processor, _ := NewSyncProcessor(sink)
		if err := processor.Flush(context.Background()); err != nil {
			t.Fatalf("Flush unexpected error: %v", err)
		}
		if sink.flushCalls != 1 {
			t.Fatalf("sink flush calls = %d, want 1", sink.flushCalls)
		}
	})
	t.Run("flush failure latches", func(t *testing.T) {
		flushErr := errors.New("flush failed")
		sink := &recordingSink{flushErr: flushErr}
		processor, _ := NewSyncProcessor(sink)
		if err := processor.Flush(context.Background()); !errors.Is(err, flushErr) {
			t.Fatalf("Flush error = %v, want wrapped %v", err, flushErr)
		}
		sink.flushErr = nil
		if err := processor.Flush(context.Background()); !errors.Is(err, flushErr) {
			t.Fatalf("second Flush error = %v, want latched %v", err, flushErr)
		}
		if sink.flushCalls != 1 {
			t.Fatalf("sink flush calls = %d, want 1", sink.flushCalls)
		}
	})
}

func TestSyncProcessorShutdown(t *testing.T) {
	t.Run("flushes and closes sink once", func(t *testing.T) {
		sink := &recordingSink{}
		processor, _ := NewSyncProcessor(sink)
		if err := processor.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown unexpected error: %v", err)
		}
		if sink.flushCalls != 1 || sink.closeCalls != 1 {
			t.Fatalf("sink flush/close calls = %d/%d, want 1/1", sink.flushCalls, sink.closeCalls)
		}
		first := processor.Shutdown(context.Background())
		if first != nil {
			t.Fatalf("repeated Shutdown error = %v, want nil", first)
		}
		if sink.flushCalls != 1 || sink.closeCalls != 1 {
			t.Fatalf("repeated Shutdown changed sink flush/close calls to %d/%d, want 1/1", sink.flushCalls, sink.closeCalls)
		}
	})
	t.Run("closes sink despite write failure", func(t *testing.T) {
		writeErr := errors.New("short write")
		sink := &recordingSink{writeErr: writeErr}
		processor, _ := NewSyncProcessor(sink)
		if err := processor.Process(CaptureStart{}); !errors.Is(err, writeErr) {
			t.Fatalf("Process error = %v, want wrapped %v", err, writeErr)
		}
		err := processor.Shutdown(context.Background())
		if !errors.Is(err, writeErr) {
			t.Fatalf("Shutdown error = %v, want combined error wrapping %v", err, writeErr)
		}
		if sink.closeCalls != 1 {
			t.Fatalf("sink close calls = %d, want 1 after write failure", sink.closeCalls)
		}
	})
	t.Run("process after shutdown", func(t *testing.T) {
		sink := &recordingSink{}
		processor, _ := NewSyncProcessor(sink)
		if err := processor.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown unexpected error: %v", err)
		}
		if err := processor.Process(CaptureStart{}); !errors.Is(err, ErrProcessorShutdown) {
			t.Fatalf("Process error = %v, want ErrProcessorShutdown", err)
		}
		if err := processor.Flush(context.Background()); !errors.Is(err, ErrProcessorShutdown) {
			t.Fatalf("Flush error = %v, want ErrProcessorShutdown", err)
		}
	})
	t.Run("canceled context leaves shutdown resumable", func(t *testing.T) {
		sink := &recordingSink{}
		processor, _ := NewSyncProcessor(sink)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := processor.Shutdown(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Shutdown(canceled) error = %v, want context.Canceled", err)
		}
		if sink.closeCalls != 0 {
			t.Fatalf("sink closed after canceled Shutdown, want not closed")
		}
		if err := processor.Process(CaptureStart{}); err != nil {
			t.Fatalf("Process after canceled Shutdown error = %v, want admission still open", err)
		}
		if err := processor.Shutdown(context.Background()); err != nil {
			t.Fatalf("resumed Shutdown error = %v, want nil", err)
		}
		if sink.closeCalls != 1 {
			t.Fatalf("sink close calls = %d, want 1", sink.closeCalls)
		}
	})
}
