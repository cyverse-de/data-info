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

// Checksum records a data object's checksum in the catalog.
//
// Every upload has to do this. The reference's client checksummed as it wrote, and the stat
// endpoints report that catalog column verbatim, so an object created without one answers
// with an empty md5 until something else computes it.
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

// SetPermission grants or removes a user's access to a path.
//
// PermissionNone removes it. iRODS spells that as its own access level rather than as the
// absence of one, so a caller wanting to unshare passes it here rather than calling
// something else.
func (s *Scope) SetPermission(ctx context.Context, path, user string, level Permission, recurse bool) error {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	perm := irodsclient.Permission(level)
	if err := irodsclient.SetACL(ctx, sess, path, perm, user, s.deps.Zone, recurse, false); err != nil {
		return err
	}

	s.invalidate(path)
	return nil
}

// SetInherit turns a collection's inheritance flag on or off.
func (s *Scope) SetInherit(ctx context.Context, path string, inherit, recurse bool) error {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	if err := irodsclient.SetInherit(ctx, sess, path, inherit, recurse, false); err != nil {
		return err
	}

	s.invalidate(path)
	return nil
}

// Inherits reports whether a collection passes its access list down to new children.
func (s *Scope) Inherits(ctx context.Context, path string) (bool, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return false, err
	}
	return irodsclient.Inherits(ctx, sess, normalizePath(path))
}

// Children lists what a collection directly holds, as full paths.
//
// This asks iRODS rather than the catalog, and is not paged: its callers are repairing
// permissions after a write and need to know what is there now, not what a listing query
// would show a particular user.
func (s *Scope) Children(ctx context.Context, path string) ([]string, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}

	entries, err := irodsclient.List(ctx, sess, normalizePath(path))
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Path)
	}
	return out, nil
}

// SetAVU records a metadata triple, replacing any the path already carries under the same
// attribute.
//
// iRODS allows several values under one attribute, so adding without removing would leave
// both -- and a path with two different recorded origins is a path that cannot be restored.
func (s *Scope) SetAVU(ctx context.Context, path string, avu AVU) error {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	existing, err := irodsclient.ListAVUs(ctx, sess, path)
	if err != nil {
		return err
	}

	for _, current := range existing {
		if current.Attribute != avu.Attribute {
			continue
		}
		if err := irodsclient.DeleteAVU(ctx, sess, path, current); err != nil {
			return err
		}
	}

	if err := irodsclient.AddAVU(ctx, sess, path, avu); err != nil {
		return err
	}

	s.invalidate(path)
	return nil
}

// AddAVUIfAbsent records a metadata triple unless the path already carries the same attribute
// and value.
//
// iRODS allows duplicates, and adding one that is already there would leave two rows a caller
// then has to delete twice. The unit is deliberately not part of the comparison: that is what
// the reference compares on, so re-adding an AVU with a different unit is a no-op rather than
// a change.
func (s *Scope) AddAVUIfAbsent(ctx context.Context, path string, avu AVU) error {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	existing, err := irodsclient.ListAVUs(ctx, sess, path)
	if err != nil {
		return err
	}
	for _, current := range existing {
		if current.Attribute == avu.Attribute && current.Value == avu.Value {
			return nil
		}
	}

	if err := irodsclient.AddAVU(ctx, sess, path, avu); err != nil {
		return err
	}

	s.invalidate(path)
	return nil
}

// DeleteAVU removes a metadata triple, matching on attribute and value.
//
// The unit is not compared, for the same reason it is not compared when adding: the reference
// deletes by attribute and value, so an AVU whose unit has drifted is still removed rather
// than left behind.
func (s *Scope) DeleteAVU(ctx context.Context, path string, avu AVU) error {
	path = normalizePath(path)

	sess, err := s.session(ctx)
	if err != nil {
		return err
	}

	existing, err := irodsclient.ListAVUs(ctx, sess, path)
	if err != nil {
		return err
	}

	for _, current := range existing {
		if current.Attribute != avu.Attribute || current.Value != avu.Value {
			continue
		}
		if err := irodsclient.DeleteAVU(ctx, sess, path, current); err != nil {
			return err
		}
	}

	s.invalidate(path)
	return nil
}

// MaxReadableFileSize bounds what ReadFile will return.
//
// These files are configuration a person wrote -- a CSV of metadata, a path list -- so this
// is generous for that and small enough that pointing the endpoint at a data file fails
// rather than filling memory.
const MaxReadableFileSize = 32 << 20

// ReadFile returns a data object's contents, up to MaxReadableFileSize.
func (s *Scope) ReadFile(ctx context.Context, path string) ([]byte, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}
	return irodsclient.ReadFile(ctx, sess, normalizePath(path), MaxReadableFileSize)
}

// ChildEntry is one member of a collection, with enough to know what it is.
type ChildEntry struct {
	Path  string
	IsDir bool
}

// ChildEntries lists what a collection holds, saying which members are collections.
//
// Children answers the same question without the types, and most callers only need paths.
// This exists for the walks that have to descend, which would otherwise cost a stat per
// member to find out where to go.
func (s *Scope) ChildEntries(ctx context.Context, path string) ([]ChildEntry, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}

	entries, err := irodsclient.List(ctx, sess, normalizePath(path))
	if err != nil {
		return nil, err
	}

	out := make([]ChildEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ChildEntry{Path: entry.Path, IsDir: entry.Type == irodsclient.ObjectTypeDir})
	}
	return out, nil
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
