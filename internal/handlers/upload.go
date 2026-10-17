package handlers

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// uploadPartName is the multipart field the file arrives in.
const uploadPartName = "file"

// uploadResponse is what both upload endpoints return.
type uploadResponse struct {
	File service.Stat `json:"file"`
}

// Upload handles POST /data. Bytes go to a hidden temporary object that is renamed into place
// once complete, so an interrupted upload never leaves a truncated file that makes a retry
// fail with ERR_EXISTS.
func (h *Writes) Upload(c echo.Context) error {
	ctx := c.Request().Context()

	// The file name is checked before the caller is identified, the error order callers have
	// always seen.
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
		// Deliberately not a schema failure: a missing dest is reported as a missing (null) path.
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

	return h.respondWithStat(ctx, c, scope, user, path)
}

// Overwrite handles PUT /data/{data-id}, replacing contents in place; staging and renaming
// would give the file a new UUID and access list.
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
		// An unresolved id is reported as a null path; callers parse this shape.
		return missingUploadPath()
	}
	path = strings.TrimRight(path, "/")

	if err := requirePresent(ctx, scope, path); err != nil {
		return err
	}
	if err := requireFile(ctx, scope, path); err != nil {
		return err
	}
	// Read, not write, is checked deliberately; iRODS still refuses the write itself (see
	// docs/deferred-fixes.md).
	if err := requirePermission(ctx, scope, user, path, rods.PermissionRead); err != nil {
		return err
	}
	if err := checkPathLength(path); err != nil {
		return err
	}

	if _, err := scope.WriteFile(ctx, path, part); err != nil {
		return err
	}

	// The old contents' checksum is not the new contents', and iRODS clears it on write, so
	// this is what puts a correct one back rather than leaving the object with none.
	if _, err := scope.Checksum(ctx, path); err != nil {
		return err
	}

	return h.respondWithStat(ctx, c, scope, user, path)
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

	// Recorded before the rename, on the temporary object, so a failure leaves nothing at
	// the destination and the cleanup below still has something to remove. The value
	// travels with the object when it is renamed.
	if _, err := scope.Checksum(ctx, temp); err != nil {
		h.scheduleTempCleanup(user, temp)
		return err
	}

	// Ownership is granted on the temporary object for the same reason. The access list
	// travels with it, and doing it here means the file is never visible under its real
	// name without its owner already set.
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

// respondWithStat answers an upload with the destination's status. infoType is empty because
// info-typer sets it from the AMQP event shortly afterwards.
func (h *Writes) respondWithStat(
	ctx context.Context,
	c echo.Context,
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

// uploadPart returns the file part unread so it streams straight into iRODS; echo's form
// helpers would spool large uploads to disk.
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
