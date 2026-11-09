package rods

import (
	"context"
	"strings"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/irodsclient"
)

// Ticket is an access token for a path.
type Ticket = irodsclient.Ticket

// TicketsUnder lists the tickets on the given paths and on anything inside them, for a
// delete: iRODS leaves the tickets on removed objects behind. Like the other ticket lookups it
// reads the catalog, so it is not scoped to the requesting account.
func (s *Scope) TicketsUnder(ctx context.Context, paths []string) ([]Ticket, error) {
	return s.ticketsOn(ctx, paths, true)
}

// TicketsForPath lists the tickets issued on exactly this path, not its ancestors: a
// collection's ticket still covers the rest of the collection, so deleting one file must
// not remove it.
func (s *Scope) TicketsForPath(ctx context.Context, path string) ([]Ticket, error) {
	return s.ticketsOn(ctx, []string{path}, false)
}

// ticketsOn asks the catalog for the tickets on paths rather than listing every ticket the
// account can see and filtering: for the proxy account that is every ticket in the zone,
// which took minutes and over a gigabyte of memory per listing.
func (s *Scope) ticketsOn(ctx context.Context, paths []string, descendants bool) ([]Ticket, error) {
	rows, err := s.deps.ICAT.TicketsOnPaths(ctx, icat.TicketQuery{
		Paths:       normalizePaths(paths),
		Descendants: descendants,
	})
	if err != nil {
		return nil, err
	}

	out := make([]Ticket, 0, len(rows))
	for _, row := range rows {
		out = append(out, ticketOf(row))
	}
	return out, nil
}

// TicketsWithin picks from a TicketsUnder listing the ones on path and everything inside it,
// so that a bulk delete queries once.
func TicketsWithin(all []Ticket, path string) []Ticket {
	path = normalizePath(path)

	out := make([]Ticket, 0, len(all))
	for _, ticket := range all {
		on := strings.TrimRight(ticket.Path, "/")
		if on == path || strings.HasPrefix(on, path+"/") {
			out = append(out, ticket)
		}
	}
	return out
}

// GetTicket returns one ticket by name, or nil when there is none.
func (s *Scope) GetTicket(ctx context.Context, name string) (*Ticket, error) {
	row, err := s.deps.ICAT.TicketByName(ctx, name)
	if err != nil || row == nil {
		return nil, err
	}
	ticket := ticketOf(*row)
	return &ticket, nil
}

func ticketOf(row icat.Ticket) Ticket {
	return Ticket{
		ID:             row.ID,
		Name:           row.Name,
		Type:           row.Type,
		Owner:          row.Owner,
		Path:           row.Path,
		ExpiresAt:      row.Expires(),
		UsesLimit:      row.UsesLimit,
		UsesCount:      row.UsesCount,
		WriteFileLimit: row.WriteFileLimit,
		WriteFileCount: row.WriteFileCount,
		WriteByteLimit: row.WriteByteLimit,
		WriteByteCount: row.WriteByteCount,
	}
}

// CreateTicket issues a ticket granting an access type on a path.
func (s *Scope) CreateTicket(ctx context.Context, name, ticketType, path string) error {
	sess, err := s.session(ctx)
	if err != nil {
		return err
	}
	return irodsclient.CreateTicket(ctx, sess, name, ticketType, normalizePath(path))
}

// TicketLimits are the optional caps a ticket can carry. iRODS sets them after creation, so
// if setting them fails the caller removes the ticket rather than leave it less restricted.
type TicketLimits struct {
	Uses      *int64
	FileWrite *int64
}

// SetTicketLimits applies the caps a ticket was asked for.
func (s *Scope) SetTicketLimits(ctx context.Context, name string, limits TicketLimits) error {
	sess, err := s.session(ctx)
	if err != nil {
		return err
	}
	return irodsclient.SetTicketLimits(ctx, sess, name, limits.Uses, limits.FileWrite)
}

// PublicizeTicket lets anyone with the ticket use it, by allowing the public group.
func (s *Scope) PublicizeTicket(ctx context.Context, name string) error {
	sess, err := s.session(ctx)
	if err != nil {
		return err
	}
	return irodsclient.AddTicketGroup(ctx, sess, name, PublicGroup)
}

// PublicGroup is the iRODS group that stands for "anyone".
const PublicGroup = "public"

// DeleteTicket removes a ticket by name.
func (s *Scope) DeleteTicket(ctx context.Context, name string) error {
	sess, err := s.session(ctx)
	if err != nil {
		return err
	}
	return irodsclient.DeleteTicket(ctx, sess, name)
}
