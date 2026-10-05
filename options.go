package trail

// A StartOption configures span creation in Tracer.Start. Options can only
// be created by this package.
type StartOption func(*startConfig)

// startConfig collects StartOption values for one span start.
type startConfig struct {
	newRoot bool
}

// WithNewRoot starts a new trace instead of joining the span carried by the
// context, even when that span is an active parent from the same provider.
func WithNewRoot() StartOption {
	return func(c *startConfig) { c.newRoot = true }
}

// A ProviderOption configures a Provider at construction. Options can only
// be created by this package.
type ProviderOption func(*providerConfig)

// providerConfig collects ProviderOption values before a provider is
// initialized. The identifier and clock seams are provisional test seams;
// production defaults fill them when unset.
type providerConfig struct {
	ids   idGenerator
	clock clock
}
