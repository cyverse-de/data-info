package irodsclient

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	irodsfs "github.com/cyverse/go-irodsclient/fs"
	"github.com/cyverse/go-irodsclient/irods/types"
)

// Config describes how to reach iRODS and how many sessions to keep open.
type Config struct {
	Host string
	Port int
	Zone string

	// ProxyUser and ProxyPassword are the account data-info authenticates as. Requests on
	// behalf of a user run in client-user mode on top of it.
	ProxyUser     string
	ProxyPassword string

	Resource string

	// AppName is reported to the iRODS server for connection accounting.
	AppName string

	// MaxSessions bounds how many distinct client users are held open at once. Each one
	// owns its own connection pool, so this multiplied by the per-session connection
	// count is the ceiling on agents this replica can occupy on the iRODS server.
	MaxSessions int

	// IdleTimeout evicts a session that has gone unused.
	IdleTimeout time.Duration

	// OperationTimeout and LongOperationTimeout become socket deadlines on the metadata
	// connection. They bound how long an orphaned call can hold a connection after its
	// caller has given up, so neither should be generous.
	OperationTimeout     time.Duration
	LongOperationTimeout time.Duration

	// IOOperationTimeout and IOLongOperationTimeout apply to the connection used for
	// file transfer, where a single operation legitimately runs for hours.
	IOOperationTimeout     time.Duration
	IOLongOperationTimeout time.Duration
}

// Defaults for Config. The operation timeouts intentionally stay close to
// go-irodsclient's own (1m/5m) rather than the much longer values a batch tool would use:
// a request-path service wants an abandoned call to unwind quickly, and listing and bulk
// calls run under the *long* timeout, so both have to be bounded.
const (
	DefaultMaxSessions            = 32
	DefaultIdleTimeout            = 5 * time.Minute
	DefaultOperationTimeout       = 1 * time.Minute
	DefaultLongOperationTimeout   = 5 * time.Minute
	DefaultIOOperationTimeout     = 5 * time.Minute
	DefaultIOLongOperationTimeout = 12 * time.Hour
)

func (c *Config) applyDefaults() {
	if c.MaxSessions <= 0 {
		c.MaxSessions = DefaultMaxSessions
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleTimeout
	}
	if c.OperationTimeout <= 0 {
		c.OperationTimeout = DefaultOperationTimeout
	}
	if c.LongOperationTimeout <= 0 {
		c.LongOperationTimeout = DefaultLongOperationTimeout
	}
	if c.IOOperationTimeout <= 0 {
		c.IOOperationTimeout = DefaultIOOperationTimeout
	}
	if c.IOLongOperationTimeout <= 0 {
		c.IOLongOperationTimeout = DefaultIOLongOperationTimeout
	}
	if c.AppName == "" {
		c.AppName = "data-info"
	}
}

// Pool hands out sessions, one per client user plus one for the proxy account itself.
//
// A single FileSystem cannot serve more than one client user: the client username travels
// in the startup packet sent when a connection is established, and there is no way to
// change it afterwards. The alternatives are to run everything as the proxy account and
// enforce permissions ourselves -- which widens the blast radius of any missed check to
// include move, rename, upload and mkdir -- or to build a FileSystem per request, which
// costs an authentication round trip and two goroutines every time and would exhaust the
// server's agents under a listing burst. So sessions are pooled per user and evicted by
// least-recent use.
type Pool struct {
	cfg Config

	mu      sync.Mutex
	entries map[string]*list.Element // client user ("" for the proxy account) -> lru element
	lru     *list.List               // front is most recently used
	closed  bool
}

// entry is one user's FileSystem and its bookkeeping.
type entry struct {
	user     string
	fs       *irodsfs.FileSystem
	lastUsed time.Time
	inUse    int
}

// NewPool validates the configuration and returns a pool. It opens no connections; the
// first session does that, so the service starts even while iRODS is down.
func NewPool(cfg Config) (*Pool, error) {
	cfg.applyDefaults()

	if cfg.Host == "" {
		return nil, fmt.Errorf("irods host is required")
	}
	if cfg.Port <= 0 {
		return nil, fmt.Errorf("irods port is required")
	}
	if cfg.Zone == "" {
		return nil, fmt.Errorf("irods zone is required")
	}
	if cfg.ProxyUser == "" {
		return nil, fmt.Errorf("irods proxy user is required")
	}

	return &Pool{
		cfg:     cfg,
		entries: make(map[string]*list.Element),
		lru:     list.New(),
	}, nil
}

// Admin returns a session acting as the proxy account itself, with no client user. This
// is the Clojure with-jargon-exceptions [cm] form, used where data-info enforces
// permissions itself rather than delegating to iRODS.
func (p *Pool) Admin(ctx context.Context) (*Session, error) {
	return p.session(ctx, "")
}

// ForUser returns a session acting as user, so iRODS enforces that user's permissions.
// This is the Clojure with-jargon-exceptions :client-user user form.
func (p *Pool) ForUser(ctx context.Context, user string) (*Session, error) {
	if user == "" {
		return nil, fmt.Errorf("a client user is required; use Admin for the proxy account")
	}
	return p.session(ctx, user)
}

