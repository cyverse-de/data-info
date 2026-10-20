package rods

import (
	"context"
	"database/sql"

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
	// Checksum is nil only when the catalog holds none; an empty one is still reported
	// because terrain requires md5 on every file.
	Checksum *string
	Exists   bool
}

// ACLEntry is one user's access to a path.
type ACLEntry struct {
	User       string
	Zone       string
	Permission Permission
}

// AVU is one metadata triple.
type AVU = irodsclient.AVU

// View is the read surface handlers program against. Every accessor returns at once with a
// value whose computation has already started, so a caller can ask for several facts and
// then await them all.
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

	// UUIDListing returns a sorted page of the items carrying a set of data ids, and
	// UUIDCount how many there are in total.
	UUIDListing(ctx context.Context, q UUIDListingQuery) *lazy.Value[[]icat.ListingRow]
	UUIDCount(ctx context.Context, q UUIDListingQuery) *lazy.Value[int64]
}

// ListingQuery selects a page of a collection's children.
type ListingQuery = icat.ListingQuery

// UUIDListingQuery selects a page of the items carrying a set of data ids.
type UUIDListingQuery = icat.UUIDListingQuery

// ChildCounts is how many files and subfolders a collection holds.
type ChildCounts = icat.ChildCounts

// Permits reports whether a held access level satisfies a required one; own implies write
// implies read.
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
		Checksum:   nullableString(row.DataChecksum),
		Exists:     true,
	}
}

// nullableString keeps the difference between a NULL column and an empty one.
func nullableString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
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
