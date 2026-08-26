package irodsclient

import (
	"context"
	"time"

	irodsfs "github.com/cyverse/go-irodsclient/fs"
	"github.com/cyverse/go-irodsclient/irods/types"
)

// Ticket is an access token for a path, as the DE reports it.
type Ticket struct {
	ID             int64
	Name           string
	Type           string
	Owner          string
	Path           string
	ExpiresAt      time.Time
	UsesLimit      int64
	UsesCount      int64
	WriteFileLimit int64
	WriteFileCount int64
	WriteByteLimit int64
	WriteByteCount int64
}

// ListTickets returns every ticket the connected account can see.
//
// iRODS has no way to ask for the tickets on one path, so callers that want those list
// everything and filter. That is the same shape the Clojure service's query had; what differs
// is that this filters client-side.
func ListTickets(ctx context.Context, s *Session) ([]Ticket, error) {
	found, err := Do(ctx, s, func(fsys *irodsfs.FileSystem) ([]*types.IRODSTicket, error) {
		return fsys.ListTickets()
	})
	if err != nil {
		return nil, err
	}

	out := make([]Ticket, 0, len(found))
	for _, ticket := range found {
		if ticket == nil {
			continue
		}
		out = append(out, convertTicket(ticket))
	}
	return out, nil
}

// DeleteTicket removes a ticket by name.
func DeleteTicket(ctx context.Context, s *Session, name string) error {
	_, err := Do(ctx, s, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.DeleteTicket(name)
	})
	return err
}

// CreateTicket issues a ticket granting an access type on a path.
func CreateTicket(ctx context.Context, s *Session, name, ticketType, path string) error {
	_, err := DoPath(ctx, s, path, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.CreateTicket(name, types.TicketType(ticketType), path)
	})
	return err
}

// SetTicketLimits applies the optional caps a ticket carries. A nil limit is left alone.
func SetTicketLimits(ctx context.Context, s *Session, name string, uses, fileWrite *int64) error {
	_, err := Do(ctx, s, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		if uses != nil {
			if err := fsys.ModifyTicketUseLimit(name, *uses); err != nil {
				return struct{}{}, err
			}
		}
		if fileWrite != nil {
			if err := fsys.ModifyTicketWriteFileLimit(name, *fileWrite); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

// AddTicketGroup allows a group to use a ticket.
func AddTicketGroup(ctx context.Context, s *Session, name, group string) error {
	_, err := Do(ctx, s, func(fsys *irodsfs.FileSystem) (struct{}, error) {
		return struct{}{}, fsys.AddTicketAllowedGroup(name, group)
	})
	return err
}

// convertTicket maps a go-irodsclient ticket onto ours.
func convertTicket(t *types.IRODSTicket) Ticket {
	return Ticket{
		ID:             t.ID,
		Name:           t.Name,
		Type:           string(t.Type),
		Owner:          t.Owner,
		Path:           t.Path,
		ExpiresAt:      t.ExpirationTime,
		UsesLimit:      t.UsesLimit,
		UsesCount:      t.UsesCount,
		WriteFileLimit: t.WriteFileLimit,
		WriteFileCount: t.WriteFileCount,
		WriteByteLimit: t.WriteByteLimit,
		WriteByteCount: t.WriteByteCount,
	}
}
