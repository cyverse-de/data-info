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
type UUIDListingQuery struct {
	UUIDs []string
	User  string
	Zone  string

	GroupIDs []int64

	// InfoTypes filters data objects by info type; IncludeUnknownInfoType independently keeps
	// those with none, as in ListingQuery.
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
	// Skip the query: the catalog would build the whole union only to return nothing.
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
	// The sort column and direction are interpolated identifiers; see pagedFolder.
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
		return nil, queryError(err, "listing %d data ids", len(q.UUIDs))
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
		return 0, queryError(err, "counting %d data ids", len(q.UUIDs))
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
