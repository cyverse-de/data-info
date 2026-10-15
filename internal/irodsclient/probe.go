package irodsclient

import (
	"context"
	"fmt"
	"time"

	irodsfs "github.com/cyverse/go-irodsclient/fs"
)

// Probe reports whether iRODS is reachable and will accept our credentials. Unlike a bare
// TCP dial it exercises authentication, which is what the Clojure irods-running? check did
// by opening a Jargon connection and stat-ing the home collection.
//
// The result is cached briefly, because GET / reports iRODS health on every call without
// authentication. The probe itself runs in the background on its own budget, and a caller
// waits for it only as long as the caller's own context allows: the budget is longer than
// the kubelet's readiness timeout, and a probe that held a lock for all of it would queue
// every later health check behind it.
func (p *Pool) Probe(ctx context.Context) error {
	p.probeMu.Lock()
	if !p.probeAt.IsZero() && time.Since(p.probeAt) < p.cfg.ProbeCacheTTL {
		result := p.probeResult
		p.probeMu.Unlock()
		return result
	}
	running := p.probeRunning
	if running == nil {
		running = make(chan struct{})
		p.probeRunning = running
		go p.runProbe(ctx, running)
	}
	p.probeMu.Unlock()

	select {
	case <-running:
		p.probeMu.Lock()
		defer p.probeMu.Unlock()
		return p.probeResult
	case <-ctx.Done():
		return fmt.Errorf("the iRODS health check had not finished when the caller gave up "+
			"(%w); iRODS is probably slow to accept connections or unreachable", ctx.Err())
	}
}

// runProbe probes iRODS, records the result for Probe's callers, and closes running.
func (p *Pool) runProbe(ctx context.Context, running chan struct{}) {
	err := p.probeWithRetries(ctx)

	p.probeMu.Lock()
	defer p.probeMu.Unlock()
	p.probeResult = err
	p.probeAt = time.Now()
	p.probeRunning = nil
	close(running)
}

// probeWithRetries retries a refused connection with a linear backoff.
//
// A busy iRODS server answers a new connection by closing it, which surfaces as
// "connection rejected: EOF". That happens to healthy clients under ordinary load, so one
// refusal is not evidence that the backend is down. The retry lives here rather than at
// construction because connections are established lazily, inside the first operation --
// NewFileSystem does no I/O, so there is nothing to retry at that point.
//
// Only the probe retries. Repeating an arbitrary operation is not safe: a failure that
// looks like a refused connection may be a connection dropped part way through a write,
// and this layer cannot tell the two apart. Retrying reads is worth revisiting once the
// read paths exist and can say for themselves whether they are idempotent.
func (p *Pool) probeWithRetries(ctx context.Context) error {
	// Detached from the caller once, for the whole loop including its backoff. GET /
	// gives its probe a couple of seconds, which is long enough for a healthy server and
	// short enough that ordinary latency would look like a failure and discard a working
	// session. Inheriting the caller's cancellation would also abort the wait between
	// attempts and report that cancellation as the health result rather than what iRODS
	// actually said. The cached result means this runs rarely, so a longer budget costs
	// little.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.ProbeTimeout)
	defer cancel()

	var err error

	for attempt := 0; attempt <= p.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				// Out of budget. Report what iRODS said, not the deadline.
				return err
			case <-time.After(time.Duration(attempt) * p.cfg.RetrySleep):
			}
		}

		err = p.probe(ctx)
		if err == nil || !IsUnavailable(err) {
			return err
		}
	}

	return err
}

func (p *Pool) probe(ctx context.Context) error {
	s, err := p.session(ctx, probeKey, "")
	if err != nil {
		return err
	}
	defer s.Close()

	_, err = Do(ctx, s, func(fsys *irodsfs.FileSystem) (any, error) {
		return fsys.GetServerVersion()
	})
	return err
}
