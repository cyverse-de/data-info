package irodsclient

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/cyverse-de/data-info/internal/panics"
	irodsfs "github.com/cyverse/go-irodsclient/fs"
)

// Session is a checked-out FileSystem bound to one client user. Return it with Close.
type Session struct {
	pool       *Pool
	entry      *entry
	fs         *irodsfs.FileSystem
	clientUser string

	poisoned atomic.Bool
	closed   atomic.Bool
}

// ClientUser reports the user this session acts as. It is empty for the proxy account.
func (s *Session) ClientUser() string { return s.clientUser }

// IsProxied reports whether this session acts on behalf of somebody other than the account
// it authenticated as. Access-control changes use admin mode exactly when it is not: an
// unproxied ordinary call gets CAT_INVALID_ARGUMENT for removals the account cannot make.
func (s *Session) IsProxied() bool { return s.clientUser != "" }

// Close returns the session to the pool.
func (s *Session) Close() {
	if s.closed.Swap(true) {
		return
	}
	s.pool.release(s.entry, s.poisoned.Load())
}

// Do runs fn against the session's FileSystem, racing it against ctx and translating errors
// into the DE vocabulary. go-irodsclient takes no context, so a call abandoned on
// cancellation keeps running mid-protocol: the session is poisoned, and the orphaned call
// unwinds at the operation timeout's socket deadline.
func Do[T any](ctx context.Context, s *Session, fn func(*irodsfs.FileSystem) (T, error)) (T, error) {
	return DoPath(ctx, s, "", fn)
}

// DoPath is Do with a path folded into any error it produces, so callers whose operation
// concerns a single path do not have to re-wrap the result.
func DoPath[T any](ctx context.Context, s *Session, path string, fn func(*irodsfs.FileSystem) (T, error)) (T, error) {
	return doLeased(ctx, s, path, metadataConn, fn)
}

// doLeased is DoPath for a call drawing on the given kind of connection.
func doLeased[T any](ctx context.Context, s *Session, path string, kind connKind, fn func(*irodsfs.FileSystem) (T, error)) (T, error) {
	var zero T

	if err := ctx.Err(); err != nil {
		return zero, err
	}

	release, err := s.pool.lease(ctx, s.entry, kind)
	if err != nil {
		return zero, err
	}
	defer release()

	return run(ctx, s, path, fn)
}

// run is the body of DoPath for a caller that already holds a lease.
func run[T any](ctx context.Context, s *Session, path string, fn func(*irodsfs.FileSystem) (T, error)) (T, error) {
	var zero T

	type result struct {
		value T
		err   error
	}
	// Buffered so the goroutine can finish and exit even after ctx has won the race.
	done := make(chan result, 1)

	go func() {
		defer func() {
			if p := panics.From(recover()); p != nil {
				done <- result{err: p}
			}
		}()
		value, err := fn(s.fs)
		done <- result{value, err}
	}()

	select {
	case r := <-done:
		// A panic may have left the connection part way through a request.
		var panicked *panics.Error
		if errors.As(r.err, &panicked) {
			s.poisoned.Store(true)
			return zero, r.err
		}
		if r.err != nil {
			err := Translate(r.err, path, s.clientUser)

			// go-irodsclient records a connection or authentication failure on its session
			// and fails every later acquisition, so discard it.
			if IsUnavailable(err) {
				s.poisoned.Store(true)
			}
			return zero, err
		}
		return r.value, nil
	case <-ctx.Done():
		s.poisoned.Store(true)
		return zero, ctx.Err()
	}
}
