package handlers

import (
	"context"
	"net/url"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// shareRequest is the body of POST /sharer.
type shareRequest struct {
	Sharing []shareeEntry `json:"sharing"`
}

type shareeEntry struct {
	User  string           `json:"user"`
	Paths []sharePathEntry `json:"paths"`
}

type sharePathEntry struct {
	Path       string `json:"path"`
	Permission string `json:"permission"`
}

// unshareRequest is the body of POST /unsharer.
type unshareRequest struct {
	Unshare []unshareeEntry `json:"unshare"`
}

type unshareeEntry struct {
	User  string   `json:"user"`
	Paths []string `json:"paths"`
}

// shareOutcome is one path's result. A skip is reported as a success carrying the reason,
// because nothing went wrong -- there was simply nothing to do.
type shareOutcome struct {
	Path       string `json:"path"`
	Permission string `json:"permission,omitempty"`
	Success    bool   `json:"success"`
	Reason     string `json:"reason,omitempty"`
	Error      any    `json:"error,omitempty"`
}

// Share handles POST /sharer.
//
// A failure on one path fails that path and nothing else. Sharing is a bulk operation the DE
// runs over a selection, and refusing the whole request because one item in it is not owned
// by the caller would make the endpoint unusable for its actual purpose.
func (h *Writes) Share(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	var body shareRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}
	if err := h.deps.CheckPathCount(countSharePaths(body.Sharing)); err != nil {
		return err
	}

	results := make([]map[string]any, 0, len(body.Sharing))
	for _, entry := range body.Sharing {
		outcomes, err := h.shareWith(ctx, scope, user, entry)
		if err != nil {
			return err
		}
		results = append(results, map[string]any{"user": entry.User, "sharing": outcomes})
	}

	return writeJSONOK(c, map[string]any{"sharing": results})
}

// shareWith applies one sharee's entry.
func (h *Writes) shareWith(
	ctx context.Context,
	scope *rods.Scope,
	user string,
	entry shareeEntry,
) ([]shareOutcome, error) {
	outcomes := make([]shareOutcome, 0, len(entry.Paths))

	// Checked once for the entry rather than once per path: it is a lookup, and every path
	// in the entry would fail the same way.
	known, err := scope.UserExists(ctx, entry.User).Get(ctx)
	if err != nil {
		return nil, err
	}

	for _, item := range entry.Paths {
		path := strings.TrimRight(item.Path, "/")
		outcome := shareOutcome{Path: path, Permission: item.Permission}

		if !known {
			outcome.Error = envelopeOf(apierror.New(apierror.ErrNotAUser).With("user", entry.User))
			outcomes = append(outcomes, outcome)
			continue
		}

		reason, err := h.applyShare(ctx, scope, user, entry.User, path, item.Permission)
		switch {
		case err != nil:
			outcome.Error = envelopeOf(err)
		default:
			outcome.Success = true
			outcome.Reason = reason
		}
		outcomes = append(outcomes, outcome)
	}

	return outcomes, nil
}

// applyShare validates and performs one share.
func (h *Writes) applyShare(
	ctx context.Context,
	scope *rods.Scope,
	user, sharee, path, permission string,
) (string, error) {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return "", err
	}
	if !stat.Exists {
		return "", apierror.New(apierror.ErrDoesNotExist).With("path", path)
	}
	if stat.Permission != rods.PermissionOwn {
		return "", apierror.New(apierror.ErrNotOwner).With("user", user).With("path", path)
	}

	return service.Share(ctx, scope, h.shareRequest(user, sharee, path, rods.Permission(permission)))
}

// Unshare handles POST /unsharer.
func (h *Writes) Unshare(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	var body unshareRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	var total int
	for _, entry := range body.Unshare {
		total += len(entry.Paths)
	}
	if err := h.deps.CheckPathCount(total); err != nil {
		return err
	}

	results := make([]map[string]any, 0, len(body.Unshare))
	for _, entry := range body.Unshare {
		known, err := scope.UserExists(ctx, entry.User).Get(ctx)
		if err != nil {
			return err
		}

		outcomes := make([]shareOutcome, 0, len(entry.Paths))
		for _, raw := range entry.Paths {
			path := strings.TrimRight(raw, "/")
			outcome := shareOutcome{Path: path}

			if !known {
				outcome.Error = envelopeOf(apierror.New(apierror.ErrNotAUser).With("user", entry.User))
				outcomes = append(outcomes, outcome)
				continue
			}

			reason, err := h.applyUnshare(ctx, scope, user, entry.User, path)
			if err != nil {
				outcome.Error = envelopeOf(err)
			} else {
				outcome.Success = true
				outcome.Reason = reason
			}
			outcomes = append(outcomes, outcome)
		}

		results = append(results, map[string]any{"user": entry.User, "unshare": outcomes})
	}

	return writeJSONOK(c, map[string]any{"unshare": results})
}

// applyUnshare validates and performs one unshare.
func (h *Writes) applyUnshare(
	ctx context.Context,
	scope *rods.Scope,
	user, sharee, path string,
) (string, error) {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return "", err
	}
	if !stat.Exists {
		return "", apierror.New(apierror.ErrDoesNotExist).With("path", path)
	}
	if stat.Permission != rods.PermissionOwn {
		return "", apierror.New(apierror.ErrNotOwner).With("user", user).With("path", path)
	}

	return service.Unshare(ctx, scope, h.shareRequest(user, sharee, path, rods.PermissionNone))
}

