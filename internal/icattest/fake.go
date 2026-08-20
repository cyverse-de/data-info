// Package icattest provides an in-memory catalog for tests.
//
// It follows the convention the DE's newer Go services use of putting a fake next to the
// client it stands in for, rather than reaching for a mocking framework.
package icattest

import (
	"context"
	"database/sql"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cyverse-de/data-info/internal/icat"
)

// Fake is an in-memory icat.Store.
type Fake struct {
	mu sync.Mutex

	// Rows are the catalog rows, keyed by absolute path.
	Rows map[string]icat.Row

	// Perms are the access entries, keyed by absolute path.
	Perms map[string][]icat.Perm

	// GroupIDs are the group ids returned for any user.
	GroupIDs []int64

	// Children are the child counts returned per collection path.
	Children map[string]icat.ChildCounts

	// UUIDs maps a data id onto the path carrying it.
	UUIDs map[string]string

	// Users maps an account name onto its kind.
	Users map[string]icat.UserKind

	// Err, when set, is returned by every query.
	Err error

	// Delay, when set, is how long every query takes. A fake that answers instantly
	// cannot saturate a semaphore, so it cannot surface a starvation deadlock.
	Delay time.Duration

	// Counters record how often each query ran, so a test can prove that a batched call
	// really was one query and that memoized lookups really did not repeat.
	GetItemsCalls      atomic.Int64
	PermsCalls         atomic.Int64
	UserGroupIDsCalls  atomic.Int64
	CountChildrenCalls atomic.Int64
	PagedFolderCalls   atomic.Int64
	PathsPerGetItems   []int
}

var _ icat.Store = (*Fake)(nil)

// New returns an empty fake.
func New() *Fake {
	return &Fake{
		Rows:     map[string]icat.Row{},
		Perms:    map[string][]icat.Perm{},
		Children: map[string]icat.ChildCounts{},
		UUIDs:    map[string]string{},
		Users:    map[string]icat.UserKind{},
		GroupIDs: []int64{1, 2},
	}
}

// AddCollection records a collection at path.
func (f *Fake) AddCollection(p string, accessTypeID int64) {
	f.add(p, icat.ObjectTypeCollection, 0, accessTypeID)
}

// AddDataObject records a data object at path.
func (f *Fake) AddDataObject(p string, size, accessTypeID int64) {
	f.add(p, icat.ObjectTypeDataObject, size, accessTypeID)
}

func (f *Fake) add(p string, t icat.ObjectType, size, accessTypeID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.Rows[p] = icat.Row{
		Type:         t,
		FullPath:     p,
		BaseName:     path.Base(p),
		DataSize:     size,
		CreateTS:     "01700000000",
		ModifyTS:     "01700000001",
		AccessTypeID: accessTypeID,
	}
}

// AddPerm records one user's access to a path.
func (f *Fake) AddPerm(p, user, zone string, accessTypeID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.Perms[p] = append(f.Perms[p], icat.Perm{
		FullPath:     p,
		UserName:     user,
		Zone:         zone,
		AccessTypeID: accessTypeID,
	})
}

// UserGroupIDs implements icat.Reader.
func (f *Fake) UserGroupIDs(ctx context.Context, _, _ string) ([]int64, error) {
	f.UserGroupIDsCalls.Add(1)
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.Err != nil {
		return nil, f.Err
	}
	return f.GroupIDs, nil
}

