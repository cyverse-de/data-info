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
	kindUserGroups
	kindChildCounts
)

// Open returns a request-scoped view bound to ctx. It performs no I/O.
//
// ctx should be the request's context: everything the scope resolves belongs to that
// request and should stop when it does.
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

// Close releases anything the scope holds. It is safe to call more than once.
//
// It waits for protocol work already in flight before returning the session to the pool.
// Returning it early would let the pool evict and tear down the connections a call is still
// using -- the scope deliberately lets a caller stop waiting without cancelling the work, so
// that is not a rare case.
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

// session returns the scope's iRODS session, opening it on first use.
//
// It is opened lazily because most requests never touch the protocol: the catalog answers
// the reads, and a session costs a connection the server may not have spare.
func (s *Scope) session(ctx context.Context) (*irodsclient.Session, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("rods: the scope is closed")
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
		return nil, fmt.Errorf("rods: the scope is closed")
	}
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
//
// It deliberately takes no catalog slot. Every other catalog lookup waits on this one, so
// if it needed a slot of its own it could be starved by the very lookups that are waiting
// for it: once CatalogParallelism lookups hold every slot, none can proceed and none can
// release. That is a real deadlock, not a theoretical one. The general rule this follows is
// that nothing running under the catalog semaphore may wait on another value that needs it;
// this is the only shared dependency, so exempting it is enough to keep that true.
func (s *Scope) groupIDsLocked(ctx context.Context) *lazy.Value[[]int64] {
	if s.group != nil {
		return s.group
	}

	s.group = lazy.Go(ctx, nil, func(ctx context.Context) ([]int64, error) {
		return s.deps.ICAT.UserGroupIDs(ctx, s.opts.User, s.deps.Zone)
	})
	return s.group
}

// normalizePath puts a path into the form the catalog reports, so that a caller's trailing
// slash cannot make a lookup miss.
//
// The catalog trims trailing slashes when it splits a path, so rows come back canonical. A
// scope that keyed its memo and its published rows by the caller's spelling would report a
// path as absent when the only difference was a slash, and would re-query for one it had
// already fetched. Hiding that is this layer's job.
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
