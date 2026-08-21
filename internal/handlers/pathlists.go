package handlers

import (
	"context"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// PathLists serves POST /path-list-creator.
type PathLists struct{ deps Deps }

// NewPathLists builds the path-list handler.
func NewPathLists(deps Deps) *PathLists { return &PathLists{deps: deps} }

// Create handles POST /path-list-creator.
//
// It writes a file listing what a selection contains, which an analysis then runs over. The
// paths given are not themselves in the result: a folder contributes what is inside it and a
// file contributes itself, which is the point of the endpoint.
func (p *PathLists) Create(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	dest := strings.TrimRight(strings.TrimSpace(c.QueryParam("dest")), "/")
	if dest == "" {
		return schemaError("dest must be a non-blank string")
	}

	listType := strings.TrimSpace(c.QueryParam("path-list-info-type"))
	if listType == "" {
		listType = p.deps.PathLists.HTInfoType
	}
	identifier, err := p.fileIdentifier(listType)
	if err != nil {
		return err
	}

	foldersOnly, err := optionalBool(c, "folders-only")
	if err != nil {
		return err
	}
	recursive, err := optionalBool(c, "recursive")
	if err != nil {
		return err
	}

	var body pathsRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	if len(body.Paths) == 0 {
		return schemaError("paths must not be empty")
	}
	requested := trimAll(body.Paths)

	query := service.PathListQuery{
		FileIdentifier: identifier,
		NamePattern:    c.QueryParam("name-pattern"),
		InfoTypes:      infoTypeValues(c),
		FoldersOnly:    foldersOnly,
		Recursive:      recursive,
	}

	scope, err := p.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := p.validate(ctx, scope, user, dest, requested); err != nil {
		return err
	}

	entries, err := p.gather(ctx, scope, user, requested, query)
	if err != nil {
		return err
	}

	contents, err := service.BuildPathList(entries, query)
	if err != nil {
		if err == service.ErrNoMatchingPaths {
			return apierror.New(apierror.ErrNotFound).With("reason", err.Error())
		}
		return err
	}

	if _, err := scope.WriteFile(ctx, dest, strings.NewReader(contents)); err != nil {
		return err
	}
	if _, err := scope.Checksum(ctx, dest); err != nil {
		return err
	}

	// The type is recorded so that the DE offers the file where a path list is wanted. It
	// is written as the service rather than as the caller because it is the service's
	// bookkeeping, not the caller's metadata.
	if err := p.recordType(ctx, dest, listType); err != nil {
		return err
	}

	stat, err := service.StatOf(ctx, scope, user, dest, service.StatOptions{
		Fields:      service.ParseFieldSet("", ""),
		Layout:      p.deps.Layout,
		PermsFilter: p.deps.PermsFilter,
	})
	if err != nil {
		return err
	}

	return writeJSONOK(c, uploadResponse{File: stat})
}

// validate checks the destination and the requested paths before anything is read.
func (p *PathLists) validate(
	ctx context.Context,
	scope *rods.Scope,
	user, dest string,
	requested []string,
) error {
	destDir := paths.Dir(dest)

	if err := requireAllExist(ctx, scope, []string{destDir}); err != nil {
		return err
	}
	if err := requireAllWriteable(ctx, scope, user, []string{destDir}); err != nil {
		return err
	}
	if err := requireNoneExist(ctx, scope, []string{dest}); err != nil {
		return err
	}
	if err := requireAllExist(ctx, scope, requested); err != nil {
		return err
	}

	// A base path is a collection the DE owns -- a user's home, the trash root, the shared
	// collection. Listing one is not what anybody means by selecting it.
	for _, path := range requested {
		if p.deps.Layout.IsBasePath(user, path) {
			return apierror.New(apierror.ErrBadOrMissingField).With("path", path)
		}
	}
	return nil
}

// gather collects every candidate the requested paths contribute.
func (p *PathLists) gather(
	ctx context.Context,
	scope *rods.Scope,
	user string,
	requested []string,
	query service.PathListQuery,
) ([]service.PathListEntry, error) {
	var out []service.PathListEntry

	for _, path := range requested {
		stat, err := scope.Stat(ctx, path).Get(ctx)
		if err != nil {
			return nil, err
		}

		if stat.Type != rods.ObjectTypeDir {
			// A file named directly contributes itself, filtered by info type here
			// because there is no catalog query to do it for us.
			if !service.KeepInfoType(stat.InfoType, query.InfoTypes) {
				continue
			}
			out = append(out, entryOf(stat))
			continue
		}

		below, err := p.walk(ctx, scope, user, path, query)
		if err != nil {
			return nil, err
		}
		out = append(out, below...)
	}

	return out, nil
}

// walk lists a folder's contents, descending when the request asks for it.
func (p *PathLists) walk(
	ctx context.Context,
	scope *rods.Scope,
	user, path string,
	query service.PathListQuery,
) ([]service.PathListEntry, error) {
	rows, err := scope.Listing(ctx, icat.ListingQuery{
		Path:          path,
		User:          user,
		Zone:          p.deps.Layout.Zone,
		InfoTypes:     query.InfoTypes,
		EntityType:    service.ListingEntityType(query.Recursive, query.FoldersOnly),
		SortColumn:    icat.SortColumn("full_path"),
		SortDirection: icat.SortAscending,
		// Every row. A page boundary here would silently truncate the list.
		Limit:             -1,
		InfoTypeAttribute: p.deps.InfoTypeAttribute,
	}).Get(ctx)
	if err != nil {
		return nil, err
	}

	var out []service.PathListEntry
	for _, row := range rows {
		kind := rods.ObjectTypeFile
		if row.IsCollection() {
			kind = rods.ObjectTypeDir
		}

		entry := service.PathListEntry{
			Path:       row.FullPath,
			Label:      row.BaseName,
			Type:       kind,
			InfoType:   row.InfoType.String,
			Permission: rods.Permission(row.Permission()),
		}
		out = append(out, entry)

		if query.Recursive && entry.Type == rods.ObjectTypeDir {
			below, err := p.walk(ctx, scope, user, entry.Path, query)
			if err != nil {
				return nil, err
			}
			out = append(out, below...)
		}
	}

	return out, nil
}

// fileIdentifier reports the header line a path list of the given type opens with.
func (p *PathLists) fileIdentifier(listType string) (string, error) {
	switch listType {
	case p.deps.PathLists.HTInfoType:
		return p.deps.PathLists.HTIdentifier, nil
	case p.deps.PathLists.MultiInputInfoType:
		return p.deps.PathLists.MultiInputIdentifier, nil
	default:
		return "", schemaError("path-list-info-type must be " +
			p.deps.PathLists.HTInfoType + " or " + p.deps.PathLists.MultiInputInfoType)
	}
}

// recordType writes the info type of the generated file.
func (p *PathLists) recordType(ctx context.Context, dest, listType string) error {
	proxy, err := p.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer proxy.Close()

	return proxy.SetAVU(ctx, dest, rods.AVU{
		Attribute: p.deps.InfoTypeAttribute,
		Value:     listType,
		Unit:      service.SystemUnit,
	})
}

// entryOf renders a stat as a path-list candidate.
func entryOf(stat rods.Stat) service.PathListEntry {
	return service.PathListEntry{
		Path:       stat.Path,
		Label:      paths.Base(stat.Path),
		Type:       stat.Type,
		InfoType:   stat.InfoType,
		Permission: stat.Permission,
	}
}

// infoTypeValues reads the info-type filter, which may be repeated or comma-separated.
func infoTypeValues(c echo.Context) []string {
	types, _ := infoTypeFilter(c)
	return types
}
