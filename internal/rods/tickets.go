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

// TicketsForPath lists the tickets issued on exactly this path.
//
// Not on its ancestors. A ticket on a collection is a grant on that collection, and it stays
// valid for everything else in it -- reporting it here would inflate a listing, and removing
// it when one file inside is deleted would revoke access to the rest.
//
// iRODS cannot be asked for the tickets on a path, so everything is listed and filtered here.
// That is fine at the DE's ticket counts; if it ever stops being fine, the filter belongs in
// the client library rather than in a cache here.
func (s *Scope) TicketsForPath(ctx context.Context, path string) ([]Ticket, error) {
	return s.ticketsMatching(ctx, path, false)
}

// TicketsUnderPath lists the tickets on a path and on everything inside it.
//
// For deletion only. Removing a collection takes its contents with it, so a ticket on
// something inside is left pointing at nothing -- iRODS does not clean those up and the
// reference does not either. Including them here is a deliberate improvement on that.
func (s *Scope) TicketsUnderPath(ctx context.Context, path string) ([]Ticket, error) {
	return s.ticketsMatching(ctx, path, true)
}

func (s *Scope) ticketsMatching(ctx context.Context, path string, includeDescendants bool) ([]Ticket, error) {
	all, err := s.Tickets(ctx)
	if err != nil {
		return nil, err
	}

	path = normalizePath(path)

	out := make([]Ticket, 0, len(all))
	for _, ticket := range all {
		on := strings.TrimRight(ticket.Path, "/")

		if on == path || (includeDescendants && strings.HasPrefix(on, path+"/")) {
			out = append(out, ticket)
		}
	}
	return out, nil
}

// GetTicket returns one ticket by name, or nil when there is none.
//
// Resolved by listing and matching rather than by asking for the one ticket. The client's
// direct lookup builds a GenQuery with a condition on the ticket-string column without
// selecting it, and iRODS 4.3.1 answers that with nothing -- so a ticket this service had
// just created came back as missing. Verified against QA: the listing finds it and the direct
// lookup does not.
//
// The cost is one query for every ticket the account owns rather than one for the ticket
// asked about. At the DE's ticket counts that is not worth a workaround with a subtler
// failure; if it becomes worth it, the fix belongs in the client library.
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
