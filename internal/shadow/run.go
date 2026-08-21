package shadow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
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

	return Result{Case: c, Diffs: r.Normalizer.Compare(reference, candidate)}
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

// pairedRun is what a paired case produced: which subtree each service was given, and what
// each answered.
type pairedRun struct {
	referenceRoot string
	candidateRoot string
	referenceResp Response
	candidateResp Response
	diffs         []Diff
}

// sendPaired builds a subtree per service, sends the case to each, and diffs the answers.
// A nil first return means the case is finished -- skipped or failed -- and the Result says
// why.
func (r *Runner) sendPaired(ctx context.Context, c Case) (*pairedRun, Result) {
	if r.Fixtures == nil || r.Reader == nil {
		return nil, Result{Case: c, Skipped: "no scratch collection was configured, so paired cases cannot be run"}
	}

	referenceRoot, candidateRoot, err := r.Fixtures.Prepare(ctx, c, r.User)
	if err != nil {
		return nil, Result{Case: c, Err: fmt.Errorf("preparing fixtures: %w", err)}
	}

	referenceVars := r.varsWithRoot(referenceRoot)
	candidateVars := r.varsWithRoot(candidateRoot)

	if c.Seed != nil {
		for root, vars := range map[string]map[string]string{
			referenceRoot: referenceVars,
			candidateRoot: candidateVars,
		} {
			seeded, err := r.Fixtures.Seed(ctx, c.Seed, r.User, root)
			if err != nil {
				return nil, Result{Case: c, Err: fmt.Errorf("seeding fixtures: %w", err)}
			}
			for k, v := range seeded {
				vars[k] = v
			}
		}
	}

	referenceResp, err := r.send(ctx, r.Reference, c.Expand(referenceVars))
	if err != nil {
		return nil, Result{Case: c, Err: fmt.Errorf("reference: %w", err)}
	}

	candidateResp, err := r.send(ctx, r.Candidate, c.Expand(candidateVars))
	if err != nil {
		return nil, Result{Case: c, Err: fmt.Errorf("candidate: %w", err)}
	}

	return &pairedRun{
		referenceRoot: referenceRoot,
		candidateRoot: candidateRoot,
		referenceResp: referenceResp,
		candidateResp: candidateResp,
		diffs:         r.Normalizer.Compare(referenceResp, candidateResp),
	}, Result{}
}

// withStateDiffs reads back both subtrees and appends what differs.
func (r *Runner) withStateDiffs(ctx context.Context, c Case, paired *pairedRun, diffs []Diff) Result {
	referenceState, err := r.Reader.StateOf(ctx, r.User, paired.referenceRoot)
	if err != nil {
		return Result{Case: c, Err: fmt.Errorf("reading the reference's result: %w", err)}
	}
	candidateState, err := r.Reader.StateOf(ctx, r.User, paired.candidateRoot)
	if err != nil {
		return Result{Case: c, Err: fmt.Errorf("reading the candidate's result: %w", err)}
	}

	for _, d := range r.Normalizer.Compare(
		Response{Status: 200, Body: referenceState},
		Response{Status: 200, Body: candidateState},
	) {
		diffs = append(diffs, Diff{Kind: "state:" + d.Kind, Detail: d.Detail})
	}

	return Result{Case: c, Diffs: diffs}
}

// varsWithRoot points a case at one side's copy of the fixture.
func (r *Runner) varsWithRoot(root string) map[string]string {
	vars := make(map[string]string, len(r.Vars)+1)
	for k, v := range r.Vars {
		vars[k] = v
	}
	vars["Root"] = root
	return vars
}

// send issues one request.
func (r *Runner) send(ctx context.Context, base string, c Case) (Response, error) {
	target := base + c.Path
	if len(c.Query) > 0 {
		values := url.Values{}
		// Sorted so the two services see identical query strings.
		keys := make([]string, 0, len(c.Query))
		for k := range c.Query {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			values.Set(k, c.Query[k])
		}
		target += "?" + values.Encode()
	}

	var (
		body        io.Reader
		contentType string
	)
	switch {
	case c.Upload != nil:
		encoded, boundary, err := multipartBody(c.Upload)
		if err != nil {
			return Response{}, err
		}
		body, contentType = bytes.NewReader(encoded), boundary
	case c.Body != nil:
		encoded, err := json.Marshal(c.Body)
		if err != nil {
			return Response{}, fmt.Errorf("encoding the body: %w", err)
		}
		body, contentType = bytes.NewReader(encoded), "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, c.Method, target, body)
	if err != nil {
		return Response{}, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := r.Client.Do(req)
	if err != nil {
		return Response{}, err
	}
	// The body is fully read below; a close failure after that tells the caller nothing
	// they can act on.
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("reading the response: %w", err)
	}

	return Response{Status: resp.StatusCode, Body: raw}, nil
}

// multipartBody encodes an upload case, returning the body and the content type that
// carries its boundary.
func multipartBody(u *Upload) ([]byte, string, error) {
	field := u.Field
	if field == "" {
		field = "file"
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	part, err := w.CreateFormFile(field, u.Filename)
	if err != nil {
		return nil, "", fmt.Errorf("building the upload body: %w", err)
	}
	if _, err := io.WriteString(part, u.Content); err != nil {
		return nil, "", fmt.Errorf("writing the upload body: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("closing the upload body: %w", err)
	}

	return buf.Bytes(), w.FormDataContentType(), nil
}

// Report renders results, and reports whether everything that ran matched.
//
// Skipped cases are listed rather than counted silently. A harness that quietly drops cases
// reads as "everything passed" when it means "everything I bothered to run passed".
func Report(w io.Writer, results []Result) (bool, error) {
	out := &errWriter{w: w}
	var passed, failed, skipped int

	for _, res := range results {
		switch {
		case res.Err != nil:
			failed++
			out.printf("ERROR   %s: %v\n", res.Case.ID, res.Err)
		case res.Skipped != "":
			skipped++
			out.printf("SKIP    %s: %s\n", res.Case.ID, res.Skipped)
		case len(res.Diffs) > 0:
			failed++
			out.printf("DIFF    %s\n", res.Case.ID)
			for _, d := range res.Diffs {
				out.printf("          %s: %s\n", d.Kind, d.Detail)
			}
		default:
			passed++
			out.printf("ok      %s\n", res.Case.ID)
		}
	}

	out.printf("\n%d matched, %d differed, %d skipped\n", passed, failed, skipped)
	if skipped > 0 {
		out.printf("note: skipped cases were not compared; the run is not evidence about them\n")
	}

	return failed == 0, out.err
}

// errWriter records the first write failure so a report does not have to check every line.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}
