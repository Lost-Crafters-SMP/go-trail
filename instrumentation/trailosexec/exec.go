// Package trailosexec traces child process execution through the os/exec
// API.
//
// Command and CommandContext return a Cmd that embeds *exec.Cmd and adds one
// execution span per run: named after the command as passed, started in
// Start, ended in Wait, Run, Output, or CombinedOutput. Process setup,
// cancellation, pipes, and error values are the standard library's own
// behavior; the wrapper adds only recording.
//
// Default recorded data is conservative: the command name, the argument
// count, the process ID after a successful start, and the exit code. The
// argument list, environment, working directory, and resolved executable
// path are recorded only through the corresponding options, and raw error
// text only through WithErrorDetails; by default failures set Error status
// with stable classified descriptions.
//
// A Cmd that is started but never waited on leaves its span live, mirroring
// the abandoned process it describes; Trail's bounded active-span accounting
// reports it. Calling methods on the embedded *exec.Cmd (for example
// cmd.Cmd.Run()) bypasses instrumentation. The wrapper adds no internal
// synchronization beyond exec.Cmd's own sequencing contract: callers
// coordinate Start and Wait as they already must.
package trailosexec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"go.lostcrafters.com/trail"
)

// scope is the instrumentation scope used for execution spans.
const scope = "go.lostcrafters.com/trail/instrumentation/trailosexec"

// An Option configures a Cmd's instrumentation. Options can only be created
// by this package.
type Option func(*options)

// options collects Option values.
type options struct {
	tracer       trail.Tracer
	tracerSet    bool
	args         bool
	env          bool
	dir          bool
	path         bool
	errorDetails bool
}

// WithTracer records execution spans with t instead of the tracer resolved
// from the default provider at construction time.
func WithTracer(t trail.Tracer) Option {
	return func(o *options) { o.tracer = t; o.tracerSet = true }
}

// WithArgs records the full argument list (excluding the command name) as
// the exec.args attribute. Arguments can carry secrets or local paths; the
// caller opts in and owns that decision. Recorded list attributes keep at
// most their first 64 elements.
func WithArgs() Option {
	return func(o *options) { o.args = true }
}

// WithEnv records KEY=VALUE environment entries as the exec.env attribute
// when the command sets an explicit environment. Values can carry secrets,
// and only the first 64 entries are kept.
func WithEnv() Option {
	return func(o *options) { o.env = true }
}

// WithDir records the command's working directory as exec.dir when set.
func WithDir() Option {
	return func(o *options) { o.dir = true }
}

// WithPath records the resolved executable path as exec.path after a
// successful start.
func WithPath() Option {
	return func(o *options) { o.path = true }
}

// WithErrorDetails additionally records the failing error with
// Span.RecordError, exposing raw error text that can contain paths or
// addresses.
func WithErrorDetails() Option {
	return func(o *options) { o.errorDetails = true }
}

// A Cmd wraps *exec.Cmd with one Trail execution span per run. Promoted
// fields and methods behave exactly as os/exec's; Run, Start, Wait, Output,
// and CombinedOutput are overridden to record. Calling the embedded
// methods directly (cmd.Cmd.Run()) bypasses instrumentation, and passing
// the wrapper to APIs requiring *exec.Cmd needs cmd.Cmd.
type Cmd struct {
	*exec.Cmd

	tracer  trail.Tracer
	opts    options
	parent  context.Context
	started bool
	span    trail.Span
}

// Command returns a Cmd wrapping exec.Command. Without a context the
// execution span is a trace root; prefer CommandContext, which parents the
// span from the supplied context, for normal usage.
func Command(name string, arg ...string) *Cmd {
	return newCmd(context.Background(), exec.Command(name, arg...))
}

// CommandContext returns a Cmd wrapping exec.CommandContext, preserving the
// standard library's cancellation semantics (the process is killed when ctx
// is done) and parenting the execution span from ctx. It panics on a nil
// context, like exec.CommandContext.
func CommandContext(ctx context.Context, name string, arg ...string) *Cmd {
	return newCmd(ctx, exec.CommandContext(ctx, name, arg...))
}

func newCmd(ctx context.Context, cmd *exec.Cmd) *Cmd {
	return &Cmd{
		Cmd:    cmd,
		tracer: trail.GetTracer(scope),
		parent: ctx,
	}
}

// Apply sets instrumentation options on c and returns c so it can be chained
// at construction:
//
//	cmd := trailosexec.CommandContext(ctx, "go", "test").Apply(trailosexec.WithArgs())
//
// Apply must be called before Start.
func (c *Cmd) Apply(opts ...Option) *Cmd {
	for _, opt := range opts {
		if opt != nil {
			opt(&c.opts)
		}
	}
	if c.opts.tracerSet {
		c.tracer = c.opts.tracer
	}
	return c
}

