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

	// IdleTimeout evicts a session that has gone unused. A background sweeper enforces
	// it, so a service that goes quiet does not hold sessions open indefinitely.
	IdleTimeout time.Duration

	// MaxConnections bounds the metadata connections one session may open. It is the
	// per-user concurrency limit: go-irodsclient defaults it to 2, and because almost
	// every operation is willing to share a connection, extra concurrency beyond it does
	// not fail -- it serialises. Raising it trades against the iRODS server's agent
	// limit, whose ceiling for this replica is MaxSessions multiplied by this.
	MaxConnections int

	// MaxIOConnections bounds the connections used for file transfer.
	MaxIOConnections int

	// ProbeTimeout bounds one health check. It is deliberately independent of the
	// caller's deadline: a probe that gives up early would poison the session it ran on,
	// and that session is the one every admin request uses.
	ProbeTimeout time.Duration

	// ProbeCacheTTL is how long a health-check result is reused. GET / reports iRODS
	// health on every call and takes no authentication, so probing it every time would
	// let an unauthenticated caller drive iRODS load.
	ProbeCacheTTL time.Duration

	// MaxRetries and RetrySleep govern retrying a refused connection. An iRODS server
	// under load closes new connections rather than queueing them, and it does so to
	// healthy clients, so a single refusal is not a reason to fail a request. Jargon
	// retried for the same reason, and the Clojure service configured it with
	// data-info.irods.max-retries and .retry-sleep.
	MaxRetries int
	RetrySleep time.Duration

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
	DefaultMaxConnections         = 4
	DefaultMaxIOConnections       = 8
	DefaultIdleTimeout            = 5 * time.Minute
	DefaultProbeCacheTTL          = 5 * time.Second
	DefaultProbeTimeout           = 15 * time.Second
	DefaultMaxRetries             = 3
	DefaultRetrySleep             = 250 * time.Millisecond
	DefaultOperationTimeout       = 1 * time.Minute
	DefaultLongOperationTimeout   = 5 * time.Minute
	DefaultIOOperationTimeout     = 5 * time.Minute
	DefaultIOLongOperationTimeout = 12 * time.Hour
)

