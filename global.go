package trail

import "sync/atomic"

// defaultProvider holds the process-wide default provider registration. A
// nil registration resolves to the safe no-op default.
var defaultProvider atomic.Pointer[Provider]

// SetDefaultProvider registers p as the process-wide default provider used by
// GetTracer. Passing nil restores the no-op default.
//
// SetDefaultProvider is concurrency-safe and affects only future GetTracer
// calls; tracers and spans already created keep the provider they were
// created with. Registration transfers no lifecycle ownership: the caller
// that constructed the provider retains it and remains responsible for
// flushing and shutting it down. Registering a provider performs no other
// work; it creates no files or workers and runs no implicit configuration.
func SetDefaultProvider(p *Provider) {
	defaultProvider.Store(p)
}

// GetTracer returns a Tracer for the named instrumentation scope from the
// provider registered with SetDefaultProvider at the time of the call.
// Before configuration, and after registering nil, it returns a disabled
// tracer using the same scope rules as Provider.Tracer. The provider is the
// only globally registered object; no tracer is stored globally.
func GetTracer(scope string) Tracer {
	return defaultProvider.Load().Tracer(scope)
}