// AddPermission handles PUT /data/{data-id}/permissions/{share-with}/{permission}.
func (h *Writes) AddPermission(c echo.Context) error {
	return h.changePermission(c, func(ctx context.Context, scope *rods.Scope, user, other, path string) error {
		_, err := service.Share(ctx, scope,
			h.shareRequest(user, other, path, rods.Permission(c.Param("permission"))))
		return err
	}, c.Param("share-with"))
}

// RemovePermission handles DELETE /data/{data-id}/permissions/{unshare-with}.
func (h *Writes) RemovePermission(c echo.Context) error {
	return h.changePermission(c, func(ctx context.Context, scope *rods.Scope, user, other, path string) error {
		_, err := service.Unshare(ctx, scope, h.shareRequest(user, other, path, rods.PermissionNone))
		return err
	}, c.Param("unshare-with"))
}

// changePermission serves the two by-id routes, which differ only in what they do once the
// path and the users are resolved -- and answer with the same thing afterwards.
func (h *Writes) changePermission(
	c echo.Context,
	apply func(ctx context.Context, scope *rods.Scope, user, other, path string) error,
	other string,
) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}
	if strings.TrimSpace(other) == "" {
		return schemaError("the user to change permissions for must be a non-blank string")
	}

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	path, err := resolveID(ctx, scope, c.Param("data-id"))
	if err != nil {
		return err
	}
	path = strings.TrimRight(path, "/")

	if err := requireOwns(ctx, scope, user, path); err != nil {
		return err
	}

	known, err := scope.UserExists(ctx, other).Get(ctx)
	if err != nil {
		return err
	}
	if !known {
		return apierror.New(apierror.ErrNotAUser).With("user", other)
	}

	if err := apply(ctx, scope, user, other, path); err != nil {
		return err
	}

	// The whole access list afterwards, not just what changed: the DE renders it, and
	// asking again would race whatever else is sharing the same path.
	acl, err := scope.ACL(ctx, path).Get(ctx)
	if err != nil {
		return err
	}

	return writeJSONOK(c, map[string]any{"user-permissions": h.deps.visiblePermissions(acl, user)})
}

// Anonymize handles POST /anonymizer: read access for the anonymous account, plus the URLs
// that account can be reached through.
func (h *Writes) Anonymize(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	var body pathsRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	if err := h.deps.CheckPathCount(len(body.Paths)); err != nil {
		return err
	}

	requested := trimAll(body.Paths)

	scope, err := h.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}
	if err := requireAllExist(ctx, scope, requested); err != nil {
		return err
	}
	if err := requireAllAreFiles(ctx, scope, requested); err != nil {
		return err
	}
	if err := requireOwnsAll(ctx, scope, user, requested); err != nil {
		return err
	}

	urls := make(map[string]string, len(requested))
	for _, path := range requested {
		req := h.shareRequest(user, h.deps.AnonUser, path, rods.PermissionRead)
		if _, err := service.Share(ctx, scope, req); err != nil {
			return err
		}
		urls[path] = h.anonURL(path)
	}

	return writeJSONOK(c, map[string]any{"user": user, "paths": urls})
}

// anonURL builds the address the anonymous file service serves a path at.
//
// The longest configured prefix wins, so that a more specific mapping is not shadowed by a
// more general one that happens to be checked first. A path no mapping covers gets an empty
// URL rather than a wrong one.
func (h *Writes) anonURL(path string) string {
	var best string
	for prefix := range h.deps.AnonMappings {
		if len(path) > len(prefix) && strings.HasPrefix(path, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return ""
	}

	mapped := h.deps.AnonMappings[best] + path[len(best):]

	// Each segment is escaped on its own so the separators survive, and a space becomes
	// %20 rather than a plus -- this ends up in a URL a person is given, not in a form.
	segments := strings.Split(mapped, "/")
	for i, segment := range segments {
		segments[i] = strings.ReplaceAll(url.QueryEscape(segment), "+", "%20")
	}

	return strings.TrimRight(h.deps.AnonBaseURL, "/") + "/" + strings.Join(segments, "/")
}

// shareRequest fills in the parts of a share that come from configuration rather than from
// the caller.
func (h *Writes) shareRequest(user, other, path string, level rods.Permission) service.ShareRequest {
	return service.ShareRequest{
		Owner:      user,
		Sharee:     other,
		Path:       path,
		Permission: level,
		AdminUsers: h.deps.AdminUsers,
		ProxyUser:  h.deps.ProxyUser,
		Layout:     h.deps.Layout,
	}
}

// countSharePaths totals the paths across every entry of a share request.
func countSharePaths(entries []shareeEntry) int {
	var total int
	for _, entry := range entries {
		total += len(entry.Paths)
	}
	return total
}

// envelopeOf renders an error the way it would appear as a whole response body, so that a
// per-path failure carries the same shape a caller already knows how to read.
func envelopeOf(err error) any { return apierror.Envelope(err) }
