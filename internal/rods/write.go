package rods

import (
	"context"
	"io"

	"github.com/cyverse-de/data-info/internal/irodsclient"
)

// Ops are the operations that change the data store. Unlike View they are not lazy, since a
// write cannot be dispatched speculatively and abandoned, and all go over the iRODS
// protocol: writing the catalog behind the server's back would desynchronise its caches.
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

	s.invalidate(path)
	return nil
}

// SetOwner makes a user the owner of a path, administratively only when the session is not
// proxied (see Session.IsProxied). recurse is ignored for a data object.
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

// WriteFile streams r into a new data object at path, on the deployment's configured
// default resource.
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

// FileExists reports whether a data object is at path, asking the server rather than the
// catalog because callers are checking a write they just made.
func (s *Scope) FileExists(ctx context.Context, path string) (bool, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return false, err
	}
	return irodsclient.ExistsFile(ctx, sess, normalizePath(path))
}

// Checksum records a data object's checksum in the catalog. Every upload needs it, or stat
// reports an empty md5 for that object.
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

// Move renames a path. Permissions are left alone: repairing them across collections is the
// caller's job (see service.RepairPermissions).
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

// Delete removes a path, a collection with everything under it, since iRODS refuses a
// non-recursive removal of a collection that is not empty.
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

// Invalidate forgets what the scope remembered about a path. Writes through this scope do
// it already; call it when the path was changed through a different scope.
func (s *Scope) Invalidate(path string) { s.invalidate(normalizePath(path)) }

// invalidate is Invalidate for an already-normalised path.
func (s *Scope) invalidate(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.rows, path)
	for _, kind := range []kind{kindItem, kindACL, kindAVUs, kindChildCounts} {
		delete(s.memo, memoKey{kind, path})
	}
}

// recurseIfCollection drops a recursion request for a data object, for which iRODS answers
// CAT_INVALID_ARGUMENT to a recursive access change.
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
