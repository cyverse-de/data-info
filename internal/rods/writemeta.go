package rods

import (
	"context"

	"github.com/cyverse-de/data-info/internal/irodsclient"
)

// SetPermission grants a user an access level on a path, or removes their access when the
// level is PermissionNone.
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

	// Administrative only when acting as the service account itself; see Session.IsProxied.
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

	// Only a collection has an inheritance flag; for a data object this is a no-op.
	stat, err := s.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if stat.Type != ObjectTypeDir {
		return nil
	}

	// Never administrative, unlike the access-control calls, to match the Clojure service,
	// which did not switch inheritance changes on proxying.
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

// Children lists a collection's direct members as full paths, from iRODS rather than the
// catalog, because callers repairing permissions need what is there now, unfiltered by user.
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
// attribute, since iRODS would otherwise keep both values.
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
// and value. The unit is ignored, as the Clojure service ignored it, so re-adding with a
// different unit is a no-op.
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

// DeleteAVU removes metadata matching attribute and value, whatever its unit, so an AVU
// whose unit has drifted is still removed.
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
