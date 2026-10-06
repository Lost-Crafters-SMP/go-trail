package trailhttp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.lostcrafters.com/trail"
	"go.lostcrafters.com/trail/instrumentation/trailslog"
)

func TestMiddlewareRecordsRequestSpan(t *testing.T) {
	tracer, sink := newTestTracer(t)
	// The middleware wraps the mux from the outside: ServeMux matches after
	// the span starts, so the name stays method-only and the pattern is
	// recorded as http.route.
	mux := http.NewServeMux()
	mux.HandleFunc("/users/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, "hello"); err != nil {
			t.Errorf("write: %v", err)
		}
	})
	server := httptest.NewServer(NewMiddleware(WithTracer(tracer))(mux))
	defer server.Close()

	resp, err := http.Get(server.URL + "/users/7")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}

	start := singleStart(t, sink)
	if start.Name != "GET" {
		t.Fatalf("span name = %q, want method-only for outer-wrapped mux", start.Name)
	}
	host, _, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("split server url: %v", err)
	}
	attrs := attrsFor(sink, start.SpanID)
	if attrs["http.request.method"].String() != http.MethodGet {
		t.Fatalf("method = %v", attrs["http.request.method"].String())
	}
	if attrs["url.scheme"].String() != "http" {
		t.Fatalf("scheme = %v", attrs["url.scheme"].String())
	}
	if attrs["url.path"].String() != "/users/7" {
		t.Fatalf("path = %v", attrs["url.path"].String())
	}
	if attrs["http.route"].String() != "/users/{id}" {
		t.Fatalf("route = %v", attrs["http.route"].String())
	}
	if attrs["server.address"].String() != host {
		t.Fatalf("server.address = %v", attrs["server.address"].String())
	}
	if got := attrs["server.port"].Int64(); got == 0 || attrs["server.port"].Kind() != trail.KindInt64 {
		t.Fatalf("server.port = %d", got)
	}
	if attrs["network.protocol.version"].String() != "1.1" {
		t.Fatalf("protocol version = %v", attrs["network.protocol.version"].String())
	}
	if got := attrs["http.response.status_code"].Int64(); got != 200 {
		t.Fatalf("status code = %d", got)
	}
	if got := attrs["http.response.body.size"].Int64(); got != 5 {
		t.Fatalf("body size = %d, want 5", got)
	}
	if st := statusFor(sink, start.SpanID); st != nil {
		t.Fatalf("status on 2xx = %+v, want unset", st)
	}
	if len(spanEnds(sink)) != 1 {
		t.Fatalf("span ends = %d, want 1", len(spanEnds(sink)))
	}
}

func TestMiddlewareInsideMuxRouteNamed(t *testing.T) {
	tracer, sink := newTestTracer(t)
	// Mounted inside the route, the middleware sees the pattern the mux
	// matched before the span starts, so the span is named with it.
	mw := NewMiddleware(WithTracer(tracer))
	mux := http.NewServeMux()
	mux.Handle("/jobs/{job}", mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "ok"); err != nil {
			t.Errorf("write: %v", err)
		}
	})))
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Post(server.URL+"/jobs/42", "text/plain", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if start := singleStart(t, sink); start.Name != "POST /jobs/{job}" {
		t.Fatalf("span name = %q, want \"POST /jobs/{job}\"", start.Name)
	}
}

func TestMiddlewareNoRouteMethodOnly(t *testing.T) {
	tracer, sink := newTestTracer(t)
	handler := NewMiddleware(WithTracer(tracer))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "ok"); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/anything/at/all", nil)
	handler.ServeHTTP(rec, req)

	if start := singleStart(t, sink); start.Name != http.MethodPost {
		t.Fatalf("span name = %q, want method only", start.Name)
	}
	attrs := attrsFor(sink, singleStart(t, sink).SpanID)
	if _, ok := attrs["http.route"]; ok {
		t.Fatal("http.route recorded without a route")
	}
	if attrs["url.path"].String() != "/anything/at/all" {
		t.Fatalf("path = %v", attrs["url.path"].String())
	}
}

func TestMiddlewareRouteExtractorPrecedence(t *testing.T) {
	tracer, sink := newTestTracer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/users/{id}", func(http.ResponseWriter, *http.Request) {})
	mw := NewMiddleware(
		WithTracer(tracer),
		WithRouteExtractor(func(*http.Request) string { return "/custom/route" }),
	)
	server := httptest.NewServer(mw(mux))
	defer server.Close()

	resp, err := http.Get(server.URL + "/users/9")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}

	start := singleStart(t, sink)
	if start.Name != "GET /custom/route" {
		t.Fatalf("span name = %q, want extractor precedence", start.Name)
	}
	if attrs := attrsFor(sink, start.SpanID); attrs["http.route"].String() != "/custom/route" {
		t.Fatalf("http.route = %v", attrs["http.route"].String())
	}
}