// GetItems implements icat.Reader.
func (f *Fake) GetItems(ctx context.Context, q icat.ItemQuery) ([]icat.Row, error) {
	f.GetItemsCalls.Add(1)
	if err := f.wait(ctx); err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.PathsPerGetItems = append(f.PathsPerGetItems, len(q.Paths))
	f.mu.Unlock()

	if f.Err != nil {
		return nil, f.Err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	var out []icat.Row
	for _, p := range q.Paths {
		if row, ok := f.Rows[strings.TrimRight(p, "/")]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

// PermsForItems implements icat.Reader.
func (f *Fake) PermsForItems(ctx context.Context, paths []string) ([]icat.Perm, error) {
	f.PermsCalls.Add(1)
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.Err != nil {
		return nil, f.Err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	var out []icat.Perm
	for _, p := range paths {
		out = append(out, f.Perms[p]...)
	}
	return out, nil
}

// wait simulates query latency, so tests can saturate the caller's concurrency limits.
func (f *Fake) wait(ctx context.Context) error {
	if f.Delay <= 0 {
		return nil
	}
	select {
	case <-time.After(f.Delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AddUser records an account.
func (f *Fake) AddUser(name string, kind icat.UserKind) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Users[name] = kind
}

// LookupUser implements icat.Reader.
func (f *Fake) LookupUser(ctx context.Context, user, _ string) (icat.UserKind, error) {
	if err := f.wait(ctx); err != nil {
		return icat.UserKindNone, err
	}
	if f.Err != nil {
		return icat.UserKindNone, f.Err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Users[user], nil
}

// SetUUID records the path carrying a data id.
func (f *Fake) SetUUID(uuid, p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.UUIDs[uuid] = p
}

// PathsForUUIDs implements icat.Reader.
func (f *Fake) PathsForUUIDs(ctx context.Context, uuids []string) ([]icat.UUIDPath, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.Err != nil {
		return nil, f.Err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	var out []icat.UUIDPath
	for _, u := range uuids {
		if p, ok := f.UUIDs[u]; ok {
			out = append(out, icat.UUIDPath{UUID: u, FullPath: p})
		}
	}
	return out, nil
}

// SetChildCounts records how many children a collection holds.
func (f *Fake) SetChildCounts(p string, files, dirs int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Children[p] = icat.ChildCounts{Files: files, Dirs: dirs}
}

// CountChildren implements icat.Reader.
func (f *Fake) CountChildren(ctx context.Context, q icat.ChildCountQuery) (icat.ChildCounts, error) {
	f.CountChildrenCalls.Add(1)
	if err := f.wait(ctx); err != nil {
		return icat.ChildCounts{}, err
	}
	if f.Err != nil {
		return icat.ChildCounts{}, f.Err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Children[q.Path], nil
}

// CountChildrenBatch implements icat.Reader.
func (f *Fake) CountChildrenBatch(ctx context.Context, q icat.BatchChildCountQuery) ([]icat.PathChildCounts, error) {
	f.CountChildrenCalls.Add(1)
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.Err != nil {
		return nil, f.Err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]icat.PathChildCounts, 0, len(q.Paths))
	for _, p := range q.Paths {
		counts := f.Children[p]
		out = append(out, icat.PathChildCounts{FullPath: p, Files: counts.Files, Dirs: counts.Dirs})
	}
	return out, nil
}

// PagedFolder implements icat.Reader.
//
// It sorts and pages in memory the way the real query does, so a test can check that an
// endpoint asked for the right page and passed the order through, rather than only that it
// returned something.
func (f *Fake) PagedFolder(ctx context.Context, q icat.ListingQuery) ([]icat.ListingRow, error) {
	f.PagedFolderCalls.Add(1)
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.Err != nil {
		return nil, f.Err
	}

	f.mu.Lock()
	children := make([]icat.Row, 0)
	prefix := strings.TrimRight(q.Path, "/") + "/"
	for p, row := range f.Rows {
		if strings.HasPrefix(p, prefix) && !strings.Contains(strings.TrimPrefix(p, prefix), "/") {
			children = append(children, row)
		}
	}
	f.mu.Unlock()

	// Folders first, then the requested column, matching the query's ORDER BY.
	sort.SliceStable(children, func(i, j int) bool {
		if children[i].Type != children[j].Type {
			return children[i].Type == icat.ObjectTypeCollection
		}
		less := compareRows(children[i], children[j], q.SortColumn)
		if q.SortDirection == icat.SortDescending {
			return !less
		}
		return less
	})

	total := int64(len(children))
	if q.Offset >= len(children) {
		return nil, nil
	}
	end := min(q.Offset+q.Limit, len(children))

	page := make([]icat.ListingRow, 0, end-q.Offset)
	for _, row := range children[q.Offset:end] {
		row.TotalCount = sql.NullInt64{Int64: total, Valid: true}
		page = append(page, row)
	}
	return page, nil
}

// compareRows orders two rows by the requested column.
func compareRows(a, b icat.Row, column icat.SortColumn) bool {
	switch column {
	case "data_size":
		return a.DataSize < b.DataSize
	case "create_ts":
		return a.CreateTS < b.CreateTS
	case "modify_ts":
		return a.ModifyTS < b.ModifyTS
	case "full_path":
		return a.FullPath < b.FullPath
	default:
		return a.BaseName < b.BaseName
	}
}

// WithTx implements icat.Store.
func (f *Fake) WithTx(_ context.Context, fn func(icat.Tx) error) error { return fn(f) }

// Ping implements icat.Store.
func (f *Fake) Ping(context.Context) error { return f.Err }

// Close implements icat.Store.
func (f *Fake) Close() error { return nil }
