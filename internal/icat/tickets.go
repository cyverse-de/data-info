package icat

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

var (
	//go:embed sql/tickets_on_paths.sql
	sqlTicketsOnPaths string

	//go:embed sql/ticket_by_name.sql
	sqlTicketByName string
)

// Ticket is a ticket as the catalog records it.
type Ticket struct {
	ID             int64          `db:"ticket_id"`
	Name           string         `db:"ticket_string"`
	Type           string         `db:"ticket_type"`
	Owner          string         `db:"owner"`
	Path           string         `db:"path"`
	ExpiryTS       sql.NullString `db:"ticket_expiry_ts"`
	UsesLimit      int64          `db:"uses_limit"`
	UsesCount      int64          `db:"uses_count"`
	WriteFileLimit int64          `db:"write_file_limit"`
	WriteFileCount int64          `db:"write_file_count"`
	WriteByteLimit int64          `db:"write_byte_limit"`
	WriteByteCount int64          `db:"write_byte_count"`
}

// Expires is when the ticket stops working, or the zero time for one that never does, which
// the catalog records as an empty or non-positive value.
func (t Ticket) Expires() time.Time {
	seconds, err := strconv.ParseInt(strings.TrimSpace(t.ExpiryTS.String), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

// TicketQuery selects the tickets issued on a set of paths.
type TicketQuery struct {
	Paths []string

	// Descendants also selects tickets on anything inside the paths, for a delete: iRODS
	// leaves the tickets on removed objects behind.
	Descendants bool
}

// TicketsOnPaths implements Reader.
func (s *PGStore) TicketsOnPaths(ctx context.Context, q TicketQuery) ([]Ticket, error) {
	return ticketsOnPaths(ctx, s.queryer(), q)
}

// TicketsOnPaths implements Reader.
func (t *pgTx) TicketsOnPaths(ctx context.Context, q TicketQuery) ([]Ticket, error) {
	return ticketsOnPaths(ctx, t.queryer(), q)
}

func ticketsOnPaths(ctx context.Context, qr queryer, q TicketQuery) ([]Ticket, error) {
	if len(q.Paths) == 0 {
		return nil, nil
	}

	dirnames, basenames, err := splitPaths(q.Paths)
	if err != nil {
		return nil, err
	}

	var tickets []Ticket
	err = qr.SelectContext(ctx, &tickets, sqlTicketsOnPaths,
		pq.Array(q.Paths), pq.Array(dirnames), pq.Array(basenames), q.Descendants)
	if err != nil {
		return nil, queryError(err, "listing the tickets on %d path(s)", len(q.Paths))
	}
	return tickets, nil
}

// TicketByName implements Reader.
func (s *PGStore) TicketByName(ctx context.Context, name string) (*Ticket, error) {
	return ticketByName(ctx, s.queryer(), name)
}

// TicketByName implements Reader.
func (t *pgTx) TicketByName(ctx context.Context, name string) (*Ticket, error) {
	return ticketByName(ctx, t.queryer(), name)
}

func ticketByName(ctx context.Context, qr queryer, name string) (*Ticket, error) {
	var ticket Ticket
	err := qr.GetContext(ctx, &ticket, sqlTicketByName, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, queryError(err, "looking up ticket %q", name)
	}
	return &ticket, nil
}
