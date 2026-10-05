# Trail

**Lightweight local-first tracing for Go applications.**

Trail is a small span-based tracing library for debugging and performance
analysis: capture application-defined operations locally, then inspect or share
the trace artifact later. No collector or server is required. Short-lived tools
are the first use case, not a restriction on the architecture; daemons, desktop
applications, tests, build tools, and agents are also intended users.

Trail is not an OpenTelemetry implementation, logger, monitoring platform, or
replacement for Go's runtime tracing and pprof.

## Status

**v0.1 core implemented.** The explicit Provider -> Tracer -> Span API with
context propagation, a no-op-safe global default provider, the
Tracer -> Processor -> Sink pipeline with SyncProcessor, bounded typed
attributes/events/status/RecordError, trace-completion bookkeeping, and the
versioned Trail JSONL journal with an exclusive-create file sink
(`go.lostcrafters.com/trail/file`) are implemented and tested, including
race-detector runs and disabled-path allocation benchmarks.

Not implemented: asynchronous processing, rotation, retention, compression,
viewers, OTLP conversion, remote export, and distributed propagation. See
[the design](docs/design.md) for contracts, the format comparison, and open
questions. The native journal is a Trail format with OTel-aligned semantics,
not an OTLP-compliance claim.

Explicit providers remain primary: `p.Tracer("myapp")`. Optional global convenience
is `SetDefaultProvider(p)` followed by `GetTracer("myapp")`, which resolves through
the Provider registered at that call. The default Provider is initially no-op;
existing tracers keep their Provider, and callers still own Flush/Shutdown.
There is no package-level Start.

Module: `go.lostcrafters.com/trail`. Configure the vanity domain's `go.import`
metadata to point to the hosting repository before publishing.

## Development

Install [mise](https://mise.jdx.dev/), then prepare the pinned tools:

```sh
mise trust
mise install
```

Inside a Git checkout, install the repository-local [hk](https://hk.jdx.dev/)
hooks:

```sh
mise run hooks:install
```

Run `mise run fmt` before `mise run check` (build, formatting checks, lint, tests,
and module tidiness). Use `mise tasks ls` for individual tasks. Race tests require
a C compiler and are separate: `mise run test:race`.

Pre-commit formats staged Go files and runs golangci-lint; pre-push runs tests and
checks module tidiness. Hooks resolve tools through mise and preserve unstaged
changes while fixing staged files.

Tests cover identifiers, handles, globals, lifecycle and completion
contracts, bounded attributes and events, the conformance suite, journal
encoding, torn-tail recovery, and end-to-end pipeline behavior against a
real file. Production code depends only on the Go standard library.
