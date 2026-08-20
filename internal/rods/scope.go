// Package rods is the request-scoped view of the data store.
//
// It sits over two backends -- the iRODS protocol and the catalog database -- and hides
// which one answers a given question. Handlers ask it for facts about paths; it decides
// where those come from, resolves them concurrently, and remembers them for the life of
// one request.
//
// Three behaviours are load-bearing, all inherited from the Clojure layer this replaces:
//
//   - Lookups start when they are asked for, not when they are read. Formatting a single
//     directory entry asks for half a dozen facts and then reads them all; if each only
//     began on read, one round of concurrent work would become six sequential ones.
//   - A lookup may answer from another's result, but must never block waiting for one.
//     A stat can use a listing that has already resolved; waiting for one that has not
//     would stall on work that may be far more expensive, or that nobody asked for.
//   - Results are remembered per request, so asking twice costs once.
//
// What is deliberately not carried over is the Clojure layer's habit of probing several
// backends in turn to see which could answer. Each fact has one source here, chosen for
// where it is actually fast; see the routing table on Scope.
package rods

import (
	"context"
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
//	catalog   stat, object type, size, timestamps, uuid, info type, permission, access lists
//	protocol  metadata (AVUs), user existence, group membership, and everything that writes
//
// The split is not aesthetic. Measurement showed a stat costs about 10ms over the protocol
// and an access list about 20ms, per path, while the catalog answers a thousand paths in
// tens of milliseconds -- and the bulk endpoints accept a thousand paths. See
// docs/irods-vs-icat-measurements.md.
type Scope struct {
	deps Deps
	opts Options

	catalogSem  *semaphore.Weighted
	protocolSem *semaphore.Weighted

	mu    sync.Mutex
	memo  map[memoKey]any
	rows  map[string]icat.Row
	sess  *irodsclient.Session
	group *lazy.Value[[]int64]
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
	kindUserGroups
)

// Open returns a request-scoped view. It performs no I/O.
func Open(deps Deps, opts Options) *Scope {
	if opts.CatalogParallelism <= 0 {
		opts.CatalogParallelism = DefaultCatalogParallelism
	}
	if opts.ProtocolParallelism <= 0 {
		opts.ProtocolParallelism = DefaultProtocolParallelism
	}

	return &Scope{
		deps:        deps,
		opts:        opts,
		catalogSem:  semaphore.NewWeighted(opts.CatalogParallelism),
		protocolSem: semaphore.NewWeighted(opts.ProtocolParallelism),
		memo:        make(map[memoKey]any),
		rows:        make(map[string]icat.Row),
	}
}

// User is whose permissions this scope reads with.
func (s *Scope) User() string { return s.opts.User }

// Zone is the iRODS zone.
func (s *Scope) Zone() string { return s.deps.Zone }

// Close releases anything the scope holds. It is safe to call more than once.
func (s *Scope) Close() {
	s.mu.Lock()
	sess := s.sess
	s.sess = nil
	s.mu.Unlock()

	if sess != nil {
		sess.Close()
	}
}

// session returns the scope's iRODS session, opening it on first use.
//
// It is opened lazily because most requests never touch the protocol: the catalog answers
// the reads, and a session costs a connection the server may not have spare.
func (s *Scope) session(ctx context.Context) (*irodsclient.Session, error) {
	s.mu.Lock()
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

	// Another goroutine may have opened one while this was connecting.
	if s.sess != nil {
		sess.Close()
		return s.sess, nil
	}
	s.sess = sess
	return sess, nil
}

// memoize returns the remembered value for a key, or stores the one build produces.
//
// build runs while the scope's lock is held, so it must only dispatch work, never perform
// it -- lazy.Go does exactly that. It must also not call any method that takes the lock
// itself; the locked variants exist for the things build legitimately needs to read.
func memoize[T any](s *Scope, key memoKey, build func() *lazy.Value[T]) *lazy.Value[T] {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.memo[key]; ok {
		if typed, ok := existing.(*lazy.Value[T]); ok {
			return typed
		}
	}

	value := build()
	s.memo[key] = value
	return value
}

// publishRows records catalog rows so later single-path lookups can answer from them
// without another query. This is what makes a batched load pay off for the per-path
// accessors that follow it.
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

// groupIDs returns the requesting user's group ids, fetched once per scope.
//
// Every permission-filtered catalog query needs them, so resolving them once and passing
// them in saves a lookup per query rather than per request.
func (s *Scope) groupIDs(ctx context.Context) *lazy.Value[[]int64] {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.groupIDsLocked(ctx)
}

// groupIDsLocked is groupIDs for callers that already hold the lock.
func (s *Scope) groupIDsLocked(ctx context.Context) *lazy.Value[[]int64] {
	if s.group != nil {
		return s.group
	}

	s.group = lazy.Go(ctx, s.catalogSem, func(ctx context.Context) ([]int64, error) {
		return s.deps.ICAT.UserGroupIDs(ctx, s.opts.User, s.deps.Zone)
	})
	return s.group
}
