package handlers

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// uploadPartName is the multipart field the file arrives in. The Clojure route declares it
// as a required parameter of that name, so a request without it never reaches the handler.
const uploadPartName = "file"

// uploadResponse is what both upload endpoints return.
type uploadResponse struct {
	File service.Stat `json:"file"`
}

// Upload handles POST /data.
//
// It creates a new file under the destination directory, named after the uploaded part. The
// bytes go to a hidden temporary object in that same directory first and are published
// under the real name only once the whole stream has landed, so an interrupted upload never
// leaves a truncated file behind -- which would otherwise make the next attempt fail with
// ERR_EXISTS against a file the user never successfully created.
func (h *Writes) Upload(c echo.Context) error {
	ctx := c.Request().Context()

	// The order here is the reference's, and it is not the obvious one: everything below
	// happens inside multipart middleware that runs before the route's own parameters are
	// coerced, so the file's name is checked before the caller is even identified.
	part, err := uploadPart(c)
	if err != nil {
		return err
	}
	defer part.Close() //nolint:errcheck // the request body is discarded either way

	name := part.FileName()
	if name == "" {
		return schemaError("file must be an uploaded file")
	}
	if !goodPathname(name, h.deps.BadChars) {
		return apierror.New(apierror.ErrBadOrMissingField).With("path", name)
	}

	user := uploadUser(c)
	if user == "" {
		return unknownUploadUser()
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, true); err != nil {
		return err
	}

	dest := strings.TrimRight(strings.TrimSpace(c.QueryParam("dest")), "/")
	if dest == "" {
		// Not a schema failure: the reference never coerces this parameter before the
		// upload runs, so a missing destination arrives as a nil path and is reported by
		// the existence check that follows.
		return missingUploadPath()
	}

	path := paths.Join(dest, name)

	if err := requireAbsent(ctx, scope, path); err != nil {
		return err
	}
	if err := requirePresent(ctx, scope, dest); err != nil {
		return err
	}
	if err := requirePermission(ctx, scope, user, dest, rods.PermissionWrite); err != nil {
		return err
	}
	if err := checkPathLength(path); err != nil {
		return err
	}

	if err := h.writeAtomically(ctx, scope, user, path, part); err != nil {
		return err
	}

	return h.respondWithStat(c, ctx, scope, user, path)
}

// Overwrite handles PUT /data/{data-id}.
//
// The contents are replaced in place rather than staged and renamed. That is the reference's
// behavior and it is the right one here: the file already exists, so a failed write leaves
// the previous contents truncated rather than orphaning an object, and staging would break
// the file's identity -- its UUID and access list belong to the object being replaced.
func (h *Writes) Overwrite(c echo.Context) error {
	ctx := c.Request().Context()

	part, err := uploadPart(c)
	if err != nil {
		return err
	}
	defer part.Close() //nolint:errcheck // the request body is discarded either way

	user := uploadUser(c)
	if user == "" {
		return unknownUploadUser()
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, true); err != nil {
		return err
	}

	id := c.Param("data-id")

	found, err := scope.PathsForUUIDs(ctx, []string{id}).Get(ctx)
	if err != nil {
		return err
	}
	path, ok := found[id]
	if !ok {
		// An id that resolves to nothing becomes a nil path, which the existence check
		// then reports -- so the caller is told a path is missing without being told
		// which, because there is none. Reproduced rather than improved on: the shape is
		// what callers parse.
		return missingUploadPath()
	}
	path = strings.TrimRight(path, "/")

	if err := requirePresent(ctx, scope, path); err != nil {
		return err
	}
	if err := requireFile(ctx, scope, path); err != nil {
		return err
	}
	// Read, not write. The reference validates the wrong permission on this route, and the
	// check that matters still happens: iRODS refuses the write itself. See
	// docs/deferred-fixes.md.
	if err := requirePermission(ctx, scope, user, path, rods.PermissionRead); err != nil {
		return err
	}
	if err := checkPathLength(path); err != nil {
		return err
	}

	if _, err := scope.WriteFile(ctx, path, part); err != nil {
		return err
	}

	return h.respondWithStat(c, ctx, scope, user, path)
}

// writeAtomically streams an upload into a temporary object beside its destination and
// renames it into place, cleaning up after itself when either step fails.
func (h *Writes) writeAtomically(
	ctx context.Context,
	scope *rods.Scope,
	user, path string,
	body io.Reader,
) error {
	temp := service.TempUploadPath(path)

	if _, err := scope.WriteFile(ctx, temp, body); err != nil {
		h.scheduleTempCleanup(user, temp)
		return err
	}

	// Ownership is granted on the temporary object rather than after the rename. The
	// access list travels with the object, and doing it here means the file is never
	// visible under its real name without its owner already set.
	if err := scope.SetOwner(ctx, temp, user, false); err != nil {
		h.scheduleTempCleanup(user, temp)
		return err
	}

	if err := scope.Rename(ctx, temp, path); err != nil {
		h.scheduleTempCleanup(user, temp)
		return err
	}

	return nil
}

// respondWithStat answers an upload with the destination's status information.
//
// infoType comes back empty. The Clojure service sniffed the upload as it streamed and wrote
// the AVU itself; file-type detection now belongs to info-typer, which types the object from
// the AMQP event shortly afterwards. Callers that need the type read it back rather than
// taking it from this response.
func (h *Writes) respondWithStat(
	c echo.Context,
	ctx context.Context,
	scope *rods.Scope,
	user, path string,
) error {
	stat, err := service.StatOf(ctx, scope, user, path, service.StatOptions{
		Fields:      service.ParseFieldSet("", ""),
		Layout:      h.deps.Layout,
		PermsFilter: h.deps.PermsFilter,
	})
	if err != nil {
		return err
	}
	return writeJSONOK(c, uploadResponse{File: stat})
}

