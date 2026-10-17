package service

import (
	"context"

	"github.com/cyverse-de/data-info/internal/lazy"
	"github.com/cyverse-de/data-info/internal/mediatype"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
)

// Stat is one path's status information as the API reports it.
//
// The JSON keys are a wire contract, spelling inconsistencies included: most are hyphenated
// but infoType is camel case. Every field is omitempty because a filtered request emits only
// what it asked for, and a field the caller excluded must be absent rather than null.
type Stat struct {
	ID           string  `json:"id,omitempty"`
	Path         string  `json:"path,omitempty"`
	Type         string  `json:"type,omitempty"`
	Label        string  `json:"label,omitempty"`
	DateCreated  *int64  `json:"date-created,omitempty"`
	DateModified *int64  `json:"date-modified,omitempty"`
	Permission   string  `json:"permission,omitempty"`
	ShareCount   *int64  `json:"share-count,omitempty"`
	FileCount    *int64  `json:"file-count,omitempty"`
	DirCount     *int64  `json:"dir-count,omitempty"`
	FileSize     *int64  `json:"file-size,omitempty"`
	ContentType  string  `json:"content-type,omitempty"`
	InfoType     *string `json:"infoType,omitempty"`
	MD5          *string `json:"md5,omitempty"`
}

// StatOptions shape one stat.
type StatOptions struct {
	// Fields selects what to emit.
	Fields FieldSet

	// Layout locates the special collections, for the label.
	Layout paths.Layout

	// PermsFilter names accounts left out of a share count: the requesting user, the
	// service's own proxy account, and whatever else a deployment lists.
	PermsFilter map[string]bool
}

// StatOf builds the status information for one path, computing only the requested fields,
// since child counts and share counts each cost a query.
func StatOf(ctx context.Context, view rods.View, user, path string, opts StatOptions) (Stat, error) {
	fields := opts.Fields

	base, err := view.Stat(ctx, path).Get(ctx)
	if err != nil {
		return Stat{}, err
	}

	isDir := base.Type == rods.ObjectTypeDir
	out := Stat{}

	if fields.Has(FieldPath) {
		out.Path = base.Path
	}
	if fields.Has(FieldID) {
		out.ID = base.UUID
	}
	if fields.Has(FieldLabel) {
		out.Label = opts.Layout.Label(user, base.Path)
	}
	if fields.Has(FieldType) {
		out.Type = string(base.Type)
	}
	if fields.Has(FieldDateCreated) {
		created := base.CreatedMS
		out.DateCreated = &created
	}
	if fields.Has(FieldDateModified) {
		modified := base.ModifiedMS
		out.DateModified = &modified
	}
	if fields.Has(FieldPermission) {
		out.Permission = string(base.Permission)
	}

	if !isDir {
		if fields.Has(FieldFileSize) {
			size := base.Size
			out.FileSize = &size
		}
		if fields.Has(FieldMD5) {
			out.MD5 = base.Checksum
		}
		if fields.Has(FieldContentType) {
			out.ContentType = mediatype.OfName(base.Path)
		}
		if fields.Has(FieldInfoType) {
			infoType := FileInfoType(base.InfoType)
			out.InfoType = &infoType
		}
	}

	// Share counts are reported only to owners; others get no field rather than zero.
	if fields.Has(FieldShareCount) && base.Permission == rods.PermissionOwn {
		acl, err := view.ACL(ctx, base.Path).Get(ctx)
		if err != nil {
			return Stat{}, err
		}
		count := countShares(acl, user, opts.PermsFilter)
		out.ShareCount = &count
	}

	if isDir && fields.HasAny(FieldFileCount, FieldDirCount) {
		counts, err := view.ChildCounts(ctx, base.Path).Get(ctx)
		if err != nil {
			return Stat{}, err
		}
		if fields.Has(FieldFileCount) {
			files := counts.Files
			out.FileCount = &files
		}
		if fields.Has(FieldDirCount) {
			dirs := counts.Dirs
			out.DirCount = &dirs
		}
	}

	return out, nil
}

// countShares counts who a path is shared with, excluding the owner themselves and the
// accounts a deployment filters out.
func countShares(acl []rods.ACLEntry, user string, filter map[string]bool) int64 {
	var count int64
	for _, entry := range acl {
		if entry.User == user || filter[entry.User] {
			continue
		}
		count++
	}
	return count
}

// StatsOf builds status information for many paths, resolving them in one catalog query.
// Bulk handlers must use this rather than calling StatOf in a loop.
func StatsOf(ctx context.Context, view rods.View, user string, paths []string, opts StatOptions) (map[string]Stat, error) {
	if len(paths) == 0 {
		return map[string]Stat{}, nil
	}

	// One query for the rows. The per-path work below then answers from what this
	// published rather than querying again.
	if _, err := view.Stats(ctx, paths).Get(ctx); err != nil {
		return nil, err
	}

	return StatsOfLoaded(ctx, view, user, paths, opts)
}

// StatsOfLoaded is StatsOf for paths the view has already resolved, such as a listing
// page's rows.
func StatsOfLoaded(ctx context.Context, view rods.View, user string, paths []string, opts StatOptions) (map[string]Stat, error) {
	if len(paths) == 0 {
		return map[string]Stat{}, nil
	}

	// Prefetch requested access lists and child counts in one query each, so StatOf below
	// does not query per path.
	if opts.Fields.Has(FieldShareCount) {
		if _, err := view.ACLs(ctx, paths).Get(ctx); err != nil {
			return nil, err
		}
	}
	if opts.Fields.HasAny(FieldFileCount, FieldDirCount) {
		if _, err := view.ChildCountsFor(ctx, paths).Get(ctx); err != nil {
			return nil, err
		}
	}

	// Dispatch every path, then await. Each has already started, so this resolves them
	// concurrently rather than one after another.
	values := make(map[string]*lazy.Value[Stat], len(paths))
	for _, p := range paths {
		values[p] = lazy.Go(ctx, nil, func(ctx context.Context) (Stat, error) {
			return StatOf(ctx, view, user, p, opts)
		})
	}

	out := make(map[string]Stat, len(paths))
	for p, value := range values {
		stat, err := value.Get(ctx)
		if err != nil {
			return nil, err
		}
		out[p] = stat
	}
	return out, nil
}
