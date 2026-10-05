# Trail

**Lightweight local-first tracing for Go applications.**

Trail is being designed as a small span-based tracing library for debugging and
performance analysis: capture application-defined operations locally, then
inspect or share the trace artifact later. No collector or server should be
required. Short-lived tools are the first use case, not a restriction on the
architecture; daemons, desktop applications, tests, build tools, and agents are
also intended users.

Trail is not an OpenTelemetry implementation, logger, monitoring platform, or
replacement for Go's runtime tracing and pprof.

## Status

**Research/design only.** This repository contains tooling and an empty `trail`
package, not a working tracing library. The API, file format, sinks, rotation,
viewer, and remote export are not implemented.

See [the design proposal](docs/design.md) for the researched API/lifecycle model,
format comparison, dependency tradeoffs, and open questions. It recommends an
OTel-familiar API and a versioned local JSONL journal, not an OTLP-compliance claim.
The proposed pipeline is Tracer -> Processor -> Sink: SyncProcessor first, with
bounded asynchronous processing as a future configuration option using the same
span semantics and journal format.
Explicit providers remain primary: `p.Tracer("myapp")`. Optional global convenience
is `SetDefaultProvider(p)` followed by `GetTracer("myapp")`, which resolves through
the Provider registered at that call. The default Provider is initially no-op;
existing tracers keep their Provider, and callers still own Flush/Shutdown.
No package-level Start is proposed for v0.1.

Module: `go.lostcrafters.com/trail`. Configure the vanity domain's `go-import`
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

There are no tests yet: scaffold checks do not validate the proposed tracing
behavior. No tracing dependencies have been added.
