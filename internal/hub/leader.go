package hub

import (
	"cmp"
	"context"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kerf-dev/kerf/internal/hub/store"
)

// Elector elects the hub leader on Postgres and delivers the replicas'
// notifications. Every replica runs one on a dedicated connection that
//   - LISTENs on store.NotifyChannel (token revocations, recompute and readiness triggers, settings);
//   - tries pg_try_advisory_lock(Key) every Retry while following. The replica holding the lock is
//     the leader and runs Lead until the connection is lost or the process stops.
//
// Postgres releases the lock when the connection ends: at once when the process dies (the kernel
// closes the socket), within ~8 s when the node or network dies (server-side TCP keepalives and
// tcp_user_timeout, set on this connection). The leader pings the connection every Check with a
// timeout of Check and stops its work as soon as a ping fails, which is before Postgres can give
// the lock to another replica. Followers retry every Retry (5 s): failover takes at most ~13 s.
type Elector struct {
	// ConnConfig is the dedicated connection's configuration (ElectorConnConfig).
	ConnConfig *pgx.ConnConfig
	Key        int64
	Retry      time.Duration
	Check      time.Duration
	// Identity names this replica in logs (POD_NAME, else the hostname).
	Identity string
	Log      *slog.Logger
	Metrics  *Metrics
	// Lead runs the leader's work; ctx ends when leadership is lost. It must return soon after.
	Lead func(ctx context.Context)
	// OnNotify handles a notification payload. It must not block (start a goroutine for slow work).
	OnNotify func(payload string)
	// OnListen is called with true once notifications are delivered and false when they may be
	// missed (connection lost).
	OnListen func(listening bool)

	// reconnect is the pause before reconnecting after a lost connection (default 1s; tests).
	reconnect time.Duration

	leader    atomic.Bool
	listening atomic.Bool
}

// IsLeader reports whether this replica leads now.
func (e *Elector) IsLeader() bool { return e.leader.Load() }

// Listening reports whether notifications reach this replica now.
func (e *Elector) Listening() bool { return e.listening.Load() }

// Identity returns the replica's name for logs: env POD_NAME, else the hostname.
func Identity() string {
	if v := os.Getenv("POD_NAME"); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "hub-" + strconv.Itoa(os.Getpid())
}

// ElectorApplicationName is the application_name of the elector connection of a replica (find
// the leader with pg_locks/pg_stat_activity).
func ElectorApplicationName(identity string) string {
	n := "ro-hub-elector:" + identity
	if len(n) > 63 {
		n = n[:63]
	}
	return n
}

// ElectorConnConfig is the configuration of the elector's dedicated connection: the hub's
// database URL with server-side keepalives and tcp_user_timeout (the server notices a dead
// leader within ~8 s and releases its lock) and client-side keepalives.
func ElectorConnConfig(databaseURL, identity string) (*pgx.ConnConfig, error) {
	cc, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	rp := cc.RuntimeParams
	rp["application_name"] = ElectorApplicationName(identity)
	rp["tcp_keepalives_idle"] = "4"
	rp["tcp_keepalives_interval"] = "2"
	rp["tcp_keepalives_count"] = "2"
	rp["tcp_user_timeout"] = "8000" // ms, PostgreSQL 12+
	if cc.ConnectTimeout == 0 || cc.ConnectTimeout > 5*time.Second {
		cc.ConnectTimeout = 5 * time.Second
	}
	d := &net.Dialer{KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: 4 * time.Second, Interval: 2 * time.Second, Count: 2}}
	cc.DialFunc = d.DialContext
	return cc, nil
}

func (e *Elector) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func (e *Elector) retry() time.Duration {
	if e.Retry > 0 {
		return e.Retry
	}
	return 5 * time.Second
}

func (e *Elector) check() time.Duration {
	if e.Check > 0 {
		return e.Check
	}
	return 2 * time.Second
}

func (e *Elector) setListening(v bool) {
	if e.listening.Swap(v) != v && e.OnListen != nil {
		e.OnListen(v)
	}
}

func (e *Elector) setLeader(v bool, reason string) {
	if e.leader.Swap(v) == v {
		return
	}
	if e.Metrics != nil {
		e.Metrics.setLeader(v)
	}
	if v {
		e.log().Info("hub leadership acquired", "pod", e.Identity)
	} else {
		e.log().Info("hub leadership lost", "pod", e.Identity, "reason", reason)
	}
}

