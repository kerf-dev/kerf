package tunnel

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var quiet = slog.New(slog.DiscardHandler)

func TestAcceptKey(t *testing.T) {
	// RFC 6455, section 1.3.
	if got := acceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("acceptKey = %q", got)
	}
}

func TestListenerHandshake(t *testing.T) {
	upgrade := func(r *http.Request) {
		r.Header.Set("Connection", "keep-alive, Upgrade")
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	}
	tests := []struct {
		name   string
		token  string // configured on the agent
		ready  bool
		method string
		path   string
		mod    func(*http.Request)
		want   int
	}{
		{name: "ok", token: "s3cret", ready: true, mod: func(r *http.Request) { upgrade(r); r.Header.Set("Authorization", "Bearer s3cret") }, want: http.StatusSwitchingProtocols},
		{name: "wrong token", token: "s3cret", ready: true, mod: func(r *http.Request) { upgrade(r); r.Header.Set("Authorization", "Bearer nope") }, want: http.StatusUnauthorized},
		{name: "no token sent", token: "s3cret", ready: true, mod: upgrade, want: http.StatusUnauthorized},
		{name: "no token configured", token: "", ready: true, mod: func(r *http.Request) { upgrade(r); r.Header.Set("Authorization", "Bearer ") }, want: http.StatusServiceUnavailable},
		{name: "not the leader", token: "s3cret", ready: false, mod: func(r *http.Request) { upgrade(r); r.Header.Set("Authorization", "Bearer s3cret") }, want: http.StatusServiceUnavailable},
		{name: "no upgrade", token: "s3cret", ready: true, mod: func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }, want: http.StatusBadRequest},
		{name: "post", token: "s3cret", ready: true, method: http.MethodPost, mod: upgrade, want: http.StatusMethodNotAllowed},
		{name: "other path", token: "s3cret", ready: true, path: "/healthz", mod: upgrade, want: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewListener("", StaticToken(tt.token), func() bool { return tt.ready }, quiet)
			srv := httptest.NewServer(l)
			defer srv.Close()
			method, path := tt.method, tt.path
			if method == "" {
				method = http.MethodGet
			}
			if path == "" {
				path = Path
			}
			req, _ := http.NewRequest(method, srv.URL+path, nil)
			tt.mod(req)
			resp, err := http.DefaultTransport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
			if tt.want == http.StatusSwitchingProtocols && resp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
				t.Fatalf("Sec-WebSocket-Accept = %q", resp.Header.Get("Sec-WebSocket-Accept"))
			}
		})
	}
}

// pair starts an agent listener and a hub dialer serving h; it returns the agent's hub client.
func pair(t *testing.T, agentToken, hubToken string, conns int, h http.Handler) (*http.Client, *Listener, context.CancelFunc) {
	t.Helper()
	l := NewListener("", StaticToken(agentToken), nil, quiet)
	srv := httptest.NewServer(l)
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dialer{Cluster: "c1", URL: srv.URL, Token: StaticToken(hubToken), Connections: conns, Handler: h, Log: quiet}
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; srv.Close() })
	return l.Client(), l, cancel
}

func TestRoundTrip(t *testing.T) {
	var inFlight, peak atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("X-Path", r.URL.Path)
		_, _ = io.WriteString(w, r.Header.Get("Authorization"))
	})
	client, _, _ := pair(t, "hub-token", "hub-token", 2, h)

	// More concurrent requests than connections: they wait for a free connection, none fails.
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, BaseURL+"/v1/clusters/c1/config", nil)
			req.Header.Set("Authorization", "Bearer agent-token")
			resp, err := client.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK || string(b) != "Bearer agent-token" || resp.Header.Get("X-Path") != "/v1/clusters/c1/config" {
				errs <- &net.OpError{Op: "check", Err: io.ErrUnexpectedEOF}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if p := peak.Load(); p > 2 {
		t.Fatalf("peak concurrent requests = %d, want <= 2 (the connections)", p)
	}
}

func TestWrongTokenNeverConnects(t *testing.T) {
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("handler reached with a wrong tunnel token") })
	client, _, _ := pair(t, "agent-side", "hub-side", 1, h)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/x", nil)
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("request succeeded without a valid tunnel")
	}
}

func TestReconnectAfterClose(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	client, _, _ := pair(t, "t", "t", 1, h)
	for i := range 3 {
		req, _ := http.NewRequest(http.MethodGet, BaseURL+"/x", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		// Drop the connection: the hub must dial a new one for the next request.
		client.CloseIdleConnections()
	}
}

func TestDialSkipsStale(t *testing.T) {
	l := NewListener("", StaticToken("t"), nil, quiet)
	l.MaxIdle = 10 * time.Millisecond
	staleA, staleB := net.Pipe()
	defer staleB.Close()
	freshA, freshB := net.Pipe()
	defer freshB.Close()
	l.pool <- pooled{c: staleA, at: time.Now().Add(-time.Second)}
	l.pool <- pooled{c: freshA, at: time.Now()}
	c, err := l.DialContext(context.Background(), "tcp", "hub.tunnel:80")
	if err != nil {
		t.Fatal(err)
	}
	if c != freshA {
		t.Fatal("DialContext returned the stale connection")
	}
	// The stale one was closed.
	if _, err := staleB.Write([]byte("x")); err == nil {
		t.Fatal("stale connection still open")
	}
}

func TestDialWaitsForContext(t *testing.T) {
	l := NewListener("", StaticToken("t"), nil, quiet)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.DialContext(ctx, "tcp", "hub.tunnel:80"); err == nil {
		t.Fatal("DialContext returned without a connection")
	}
}

func TestDialerRejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "ftp://x", "http://", "::"} {
		d := &Dialer{Cluster: "c1", URL: u, Token: StaticToken("t"), Log: quiet}
		if err := d.Run(context.Background()); err == nil {
			t.Errorf("Run(%q) = nil, want an error", u)
		}
	}
}
