package irodsclient

import (
	"container/list"
	"time"
)

// evictLocked unlinks least-recently-used sessions that are over the limit or have gone
// idle.
//
// A session in use is never evicted, even if that leaves the pool over MaxSessions. An
// evicted session's next caller opens a new one alongside it, and for the admin session
// that is a second service-account connection, which QA refuses. A long download or job
// would trigger it just by outlasting IdleTimeout.
func (p *Pool) evictLocked() {
	cutoff := time.Now().Add(-p.cfg.IdleTimeout)

	for el := p.lru.Back(); el != nil; {
		prev := el.Prev()
		e := el.Value.(*entry)

		if e.inUse == 0 && (p.lru.Len() > p.cfg.MaxSessions || e.lastUsed.Before(cutoff)) {
			p.unlinkLocked(el, e)
		}
		el = prev
	}
}

// unlinkLocked removes an entry from the pool and releases its FileSystem if nobody is
// holding it. Callers must hold p.mu.
func (p *Pool) unlinkLocked(el *list.Element, e *entry) {
	p.lru.Remove(el)
	delete(p.entries, e.user)
	e.dead = true

	if e.inUse == 0 {
		p.releaseFSLocked(e)
	}
}

// releaseFSLocked tears an entry's FileSystem down off the caller's goroutine, since
// Release closes connections and can block. The FileSystem stays tracked until Release
// returns, so its connections count against MaxTotalConnections until they are gone.
// Callers must hold p.mu.
func (p *Pool) releaseFSLocked(e *entry) {
	if e.fs == nil {
		return
	}
	fsys := e.fs
	e.fs = nil
	go func() {
		fsys.Release()

		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.tracked, e)
		p.signalLocked()
	}()
}

// release returns a session to the pool.
//
// A poisoned session's FileSystem is discarded rather than reused. That covers two cases:
// a call abandoned on context cancellation, which leaves a connection mid-protocol, and a
// connection or authentication failure, which go-irodsclient records on the session so
// that every later acquisition on it fails -- and, in v0.20.1 and v0.21.0, deadlocks. See
// Session.
func (p *Pool) release(e *entry, poisoned bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	e.inUse--
	e.lastUsed = time.Now()

	if poisoned && !e.dead {
		if el, ok := p.entries[e.user]; ok && el.Value.(*entry) == e {
			p.unlinkLocked(el, e)
			return
		}
		// Already gone from the pool; fall through to the last-holder check.
		e.dead = true
	}

	// Whoever drops the count to zero on an unlinked entry owns releasing it. Without
	// this the entry would be unreachable from the pool and never released by anyone.
	if e.dead {
		if e.inUse <= 0 {
			p.releaseFSLocked(e)
		}
		return
	}

	if p.closed {
		if e.inUse <= 0 {
			e.dead = true
			p.releaseFSLocked(e)
		}
		return
	}

	p.evictLocked()
}

// startSweeper runs idle eviction in the background. Without it a session is only ever
// evicted by another checkout or release, so a service that goes quiet holds every
// FileSystem it ever opened -- and the iRODS agents behind them -- indefinitely.
func (p *Pool) startSweeper() {
	interval := p.cfg.IdleTimeout / 2
	if interval < time.Second {
		interval = time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(p.sweeperDone)

	for {
		select {
		case <-p.sweeperStop:
			return
		case <-ticker.C:
			p.mu.Lock()
			if !p.closed {
				p.evictLocked()
			}
			p.mu.Unlock()

			p.flushWaits()
		}
	}
}

// Close tears down the pool. Sessions still in use are unlinked and released by their last
// holder rather than pulled out from under an in-flight call.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.signalLocked()

	for el := p.lru.Front(); el != nil; {
		next := el.Next()
		p.unlinkLocked(el, el.Value.(*entry))
		el = next
	}
	p.mu.Unlock()

	close(p.sweeperStop)
	<-p.sweeperDone
}
