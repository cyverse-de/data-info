package rods

import (
	"context"
	"strings"

	"github.com/cyverse-de/data-info/internal/irodsclient"
)

// Ticket is an access token for a path.
type Ticket = irodsclient.Ticket

// Tickets lists every ticket the requesting account can see.
func (s *Scope) Tickets(ctx context.Context) ([]Ticket, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}
	return irodsclient.ListTickets(ctx, sess)
}

// TicketsForPath lists the tickets issued on exactly this path, not its ancestors: a
// collection's ticket still covers the rest of the collection, so deleting one file must
// not remove it.
func (s *Scope) TicketsForPath(ctx context.Context, path string) ([]Ticket, error) {
	return s.ticketsMatching(ctx, path, false)
}

// TicketsUnder picks from a Tickets listing the ones on path and everything inside it, for
// deletion, since iRODS leaves tickets on removed descendants dangling. It takes a listing
// so that a bulk delete lists once.
func TicketsUnder(all []Ticket, path string) []Ticket {
	return matchTickets(all, path, true)
}

func (s *Scope) ticketsMatching(ctx context.Context, path string, includeDescendants bool) ([]Ticket, error) {
	all, err := s.Tickets(ctx)
	if err != nil {
		return nil, err
	}
	return matchTickets(all, path, includeDescendants), nil
}

func matchTickets(all []Ticket, path string, includeDescendants bool) []Ticket {
	path = normalizePath(path)

	out := make([]Ticket, 0, len(all))
	for _, ticket := range all {
		on := strings.TrimRight(ticket.Path, "/")

		if on == path || (includeDescendants && strings.HasPrefix(on, path+"/")) {
			out = append(out, ticket)
		}
	}
	return out
}

// GetTicket returns one ticket by name, or nil when there is none. It lists and matches
// because go-irodsclient's direct lookup filters on the ticket-string column without
// selecting it, which iRODS 4.3.1 answers with nothing, even for a ticket just created.
func (s *Scope) GetTicket(ctx context.Context, name string) (*Ticket, error) {
	all, err := s.Tickets(ctx)
	if err != nil {
		return nil, err
	}

	for _, ticket := range all {
		if ticket.Name == name {
			return &ticket, nil
		}
	}
	return nil, nil
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