func (p *Pool) session(ctx context.Context, user string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("irods pool is closed")
	}

	if el, ok := p.entries[user]; ok {
		e := el.Value.(*entry)
		e.lastUsed = time.Now()
		e.inUse++
		p.lru.MoveToFront(el)
		p.mu.Unlock()
		return &Session{pool: p, entry: e, fs: e.fs, clientUser: user}, nil
	}
	p.mu.Unlock()

	// Build outside the lock: NewFileSystem can block on the network, and holding the
	// mutex through it would serialise every other user's requests behind it.
	fsys, err := p.newFileSystem(user)
	if err != nil {
		return nil, Translate(err, "", user)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		fsys.Release()
		return nil, fmt.Errorf("irods pool is closed")
	}

	// Another goroutine may have built one for the same user while we were connecting.
	if el, ok := p.entries[user]; ok {
		fsys.Release()
		e := el.Value.(*entry)
		e.lastUsed = time.Now()
		e.inUse++
		p.lru.MoveToFront(el)
		return &Session{pool: p, entry: e, fs: e.fs, clientUser: user}, nil
	}

	e := &entry{user: user, fs: fsys, lastUsed: time.Now(), inUse: 1}
	p.entries[user] = p.lru.PushFront(e)
	p.evictLocked()

	return &Session{pool: p, entry: e, fs: fsys, clientUser: user}, nil
}

// newFileSystem builds a FileSystem for one client user. An empty user means the proxy
// account acts as itself.
func (p *Pool) newFileSystem(clientUser string) (*irodsfs.FileSystem, error) {
	user := clientUser
	if user == "" {
		user = p.cfg.ProxyUser
	}

	account, err := types.CreateIRODSProxyAccount(
		p.cfg.Host, p.cfg.Port,
		user, p.cfg.Zone,
		p.cfg.ProxyUser, p.cfg.Zone,
		types.AuthSchemeNative, p.cfg.ProxyPassword, p.cfg.Resource,
	)
	if err != nil {
		return nil, err
	}

	fsc := irodsfs.NewFileSystemConfig(p.cfg.AppName)

	// Caching has to stay off. A pooled FileSystem outlives the request that created it,
	// a second replica mutates the same collections, and info-typer writes ipc-filetype
	// AVUs out of band -- so a cached listing or stat can be wrong by the time it is
	// read. The Clojure service had no such cache; introducing one silently would be a
	// correctness regression rather than an optimisation.
	fsc.Cache.NoCache = true

	// Connect lazily so a pool built while iRODS is down still returns.
	fsc.MetadataConnection.InitNumber = 0
	fsc.MetadataConnection.OperationTimeout = types.Duration(p.cfg.OperationTimeout)
	fsc.MetadataConnection.LongOperationTimeout = types.Duration(p.cfg.LongOperationTimeout)

	fsc.IOConnection.InitNumber = 0
	fsc.IOConnection.OperationTimeout = types.Duration(p.cfg.IOOperationTimeout)
	fsc.IOConnection.LongOperationTimeout = types.Duration(p.cfg.IOLongOperationTimeout)

	return irodsfs.NewFileSystem(account, fsc)
}

// evictLocked drops least-recently-used sessions that nobody is holding, until the pool is
// within its limit. A session in use is skipped rather than torn down under its caller.
func (p *Pool) evictLocked() {
	cutoff := time.Now().Add(-p.cfg.IdleTimeout)

	for el := p.lru.Back(); el != nil; {
		prev := el.Prev()
		e := el.Value.(*entry)

		overLimit := p.lru.Len() > p.cfg.MaxSessions
		idle := e.lastUsed.Before(cutoff)

		if e.inUse == 0 && (overLimit || idle) {
			p.lru.Remove(el)
			delete(p.entries, e.user)
			go e.fs.Release()
		}
		el = prev
	}
}

// release returns a session to the pool. A poisoned session's FileSystem is torn down
// instead of reused; see Session.
func (p *Pool) release(e *entry, poisoned bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	e.inUse--
	e.lastUsed = time.Now()

	if !poisoned {
		p.evictLocked()
		return
	}

	if el, ok := p.entries[e.user]; ok && el.Value.(*entry) == e {
		p.lru.Remove(el)
		delete(p.entries, e.user)
	}
	if e.inUse <= 0 {
		go e.fs.Release()
	}
}

// Close tears down every session. In-flight calls are not interrupted; their sessions are
// released when they finish.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true
	for el := p.lru.Front(); el != nil; el = el.Next() {
		e := el.Value.(*entry)
		if e.inUse == 0 {
			go e.fs.Release()
		}
	}
	p.entries = make(map[string]*list.Element)
	p.lru.Init()
}

// Probe reports whether iRODS is reachable and will accept our credentials. Unlike a bare
// TCP dial it exercises authentication, which is what the Clojure irods-running? check did
// by opening a Jargon connection and stat-ing the home collection.
func (p *Pool) Probe(ctx context.Context) error {
	s, err := p.Admin(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	_, err = Do(ctx, s, func(fsys *irodsfs.FileSystem) (any, error) {
		return fsys.GetServerVersion()
	})
	return err
}
