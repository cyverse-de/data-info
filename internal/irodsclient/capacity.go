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

const (
	// metadataWeight is what an ordinary call is assumed to need.
	metadataWeight = 1

	// transferWeight is what a file transfer is charged. It runs on one I/O connection, but
	// a session's open count does not say which pool its connections are in, and an idle
	// metadata connection cannot carry a transfer. Charging two keeps that case inside the
	// cap, at the cost of overcharging a session that has nothing open.
	transferWeight = 2

	// capacityPollInterval is how often a waiting call recounts. go-irodsclient closes idle
	// connections on its own schedule without telling anybody, so waiting only for this
	// pool's own signals could sleep through a slot freed that way.
	capacityPollInterval = time.Second

	// waitReportInterval is the least time between warnings about calls that waited.
	waitReportInterval = time.Minute
)

// errPoolClosed is what a caller gets from a pool that has been closed.
var errPoolClosed = errors.New("irods pool is closed")

// lease reserves room for a call on e that may need up to weight connections, waiting
// until there is room, and returns the function that gives it back.
//
// The Data Store's HAProxy refuses a client address's connections past a small number, and
// every pod on a node shares an address, so a connection opened past it fails the request.
// go-irodsclient offers no hook on connection creation, so the count is kept from outside:
// each session is charged the larger of the connections it has open and what its calls in
// flight may need, and a call is admitted while the total stays within
// MaxTotalConnections. A waiting call makes room by evicting idle users' sessions, and
// otherwise waits for a call to finish or for go-irodsclient to close an idle connection.
func (p *Pool) lease(ctx context.Context, e *entry, weight int) (func(), error) {
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
		open := make(map[*entry]int, len(tracked))
		for te, fsys := range tracked {
			open[te] = p.countOpen(fsys)
		}

		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errPoolClosed
		}

		charged := chargedConnections(open, p.tracked, e)
		before := max(open[e], e.leased)
		need := max(open[e], e.leased+weight) - before

		// An empty replica admits anything, so a call heavier than the whole cap still runs.
		if charged+need <= p.cfg.MaxTotalConnections || charged == 0 {
			e.leased += weight
			p.mu.Unlock()

			if !waitingSince.IsZero() {
				p.reportWait(time.Since(waitingSince))
			}

			return func() {
				p.mu.Lock()
				defer p.mu.Unlock()
				e.leased -= weight
				p.signalLocked()
			}, nil
		}

		p.reclaimLocked(open, charged+need-p.cfg.MaxTotalConnections)
		p.mu.Unlock()

		if waitingSince.IsZero() {
			waitingSince = time.Now()
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("gave up after %s waiting for one of this replica's %d iRODS connections: %w",
				time.Since(waitingSince).Round(time.Millisecond), p.cfg.MaxTotalConnections, ctx.Err())
		case <-changed:
		case <-time.After(capacityPollInterval):
		}
	}
}

// reportWait records a call that had to wait for a connection, warning at most once per
// waitReportInterval with what has accumulated since the last warning.
func (p *Pool) reportWait(waited time.Duration) {
	due, waits, longest := p.waits.record(time.Now(), waited)
	if !due {
		return
	}
	p.cfg.Log.WithFields(logrus.Fields{
		"waits":   waits,
		"longest": longest.Round(time.Millisecond).String(),
		"limit":   p.cfg.MaxTotalConnections,
	}).Warn("calls waited for an iRODS connection because this replica was at " +
		"irods.maxtotalconnections; this usually means many users were active at once " +
		"or long transfers were holding connections")
}

// waitReport rolls up calls that waited for a connection, so a burst of them is one warning
// rather than one per call. Waits after a warning are carried into the next one.
type waitReport struct {
	mu       sync.Mutex
	reported time.Time
	waits    int
	longest  time.Duration
}

// record notes a wait that ended at now and reports whether a warning is due, with the
// count and the longest of the waits it covers.
func (r *waitReport) record(now time.Time, waited time.Duration) (due bool, waits int, longest time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.waits++
	r.longest = max(r.longest, waited)
	if !r.reported.IsZero() && now.Sub(r.reported) < waitReportInterval {
		return false, 0, 0
	}

	waits, longest = r.waits, r.longest
	r.reported, r.waits, r.longest = now, 0, 0
	return true, waits, longest
}

// chargedConnections sums what every tracked session is charged. It reads leased counts, so
// callers must hold p.mu. The entry being leased for is included even if it has no
// FileSystem tracked yet.
func chargedConnections(open map[*entry]int, tracked map[*entry]*irodsfs.FileSystem, e *entry) int {
	var total int
	for te := range tracked {
		total += max(open[te], te.leased)
	}
	if _, ok := tracked[e]; !ok {
		total += e.leased
	}
	return total
}

// reclaimLocked evicts idle sessions that hold connections, least recently used first, until
// at least excess connections are on their way to being closed. Callers must hold p.mu.
func (p *Pool) reclaimLocked(open map[*entry]int, excess int) {
	for el := p.lru.Back(); el != nil && excess > 0; {
		prev := el.Prev()
		e := el.Value.(*entry)

		if e.inUse == 0 && open[e] > 0 {
			excess -= open[e]
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
