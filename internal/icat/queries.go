package icat

import (
	"context"
	_ "embed"
	"fmt"
	"path"
	"strings"

	"github.com/lib/pq"
)

// The catalog queries. They live in .sql files so that they diff cleanly against the
// Clojure originals they are ported from.
var (
	//go:embed sql/user_group_ids.sql
	sqlUserGroupIDs string

	//go:embed sql/get_items.sql
	sqlGetItems string

	//go:embed sql/perms_for_items.sql
	sqlPermsForItems string
)

// ItemQuery selects catalog rows for a set of paths on behalf of a user.
type ItemQuery struct {
	// Paths are absolute iRODS paths.
	Paths []string

	// User and Zone identify whose permissions filter the result.
	User string
	Zone string

	// GroupIDs, when set, is used instead of looking the user's groups up again. Callers
	// resolving several things for one user should fetch it once and pass it: every
	// permission-filtered query needs it.
	GroupIDs []int64
}

// UserGroupIDs returns the group ids a user belongs to.
func (s *PGStore) UserGroupIDs(ctx context.Context, user, zone string) ([]int64, error) {
	return userGroupIDs(ctx, s.queryer(), user, zone)
}

// UserGroupIDs returns the group ids a user belongs to.
func (t *pgTx) UserGroupIDs(ctx context.Context, user, zone string) ([]int64, error) {
	return userGroupIDs(ctx, t.queryer(), user, zone)
}

func userGroupIDs(ctx context.Context, q queryer, user, zone string) ([]int64, error) {
	if user == "" {
		return nil, fmt.Errorf("icat: a username is required")
	}

	var ids []int64
	if err := q.SelectContext(ctx, &ids, sqlUserGroupIDs, user, zone); err != nil {
		return nil, fmt.Errorf("icat: looking up group ids for %q: %w", user, err)
	}
	return ids, nil
}

// GetItems returns catalog rows for the given paths.
func (s *PGStore) GetItems(ctx context.Context, q ItemQuery) ([]Row, error) {
	return getItems(ctx, s.queryer(), q)
}

// GetItems returns catalog rows for the given paths.
func (t *pgTx) GetItems(ctx context.Context, q ItemQuery) ([]Row, error) {
	return getItems(ctx, t.queryer(), q)
}

func getItems(ctx context.Context, qr queryer, q ItemQuery) ([]Row, error) {
	if len(q.Paths) == 0 {
		return nil, nil
	}
	if q.User == "" && len(q.GroupIDs) == 0 {
		return nil, fmt.Errorf("icat: a user or a set of group ids is required")
	}

	dirnames, basenames := splitPaths(q.Paths)

	var groupIDs any
	if len(q.GroupIDs) > 0 {
		groupIDs = pq.Array(q.GroupIDs)
	}

	var rows []Row
	err := qr.SelectContext(ctx, &rows, sqlGetItems,
		pq.Array(dirnames), pq.Array(basenames), q.User, q.Zone, groupIDs)
	if err != nil {
		return nil, fmt.Errorf("icat: getting %d item(s): %w", len(q.Paths), err)
	}
	return rows, nil
}

// GetItem returns the catalog row for one path, or ErrNoSuchItem.
//
// It exists so callers with a single path do not have to unpack a slice, and so that
// "absent" is an error rather than an empty result they might forget to check. Callers with
// more than one path should use GetItems: the whole reason this query is batched is that a
// round trip per path is what made the protocol too slow for the bulk endpoints.
func GetItem(ctx context.Context, r Reader, q ItemQuery) (Row, error) {
	if len(q.Paths) != 1 {
		return Row{}, fmt.Errorf("icat: GetItem takes exactly one path, got %d", len(q.Paths))
	}

	rows, err := r.GetItems(ctx, q)
	if err != nil {
		return Row{}, err
	}
	if len(rows) == 0 {
		return Row{}, ErrNoSuchItem
	}
	return rows[0], nil
}

// PermsForItems returns every user's access to each of the given paths.
func (s *PGStore) PermsForItems(ctx context.Context, paths []string) ([]Perm, error) {
	return permsForItems(ctx, s.queryer(), paths)
}

// PermsForItems returns every user's access to each of the given paths.
func (t *pgTx) PermsForItems(ctx context.Context, paths []string) ([]Perm, error) {
	return permsForItems(ctx, t.queryer(), paths)
}

func permsForItems(ctx context.Context, qr queryer, paths []string) ([]Perm, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	dirnames, basenames := splitPaths(paths)

	var perms []Perm
	err := qr.SelectContext(ctx, &perms, sqlPermsForItems, pq.Array(dirnames), pq.Array(basenames))
	if err != nil {
		return nil, fmt.Errorf("icat: getting permissions for %d path(s): %w", len(paths), err)
	}
	return perms, nil
}

// splitPaths splits absolute iRODS paths into parallel dirname and basename arrays, which
// is the shape the queries join against.
//
// path.Split is used rather than filepath: iRODS paths always use forward slashes, so on a
// platform with a different separator filepath would produce the wrong answer.
func splitPaths(paths []string) (dirnames, basenames []string) {
	dirnames = make([]string, 0, len(paths))
	basenames = make([]string, 0, len(paths))

	for _, p := range paths {
		p = strings.TrimRight(p, "/")
		dir, base := path.Split(p)
		dirnames = append(dirnames, strings.TrimRight(dir, "/"))
		basenames = append(basenames, base)
	}
	return dirnames, basenames
}
