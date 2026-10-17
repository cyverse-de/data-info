package handlers

import (
	"context"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/labstack/echo/v4"
)

// uploadUser reads the caller on the upload routes, where a missing user is reported as an
// unknown one rather than as a schema failure.
func uploadUser(c echo.Context) string {
	return strings.TrimSpace(c.QueryParam("user"))
}

// unknownUploadUser reports a missing caller as a null "users" entry, as the Clojure service
// did; callers read the key, not its contents.
func unknownUploadUser() error {
	return apierror.New(apierror.ErrNotAUser).With("users", []any{nil})
}

// missingUploadPath reports an upload whose destination could not be worked out, either
// because no destination was given or because an id resolved to nothing.
func missingUploadPath() error {
	return apierror.New(apierror.ErrDoesNotExist).With("paths", []any{nil})
}

// requireAbsent rejects a path that already exists.
func requireAbsent(ctx context.Context, scope *rods.Scope, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if stat.Exists {
		return apierror.New(apierror.ErrExists).With("paths", []string{path})
	}
	return nil
}

// requirePresent rejects a path that does not exist, or that the caller cannot see.
func requirePresent(ctx context.Context, scope *rods.Scope, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if !stat.Exists {
		return apierror.New(apierror.ErrDoesNotExist).With("paths", []string{path})
	}
	return nil
}

// requireFile rejects a path that is not a data object.
func requireFile(ctx context.Context, scope *rods.Scope, path string) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if stat.Type != rods.ObjectTypeFile {
		return apierror.New(apierror.ErrNotAFile).With("paths", []string{path})
	}
	return nil
}

// requirePermission rejects a path the caller does not hold at least the given access on.
func requirePermission(
	ctx context.Context,
	scope *rods.Scope,
	user, path string,
	required rods.Permission,
) error {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return err
	}
	if rods.Permits(stat.Permission, required) {
		return nil
	}
	return insufficientPermission(required).With("paths", []string{path}).With("user", user)
}

// checkPathLength rejects a path too long for iRODS before any data is streamed. It sets no
// status, so these codes answer whatever the status table gives them on every route.
func checkPathLength(path string) error {
	switch paths.CheckLength(path) {
	case paths.LengthPath:
		return apierror.New(apierror.ErrBadPathLength).
			With("full-path", path)
	case paths.LengthDir:
		return apierror.New(apierror.ErrBadDirnameLength).
			With("dir-path", paths.Dir(path)).
			With("full-path", path)
	case paths.LengthBasename:
		return apierror.New(apierror.ErrBadBasenameLength).
			With("file-path", paths.Base(path)).
			With("full-path", path)
	}
	return nil
}
