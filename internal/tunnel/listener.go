package tunnel

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// DefaultMaxIdle is how long the agent keeps a hub connection it has not used. Older ones are closed and the hub
// dials again, which stays under the idle timeouts of common load balancers.
const DefaultMaxIdle = 40 * time.Second

// Listener is the agent side: the HTTP endpoint the hub connects to, and the pool of connections the agent's hub
// client takes its connections from. It is a controller-runtime Runnable that runs on every replica: only the one
// for which Ready reports true accepts connections (the others answer 503, so the hub dials again and its
// connections end up on the replica that calls the hub).
type Listener struct {
	// Addr is the listen address of the endpoint, e.g. ":8443".
	Addr string
	// Token is the tunnel token the hub must present. An empty token refuses every connection.
	Token TokenSource
	// Ready reports whether this replica uses hub connections (the elected leader). Nil means always.
	Ready func() bool
	// MaxIdle overrides DefaultMaxIdle.
	MaxIdle time.Duration
	Log     *slog.Logger

	pool chan pooled
}

type pooled struct {
	c  net.Conn
	at time.Time
}

// NewListener returns a Listener for addr, the token the hub must present and the leader check.
func NewListener(addr string, token TokenSource, ready func() bool, log *slog.Logger) *Listener {
	if log == nil {
		log = slog.Default()
	}
	return &Listener{Addr: addr, Token: token, Ready: ready, Log: log, pool: make(chan pooled, 64)}
}

func (l *Listener) maxIdle() time.Duration {
	if l.MaxIdle > 0 {
		return l.MaxIdle
	}
	return DefaultMaxIdle
}

// Client returns the HTTP client for the hub in listen mode: every connection comes from the pool. Use it with
// BaseURL.
func (l *Listener) Client() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:     l.DialContext,
			IdleConnTimeout: 30 * time.Second,
			// No proxy: the connection already reaches the hub.
		},
	}
}

// DialContext takes a connection the hub opened, waiting until one arrives or ctx ends. Connections older than
// MaxIdle are closed and skipped: the load balancer between hub and agent may have dropped them.
func (l *Listener) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	for {
		select {
		case p := <-l.pool:
			if time.Since(p.at) > l.maxIdle() {
				_ = p.c.Close()
				continue
			}
			return p.c, nil
		case <-ctx.Done():
			return nil, fmt.Errorf("no connection from the hub: %w", ctx.Err())
		}
	}
}

// CheckToken fails while the tunnel token is missing (a readiness check).
func (l *Listener) CheckToken(*http.Request) error {
	t, err := l.Token()
	if err != nil {
		return err
	}
	if t == "" {
		return errors.New("hub tunnel token missing")
	}
	return nil
}

// ServeHTTP accepts the hub's upgrade on Path and puts the connection in the pool.
func (l *Listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != Path {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if !headerHas(r.Header.Values("Connection"), "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || key == "" {
		http.Error(w, "expected a WebSocket upgrade", http.StatusBadRequest)
		return
	}
	want, err := l.Token()
	if err != nil || want == "" {
		l.Log.WarnContext(r.Context(), "hub connection refused: tunnel token missing", "err", err)
		http.Error(w, "tunnel token not configured", http.StatusServiceUnavailable)
		return
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		l.Log.WarnContext(r.Context(), "hub connection refused: wrong tunnel token", "remote", r.RemoteAddr)
		http.Error(w, "invalid tunnel token", http.StatusUnauthorized)
		return
	}
	if l.Ready != nil && !l.Ready() {
		http.Error(w, "not the leader", http.StatusServiceUnavailable)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connection cannot be taken over", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		l.Log.WarnContext(r.Context(), "hub connection: hijack failed", "err", err)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if _, err := fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", acceptKey(key)); err != nil {
		_ = conn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}
	// The hub serves on this connection and sends nothing first, so nothing is buffered in rw.Reader.
	select {
	case l.pool <- pooled{c: conn, at: time.Now()}:
	default:
		_ = conn.Close() // more connections than the agent can use
	}
}

// reap closes pooled connections older than MaxIdle, so the hub replaces them before the agent needs them.
func (l *Listener) reap() {
	for range len(l.pool) {
		select {
		case p := <-l.pool:
			if time.Since(p.at) > l.maxIdle() {
				_ = p.c.Close()
				continue
			}
			select {
			case l.pool <- p:
			default:
				_ = p.c.Close()
			}
		default:
			return
		}
	}
}

// Start serves the endpoint until ctx ends.
func (l *Listener) Start(ctx context.Context) error {
	srv := &http.Server{Addr: l.Addr, Handler: l, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	l.Log.InfoContext(ctx, "listening for hub connections", "address", l.Addr, "path", Path)
	t := time.NewTicker(l.maxIdle() / 4)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			l.reap()
		case err := <-errc:
			return fmt.Errorf("tunnel endpoint: %w", err)
		case <-ctx.Done():
			shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
			for {
				select {
				case p := <-l.pool:
					_ = p.c.Close()
				default:
					return nil
				}
			}
		}
	}
}

// NeedLeaderElection is false: every replica answers, see Listener.
func (*Listener) NeedLeaderElection() bool { return false }
