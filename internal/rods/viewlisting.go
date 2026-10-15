package rods

import (
	"context"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/lazy"
)

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

// PathsForUUIDs resolves data ids to paths.
//
// Ids that resolve to nothing are simply absent from the result; the caller decides whether
// that is an error, since some endpoints are asked to ignore missing entries.
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

// Listing returns a sorted page of a collection's children.
//
// The rows are published into the scope, so a handler that lists a folder and then asks
// about individual entries answers from the listing rather than querying again -- which is
// what makes formatting a page cost one query rather than one per entry.
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

// Subfolders returns every subfolder of a collection.
//
// This is a separate query rather than a filtered listing. The paged listing builds its
// intermediate results over every data object in the collection before deciding what to keep,
// so using it for a tree view would make a hot endpoint far more expensive on a folder
// holding many files. It is also unpaged, as the Clojure service is, so a folder with many
// subfolders is not silently truncated.
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

// UUIDListing returns a sorted page of the items carrying a set of data ids.
//
// The rows are published into the scope, so decorating each entry afterwards -- with a
// label, a share count, whatever the caller asked for -- answers from this page rather than
// querying the catalog again per entry.
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
