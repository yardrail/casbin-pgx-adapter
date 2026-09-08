package pgxadapter

import "time"

// WatcherOption configures a PgxWatcher.
type WatcherOption func(*PgxWatcher)

// WithWatcherTableName sets the Casbin table name used to derive the
// Postgres LISTEN/NOTIFY channel. Defaults to "casbin_rule".
func WithWatcherTableName(name string) WatcherOption {
	return func(w *PgxWatcher) {
		w.channel = notifyChannel(name)
	}
}

// WithDebounce sets the debounce window for coalescing rapid
// notifications into a single callback invocation. Defaults to 50ms.
// Set to 0 to disable debouncing entirely.
func WithDebounce(d time.Duration) WatcherOption {
	return func(w *PgxWatcher) {
		w.debounce = d
	}
}
