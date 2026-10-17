// Package icat queries the iRODS catalog database directly, for the bulk and listing reads
// the iRODS protocol (internal/irodsclient) cannot answer efficiently: a bulk request may
// name as many paths as the maxpaths setting allows, and each protocol round trip costs
// 10-20ms (see docs/irods-vs-icat-measurements.md). Every request value is a bind
// parameter; only sort columns and directions are interpolated, and only from a fixed table.
package icat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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

	// FoldersInFolder returns every subfolder of a collection, unpaged.
	FoldersInFolder(ctx context.Context, q ListingQuery) ([]ListingRow, error)

	// PagedUUIDs returns a sorted page of the items carrying a set of data ids, collections
	// and data objects paged together.
	PagedUUIDs(ctx context.Context, q UUIDListingQuery) ([]ListingRow, error)

	// CountUUIDs returns how many of a set of data ids name something the user can see,
	// which is the total reported alongside a page from PagedUUIDs.
	CountUUIDs(ctx context.Context, q UUIDListingQuery) (int64, error)

	// LookupUser reports what kind of account a name refers to, or UserKindNone if the
	// zone has no such account.
	LookupUser(ctx context.Context, user, zone string) (UserKind, error)

	// UserGroupNames returns the names of the groups a user belongs to, excluding the
	// group of one that iRODS records for the user themselves.
	UserGroupNames(ctx context.Context, user, zone string) ([]string, error)

	// PathsForUUIDs resolves data ids to paths. It is not scoped to a user; callers check
	// access to the paths afterwards.
	PathsForUUIDs(ctx context.Context, uuids []string) ([]UUIDPath, error)

	// CountChildren returns how many files and subfolders a collection holds, counting
	// only what the requesting user can see.
	CountChildren(ctx context.Context, q ChildCountQuery) (ChildCounts, error)

	// CountChildrenBatch returns child counts for many collections in one query.
	CountChildrenBatch(ctx context.Context, q BatchChildCountQuery) ([]PathChildCounts, error)

	// PermsForItems returns the full access list of each path and is NOT scoped to a user.
	// Callers must confirm the requester may see a path first, or they will disclose its ACL.
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

// maxConnectDelay caps the backoff between connection attempts at startup.
const maxConnectDelay = 10 * time.Second

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

	db, err := sql.Open("postgres", cfg.URI)
	if err != nil {
		return nil, fmt.Errorf("icat: opening: %w", err)
	}
	if err := pingUntil(db, cfg.ConnectTimeout); err != nil {
		pingErr := fmt.Errorf("icat: the catalog did not answer within %s; "+
			"check that the database is up and the connection URI is right: %w", cfg.ConnectTimeout, err)
		return nil, errors.Join(pingErr, db.Close())
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	// Go's default idle limit of two would close most connections after every burst, only
	// to reopen them (with a TLS handshake) on the next.
	db.SetMaxIdleConns(cfg.MaxOpenConns)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	return &PGStore{db: sqlx.NewDb(db, "postgres")}, nil
}

// pingUntil pings the database, backing off between attempts, until it answers or timeout
// passes.
func pingUntil(db *sql.DB, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	delay := time.Second
	for {
		err := db.PingContext(ctx)
		if err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		delay = min(delay*2, maxConnectDelay)
	}
}

// Ping reports whether the catalog is reachable.
func (s *PGStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close releases the connection pool.
func (s *PGStore) Close() error { return s.db.Close() }

// WithTx runs fn inside a read-only transaction, rolling back on error. iRODS owns the
// catalog and caches it, so PostgreSQL is asked to enforce that this service never writes.
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
