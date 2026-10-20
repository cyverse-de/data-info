package icat

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed sql/paged_folder.sql
var sqlPagedFolder string

// SortColumn names a column a listing may be ordered by.
type SortColumn string

// sortColumns maps the API's sort-field names onto catalog columns. Raw column names are
// deliberately absent, so ResolveSortColumn rejects a request that names one.
var sortColumns = map[string]SortColumn{
	"datecreated":  "create_ts",
	"datemodified": "modify_ts",
	"name":         "base_name",
	"path":         "full_path",
	"size":         "data_size",
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

// ResolveSortColumn maps a sort-field parameter onto a column. An unrecognised value is an
// error, not a silent default, so the caller chooses the status (docs/deferred-fixes.md #3).
func ResolveSortColumn(field string) (SortColumn, error) {
	if strings.TrimSpace(field) == "" {
		return DefaultSortColumn, nil
	}
	if column, ok := sortColumns[strings.ToLower(strings.TrimSpace(field))]; ok {
		return column, nil
	}
	return "", fmt.Errorf("icat: %q is not a sortable field", field)
}

// ResolveSortDirection maps a sort-dir parameter onto a direction, defaulting to ascending.
// Only upper-case ASC and DESC are accepted, as the Clojure service's parameter schema did.
func ResolveSortDirection(dir string) (SortDirection, error) {
	switch SortDirection(dir) {
	case "", SortAscending:
		return SortAscending, nil
	case SortDescending:
		return SortDescending, nil
	default:
		return "", fmt.Errorf("icat: %q is not a sort direction", dir)
	}
}

// ListingQuery selects a page of a collection's children.
type ListingQuery struct {
	Path string
	User string
	Zone string

	GroupIDs []int64

	// InfoTypes filters data objects by info type. Empty means no filtering.
	InfoTypes []string

	// IncludeUnknownInfoType keeps objects with no info type. It is independent of InfoTypes,
	// so a request for only untyped objects is not read as no filter.
	IncludeUnknownInfoType bool

	// EntityType selects what the listing contains.
	EntityType EntityType

	InfoTypeAttribute string

	SortColumn    SortColumn
	SortDirection SortDirection

	Limit  int
	Offset int
}

// ListingRow is a Row returned by a listing query.
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
	// A negative limit means every row, for the path-list endpoint's whole-tree walk.
	var limit any = q.Limit
	switch {
	case q.Limit < 0:
		limit = nil
	case q.Limit == 0:
		return nil, fmt.Errorf("icat: a positive limit is required, or a negative one for no limit")
	}
	if q.Offset < 0 {
		return nil, fmt.Errorf("icat: a non-negative offset is required, got %d", q.Offset)
	}

	column := q.SortColumn
	if column == "" {
		column = DefaultSortColumn
	}
	// The sort column and direction are identifiers, so they are interpolated rather than
	// bound; only values from sortColumns may reach the statement.
	if !isKnownSortColumn(column) {
		return nil, fmt.Errorf("icat: %q is not a sortable column", column)
	}
	direction := q.SortDirection
	if direction != SortDescending {
		direction = SortAscending
	}

	entityType := q.EntityType
	if entityType == "" {
		entityType = EntityAny
	}

	statement := fmt.Sprintf(sqlPagedFolder, column, direction)

	var rows []ListingRow
	err := qr.SelectContext(ctx, &rows, statement,
		strings.TrimRight(q.Path, "/"), q.User, q.Zone, groupIDArray(q.GroupIDs),
		infoTypeArray(q.InfoTypes), q.IncludeUnknownInfoType, limit, q.Offset,
		infoTypeAttributeOr(q.InfoTypeAttribute), string(entityType))
	if err != nil {
		return nil, queryError(err, "listing %q", q.Path)
	}
	return rows, nil
}

// isKnownSortColumn reports whether column is one of sortColumns.
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

	var rows []ListingRow
	err := qr.SelectContext(ctx, &rows, sqlFoldersInFolder,
		strings.TrimRight(q.Path, "/"), q.User, q.Zone, groupIDArray(q.GroupIDs))
	if err != nil {
		return nil, queryError(err, "listing subfolders of %q", q.Path)
	}
	return rows, nil
}
