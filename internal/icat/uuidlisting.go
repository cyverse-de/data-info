package icat

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

//go:embed sql/paged_uuid_listing.sql
var sqlPagedUUIDs string

//go:embed sql/count_uuids.sql
var sqlCountUUIDs string

// UUIDListingQuery selects a page of the items carrying a set of data ids.
//
// It is deliberately not ListingQuery: that one is scoped by a parent collection and this
// one by a set of ids, and the two have no path in common. Sharing a type would leave a
// field on each that the other must never set.
type UUIDListingQuery struct {
	UUIDs []string
	User  string
	Zone  string

	GroupIDs []int64

	// InfoTypes filters data objects by info type, and IncludeUnknownInfoType keeps those
	// that have none. They are independent for the same reason as in ListingQuery: asking
	// for only untyped objects is a real request.
	InfoTypes              []string
	IncludeUnknownInfoType bool

	InfoTypeAttribute string

	SortColumn    SortColumn
	SortDirection SortDirection

	Limit  int
	Offset int
}

// PagedUUIDs returns a sorted page of the items carrying the given data ids.
func (s *PGStore) PagedUUIDs(ctx context.Context, q UUIDListingQuery) ([]ListingRow, error) {
	return pagedUUIDs(ctx, s.queryer(), q)
}

// PagedUUIDs returns a sorted page of the items carrying the given data ids.
func (t *pgTx) PagedUUIDs(ctx context.Context, q UUIDListingQuery) ([]ListingRow, error) {
	return pagedUUIDs(ctx, t.queryer(), q)
}

func pagedUUIDs(ctx context.Context, qr queryer, q UUIDListingQuery) ([]ListingRow, error) {
	// An empty set is answered without a query, as the reference does. The statement would
	// return nothing anyway, but only after the catalog had built the union.
	if len(q.UUIDs) == 0 {
		return nil, nil
	}
	if len(q.GroupIDs) == 0 && (q.User == "" || q.Zone == "") {
		return nil, fmt.Errorf("icat: a user and zone are required when resolving groups")
	}
	if q.Limit <= 0 {
		return nil, fmt.Errorf("icat: a positive limit is required, got %d", q.Limit)
	}
	if q.Offset < 0 {
		return nil, fmt.Errorf("icat: a non-negative offset is required, got %d", q.Offset)
	}

	column := q.SortColumn
	if column == "" {
		column = DefaultSortColumn
	}
	// Guard the interpolation, as paged_folder.sql does: the sort column and direction are
	// identifiers and cannot be parameters, so a value that did not come from the fixed
	// table must never reach the statement.
	if !isKnownSortColumn(column) {
		return nil, fmt.Errorf("icat: %q is not a sortable column", column)
	}
	direction := q.SortDirection
	if direction != SortDescending {
		direction = SortAscending
	}

	statement := fmt.Sprintf(sqlPagedUUIDs, column, direction)

	var rows []ListingRow
	err := qr.SelectContext(ctx, &rows, statement,
		pq.Array(q.UUIDs), q.User, q.Zone, groupIDArray(q.GroupIDs),
		infoTypeArray(q.InfoTypes), q.IncludeUnknownInfoType,
		q.Limit, q.Offset, infoTypeAttributeOr(q.InfoTypeAttribute))
	if err != nil {
		return nil, fmt.Errorf("icat: listing %d data ids: %w", len(q.UUIDs), err)
	}
	return rows, nil
}

// CountUUIDs returns how many of the given data ids name something the user can see.
func (s *PGStore) CountUUIDs(ctx context.Context, q UUIDListingQuery) (int64, error) {
	return countUUIDs(ctx, s.queryer(), q)
}

// CountUUIDs returns how many of the given data ids name something the user can see.
func (t *pgTx) CountUUIDs(ctx context.Context, q UUIDListingQuery) (int64, error) {
	return countUUIDs(ctx, t.queryer(), q)
}

func countUUIDs(ctx context.Context, qr queryer, q UUIDListingQuery) (int64, error) {
	if len(q.UUIDs) == 0 {
		return 0, nil
	}
	if len(q.GroupIDs) == 0 && (q.User == "" || q.Zone == "") {
		return 0, fmt.Errorf("icat: a user and zone are required when resolving groups")
	}

	var total int64
	err := qr.GetContext(ctx, &total, sqlCountUUIDs,
		pq.Array(q.UUIDs), q.User, q.Zone, groupIDArray(q.GroupIDs),
		infoTypeArray(q.InfoTypes), q.IncludeUnknownInfoType,
		infoTypeAttributeOr(q.InfoTypeAttribute))
	if err != nil {
		return 0, fmt.Errorf("icat: counting %d data ids: %w", len(q.UUIDs), err)
	}
	return total, nil
}

// groupIDArray passes group ids as an array, or NULL to have the query derive them.
func groupIDArray(ids []int64) any {
	if len(ids) == 0 {
		return nil
	}
	return pq.Array(ids)
}

// infoTypeArray passes info types as a lower-cased array, or NULL for no filtering.
func infoTypeArray(types []string) any {
	if len(types) == 0 {
		return nil
	}
	lowered := make([]string, 0, len(types))
	for _, t := range types {
		lowered = append(lowered, strings.ToLower(t))
	}
	return pq.Array(lowered)
}

// infoTypeAttributeOr supplies the default info-type attribute when none is configured.
func infoTypeAttributeOr(attribute string) string {
	if attribute == "" {
		return DefaultInfoTypeAttribute
	}
	return attribute
}
