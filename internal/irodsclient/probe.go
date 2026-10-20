package irodsclient

import (
	"context"
	"fmt"
	"time"

	irodsfs "github.com/cyverse/go-irodsclient/fs"
)

// Probe reports whether iRODS is reachable and accepts our credentials. The result is
// cached for ProbeCacheTTL, and the probe runs on its own budget while each caller waits
// only as long as its context allows.
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

// probeWithRetries retries a refused connection with linear backoff: a busy iRODS server
// closes new connections ("connection rejected: EOF") even from healthy clients. Only the
// probe retries, since elsewhere a refusal cannot be told apart from a connection dropped
// part way through a write.
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
