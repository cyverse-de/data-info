// Package rods is the request-scoped view of the data store, hiding whether the iRODS
// protocol or the catalog database answers a given question.
//
// Lookups start when asked for, not when read, so the facts one entry needs resolve
// concurrently; a lookup may answer from another's resolved result but never waits on one
// still running; and results are remembered for the request. See Scope for which backend
// answers what.
package rods

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/irodsclient"
	"github.com/cyverse-de/data-info/internal/lazy"
	"golang.org/x/sync/semaphore"
)

// Deps are the backends a scope reads through.
type Deps struct {
	ICAT  icat.Store
	IRODS *irodsclient.Pool

	// Zone is the iRODS zone every path belongs to.
	Zone string

	// InfoTypeAttribute is the AVU attribute holding a data object's info type.
	InfoTypeAttribute string
}

// Options tune one scope.
type Options struct {
	// User is whose permissions filter what this scope can see. Required.
	User string

	// CatalogParallelism bounds concurrent catalog queries, and ProtocolParallelism
	// concurrent iRODS operations. The protocol limit is much the lower of the two: a
	// session holds only a handful of connections, and exceeding them does not fail, it
	// serialises.
	CatalogParallelism  int64
	ProtocolParallelism int64
}

// Defaults for Options.
const (
	DefaultCatalogParallelism  = 5
	DefaultProtocolParallelism = 2
)

// Scope is one request's view. Open one per request and Close it when the request ends.
//
// Where each fact comes from:
//
//	catalog   stat, object type, size, timestamps, uuid, info type, permission, access lists,
//	          user existence and kind, a user's groups
//	protocol  metadata (AVUs), tickets, group administration, file contents, and everything
//	          that writes
//
// Over the protocol a stat costs about 10ms and an access list about 20ms per path, while
// the catalog answers the bulk endpoints' thousand paths in tens of milliseconds; see
// docs/irods-vs-icat-measurements.md.
type Scope struct {
	deps Deps
	opts Options

	// ctx bounds every memoized computation. Memoized work is shared, so binding it to
	// whichever caller happened to ask first would let one handler's short deadline kill
	// a value the rest of the request still needs.
	ctx    context.Context
	cancel context.CancelFunc

	catalogSem  *semaphore.Weighted
	protocolSem *semaphore.Weighted

	mu     sync.Mutex
	memo   map[memoKey]any
	rows   map[string]icat.Row
	sess   *irodsclient.Session
	group  *lazy.Value[[]int64]
	closed bool

	// protocol tracks work still using the iRODS session, so Close does not return it to
	// the pool while a call is in flight.
	protocol sync.WaitGroup
}

// memoKey identifies one remembered lookup.
type memoKey struct {
	kind kind
	arg  string
}

// kind distinguishes the lookups a scope remembers.
type kind uint8

const (
	kindItem kind = iota
	kindACL
	kindAVUs
	kindUserExists
	kindUserKind
	kindUserGroups
	kindChildCounts
)

// Open returns a view bound to ctx, which should be the request's context. It performs no
// I/O.
func Open(ctx context.Context, deps Deps, opts Options) (*Scope, error) {
	if opts.User == "" {
		return nil, fmt.Errorf("rods: a user is required")
	}
	if deps.ICAT == nil {
		return nil, fmt.Errorf("rods: a catalog store is required")
	}
	if deps.IRODS == nil {
		return nil, fmt.Errorf("rods: an iRODS pool is required")
	}
	if deps.Zone == "" {
		return nil, fmt.Errorf("rods: a zone is required")
	}

	if opts.CatalogParallelism <= 0 {
		opts.CatalogParallelism = DefaultCatalogParallelism
	}
	if opts.ProtocolParallelism <= 0 {
		opts.ProtocolParallelism = DefaultProtocolParallelism
	}

	scopeCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	// Still tied to the request: WithoutCancel detaches so that one caller's derived
	// deadline cannot kill shared work, and this restores the request's own lifetime.
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-scopeCtx.Done():
		}
	}()

	return &Scope{
		deps:        deps,
		opts:        opts,
		ctx:         scopeCtx,
		cancel:      cancel,
		catalogSem:  semaphore.NewWeighted(opts.CatalogParallelism),
		protocolSem: semaphore.NewWeighted(opts.ProtocolParallelism),
		memo:        make(map[memoKey]any),
		rows:        make(map[string]icat.Row),
	}, nil
}

// User is whose permissions this scope reads with.
func (s *Scope) User() string { return s.opts.User }

// Zone is the iRODS zone.
func (s *Scope) Zone() string { return s.deps.Zone }

// errScopeClosed is what a lookup on a scope that has been closed fails with.
var errScopeClosed = errors.New("rods: the scope is closed")

