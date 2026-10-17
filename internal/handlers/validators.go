package handlers

import (
	"context"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/rods"
)

// requireAllExist rejects a request naming anything missing, listing every missing path. Pass
// the service account's scope when a path the caller can't see should fail the following
// permission check rather than be reported missing.
func requireAllExist(ctx context.Context, scope *rods.Scope, requested []string) error {
	stats, err := scope.Stats(ctx, requested).Get(ctx)
	if err != nil {
		return err
	}

	var missing []string
	for _, path := range requested {
		if stat, ok := stats[path]; !ok || !stat.Exists {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		return apierror.New(apierror.ErrDoesNotExist).With("paths", missing)
	}
	return nil
}

// requireNoneExist rejects a request naming anything that already is there.
func requireNoneExist(ctx context.Context, scope *rods.Scope, requested []string) error {
	stats, err := scope.Stats(ctx, requested).Get(ctx)
	if err != nil {
		return err
	}

	var existing []string
	for _, path := range requested {
		if stat, ok := stats[path]; ok && stat.Exists {
			existing = append(existing, path)
		}
	}
	if len(existing) > 0 {
		return apierror.New(apierror.ErrExists).With("paths", existing)
	}
	return nil
}

// requirePathExists rejects one missing path under the singular "path" key.
func requirePathExists(ctx context.Context, scope *rods.Scope, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists {
		return apierror.New(apierror.ErrDoesNotExist).With("path", path)
	}
	return nil
}

// requireIsDir rejects a path that is not a collection.
func requireIsDir(ctx context.Context, scope *rods.Scope, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if stat.Type != rods.ObjectTypeDir {
		return apierror.New(apierror.ErrNotAFolder).With("path", path)
	}
	return nil
}

// requireWriteable rejects a path the caller cannot write to.
func requireWriteable(ctx context.Context, scope *rods.Scope, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !rods.Permits(stat.Permission, rods.PermissionWrite) {
		return apierror.New(apierror.ErrNotWriteable).With("path", path)
	}
	return nil
}

// requireOwnsAll rejects a request naming anything the caller does not own.
//
// Ownership rather than write access: moving something out of a collection changes what
// everyone else can see, which is the owner's decision to make.
func requireOwnsAll(ctx context.Context, scope *rods.Scope, user string, requested []string) error {
	stats, err := scope.Stats(ctx, requested).Get(ctx)
	if err != nil {
		return err
	}

	var unowned []string
	for _, path := range requested {
		if stat, ok := stats[path]; !ok || stat.Permission != rods.PermissionOwn {
			unowned = append(unowned, path)
		}
	}
	if len(unowned) > 0 {
		return apierror.New(apierror.ErrNotOwner).With("user", user).With("paths", unowned)
	}
	return nil
}

// requireAllWriteable rejects a request naming anything the caller cannot write to.
func requireAllWriteable(ctx context.Context, scope *rods.Scope, user string, requested []string) error {
	stats, err := scope.Stats(ctx, requested).Get(ctx)
	if err != nil {
		return err
	}

	var refused []string
	for _, path := range requested {
		stat, ok := stats[path]
		if !ok || !rods.Permits(stat.Permission, rods.PermissionWrite) {
			refused = append(refused, path)
		}
	}
	if len(refused) > 0 {
		return apierror.New(apierror.ErrNotWriteable).With("paths", refused)
	}
	return nil
}

// requireAllAreFiles rejects a request naming anything that is not a data object.
func requireAllAreFiles(ctx context.Context, scope *rods.Scope, requested []string) error {
	stats, err := scope.Stats(ctx, requested).Get(ctx)
	if err != nil {
		return err
	}

	var refused []string
	for _, path := range requested {
		if stat, ok := stats[path]; !ok || stat.Type != rods.ObjectTypeFile {
			refused = append(refused, path)
		}
	}
	if len(refused) > 0 {
		// "path" holding a list is a known quirk, kept because callers parse it.
		return apierror.New(apierror.ErrNotAFile).With("path", refused)
	}
	return nil
}

// requireOwns rejects one path the caller does not own.
func requireOwns(ctx context.Context, scope *rods.Scope, user, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if stat.Permission != rods.PermissionOwn {
		return apierror.New(apierror.ErrNotOwner).With("user", user).With("path", path)
	}
	return nil
}

// requireExistsAndOwned checks existence as the service account, then ownership as the caller.
func requireExistsAndOwned(ctx context.Context, admin, scope *rods.Scope, user, path string) error {
	stat, err := admin.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists {
		return apierror.New(apierror.ErrDoesNotExist).With("path", path)
	}
	return requireOwns(ctx, scope, user, path)
}

// requireDoesNotExist rejects one path that is already there.
func requireDoesNotExist(ctx context.Context, scope *rods.Scope, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if stat.Exists {
		return apierror.New(apierror.ErrExists).With("path", path)
	}
	return nil
}

// resolveID turns a data id into a path, reporting an unknown id under "uuid" rather than a
// path the caller might not be allowed to see. The bulk stat endpoints use "ids"; callers read
// both.
func resolveID(ctx context.Context, scope *rods.Scope, id string) (string, error) {
	found, err := scope.PathsForUUIDs(ctx, []string{id}).Get(ctx)
	if err != nil {
		return "", err
	}

	path, ok := found[id]
	if !ok {
		return "", apierror.New(apierror.ErrDoesNotExist).With("uuid", id)
	}
	return path, nil
}

// requireRodsExists rejects a missing path. The requireRods* validators report even a single
// path inside a "paths" list.
func requireRodsExists(ctx context.Context, scope *rods.Scope, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists {
		return apierror.New(apierror.ErrDoesNotExist).With("paths", []string{path})
	}
	return nil
}

// requireRodsIsFile rejects a path that is not a data object.
func requireRodsIsFile(ctx context.Context, scope *rods.Scope, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if stat.Type != rods.ObjectTypeFile {
		return apierror.New(apierror.ErrNotAFile).With("paths", []string{path})
	}
	return nil
}

// requireRodsReadable rejects a path the caller cannot read.
func requireRodsReadable(ctx context.Context, scope *rods.Scope, user, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !rods.Permits(stat.Permission, rods.PermissionRead) {
		return apierror.New(apierror.ErrNotReadable).With("paths", []string{path}).With("user", user)
	}
	return nil
}

// requirePathReadable rejects one path the caller cannot read, under the singular "path" key.
func requirePathReadable(ctx context.Context, scope *rods.Scope, user, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !rods.Permits(stat.Permission, rods.PermissionRead) {
		return apierror.New(apierror.ErrNotReadable).With("path", path).With("user", user)
	}
	return nil
}