func (c *Config) applyDefaults() {
	if c.MaxSessions <= 0 {
		c.MaxSessions = DefaultMaxSessions
	}
	if c.MaxConnections <= 0 {
		c.MaxConnections = DefaultMaxConnections
	}
	if c.MaxIOConnections <= 0 {
		c.MaxIOConnections = DefaultMaxIOConnections
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleTimeout
	}
	if c.ProbeCacheTTL <= 0 {
		c.ProbeCacheTTL = DefaultProbeCacheTTL
	}
	if c.ProbeTimeout <= 0 {
		c.ProbeTimeout = DefaultProbeTimeout
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = DefaultMaxRetries
	}
	if c.RetrySleep <= 0 {
		c.RetrySleep = DefaultRetrySleep
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
	entries map[string]*list.Element // session key -> lru element
	lru     *list.List               // front is most recently used
	closed  bool

	sweeperStop chan struct{}
	sweeperDone chan struct{}

	probeMu     sync.Mutex
	probeAt     time.Time
	probeResult error
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
		return nil, fmt.Errorf("irods pool is closed")
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
		return nil, fmt.Errorf("irods pool is closed")
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
	fsc.MetadataConnection.OperationTimeout = types.Duration(p.cfg.OperationTimeout)
	fsc.MetadataConnection.LongOperationTimeout = types.Duration(p.cfg.LongOperationTimeout)

	fsc.IOConnection.InitNumber = 0
	fsc.IOConnection.MaxNumber = p.cfg.MaxIOConnections
	fsc.IOConnection.OperationTimeout = types.Duration(p.cfg.IOOperationTimeout)
	fsc.IOConnection.LongOperationTimeout = types.Duration(p.cfg.IOLongOperationTimeout)

	return irodsfs.NewFileSystem(account, fsc)
}

// evictLocked unlinks least-recently-used sessions that are over the limit or have gone
// idle. A session still in use is unlinked but not released; the last holder does that.
func (p *Pool) evictLocked() {
	cutoff := time.Now().Add(-p.cfg.IdleTimeout)

	for el := p.lru.Back(); el != nil; {
		prev := el.Prev()
		e := el.Value.(*entry)

		if p.lru.Len() > p.cfg.MaxSessions || e.lastUsed.Before(cutoff) {
			p.unlinkLocked(el, e)
		}
		el = prev
	}
}

// unlinkLocked removes an entry from the pool and releases its FileSystem if nobody is
// holding it. Callers must hold p.mu.
func (p *Pool) unlinkLocked(el *list.Element, e *entry) {
	p.lru.Remove(el)
	delete(p.entries, e.user)
	e.dead = true

	if e.inUse == 0 {
		e.releaseFS()
	}
}

// releaseFS tears the FileSystem down off the caller's goroutine, since Release closes
// connections and can block. The nil check is not decoration: this runs in its own
// goroutine, where a panic would take the process down rather than fail one request.
func (e *entry) releaseFS() {
	if e.fs == nil {
		return
	}
	fsys := e.fs
	e.fs = nil
	go fsys.Release()
}

// release returns a session to the pool.
//
// A poisoned session's FileSystem is discarded rather than reused. That covers two cases:
// a call abandoned on context cancellation, which leaves a connection mid-protocol, and a
// connection or authentication failure, which go-irodsclient records on the session so
// that every later acquisition on it fails -- and, in v0.20.1 and v0.21.0, deadlocks. See
// Session.
func (p *Pool) release(e *entry, poisoned bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	e.inUse--
	e.lastUsed = time.Now()

	if poisoned && !e.dead {
		if el, ok := p.entries[e.user]; ok && el.Value.(*entry) == e {
			p.unlinkLocked(el, e)
			return
		}
		// Already gone from the pool; fall through to the last-holder check.
		e.dead = true
	}

	// Whoever drops the count to zero on an unlinked entry owns releasing it. Without
	// this the entry would be unreachable from the pool and never released by anyone.
	if e.dead {
		if e.inUse <= 0 {
			e.releaseFS()
		}
		return
	}

	if p.closed {
		if e.inUse <= 0 {
			e.dead = true
			e.releaseFS()
		}
		return
	}

	p.evictLocked()
}

// startSweeper runs idle eviction in the background. Without it a session is only ever
// evicted by another checkout or release, so a service that goes quiet holds every
// FileSystem it ever opened -- and the iRODS agents behind them -- indefinitely.
func (p *Pool) startSweeper() {
	interval := p.cfg.IdleTimeout / 2
	if interval < time.Second {
		interval = time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(p.sweeperDone)

	for {
		select {
		case <-p.sweeperStop:
			return
		case <-ticker.C:
			p.mu.Lock()
			if !p.closed {
				p.evictLocked()
			}
			p.mu.Unlock()
		}
	}
}

// Close tears down the pool. Sessions still in use are unlinked and released by their last
// holder rather than pulled out from under an in-flight call.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true

	for el := p.lru.Front(); el != nil; {
		next := el.Next()
		p.unlinkLocked(el, el.Value.(*entry))
		el = next
	}
	p.mu.Unlock()

	close(p.sweeperStop)
	<-p.sweeperDone
}

// Probe reports whether iRODS is reachable and will accept our credentials. Unlike a bare
// TCP dial it exercises authentication, which is what the Clojure irods-running? check did
// by opening a Jargon connection and stat-ing the home collection.
//
// The result is cached briefly and the probe runs on its own session. GET / reports iRODS
// health on every call, and that endpoint takes no authentication, so an uncached probe
// would let anyone drive a connection acquisition and a round trip per request. Using the
// shared admin session would be worse still: a probe deadline that expires poisons the
// session it ran on, which would tear down the session every other admin request is using.
func (p *Pool) Probe(ctx context.Context) error {
	p.probeMu.Lock()
	defer p.probeMu.Unlock()

	if !p.probeAt.IsZero() && time.Since(p.probeAt) < p.cfg.ProbeCacheTTL {
		return p.probeResult
	}

	p.probeResult = p.probeWithRetries(ctx)
	p.probeAt = time.Now()
	return p.probeResult
}

// probeWithRetries retries a refused connection with a linear backoff.
//
// A busy iRODS server answers a new connection by closing it, which surfaces as
// "connection rejected: EOF". That happens to healthy clients under ordinary load, so one
// refusal is not evidence that the backend is down. The retry lives here rather than at
// construction because connections are established lazily, inside the first operation --
// NewFileSystem does no I/O, so there is nothing to retry at that point.
//
// Only the probe retries. Repeating an arbitrary operation is not safe: a failure that
// looks like a refused connection may be a connection dropped part way through a write,
// and this layer cannot tell the two apart. Retrying reads is worth revisiting once the
// read paths exist and can say for themselves whether they are idempotent.
func (p *Pool) probeWithRetries(ctx context.Context) error {
	// Detached from the caller once, for the whole loop including its backoff. GET /
	// gives its probe a couple of seconds, which is long enough for a healthy server and
	// short enough that ordinary latency would look like a failure and discard a working
	// session. Inheriting the caller's cancellation would also abort the wait between
	// attempts and report that cancellation as the health result rather than what iRODS
	// actually said. The cached result means this runs rarely, so a longer budget costs
	// little.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.ProbeTimeout)
	defer cancel()

	var err error

	for attempt := 0; attempt <= p.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				// Out of budget. Report what iRODS said, not the deadline.
				return err
			case <-time.After(time.Duration(attempt) * p.cfg.RetrySleep):
			}
		}

		err = p.probe(ctx)
		if err == nil || !IsUnavailable(err) {
			return err
		}
	}

	return err
}

func (p *Pool) probe(ctx context.Context) error {
	s, err := p.session(ctx, probeKey, "")
	if err != nil {
		return err
	}
	defer s.Close()

	_, err = Do(ctx, s, func(fsys *irodsfs.FileSystem) (any, error) {
		return fsys.GetServerVersion()
	})
	return err
}
