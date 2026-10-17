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
// The result is cached for ProbeCacheTTL, because GET / reports iRODS health on every call
// without authentication. The probe runs in the background on its own budget and a caller
// waits only as long as its own context allows, so a slow probe never queues later health
// checks behind it.
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
// Only the probe retries: a failure that looks like a refused connection may be a
// connection dropped part way through a write, and this layer cannot tell the two apart.
func (p *Pool) probeWithRetries(ctx context.Context) error {
	// Detached from the caller for the whole loop, backoff included: a caller's deadline
	// must neither poison the admin session nor be reported as the health result.
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
