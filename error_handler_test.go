package trail

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestErrorHandlerReentryAndOnce(t *testing.T) {
	failure := errors.New("write failed")
	var calls atomic.Int32
	var provider *Provider
	var sink *recordingSink
	provider, sink, _, _ = newLifecycleProvider(t, WithErrorHandler(func(err error) {
		calls.Add(1)
		if !errors.Is(err, failure) {
			t.Errorf("notification = %v, want write failure", err)
		}
		// All of these need admission; no callback lock may block recursion.
		_ = provider.Flush(context.Background())
		_, span := provider.Tracer("callback").Start(context.Background(), "nested")
		span.End()
		_ = provider.Shutdown(context.Background())
	}))
	_, span := provider.Tracer("test").Start(context.Background(), "work")
	sink.writeErr = failure
	done := make(chan struct{})
	go func() {
		defer close(done)
		span.SetAttributes(String("key", "value"))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler reentry deadlocked")
	}
	if calls.Load() != 1 {
		t.Fatalf("notifications = %d, want 1", calls.Load())
	}
	if err := provider.Shutdown(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("Shutdown lost authoritative error: %v", err)
	}
}

func TestErrorHandlerConcurrentFailure(t *testing.T) {
	var calls atomic.Int32
	provider, sink, _, _ := newLifecycleProvider(t, randomIDs(), WithErrorHandler(func(error) {
		calls.Add(1)
	}))
	_, span := provider.Tracer("test").Start(context.Background(), "work")
	sink.writeErr = errors.New("broken output")
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			span.AddEvent("event")
			_, child := provider.Tracer("test").Start(context.Background(), "child")
			child.End()
			_ = provider.Flush(context.Background())
		})
	}
	wg.Wait()
	span.End()
	_ = provider.Shutdown(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("notifications = %d, want 1 despite repeated failures", calls.Load())
	}
}

func TestErrorHandlerCleanupFailure(t *testing.T) {
	writeErr := errors.New("write")
	closeErr := errors.New("close")
	var received []error
	provider, sink, _, _ := newLifecycleProvider(t, WithErrorHandler(func(err error) {
		received = append(received, err)
	}))
	sink.writeErr, sink.closeErr = writeErr, closeErr
	_, span := provider.Tracer("test").Start(context.Background(), "work")
	span.End()
	terminal := provider.Shutdown(context.Background())
	_ = provider.Shutdown(context.Background())
	if len(received) != 2 || !errors.Is(received[0], writeErr) || !errors.Is(received[1], closeErr) {
		t.Fatalf("notifications = %v, want write and close once each", received)
	}
	if !errors.Is(terminal, writeErr) || !errors.Is(terminal, closeErr) {
		t.Fatalf("Shutdown error = %v, want both failures", terminal)
	}
}

func TestErrorHandlerConstructorFailure(t *testing.T) {
	failure := errors.New("header/capture write")
	sink := &recordingSink{writeErr: failure}
	processor, err := NewSyncProcessor(sink)
	if err != nil {
		t.Fatal(err)
	}
	var received error
	_, err = NewProvider(processor, WithErrorHandler(func(err error) { received = err }))
	if !errors.Is(err, failure) || !errors.Is(received, failure) {
		t.Fatalf("constructor returned %v and notified %v", err, received)
	}
	if sink.closeCalls != 0 {
		t.Fatal("failed constructor took processor ownership")
	}
	_ = processor.Shutdown(context.Background())
}

func TestErrorHandlerIDFailureRemainsReturned(t *testing.T) {
	var calls atomic.Int32
	provider, _, _, ids := newLifecycleProvider(t, WithErrorHandler(func(error) { calls.Add(1) }))
	failure := errors.New("ID generation")
	ids.spanErr = failure
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { _, _ = provider.Tracer("test").Start(context.Background(), "work") })
	}
	wg.Wait()
	if calls.Load() != 1 || !errors.Is(provider.Flush(context.Background()), failure) {
		t.Fatal("ID failure must notify once and remain visible through Flush")
	}
	if !errors.Is(provider.Shutdown(context.Background()), failure) {
		t.Fatal("Shutdown lost ID failure")
	}
}

func TestErrorHandlerCancellationIsNotTerminal(t *testing.T) {
	var calls atomic.Int32
	provider, _, _, _ := newLifecycleProvider(t, WithErrorHandler(func(error) { calls.Add(1) }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = provider.Flush(ctx)
	_ = provider.Shutdown(ctx)
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("cancellation notified %d times, want 0", calls.Load())
	}
}

func TestErrorHandlerFlushFailureAndNilHandler(t *testing.T) {
	failure := errors.New("flush")
	var calls int
	provider, sink, _, _ := newLifecycleProvider(t, WithErrorHandler(func(err error) {
		calls++
		if !errors.Is(err, failure) {
			t.Errorf("notification = %v", err)
		}
	}))
	sink.flushErr = failure
	for range 3 {
		if err := provider.Flush(context.Background()); !errors.Is(err, failure) {
			t.Fatalf("Flush = %v, want failure", err)
		}
	}
	_ = provider.Shutdown(context.Background())
	if calls != 1 {
		t.Fatalf("notifications = %d, want 1", calls)
	}
	quiet, quietSink, _, _ := newLifecycleProvider(t, WithErrorHandler(nil))
	quietSink.flushErr = failure
	if err := quiet.Flush(context.Background()); !errors.Is(err, failure) {
		t.Fatal("nil handler suppressed returned failure")
	}
	_ = quiet.Shutdown(context.Background())
}

func TestBlockedErrorHandlerDoesNotHoldAdmission(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	provider, sink, _, _ := newLifecycleProvider(t, WithErrorHandler(func(error) {
		close(entered)
		<-release
	}))
	_, span := provider.Tracer("test").Start(context.Background(), "work")
	sink.writeErr = errors.New("broken")
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		span.AddEvent("event")
	}()
	defer func() { close(release); <-finished }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not run")
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		_ = provider.Shutdown(context.Background())
	}()
	select {
	case <-shutdownDone:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked handler held admission during shutdown")
	}
}
