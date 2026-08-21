package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
)

// DefaultAsyncTimeout bounds how long an async case waits for a task to reach a terminal
// status. It is well short of the ten-minute stall timeout: a job that has not finished a
// fixture-sized move in this long is not slow, it is stuck, and waiting for the stall
// behavior to notice would cost ten minutes per case.
const DefaultAsyncTimeout = 90 * time.Second

// asyncPollInterval is how often a pending task is re-read. Short enough that a case does
// not spend most of its time waiting on the poll, long enough that a run does not hammer
// async-tasks: these jobs take seconds, not milliseconds.
const asyncPollInterval = time.Second

// taskIDFrom pulls the async task id out of a response.
//
// The key is hyphenated because it is the wire contract, and it is absent when nothing had
// to be done -- a move whose source is already at the destination reports no task. An empty
// id is a legitimate answer, not a failure; whether the two services agree about it is
// settled by the response diff.
func taskIDFrom(body []byte) string {
	var envelope struct {
		TaskID string `json:"async-task-id"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	return envelope.TaskID
}

// awaitTask polls until the task carries an end date.
//
// The end date is the completion signal rather than the status, and deliberately so: an
// absent end date is exactly what holds a path lock, so a task that has one is a task whose
// paths are free. Reading the last status instead would report a job finished while the
// thing that matters about finishing had not happened.
func (r *Runner) awaitTask(ctx context.Context, id string) (*asynctasks.Task, error) {
	deadline := time.Now().Add(r.asyncTimeout())

	ticker := time.NewTicker(asyncPollInterval)
	defer ticker.Stop()

	for {
		task, err := r.Tasks.GetByID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("reading task %s: %w", id, err)
		}
		if task.EndDate != nil {
			return task, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("task %s had no end date after %s; last status %q",
				id, r.asyncTimeout(), lastStatus(task))
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Runner) asyncTimeout() time.Duration {
	if r.AsyncTimeout > 0 {
		return r.AsyncTimeout
	}
	return DefaultAsyncTimeout
}

func lastStatus(t *asynctasks.Task) string {
	if len(t.Statuses) == 0 {
		return ""
	}
	return t.Statuses[len(t.Statuses)-1].Status
}

// statusTrail renders a task's status history for comparison.
//
// The sequence is what terrain's move poller watches, so it is contract rather than
// diagnostics: a client that shows progress reads these in order. Created dates are left
// out because they differ by construction; the details are kept, and the paths inside them
// canonicalise through the same side and uuid rules as any other body.
func statusTrail(t *asynctasks.Task) []byte {
	type entry struct {
		Status string `json:"status"`
		Detail string `json:"detail,omitempty"`
	}
	trail := make([]entry, 0, len(t.Statuses))
	for _, s := range t.Statuses {
		trail = append(trail, entry{Status: s.Status, Detail: s.Detail})
	}

	// A fixed shape rather than the bare array, so a diff names what it is looking at.
	encoded, err := json.Marshal(map[string]any{"statuses": trail})
	if err != nil {
		return []byte(`{"statuses":"unencodable"}`)
	}
	return encoded
}
