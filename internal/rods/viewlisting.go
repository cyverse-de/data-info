package rods

import (
	"context"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/lazy"
)

// ChildCounts reports how many files and subfolders a collection holds that the user can see.
func (s *Scope) ChildCounts(_ context.Context, path string) *lazy.Value[ChildCounts] {
	path = normalizePath(path)

	return memoize(s, memoKey{kindChildCounts, path}, func() *lazy.Value[ChildCounts] {
		groups := s.groupIDsLocked(s.ctx)

		return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) (ChildCounts, error) {
			ids, err := groups.Get(ctx)
			if err != nil {
				return ChildCounts{}, err
			}
			return s.deps.ICAT.CountChildren(ctx, icat.ChildCountQuery{
				Path:     path,
				User:     s.opts.User,
				Zone:     s.deps.Zone,
				GroupIDs: ids,
			})
		})
	})
}

// PathsForUUIDs resolves data ids to paths. Ids that resolve to nothing are absent from the
// result; whether that is an error is the caller's call.
func (s *Scope) PathsForUUIDs(_ context.Context, uuids []string) *lazy.Value[map[string]string] {
	return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) (map[string]string, error) {
		if len(uuids) == 0 {
			return map[string]string{}, nil
		}

		found, err := s.deps.ICAT.PathsForUUIDs(ctx, uuids)
		if err != nil {
			return nil, err
		}

		out := make(map[string]string, len(found))
		for _, f := range found {
			out[f.UUID] = f.FullPath
		}
		return out, nil
	})
}

// ChildCountsFor resolves child counts for many collections in one query, seeding the
// per-path memo so the accessors a handler runs afterwards cost nothing.
func (s *Scope) ChildCountsFor(_ context.Context, paths []string) *lazy.Value[map[string]ChildCounts] {
	paths = normalizePaths(paths)
	groups := s.groupIDs(s.ctx)

	return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) (map[string]ChildCounts, error) {
		if len(paths) == 0 {
			return map[string]ChildCounts{}, nil
		}

		ids, err := groups.Get(ctx)
		if err != nil {
			return nil, err
		}

		rows, err := s.deps.ICAT.CountChildrenBatch(ctx, icat.BatchChildCountQuery{
			Paths:    paths,
			User:     s.opts.User,
			Zone:     s.deps.Zone,
			GroupIDs: ids,
		})
		if err != nil {
			return nil, err
		}

		out := make(map[string]ChildCounts, len(rows))
		for _, row := range rows {
			out[row.FullPath] = ChildCounts{Files: row.Files, Dirs: row.Dirs}
		}

		s.seedChildCounts(paths, out)
		return out, nil
	})
}

// Listing returns a sorted page of a collection's children. The rows are published, so
// per-entry lookups afterwards answer from this page rather than querying again.
func (s *Scope) Listing(_ context.Context, q ListingQuery) *lazy.Value[[]icat.ListingRow] {
	q.Path = normalizePath(q.Path)
	q.User = s.opts.User
	q.Zone = s.deps.Zone
	if q.InfoTypeAttribute == "" {
		q.InfoTypeAttribute = s.deps.InfoTypeAttribute
	}

	groups := s.groupIDs(s.ctx)

	return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) ([]icat.ListingRow, error) {
		ids, err := groups.Get(ctx)
		if err != nil {
			return nil, err
		}
		q.GroupIDs = ids

		rows, err := s.deps.ICAT.PagedFolder(ctx, q)
		if err != nil {
			return nil, err
		}

		s.publishRows(rows)
		return rows, nil
	})
}

// Subfolders returns every subfolder of a collection, unpaged so none are truncated. It is a
// dedicated query because the paged listing scans every data object first, which is costly
// for a tree view of a folder holding many files.
func (s *Scope) Subfolders(_ context.Context, path string) *lazy.Value[[]icat.ListingRow] {
	path = normalizePath(path)
	groups := s.groupIDs(s.ctx)

	return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) ([]icat.ListingRow, error) {
		ids, err := groups.Get(ctx)
		if err != nil {
			return nil, err
		}

		rows, err := s.deps.ICAT.FoldersInFolder(ctx, icat.ListingQuery{
			Path: path, User: s.opts.User, Zone: s.deps.Zone, GroupIDs: ids,
		})
		if err != nil {
			return nil, err
		}

		s.publishRows(rows)
		return rows, nil
	})
}

// UUIDListing returns a sorted page of the items carrying a set of data ids. The rows are
// published, so decorating each entry afterwards answers from this page.
func (s *Scope) UUIDListing(_ context.Context, q UUIDListingQuery) *lazy.Value[[]icat.ListingRow] {
	q.User = s.opts.User
	q.Zone = s.deps.Zone
	if q.InfoTypeAttribute == "" {
		q.InfoTypeAttribute = s.deps.InfoTypeAttribute
	}

	groups := s.groupIDs(s.ctx)

	return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) ([]icat.ListingRow, error) {
		ids, err := groups.Get(ctx)
		if err != nil {
			return nil, err
		}
		q.GroupIDs = ids

		rows, err := s.deps.ICAT.PagedUUIDs(ctx, q)
		if err != nil {
			return nil, err
		}

		s.publishRows(rows)
		return rows, nil
	})
}

// UUIDCount returns how many of a set of data ids name something the user can see.
func (s *Scope) UUIDCount(_ context.Context, q UUIDListingQuery) *lazy.Value[int64] {
	q.User = s.opts.User
	q.Zone = s.deps.Zone
	if q.InfoTypeAttribute == "" {
		q.InfoTypeAttribute = s.deps.InfoTypeAttribute
	}

	groups := s.groupIDs(s.ctx)

	return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) (int64, error) {
		ids, err := groups.Get(ctx)
		if err != nil {
			return 0, err
		}
		q.GroupIDs = ids

		return s.deps.ICAT.CountUUIDs(ctx, q)
	})
}
