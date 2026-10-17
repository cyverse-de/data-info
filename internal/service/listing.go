package service

import (
	"strings"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/paths"
)

// ListingEntry is one row of a folder listing. Its keys differ from Stat's (dateCreated vs
// date-created, name vs label), and both shapes are wire contracts.
type ListingEntry struct {
	ID           string `json:"id"`
	DateCreated  int64  `json:"dateCreated"`
	DateModified int64  `json:"dateModified"`
	BadName      bool   `json:"badName"`
	InfoType     any    `json:"infoType"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Permission   string `json:"permission"`
	Size         int64  `json:"size"`
}

// Listing is a folder's own entry with a page of its contents merged in at the top level.
type Listing struct {
	ListingEntry

	Files   []ListingEntry `json:"files"`
	Folders []ListingEntry `json:"folders"`

	// Total is how many entries the folder holds in all, or null when not computed.
	Total *int64 `json:"total"`

	// TotalBad is always zero; it is kept because callers expect the field.
	TotalBad int64 `json:"totalBad"`

	// Readme is the folder's README entry, or false when there is none.
	Readme any `json:"readme"`
}

// ReadmeNames are the files a listing looks for, in the Clojure service's priority order.
var ReadmeNames = []string{"README.md", "README.txt", "README", "readme.md", "readme.txt", "readme"}

// BadNameRule is the client's description of names it cannot display; matching entries are
// flagged, not filtered out.
type BadNameRule struct {
	// Chars are characters that make a name bad if it contains any of them.
	Chars string

	// Names are exact base names that are bad.
	Names []string

	// Paths are exact absolute paths that are bad.
	Paths []string
}

// Matches reports whether an entry should be flagged.
func (r BadNameRule) Matches(path, name string) bool {
	if r.Chars != "" && strings.ContainsAny(name, r.Chars) {
		return true
	}
	for _, bad := range r.Names {
		if strings.EqualFold(name, bad) {
			return true
		}
	}
	for _, bad := range r.Paths {
		if path == bad {
			return true
		}
	}
	return false
}

// UnknownInfoType is what a data object nothing has typed yet reports.
const UnknownInfoType = "unknown"

// FileInfoType reports a data object's info type, or UnknownInfoType when it has none.
func FileInfoType(infoType string) string {
	if infoType == "" {
		return UnknownInfoType
	}
	return infoType
}

// EntryOf converts a catalog row into a listing entry.
func EntryOf(row icat.ListingRow, rule BadNameRule) ListingEntry {
	// A folder reports the catalog column as it is, which is null.
	var infoType any
	switch {
	case !row.IsCollection():
		infoType = FileInfoType(row.InfoType.String)
	case row.InfoType.String != "":
		infoType = row.InfoType.String
	}

	return ListingEntry{
		ID:           row.UUID.String,
		DateCreated:  row.CreatedMillis(),
		DateModified: row.ModifiedMillis(),
		BadName:      rule.Matches(row.FullPath, row.BaseName),
		InfoType:     infoType,
		Name:         row.BaseName,
		Path:         row.FullPath,
		Permission:   string(row.Permission()),
		Size:         row.DataSize,
	}
}

// ListingOf builds a listing from the folder's own entry and a page of its children.
func ListingOf(self ListingEntry, rows []icat.ListingRow, rule BadNameRule) Listing {
	// Non-nil slices, so the response carries empty arrays rather than nulls.
	out := Listing{
		ListingEntry: self,
		Files:        []ListingEntry{},
		Folders:      []ListingEntry{},
		Readme:       false,
	}

	// A folder describes itself with no info type and no size, whatever the catalog holds.
	out.InfoType = nil
	out.Size = 0

	for _, row := range rows {
		entry := EntryOf(row, rule)
		if row.IsCollection() {
			out.Folders = append(out.Folders, entry)
		} else {
			out.Files = append(out.Files, entry)
		}

		// COUNT(*) OVER () is the same on every row; an empty page has none, so Total stays null.
		if row.TotalCount.Valid {
			total := row.TotalCount.Int64
			out.Total = &total
		}
	}

	return out
}

// FolderEntry is one subfolder of a navigation listing, which reports less than a full
// listing entry.
type FolderEntry struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	Label        string `json:"label"`
	Permission   string `json:"permission"`
	DateCreated  int64  `json:"date-created"`
	DateModified int64  `json:"date-modified"`
}

// FolderEntryOf converts a catalog row into a navigation entry.
func FolderEntryOf(row icat.ListingRow, user string, layout paths.Layout) FolderEntry {
	return FolderEntry{
		ID:           row.UUID.String,
		Path:         row.FullPath,
		Label:        layout.Label(user, row.FullPath),
		Permission:   string(row.Permission()),
		DateCreated:  row.CreatedMillis(),
		DateModified: row.ModifiedMillis(),
	}
}
