package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/cyverse-de/data-info/internal/lazy"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
)

// PermissionStore is the deliberately narrow surface that permission repair after a move
// may touch.
type PermissionStore interface {
	ACL(ctx context.Context, path string) *lazy.Value[[]rods.ACLEntry]
	ACLs(ctx context.Context, paths []string) *lazy.Value[map[string][]rods.ACLEntry]
	Inherits(ctx context.Context, path string) (bool, error)
	Children(ctx context.Context, path string) ([]string, error)
	SetPermission(ctx context.Context, path, user string, level rods.Permission, recurse bool) error
}

// MoveContext is what a permission repair needs to know about the move that happened.
type MoveContext struct {
	// Source and Destination are where the thing was and where it now is.
	Source      string
	Destination string

	// User is who asked for the move. Their own access is never touched.
	User string

	// AdminUsers and ProxyUser are accounts whose access is structural rather than shared,
	// so they are left alone too.
	AdminUsers map[string]bool
	ProxyUser  string

	// Layout locates the collections a repair must never climb past.
	Layout paths.Layout
}

// RepairPermissions fixes access lists after a move, since iRODS carries an object's ACL
// along unchanged. What it does depends on whether the source and destination collections
// inherit; a move within one collection needs nothing.
func RepairPermissions(ctx context.Context, store PermissionStore, move MoveContext) error {
	sourceParent := paths.Dir(move.Source)
	destinationParent := paths.Dir(move.Destination)

	if sourceParent == destinationParent {
		return nil
	}

	sourceInherits, err := store.Inherits(ctx, sourceParent)
	if err != nil {
		return fmt.Errorf("reading the inheritance of %q: %w", sourceParent, err)
	}
	destinationInherits, err := store.Inherits(ctx, destinationParent)
	if err != nil {
		return fmt.Errorf("reading the inheritance of %q: %w", destinationParent, err)
	}

	// Coming out of a collection that does not inherit, whoever could see the moved thing
	// may have been given read access to its ancestors purely to reach it. That access is
	// obsolete now, and nothing else will remove it.
	if !sourceInherits {
		if err := removeObsoleteAccess(ctx, store, move); err != nil {
			return err
		}
	}

	switch {
	case destinationInherits:
		// The destination hands its own access list to what it contains, so whatever the
		// object arrived with is not what it should have.
		if err := clearAccess(ctx, store, move); err != nil {
			return err
		}
		return grantParentAccess(ctx, store, move)

	case sourceInherits:
		// It arrived carrying the source's list, and the destination grants nothing, so
		// the list it carried is all that would be left. Strip it.
		return clearAccess(ctx, store, move)

	default:
		// Neither collection inherits, so the object keeps the list it arrived with -- and
		// the people on it need to be able to reach it where it now is.
		return grantAncestorAccess(ctx, store, move)
	}
}

// clearAccess removes everyone else's access to the moved object.
func clearAccess(ctx context.Context, store PermissionStore, move MoveContext) error {
	sharees, err := shareesOf(ctx, store, move.Destination, move)
	if err != nil {
		return err
	}

	for _, sharee := range sharees {
		if err := store.SetPermission(ctx, move.Destination, sharee, rods.PermissionNone, true); err != nil {
			return fmt.Errorf("removing %q's access to %q: %w", sharee, move.Destination, err)
		}
	}
	return nil
}

// grantParentAccess gives the moved object the destination collection's access list, as
// inheritance would have on creation.
func grantParentAccess(ctx context.Context, store PermissionStore, move MoveContext) error {
	parent := paths.Dir(move.Destination)

	entries, err := store.ACL(ctx, parent).Get(ctx)
	if err != nil {
		return fmt.Errorf("reading the access list of %q: %w", parent, err)
	}

	for _, entry := range entries {
		if move.ignores(entry.User) {
			continue
		}
		if err := store.SetPermission(ctx, move.Destination, entry.User, entry.Permission, true); err != nil {
			return fmt.Errorf("granting %q %s on %q: %w", entry.User, entry.Permission, move.Destination, err)
		}
	}
	return nil
}

// grantAncestorAccess grants each sharee read on the collections above the moved object so
// they can navigate to it. Existing write or own is kept, since iRODS access is a single level
// and setting read would downgrade it.
func grantAncestorAccess(ctx context.Context, store PermissionStore, move MoveContext) error {
	sharees, err := shareesOf(ctx, store, move.Destination, move)
	if err != nil {
		return err
	}

	// The walk stops at the zone's shared roots. Unlike the source-side cleanup it does not
	// stop at the requesting user's own home: reaching something inside it is the whole
	// point of the grant.
	ancestors := ancestorsOf(move.Destination, func(path string) bool {
		return move.Layout.IsSharing(path) || move.Layout.IsTrashBase(path)
	})

	for _, sharee := range sharees {
		for _, ancestor := range ancestors {
			held, err := permissionOf(ctx, store, ancestor, sharee)
			if err != nil {
				return err
			}
			if held == rods.PermissionWrite || held == rods.PermissionOwn {
				continue
			}
			if err := store.SetPermission(ctx, ancestor, sharee, rods.PermissionRead, false); err != nil {
				return fmt.Errorf("granting %q read on %q: %w", sharee, ancestor, err)
			}
		}
	}
	return nil
}

