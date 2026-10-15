package irodsclient

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	irodsfs "github.com/cyverse/go-irodsclient/fs"
)

// fakeOpen stands in for go-irodsclient's own connection counts.
type fakeOpen struct {
	mu    sync.Mutex
	conns map[*irodsfs.FileSystem]int
}

func (f *fakeOpen) count(fsys *irodsfs.FileSystem) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns[fsys]
}

func (f *fakeOpen) set(fsys *irodsfs.FileSystem, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns[fsys] = n
}

// capacityPool returns a pool capped at limit whose connection counts the test controls.
func capacityPool(t *testing.T, limit int) (*Pool, *fakeOpen) {
	t.Helper()
	pool := testPool(t, func(c *Config) { c.MaxTotalConnections = limit })
	open := &fakeOpen{conns: map[*irodsfs.FileSystem]int{}}
	pool.countOpen = open.count
	return pool, open
}

// trackedSession checks out a session for user whose FileSystem reports open connections.
// The entry's own fs stays nil so that eviction never calls Release on a fake.
func trackedSession(pool *Pool, open *fakeOpen, user string, conns int) *Session {
	s := checkout(pool, user)
	fsys := &irodsfs.FileSystem{}
	open.set(fsys, conns)

	pool.mu.Lock()
	pool.tracked[s.entry] = fsys
	pool.mu.Unlock()
	return s
}

// leaseAsync starts a lease and reports its outcome on the returned channel.
func leaseAsync(ctx context.Context, pool *Pool, s *Session, weight int) <-chan error {
	done := make(chan error, 1)
	go func() {
		release, err := pool.lease(ctx, s.entry, weight)
		if err == nil {
			defer release()
		}
		done <- err
	}()
	return done
}

func TestLeaseAdmission(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		// open connections held by other users' sessions, all in use
		others []int
		// connections already open on the caller's own session
		own    int
		weight int
		admit  bool
	}{
		{name: "room under the limit", limit: 4, others: []int{2}, weight: 1, admit: true},
		{name: "exactly at the limit after admission", limit: 4, others: []int{3}, weight: 1, admit: true},
		{name: "would cross the limit", limit: 4, others: []int{4}, weight: 1, admit: false},
		{name: "an idle connection of its own needs no new room", limit: 4, others: []int{3}, own: 1, weight: 1, admit: true},
		{name: "a transfer needs two", limit: 4, others: []int{3}, weight: 2, admit: false},
		{name: "a transfer with one open needs one more", limit: 4, others: []int{2}, own: 1, weight: 2, admit: true},
		{name: "anything runs on an empty replica", limit: 1, weight: 2, admit: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool, open := capacityPool(t, tc.limit)
			for i, n := range tc.others {
				other := trackedSession(pool, open, "other"+string(rune('a'+i)), n)
				t.Cleanup(other.Close)
			}
			caller := trackedSession(pool, open, "caller", tc.own)
			t.Cleanup(caller.Close)

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			err := <-leaseAsync(ctx, pool, caller, tc.weight)

			if tc.admit && err != nil {
				t.Errorf("lease refused: %v", err)
			}
			if !tc.admit && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("lease = %v, want it to wait until the deadline", err)
			}
		})
	}
}

