package irodsclient

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	irodsfs "github.com/cyverse/go-irodsclient/fs"
	"github.com/sirupsen/logrus"
)

// connKind says which of a session's two connection pools a call draws on.
type connKind int

const (
	// metadataConn is what an ordinary call uses.
	metadataConn connKind = iota
	// transferConn is what a file transfer uses: go-irodsclient opens, reads and writes a
	// handle entirely on an I/O connection.
	transferConn
)

const (
	// metadataReserve is how much of MaxTotalConnections transfers may not take, so that a
	// few long downloads cannot leave every other call on the replica waiting behind them.
	metadataReserve = 1

	// MinTotalConnections is the smallest cap that leaves room for a transfer beside the
	// metadata reserve.
	MinTotalConnections = metadataReserve + 1

	// capacityPollInterval is how often a waiting call recounts. go-irodsclient closes idle
	// connections on its own schedule without telling anybody, so waiting only for this
	// pool's own signals could sleep through a slot freed that way.
	capacityPollInterval = time.Second

	// waitReportInterval is the least time between warnings about calls that waited.
	waitReportInterval = time.Minute
)

// errPoolClosed is what a caller gets from a pool that has been closed.
var errPoolClosed = errors.New("irods pool is closed")

// usage counts connections in each of a session's two pools.
type usage struct {
	metadata int
	transfer int
}

func (u usage) total() int { return u.metadata + u.transfer }

// add returns u with one more connection of kind.
func (u usage) add(kind connKind) usage {
	if kind == transferConn {
		u.transfer++
	} else {
		u.metadata++
	}
	return u
}

// charge is what a session costs against the cap: in each pool, the larger of what it has
// open and what its calls in flight may need.
func charge(open, leased usage) int {
	return max(open.metadata, leased.metadata) + max(open.transfer, leased.transfer)
}

// openUsage reports a FileSystem's open connections in each pool.
func openUsage(fsys *irodsfs.FileSystem) usage {
	return usage{
		metadata: fsys.GetMetadataSession().GetOpenConnections(),
		transfer: fsys.GetIOSession().GetOpenConnections(),
	}
}

// lease reserves room for a call on e that may need one more connection of kind, waiting
// until there is room, and returns the function that gives it back.
//
// The Data Store's HAProxy refuses a client address's connections past a small number, and
// every pod on a node shares an address. go-irodsclient has no hook on connection creation,
// so the count is kept here: each session is charged per pool (see charge), and a call is
// admitted while the total fits MaxTotalConnections, less metadataReserve for a transfer,
// evicting idle sessions to make room.
func (p *Pool) lease(ctx context.Context, e *entry, kind connKind) (func(), error) {
	limit := p.cfg.MaxTotalConnections
	if kind == transferConn {
		limit -= metadataReserve
	}

	var waitingSince time.Time

	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errPoolClosed
		}
		tracked := maps.Clone(p.tracked)
		changed := p.changed
		p.mu.Unlock()

		// Counted outside the lock: go-irodsclient holds its own pool lock while it dials,
		// and waiting on that here would stall every other caller behind one slow connect.
		open := make(map[*entry]usage, len(tracked))
		for te, fsys := range tracked {
			open[te] = p.countOpen(fsys)
		}

		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errPoolClosed
		}

		charged := chargedConnections(open, p.tracked, e)
		need := charge(open[e], e.leased.add(kind)) - charge(open[e], e.leased)

		// A call that needs no new connection always runs, and an empty replica admits
		// anything, so a call cannot be shut out by a cap smaller than what it needs.
		if need == 0 || charged+need <= limit || charged == 0 {
			e.leased = e.leased.add(kind)
			p.mu.Unlock()

			if !waitingSince.IsZero() {
				p.reportWait(time.Since(waitingSince), false)
			}

			return func() {
				p.mu.Lock()
				defer p.mu.Unlock()
				if kind == transferConn {
					e.leased.transfer--
				} else {
					e.leased.metadata--
				}
				p.signalLocked()
			}, nil
		}

		p.reclaimLocked(open, charged+need-limit)
		p.mu.Unlock()

		if waitingSince.IsZero() {
			waitingSince = time.Now()
		}

		select {
		case <-ctx.Done():
			waited := time.Since(waitingSince)
			p.reportWait(waited, true)
			return nil, fmt.Errorf("gave up after %s waiting for one of this replica's %d iRODS connections: %w",
				waited.Round(time.Millisecond), p.cfg.MaxTotalConnections, ctx.Err())
		case <-changed:
		case <-time.After(capacityPollInterval):
		}
	}
}

