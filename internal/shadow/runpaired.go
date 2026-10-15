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
)

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

	for _, side := range []struct {
		name string
		base string
		root string
		vars map[string]string
	}{
		{"reference", r.Reference, referenceRoot, referenceVars},
		{"candidate", r.Candidate, candidateRoot, candidateVars},
	} {
		if err := r.runSteps(ctx, c.Before, side.base, side.root, side.vars); err != nil {
			return nil, Result{Case: c, Err: fmt.Errorf("%s setup: %w", side.name, err)}
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

// runSteps puts one side's fixture into the state the case needs.
//
// A step's response is not compared. It is setup, so the only thing asked of it is that it
// worked: a step that fails fails the case, rather than being reported as a difference
// between two services that were never given the same starting point.
func (r *Runner) runSteps(ctx context.Context, steps []Step, base, root string, vars map[string]string) error {
	for i, step := range steps {
		if step.Seed != nil {
			seeded, err := r.Fixtures.Seed(ctx, step.Seed, r.User, root)
			if err != nil {
				return fmt.Errorf("step %d (%s): %w", i+1, step.describe(), err)
			}
			for k, v := range seeded {
				vars[k] = v
			}
			continue
		}

		expanded := step.Expand(vars)
		resp, err := r.send(ctx, base, Case{
			Method: expanded.Method,
			Path:   expanded.Path,
			Query:  expanded.Query,
			Body:   expanded.Body,
		})
		if err != nil {
			return fmt.Errorf("step %d (%s): %w", i+1, step.describe(), err)
		}
		if resp.Status >= 400 {
			return fmt.Errorf("step %d (%s): status %d: %s", i+1, step.describe(), resp.Status, resp.Body)
		}

		if step.CaptureTrashPath {
			trashPath, err := trashPathFrom(resp.Body)
			if err != nil {
				return fmt.Errorf("step %d (%s): %w", i+1, step.describe(), err)
			}
			vars["TrashPath"] = trashPath
		}

		if !step.Await {
			continue
		}
		if r.Tasks == nil {
			return fmt.Errorf("step %d (%s) awaits a task, but no async-tasks URL was configured", i+1, step.describe())
		}
		id := taskIDFrom(resp.Body)
		if id == "" {
			// The step was expected to start work and did not, so whatever the case
			// compares next would rest on a fixture that was never changed.
			return fmt.Errorf("step %d (%s) awaits a task, but the response carried no async-task-id", i+1, step.describe())
		}
		if _, err := r.awaitTask(ctx, id); err != nil {
			return fmt.Errorf("step %d (%s): %w", i+1, step.describe(), err)
		}
	}
	return nil
}

// trashPathFrom reads the one trash path a delete's response names.
func trashPathFrom(body []byte) (string, error) {
	var envelope struct {
		TrashPaths map[string]string `json:"trash-paths"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("reading trash-paths: %w", err)
	}
	if len(envelope.TrashPaths) != 1 {
		return "", fmt.Errorf("capturing a trash path needs exactly one under trash-paths, got %d", len(envelope.TrashPaths))
	}
	for _, trashPath := range envelope.TrashPaths {
		return trashPath, nil
	}
	return "", nil
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

	return Result{Case: c, Diffs: diffs, Code: errorCodeFrom(paired.referenceResp.Body)}
}

// errorCodeFrom pulls the error_code out of a response, if it carries one.
func errorCodeFrom(body []byte) string {
	var envelope struct {
		Code string `json:"error_code"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	return envelope.Code
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

	return Response{Status: resp.StatusCode, Body: raw, Headers: resp.Header}, nil
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
