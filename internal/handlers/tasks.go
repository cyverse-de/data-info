package handlers

import (
	"context"
	"fmt"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/jobs"
	"github.com/cyverse-de/data-info/internal/worker"
)

// startTask records the task before starting its job, because the record is what locks the
// task's paths.
func (h *Writes) startTask(
	ctx context.Context,
	taskType, user string,
	data map[string]any,
	job worker.Job,
) (string, error) {
	if h.deps.Creator == nil || h.deps.Worker == nil {
		return "", fmt.Errorf("no async task machinery is configured, so %s cannot be started", taskType)
	}

	// Also in the data, so an operator can find which replica owns a stalled task without
	// reading its history.
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
		// The cause is logged, not sent, because it names async-tasks' URL (deferred fix 24).
		return "", apierror.New(apierror.ErrUnavailable).
			With("reason", fmt.Sprintf("could not record the %s task", taskType)).
			WithCause(fmt.Errorf("recording a %s task: %w", taskType, err))
	}

	if err := h.deps.Worker.Start(taskID, taskType, job); err != nil {
		// The paths stay locked until asynctasks.StallTimeout releases them; completing a task
		// this service never ran would be wrong. Unavailable, because a retry should go elsewhere.
		return "", apierror.New(apierror.ErrUnavailable).
			With("reason", fmt.Sprintf("could not start the %s job; retry the request", taskType)).
			WithCause(fmt.Errorf("starting the %s job for %s: %w", taskType, taskID, err))
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