// removeObsoleteAccess takes back read access to the collections the moved object used to be
// reached through, for anyone who can no longer see anything in them.
//
// The walk stops at the first collection that still holds something the sharee can read:
// above that point their access is earning its keep for some other object.
func removeObsoleteAccess(ctx context.Context, store PermissionStore, move MoveContext) error {
	parent := paths.Dir(move.Source)

	entries, err := store.ACL(ctx, parent).Get(ctx)
	if err != nil {
		return fmt.Errorf("reading the access list of %q: %w", parent, err)
	}

	// This walk also stops at the requesting user's own home, which the destination-side
	// walk does not. Stripping access there would undo sharing the user set up deliberately
	// rather than access granted only to reach the thing that moved.
	ancestors := ancestorsOf(move.Source, func(path string) bool {
		return move.Layout.IsSharing(path) ||
			move.Layout.IsTrashBase(path) ||
			path == move.Layout.UserHome(move.User)
	})

	for _, entry := range entries {
		if move.ignores(entry.User) {
			continue
		}

		for _, ancestor := range ancestors {
			// Only access that exists purely to reach something is taken back. Write or
			// own on a collection was granted for its own sake, and iRODS levels do not
			// let read be removed without removing those too.
			held, err := permissionOf(ctx, store, ancestor, entry.User)
			if err != nil {
				return err
			}
			if held != rods.PermissionRead {
				continue
			}

			reachable, err := holdsAnythingReadableBy(ctx, store, ancestor, entry.User)
			if err != nil {
				return err
			}
			if reachable {
				// Their access is earning its keep here and, by the same argument,
				// everywhere above.
				break
			}

			if err := store.SetPermission(ctx, ancestor, entry.User, rods.PermissionNone, false); err != nil {
				return fmt.Errorf("removing %q's access to %q: %w", entry.User, ancestor, err)
			}
		}
	}
	return nil
}

// permissionOf reports what one user holds on a path.
func permissionOf(ctx context.Context, store PermissionStore, path, user string) (rods.Permission, error) {
	entries, err := store.ACL(ctx, path).Get(ctx)
	if err != nil {
		return rods.PermissionNone, fmt.Errorf("reading the access list of %q: %w", path, err)
	}

	for _, entry := range entries {
		if entry.User == user {
			return entry.Permission, nil
		}
	}
	return rods.PermissionNone, nil
}

// holdsAnythingReadableBy reports whether a collection directly holds something a user can
// read.
func holdsAnythingReadableBy(ctx context.Context, store PermissionStore, path, user string) (bool, error) {
	children, err := store.Children(ctx, path)
	if err != nil {
		return false, fmt.Errorf("listing %q: %w", path, err)
	}
	if len(children) == 0 {
		return false, nil
	}

	// One query rather than one per child. A home collection can hold thousands of entries
	// and this runs once per sharee per ancestor.
	acls, err := store.ACLs(ctx, children).Get(ctx)
	if err != nil {
		return false, fmt.Errorf("reading the access lists under %q: %w", path, err)
	}

	for _, entries := range acls {
		for _, entry := range entries {
			if entry.User == user && entry.Permission != rods.PermissionNone {
				return true, nil
			}
		}
	}
	return false, nil
}

// shareesOf lists who other than the requester and the service's own accounts can see a path.
func shareesOf(ctx context.Context, store PermissionStore, path string, move MoveContext) ([]string, error) {
	entries, err := store.ACL(ctx, path).Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the access list of %q: %w", path, err)
	}

	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if move.ignores(entry.User) {
			continue
		}
		out = append(out, entry.User)
	}
	return out, nil
}

// ignores reports whether a permission entry belongs to an account whose access is structural
// rather than shared, and so must be left alone.
func (m MoveContext) ignores(user string) bool {
	return user == m.User || user == m.ProxyUser || m.AdminUsers[user]
}

// ancestorsOf lists the collections above path, nearest first, stopping before the first one
// stop accepts or the zone root.
func ancestorsOf(path string, stop func(string) bool) []string {
	var out []string

	for current := paths.Dir(path); ; current = paths.Dir(current) {
		if current == "" || current == "/" || current == "." || stop(current) {
			return out
		}
		// A path outside the expected layout would otherwise walk all the way up, and
		// granting a sharee read on the zone root would give them the whole zone's listing.
		if strings.Count(current, "/") < 2 {
			return out
		}
		out = append(out, current)

		if parent := paths.Dir(current); parent == current {
			return out
		}
	}
}
