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

	// Checksum records a data object's checksum in the catalog, returning it.
	Checksum(ctx context.Context, path string) (string, error)

	// Move renames a path, whatever kind of thing is at it.
	Move(ctx context.Context, from, to string) error

	// Delete removes a path, taking a collection's contents with it.
	Delete(ctx context.Context, path string, force bool) error

	// SetPermission grants a user an access level, or removes their access when the level
	// is PermissionNone.
	SetPermission(ctx context.Context, path, user string, level Permission, recurse bool) error

	// SetInherit turns a collection's inheritance flag on or off.
	SetInherit(ctx context.Context, path string, inherit, recurse bool) error

	// Inherits reports whether a collection passes its access list down to new children.
	Inherits(ctx context.Context, path string) (bool, error)

	// Children lists what a collection directly holds.
	Children(ctx context.Context, path string) ([]string, error)
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
// Administrative only when the session is not proxied; see Session.IsProxied. Acting as the
// user, the grant is theirs to make and the ordinary call is right; acting as the service
// account directly, the ordinary call is the one iRODS refuses.
//
// Recursion is the caller's to decide, and the two callers differ: the Clojure service grants
// recursively on a new collection, so that everything created beneath it in the same request
// is covered, and non-recursively on a data object, where there is nothing beneath it.
func (s *Scope) SetOwner(ctx context.Context, path, user string, recurse bool) error {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	recurse, err = s.recurseIfCollection(ctx, path, recurse)
	if err != nil {
		return err
	}

	if err := irodsclient.SetACL(ctx, sess, path, irodsclient.PermissionOwn, user, s.deps.Zone, recurse, !sess.IsProxied()); err != nil {
		return err
	}

	s.invalidate(path)
	return nil
}

// WriteFile streams r into a new data object at path.
//
// No resource is named, so the object lands on the one the service authenticated with, which
// is the deployment's configured default. That is the same resource the Clojure service wrote
// to, which took it from the same setting by way of its connection.
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

// Checksum records a data object's checksum in the catalog.
//
// Every upload has to do this. The Clojure service's client checksummed as it wrote, and the
// stat endpoints report that catalog column verbatim, so an object created without one
// answers with an empty md5 until something else computes it.
func (s *Scope) Checksum(ctx context.Context, path string) (string, error) {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return "", err
	}

	sum, err := irodsclient.Checksum(ctx, sess, path, "")
	if err != nil {
		return "", err
	}

	s.invalidate(path)
	return sum, nil
}

// Move renames a path.
//
// Nothing is done here about permissions. Across collections they need repair, and what that
// repair is depends on both collections' inheritance flags, so it belongs to the caller that
// knows why the move is happening -- see service.RepairPermissions.
func (s *Scope) Move(ctx context.Context, from, to string) error {
	from, to = normalizePath(from), normalizePath(to)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	if err := irodsclient.Move(ctx, sess, from, to); err != nil {
		return err
	}

	s.invalidate(from)
	s.invalidate(to)
	return nil
}

// Delete removes a path.
//
// A collection is removed with everything under it: the endpoints that reach here have
// already told the user that is what will happen, and iRODS refuses a non-recursive removal
// of anything that is not empty.
func (s *Scope) Delete(ctx context.Context, path string, force bool) error {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	stat, err := s.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}

	if stat.Type == ObjectTypeDir {
		err = irodsclient.RemoveDir(ctx, sess, path, true, force)
	} else {
		err = irodsclient.RemoveFile(ctx, sess, path, force)
	}
	if err != nil {
		return err
	}

	s.invalidate(path)
	return nil
}

// Invalidate forgets what the scope remembered about a path.
//
// Every write through this scope does it already. It is exported for the handlers that
// change a path through a *different* scope -- the ones that create a collection as the
// service's own account and then report it as the caller -- because those two scopes have
// separate memories and the caller's would otherwise still hold the answer from before.
func (s *Scope) Invalidate(path string) { s.invalidate(normalizePath(path)) }

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

// recurseIfCollection drops a recursion request for a data object.
//
// Recursion is a property of a collection, and iRODS answers CAT_INVALID_ARGUMENT for an
// access change that asks for it on a data object -- which made every unshare of a file fail,
// and with it every delete of one carrying access from a non-inheriting parent. clj-jargon's
// set-permissions never had the problem because it routes files and collections to different
// functions and only the collection one takes the flag at all.
func (s *Scope) recurseIfCollection(ctx context.Context, path string, recurse bool) (bool, error) {
	if !recurse {
		return false, nil
	}

	stat, err := s.Stat(ctx, path).Get(ctx)
	if err != nil {
		return false, err
	}
	return stat.Type == ObjectTypeDir, nil
}
