package trailhttp

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync"
	"time"

	"go.lostcrafters.com/trail"
)

// roundTripKey marks requests already carrying a trailhttp span so that
// stacked trailhttp transports do not instrument the same round trip twice.
type roundTripKey struct{}

// Transport is an http.RoundTripper that records one Trail span per actual
// round trip attempt. It composes with any existing httptrace.ClientTrace
// in the request context rather than replacing it.
type Transport struct {
	base http.RoundTripper
	cfg  config
}

// NewTransport returns a Transport around base. A nil base uses
// http.DefaultTransport.
func NewTransport(base http.RoundTripper, opts ...Option) *Transport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &Transport{base: base, cfg: newConfig(opts)}
}

// RoundTrip executes a single HTTP transaction and records its span. The
// span ends when RoundTrip returns: response-body transfer time is outside
// the span, marked instead by the first_response_byte event. Redirects and
// retries performed by an http.Client appear as sibling spans, one per
// attempt, because the client re-invokes the transport with the caller's
// original request.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.cfg.tracer.Enabled() {
		return t.base.RoundTrip(req)
	}
	if req.Context().Value(roundTripKey{}) != nil {
		// A wrapping trailhttp transport already opened the span for this
		// attempt; recording another would duplicate it.
		return t.base.RoundTrip(req)
	}

	name := req.Method
	if req.URL.Host != "" {
		name = req.Method + " " + req.URL.Host
	}
	attrs := []trail.Attribute{trail.String("http.request.method", req.Method)}
	if req.URL.Scheme != "" {
		attrs = append(attrs, trail.String("url.scheme", req.URL.Scheme))
	}
	if host := req.URL.Hostname(); host != "" {
		attrs = append(attrs, trail.String("server.address", host))
	}
	if port := req.URL.Port(); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			attrs = append(attrs, trail.Int("server.port", p))
		}
	}
	if req.URL.Path != "" {
		attrs = append(attrs, trail.String("url.path", req.URL.Path))
	}
	if t.cfg.queryString && req.URL.RawQuery != "" {
		attrs = append(attrs, trail.String("url.query", req.URL.RawQuery))
	}

	ctx, span := t.cfg.tracer.Start(req.Context(), name, trail.WithAttributes(attrs...))
	trace := &phaseRecorder{
		span:        span,
		peerAddress: t.cfg.peerAddress,
	}
	traced := httptrace.WithClientTrace(
		context.WithValue(ctx, roundTripKey{}, struct{}{}),
		trace.clientTrace(),
	)
	resp, err := t.base.RoundTrip(req.WithContext(traced))
	if err != nil {
		// Caller-initiated cancellation is not a failure of the operation
		// being traced; everything else, including deadlines, is.
		if !errors.Is(err, context.Canceled) {
			span.SetStatus(trail.StatusError, "")
		}
		if t.cfg.errorDetails {
			span.RecordError(err)
		}
	} else {
		updates := []trail.Attribute{trail.Int("http.response.status_code", resp.StatusCode)}
		if resp.ContentLength >= 0 {
			updates = append(updates, trail.Int64("http.response.content_length", resp.ContentLength))
		}
		span.SetAttributes(updates...)
		if resp.StatusCode >= 400 {
			span.SetStatus(trail.StatusError, "")
		}
	}
	span.End()
	return resp, err
}

// CloseIdleConnections forwards to the base transport when it supports it.
func (t *Transport) CloseIdleConnections() {
	type closeIdler interface{ CloseIdleConnections() }
	if ci, ok := t.base.(closeIdler); ok {
		ci.CloseIdleConnections()
	}
}

// phaseRecorder turns httptrace callbacks into bounded events on the
// request span. Callbacks may fire from other goroutines, so phase start
// times are guarded by a mutex.
type phaseRecorder struct {
	span        trail.Span
	peerAddress bool

	mu        sync.Mutex
	dnsStart  time.Time
	connStart time.Time
	tlsStart  time.Time
}

func (p *phaseRecorder) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.dnsStart = time.Now()
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			p.mu.Lock()
			start := p.dnsStart
			p.dnsStart = time.Time{}
			p.mu.Unlock()
			if !start.IsZero() {
				p.span.AddEvent("dns_done", trail.WithAttributes(
					trail.Duration("duration", time.Since(start)),
				))
			}
		},
		ConnectStart: func(string, string) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.connStart = time.Now()
		},
		ConnectDone: func(string, string, error) {
			p.mu.Lock()
			start := p.connStart
			p.connStart = time.Time{}
			p.mu.Unlock()
			if !start.IsZero() {
				p.span.AddEvent("connect_done", trail.WithAttributes(
					trail.Duration("duration", time.Since(start)),
				))
			}
		},
		TLSHandshakeStart: func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.tlsStart = time.Now()
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			p.mu.Lock()
			start := p.tlsStart
			p.tlsStart = time.Time{}
			p.mu.Unlock()
			if !start.IsZero() {
				p.span.AddEvent("tls_done", trail.WithAttributes(
					trail.Duration("duration", time.Since(start)),
				))
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			attrs := []trail.Attribute{trail.Bool("reused", info.Reused)}
			if info.WasIdle {
				attrs = append(attrs, trail.Duration("idle", info.IdleTime))
			}
			if p.peerAddress && info.Conn != nil {
				attrs = append(attrs, trail.String("network.peer.address", info.Conn.RemoteAddr().String()))
			}
			p.span.AddEvent("got_conn", trail.WithAttributes(attrs...))
		},
		GotFirstResponseByte: func() {
			p.span.AddEvent("first_response_byte")
		},
	}
}
