package trail

import (
	"crypto/rand"
	"fmt"
	"sync"
)

// idGenerator produces identifiers. Implementations need not be
// concurrency-safe; the provider serializes calls.
type idGenerator interface {
	newTraceID() (TraceID, error)
	newSpanID() (SpanID, error)
}

// randomIDGenerator reads identifiers from the cryptographic random source,
// retrying a generated zero value.
type randomIDGenerator struct {
	mu sync.Mutex
}

// newTraceID returns a fresh non-zero trace identifier.
func (g *randomIDGenerator) newTraceID() (TraceID, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var id TraceID
	for {
		if _, err := rand.Read(id[:]); err != nil {
			return TraceID{}, fmt.Errorf("trail: generate trace ID: %w", err)
		}
		if id.IsValid() {
			return id, nil
		}
	}
}

// newSpanID returns a fresh non-zero span identifier.
func (g *randomIDGenerator) newSpanID() (SpanID, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var id SpanID
	for {
		if _, err := rand.Read(id[:]); err != nil {
			return SpanID{}, fmt.Errorf("trail: generate span ID: %w", err)
		}
		if id.IsValid() {
			return id, nil
		}
	}
}
