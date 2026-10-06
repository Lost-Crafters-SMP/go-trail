package trailhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"go.lostcrafters.com/trail"
)

func newTestClient(t *testing.T, tracer trail.Tracer, opts ...Option) *http.Client {
	t.Helper()
	return &http.Client{
		Transport: NewTransport(&http.Transport{}, append([]Option{WithTracer(tracer)}, opts...)...),
	}
}

// okHandler serves a fixed body for tests that do not inspect it.
func okHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "ok"); err != nil {
			t.Errorf("write: %v", err)
		}
	}
}

// drainAndClose consumes and closes a response body.
func drainAndClose(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp == nil {
		return
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Errorf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close body: %v", err)
	}
}

func TestTransportRecordsRequestSpan(t *testing.T) {
	tracer, sink := newTestTracer(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "payload"); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	client := newTestClient(t, tracer)
	resp, err := client.Get(server.URL + "/resources/first")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}

	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("split server url: %v", err)
	}
	start := singleStart(t, sink)
	if want := "GET " + host + ":" + port; start.Name != want {
		t.Fatalf("span name = %q, want %q", start.Name, want)
	}
	attrs := attrsFor(sink, start.SpanID)
	if attrs["http.request.method"].String() != http.MethodGet {
		t.Fatalf("method = %v", attrs["http.request.method"].String())
	}
	if attrs["url.scheme"].String() != "http" {
		t.Fatalf("scheme = %v", attrs["url.scheme"].String())
	}
	if attrs["server.address"].String() != host {
		t.Fatalf("server.address = %v", attrs["server.address"].String())
	}
	if attrs["server.port"].Int64() != int64(mustAtoi(t, port)) {
		t.Fatalf("server.port = %d", attrs["server.port"].Int64())
	}
	if attrs["url.path"].String() != "/resources/first" {
		t.Fatalf("path = %v", attrs["url.path"].String())
	}
	if got := attrs["http.response.status_code"].Int64(); got != 200 {
		t.Fatalf("status code = %d", got)
	}
	if got := attrs["http.response.content_length"].Int64(); got != int64(len("payload")) {
		t.Fatalf("content length = %d", got)
	}
	if st := statusFor(sink, start.SpanID); st != nil {
		t.Fatalf("status on 2xx = %+v, want unset", st)
	}
	events := eventNames(sink, start.SpanID)
	if !contains(events, "got_conn") || !contains(events, "first_response_byte") {
		t.Fatalf("phase events = %v, want got_conn and first_response_byte", events)
	}
	if reused, ok := eventAttr(sink, start.SpanID, "got_conn", "reused"); ok && reused.Bool() {
		t.Fatal("fresh connection reported reused")
	}
	if len(spanEnds(sink)) != 1 {
		t.Fatalf("span ends = %d, want 1", len(spanEnds(sink)))
	}
}

func TestTransportWarmConnectionReused(t *testing.T) {
	tracer, sink := newTestTracer(t)
	server := httptest.NewServer(okHandler(t))
	defer server.Close()

	client := newTestClient(t, tracer)
	for range 2 {
		resp, err := client.Get(server.URL + "/warm")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if _, err := io.ReadAll(resp.Body); err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close body: %v", err)
		}
	}
	starts := spanStarts(sink)
	if len(starts) != 2 {
		t.Fatalf("span starts = %d, want 2", len(starts))
	}
	reused, ok := eventAttr(sink, starts[1].SpanID, "got_conn", "reused")
	if !ok || !reused.Bool() {
		t.Fatal("second request did not report a reused connection")
	}
	if _, ok := eventAttr(sink, starts[1].SpanID, "got_conn", "idle"); !ok {
		t.Fatal("idle duration missing on reused connection")
	}
}

