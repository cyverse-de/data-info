package service

import (
	"strings"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/paths"
)

// ListingEntry is one row of a folder listing.
//
// This is a different vocabulary from Stat for the same underlying facts: camel-cased
// dateCreated rather than hyphenated date-created, name rather than label, size rather than
// file-size. Both are wire contracts and the difference is not tidied, because callers parse
// each shape where it appears.
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

// Listing is a folder's own entry with a page of its contents alongside.
//
// The folder describes itself with the same fields as one of its entries -- id, name, path,
// permission, dates, badName, infoType, size -- and the page is merged in at the top level
// rather than nested. A caller reads one object, not a wrapper around two.
type Listing struct {
	ListingEntry

	Files   []ListingEntry `json:"files"`
	Folders []ListingEntry `json:"folders"`

	// Total is how many entries the folder holds in all, which is not the length of this
	// page. It is null when it was not computed.
	Total *int64 `json:"total"`

	// TotalBad is always zero. The reference computes nothing for it and reports it
	// regardless, so it is carried rather than dropped.
	TotalBad int64 `json:"totalBad"`

	// Readme is the README under this folder, or false when there is none. The field is
	// deliberately untyped: the reference emits an entry object or the boolean false.
	Readme any `json:"readme"`
}

// ReadmeNames are the files a listing looks for, in the order the reference checks them.
var ReadmeNames = []string{"README.md", "README.txt", "README", "readme.md", "readme.txt", "readme"}

// BadNameRule decides which entries a client should flag as unrenderable.
//
// The client asks for this rather than the service deciding: a caller passes the characters,
// names and paths it cannot display, and the listing marks matching entries so they can be
// shown differently. None of it filters anything out.
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

// EntryOf converts a catalog row into a listing entry.
func EntryOf(row icat.ListingRow, rule BadNameRule) ListingEntry {
	// Null rather than an empty string when there is no info type: the reference reports
	// the catalog column, which is null for a folder and for a file that has none.
	var infoType any
	if row.InfoType.Valid && row.InfoType.String != "" {
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
	// Non-nil slices: the response carries empty arrays rather than nulls, and a caller
	// iterating the result should not have to distinguish the two.
	out := Listing{
		ListingEntry: self,
		Files:        []ListingEntry{},
		Folders:      []ListingEntry{},
		Readme:       false,
	}

	// A folder describes itself with no info type and no size, whatever the catalog holds
	// for it. These set the embedded entry's fields.
	out.InfoType = nil
	out.Size = 0

	for _, row := range rows {
		entry := EntryOf(row, rule)
		if row.IsCollection() {
			out.Folders = append(out.Folders, entry)
		} else {
			out.Files = append(out.Files, entry)
		}

		// COUNT(*) OVER () rides along on every row, so it is the same on all of them. A
		// page with no rows carries no count, and the total is reported as null -- which
		// is what the reference does for an empty folder.
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
