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
	conns map[*irodsfs.FileSystem]usage
}

func (f *fakeOpen) count(fsys *irodsfs.FileSystem) usage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns[fsys]
}

func (f *fakeOpen) set(fsys *irodsfs.FileSystem, u usage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns[fsys] = u
}

// capacityPool returns a pool capped at limit whose connection counts the test controls.
func capacityPool(t *testing.T, limit int) (*Pool, *fakeOpen) {
	t.Helper()
	pool := testPool(t, func(c *Config) { c.MaxTotalConnections = limit })
	open := &fakeOpen{conns: map[*irodsfs.FileSystem]usage{}}
	pool.countOpen = open.count
	return pool, open
}

// trackedSession checks out a session for user whose FileSystem reports open connections.
// The entry's own fs stays nil so that eviction never calls Release on a fake.
func trackedSession(pool *Pool, open *fakeOpen, user string, conns usage) *Session {
	s := checkout(pool, user)
	fsys := &irodsfs.FileSystem{}
	open.set(fsys, conns)

	pool.mu.Lock()
	pool.tracked[s.entry] = fsys
	pool.mu.Unlock()
	return s
}

// leaseAsync starts a lease and reports its outcome on the returned channel.
func leaseAsync(ctx context.Context, pool *Pool, s *Session, kind connKind) <-chan error {
	done := make(chan error, 1)
	go func() {
		release, err := pool.lease(ctx, s.entry, kind)
		if err == nil {
			defer release()
		}
		done <- err
	}()
	return done
}

func meta(n int) usage     { return usage{metadata: n} }
func transfer(n int) usage { return usage{transfer: n} }

func TestLeaseAdmission(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		// open connections held by other users' sessions, all in use
		others []usage
		// connections already open on the caller's own session
		own   usage
		kind  connKind
		admit bool
	}{
		{name: "room under the limit", limit: 4, others: []usage{meta(2)}, kind: metadataConn, admit: true},
		{name: "exactly at the limit after admission", limit: 4, others: []usage{meta(3)}, kind: metadataConn, admit: true},
		{name: "would cross the limit", limit: 4, others: []usage{meta(4)}, kind: metadataConn, admit: false},
		{name: "an idle connection of its own needs no new room", limit: 4, others: []usage{meta(3)}, own: meta(1), kind: metadataConn, admit: true},
		{name: "an idle metadata connection cannot carry a transfer", limit: 4, others: []usage{meta(2)}, own: meta(1), kind: transferConn, admit: false},
		{name: "an idle transfer connection can", limit: 4, others: []usage{meta(3)}, own: transfer(1), kind: transferConn, admit: true},
		{name: "a transfer leaves the reserve alone", limit: 4, others: []usage{transfer(3)}, kind: transferConn, admit: false},
		{name: "a metadata call may use the reserve", limit: 4, others: []usage{transfer(3)}, kind: metadataConn, admit: true},
		{name: "anything runs on an empty replica", limit: 1, kind: transferConn, admit: true},
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
			err := <-leaseAsync(ctx, pool, caller, tc.kind)

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
	s := trackedSession(pool, open, "user", usage{})
	defer s.Close()

	var releases []func()
	for range 2 {
		release, err := pool.lease(context.Background(), s.entry, metadataConn)
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		releases = append(releases, release)
	}

	waiting := leaseAsync(context.Background(), pool, s, metadataConn)
	select {
	case err := <-waiting:
		t.Fatalf("a third lease returned %v while two held every connection", err)
	case <-time.After(100 * time.Millisecond):
	}

	releases[0]()
	select {
	case err := <-waiting:
		if err != nil {
			t.Errorf("third lease after a release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("the third lease was not woken by the release")
	}
	releases[1]()
}

// TestLeaseEvictsIdleSessionsToMakeRoom covers the common way room appears: another user's
// session that has gone quiet still holds connections, and evicting it frees them.
func TestLeaseEvictsIdleSessionsToMakeRoom(t *testing.T) {
	pool, open := capacityPool(t, 2)

	idle := trackedSession(pool, open, "idle", meta(2))
	idleEntry := idle.entry
	idle.Close()

	caller := trackedSession(pool, open, "caller", usage{})
	defer caller.Close()

	waiting := leaseAsync(context.Background(), pool, caller, metadataConn)

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
	busy := trackedSession(pool, open, "busy", meta(2))
	defer busy.Close()
	caller := trackedSession(pool, open, "caller", usage{})
	defer caller.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := <-leaseAsync(ctx, pool, caller, metadataConn); !errors.Is(err, context.DeadlineExceeded) {
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
	busy := trackedSession(pool, open, "busy", meta(2))
	defer busy.Close()
	caller := trackedSession(pool, open, "caller", usage{})
	defer caller.Close()

	waiting := leaseAsync(context.Background(), pool, caller, metadataConn)
	time.Sleep(50 * time.Millisecond)

	pool.mu.Lock()
	fsys := pool.tracked[busy.entry]
	pool.mu.Unlock()
	open.set(fsys, meta(1))

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
	busy := trackedSession(pool, open, "busy", meta(1))
	caller := trackedSession(pool, open, "caller", usage{})

	waiting := leaseAsync(context.Background(), pool, caller, metadataConn)
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

// TestWaitReport keeps a burst of queued calls to one warning a minute, counts the calls
// that gave up, and reports the tail of a burst once the interval passes even if nothing
// waits afterwards.
func TestWaitReport(t *testing.T) {
	start := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

	type step struct {
		at     time.Duration
		flush  bool
		waited time.Duration
		gaveUp bool
		due    bool
		want   waitTally
	}
	steps := []step{
		{at: 0, waited: time.Second, due: true, want: waitTally{waits: 1, longest: time.Second}},
		{at: 10 * time.Second, waited: 3 * time.Second},
		{at: 20 * time.Second, waited: 30 * time.Second, gaveUp: true},
		{at: 30 * time.Second, flush: true},
		{at: waitReportInterval, flush: true, due: true, want: waitTally{waits: 2, gaveUp: 1, longest: 30 * time.Second}},
		{at: waitReportInterval + time.Second, flush: true},
		{at: 3 * waitReportInterval, waited: 5 * time.Second, due: true, want: waitTally{waits: 1, longest: 5 * time.Second}},
	}

	var r waitReport
	for i, s := range steps {
		var (
			got waitTally
			due bool
		)
		if s.flush {
			got, due = r.flush(start.Add(s.at))
		} else {
			got, due = r.record(start.Add(s.at), s.waited, s.gaveUp)
		}
		if due != s.due || got != s.want {
			t.Errorf("step %d: (%+v, %v), want (%+v, %v)", i, got, due, s.want, s.due)
		}
	}
}