// Run starts the command and waits for it to complete, recording one
// execution span covering both phases. It is implemented in terms of Start
// and Wait, so the span cannot be duplicated.
func (c *Cmd) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

// Start starts the command and opens its execution span. On success the
// process ID is recorded immediately; on failure the failure is classified,
// the span is marked Error, and the span ends. A second Start mirrors the
// standard library's error without creating another span.
func (c *Cmd) Start() error {
	if c.started {
		return c.Cmd.Start()
	}
	c.started = true
	c.beginSpan()
	if err := c.Cmd.Start(); err != nil {
		c.finishSpan(err, true)
		return err
	}
	attrs := []trail.Attribute{trail.Int("process.pid", c.Process.Pid)}
	if c.opts.path {
		attrs = append(attrs, trail.String("exec.path", c.Path))
	}
	c.span.SetAttributes(attrs...)
	return nil
}

// Wait waits for the command, records the exit code when a process state is
// available, classifies any failure, and ends the execution span. Calling
// Wait without a preceding Start defers entirely to the standard library's
// error; no span exists to finish.
func (c *Cmd) Wait() error {
	if !c.started {
		return c.Cmd.Wait()
	}
	err := c.Cmd.Wait()
	c.finishSpan(err, false)
	return err
}

// Output runs the command and returns its standard output, recording
// exactly one execution span around the standard library's own
// implementation, including its stdout/stderr capture and ExitError
// behavior.
func (c *Cmd) Output() ([]byte, error) {
	c.started = true
	c.beginSpan()
	out, err := c.Cmd.Output()
	c.recordStartAttrs()
	c.finishSpan(err, false)
	return out, err
}

// CombinedOutput runs the command and returns its combined output,
// recording exactly one execution span around the standard library's own
// implementation.
func (c *Cmd) CombinedOutput() ([]byte, error) {
	c.started = true
	c.beginSpan()
	out, err := c.Cmd.CombinedOutput()
	c.recordStartAttrs()
	c.finishSpan(err, false)
	return out, err
}

// recordStartAttrs records attributes that are normally recorded after a
// successful Start: the process PID and, if opted in, the executable path.
func (c *Cmd) recordStartAttrs() {
	attrs := []trail.Attribute{trail.Int("process.pid", c.Process.Pid)}
	if c.opts.path {
		attrs = append(attrs, trail.String("exec.path", c.Path))
	}
	c.span.SetAttributes(attrs...)
}

// beginSpan opens the execution span and records the construction-time
// attributes the caller opted into. Attribute values are captured now, at
// start time, so later field mutation is not picked up.
func (c *Cmd) beginSpan() {
	if !c.tracer.Enabled() {
		return
	}
	name := ""
	if len(c.Args) > 0 {
		name = c.Args[0]
	}
	var attrs []trail.Attribute
	if len(c.Args) > 1 {
		attrs = append(attrs, trail.Int("exec.args.count", len(c.Args)-1))
	}
	if c.opts.args && len(c.Args) > 1 {
		attrs = append(attrs, trail.Strings("exec.args", c.Args[1:]))
	}
	if c.opts.env && len(c.Env) > 0 {
		attrs = append(attrs, trail.Strings("exec.env", c.Env))
	}
	if c.opts.dir && c.Dir != "" {
		attrs = append(attrs, trail.String("exec.dir", c.Dir))
	}
	var startOpts []trail.StartOption
	if attrs != nil {
		startOpts = append(startOpts, trail.WithAttributes(attrs...))
	}
	_, span := c.tracer.Start(c.parent, name, startOpts...)
	c.span = span
}

// finishSpan records the outcome and ends the span. startPhase selects the
// generic description used for otherwise-unclassified failures.
func (c *Cmd) finishSpan(err error, startPhase bool) {
	span := c.span
	c.span = trail.Span{}
	if !span.IsRecording() {
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, exec.ErrNotFound):
			span.SetStatus(trail.StatusError, "executable not found")
		default:
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				if code := exitErr.ExitCode(); code >= 0 {
					span.SetStatus(trail.StatusError, fmt.Sprintf("exit status %d", code))
				} else {
					span.SetStatus(trail.StatusError, "terminated by signal")
				}
			} else if startPhase {
				span.SetStatus(trail.StatusError, "start failed")
			} else {
				span.SetStatus(trail.StatusError, "execution failed")
			}
		}
		if c.opts.errorDetails {
			span.RecordError(err)
		}
	}
	if c.ProcessState != nil {
		span.SetAttributes(trail.Int("process.exit.code", c.ProcessState.ExitCode()))
	}
	span.End()
}
