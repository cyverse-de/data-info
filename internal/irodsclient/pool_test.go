package irodsclient

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func testPool(t *testing.T, mutate func(*Config)) *Pool {
	t.Helper()

	// A reserved name that cannot resolve; a bare "irods" resolves through a search domain
	// on some networks.
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

// TestEviction covers which sessions the sweeper and new checkouts may evict. One in use
// must stay, however long it has been held or however far over the limit the pool is: its
// next caller would otherwise open a second session beside it.
func TestEviction(t *testing.T) {
	tests := []struct {
		name        string
		maxSessions int
		held        bool
		idle        bool
		wantEvicted bool
	}{
		{"held past the idle timeout", 10, true, true, false},
		{"held while over the limit", 1, true, false, false},
		{"released past the idle timeout", 10, false, true, true},
		{"released while over the limit", 1, false, false, true},
		{"released, recent and within the limit", 10, false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := testPool(t, func(c *Config) {
				c.MaxSessions = tt.maxSessions
				c.IdleTimeout = time.Minute
			})

			subject := checkout(pool, "alice")
			if !tt.held {
				subject.Close()
			}
			newest := checkout(pool, "bob")
			defer newest.Close()

			pool.mu.Lock()
			if tt.idle {
				subject.entry.lastUsed = time.Now().Add(-time.Hour)
			}
			pool.evictLocked()
			pool.mu.Unlock()

			if subject.entry.dead != tt.wantEvicted {
				t.Errorf("evicted = %t, want %t", subject.entry.dead, tt.wantEvicted)
			}
			if tt.held {
				subject.Close()
			}
		})
	}
}

// TestProbeSharesTheAdminSession: a separate probe session would be a second concurrent
// service-account connection, which a server may refuse.
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

// waitForProbe waits for the probe in flight, if any, to record its result.
func waitForProbe(t *testing.T, p *Pool) {
	t.Helper()

	p.probeMu.Lock()
	running := p.probeRunning
	p.probeMu.Unlock()
	if running == nil {
		return
	}

	select {
	case <-running:
	case <-time.After(10 * time.Second):
		t.Fatal("the probe never finished")
	}
}

// TestProbeIgnoresTheCallersDeadline: the caller stops waiting, but the probe carries on to
// a result of its own rather than poisoning the admin session or recording the caller's
// cancellation.
func TestProbeIgnoresTheCallersDeadline(t *testing.T) {
	pool := testPool(t, func(c *Config) {
		c.ProbeTimeout = 200 * time.Millisecond
		c.MaxRetries = 1
		c.RetrySleep = time.Millisecond
	})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if err := pool.Probe(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe = %v, want the caller's cancellation", err)
	}
	waitForProbe(t, pool)

	pool.probeMu.Lock()
	result := pool.probeResult
	pool.probeMu.Unlock()

	// It should have tried and failed to reach iRODS, not recorded the caller's
	// cancellation as the health result.
	if result == nil {
		t.Fatal("the probe against an unresolvable host succeeded")
	}
	if errors.Is(result, context.Canceled) {
		t.Error("the probe adopted the caller's cancellation instead of its own budget")
	}
}

// TestProbeDoesNotHoldCallersPastTheirDeadline: against a server that accepts connections
// and never answers, each caller returns at its own deadline, not the probe's.
func TestProbeDoesNotHoldCallersPastTheirDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	var held []net.Conn
	var heldMu sync.Mutex
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			heldMu.Lock()
			held = append(held, conn)
			heldMu.Unlock()
		}
	}()
	t.Cleanup(func() {
		heldMu.Lock()
		defer heldMu.Unlock()
		for _, conn := range held {
			_ = conn.Close()
		}
	})

	port := listener.Addr().(*net.TCPAddr).Port
	pool := testPool(t, func(c *Config) {
		c.Host = "127.0.0.1"
		c.Port = port
		c.ProbeTimeout = 2 * time.Second
		c.MaxRetries = 0
	})

	for caller := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		start := time.Now()
		err := pool.Probe(ctx)
		cancel()

		if err == nil {
			t.Fatalf("caller %d: a server that never answers was reported healthy", caller)
		}
		if waited := time.Since(start); waited > time.Second {
			t.Errorf("caller %d waited %s for a probe it gave 100ms", caller, waited)
		}
	}

	waitForProbe(t, pool)
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

// TestProxyUserReusesTheAdminSession covers a connection the service would otherwise open
// for no reason. Acting as the proxy account through client-user proxying is the same as
// acting as it directly, and on a zone that grants few connections the second one may simply
// be refused.
func TestProxyUserReusesTheAdminSession(t *testing.T) {
	pool := testPool(t, nil)

	// Seed the admin entry so neither call has to reach the network.
	admin := checkout(pool, adminKey)
	defer admin.Close()

	asProxy, err := pool.ForUser(context.Background(), pool.cfg.ProxyUser)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	defer asProxy.Close()

	if asProxy.entry != admin.entry {
		t.Error("acting as the proxy account opened a second session")
	}
}

// TestOtherUsersGetTheirOwnSession is the other half: the client user is fixed for a
// connection's lifetime, so two different users must not share one.
func TestOtherUsersGetTheirOwnSession(t *testing.T) {
	pool := testPool(t, nil)

	admin := checkout(pool, adminKey)
	defer admin.Close()

	other := checkout(pool, "someone-else")
	defer other.Close()

	if other.entry == admin.entry {
		t.Error("a different user shared the admin session")
	}
}