// TestLeaseWaitsForAReleaseOnTheSameSession is the ordinary queueing case: one user's
// calls in flight hold every connection, and the next call runs once one finishes.
func TestLeaseWaitsForAReleaseOnTheSameSession(t *testing.T) {
	pool, open := capacityPool(t, 2)
	s := trackedSession(pool, open, "user", 0)
	defer s.Close()

	first, err := pool.lease(context.Background(), s.entry, 2)
	if err != nil {
		t.Fatalf("first lease: %v", err)
	}

	waiting := leaseAsync(context.Background(), pool, s, 1)
	select {
	case err := <-waiting:
		t.Fatalf("second lease returned %v while the first held every connection", err)
	case <-time.After(100 * time.Millisecond):
	}

	first()
	select {
	case err := <-waiting:
		if err != nil {
			t.Errorf("second lease after the release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("the second lease was not woken by the release")
	}
}

// TestLeaseEvictsIdleSessionsToMakeRoom covers the common way room appears: another user's
// session that has gone quiet still holds connections, and evicting it frees them.
func TestLeaseEvictsIdleSessionsToMakeRoom(t *testing.T) {
	pool, open := capacityPool(t, 2)

	idle := trackedSession(pool, open, "idle", 2)
	idleEntry := idle.entry
	idle.Close()

	caller := trackedSession(pool, open, "caller", 0)
	defer caller.Close()

	waiting := leaseAsync(context.Background(), pool, caller, 1)

	deadline := time.After(2 * time.Second)
	for {
		pool.mu.Lock()
		_, linked := pool.entries["idle"]
		pool.mu.Unlock()
		if !linked {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the idle session was not evicted")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Stand in for Release finishing, which is what stops the connections counting.
	pool.mu.Lock()
	delete(pool.tracked, idleEntry)
	pool.signalLocked()
	pool.mu.Unlock()

	select {
	case err := <-waiting:
		if err != nil {
			t.Errorf("lease after the eviction: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("the lease did not proceed once the idle session's connections were gone")
	}
}

// TestLeaseNeverEvictsSessionsInUse keeps eviction from tearing down a connection another
// request is part way through using.
func TestLeaseNeverEvictsSessionsInUse(t *testing.T) {
	pool, open := capacityPool(t, 2)
	busy := trackedSession(pool, open, "busy", 2)
	defer busy.Close()
	caller := trackedSession(pool, open, "caller", 0)
	defer caller.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := <-leaseAsync(ctx, pool, caller, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lease = %v, want it to wait until the deadline", err)
	}

	pool.mu.Lock()
	_, linked := pool.entries["busy"]
	pool.mu.Unlock()
	if !linked {
		t.Error("a session in use was evicted")
	}
}

// TestLeaseNoticesConnectionsClosedElsewhere covers go-irodsclient closing an idle
// connection on its own schedule, which nothing signals.
func TestLeaseNoticesConnectionsClosedElsewhere(t *testing.T) {
	pool, open := capacityPool(t, 2)
	busy := trackedSession(pool, open, "busy", 2)
	defer busy.Close()
	caller := trackedSession(pool, open, "caller", 0)
	defer caller.Close()

	waiting := leaseAsync(context.Background(), pool, caller, 1)
	time.Sleep(50 * time.Millisecond)

	pool.mu.Lock()
	fsys := pool.tracked[busy.entry]
	pool.mu.Unlock()
	open.set(fsys, 1)

	select {
	case err := <-waiting:
		if err != nil {
			t.Errorf("lease: %v", err)
		}
	case <-time.After(3 * capacityPollInterval):
		t.Error("the lease did not notice the connection closing")
	}
}

// TestLeaseIsRefusedOnceThePoolCloses keeps shutdown from leaving callers waiting.
func TestLeaseIsRefusedOnceThePoolCloses(t *testing.T) {
	pool, open := capacityPool(t, 1)
	busy := trackedSession(pool, open, "busy", 1)
	caller := trackedSession(pool, open, "caller", 0)

	waiting := leaseAsync(context.Background(), pool, caller, 1)
	time.Sleep(50 * time.Millisecond)
	pool.Close()

	select {
	case err := <-waiting:
		if err == nil {
			t.Error("lease succeeded on a closed pool")
		}
	case <-time.After(2 * time.Second):
		t.Error("closing the pool did not wake the waiting lease")
	}
	busy.Close()
	caller.Close()
}

// TestFileReaderReleasesItsLeaseOnce guards the lease a download holds: a reader closed
// twice must not give back room it no longer holds.
func TestFileReaderReleasesItsLeaseOnce(t *testing.T) {
	releases := 0
	r := &fileReader{handle: nopReadCloser{}, release: func() { releases++ }}

	_ = r.Close()
	_ = r.Close()

	if releases != 1 {
		t.Errorf("released %d times, want 1", releases)
	}
}

type nopReadCloser struct{}

func (nopReadCloser) Read([]byte) (int, error) { return 0, nil }
func (nopReadCloser) Close() error             { return nil }
