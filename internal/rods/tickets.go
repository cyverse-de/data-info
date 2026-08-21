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

// TicketsForPath lists the tickets granting access to a path or anything under it.
//
// iRODS cannot be asked this directly, so everything is listed and filtered here. That is
// fine at the DE's ticket counts and it is what the callers need; if it ever stops being
// fine, the filter belongs in the client library rather than in a cache here.
func (s *Scope) TicketsForPath(ctx context.Context, path string) ([]Ticket, error) {
	all, err := s.Tickets(ctx)
	if err != nil {
		return nil, err
	}

	path = normalizePath(path)

	out := make([]Ticket, 0, len(all))
	for _, ticket := range all {
		on := strings.TrimRight(ticket.Path, "/")
		// A ticket on a collection covers what is inside it, so deleting that collection
		// invalidates the ticket just as surely as deleting the object it named.
		if on == path || strings.HasPrefix(path, on+"/") || strings.HasPrefix(on, path+"/") {
			out = append(out, ticket)
		}
	}
	return out, nil
}

// GetTicket returns one ticket by name, or nil when there is none.
func (s *Scope) GetTicket(ctx context.Context, name string) (*Ticket, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, err
	}
	return irodsclient.GetTicket(ctx, sess, name)
}

// CreateTicket issues a ticket granting an access type on a path.
func (s *Scope) CreateTicket(ctx context.Context, name, ticketType, path string) error {
	sess, err := s.session(ctx)
	if err != nil {
		return err
	}
	return irodsclient.CreateTicket(ctx, sess, name, ticketType, normalizePath(path))
}

// TicketLimits are the optional caps a ticket can carry.
//
// iRODS sets them after the ticket exists rather than at creation, so a failure part-way
// leaves a ticket that works but is less restricted than asked for -- which is why the caller
// removes it rather than reporting partial success.
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
