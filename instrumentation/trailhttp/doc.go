// Package trailhttp instruments net/http servers and clients with Trail
// spans.
//
// Server middleware records one span per handled request, named by the
// request method and, when one is known before the handler runs, the
// matched low-cardinality route template (for example "GET /users/{id}");
// otherwise the span name is the method alone. Routes from an outer
// ServeMux are matched only after the span starts, so wrapping a whole mux
// yields method-only names with the pattern recorded as http.route, while
// mounting the middleware inside mux routes (or supplying
// WithRouteExtractor) names spans with the template. The raw URL path is
// never used as a span name.
//
// The client Transport records one span per actual round trip attempt,
// named by the method and destination host (for example "GET
// api.example.com"). Redirects and retries therefore produce sibling spans
// under the caller's parent context.
//
// Default recorded data is minimal: the request method, URL scheme, server
// address and port, URL path (never the query string), the matched route,
// the HTTP protocol version, the response status code, the response size,
// bounded connection and phase events on the client, and nothing from
// request or response bodies or headers. The query string, peer network
// address, and raw error or panic text are opt-in.
//
// These are Trail semantics informed by common HTTP tracing conventions;
// Trail does not claim OpenTelemetry semantic-convention compliance.
// Intentional choices include: DNS, connect, and TLS phases recorded as
// events rather than child spans, no url.full, no X-Forwarded-Host
// precedence for server.address, no resend accounting, and a client span
// that ends when the transport returns rather than when the response body
// is consumed, so response-body transfer time is outside the span.
package trailhttp
