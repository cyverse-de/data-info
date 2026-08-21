package handlers

import (
	"context"
	"fmt"

	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/jobs"
	"github.com/cyverse-de/data-info/internal/worker"
)

// startTask records a long-running operation and dispatches the job that performs it.
//
// The record comes first and the job second, and the order matters: the record is what locks
// the task's paths, so a job that started before it existed would be working on a tree
// anything else was still free to touch.
func (h *Writes) startTask(
	ctx context.Context,
	taskType, user string,
	data map[string]any,
	job worker.Job,
) (string, error) {
	if h.deps.Creator == nil || h.deps.Worker == nil {
		return "", fmt.Errorf("no async task machinery is configured, so %s cannot be started", taskType)
	}

	// The instance is recorded in the task's data as well as in its statuses, because an
	// operator looking for which replica owns a stalled task should not have to read the
	// history to find out.
	data["instance-id"] = worker.InstanceID()

	taskID, err := h.deps.Creator.Create(ctx, asynctasks.Task{
		Type:     taskType,
		Username: user,
		Data:     data,
		Statuses: []asynctasks.Status{{
			Status: asynctasks.StatusRegistered,
			Detail: "[" + worker.InstanceID() + "]",
		}},
		Behaviors: []asynctasks.Behavior{asynctasks.StallBehavior()},
	})
	if err != nil {
		return "", fmt.Errorf("recording a %s task: %w", taskType, err)
	}

	if err := h.deps.Worker.Start(taskID, taskType, job); err != nil {
		// The record exists and its paths are locked, but nothing is going to do the work.
		// Nothing here can unlock them -- completing a task this service did not perform
		// would be a lie -- so the stall timeout is what releases them, ten minutes from
		// now. Reported as unavailable because retrying elsewhere is the right response.
		return "", fmt.Errorf("starting the %s job for %s: %w", taskType, taskID, err)
	}

	return taskID, nil
}

// jobDeps builds what a job needs from what the handlers have.
func (h *Writes) jobDeps() jobs.Deps {
	return jobs.Deps{
		OpenScope:  h.deps.OpenScope,
		Notifier:   h.deps.Notifier,
		Publisher:  h.deps.Publisher,
		Log:        h.deps.Logger(),
		Layout:     h.deps.Layout,
		AdminUsers: h.deps.AdminUsers,
		ProxyUser:  h.deps.ProxyUser,
	}
}
