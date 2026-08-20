package icat

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

//go:embed sql/paged_folder.sql
var sqlPagedFolder string

// SortColumn names a column a listing may be ordered by.
type SortColumn string

// sortColumns maps the sort-field vocabulary onto catalog columns.
//
// Both spellings are accepted, matching resolve-sort-field: the API's names, and the raw
// column names that clj-icat-direct also allowed through.
var sortColumns = map[string]SortColumn{
	"datecreated":  "create_ts",
	"datemodified": "modify_ts",
	"name":         "base_name",
	"path":         "full_path",
	"size":         "data_size",
	"create_ts":    "create_ts",
	"modify_ts":    "modify_ts",
	"base_name":    "base_name",
	"full_path":    "full_path",
	"data_size":    "data_size",
}

// EntityType selects what a listing contains.
type EntityType string

// The entity types. EntityAny is the default.
const (
	EntityAny    EntityType = "any"
	EntityFile   EntityType = "file"
	EntityFolder EntityType = "folder"
)

// ResolveEntityType maps an entity-type parameter onto a type, defaulting to any.
func ResolveEntityType(value string) (EntityType, error) {
	switch EntityType(strings.ToLower(strings.TrimSpace(value))) {
	case "", EntityAny:
		return EntityAny, nil
	case EntityFile:
		return EntityFile, nil
	case EntityFolder:
		return EntityFolder, nil
	default:
		return "", fmt.Errorf("icat: %q is not an entity type", value)
	}
}

// DefaultSortColumn is what an unspecified sort-field means.
const DefaultSortColumn SortColumn = "base_name"

// SortDirection is the order a listing is returned in.
type SortDirection string

// The sort directions.
const (
	SortAscending  SortDirection = "ASC"
	SortDescending SortDirection = "DESC"
)

// ResolveSortColumn maps a sort-field parameter onto a column.
//
// An unrecognised value is an error rather than a silent default. The reference lets it
// reach a bare exception and answers 500, which is recorded as a deferred fix; reporting it
// here lets the caller decide which to produce.
func ResolveSortColumn(field string) (SortColumn, error) {
	if strings.TrimSpace(field) == "" {
		return DefaultSortColumn, nil
	}
	if column, ok := sortColumns[strings.ToLower(strings.TrimSpace(field))]; ok {
		return column, nil
	}
	return "", fmt.Errorf("icat: %q is not a sortable field", field)
}

// ResolveSortDirection maps a sort-dir parameter onto a direction.
//
// An unrecognised value means ascending, silently. That is what resolve-sort-dir does -- its
// case has a default -- and callers rely on lowercase asc working.
func ResolveSortDirection(dir string) SortDirection {
	if strings.EqualFold(strings.TrimSpace(dir), "desc") {
		return SortDescending
	}
	return SortAscending
}

// ListingQuery selects a page of a collection's children.
type ListingQuery struct {
	Path string
	User string
	Zone string

	GroupIDs []int64

	// InfoTypes filters data objects by info type. Empty means no filtering.
	InfoTypes []string

	// IncludeUnknownInfoType keeps objects that have no info type. It is independent of
	// InfoTypes: asking for only untyped objects is a real request, spelled in the
	// reference by naming "unknown" and nothing else, and it must not be read as no
	// filtering at all.
	IncludeUnknownInfoType bool

	// EntityType selects what the listing contains.
	EntityType EntityType

	InfoTypeAttribute string

	SortColumn    SortColumn
	SortDirection SortDirection

	Limit  int
	Offset int
}

// ListingRow is one entry of a listing. It carries the same columns as Row plus the total.
type ListingRow = Row

// PagedFolder returns a sorted page of a collection's children.
func (s *PGStore) PagedFolder(ctx context.Context, q ListingQuery) ([]ListingRow, error) {
	return pagedFolder(ctx, s.queryer(), q)
}

// PagedFolder returns a sorted page of a collection's children.
func (t *pgTx) PagedFolder(ctx context.Context, q ListingQuery) ([]ListingRow, error) {
	return pagedFolder(ctx, t.queryer(), q)
}

func pagedFolder(ctx context.Context, qr queryer, q ListingQuery) ([]ListingRow, error) {
	if q.Path == "" {
		return nil, fmt.Errorf("icat: a path is required")
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
	// Guard the interpolation. These two are identifiers, so they cannot be parameters;
	// everything else in the query is. A value that did not come from the table above must
	// never reach the statement.
	if !isKnownSortColumn(column) {
		return nil, fmt.Errorf("icat: %q is not a sortable column", column)
	}
	direction := q.SortDirection
	if direction != SortDescending {
		direction = SortAscending
	}

	var infoTypes any
	if len(q.InfoTypes) > 0 {
		lowered := make([]string, 0, len(q.InfoTypes))
		for _, t := range q.InfoTypes {
			lowered = append(lowered, strings.ToLower(t))
		}
		infoTypes = pq.Array(lowered)
	}

	var groupIDs any
	if len(q.GroupIDs) > 0 {
		groupIDs = pq.Array(q.GroupIDs)
	}

	attribute := q.InfoTypeAttribute
	if attribute == "" {
		attribute = DefaultInfoTypeAttribute
	}

	entityType := q.EntityType
	if entityType == "" {
		entityType = EntityAny
	}

	statement := fmt.Sprintf(sqlPagedFolder, column, direction)

	var rows []ListingRow
	err := qr.SelectContext(ctx, &rows, statement,
		strings.TrimRight(q.Path, "/"), q.User, q.Zone, groupIDs,
		infoTypes, q.IncludeUnknownInfoType, q.Limit, q.Offset, attribute, string(entityType))
	if err != nil {
		return nil, fmt.Errorf("icat: listing %q: %w", q.Path, err)
	}
	return rows, nil
}

// isKnownSortColumn reports whether a column came from the table above.
func isKnownSortColumn(column SortColumn) bool {
	for _, known := range sortColumns {
		if known == column {
			return true
		}
	}
	return false
}

//go:embed sql/folders_in_folder.sql
var sqlFoldersInFolder string

// FoldersInFolder returns every subfolder of a collection, unpaged.
func (s *PGStore) FoldersInFolder(ctx context.Context, q ListingQuery) ([]ListingRow, error) {
	return foldersInFolder(ctx, s.queryer(), q)
}

// FoldersInFolder returns every subfolder of a collection, unpaged.
func (t *pgTx) FoldersInFolder(ctx context.Context, q ListingQuery) ([]ListingRow, error) {
	return foldersInFolder(ctx, t.queryer(), q)
}

func foldersInFolder(ctx context.Context, qr queryer, q ListingQuery) ([]ListingRow, error) {
	if q.Path == "" {
		return nil, fmt.Errorf("icat: a path is required")
	}
	if len(q.GroupIDs) == 0 && (q.User == "" || q.Zone == "") {
		return nil, fmt.Errorf("icat: a user and zone are required when resolving groups")
	}

	var groupIDs any
	if len(q.GroupIDs) > 0 {
		groupIDs = pq.Array(q.GroupIDs)
	}

	var rows []ListingRow
	err := qr.SelectContext(ctx, &rows, sqlFoldersInFolder,
		strings.TrimRight(q.Path, "/"), q.User, q.Zone, groupIDs)
	if err != nil {
		return nil, fmt.Errorf("icat: listing subfolders of %q: %w", q.Path, err)
	}
	return rows, nil
}