func TestMiddlewareStatusClassification(t *testing.T) {
	tests := []struct {
		name       string
		handler    func(http.ResponseWriter, *http.Request)
		wantStatus *trail.SpanStatus
	}{
		{
			name:    "not found stays unset",
			handler: func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) },
		},
		{
			name: "server error marks error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
			wantStatus: &trail.SpanStatus{Code: trail.StatusError},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tracer, sink := newTestTracer(t)
			handler := NewMiddleware(WithTracer(tracer))(http.HandlerFunc(tc.handler))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
			start := singleStart(t, sink)
			got := statusFor(sink, start.SpanID)
			switch {
			case tc.wantStatus == nil && got != nil:
				t.Fatalf("status = %+v, want unset", got)
			case tc.wantStatus != nil && (got == nil || got.Code != tc.wantStatus.Code):
				t.Fatalf("status = %+v, want %+v", got, tc.wantStatus)
			}
			if attrs := attrsFor(sink, start.SpanID); attrs["http.response.status_code"].Int64() != int64(rec.Code) {
				t.Fatalf("status code attr = %d, want %d", attrs["http.response.status_code"].Int64(), rec.Code)
			}
		})
	}
}

func TestMiddlewareDefaultStatus200(t *testing.T) {
	tracer, sink := newTestTracer(t)
	handler := NewMiddleware(WithTracer(tracer))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "body-without-writeheader"); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	start := singleStart(t, sink)
	if got := attrsFor(sink, start.SpanID)["http.response.status_code"].Int64(); got != 200 {
		t.Fatalf("status code = %d, want implicit 200", got)
	}
	if got := attrsFor(sink, start.SpanID)["http.response.body.size"].Int64(); got != int64(len("body-without-writeheader")) {
		t.Fatalf("body size = %d", got)
	}
}

func TestMiddlewarePanicRecordedAndRepanicked(t *testing.T) {
	tracer, sink := newTestTracer(t)
	var repanicked any
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { repanicked = recover() }()
		panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("handler exploded")
		})
		NewMiddleware(WithTracer(tracer))(panicking).ServeHTTP(w, r)
	})
	rec := httptest.NewRecorder()
	outer.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if repanicked != "handler exploded" {
		t.Fatalf("panic value = %v, want unchanged re-panic", repanicked)
	}
	start := singleStart(t, sink)
	if st := statusFor(sink, start.SpanID); st == nil || st.Code != trail.StatusError || st.Description != "panic" {
		t.Fatalf("status = %+v, want Error \"panic\"", st)
	}
	events := spanEvents(sink, start.SpanID)
	if len(events) != 1 || events[0].Name != "panic" {
		t.Fatalf("events = %+v, want single value-free panic event", events)
	}
	for _, e := range events {
		for _, a := range e.Attributes {
			if strings.Contains(a.String(), "exploded") {
				t.Fatalf("panic value recorded by default: %+v", e)
			}
		}
	}
	if len(spanEnds(sink)) != 1 {
		t.Fatal("panicked request did not end its span")
	}
}

func TestMiddlewarePanicErrorDetailsOptIn(t *testing.T) {
	tracer, sink := newTestTracer(t)
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret-panic") })
	handler := NewMiddleware(WithTracer(tracer), WithErrorDetails())(panicking)
	rec := httptest.NewRecorder()
	func() {
		defer func() { _ = recover() }()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	}()
	start := singleStart(t, sink)
	found := false
	for _, e := range spanEvents(sink, start.SpanID) {
		if e.Name == "error" {
			found = true
		}
	}
	if !found {
		t.Fatal("opt-in error details did not record panic text")
	}
}

// flushableStub supports Flush for optional-interface tests.
type flushableStub struct {
	http.ResponseWriter
	flushes int
}

func (s *flushableStub) Flush() { s.flushes++ }

// hijackableStub supports Hijack for optional-interface tests.
type hijackableStub struct {
	http.ResponseWriter
	hijacked bool
}

func (s *hijackableStub) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	s.hijacked = true
	return nil, nil, nil
}

func TestMiddlewarePreservesOptionalInterfaces(t *testing.T) {
	tracer, _ := newTestTracer(t)
	stub := &flushableStub{ResponseWriter: httptest.NewRecorder()}
	controllerFlushed := false
	handler := NewMiddleware(WithTracer(tracer))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if err := http.NewResponseController(w).Flush(); err == nil {
			controllerFlushed = true
		}
	}))
	handler.ServeHTTP(stub, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !controllerFlushed {
		t.Fatal("ResponseController.Flush failed through middleware")
	}
	if stub.flushes != 2 {
		t.Fatalf("underlying flushes = %d, want 2 (direct + controller)", stub.flushes)
	}

	hijackStub := &hijackableStub{ResponseWriter: httptest.NewRecorder()}
	handler2 := NewMiddleware(WithTracer(tracer))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h, ok := w.(http.Hijacker)
		if !ok {
			t.Error("direct http.Hijacker assertion failed through middleware")
			return
		}
		if _, _, err := h.Hijack(); err != nil {
			t.Errorf("hijack: %v", err)
		}
	}))
	handler2.ServeHTTP(hijackStub, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !hijackStub.hijacked {
		t.Fatal("Hijack did not reach the underlying writer")
	}
}

