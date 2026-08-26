// Package asynctasks talks to the async-tasks service, which records long-running work so
// that it survives the request that started it.
//
// The records are a cross-service contract, not private bookkeeping. Every replica of this
// service reads them to decide whether a path is already being moved or deleted, and so does
// the Clojure implementation for as long as both are deployed -- so the type names, the keys
// inside a task's data, and the meaning of an absent end date all have to match exactly.
package asynctasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// Task types this service creates. They are read by name, both here and elsewhere.
const (
	TypeMove          = "data-move"
	TypeRename        = "data-rename"
	TypeDelete        = "data-delete"
	TypeDeleteTrash   = "data-delete-trash"
	TypeRestore       = "data-restore"
	TypeUploadCleanup = "data-upload-cleanup"
)

// Status values a task moves through.
const (
	StatusRegistered = "registered"
	StatusStarted    = "started"
	StatusRunning    = "running"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// BehaviorStatusChangeTimeout is the behavior that gives a task a deadline between status
// changes.
const BehaviorStatusChangeTimeout = "statuschangetimeout"

// StallTimeout is how long a task may go without reporting before it is treated as dead.
//
// A move reports once per path per step, so a job that is alive has no trouble staying well
// inside this. Reaching it means the process running the job is gone.
const StallTimeout = "10m"

// StatusStalled is the status a task is moved to when it stops reporting.
const StatusStalled = "detected-stalled"

// StallBehavior is the rule every task that holds a lock is created with.
//
// The "complete" flag is the whole point of it, and the Clojure service omits it. Without the
// flag the timeout records that a task has stalled and stops there, leaving its end date null
// -- and since an absent end date is exactly what holds the lock, a task whose process died
// locks its paths for good rather than for ten minutes. With the flag the same timeout
// completes the task, which releases them.
//
// The trade is real but small: a job that genuinely goes ten minutes without reporting has
// its paths released while it is still working. Jobs here report per path per step, so that
// window belongs to a process that is gone, not one that is busy. Recorded in
// docs/deferred-fixes.md.
func StallBehavior() Behavior {
	return Behavior{
		Type: BehaviorStatusChangeTimeout,
		Data: map[string]any{
			"statuses": []map[string]any{{
				"start_status": StatusRunning,
				"end_status":   StatusStalled,
				"timeout":      StallTimeout,
				"complete":     true,
			}},
		},
	}
}

// Task is one unit of long-running work.
//
// An absent EndDate is what makes a task count as still running, and therefore what holds
// the lock on its paths. Nothing else marks a task as finished.
type Task struct {
	ID        string         `json:"id,omitempty"`
	Type      string         `json:"type"`
	Username  string         `json:"username,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	StartDate *time.Time     `json:"start_date,omitempty"`
	EndDate   *time.Time     `json:"end_date,omitempty"`
	Behaviors []Behavior     `json:"behaviors,omitempty"`
	Statuses  []Status       `json:"statuses,omitempty"`
}

// Behavior is a rule the async-tasks service applies to a task on its own schedule.
type Behavior struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// Status is one entry in a task's history.
type Status struct {
	Status      string    `json:"status"`
	Detail      string    `json:"detail,omitempty"`
	CreatedDate time.Time `json:"created_date,omitempty"`
}

// Filter selects tasks. Every field is optional and they combine.
type Filter struct {
	IDs       []string
	Types     []string
	Statuses  []string
	Usernames []string

	// IncludeNullEnd keeps tasks that have not finished. Selecting unfinished work needs
	// this together with EndDateSince: the date alone would exclude the very tasks that
	// matter, because theirs is null.
	IncludeNullEnd bool

	EndDateSince   *time.Time
	EndDateBefore  *time.Time
	StartDateSince *time.Time
}

// Client is an async-tasks HTTP client.
type Client struct {
	base *url.URL
	http *http.Client
}

// New builds a client for the service at base.
func New(base string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("async-tasks: base URL %q is not usable: %w", base, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("async-tasks: base URL %q needs a scheme and a host", base)
	}

	return &Client{base: parsed, http: &http.Client{Timeout: timeout}}, nil
}

// taskPath matches the prefix a task id may carry.
//
// Create answers with a Location header rather than a bare id, and this service passes that
// value around and hands it back to callers as async-task-id, so an id arriving here may be
// a path or a whole URL. Everything that addresses a task strips it back down first.
var taskPath = regexp.MustCompile(`.*/tasks/`)

// NormalizeID reduces an id or a URI naming a task to the bare id.
func NormalizeID(id string) string { return taskPath.ReplaceAllString(id, "") }

// Create records a new task and returns the id minted for it.
//
// The value returned is the Location header verbatim, because that is what the Clojure
// service returns and what callers see in an async-task-id field.
func (c *Client) Create(ctx context.Context, task Task) (string, error) {
	body, err := json.Marshal(task)
	if err != nil {
		return "", fmt.Errorf("async-tasks: encoding a task: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, c.url("tasks"), body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck // the body is drained below

	drain(resp)

	location := resp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("async-tasks: creating a %s task: no Location header was returned", task.Type)
	}
	return location, nil
}

// GetByID fetches one task, with its statuses and behaviors.
func (c *Client) GetByID(ctx context.Context, id string) (*Task, error) {
	resp, err := c.do(ctx, http.MethodGet, c.url("tasks", NormalizeID(id)), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // the body is decoded below

	var task Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return nil, fmt.Errorf("async-tasks: decoding task %q: %w", id, err)
	}
	return &task, nil
}

// DeleteByID removes a task.
func (c *Client) DeleteByID(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, c.url("tasks", NormalizeID(id)), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to read

	drain(resp)
	return nil
}

// AddStatus records progress on a task without finishing it.
func (c *Client) AddStatus(ctx context.Context, id string, status Status) error {
	return c.addStatus(ctx, id, status, false)
}

// AddCompletedStatus records a task's final status and sets its end date.
//
// The end date is the point of it. An absent one is what holds the lock on a task's paths,
// so a task whose terminal status never lands stays locked -- which is why callers retry
// this far harder than they retry progress.
func (c *Client) AddCompletedStatus(ctx context.Context, id string, status Status) error {
	return c.addStatus(ctx, id, status, true)
}

func (c *Client) addStatus(ctx context.Context, id string, status Status, complete bool) error {
	body, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("async-tasks: encoding a status: %w", err)
	}

	target := c.url("tasks", NormalizeID(id), "status")
	if complete {
		target.RawQuery = url.Values{"complete": {"true"}}.Encode()
	}

	resp, err := c.do(ctx, http.MethodPost, target, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to read

	drain(resp)
	return nil
}

// AddBehavior attaches a rule to an existing task.
func (c *Client) AddBehavior(ctx context.Context, id string, behavior Behavior) error {
	body, err := json.Marshal(behavior)
	if err != nil {
		return fmt.Errorf("async-tasks: encoding a behavior: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, c.url("tasks", NormalizeID(id), "behaviors"), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to read

	drain(resp)
	return nil
}

// ByFilter returns the tasks matching a filter.
func (c *Client) ByFilter(ctx context.Context, filter Filter) ([]Task, error) {
	target := c.url("tasks")
	target.RawQuery = filter.query().Encode()

	resp, err := c.do(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // the body is decoded below

	// The service answers a filter matching nothing with a bare null rather than an empty
	// array, which decodes to a nil slice. Callers range over the result, so that is
	// exactly what they want -- but it means a nil result is not a signal of anything.
	var tasks []Task
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return nil, fmt.Errorf("async-tasks: decoding a filtered task list: %w", err)
	}
	return tasks, nil
}

// query renders a filter as the service's query parameters.
//
// The parameter names are the service's own spelling, which is not this package's: they use
// underscores where the rest of the DE uses hyphens.
func (f Filter) query() url.Values {
	values := url.Values{}

	for _, id := range f.IDs {
		values.Add("id", id)
	}
	for _, t := range f.Types {
		values.Add("type", t)
	}
	for _, s := range f.Statuses {
		values.Add("status", s)
	}
	for _, u := range f.Usernames {
		values.Add("username", u)
	}

	// Presence is what counts, not the value: the service tests the parameter's length.
	if f.IncludeNullEnd {
		values.Set("include_null_end", strconv.FormatBool(true))
	}

	for name, when := range map[string]*time.Time{
		"end_date_since":   f.EndDateSince,
		"end_date_before":  f.EndDateBefore,
		"start_date_since": f.StartDateSince,
	} {
		if when != nil {
			values.Set(name, when.UTC().Format(time.RFC3339Nano))
		}
	}

	return values
}

// drain empties a response body so the connection can be reused. A read failure here says
// nothing the caller can act on -- the request already succeeded.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body) //nolint:errcheck // see above
}

func (c *Client) url(elements ...string) *url.URL {
	return c.base.JoinPath(elements...)
}

// do performs one request, treating any 4xx or 5xx as an error.
func (c *Client) do(ctx context.Context, method string, target *url.URL, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("async-tasks: building a request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("async-tasks: %s %s: %w", method, target.Path, err)
	}

	if resp.StatusCode >= 400 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) //nolint:errcheck // the request already failed; a partial body is still worth reporting
		resp.Body.Close()                                       //nolint:errcheck // the request already failed
		return nil, fmt.Errorf("async-tasks: %s %s returned %d: %s",
			method, target.Path, resp.StatusCode, bytes.TrimSpace(detail))
	}

	return resp, nil
}
