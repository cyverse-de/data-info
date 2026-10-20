package irodsclient

import (
	"io"
	"time"

	"github.com/sirupsen/logrus"
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

	// MaxSessions bounds how many distinct client users are held open at once. Sessions
	// in use are never evicted, so it is a target rather than a ceiling.
	MaxSessions int

	// MaxTotalConnections is the ceiling on iRODS connections this replica holds open
	// across every session. A call that would need another one waits until there is room.
	MaxTotalConnections int

	// IdleTimeout evicts a session that has gone unused, and closes a connection that has
	// sat idle inside a session still in use.
	IdleTimeout time.Duration

	// MaxConnections bounds one session's metadata connections, which makes it the per-user
	// concurrency limit: calls beyond it share connections and serialise rather than fail.
	MaxConnections int

	// MaxIOConnections bounds the connections used for file transfer.
	MaxIOConnections int

	// ProbeTimeout bounds one health check, independently of the caller's deadline, because
	// a probe cut short poisons the admin session it shares.
	ProbeTimeout time.Duration

	// ProbeCacheTTL is how long a health-check result is reused. GET / reports iRODS
	// health on every call and takes no authentication, so probing it every time would
	// let an unauthenticated caller drive iRODS load.
	ProbeCacheTTL time.Duration

	// MaxRetries and RetrySleep govern the health probe's retries of a refused connection;
	// a busy iRODS server refuses new connections even from healthy clients.
	MaxRetries int
	RetrySleep time.Duration

	// Log receives reports of calls that had to wait for a connection. Nil discards them.
	Log *logrus.Entry

	// LibraryLog receives go-irodsclient's own logging. Nil leaves it on logrus's global logger.
	LibraryLog *logrus.Entry

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

// Defaults for Config. The operation timeouts stay near go-irodsclient's own so an abandoned
// call unwinds quickly; listings run under the long timeout, so it is kept modest too.
const (
	DefaultMaxSessions            = 32
	DefaultMaxTotalConnections    = 8
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
	if c.MaxTotalConnections <= 0 {
		c.MaxTotalConnections = DefaultMaxTotalConnections
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
	if c.Log == nil {
		discard := logrus.New()
		discard.SetOutput(io.Discard)
		c.Log = logrus.NewEntry(discard)
	}
}