// Run elects until ctx is done. On return this replica no longer leads and its lock is released.
func (e *Elector) Run(ctx context.Context) {
	for ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(ctx, e.ConnConfig.ConnectTimeout+time.Second)
		conn, err := pgx.ConnectConfig(cctx, e.ConnConfig)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				e.log().WarnContext(ctx, "hub elector: connect", "pod", e.Identity, "err", err)
			}
			sleepCtx(ctx, e.retry())
			continue
		}
		reason, led := e.session(ctx, conn)
		e.close(conn, reason == "shutdown" && led)
		if ctx.Err() == nil {
			sleepCtx(ctx, cmp.Or(e.reconnect, time.Second))
		}
	}
}

// close ends a session's connection. A leader that is shutting down unlocks first and tells the
// followers, so one takes over at once.
func (e *Elector) close(conn *pgx.Conn, stepDown bool) {
	bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if stepDown && !conn.IsClosed() {
		var unlocked bool
		if err := conn.QueryRow(bg, `SELECT pg_advisory_unlock($1)`, e.Key).Scan(&unlocked); err == nil && unlocked {
			_, _ = conn.Exec(bg, `SELECT pg_notify($1, $2)`, store.NotifyChannel, store.NotifyReleased)
		}
	}
	_ = conn.Close(bg)
}

// session runs on one connection until it breaks or ctx is done; it stops leading before it
// returns. The result says why it ended ("shutdown" or the error) and whether it led.
func (e *Elector) session(ctx context.Context, conn *pgx.Conn) (reason string, led bool) {
	lctx, lcancel := context.WithTimeout(ctx, e.check()*2)
	_, err := conn.Exec(lctx, "LISTEN "+store.NotifyChannel)
	lcancel()
	if err != nil {
		if ctx.Err() != nil {
			return "shutdown", false
		}
		e.log().WarnContext(ctx, "hub elector: listen", "pod", e.Identity, "err", err)
		return err.Error(), false
	}
	e.setListening(true)
	defer e.setListening(false)

	var stop func(reason string)
	defer func() {
		if stop != nil {
			led = true
			stop(reason)
		}
	}()
	var nextTry time.Time
	lastAlive := time.Now()
	for {
		if ctx.Err() != nil {
			return "shutdown", false
		}
		if stop == nil && !time.Now().Before(nextTry) {
			tctx, cancel := context.WithTimeout(ctx, e.check())
			var ok bool
			err := conn.QueryRow(tctx, `SELECT pg_try_advisory_lock($1)`, e.Key).Scan(&ok)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return "shutdown", false
				}
				e.log().WarnContext(ctx, "hub elector: lock attempt failed", "pod", e.Identity, "err", err)
				return err.Error(), false
			}
			lastAlive = time.Now()
			nextTry = lastAlive.Add(e.retry())
			if ok {
				stop = e.startLeading(ctx)
			}
		}
		wait := e.check()
		if stop == nil {
			wait = min(wait, max(time.Until(nextTry), 10*time.Millisecond))
		}
		wctx, cancel := context.WithTimeout(ctx, wait)
		n, err := conn.WaitForNotification(wctx)
		timedOut := wctx.Err() != nil
		cancel()
		switch {
		case err == nil:
			lastAlive = time.Now()
			if n.Payload == store.NotifyReleased && stop == nil {
				nextTry = time.Time{}
			}
			if e.OnNotify != nil {
				e.OnNotify(n.Payload)
			}
			continue
		case ctx.Err() != nil:
			return "shutdown", false
		case !timedOut || conn.IsClosed():
			e.log().WarnContext(ctx, "hub elector: connection lost", "pod", e.Identity, "err", err)
			return err.Error(), false
		}
		if time.Since(lastAlive) >= e.check() {
			pctx, cancel := context.WithTimeout(ctx, e.check())
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return "shutdown", false
				}
				e.log().WarnContext(ctx, "hub elector: connection check failed", "pod", e.Identity, "err", err)
				return "connection check failed: " + err.Error(), false
			}
			lastAlive = time.Now()
		}
	}
}

// startLeading starts Lead and returns the function that stops it (and waits for it).
func (e *Elector) startLeading(ctx context.Context) func(reason string) {
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	e.setLeader(true, "")
	go func() {
		defer close(done)
		if e.Lead != nil {
			e.Lead(lctx)
		}
	}()
	return func(reason string) {
		e.setLeader(false, reason)
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			e.log().WarnContext(ctx, "hub elector: leader work still stopping", "pod", e.Identity)
			<-done
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
