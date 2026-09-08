package pgxadapter

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/casbin/casbin/v3/persist"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	notifyChannelPrefix   = "casbin_policy_changed_"
	debounceWindow        = 50 * time.Millisecond
	reconnectBaseDelay    = 100 * time.Millisecond
	reconnectMaxDelay     = 5 * time.Second
	reconnectBackoffScale = 2
)

var _ persist.Watcher = (*PgxWatcher)(nil)

// PgxWatcher implements persist.Watcher using Postgres LISTEN/NOTIFY.
// It acquires one dedicated connection from the pool for LISTEN and
// uses the pool for NOTIFY (via pg_notify). A background goroutine
// blocks on WaitForNotification and fires the registered callback
// when another instance (or this one) changes policy.
type PgxWatcher struct {
	pool     *pgxpool.Pool
	channel  string
	debounce time.Duration
	mu       sync.Mutex
	callback func(string)
	cancel   context.CancelFunc
	done     chan struct{}
}

func notifyChannel(tableName string) string {
	return notifyChannelPrefix + tableName
}

// NewWatcher creates a PgxWatcher that listens for policy-change
// notifications on the Postgres channel derived from the table name
// (default "casbin_rule"). The watcher acquires one dedicated
// connection from pool for the LISTEN session; call Close to release
// it before closing the pool.
func NewWatcher(pool *pgxpool.Pool, opts ...WatcherOption) (*PgxWatcher, error) {
	w := &PgxWatcher{
		pool:     pool,
		channel:  notifyChannel(defaultTableName),
		debounce: debounceWindow,
		done:     make(chan struct{}),
	}

	for _, opt := range opts {
		opt(w)
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel

	conn, err := pool.Acquire(ctx)
	if err != nil {
		cancel()

		return nil, err
	}

	_, err = conn.Exec(ctx, "LISTEN "+w.channel)
	if err != nil {
		conn.Release()
		cancel()

		return nil, err
	}

	go w.listen(ctx, conn)

	return w, nil
}

// SetUpdateCallback registers the function that will be called when a
// NOTIFY arrives on this watcher's channel. Casbin calls this
// automatically when you call enforcer.SetWatcher.
func (w *PgxWatcher) SetUpdateCallback(fn func(string)) error {
	w.mu.Lock()
	w.callback = fn
	w.mu.Unlock()

	return nil
}

// Update sends a NOTIFY on this watcher's channel, signaling all
// replicas (including this one) to reload policy. Called automatically
// by the Casbin enforcer after every mutating operation.
func (w *PgxWatcher) Update() error {
	_, err := w.pool.Exec(context.Background(), "SELECT pg_notify($1, '')", w.channel)

	return err
}

// Close stops the listener goroutine and releases the dedicated
// LISTEN connection back to the pool.
func (w *PgxWatcher) Close() {
	w.cancel()
	<-w.done
}

func (w *PgxWatcher) listen(ctx context.Context, conn *pgxpool.Conn) {
	defer close(w.done)
	defer conn.Release()

	for {
		_, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			conn.Release()
			conn = w.reconnect(ctx)

			if conn == nil {
				return
			}

			w.fireCallback()

			continue
		}

		w.debounceAndFire(ctx, conn)
	}
}

// debounceAndFire drains any further notifications that arrive within
// the debounce window, then fires the callback exactly once. When
// debouncing is disabled (debounce == 0), fires immediately.
func (w *PgxWatcher) debounceAndFire(ctx context.Context, conn *pgxpool.Conn) {
	if w.debounce > 0 {
		drainCtx, drainCancel := context.WithTimeout(ctx, w.debounce)
		defer drainCancel()

		for {
			_, err := conn.Conn().WaitForNotification(drainCtx)
			if err != nil {
				break
			}
		}
	}

	w.fireCallback()
}

func (w *PgxWatcher) fireCallback() {
	w.mu.Lock()
	fn := w.callback
	w.mu.Unlock()

	if fn != nil {
		fn("")
	}
}

func (w *PgxWatcher) reconnect(ctx context.Context) *pgxpool.Conn {
	delay := reconnectBaseDelay

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}

		conn, err := w.pool.Acquire(ctx)
		if err != nil {
			slog.Warn("casbin watcher: reconnect acquire failed", "err", err)

			delay = min(delay*reconnectBackoffScale, reconnectMaxDelay)

			continue
		}

		_, err = conn.Exec(ctx, "LISTEN "+w.channel)
		if err != nil {
			slog.Warn("casbin watcher: reconnect LISTEN failed", "err", err)

			conn.Release()

			delay = min(delay*reconnectBackoffScale, reconnectMaxDelay)

			continue
		}

		return conn
	}
}
