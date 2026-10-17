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
// it authenticated as.
//
// It decides whether an access-control change is made administratively. clj-jargon dispatches
// set-dataobj-perms and set-coll-perms on the same question: a proxied connection uses the
// ordinary calls, and one acting as the service account directly uses the InAdminMode ones.
// Getting it backwards is not cosmetic -- iRODS answers CAT_INVALID_ARGUMENT for an ordinary
// removal the account is not entitled to make, which failed every delete of a file carrying
// access from a non-inheriting parent.
func (s *Session) IsProxied() bool { return s.clientUser != "" }

// FileSystem exposes the underlying client. Prefer Do, which adds cancellation and error
// translation.
func (s *Session) FileSystem() *irodsfs.FileSystem { return s.fs }

// Close returns the session to the pool.
func (s *Session) Close() {
	if s.closed.Swap(true) {
		return
	}
	s.pool.release(s.entry, s.poisoned.Load())
}

// Do runs fn against the session's FileSystem, adding the cancellation the library does
// not provide and translating the result into the DE error vocabulary.
//
// No go-irodsclient method takes a context.Context, so a cancelled request cannot abort an
// iRODS call already in flight. Do races the call against ctx and returns as soon as
// either finishes. When ctx wins, the call is still running: it holds a connection that is
// mid-protocol, with a half-read response nobody will consume, so the session is marked
// poisoned and released rather than handed to the next caller.
//
// The orphaned goroutine is not leaked indefinitely. go-irodsclient turns its operation
// timeouts into real socket deadlines, so a blocked read unwinds with a deadline error
// once the budget expires. That budget is per round trip rather than per call, so a
// paged query can take a multiple of it -- which is why both the operation and the long
// operation timeouts are kept modest on the metadata connection.
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

			// A connection or authentication failure is recorded on the underlying
			// session and makes every later acquisition on it fail. Discarding the
			// session keeps a failed connection from ever being reused.
			//
			// It also sidesteps the mutex leak that upstream v0.20.1 and v0.21.0 have on
			// that path, where AcquireConnection returns while still holding the session
			// mutex and wedges every later caller with no socket to time out. v0.21.4
			// fixes it; this stays because a connection that has already failed is not
			// worth handing to the next request either way.
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
