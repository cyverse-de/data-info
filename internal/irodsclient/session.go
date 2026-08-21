package irodsclient

import (
	"context"
	"sync/atomic"

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

// FileSystem exposes the underlying client for the few callers that need it directly,
// such as file handles that outlive a single call. Prefer Do, which adds cancellation and
// error translation.
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
	var zero T

	if err := ctx.Err(); err != nil {
		return zero, err
	}

	type result struct {
		value T
		err   error
	}
	// Buffered so the goroutine can finish and exit even after ctx has won the race.
	done := make(chan result, 1)

	go func() {
		value, err := fn(s.fs)
		done <- result{value, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			err := Translate(r.err, path, s.clientUser)

			// A connection or authentication failure is recorded on the underlying
			// session and makes every later acquisition on it fail. Discarding the
			// session keeps a failed connection from ever being reused.
			//
			// It also sidesteps the mutex leak that upstream v0.20.1 and v0.21.0 have on
			// that path, where AcquireConnection returns while still holding the session
			// mutex and wedges every later caller with no socket to time out. go.mod
			// points at a fork carrying the fix; this stays because a connection that has
			// already failed is not worth handing to the next request either way.
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
