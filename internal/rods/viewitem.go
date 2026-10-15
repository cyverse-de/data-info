package rods

import (
	"context"
	"errors"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/lazy"
)

// item resolves one path's catalog row, answering from a published row when one is already
// there. Every scalar accessor funnels through this, so asking for six facts about a path
// costs one query.
func (s *Scope) item(_ context.Context, path string) *lazy.Value[icat.Row] {
	path = normalizePath(path)

	return memoize(s, memoKey{kindItem, path}, func() *lazy.Value[icat.Row] {
		// A batched load may already have answered this. Checking without waiting is the
		// whole point: a listing that is still running must not stall a stat. The locked
		// variant is required here -- build runs with the scope's lock held.
		if row, ok := s.peekRowLocked(path); ok {
			return lazy.Resolved(row)
		}

		groups := s.groupIDsLocked(s.ctx)

		return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) (icat.Row, error) {
			if row, ok := s.peekRow(path); ok {
				return row, nil
			}

			ids, err := groups.Get(ctx)
			if err != nil {
				return icat.Row{}, err
			}

			row, err := icat.GetItem(ctx, s.deps.ICAT, icat.ItemQuery{
				Paths:             []string{path},
				User:              s.opts.User,
				Zone:              s.deps.Zone,
				GroupIDs:          ids,
				InfoTypeAttribute: s.deps.InfoTypeAttribute,
			})
			if err != nil {
				return icat.Row{}, err
			}

			s.publishRows([]icat.Row{row})
			return row, nil
		})
	})
}

// Stat reports what is at a path. A path that is absent, or that the user cannot see,
// resolves to a Stat with Exists false rather than an error: callers routinely ask about
// paths that may not be there, and the Clojure accessors behaved the same way.
func (s *Scope) Stat(ctx context.Context, path string) *lazy.Value[Stat] {
	path = normalizePath(path)
	item := s.item(ctx, path)

	return lazy.Go(s.ctx, nil, func(ctx context.Context) (Stat, error) {
		row, err := item.Get(ctx)
		if err != nil {
			if errors.Is(err, icat.ErrNoSuchItem) {
				return Stat{Path: path, Type: ObjectTypeNone}, nil
			}
			return Stat{}, err
		}
		return statOf(row), nil
	})
}

// Stats resolves many paths in one query.
func (s *Scope) Stats(_ context.Context, paths []string) *lazy.Value[map[string]Stat] {
	paths = normalizePaths(paths)

	// Resolved before the catalog slot is taken, not inside it. A goroutine holding a slot
	// must never wait on a value that needs one.
	groups := s.groupIDs(s.ctx)

	return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) (map[string]Stat, error) {
		if len(paths) == 0 {
			return map[string]Stat{}, nil
		}

		ids, err := groups.Get(ctx)
		if err != nil {
			return nil, err
		}

		rows, err := s.deps.ICAT.GetItems(ctx, icat.ItemQuery{
			Paths:             paths,
			User:              s.opts.User,
			Zone:              s.deps.Zone,
			GroupIDs:          ids,
			InfoTypeAttribute: s.deps.InfoTypeAttribute,
		})
		if err != nil {
			return nil, err
		}

		// Publishing before returning means any per-path accessor that runs afterwards
		// answers from these rows instead of querying again.
		s.publishRows(rows)

		out := make(map[string]Stat, len(rows))
		for _, row := range rows {
			out[row.FullPath] = statOf(row)
		}

		// Record the absences too. Without this, a bulk request over paths that mostly do
		// not exist -- which is the common shape, since callers ask about paths they are
		// not sure about -- would send every follow-up lookup back to the catalog one at a
		// time, reintroducing exactly the N+1 the batch exists to avoid.
		s.recordAbsent(paths, out)

		return out, nil
	})
}

// ObjectType reports what kind of thing is at a path.
func (s *Scope) ObjectType(ctx context.Context, path string) *lazy.Value[ObjectType] {
	stat := s.Stat(ctx, path)

	return lazy.Go(s.ctx, nil, func(ctx context.Context) (ObjectType, error) {
		st, err := stat.Get(ctx)
		if err != nil {
			return ObjectTypeNone, err
		}
		return st.Type, nil
	})
}

// Permission reports the requesting user's access to a path.
func (s *Scope) Permission(ctx context.Context, path string) *lazy.Value[Permission] {
	stat := s.Stat(ctx, path)

	return lazy.Go(s.ctx, nil, func(ctx context.Context) (Permission, error) {
		st, err := stat.Get(ctx)
		if err != nil {
			return icat.PermissionNone, err
		}
		return st.Permission, nil
	})
}

// UUID reports a path's ipc_UUID.
func (s *Scope) UUID(ctx context.Context, path string) *lazy.Value[string] {
	stat := s.Stat(ctx, path)

	return lazy.Go(s.ctx, nil, func(ctx context.Context) (string, error) {
		st, err := stat.Get(ctx)
		if err != nil {
			return "", err
		}
		return st.UUID, nil
	})
}
