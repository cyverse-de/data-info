package handlers

import (
	"context"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/rods"
)

// The validators below reproduce data_info.util.validators -- the jargon family. They report
// a single path under "path" and a list under "paths", and they are not interchangeable with
// the clj-irods family used elsewhere, which always reports a list and usually names the user.
//
// The two disagree about the envelope for the same condition, they are both live on different
// endpoints, and callers read the keys. Which family an endpoint uses is decided by which one
// the reference reached for there, so it is recorded at each call site rather than chosen.

// requireAllExist rejects a request naming anything that is not there, listing everything
// missing rather than stopping at the first.
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

// requirePathExists rejects one path that is not there, reporting it under the singular key.
//
// Its plural sibling above reports a list. Which of the two an endpoint uses is decided by
// which validator the reference reached for there, and callers read the key.
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
		// The plural key with no user, which is what this validator attaches -- unlike its
		// singular sibling above, which attaches neither.
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
		// The singular key with a list in it, which is what this validator attaches. It
		// reads like a mistake and it is one, but callers parse it.
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

// resolveID turns a data id into a path, reporting the id rather than the path when it names
// nothing -- echoing a path the caller cannot see would disclose it.
//
// The key is the singular "uuid", which is what uuids/path-for-uuid attaches. The bulk stat
// endpoints report the same condition under "ids", because they reach it through a different
// validator. Both are live and callers read the keys.
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

// The three below are the clj-irods family for an endpoint that concerns a single path.
// They report it inside a list, because that validator accepts either one path or many and
// always attaches a list -- so ERR_NOT_A_FILE arrives here under "paths" where the jargon
// sibling above puts it under "path". The Rods in the name is the family, not the shape.
//
// Each reads through the scope's per-path accessor, which answers from a batch that has
// already run, so an endpoint applying all three to one path pays one catalog query rather
// than three.

// requireRodsExists rejects a path that is not there.
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

// requirePathReadable rejects one path the caller cannot read, under the jargon family's
// singular keys.
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