// Close releases the scope's resources; it is safe to call more than once. It waits for
// in-flight protocol work before returning the session, because callers may stop waiting on
// a lookup without cancelling it.
func (s *Scope) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	s.cancel()
	s.protocol.Wait()

	s.mu.Lock()
	sess := s.sess
	s.sess = nil
	s.mu.Unlock()

	if sess != nil {
		sess.Close()
	}
}

// session returns the scope's iRODS session, opening it on first use: most requests never
// touch the protocol, and a session costs a connection the server may not have spare.
func (s *Scope) session(ctx context.Context) (*irodsclient.Session, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errScopeClosed
	}
	if s.sess != nil {
		defer s.mu.Unlock()
		return s.sess, nil
	}
	s.mu.Unlock()

	sess, err := s.deps.IRODS.ForUser(ctx, s.opts.User)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Another goroutine may have opened one while this was connecting, and Close may have
	// run. Either way this one is surplus.
	if s.closed {
		sess.Close()
		return nil, errScopeClosed
	}
	if s.sess != nil {
		sess.Close()
		return s.sess, nil
	}
	s.sess = sess
	return sess, nil
}

// memoize returns the remembered value for a key, or stores the one build produces. build
// runs under the scope's lock, so it must only dispatch work (lazy.Go) and may call only the
// *Locked variants.
func memoize[T any](s *Scope, key memoKey, build func() *lazy.Value[T]) *lazy.Value[T] {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.memo[key]; ok {
		if typed, ok := existing.(*lazy.Value[T]); ok {
			return typed
		}
	}

	// Checked under the lock Close takes to set closed, so that nothing built here can add
	// protocol work after Close has begun waiting for it.
	if s.closed {
		return lazy.Failed[T](errScopeClosed)
	}

	value := build()
	s.memo[key] = value
	return value
}

// publishRows records catalog rows so later single-path lookups can answer from them
// without another query.
func (s *Scope) publishRows(rows []icat.Row) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, row := range rows {
		s.rows[row.FullPath] = row
	}
}

// peekRow returns a published row without waiting.
func (s *Scope) peekRow(path string) (icat.Row, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.peekRowLocked(path)
}

// peekRowLocked is peekRow for callers that already hold the lock, such as anything running
// inside memoize's build.
func (s *Scope) peekRowLocked(path string) (icat.Row, bool) {
	row, ok := s.rows[path]
	return row, ok
}

// groupIDs returns the requesting user's group ids, which every permission-filtered catalog
// query needs, fetched once per scope.
func (s *Scope) groupIDs(ctx context.Context) *lazy.Value[[]int64] {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.groupIDsLocked(ctx)
}

// groupIDsLocked is groupIDs for callers that already hold the lock. It takes no catalog
// slot: every catalog lookup waits on it, so with every slot held by waiters it could never
// run. Nothing under the catalog semaphore may wait on a value that needs it.
func (s *Scope) groupIDsLocked(ctx context.Context) *lazy.Value[[]int64] {
	if s.group != nil {
		return s.group
	}

	s.group = lazy.Go(ctx, nil, func(ctx context.Context) ([]int64, error) {
		return s.deps.ICAT.UserGroupIDs(ctx, s.opts.User, s.deps.Zone)
	})
	return s.group
}

// normalizePath trims trailing slashes to match the catalog's canonical paths, so a
// caller's spelling cannot make a memo or row lookup miss.
func normalizePath(p string) string {
	if p == "/" {
		return p
	}
	return strings.TrimRight(p, "/")
}

// normalizePaths normalises a batch, preserving order.
func normalizePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, normalizePath(p))
	}
	return out
}

// recordAbsent marks requested paths the catalog did not return, so a later single-path
// lookup answers immediately instead of querying again.
func (s *Scope) recordAbsent(requested []string, found map[string]Stat) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, p := range requested {
		if _, ok := found[p]; ok {
			continue
		}
		key := memoKey{kindItem, p}
		if _, ok := s.memo[key]; ok {
			continue
		}
		s.memo[key] = lazy.Failed[icat.Row](icat.ErrNoSuchItem)
	}
}

// seedChildCounts records batched counts against their per-path keys, including zero for a
// collection with no children, so later per-path lookups cost nothing.
func (s *Scope) seedChildCounts(requested []string, found map[string]ChildCounts) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, p := range requested {
		key := memoKey{kindChildCounts, p}
		if _, ok := s.memo[key]; ok {
			continue
		}
		s.memo[key] = lazy.Resolved(found[p])
	}
}

// seedACLs records batched access lists against their per-path keys, empty results
// included, so a handler that batches and then reports each path pays nothing extra.
func (s *Scope) seedACLs(requested []string, found map[string][]ACLEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, p := range requested {
		key := memoKey{kindACL, p}
		if _, ok := s.memo[key]; ok {
			continue
		}
		s.memo[key] = lazy.Resolved(found[p])
	}
}
