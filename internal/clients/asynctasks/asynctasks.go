// Package asynctasks talks to the async-tasks service, which records long-running work. Every
// replica reads those records to decide whether a path is busy, so task type names, data keys
// and the meaning of an absent end date are a contract.
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

// StallTimeout is how long a task may go without reporting before it is treated as dead. Live
// jobs report once per path per step, so reaching it means the job's process is gone.
const StallTimeout = "10m"

// StatusStalled is the status a task is moved to when it stops reporting.
const StatusStalled = "detected-stalled"

// StallBehavior is the rule every lock-holding task is created with. Its "complete" flag makes
// the timeout set the end date, so a task whose process died releases its paths rather than
// holding them forever (docs/deferred-fixes.md #14). The processor matches only a task's
// latest status, so there is one rule per status a live task can sit at.
func StallBehavior() Behavior {
	live := []string{StatusRegistered, StatusStarted, StatusRunning}

	rules := make([]map[string]any, 0, len(live))
	for _, status := range live {
		rules = append(rules, map[string]any{
			"start_status": status,
			"end_status":   StatusStalled,
			"timeout":      StallTimeout,
			"complete":     true,
		})
	}

	return Behavior{
		Type: BehaviorStatusChangeTimeout,
		Data: map[string]any{"statuses": rules},
	}
}

// Task is one unit of long-running work. An absent EndDate is the only mark of an unfinished
// task, and is what holds the lock on its paths.
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

	// IncludeNullEnd keeps unfinished tasks. Pair it with EndDateSince, which alone would
	// exclude them because their end date is null.
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

// taskPath matches the prefix on ids that arrive as the Location path or URL Create returned.
var taskPath = regexp.MustCompile(`.*/tasks/`)

// NormalizeID reduces an id or a URI naming a task to the bare id.
func NormalizeID(id string) string { return taskPath.ReplaceAllString(id, "") }

// Create records a new task and returns its Location header verbatim; callers see that value
// as async-task-id.
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

// AddCompletedStatus records a task's final status and sets its end date, which releases the
// lock on its paths; that is why callers retry it harder than progress.
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

	// A filter matching nothing returns JSON null, which decodes to a nil slice.
	var tasks []Task
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return nil, fmt.Errorf("async-tasks: decoding a filtered task list: %w", err)
	}
	return tasks, nil
}

// query renders a filter as the service's underscore-named query parameters.
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