// reportWait records a call that waited for a connection, whether it got one or gave up,
// and warns if a warning is due.
func (p *Pool) reportWait(waited time.Duration, gaveUp bool) {
	if tally, due := p.waits.record(time.Now(), waited, gaveUp); due {
		p.warnWaits(tally)
	}
}

// flushWaits warns about waits held back since the last warning once the interval has
// passed, so the tail of a burst is reported even if no call waits afterwards.
func (p *Pool) flushWaits() {
	if tally, due := p.waits.flush(time.Now()); due {
		p.warnWaits(tally)
	}
}

func (p *Pool) warnWaits(tally waitTally) {
	p.cfg.Log.WithFields(logrus.Fields{
		"waits":   tally.waits,
		"gave_up": tally.gaveUp,
		"longest": tally.longest.Round(time.Millisecond).String(),
		"limit":   p.cfg.MaxTotalConnections,
	}).Warn("calls waited for an iRODS connection because this replica was at " +
		"irods.maxtotalconnections, and those counted in gave_up failed; this usually means " +
		"many users were active at once or long transfers were holding connections")
}

// waitTally is what a warning about waits reports.
type waitTally struct {
	waits   int
	gaveUp  int
	longest time.Duration
}

// waitReport rolls up calls that waited for a connection, so a burst of them is one warning
// rather than one per call. The first wait after a quiet spell is reported at once; the
// rest are held for the next warning, at most one per waitReportInterval.
type waitReport struct {
	mu       sync.Mutex
	reported time.Time
	pending  waitTally
}

// record notes a wait that ended at now and returns the tally to warn about, if one is due.
func (r *waitReport) record(now time.Time, waited time.Duration, gaveUp bool) (waitTally, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.pending.waits++
	if gaveUp {
		r.pending.gaveUp++
	}
	r.pending.longest = max(r.pending.longest, waited)
	return r.takeLocked(now)
}

// flush returns the held-back tally if there is one and a warning is due.
func (r *waitReport) flush(now time.Time) (waitTally, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.pending.waits == 0 {
		return waitTally{}, false
	}
	return r.takeLocked(now)
}

func (r *waitReport) takeLocked(now time.Time) (waitTally, bool) {
	if !r.reported.IsZero() && now.Sub(r.reported) < waitReportInterval {
		return waitTally{}, false
	}
	tally := r.pending
	r.reported, r.pending = now, waitTally{}
	return tally, true
}

// chargedConnections sums what every tracked session is charged. It reads leased counts, so
// callers must hold p.mu. The entry being leased for is included even if it has no
// FileSystem tracked yet.
func chargedConnections(open map[*entry]usage, tracked map[*entry]*irodsfs.FileSystem, e *entry) int {
	var total int
	for te := range tracked {
		total += charge(open[te], te.leased)
	}
	if _, ok := tracked[e]; !ok {
		total += e.leased.total()
	}
	return total
}

// reclaimLocked evicts idle sessions that hold connections, least recently used first, until
// at least excess connections are on their way to being closed. Callers must hold p.mu.
func (p *Pool) reclaimLocked(open map[*entry]usage, excess int) {
	for el := p.lru.Back(); el != nil && excess > 0; {
		prev := el.Prev()
		e := el.Value.(*entry)

		if n := open[e].total(); e.inUse == 0 && n > 0 {
			excess -= n
			p.unlinkLocked(el, e)
		}
		el = prev
	}
}

// signalLocked wakes every call waiting for a connection. Callers must hold p.mu.
func (p *Pool) signalLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}
