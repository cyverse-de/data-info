package shadow

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
)

// Runner sends each case to both services and compares the answers.
type Runner struct {
	// Reference is the service being replaced. Its answer is the expected one.
	Reference string

	// Candidate is the service under test.
	Candidate string

	Client     *http.Client
	Normalizer *Normalizer

	// Vars are substituted into each case.
	Vars map[string]string

	// Fixtures builds the paired trees write cases need. When nil, write cases are
	// reported as skipped rather than run against a shared tree.
	Fixtures *Fixtures

	// Reader performs the harness's own requests: building fixtures and reading back what
	// a write left behind.
	Reader *ServiceClient

	// User is who the harness acts as when building and inspecting fixtures.
	User string

	// Tasks reads async tasks. When nil, async cases are reported as skipped rather than
	// run: without it their state probe would race the job it is meant to observe, which
	// is worse than not running them, because it fails intermittently and in the
	// direction of passing.
	Tasks *asynctasks.Client

	// AsyncTimeout bounds the wait for one task. Zero means DefaultAsyncTimeout.
	AsyncTimeout time.Duration
}

// Result is one case's outcome.
type Result struct {
	Case    Case
	Diffs   []Diff
	Skipped string
	Err     error

	// Code is the error_code the reference service returned, when it returned one. It is
	// recorded rather than asserted: the harness compares the two services against each
	// other, so what a case elicits is an observation about the run, not an expectation
	// the case declares.
	Code string
}

// Passed reports whether the case matched.
func (r Result) Passed() bool { return r.Err == nil && r.Skipped == "" && len(r.Diffs) == 0 }

// NewRunner returns a runner with a bounded HTTP client.
func NewRunner(reference, candidate, runID string) *Runner {
	return &Runner{
		Reference: strings.TrimRight(reference, "/"),
		Candidate: strings.TrimRight(candidate, "/"),
		// An explicit timeout, because a hung comparison is indistinguishable from a
		// slow one and would stall the whole run.
		Client:     &http.Client{Timeout: 2 * time.Minute},
		Normalizer: NewNormalizer(runID),
		Vars:       map[string]string{},
	}
}

// Run compares every case.
//
// Cases run one at a time. The point is fidelity, not throughput, and both services share
// one iRODS zone whose connection budget is already tight.
func (r *Runner) Run(ctx context.Context, catalog *Catalog) []Result {
	cases := catalog.Cases()
	results := make([]Result, 0, len(cases))

	for _, c := range cases {
		results = append(results, r.runOne(ctx, c))
	}
	return results
}

func (r *Runner) runOne(ctx context.Context, c Case) Result {
	if c.Skip != "" {
		return Result{Case: c, Skipped: c.Skip}
	}

	switch c.Tier {
	case TierRead:
		return r.runRead(ctx, c)
	case TierWrite:
		return r.runWrite(ctx, c)
	case TierAsync:
		return r.runAsync(ctx, c)
	default:
		return Result{Case: c, Skipped: fmt.Sprintf("tier %q is not a tier", c.Tier)}
	}
}

// runRead sends the same request to both services. Nothing changes, so they can share a
// fixture.
func (r *Runner) runRead(ctx context.Context, c Case) Result {
	expanded := c.Expand(r.Vars)

	reference, err := r.send(ctx, r.Reference, expanded)
	if err != nil {
		return Result{Case: c, Err: fmt.Errorf("reference: %w", err)}
	}

	candidate, err := r.send(ctx, r.Candidate, expanded)
	if err != nil {
		return Result{Case: c, Err: fmt.Errorf("candidate: %w", err)}
	}

	return Result{
		Case:  c,
		Diffs: r.Normalizer.Compare(reference, candidate),
		Code:  errorCodeFrom(reference.Body),
	}
}

// runWrite gives each service its own copy of the fixture and compares two things: what each
// answered, and what each left behind.
//
// The state comparison is the one that matters. A write can return an identical response
// while having created the wrong thing, put it in the wrong place, or created nothing at
// all, and only reading the tree afterwards catches that. It covers what StateOf can see --
// every path below the fixture root, with its type, size, checksum, timestamps and the
// requesting user's own permission. Access granted to somebody else is not covered; see the
// note on StateOf.
func (r *Runner) runWrite(ctx context.Context, c Case) Result {
	paired, result := r.sendPaired(ctx, c)
	if paired == nil {
		return result
	}
	return r.withStateDiffs(ctx, c, paired, paired.diffs)
}

// runAsync is runWrite with the job waited on first.
//
// The endpoints these cases hit return as soon as the task is created, so reading the tree
// straight afterwards races the work. Waiting for the task to carry an end date settles
// that, and the wait is not only bookkeeping: the end date is what releases the paths, so a
// case that gets one has also observed the lock being freed.
//
// The status trail is compared too. terrain's move poller reads that sequence to show
// progress, which makes it contract rather than diagnostics.
func (r *Runner) runAsync(ctx context.Context, c Case) Result {
	if r.Tasks == nil {
		return Result{Case: c, Skipped: "no async-tasks URL was configured, so async cases would race the job they are meant to observe"}
	}

	paired, result := r.sendPaired(ctx, c)
	if paired == nil {
		return result
	}
	diffs := paired.diffs

	referenceID := taskIDFrom(paired.referenceResp.Body)
	candidateID := taskIDFrom(paired.candidateResp.Body)

	// Neither side started a job -- an error, or a request that turned out to be a no-op.
	// Whether that agreement is correct is the response diff's business, not ours.
	if referenceID == "" && candidateID == "" {
		return r.withStateDiffs(ctx, c, paired, diffs)
	}

	// Only one side did. That is a difference in its own right, and waiting on the other's
	// absent task would ask async-tasks for an empty id, which answers with a list and hid
	// the difference behind a decoding error. The task that exists is still waited for, so
	// the state comparison does not race it.
	if referenceID == "" || candidateID == "" {
		started, id := "candidate", candidateID
		if candidateID == "" {
			started, id = "reference", referenceID
		}
		if _, err := r.awaitTask(ctx, id); err != nil {
			return Result{Case: c, Err: fmt.Errorf("waiting for the %s's task: %w", started, err)}
		}
		diffs = append(diffs, Diff{Kind: "task", Detail: "only the " + started + " started a task"})
		return r.withStateDiffs(ctx, c, paired, diffs)
	}

	referenceTask, err := r.awaitTask(ctx, referenceID)
	if err != nil {
		return Result{Case: c, Err: fmt.Errorf("waiting for the reference's task: %w", err)}
	}
	candidateTask, err := r.awaitTask(ctx, candidateID)
	if err != nil {
		return Result{Case: c, Err: fmt.Errorf("waiting for the candidate's task: %w", err)}
	}

	for _, d := range r.Normalizer.Compare(
		Response{Status: 200, Body: statusTrail(referenceTask)},
		Response{Status: 200, Body: statusTrail(candidateTask)},
	) {
		diffs = append(diffs, Diff{Kind: "task:" + d.Kind, Detail: d.Detail})
	}

	return r.withStateDiffs(ctx, c, paired, diffs)
}
