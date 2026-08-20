package shadow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
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

	// Only read-only cases are supported so far. A write case needs paired fixtures so
	// each service touches its own copy; running one against a shared tree would have the
	// two services fighting over the same objects and would report differences that are
	// artefacts of the harness. Refusing is better than pretending.
	if c.Tier != TierRead {
		return Result{Case: c, Skipped: fmt.Sprintf("tier %q is not supported yet", c.Tier)}
	}

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

	var body io.Reader
	if c.Body != nil {
		encoded, err := json.Marshal(c.Body)
		if err != nil {
			return Response{}, fmt.Errorf("encoding the body: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, c.Method, target, body)
	if err != nil {
		return Response{}, err
	}
	if c.Body != nil {
		req.Header.Set("Content-Type", "application/json")
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
