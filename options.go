package trail

// A StartOption configures span creation in Tracer.Start. Options can only
// be created by this package.
type StartOption func(*startConfig)

// An EventOption configures event recording in Span.AddEvent and
// Span.RecordError. It is an alias of StartOption so WithAttributes applies
// to both positions; options irrelevant to one context are ignored there.
type EventOption = StartOption

// startConfig collects option values for one span start or event.
type startConfig struct {
	newRoot    bool
	attributes []Attribute
}

// resolveStartOptions isolates the configuration passed to opaque option
// functions. Callers with no options need not allocate that configuration.
func resolveStartOptions(opts []StartOption) startConfig {
	var cfg startConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// WithNewRoot starts a new trace instead of joining the span carried by the
// context, even when that span is an active parent from the same provider.
// It is ignored in event positions.
func WithNewRoot() StartOption {
	return func(c *startConfig) { c.newRoot = true }
}

// WithAttributes supplies attributes for a span start or an event. The
// slice is borrowed: bounds, deduplication, and copying happen when the
// value is recorded, so disabled calls do not copy.
func WithAttributes(attrs ...Attribute) StartOption {
	return func(c *startConfig) { c.attributes = attrs }
}

// A ProviderOption configures a Provider at construction. Options can only
// be created by this package.
type ProviderOption func(*providerConfig)

// providerConfig collects ProviderOption values before a provider is
// initialized. The identifier and clock seams are provisional test seams;
// production defaults fill them when unset.
type providerConfig struct {
	ids          idGenerator
	clock        clock
	errorHandler func(error)
}

// WithErrorHandler sets an optional notification callback for the first latched
// failure and additional terminal cleanup failures. There is no default output.
// Notifications run synchronously after internal locks are released and may
// reenter the provider. Concurrent operations may invoke the handler concurrently;
// handlers must be concurrency-safe. Flush and Shutdown remain authoritative.
// Context cancellation and ordinary bounded-data drops are not notifications.
func WithErrorHandler(handler func(error)) ProviderOption {
	return func(cfg *providerConfig) { cfg.errorHandler = handler }
}
