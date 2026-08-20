// Package icat reads the iRODS catalog database directly.
//
// Most catalog reads go through the iRODS protocol instead; see internal/irodsclient. This
// package exists for the queries the protocol cannot answer efficiently, which measurement
// showed is a larger set than it first appears. A per-path round trip costs about 10ms for
// a stat and 20ms for an access list, and the bulk endpoints accept up to
// max_paths_in_request paths in one request, so answering those over the protocol would
// take seconds where one query takes milliseconds. docs/irods-vs-icat-measurements.md has
// the numbers.
//
// Every query here is parameterised. The Clojure implementation this is ported from built
// several of its queries by string concatenation, including values taken straight from
// request parameters, so a path containing a quote could break out of the statement. Only
// sort columns and directions are ever interpolated, and only after a lookup in a fixed
// table.
package icat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/cyverse-de/dbutil"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq" // registers the postgres driver
)

// ErrNoSuchItem means the catalog has no collection or data object at the requested path,
// or the requesting user cannot see it. The catalog cannot tell those apart: a user with
// no access rows for an object simply gets no rows.
var ErrNoSuchItem = errors.New("no such catalog item")

// Reader is the read-only query surface. Both Store and Tx satisfy it.
type Reader interface {
	// UserGroupIDs returns the group ids a user belongs to, including their own id. Every
	// permission-filtered query is scoped by this set.
	UserGroupIDs(ctx context.Context, user, zone string) ([]int64, error)

	// GetItems returns catalog rows for the given absolute paths, in one query. Paths the
	// user cannot see are simply absent from the result.
	GetItems(ctx context.Context, q ItemQuery) ([]Row, error)

	// PagedFolder returns a sorted page of a collection's immediate children.
	PagedFolder(ctx context.Context, q ListingQuery) ([]ListingRow, error)

	// LookupUser reports what kind of account a name refers to, or UserKindNone if the
	// zone has no such account.
	LookupUser(ctx context.Context, user, zone string) (UserKind, error)

	// PathsForUUIDs resolves data ids to the paths carrying them.
	//
	// Like PermsForItems this is not scoped to a user: resolving an id is a lookup, and
	// the caller checks what the user may do with the path afterwards.
	PathsForUUIDs(ctx context.Context, uuids []string) ([]UUIDPath, error)

	// CountChildren returns how many files and subfolders a collection holds, counting
	// only what the requesting user can see.
	CountChildren(ctx context.Context, q ChildCountQuery) (ChildCounts, error)

	// CountChildrenBatch returns child counts for many collections in one query.
	CountChildrenBatch(ctx context.Context, q BatchChildCountQuery) ([]PathChildCounts, error)

	// PermsForItems returns the access entries on the given absolute paths, in one query.
	//
	// Unlike GetItems this is NOT scoped to a requesting user: it returns the complete
	// access list of every path it is given, to whoever asks. That is what the endpoints
	// built on it need -- they report who a path is shared with -- but it means the
	// caller owns the authorisation check. Handlers must confirm the requesting user may
	// see a path before handing it here, or they will disclose its ACL.
	PermsForItems(ctx context.Context, paths []string) ([]Perm, error)
}

// Store is a connection to the catalog.
type Store interface {
	Reader

	// WithTx runs fn inside a read-only transaction. Queries that build temporary tables
	// need one, since those tables are dropped at commit.
	WithTx(ctx context.Context, fn func(Tx) error) error

	Ping(ctx context.Context) error
	Close() error
}

// Tx is a query surface bound to a transaction.
type Tx interface {
	Reader
}

var _ Store = (*PGStore)(nil)

// PGStore is the PostgreSQL implementation of Store.
type PGStore struct {
	db *sqlx.DB
}

// Config describes the catalog connection.
type Config struct {
	// URI is a libpq connection string.
	URI string

	// MaxOpenConns bounds concurrent queries. The catalog is shared with the iRODS
	// servers themselves, so this is deliberately modest; the DE's other catalog readers
	// use ten.
	MaxOpenConns int

	// ConnMaxIdleTime closes connections that have gone unused.
	ConnMaxIdleTime time.Duration

	// ConnectTimeout bounds the initial connection attempt, which retries internally.
	ConnectTimeout time.Duration
}

// Defaults for Config.
const (
	DefaultMaxOpenConns    = 10
	DefaultConnMaxIdleTime = time.Minute
	DefaultConnectTimeout  = time.Minute
)

// Open connects to the catalog. It retries while the database is unreachable, so a service
// started before its database is up will still come up.
func Open(cfg Config) (*PGStore, error) {
	if cfg.URI == "" {
		return nil, fmt.Errorf("icat: a connection URI is required")
	}
	if cfg.MaxOpenConns <= 0 {
		cfg.MaxOpenConns = DefaultMaxOpenConns
	}
	if cfg.ConnMaxIdleTime <= 0 {
		cfg.ConnMaxIdleTime = DefaultConnMaxIdleTime
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}

	connector, err := dbutil.NewDefaultConnector(cfg.ConnectTimeout.String())
	if err != nil {
		return nil, fmt.Errorf("icat: building the connector: %w", err)
	}

	// dbutil opens through otelsql, so every query is traced without further wiring.
	db, err := connector.Connect("postgres", cfg.URI)
	if err != nil {
		return nil, fmt.Errorf("icat: connecting: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	// Without this the idle limit stays at Go's default of two, so a burst of bulk
	// queries opens ten connections and immediately tears eight of them down -- including
	// the TLS handshake when sslmode is not disable. Keeping the two limits equal means
	// the pool actually holds what it was sized for.
	db.SetMaxIdleConns(cfg.MaxOpenConns)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	return &PGStore{db: sqlx.NewDb(db, "postgres")}, nil
}

// Ping reports whether the catalog is reachable.
func (s *PGStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close releases the connection pool.
func (s *PGStore) Close() error { return s.db.Close() }

// WithTx runs fn inside a read-only transaction, rolling back on error.
//
// Read-only is not decoration. This service never writes to the catalog -- iRODS owns it,
// and writing behind the server's back would corrupt its own caches -- so the transaction
// declares that and lets PostgreSQL enforce it.
func (s *PGStore) WithTx(ctx context.Context, fn func(Tx) error) error {
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("icat: beginning a transaction: %w", err)
	}

	if err := fn(&pgTx{tx: tx}); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("icat: rolling back: %w", rbErr))
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("icat: committing: %w", err)
	}
	return nil
}

// queryer is the subset of sqlx both a database and a transaction provide.
type queryer interface {
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
	GetContext(ctx context.Context, dest any, query string, args ...any) error
}

func (s *PGStore) queryer() queryer { return s.db }

// pgTx is a Reader bound to a transaction.
type pgTx struct {
	tx *sqlx.Tx
}

func (t *pgTx) queryer() queryer { return t.tx }

var _ Tx = (*pgTx)(nil)
