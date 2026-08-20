package irodsclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testPool(t *testing.T, mutate func(*Config)) *Pool {
	t.Helper()

	// A reserved name that cannot resolve anywhere. A bare "irods" resolves on some
	// networks through a search domain, which quietly changes what these tests exercise
	// -- it masked a real bug locally that CI caught.
	cfg := Config{Host: "irods.invalid", Port: 1247, Zone: "iplant", ProxyUser: "rods"}
	if mutate != nil {
		mutate(&cfg)
	}

	pool, err := NewPool(cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// checkout fakes a session against a pooled entry without touching the network, so the
// bookkeeping can be tested on its own.
func checkout(p *Pool, key string) *Session {
	p.mu.Lock()
	defer p.mu.Unlock()

	if el, ok := p.entries[key]; ok {
		e := el.Value.(*entry)
		e.inUse++
		p.lru.MoveToFront(el)
		return &Session{pool: p, entry: e, clientUser: key}
	}

	e := &entry{user: key, lastUsed: time.Now(), inUse: 1}
	p.entries[key] = p.lru.PushFront(e)
	return &Session{pool: p, entry: e, clientUser: key}
}

func (p *Pool) entryCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lru.Len()
}

// TestPoisonedEntryWithOtherHoldersIsStillUnlinked covers a leak that is easy to
// reintroduce. When one holder poisons an entry another holder is still using, the entry
// has to be unlinked immediately and released by whoever lets go last. Releasing only when
// the poisoning holder happens to be the last one leaves the entry unreachable from the
// pool and released by nobody.
func TestPoisonedEntryWithOtherHoldersIsStillUnlinked(t *testing.T) {
	pool := testPool(t, nil)

	first := checkout(pool, adminKey)
	second := checkout(pool, adminKey)

	if got := first.entry.inUse; got != 2 {
		t.Fatalf("inUse = %d, want 2", got)
	}

	// The first holder's call was cancelled.
	first.poisoned.Store(true)
	first.Close()

	if pool.entryCount() != 0 {
		t.Error("the poisoned entry is still in the pool; another caller could pick it up")
	}
	if !second.entry.dead {
		t.Error("the entry was not marked dead, so the last holder will not release it")
	}
	if second.entry.inUse != 1 {
		t.Errorf("inUse = %d, want 1", second.entry.inUse)
	}

	second.Close()
	if second.entry.inUse != 0 {
		t.Errorf("inUse after the last release = %d, want 0", second.entry.inUse)
	}
}

// TestPoisonedSessionIsNotReused is the point of poisoning: the next caller must get a
// fresh entry rather than the failed one.
func TestPoisonedSessionIsNotReused(t *testing.T) {
	pool := testPool(t, nil)

	first := checkout(pool, adminKey)
	firstEntry := first.entry
	first.poisoned.Store(true)
	first.Close()

	second := checkout(pool, adminKey)
	defer second.Close()

	if second.entry == firstEntry {
		t.Error("a poisoned entry was handed back out")
	}
}

// TestCloseIsIdempotentAndStopsTheSweeper guards against a double close panicking on the
// sweeper channel, which a deferred Close plus an explicit one would hit.
func TestCloseIsIdempotentAndStopsTheSweeper(t *testing.T) {
	pool, err := NewPool(Config{Host: "irods.invalid", Port: 1247, Zone: "iplant", ProxyUser: "rods"})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	pool.Close()
	pool.Close()

	select {
	case <-pool.sweeperDone:
	case <-time.After(2 * time.Second):
		t.Error("the sweeper did not stop")
	}
}

// TestSessionsAfterCloseAreRefused stops work being started against a pool that is
// shutting down.
func TestSessionsAfterCloseAreRefused(t *testing.T) {
	pool, err := NewPool(Config{Host: "irods.invalid", Port: 1247, Zone: "iplant", ProxyUser: "rods"})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	pool.Close()

	if _, err := pool.Admin(context.Background()); err == nil {
		t.Error("Admin succeeded on a closed pool")
	}
}

// TestEvictionRespectsInUse makes sure a session being used is never pulled out from under
// its caller, however far over the limit the pool is.
func TestEvictionRespectsInUse(t *testing.T) {
	pool := testPool(t, func(c *Config) { c.MaxSessions = 1 })

	held := checkout(pool, "alice")
	defer held.Close()

	other := checkout(pool, "bob")
	defer other.Close()

	if held.entry.dead {
		t.Error("an in-use entry was unlinked while over the session limit")
	}
}

// TestProbeSharesTheAdminSession pins a deliberate choice. An earlier version gave the
// probe its own session so a short probe deadline could not poison the session admin work
// uses. That costs a second concurrent connection, which a server may refuse outright --
// QA does, for the service account -- so the probe shares the admin session and gets a
// budget long enough that a timeout is real.
func TestProbeSharesTheAdminSession(t *testing.T) {
	pool := testPool(t, nil)

	admin := checkout(pool, adminKey)
	defer admin.Close()

	probe := checkout(pool, probeKey)
	defer probe.Close()

	if admin.entry != probe.entry {
		t.Error("the probe opened a second session; that is a connection the server may refuse")
	}
}

// TestProbeIgnoresTheCallersDeadline covers the other half of that choice. GET / gives its
// probe a couple of seconds; if the probe inherited that, ordinary latency would look like
// a failure and discard the session every admin request uses.
func TestProbeIgnoresTheCallersDeadline(t *testing.T) {
	pool := testPool(t, func(c *Config) {
		c.ProbeTimeout = 200 * time.Millisecond
		c.MaxRetries = 1
		c.RetrySleep = time.Millisecond
	})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	err := pool.Probe(cancelled)
	if err == nil {
		t.Fatal("the probe against an unresolvable host succeeded")
	}
	// It should have tried and failed to reach iRODS, not simply reported the caller's
	// cancellation back.
	if errors.Is(err, context.Canceled) {
		t.Error("the probe adopted the caller's cancellation instead of its own budget")
	}
}

// TestProbeResultIsCached matters because GET / reports iRODS health on every call and
// takes no authentication, so an uncached probe lets anyone drive iRODS load.
func TestProbeResultIsCached(t *testing.T) {
	pool := testPool(t, func(c *Config) { c.ProbeCacheTTL = time.Hour })

	// Seed a fresh successful result, then check that repeated probes reuse it. If the
	// cache were bypassed each call would try to open a session against a host that does
	// not resolve, which would both fail and leave an entry behind.
	pool.probeMu.Lock()
	pool.probeAt = time.Now()
	pool.probeResult = nil
	pool.probeMu.Unlock()

	for i := 0; i < 5; i++ {
		if err := pool.Probe(context.Background()); err != nil {
			t.Fatalf("probe %d returned %v; the cached result should have been reused", i, err)
		}
	}

	if pool.entryCount() != 0 {
		t.Error("a cached probe opened a session, so it reached iRODS")
	}
}

// TestProbeCacheExpires is the other half: a stale result must not be served forever, or a
// recovered backend would keep reporting as down.
func TestProbeCacheExpires(t *testing.T) {
	pool := testPool(t, func(c *Config) {
		c.ProbeCacheTTL = time.Millisecond
		c.ProbeTimeout = 200 * time.Millisecond
		c.MaxRetries = 1
		c.RetrySleep = time.Millisecond
	})

	pool.probeMu.Lock()
	pool.probeAt = time.Now().Add(-time.Hour)
	pool.probeResult = nil
	pool.probeMu.Unlock()

	// The cached success is stale, so this re-probes and fails against a host that does
	// not resolve. The error is the evidence that the cache expired.
	if err := pool.Probe(context.Background()); err == nil {
		t.Error("a stale cached result was served instead of re-probing")
	}
}

func TestConfigDefaults(t *testing.T) {
	pool := testPool(t, nil)

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"max sessions", pool.cfg.MaxSessions, DefaultMaxSessions},
		{"max connections", pool.cfg.MaxConnections, DefaultMaxConnections},
		{"max io connections", pool.cfg.MaxIOConnections, DefaultMaxIOConnections},
		{"idle timeout", pool.cfg.IdleTimeout, DefaultIdleTimeout},
		{"probe cache ttl", pool.cfg.ProbeCacheTTL, DefaultProbeCacheTTL},
		{"operation timeout", pool.cfg.OperationTimeout, DefaultOperationTimeout},
		{"long operation timeout", pool.cfg.LongOperationTimeout, DefaultLongOperationTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}