// uploadPart returns the multipart part carrying the file.
//
// The part is returned unread: its body is the upload itself and is streamed straight into
// iRODS. Nothing is spooled to local disk on the way, which echo's own form helpers would
// do once a request passed their in-memory threshold -- at the sizes this endpoint accepts,
// that is every real upload.
func uploadPart(c echo.Context) (*multipart.Part, error) {
	reader, err := c.Request().MultipartReader()
	if err != nil {
		return nil, schemaError("the request must be a multipart upload")
	}

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, schemaError("file is required")
		}
		if err != nil {
			return nil, schemaError("the multipart body could not be parsed")
		}

		if part.FormName() == uploadPartName {
			return part, nil
		}

		// A part the endpoint does not declare. Drain it so the ones after it can be
		// reached, since parts are readable only in order.
		if _, err := io.Copy(io.Discard, part); err != nil {
			return nil, schemaError("the multipart body could not be read")
		}
		part.Close() //nolint:errcheck // nothing was written and the body is being replaced
	}
}

// tempCleanupSchedule is how long to wait before each attempt at removing an orphaned
// upload. iRODS holds a lock on a replica for a short while after a transfer aborts, so the
// first delete reliably fails; the waits back off to give the lock time to clear. It matches
// the reference's schedule, which was tuned against a real server.
var tempCleanupSchedule = []time.Duration{
	3 * time.Second, 6 * time.Second, 12 * time.Second,
	24 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second,
}

// scheduleTempCleanup removes an orphaned temporary upload object in the background.
//
// It runs detached from the request: the request has already failed and its context is
// about to be cancelled, while the object may not be removable for another minute. Each
// attempt opens its own view, because the failure that orphaned the object usually broke
// the connection the upload was using.
//
// Best effort. A failure here leaves a hidden object behind rather than user-visible
// damage, so it is logged with what a sweep would need and never reported to the caller.
//
// The reference registers this as a tracked async task so an operator can see it. That needs
// the async-task client, which arrives with the rest of the task machinery; until then the
// log line above is the record.
func (h *Writes) scheduleTempCleanup(user, path string) {
	log := h.deps.Logger().WithFields(map[string]any{"path": path, "user": user})

	go func() {
		budget := time.Minute
		for _, wait := range tempCleanupSchedule {
			budget += wait
		}

		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()

		for _, wait := range tempCleanupSchedule {
			select {
			case <-ctx.Done():
				log.Warn("gave up removing a partial upload: the cleanup budget expired; " +
					"the object is hidden but still counts against quota")
				return
			case <-time.After(wait):
			}

			gone, err := h.removeTempObject(ctx, user, path)
			if err != nil {
				log.WithError(err).Debug("could not remove a partial upload yet; " +
					"iRODS usually still holds a lock on the replica this soon after an aborted transfer")
				continue
			}
			if gone {
				log.Info("removed a partial upload left by a failed transfer")
				return
			}
		}

		log.Warn("gave up removing a partial upload after every attempt failed; " +
			"the replica lock probably never cleared, and the object needs removing by hand")
	}()
}

// removeTempObject deletes one orphaned upload object, reporting whether it is gone.
func (h *Writes) removeTempObject(ctx context.Context, user, path string) (bool, error) {
	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return false, err
	}
	defer scope.Close()

	present, err := scope.FileExists(ctx, path)
	if err != nil {
		return false, err
	}
	if !present {
		return true, nil
	}

	// A hard delete first: a partial upload has no value to recover, and leaving it in the
	// trash would only move the problem. Falling back to the trash keeps the object out of
	// the way when the replica cannot be removed outright.
	if err := scope.DeleteFile(ctx, path, true); err != nil {
		if err := scope.DeleteFile(ctx, path, false); err != nil {
			return false, err
		}
	}

	return scope.FileExists(ctx, path)
}

// uploadUser reads the caller identity on the upload routes.
//
// A missing one is not the schema failure it is everywhere else. The reference validates
// the caller inside multipart middleware, which runs before the route's parameters are
// coerced, so an absent user reaches the existence check as nil rather than being rejected.
func uploadUser(c echo.Context) string {
	return strings.TrimSpace(c.QueryParam("user"))
}

// unknownUploadUser reports a caller the upload routes could not identify at all.
//
// The null in the list is the point: the reference puts the nil username straight into the
// error, and callers read the key rather than its contents.
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
	if permits(stat.Permission, required) {
		return nil
	}
	return insufficientPermission(required).With("paths", []string{path}).With("user", user)
}

// checkPathLength rejects a path iRODS would refuse to name.
//
// The reference checks this inside the write, so an over-long name is reported only after
// the bytes have been streamed. Checking first reaches the same answer without moving the
// data, and the codes and their fields are unchanged.
func checkPathLength(path string) error {
	switch paths.CheckLength(path) {
	case paths.LengthPath:
		return apierror.New(apierror.ErrBadPathLength).
			WithStatus(http.StatusInternalServerError).
			With("full-path", path)
	case paths.LengthDir:
		return apierror.New(apierror.ErrBadDirnameLength).
			WithStatus(http.StatusInternalServerError).
			With("dir-path", paths.Dir(path)).
			With("full-path", path)
	case paths.LengthBasename:
		return apierror.New(apierror.ErrBadBasenameLength).
			WithStatus(http.StatusInternalServerError).
			With("file-path", paths.Base(path)).
			With("full-path", path)
	}
	return nil
}
