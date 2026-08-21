package rods

import (
	"context"
	"errors"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/irodsclient"
	"github.com/cyverse-de/data-info/internal/lazy"
)

// ObjectType is what lives at a path.
type ObjectType string

// The object types. ObjectTypeNone means nothing is there, or the user cannot see it --
// the catalog cannot distinguish those, since a user with no access rows simply gets no
// rows back.
const (
	ObjectTypeFile ObjectType = "file"
	ObjectTypeDir  ObjectType = "dir"
	ObjectTypeNone ObjectType = "none"
)

// Permission is an access level under the DE's names.
type Permission = icat.Permission

// The access levels, re-exported so handlers need not import the catalog package to name
// one.
const (
	PermissionNone  = icat.PermissionNone
	PermissionRead  = icat.PermissionRead
	PermissionWrite = icat.PermissionWrite
	PermissionOwn   = icat.PermissionOwn
)

// Stat is what the service reports about one path.
type Stat struct {
	Path       string
	Type       ObjectType
	UUID       string
	InfoType   string
	Size       int64
	CreatedMS  int64
	ModifiedMS int64
	Permission Permission
	Checksum   string
	Exists     bool
}

// ACLEntry is one user's access to a path.
type ACLEntry struct {
	User       string
	Zone       string
	Permission Permission
}

// AVU is one metadata triple.
type AVU = irodsclient.AVU

// View is the read surface handlers program against.
//
// Every accessor returns immediately with a value whose computation has already started, so
// a caller can ask for several facts and then await them all.
type View interface {
	Stat(ctx context.Context, path string) *lazy.Value[Stat]
	ObjectType(ctx context.Context, path string) *lazy.Value[ObjectType]
	Permission(ctx context.Context, path string) *lazy.Value[Permission]
	UUID(ctx context.Context, path string) *lazy.Value[string]
	ACL(ctx context.Context, path string) *lazy.Value[[]ACLEntry]
	AVUs(ctx context.Context, path string) *lazy.Value[[]AVU]
	UserExists(ctx context.Context, user string) *lazy.Value[bool]
	UserGroups(ctx context.Context, user string) *lazy.Value[[]string]

	// Stats resolves many paths in one catalog query. Handlers serving a bulk request
	// must use this rather than looping over Stat: the difference at the endpoints'
	// thousand-path limit is tens of milliseconds against tens of seconds.
	Stats(ctx context.Context, paths []string) *lazy.Value[map[string]Stat]

	// ACLs resolves access lists for many paths in one query, for the same reason.
	ACLs(ctx context.Context, paths []string) *lazy.Value[map[string][]ACLEntry]

	// ChildCounts reports how many files and subfolders a collection holds, counting only
	// what the requesting user can see.
	ChildCounts(ctx context.Context, path string) *lazy.Value[ChildCounts]

	// PathsForUUIDs resolves data ids to paths, in one query.
	PathsForUUIDs(ctx context.Context, uuids []string) *lazy.Value[map[string]string]

	// ChildCountsFor resolves child counts for many collections in one query.
	ChildCountsFor(ctx context.Context, paths []string) *lazy.Value[map[string]ChildCounts]

	// Listing returns a sorted page of a collection's immediate children.
	Listing(ctx context.Context, q ListingQuery) *lazy.Value[[]icat.ListingRow]

	// Subfolders returns every subfolder of a collection, unpaged.
	Subfolders(ctx context.Context, path string) *lazy.Value[[]icat.ListingRow]
}

// ListingQuery selects a page of a collection's children.
type ListingQuery = icat.ListingQuery

// ChildCounts is how many files and subfolders a collection holds.
type ChildCounts = icat.ChildCounts

// Permits reports whether a held access level satisfies a required one.
//
// iRODS access is a ladder rather than a set of flags -- own implies write implies read --
// so a check is a comparison and not a membership test.
func Permits(held, required Permission) bool {
	rank := map[Permission]int{
		PermissionNone:  0,
		PermissionRead:  1,
		PermissionWrite: 2,
		PermissionOwn:   3,
	}
	return rank[held] >= rank[required] && held != PermissionNone
}

