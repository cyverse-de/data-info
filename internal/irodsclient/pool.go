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
	entries map[string]*list.Element // session key -> lru element
	lru     *list.List               // front is most recently used
	closed  bool

	// tracked holds every FileSystem that may still have connections open, from creation
	// until its Release returns -- including ones unlinked from the pool, which still
	// count against MaxTotalConnections until they are torn down.
	tracked map[*entry]*irodsfs.FileSystem

	// changed is closed and replaced whenever room under MaxTotalConnections may have
	// appeared, which is what callers waiting for a connection wake on.
	changed chan struct{}

	// countOpen reports a FileSystem's open connections. Tests replace it.
	countOpen func(*irodsfs.FileSystem) usage

	waits waitReport

	sweeperStop chan struct{}
	sweeperDone chan struct{}

	probeMu      sync.Mutex
	probeAt      time.Time
	probeResult  error
	probeRunning chan struct{} // closed when the probe in flight finishes; nil when none is
}

// Session keys. A client user's key is their username, so these two sentinels are chosen
// to be values iRODS cannot produce: usernames cannot be empty or contain a slash.
const (
	// adminKey is the proxy account acting as itself.
	adminKey = ""

	// probeKey was once a separate session, to keep a probe's short deadline from
	// poisoning the session admin work uses. That cost a second concurrent connection,
	// which is a resource the server may simply refuse -- QA rejects a second connection
	// for the service account outright. The probe now shares the admin session and is
	// given a budget generous enough that a timeout means something is genuinely wrong,
	// in which case discarding the session is the right response anyway.
	probeKey = adminKey
)

// entry is one user's FileSystem and its bookkeeping.
type entry struct {
	user     string
	fs       *irodsfs.FileSystem
	lastUsed time.Time
	inUse    int

	// leased is the connections that calls in flight on this session may need. See
	// Pool.lease.
	leased usage

	// dead marks an entry that has been unlinked from the pool and must be released once
	// the last holder lets go. Without it an entry unlinked while another caller still
	// held it would never be released by anyone: the unlinking path sees inUse > 0 and
	// leaves it, and the remaining holder's release walks a list the entry is no longer
	// in.
	dead bool
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

	p := &Pool{
		cfg:         cfg,
		entries:     make(map[string]*list.Element),
		lru:         list.New(),
		tracked:     make(map[*entry]*irodsfs.FileSystem),
		changed:     make(chan struct{}),
		countOpen:   openUsage,
		sweeperStop: make(chan struct{}),
		sweeperDone: make(chan struct{}),
	}
	go p.startSweeper()

	return p, nil
}

// Admin returns a session acting as the proxy account itself, with no client user. This
// is the Clojure with-jargon-exceptions [cm] form, used where data-info enforces
// permissions itself rather than delegating to iRODS.
func (p *Pool) Admin(ctx context.Context) (*Session, error) {
	return p.session(ctx, adminKey, "")
}

// ForUser returns a session acting as user, so iRODS enforces that user's permissions.
// This is the Clojure with-jargon-exceptions :client-user user form.
func (p *Pool) ForUser(ctx context.Context, user string) (*Session, error) {
	if user == "" {
		return nil, fmt.Errorf("a client user is required; use Admin for the proxy account")
	}

	// Proxying as the proxy account itself is the same as acting as it directly, so reuse
	// the one session rather than opening a second identical one. That is not a
	// micro-optimisation here: a zone may grant this service very few concurrent
	// connections, and the service account is the one most likely to be making requests
	// while other work is in flight.
	if user == p.cfg.ProxyUser {
		return p.session(ctx, adminKey, "")
	}

	return p.session(ctx, user, user)
}

func (p *Pool) session(ctx context.Context, key, clientUser string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errPoolClosed
	}

	if el, ok := p.entries[key]; ok {
		e := el.Value.(*entry)
		e.lastUsed = time.Now()
		e.inUse++
		p.lru.MoveToFront(el)
		p.mu.Unlock()
		return &Session{pool: p, entry: e, fs: e.fs, clientUser: clientUser}, nil
	}
	p.mu.Unlock()

	// Build outside the lock: NewFileSystem can block on the network, and holding the
	// mutex through it would serialise every other user's requests behind it.
	fsys, err := p.newFileSystem(clientUser)
	if err != nil {
		return nil, Translate(err, "", clientUser)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		fsys.Release()
		return nil, errPoolClosed
	}

	// Another goroutine may have built one for the same user while we were connecting.
	if el, ok := p.entries[key]; ok {
		go fsys.Release() //nolint:errcheck // Release returns nothing; this is a teardown
		e := el.Value.(*entry)
		e.lastUsed = time.Now()
		e.inUse++
		p.lru.MoveToFront(el)
		return &Session{pool: p, entry: e, fs: e.fs, clientUser: clientUser}, nil
	}

	e := &entry{user: key, fs: fsys, lastUsed: time.Now(), inUse: 1}
	p.entries[key] = p.lru.PushFront(e)
	p.tracked[e] = fsys
	p.evictLocked()

	return &Session{pool: p, entry: e, fs: fsys, clientUser: clientUser}, nil
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
	fsc.Cache.Backend = &irodsfs.CacheBackendConfig{Type: irodsfs.CacheBackendTypeNone}

	// Connect lazily so a pool built while iRODS is down still returns.
	fsc.MetadataConnection.InitNumber = 0
	fsc.MetadataConnection.MaxNumber = p.cfg.MaxConnections
	// One idle connection per pool at most: idle connections count against the Data
	// Store's per-node limit, and nothing can reclaim them from a session still in use.
	fsc.MetadataConnection.MaxIdleNumber = 1
	fsc.MetadataConnection.IdleTimeout = types.Duration(p.cfg.IdleTimeout)
	fsc.MetadataConnection.OperationTimeout = types.Duration(p.cfg.OperationTimeout)
	fsc.MetadataConnection.LongOperationTimeout = types.Duration(p.cfg.LongOperationTimeout)

	fsc.IOConnection.InitNumber = 0
	fsc.IOConnection.MaxNumber = p.cfg.MaxIOConnections
	fsc.IOConnection.MaxIdleNumber = 1
	fsc.IOConnection.IdleTimeout = types.Duration(p.cfg.IdleTimeout)
	fsc.IOConnection.OperationTimeout = types.Duration(p.cfg.IOOperationTimeout)
	fsc.IOConnection.LongOperationTimeout = types.Duration(p.cfg.IOLongOperationTimeout)

	return irodsfs.NewFileSystem(account, fsc)
}