// TestMiddlewareIOCopyCompatibility verifies the common io.Copy handler
// shape works through the middleware without an io.ReaderFrom
// implementation: the copy falls back to the Write loop, which preserves
// both bytes and the recorded body size.
func TestMiddlewareIOCopyCompatibility(t *testing.T) {
	tracer, sink := newTestTracer(t)
	payload := strings.Repeat("copy-chunk-", 512)
	handler := NewMiddleware(WithTracer(tracer))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.Copy(w, strings.NewReader(payload)); err != nil {
			t.Errorf("copy: %v", err)
		}
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Body.String() != payload {
		t.Fatalf("copied body length = %d, want %d", rec.Body.Len(), len(payload))
	}
	start := singleStart(t, sink)
	if got := attrsFor(sink, start.SpanID)["http.response.body.size"].Int64(); got != int64(len(payload)) {
		t.Fatalf("recorded body size = %d, want %d", got, len(payload))
	}
}

func TestMiddlewareContextPropagation(t *testing.T) {
	tracer, sink := newTestTracer(t)
	var logs bytes.Buffer
	logger := slog.New(trailslog.NewHandler(slog.NewJSONHandler(&logs, nil)))
	handler := NewMiddleware(WithTracer(tracer))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, child := tracer.Start(r.Context(), "child-op")
		child.End()
		logger.InfoContext(r.Context(), "handling")
		if _, err := io.WriteString(w, "ok"); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	starts := spanStarts(sink)
	if len(starts) != 2 {
		t.Fatalf("span starts = %d, want server + child", len(starts))
	}
	var server, child *trail.SpanStart
	for i := range starts {
		switch starts[i].Name {
		case "GET":
			server = &starts[i]
		case "child-op":
			child = &starts[i]
		}
	}
	if server == nil || child == nil {
		t.Fatalf("missing server or child span: %+v", starts)
	}
	if child.ParentSpanID != server.SpanID {
		t.Fatalf("child parent = %v, want server span %v", child.ParentSpanID, server.SpanID)
	}
	var line map[string]any
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if line["trace_id"] != server.TraceID.String() {
		t.Fatalf("log trace_id = %v, want %v", line["trace_id"], server.TraceID.String())
	}
	if line["span_id"] != server.SpanID.String() {
		t.Fatalf("log span_id = %v, want %v", line["span_id"], server.SpanID.String())
	}
}

func TestMiddlewareDisabledRecordsNothing(t *testing.T) {
	handler := NewMiddleware(WithTracer(trail.Tracer{}))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		if _, err := io.WriteString(w, "short"); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusTeapot || rec.Body.String() != "short" {
		t.Fatalf("disabled middleware changed behavior: %d %q", rec.Code, rec.Body.String())
	}
}

func TestMiddlewarePrivacyDefaults(t *testing.T) {
	tracer, sink := newTestTracer(t)
	handler := NewMiddleware(WithTracer(tracer))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "response-body-secret"); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "/path?token=query-secret&sig=abc", nil)
	req.Header.Set("Authorization", "Bearer header-secret")
	req.Header.Set("Cookie", "session=cookie-secret")
	req.Header.Set("X-Custom", "custom-header-secret")
	req.SetBasicAuth("user-name-secret", "hunter2")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assertNoRecordText(t, sink,
		"query-secret", "header-secret", "cookie-secret", "custom-header-secret",
		"user-name-secret", "hunter2", "response-body-secret", "Authorization", "Cookie",
	)
	if attrs := attrsFor(sink, singleStart(t, sink).SpanID); attrs["url.path"].String() != "/path" {
		t.Fatalf("path = %v", attrs["url.path"].String())
	}
}

func TestMiddlewareHostWithoutPort(t *testing.T) {
	host, port, ok := splitHostPort("example.com")
	if !ok || host != "example.com" || port != 0 {
		t.Fatalf("splitHostPort = %q,%d,%t", host, port, ok)
	}
	host, port, ok = splitHostPort("example.com:8080")
	if !ok || host != "example.com" || port != 8080 {
		t.Fatalf("splitHostPort = %q,%d,%t", host, port, ok)
	}
	if _, _, ok := splitHostPort(""); ok {
		t.Fatal("empty host accepted")
	}
}

func TestMiddlewareConcurrentRequests(t *testing.T) {
	tracer, sink := newTestTracer(t)
	handler := NewMiddleware(WithTracer(tracer))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "ok"); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
			if rec.Code != 200 {
				t.Errorf("status = %d", rec.Code)
			}
		}()
	}
	wg.Wait()
	if got := len(spanStarts(sink)); got != 8 {
		t.Fatalf("span starts = %d, want 8", got)
	}
	if got := len(spanEnds(sink)); got != 8 {
		t.Fatalf("span ends = %d, want 8", got)
	}
}

func BenchmarkMiddlewareDisabled(b *testing.B) {
	handler := NewMiddleware(WithTracer(trail.Tracer{}))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/bench", nil)
	rec := httptest.NewRecorder()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		handler.ServeHTTP(rec, req)
	}
}
