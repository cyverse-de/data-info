package rods

import (
	"context"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/irodsclient"
	"github.com/cyverse-de/data-info/internal/lazy"
)

// ACL reports every user's access to a path. The query is not scoped to the requesting
// user, so a handler must confirm the user may see the path before calling it.
func (s *Scope) ACL(_ context.Context, path string) *lazy.Value[[]ACLEntry] {
	path = normalizePath(path)

	return memoize(s, memoKey{kindACL, path}, func() *lazy.Value[[]ACLEntry] {
		return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) ([]ACLEntry, error) {
			perms, err := s.deps.ICAT.PermsForItems(ctx, []string{path})
			if err != nil {
				return nil, err
			}
			return aclOf(perms), nil
		})
	})
}

// ACLs resolves access lists for many paths in one query.
func (s *Scope) ACLs(_ context.Context, paths []string) *lazy.Value[map[string][]ACLEntry] {
	paths = normalizePaths(paths)

	return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) (map[string][]ACLEntry, error) {
		if len(paths) == 0 {
			return map[string][]ACLEntry{}, nil
		}

		perms, err := s.deps.ICAT.PermsForItems(ctx, paths)
		if err != nil {
			return nil, err
		}

		byPath := make(map[string][]icat.Perm, len(paths))
		for _, p := range perms {
			byPath[p.FullPath] = append(byPath[p.FullPath], p)
		}

		out := make(map[string][]ACLEntry, len(byPath))
		for path, ps := range byPath {
			out[path] = aclOf(ps)
		}

		s.seedACLs(paths, out)

		return out, nil
	})
}

// AVUs reports a path's metadata, over the protocol rather than the catalog so that iRODS
// applies its own visibility rules.
func (s *Scope) AVUs(_ context.Context, path string) *lazy.Value[[]AVU] {
	path = normalizePath(path)

	return memoize(s, memoKey{kindAVUs, path}, func() *lazy.Value[[]AVU] {
		s.protocol.Add(1)
		// The semaphore is acquired here rather than by lazy.Go so that Done still runs when
		// the acquire fails, as it does for a lookup still queued when Close cancels the scope.
		return lazy.Go(s.ctx, nil, func(ctx context.Context) ([]AVU, error) {
			defer s.protocol.Done()

			if err := s.protocolSem.Acquire(ctx, 1); err != nil {
				return nil, err
			}
			defer s.protocolSem.Release(1)

			sess, err := s.session(ctx)
			if err != nil {
				return nil, err
			}
			return irodsclient.ListAVUs(ctx, sess, path)
		})
	})
}

// UserExists reports whether an iRODS account exists. Groups do not count. It asks the
// catalog because every request validates its caller, and a protocol call would spend a
// scarce connection each time.
func (s *Scope) UserExists(_ context.Context, user string) *lazy.Value[bool] {
	return memoize(s, memoKey{kindUserExists, user}, func() *lazy.Value[bool] {
		return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) (bool, error) {
			kind, err := s.deps.ICAT.LookupUser(ctx, user, s.deps.Zone)
			if err != nil {
				return false, err
			}
			// A group name must not satisfy a user check.
			return kind != icat.UserKindNone && kind != icat.UserKindGroup, nil
		})
	})
}

// UserKind reports what an account name refers to, which decides whether its holder may
// administer groups. Answered from the catalog, like UserExists.
func (s *Scope) UserKind(_ context.Context, user string) *lazy.Value[icat.UserKind] {
	return memoize(s, memoKey{kindUserKind, user}, func() *lazy.Value[icat.UserKind] {
		return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) (icat.UserKind, error) {
			return s.deps.ICAT.LookupUser(ctx, user, s.deps.Zone)
		})
	})
}

// UserGroups reports a user's groups as bare names, not zone-qualified; the endpoint
// appends the zone. Answered from the catalog, like UserExists.
func (s *Scope) UserGroups(_ context.Context, user string) *lazy.Value[[]string] {
	return memoize(s, memoKey{kindUserGroups, user}, func() *lazy.Value[[]string] {
		return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) ([]string, error) {
			return s.deps.ICAT.UserGroupNames(ctx, user, s.deps.Zone)
		})
	})
}
