package trailhttp

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"

	"go.lostcrafters.com/trail"
)

// scope is the instrumentation scope used for HTTP spans.
const scope = "go.lostcrafters.com/trail/instrumentation/trailhttp"

// An Option configures the server middleware or client transport. Options
// can only be created by this package.
type Option func(*config)

// config collects Option values.
type config struct {
	tracer         trail.Tracer
	routeExtractor func(*http.Request) string
	queryString    bool
	peerAddress    bool
	errorDetails   bool
}

// WithTracer records spans with t instead of the tracer resolved from the
// default provider at construction time.
func WithTracer(t trail.Tracer) Option {
	return func(c *config) { c.tracer = t }
}

// WithRouteExtractor resolves the low-cardinality route template for a
// request, for routers that do not populate http.Request.Pattern. A non-empty
// result takes precedence over Pattern; an empty result falls back to it.
func WithRouteExtractor(f func(*http.Request) string) Option {
	return func(c *config) { c.routeExtractor = f }
}

// WithQueryString records the URL query string as url.query. Query strings
// commonly carry tokens and other secrets; the caller opts in and owns that
// decision.
func WithQueryString() Option {
	return func(c *config) { c.queryString = true }
}

// WithPeerAddress records the peer network address on connection events.
func WithPeerAddress() Option {
	return func(c *config) { c.peerAddress = true }
}

// WithErrorDetails additionally records raw error or panic text with
// RecordError, exposing values that can contain URLs, addresses, or paths.
func WithErrorDetails() Option {
	return func(c *config) { c.errorDetails = true }
}

func newConfig(opts []Option) config {
	cfg := config{tracer: trail.GetTracer(scope)}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// NewMiddleware returns an http.Handler middleware that records one Trail
// span per handled request. It does not read or wrap request bodies, does
// not record query strings by default, and preserves the wrapped handler's
// behavior, including panics, which are recorded and re-raised unchanged.
//
// The span is named from the request method plus the route template when
// one is known before the handler runs: from WithRouteExtractor, or from
// http.Request.Pattern when the middleware is mounted inside a ServeMux
// route. When the middleware wraps a ServeMux from the outside, matching
// happens after the span starts, so the span keeps the method-only name and
// the matched pattern is recorded as the http.route attribute instead.
func NewMiddleware(opts ...Option) func(next http.Handler) http.Handler {
	cfg := newConfig(opts)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.tracer.Enabled() {
				next.ServeHTTP(w, r)
				return
			}
			route := resolveRoute(cfg, r)
			name := r.Method
			if route != "" {
				name = r.Method + " " + route
			}
			attrs := []trail.Attribute{
				trail.String("http.request.method", r.Method),
				trail.String("url.scheme", requestScheme(r)),
				trail.String("url.path", r.URL.Path),
			}
			if host, port, ok := splitHostPort(r.Host); ok {
				attrs = append(attrs, trail.String("server.address", host))
				if port > 0 {
					attrs = append(attrs, trail.Int("server.port", port))
				}
			}
			if route != "" {
				attrs = append(attrs, trail.String("http.route", route))
			}
			if version := protocolVersion(r.Proto); version != "" {
				attrs = append(attrs, trail.String("network.protocol.version", version))
			}
			ctx, span := cfg.tracer.Start(r.Context(), name, trail.WithAttributes(attrs...))
			rec := &responseRecorder{ResponseWriter: w}
			inner := r.WithContext(ctx)
			defer func() {
				if v := recover(); v != nil {
					span.SetStatus(trail.StatusError, "panic")
					span.AddEvent("panic")
					if cfg.errorDetails {
						span.RecordError(fmt.Errorf("%v", v))
					}
					span.End()
					panic(v)
				}
				// The effective status defaults to 200 when the handler
				// wrote a body without calling WriteHeader.
				status := rec.code
				if !rec.wroteHeader {
					status = http.StatusOK
				}
				outcome := []trail.Attribute{
					trail.Int("http.response.status_code", status),
					trail.Int64("http.response.body.size", rec.written),
				}
				if route == "" && inner.Pattern != "" {
					// A ServeMux below the middleware matched after the
					// span started; keep the route as an attribute.
					outcome = append(outcome, trail.String("http.route", inner.Pattern))
				}
				span.SetAttributes(outcome...)
				if status >= 500 {
					span.SetStatus(trail.StatusError, "")
				}
				span.End()
			}()
			next.ServeHTTP(rec, inner)
		})
	}
}

// resolveRoute returns the route template for r, preferring an explicit
// extractor and falling back to the ServeMux-populated pattern.
func resolveRoute(cfg config, r *http.Request) string {
	if cfg.routeExtractor != nil {
		if route := cfg.routeExtractor(r); route != "" {
			return route
		}
	}
	return r.Pattern
}

// requestScheme reports the URL scheme implied by the transport.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// splitHostPort splits a Host header value into host and port, reporting
// whether a host was present.
func splitHostPort(host string) (string, int, bool) {
	if host == "" {
		return "", 0, false
	}
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		return host, 0, true
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return h, 0, true
	}
	return h, port, true
}

// protocolVersion maps the request protocol to the version string recorded
// as network.protocol.version, reporting "" for unknown spellings.
func protocolVersion(proto string) string {
	switch proto {
	case "HTTP/1.0":
		return "1.0"
	case "HTTP/1.1":
		return "1.1"
	case "HTTP/2.0":
		return "2"
	case "HTTP/3.0":
		return "3"
	default:
		return ""
	}
}

// responseRecorder captures the response status code and byte count while
// delegating everything else to the wrapped ResponseWriter. It preserves
// http.ResponseController support through Unwrap and directly forwards
// Flush and Hijack for handlers that type-assert those optional interfaces
// instead of using ResponseController.
type responseRecorder struct {
	http.ResponseWriter

	code        int
	wroteHeader bool
	written     int64
}

func (r *responseRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.code = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.code = http.StatusOK
		r.wroteHeader = true
	}
	n, err := r.ResponseWriter.Write(p)
	if n > 0 {
		r.written += int64(n)
	}
	return n, err
}

// Flush forwards to the wrapped writer when it supports flushing.
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the wrapped writer when it supports hijacking.
func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("trailhttp: wrapped ResponseWriter does not support Hijack")
}

// Unwrap exposes the wrapped writer to http.ResponseController.
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