var _ View = (*Scope)(nil)

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

// ACL reports every user's access to a path.
//
// The catalog query behind this is not scoped to the requesting user, so a handler must
// confirm the user may see the path before calling it.
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

		// Seed the per-path memo, empty results included, so a handler that batches and
		// then reports each path individually does not pay a round trip per path.
		s.seedACLs(paths, out)

		return out, nil
	})
}

// AVUs reports a path's metadata.
//
// This is the one read that goes over the protocol rather than the catalog. iRODS applies
// its own visibility rules to metadata, and the Clojure service read AVUs through Jargon
// for the same reason.
func (s *Scope) AVUs(_ context.Context, path string) *lazy.Value[[]AVU] {
	path = normalizePath(path)

	return memoize(s, memoKey{kindAVUs, path}, func() *lazy.Value[[]AVU] {
		s.protocol.Add(1)
		return lazy.Go(s.ctx, s.protocolSem, func(ctx context.Context) ([]AVU, error) {
			defer s.protocol.Done()

			sess, err := s.session(ctx)
			if err != nil {
				return nil, err
			}
			return irodsclient.ListAVUs(ctx, sess, path)
		})
	})
}

// UserExists reports whether an iRODS account exists. Groups do not count.
//
// This asks the catalog rather than the protocol. Every request validates its caller, so
// asking over the protocol would cost a connection per request on a zone that grants this
// service very few -- and the catalog is where iRODS keeps the answer anyway.
func (s *Scope) UserExists(_ context.Context, user string) *lazy.Value[bool] {
	return memoize(s, memoKey{kindUserExists, user}, func() *lazy.Value[bool] {
		return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) (bool, error) {
			kind, err := s.deps.ICAT.LookupUser(ctx, user, s.deps.Zone)
			if err != nil {
				return false, err
			}
			// A group is not a user. Sharing with a group is a different operation, and
			// treating one as the other would let a group name satisfy a user check.
			return kind != icat.UserKindNone && kind != icat.UserKindGroup, nil
		})
	})
}

// UserGroups reports the groups a user belongs to, as bare names.
//
// They are not zone-qualified; the endpoint that reports them appends the zone itself. A
// caller that assumed otherwise would compare qualified names against bare ones and
// silently never match.
//
// This asks the catalog rather than the protocol, for the same reason as UserExists: it
// removes the last protocol round trip from the read path, and a read that needed a
// connection would fail whenever request traffic already held the few a zone grants.
func (s *Scope) UserGroups(_ context.Context, user string) *lazy.Value[[]string] {
	return memoize(s, memoKey{kindUserGroups, user}, func() *lazy.Value[[]string] {
		return lazy.Go(s.ctx, s.catalogSem, func(ctx context.Context) ([]string, error) {
			return s.deps.ICAT.UserGroupNames(ctx, user, s.deps.Zone)
		})
	})
}
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
// intermediate results over every data object in the collection before deciding what to
// keep, so using it for a tree view would make a hot endpoint far more expensive on a folder
// holding many files. It is also unpaged, as the reference is, so a folder with many
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

// statOf converts a catalog row into a Stat.
func statOf(row icat.Row) Stat {
	t := ObjectTypeFile
	if row.IsCollection() {
		t = ObjectTypeDir
	}

	return Stat{
		Path:       row.FullPath,
		Type:       t,
		UUID:       row.UUID.String,
		InfoType:   row.InfoType.String,
		Size:       row.DataSize,
		CreatedMS:  row.CreatedMillis(),
		ModifiedMS: row.ModifiedMillis(),
		Permission: row.Permission(),
		Checksum:   row.DataChecksum.String,
		Exists:     true,
	}
}

// aclOf converts catalog permission rows into access entries.
func aclOf(perms []icat.Perm) []ACLEntry {
	out := make([]ACLEntry, 0, len(perms))
	for _, p := range perms {
		out = append(out, ACLEntry{
			User:       p.UserName,
			Zone:       p.Zone,
			Permission: p.Permission(),
		})
	}
	return out
}
