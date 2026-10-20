// Package lazy provides values whose computation starts immediately and is awaited later, so
// a caller can dispatch several lookups (such as those for one listing entry) and have them
// run concurrently rather than one after another.
package lazy

import (
	"context"

	"github.com/cyverse-de/data-info/internal/panics"
)

// Value is a computation started at construction and awaited later.
type Value[T any] struct {
	done chan struct{}

	value T
	err   error
}

// Go starts fn immediately on its own goroutine. ctx bounds the work, while Get's ctx bounds
// only a wait, so one caller giving up does not cancel work another awaits. sem, if non-nil,
// is acquired inside the goroutine, so dispatching never blocks.
func Go[T any](ctx context.Context, sem Semaphore, fn func(context.Context) (T, error)) *Value[T] {
	v := &Value[T]{done: make(chan struct{})}

	go func() {
		defer close(v.done)
		defer func() {
			if p := panics.From(recover()); p != nil {
				v.err = p
			}
		}()

		if sem != nil {
			if err := sem.Acquire(ctx, 1); err != nil {
				v.err = err
				return
			}
			defer sem.Release(1)
		}

		v.value, v.err = fn(ctx)
	}()

	return v
}

// Resolved returns a Value that is already complete.
func Resolved[T any](value T) *Value[T] {
	v := &Value[T]{done: make(chan struct{}), value: value}
	close(v.done)
	return v
}

// Failed returns a Value that is already complete and failed.
func Failed[T any](err error) *Value[T] {
	v := &Value[T]{done: make(chan struct{}), err: err}
	close(v.done)
	return v
}

// Get waits for the computation and returns its result; ctx bounds only the wait. A finished
// result is returned even if ctx is done, rather than letting select choose at random.
func (v *Value[T]) Get(ctx context.Context) (T, error) {
	select {
	case <-v.done:
		return v.value, v.err
	default:
	}

	select {
	case <-v.done:
		return v.value, v.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// Peek returns the result if it is ready, without waiting, so one lookup can reuse another's
// finished result but never stall on it.
func (v *Value[T]) Peek() (value T, err error, ok bool) {
	select {
	case <-v.done:
		return v.value, v.err, true
	default:
		var zero T
		return zero, nil, false
	}
}

// Semaphore bounds how much work runs at once. golang.org/x/sync/semaphore.Weighted
// satisfies it.
type Semaphore interface {
	Acquire(ctx context.Context, n int64) error
	Release(n int64)
}

// Await waits on each value in argument order and returns the first error in that order.
func Await(ctx context.Context, waits ...func(context.Context) error) error {
	for _, wait := range waits {
		if err := wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Wait adapts a Value to the function Await takes, discarding the result.
func Wait[T any](v *Value[T]) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := v.Get(ctx)
		return err
	}
}
