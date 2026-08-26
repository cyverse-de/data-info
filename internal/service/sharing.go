package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/cyverse-de/data-info/internal/lazy"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
)

// Reasons a share or unshare was not performed. They are reported to the caller as successes
// carrying a reason, because nothing went wrong -- there was simply nothing to do.
const (
	SkipShareWithSelf   = "share-with-self"
	SkipShareFromTrash  = "share-from-trash"
	SkipAlreadyShared   = "already-shared"
	SkipUnshareWithSelf = "unshare-with-self"
	SkipNotShared       = "not-shared"
)

// ShareStore is what sharing needs from the data store.
type ShareStore interface {
	PermissionStore

	Stat(ctx context.Context, path string) *lazy.Value[rods.Stat]
	SetInherit(ctx context.Context, path string, inherit, recurse bool) error
}

// ShareRequest is one path being shared with one user.
type ShareRequest struct {
	// Owner is who is doing the sharing, and Sharee is who it is being shared with.
	Owner  string
	Sharee string

	Path       string
	Permission rods.Permission

	// AdminUsers and ProxyUser hold access that is structural rather than shared, which is
	// what decides whether a collection is still shared with anyone.
	AdminUsers map[string]bool
	ProxyUser  string

	Layout paths.Layout
}

// Share gives a user access to a path, returning the reason it was skipped, or "" when it was
// done.
//
// Sharing is more than one permission change. A user given access to something buried in
// somebody else's tree cannot reach it without read access to every collection on the way, so
// those are granted too -- and a shared collection gets its inherit bit set, so that what is
// put into it later is shared as well rather than silently private.
func Share(ctx context.Context, store ShareStore, req ShareRequest) (string, error) {
	if req.Owner == req.Sharee {
		return SkipShareWithSelf, nil
	}
	if req.Layout.InTrash(req.Path) {
		// Sharing something on its way out is not useful, and the trash is per-user, so
		// the sharee could not reach it anyway.
		return SkipShareFromTrash, nil
	}

	held, err := permissionOf(ctx, store, req.Path, req.Sharee)
	if err != nil {
		return "", err
	}
	if held == req.Permission {
		return SkipAlreadyShared, nil
	}

	home := ownerHomeOf(req.Path)
	stop := func(path string) bool {
		return path == home || req.Layout.IsTrashBase(path)
	}

	// The collections between the shared path and its owner's home, so the sharee can
	// navigate to it. Anyone already holding write or own keeps it.
	for _, ancestor := range ancestorsOf(req.Path, stop) {
		current, err := permissionOf(ctx, store, ancestor, req.Sharee)
		if err != nil {
			return "", err
		}
		if rods.Permits(current, rods.PermissionRead) {
			continue
		}
		if err := store.SetPermission(ctx, ancestor, req.Sharee, rods.PermissionRead, false); err != nil {
			return "", fmt.Errorf("granting %q read on %q: %w", req.Sharee, ancestor, err)
		}
	}

	stat, err := store.Stat(ctx, req.Path).Get(ctx)
	if err != nil {
		return "", err
	}
	if stat.Type == rods.ObjectTypeDir {
		// Without this, anything uploaded into a shared collection afterwards would be
		// visible only to whoever put it there -- which looks like the share silently
		// stopped working.
		if err := store.SetInherit(ctx, req.Path, true, true); err != nil {
			return "", fmt.Errorf("setting the inherit bit on %q: %w", req.Path, err)
		}
	}

	// The owner's home itself, which the walk above deliberately stops short of. It is
	// granted rather than walked because it is the one collection every share has to touch
	// and the only one whose access is not specific to this path.
	homeHeld, err := permissionOf(ctx, store, home, req.Sharee)
	if err != nil {
		return "", err
	}
	if !rods.Permits(homeHeld, rods.PermissionRead) {
		if err := store.SetPermission(ctx, home, req.Sharee, rods.PermissionRead, false); err != nil {
			return "", fmt.Errorf("granting %q read on %q: %w", req.Sharee, home, err)
		}
	}

	if err := store.SetPermission(ctx, req.Path, req.Sharee, req.Permission, true); err != nil {
		return "", fmt.Errorf("granting %q %s on %q: %w", req.Sharee, req.Permission, req.Path, err)
	}

	return "", nil
}

// Unshare takes a user's access to a path away, returning the reason it was skipped, or ""
// when it was done.
func Unshare(ctx context.Context, store ShareStore, req ShareRequest) (string, error) {
	if req.Owner == req.Sharee {
		return SkipUnshareWithSelf, nil
	}

	held, err := permissionOf(ctx, store, req.Path, req.Sharee)
	if err != nil {
		return "", err
	}
	if !rods.Permits(held, rods.PermissionRead) {
		return SkipNotShared, nil
	}

	if err := store.SetPermission(ctx, req.Path, req.Sharee, rods.PermissionNone, true); err != nil {
		return "", fmt.Errorf("removing %q's access to %q: %w", req.Sharee, req.Path, err)
	}

	stat, err := store.Stat(ctx, req.Path).Get(ctx)
	if err != nil {
		return "", err
	}
	if stat.Type == rods.ObjectTypeDir {
		if err := clearInheritWhenUnshared(ctx, store, req); err != nil {
			return "", err
		}
	}

	// The read access granted purely so this could be reached is taken back, stopping at
	// the first collection that still holds something the sharee can see.
	home := ownerHomeOf(req.Path)
	stop := func(path string) bool {
		return path == home || req.Layout.IsTrashBase(path)
	}

	for _, ancestor := range ancestorsOf(req.Path, stop) {
		current, err := permissionOf(ctx, store, ancestor, req.Sharee)
		if err != nil {
			return "", err
		}
		if current != rods.PermissionRead {
			// Write or own was granted for its own sake, and iRODS levels do not allow
			// read to be removed without removing those too.
			continue
		}

		reachable, err := holdsAnythingReadableBy(ctx, store, ancestor, req.Sharee)
		if err != nil {
			return "", err
		}
		if reachable {
			break
		}

		if err := store.SetPermission(ctx, ancestor, req.Sharee, rods.PermissionNone, false); err != nil {
			return "", fmt.Errorf("removing %q's access to %q: %w", req.Sharee, ancestor, err)
		}
	}

	return "", nil
}

// clearInheritWhenUnshared turns the inherit bit off once a collection is not shared with
// anyone.
//
// Left on, it would keep handing the owner's access list to new children of a collection
// nobody else can see -- harmless today and confusing the next time it is shared.
func clearInheritWhenUnshared(ctx context.Context, store ShareStore, req ShareRequest) error {
	entries, err := store.ACL(ctx, req.Path).Get(ctx)
	if err != nil {
		return fmt.Errorf("reading the access list of %q: %w", req.Path, err)
	}

	for _, entry := range entries {
		if entry.User == req.Owner || entry.User == req.ProxyUser || req.AdminUsers[entry.User] {
			continue
		}
		// Still shared with somebody.
		return nil
	}

	if err := store.SetInherit(ctx, req.Path, false, true); err != nil {
		return fmt.Errorf("clearing the inherit bit on %q: %w", req.Path, err)
	}
	return nil
}

// ownerHomeOf names the home collection a shared path lives under.
//
// It is taken from the path rather than from the requesting user, because the two are not the
// same: sharing something out of a collection somebody else owns still has to grant access to
// that person's home, not to the sharer's.
func ownerHomeOf(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) < 4 {
		return strings.TrimRight(path, "/")
	}
	return strings.Join(parts[:4], "/")
}
