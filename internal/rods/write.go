package rods

import (
	"context"
	"io"

	"github.com/cyverse-de/data-info/internal/irodsclient"
)

// Ops are the operations that change the data store.
//
// They are separate from View and deliberately not lazy. A read may be dispatched
// speculatively and abandoned; a write may not, and every one of these needs its error
// handled at the call site.
//
// They all go over the iRODS protocol. The catalog is iRODS' own database and this service
// never writes to it: doing so behind the server's back would leave its caches disagreeing
// with what is on disk.
type Ops interface {
	// MakeDir creates a collection. With recurse it creates missing parents too.
	MakeDir(ctx context.Context, path string, recurse bool) error

	// SetOwner makes a user the owner of a path, optionally recursing into it.
	SetOwner(ctx context.Context, path, user string, recurse bool) error

	// WriteFile streams r into a new data object at path, returning the bytes written.
	WriteFile(ctx context.Context, path string, r io.Reader) (int64, error)

	// Rename moves a data object within its own collection.
	Rename(ctx context.Context, from, to string) error

	// DeleteFile removes a data object. With force it skips the trash.
	DeleteFile(ctx context.Context, path string, force bool) error

	// FileExists reports whether a data object is at path, bypassing any cache.
	FileExists(ctx context.Context, path string) (bool, error)
}

var _ Ops = (*Scope)(nil)

// MakeDir creates a collection.
func (s *Scope) MakeDir(ctx context.Context, path string, recurse bool) error {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	if err := irodsclient.MakeDir(ctx, sess, path, recurse); err != nil {
		return err
	}

	// What the caller knew about this path is now wrong.
	s.invalidate(path)
	return nil
}

// SetOwner makes a user the owner of a path.
//
// Not in admin mode: the service is already acting as the user, who has just created the
// path and can therefore grant on it. The proxy account is not a rodsadmin, so asking for
// the administrative flag is refused outright rather than being a harmless extra privilege.
//
// Recursion is the caller's to decide, and the two callers differ: the reference grants
// recursively on a new collection, so that everything created beneath it in the same request
// is covered, and non-recursively on a data object, where there is nothing beneath it.
func (s *Scope) SetOwner(ctx context.Context, path, user string, recurse bool) error {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	if err := irodsclient.SetACL(ctx, sess, path, irodsclient.PermissionOwn, user, s.deps.Zone, recurse, false); err != nil {
		return err
	}

	s.invalidate(path)
	return nil
}

// WriteFile streams r into a new data object at path.
//
// No resource is named, so the object lands on the one the service authenticated with,
// which is the deployment's configured default. That is the same resource the reference
// wrote to, which took it from the same setting by way of its connection.
func (s *Scope) WriteFile(ctx context.Context, path string, r io.Reader) (int64, error) {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return 0, err
	}

	written, err := irodsclient.WriteFile(ctx, sess, path, r, "")
	if err != nil {
		return written, err
	}

	s.invalidate(path)
	return written, nil
}

// Rename moves a data object.
func (s *Scope) Rename(ctx context.Context, from, to string) error {
	from, to = normalizePath(from), normalizePath(to)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	if err := irodsclient.RenameFile(ctx, sess, from, to); err != nil {
		return err
	}

	s.invalidate(from)
	s.invalidate(to)
	return nil
}

// DeleteFile removes a data object.
func (s *Scope) DeleteFile(ctx context.Context, path string, force bool) error {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	if err := irodsclient.RemoveFile(ctx, sess, path, force); err != nil {
		return err
	}

	s.invalidate(path)
	return nil
}

// FileExists reports whether a data object is at path.
//
// This asks the server directly rather than going through the catalog reads the rest of the
// scope uses, because its callers are checking the result of a write they just made.
func (s *Scope) FileExists(ctx context.Context, path string) (bool, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return false, err
	}
	return irodsclient.ExistsFile(ctx, sess, normalizePath(path))
}

// invalidate forgets what the scope remembered about a path, so a read after a write sees
// the change rather than the answer from before it.
func (s *Scope) invalidate(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.rows, path)
	for _, kind := range []kind{kindItem, kindACL, kindAVUs, kindChildCounts} {
		delete(s.memo, memoKey{kind, path})
	}
}