func TestTransport4xxMarksError(t *testing.T) {
	tracer, sink := newTestTracer(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer server.Close()

	client := newTestClient(t, tracer)
	resp, err := client.Get(server.URL + "/missing")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	drainAndClose(t, resp)

	start := singleStart(t, sink)
	if st := statusFor(sink, start.SpanID); st == nil || st.Code != trail.StatusError {
		t.Fatalf("status = %+v, want Error for 4xx", st)
	}
	if got := attrsFor(sink, start.SpanID)["http.response.status_code"].Int64(); got != 404 {
		t.Fatalf("status code = %d", got)
	}
}

func TestTransportConnectionError(t *testing.T) {
	tracer, sink := newTestTracer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	client := newTestClient(t, tracer)
	if _, err := client.Get("http://" + addr + "/unreachable"); err == nil {
		t.Fatal("request to closed address succeeded")
	}
	start := singleStart(t, sink)
	if st := statusFor(sink, start.SpanID); st == nil || st.Code != trail.StatusError {
		t.Fatalf("status = %+v, want Error for transport failure", st)
	}
	if len(spanEnds(sink)) != 1 {
		t.Fatal("failed transport did not end its span")
	}
}

func TestTransportCallerCancellationLeavesUnset(t *testing.T) {
	tracer, sink := newTestTracer(t)
	server := httptest.NewServer(okHandler(t))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/canceled", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := newTestClient(t, tracer).Do(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	start := singleStart(t, sink)
	if st := statusFor(sink, start.SpanID); st != nil {
		t.Fatalf("status = %+v, want unset for caller cancellation", st)
	}
	if len(spanEnds(sink)) != 1 {
		t.Fatal("canceled request did not end its span")
	}
}

func TestTransportDeadlineExceededMarksError(t *testing.T) {
	tracer, sink := newTestTracer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:1/deadline", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := newTestClient(t, tracer).Do(req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	start := singleStart(t, sink)
	if st := statusFor(sink, start.SpanID); st == nil || st.Code != trail.StatusError {
		t.Fatalf("status = %+v, want Error for deadline", st)
	}
}

func TestTransportQueryStringOptInAndPrivacy(t *testing.T) {
	tracer, sink := newTestTracer(t)
	server := httptest.NewServer(okHandler(t))
	defer server.Close()

	resp, err := newTestClient(t, tracer).Get(server.URL + "/search?token=query-secret&page=2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	drainAndClose(t, resp)
	start := singleStart(t, sink)
	if _, ok := attrsFor(sink, start.SpanID)["url.query"]; ok {
		t.Fatal("url.query recorded without WithQueryString")
	}
	assertNoRecordText(t, sink, "query-secret")

	tracer2, sink2 := newTestTracer(t)
	client := newTestClient(t, tracer2, WithQueryString())
	resp, err = client.Get(server.URL + "/search?token=query-secret&page=2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	drainAndClose(t, resp)
	start2 := singleStart(t, sink2)
	if got := attrsFor(sink2, start2.SpanID)["url.query"].String(); got != "token=query-secret&page=2" {
		t.Fatalf("url.query = %q", got)
	}
}

func TestTransportUserInfoNeverRecorded(t *testing.T) {
	tracer, sink := newTestTracer(t)
	server := httptest.NewServer(okHandler(t))
	defer server.Close()

	target := strings.Replace(server.URL, "http://", "http://user-name-secret:hunter2@", 1)
	resp, err := newTestClient(t, tracer).Get(target + "/userinfo?token=query-secret")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	drainAndClose(t, resp)
	singleStart(t, sink) // one span, request itself succeeded
	assertNoRecordText(t, sink, "user-name-secret", "hunter2", "query-secret")
}

func TestTransportPeerAddressOptIn(t *testing.T) {
	tracer, sink := newTestTracer(t)
	server := httptest.NewServer(okHandler(t))
	defer server.Close()

	client := newTestClient(t, tracer, WithPeerAddress())
	resp, err := client.Get(server.URL + "/peer")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	drainAndClose(t, resp)

	start := singleStart(t, sink)
	peerAttr, ok := eventAttr(sink, start.SpanID, "got_conn", "network.peer.address")
	if !ok || peerAttr.String() == "" {
		t.Fatal("network.peer.address missing with WithPeerAddress")
	}
	peer := peerAttr.String()
	if !strings.HasPrefix(peer, "127.0.0.1") {
		t.Fatalf("peer address = %q", peer)
	}

	tracer2, sink2 := newTestTracer(t)
	resp, err = newTestClient(t, tracer2).Get(server.URL + "/peer")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	drainAndClose(t, resp)
	start2 := singleStart(t, sink2)
	if _, ok := eventAttr(sink2, start2.SpanID, "got_conn", "network.peer.address"); ok {
		t.Fatal("peer address recorded without WithPeerAddress")
	}
}

func TestTransportErrorDetailsOptIn(t *testing.T) {
	tracer, sink := newTestTracer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if _, err := newTestClient(t, tracer).Get("http://" + addr + "/x"); err == nil {
		t.Fatal("request succeeded")
	}
	start := singleStart(t, sink)
	for _, e := range spanEvents(sink, start.SpanID) {
		if e.Name == "error" {
			t.Fatalf("error event without WithErrorDetails: %+v", e)
		}
	}

	tracer2, sink2 := newTestTracer(t)
	if _, err := newTestClient(t, tracer2, WithErrorDetails()).Get("http://" + addr + "/x"); err == nil {
		t.Fatal("request succeeded")
	}
	start2 := singleStart(t, sink2)
	found := false
	for _, e := range spanEvents(sink2, start2.SpanID) {
		if e.Name == "error" {
			found = true
		}
	}
	if !found {
		t.Fatal("opt-in error details did not record the failure")
	}
}

func TestTransportStackedNoDuplicateSpans(t *testing.T) {
	tracer, sink := newTestTracer(t)
	server := httptest.NewServer(okHandler(t))
	defer server.Close()

	inner := NewTransport(&http.Transport{}, WithTracer(tracer))
	outer := NewTransport(inner)
	client := &http.Client{Transport: outer}
	resp, err := client.Get(server.URL + "/stacked")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	drainAndClose(t, resp)
	singleStart(t, sink)
}

func TestTransportComposesUserClientTrace(t *testing.T) {
	tracer, sink := newTestTracer(t)
	server := httptest.NewServer(okHandler(t))
	defer server.Close()

	var userConnects atomic.Int32
	req, err := http.NewRequest(http.MethodGet, server.URL+"/composed", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	ctx := httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		ConnectStart: func(string, string) { userConnects.Add(1) },
	})
	*req = *req.WithContext(ctx)

	resp, err := newTestClient(t, tracer).Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	drainAndClose(t, resp)

	if userConnects.Load() == 0 {
		t.Fatal("user ClientTrace hooks were dropped by the transport")
	}
	start := singleStart(t, sink)
	if !contains(eventNames(sink, start.SpanID), "connect_done") {
		t.Fatalf("phase events missing alongside user trace: %v", eventNames(sink, start.SpanID))
	}
}

func TestTransportMarkerDoesNotLeak(t *testing.T) {
	tracer, _ := newTestTracer(t)
	server := httptest.NewServer(okHandler(t))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/marker", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	transport := NewTransport(&http.Transport{}, WithTracer(tracer))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	drainAndClose(t, resp)
	if req.Context().Value(roundTripKey{}) != nil {
		t.Fatal("dedup marker leaked into the caller's request context")
	}
}

func TestTransportSpanEndsBeforeBodyConsumed(t *testing.T) {
	tracer, sink := newTestTracer(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "slow-body"); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	transport := NewTransport(&http.Transport{}, WithTracer(tracer))
	req, err := http.NewRequest(http.MethodGet, server.URL+"/body", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if got := len(spanEnds(sink)); got != 1 {
		t.Fatalf("span ends before body read = %d, want 1", got)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if got := len(spanEnds(sink)); got != 1 {
		t.Fatalf("body read disturbed span accounting: %d", got)
	}
}

func TestTransportRedirectsAreSiblingSpans(t *testing.T) {
	tracer, sink := newTestTracer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/b", http.StatusFound)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "landed"); err != nil {
			t.Errorf("write: %v", err)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := newTestClient(t, tracer).Get(server.URL + "/a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	starts := spanStarts(sink)
	if len(starts) != 2 {
		t.Fatalf("span starts = %d, want one per redirect attempt", len(starts))
	}
	for _, s := range starts {
		if s.ParentSpanID.IsValid() {
			t.Fatalf("redirect span unexpectedly parented: %+v", s)
		}
	}
}

func TestTransportCloseIdleConnectionsPassthrough(t *testing.T) {
	base := &recordingTransport{RoundTripper: &http.Transport{}}
	transport := NewTransport(base)
	transport.CloseIdleConnections()
	if base.closeIdleCalls.Load() != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1", base.closeIdleCalls.Load())
	}
}

type recordingTransport struct {
	RoundTripper   http.RoundTripper
	closeIdleCalls atomic.Int32
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.RoundTripper.RoundTrip(req)
}

func (t *recordingTransport) CloseIdleConnections() {
	t.closeIdleCalls.Add(1)
}

func TestTransportDisabledRecordsNothing(t *testing.T) {
	server := httptest.NewServer(okHandler(t))
	defer server.Close()

	client := &http.Client{Transport: NewTransport(&http.Transport{}, WithTracer(trail.Tracer{}))}
	resp, err := client.Get(server.URL + "/plain")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
}

func TestTransportConcurrentRequests(t *testing.T) {
	tracer, sink := newTestTracer(t)
	server := httptest.NewServer(okHandler(t))
	defer server.Close()

	client := newTestClient(t, tracer)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(server.URL + "/concurrent")
			if err != nil {
				t.Errorf("get: %v", err)
				return
			}
			if _, err := io.ReadAll(resp.Body); err != nil {
				t.Errorf("read body: %v", err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Errorf("close body: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := len(spanStarts(sink)); got != 4 {
		t.Fatalf("span starts = %d, want 4", got)
	}
	if got := len(spanEnds(sink)); got != 4 {
		t.Fatalf("span ends = %d, want 4", got)
	}
}

func eventNames(s *recordingSink, spanID trail.SpanID) []string {
	var out []string
	for _, e := range spanEvents(s, spanID) {
		out = append(out, e.Name)
	}
	return out
}

func eventAttr(s *recordingSink, spanID trail.SpanID, eventName, key string) (trail.Attribute, bool) {
	for _, e := range spanEvents(s, spanID) {
		if e.Name != eventName {
			continue
		}
		for _, a := range e.Attributes {
			if a.Key() == key {
				return a, true
			}
		}
	}
	return trail.Attribute{}, false
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("bad port %q: %v", s, err)
	}
	return n
}

func BenchmarkTransportDisabled(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "ok"); err != nil {
			b.Errorf("write: %v", err)
		}
	}))
	defer server.Close()
	client := &http.Client{Transport: NewTransport(&http.Transport{}, WithTracer(trail.Tracer{}))}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		resp, err := client.Get(server.URL + "/bench")
		if err != nil {
			b.Fatalf("get: %v", err)
		}
		if _, err := io.ReadAll(resp.Body); err != nil {
			b.Fatalf("read body: %v", err)
		}
		if err := resp.Body.Close(); err != nil {
			b.Fatalf("close: %v", err)
		}
	}
}
