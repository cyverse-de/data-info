// Package lazy provides values whose computation starts immediately and is awaited later.
//
// This is the shape the Clojure service's data access relied on. Its accessors returned
// delays that had already been dispatched to a thread pool, so a caller could ask for six
// things about a directory entry and then await all six, and they would have been resolved
// concurrently. Formatting one listing entry does exactly that, so a value that only began
// computing when it was first read would turn one round of concurrent work into six
// sequential ones.
package lazy

import (
	"context"
)

// Value is a computation started at construction and awaited later.
type Value[T any] struct {
	done chan struct{}

	value T
	err   error
}

// Go starts fn immediately on its own goroutine.
//
// ctx bounds the computation itself. Get takes its own context, which bounds only the wait,
// so a caller that gives up does not cancel work another caller may still be waiting for.
//
// sem, when non-nil, is acquired inside the goroutine before the work begins and released
// after. Acquiring inside rather than outside is what makes this non-blocking to construct:
// a caller can dispatch a dozen lookups and await them all, and only as many as the
// semaphore allows will be in flight.
func Go[T any](ctx context.Context, sem Semaphore, fn func(context.Context) (T, error)) *Value[T] {
	v := &Value[T]{done: make(chan struct{})}

	go func() {
		defer close(v.done)

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

// Get waits for the computation and returns its result. ctx bounds only the wait.
func (v *Value[T]) Get(ctx context.Context) (T, error) {
	select {
	case <-v.done:
		return v.value, v.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// Peek returns the result if it is ready, without waiting.
//
// This is what lets one lookup answer from another's result without risking a deadlock or a
// stall: a stat can check whether a listing has already resolved and use it, but must never
// block waiting for one, since the listing may be much more expensive or may never have been
// requested at all.
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

// Await resolves several values concurrently and returns the first error.
//
// The values have already started; this only waits. It exists so a caller can express
// "I need all of these" without writing the same loop each time.
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
