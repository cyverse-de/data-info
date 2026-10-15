package rods

import (
	"context"

	"github.com/cyverse-de/data-info/internal/irodsclient"
)

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

	recurse, err = s.recurseIfCollection(ctx, path, recurse)
	if err != nil {
		return err
	}

	// Administrative only when this scope acts as the service account itself rather than on
	// somebody's behalf, which is the dispatch clj-jargon makes.
	perm := irodsclient.Permission(level)
	if err := irodsclient.SetACL(ctx, sess, path, perm, user, s.deps.Zone, recurse, !sess.IsProxied()); err != nil {
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

	// Only a collection has an inheritance flag. set-inherits and remove-inherits both
	// guard on is-dir? and do nothing for a data object, rather than asking iRODS about a
	// flag it does not have.
	stat, err := s.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if stat.Type != ObjectTypeDir {
		return nil
	}

	// Never administrative, unlike the access-control calls above: set-inherits and
	// remove-inherits do not dispatch on proxying, so neither does this.
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
// the Clojure service compares on, so re-adding an AVU with a different unit is a no-op
// rather than a change.
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
// The unit is not compared, for the same reason it is not compared when adding: the Clojure
// service deletes by attribute and value, so an AVU whose unit has drifted is still removed
// rather than left behind.
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
