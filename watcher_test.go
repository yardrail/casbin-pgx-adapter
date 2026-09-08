package pgxadapter_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	pgxadapter "github.com/yardrail/casbin-pgx-adapter"
)

func mustPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := getTestDBURL()

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Skipf("Could not create pool: %v", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("Could not ping test database: %v", err)
	}

	t.Cleanup(pool.Close)

	return pool
}

func TestWatcher_NotifyTriggersCallback(t *testing.T) {
	t.Parallel()

	pool := mustPool(t)

	watcherA, err := pgxadapter.NewWatcher(pool)
	if err != nil {
		t.Fatalf("NewWatcher A: %v", err)
	}

	defer watcherA.Close()

	watcherB, err := pgxadapter.NewWatcher(pool)
	if err != nil {
		t.Fatalf("NewWatcher B: %v", err)
	}

	defer watcherB.Close()

	called := make(chan string, 1)

	err = watcherB.SetUpdateCallback(func(msg string) {
		called <- msg
	})
	if err != nil {
		t.Fatalf("SetUpdateCallback: %v", err)
	}

	err = watcherA.Update()
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	select {
	case <-called:
		// success
	case <-time.After(time.Second):
		t.Fatal("callback not fired within 1s after Update()")
	}
}

func TestWatcher_Close(t *testing.T) {
	t.Parallel()

	pool := mustPool(t)

	watcher, err := pgxadapter.NewWatcher(pool)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	var count atomic.Int32

	_ = watcher.SetUpdateCallback(func(string) {
		count.Add(1)
	})

	watcher.Close()

	sender, err := pgxadapter.NewWatcher(pool)
	if err != nil {
		t.Fatalf("NewWatcher sender: %v", err)
	}

	defer sender.Close()

	_ = sender.Update()

	time.Sleep(200 * time.Millisecond)

	if got := count.Load(); got != 0 {
		t.Fatalf("callback fired %d times after Close(), want 0", got)
	}
}

func TestWatcher_DifferentChannels(t *testing.T) {
	t.Parallel()

	pool := mustPool(t)

	watcherAlpha, err := pgxadapter.NewWatcher(pool,
		pgxadapter.WithWatcherTableName("alpha_rules"))
	if err != nil {
		t.Fatalf("NewWatcher alpha: %v", err)
	}

	defer watcherAlpha.Close()

	watcherBeta, err := pgxadapter.NewWatcher(pool,
		pgxadapter.WithWatcherTableName("beta_rules"))
	if err != nil {
		t.Fatalf("NewWatcher beta: %v", err)
	}

	defer watcherBeta.Close()

	betaCalled := make(chan struct{}, 1)

	_ = watcherBeta.SetUpdateCallback(func(string) {
		betaCalled <- struct{}{}
	})

	err = watcherAlpha.Update()
	if err != nil {
		t.Fatalf("Update alpha: %v", err)
	}

	select {
	case <-betaCalled:
		t.Fatal("beta callback fired on alpha notification — channels not isolated")
	case <-time.After(300 * time.Millisecond):
		// success: beta did not fire
	}
}

func TestWatcher_Debounce(t *testing.T) {
	t.Parallel()

	pool := mustPool(t)

	sender, err := pgxadapter.NewWatcher(pool)
	if err != nil {
		t.Fatalf("NewWatcher sender: %v", err)
	}

	defer sender.Close()

	receiver, err := pgxadapter.NewWatcher(pool)
	if err != nil {
		t.Fatalf("NewWatcher receiver: %v", err)
	}

	defer receiver.Close()

	var count atomic.Int32

	_ = receiver.SetUpdateCallback(func(string) {
		count.Add(1)
	})

	for i := range 10 {
		if err := sender.Update(); err != nil {
			t.Fatalf("Update %d: %v", i, err)
		}
	}

	time.Sleep(500 * time.Millisecond)

	got := count.Load()
	if got >= 10 {
		t.Fatalf("debounce ineffective: callback fired %d times for 10 rapid notifications", got)
	}

	if got == 0 {
		t.Fatal("callback never fired")
	}

	t.Logf("10 rapid notifications collapsed into %d callback(s)", got)
}
