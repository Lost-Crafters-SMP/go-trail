package trail

// A StartOption configures span creation in Tracer.Start. Options arrive with
// the milestones that implement them.
type StartOption func(*startConfig)

// startConfig collects StartOption values for one span start.
type startConfig struct{}
