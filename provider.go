package trail

// A Provider owns tracing state and lifecycle for a set of tracers.
//
// The zero value and a nil Provider are disabled, safe handles. Providers are
// created by NewProvider once processing is configured; they are not
// constructed directly.
type Provider struct {
	state *providerState
}

// providerState holds the state of an enabled provider. A nil state marks a
// disabled provider. Identifier, clock, admission, and lifecycle state arrive
// with the milestones that implement them.
type providerState struct{}

// Tracer returns a handle whose spans are attributed to scope. The scope is
// a component or instrumentation name; an empty scope means unknown. Tracer
// handles are cheap values that share the provider's state.
func (p *Provider) Tracer(scope string) Tracer {
	return Tracer{provider: p, scope: scope}
}

// enabled reports whether the provider can admit recording spans. It is safe
// to call on a nil Provider.
func (p *Provider) enabled() bool {
	return p != nil && p.state != nil
}
