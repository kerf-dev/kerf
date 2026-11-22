package tunnel

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultConnections is how many connections the hub keeps open to one agent.
const DefaultConnections = 4

// serveIdle closes a served connection after this long without a request (above the agent's DefaultMaxIdle for
// pooled connections, below common load balancer idle timeouts).
const serveIdle = 50 * time.Second

// Dialer is the hub side for one cluster: it keeps Connections connections open to the agent's tunnel endpoint
// and serves Handler (the hub API) on each of them.
type Dialer struct {
	Cluster string
	// URL is the agent's tunnel endpoint base, e.g. https://kerf-agent.prod.example.internal; Path is appended.
	URL string
	// Token is the tunnel token the agent expects.
	Token TokenSource
	// Connections overrides DefaultConnections.
	Connections int
	Handler     http.Handler
	// TLS overrides the TLS settings for https URLs (default: system roots, honouring SSL_CERT_FILE/SSL_CERT_DIR).
	TLS *tls.Config
	Log *slog.Logger
}

// Run keeps the connections open until ctx ends.
func (d *Dialer) Run(ctx context.Context) error {
	u, err := url.Parse(strings.TrimRight(d.URL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("agent tunnel %s: url %q must be http(s)://host[:port]", d.Cluster, d.URL)
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	log := d.Log.With("cluster", d.Cluster, "url", d.URL)
	n := d.Connections
	if n <= 0 {
		n = DefaultConnections
	}
	ln := newChanListener()
	srv := &http.Server{Handler: d.Handler, IdleTimeout: serveIdle}
	go func() { _ = srv.Serve(ln) }()
	var st connState
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() { defer wg.Done(); d.keep(ctx, u, ln, &st, log) }()
	}
	log.InfoContext(ctx, "connecting to agent", "connections", n)
	<-ctx.Done()
	_ = srv.Close()
	wg.Wait()
	return nil
}

// connState logs only changes between connected and failing, not every reconnect.
type connState struct {
	mu      sync.Mutex
	failing bool
	known   bool
}

func (s *connState) set(failing bool) (changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed = !s.known || s.failing != failing
	s.known, s.failing = true, failing
	return changed
}

// keep holds one connection slot: dial, hand the connection to the server, wait until it is closed, repeat.
func (d *Dialer) keep(ctx context.Context, u *url.URL, ln *chanListener, st *connState, log *slog.Logger) {
	backoff := time.Second
	for ctx.Err() == nil {
		c, err := d.dial(ctx, u)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if st.set(true) {
				log.WarnContext(ctx, "agent tunnel: connect failed, retrying", "err", err)
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		if st.set(false) {
			log.InfoContext(ctx, "agent tunnel connected")
		}
		backoff = time.Second
		nc := &notifyConn{Conn: c, done: make(chan struct{})}
		if !ln.push(ctx, nc) {
			_ = c.Close()
			return
		}
		select {
		case <-nc.done:
		case <-ctx.Done():
			_ = nc.Close()
			return
		}
	}
}

// dial opens a connection to the agent and performs the upgrade.
func (d *Dialer) dial(ctx context.Context, u *url.URL) (net.Conn, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, fmt.Errorf("tunnel token: %w", err)
	}
	if tok == "" {
		return nil, errors.New("tunnel token missing")
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			host = net.JoinHostPort(u.Hostname(), "443")
		} else {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var c net.Conn
	if u.Scheme == "https" {
		cfg := d.TLS.Clone()
		if cfg == nil {
			cfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		if cfg.ServerName == "" {
			cfg.ServerName = u.Hostname()
		}
		cfg.NextProtos = []string{"http/1.1"} // an upgrade needs HTTP/1.1
		c, err = (&tls.Dialer{Config: cfg}).DialContext(dctx, "tcp", host)
	} else {
		c, err = (&net.Dialer{}).DialContext(dctx, "tcp", host)
	}
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	key := base64.StdEncoding.EncodeToString(nonce[:])
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, u.String()+Path, nil)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Authorization", "Bearer "+tok)
	if dl, ok := dctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if err := req.Write(c); err != nil {
		_ = c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = c.Close()
		return nil, fmt.Errorf("agent answered HTTP %d (want 101)", resp.StatusCode)
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		_ = c.Close()
		return nil, errors.New("agent answered with a wrong Sec-WebSocket-Accept")
	}
	_ = c.SetDeadline(time.Time{})
	// The agent may send its first request right after the 101: keep what br already read.
	return &bufConn{Conn: c, r: br}, nil
}

// bufConn reads through the reader that parsed the handshake response.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// notifyConn closes done when the server closes the connection.
type notifyConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func (n *notifyConn) Close() error {
	err := n.Conn.Close()
	n.once.Do(func() { close(n.done) })
	return err
}

// chanListener is a net.Listener whose connections are pushed by the dialers.
type chanListener struct {
	ch     chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newChanListener() *chanListener {
	return &chanListener{ch: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *chanListener) push(ctx context.Context, c net.Conn) bool {
	select {
	case l.ch <- c:
		return true
	case <-l.closed:
		return false
	case <-ctx.Done():
		return false
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return tunnelAddr{} }

type tunnelAddr struct{}

func (tunnelAddr) Network() string { return "tunnel" }
func (tunnelAddr) String() string  { return "agent-tunnel" }
