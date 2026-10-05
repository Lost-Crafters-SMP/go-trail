package trail

import "time"

// timeReading pairs a wall-clock reading with a monotonic-relative tick.
// Wall readings are for correlation; ticks measure durations and elapsed
// offsets so clock adjustments cannot distort measurements.
type timeReading struct {
	wall time.Time
	tick time.Duration
}

// clock produces readings for a capture. Readings are taken in the calling
// goroutine, before waiting for admission, so processor delay never extends
// measured durations.
type clock interface {
	now() timeReading
}

// realClock reads the system clocks. Ticks derive from the monotonic
// component of time.Now relative to the origin captured at construction.
// Like the time package, it does not promise suspend-inclusive elapsed time
// on every system.
type realClock struct {
	origin time.Time
}

// newRealClock starts a capture clock at the current time.
func newRealClock() *realClock {
	return &realClock{origin: time.Now()}
}

// now returns the current wall reading and its monotonic elapsed tick.
func (c *realClock) now() timeReading {
	now := time.Now()
	return timeReading{wall: now, tick: now.Sub(c.origin)}
}
