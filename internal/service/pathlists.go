package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/rods"
)

// PathListQuery is what a path list is built from.
type PathListQuery struct {
	// FileIdentifier is the header line the file opens with. Whoever reads the file back
	// identifies it by that line, so it is configured rather than derived.
	FileIdentifier string

	// NamePattern keeps only entries whose name matches. Blank keeps everything.
	NamePattern string

	// InfoTypes keeps only files of those types. Empty keeps everything.
	InfoTypes []string

	// FoldersOnly lists folders instead of files. The two are exclusive: a path list holds
	// one kind or the other, never both.
	FoldersOnly bool

	// Recursive walks into subfolders. Without it only what the given folders directly hold
	// is considered.
	Recursive bool
}

// PathListEntry is one candidate for a path list.
type PathListEntry struct {
	Path       string
	Label      string
	Type       rods.ObjectType
	InfoType   string
	Permission rods.Permission
}

// ErrNoMatchingPaths is returned when the filters leave nothing.
//
// An empty path list is not a useful file: an analysis given one would run over nothing and
// report success, which is harder to notice than a failure here.
var ErrNoMatchingPaths = fmt.Errorf("no paths matched the request")

// BuildPathList renders the contents of a path list file.
//
// The paths given are not themselves included. A folder in the request contributes what is
// inside it; a file contributes itself. That asymmetry is the point of the endpoint: a user
// selects folders and gets the files in them.
func BuildPathList(entries []PathListEntry, q PathListQuery) (string, error) {
	pattern, err := compilePattern(q.NamePattern)
	if err != nil {
		return "", err
	}

	var kept []string
	for _, entry := range entries {
		if !keepEntry(entry, q, pattern) {
			continue
		}
		kept = append(kept, entry.Path)
	}

	if len(kept) == 0 {
		return "", ErrNoMatchingPaths
	}

	return strings.Join(append([]string{q.FileIdentifier}, kept...), "\n"), nil
}

// keepEntry reports whether one candidate belongs in the list.
func keepEntry(entry PathListEntry, q PathListQuery, pattern *regexp.Regexp) bool {
	// No permission means the entry is not really visible to the caller, whatever the
	// listing returned.
	if entry.Permission == rods.PermissionNone {
		return false
	}
	if pattern != nil && !pattern.MatchString(entry.Label) {
		return false
	}

	if entry.Type == rods.ObjectTypeDir {
		return q.FoldersOnly
	}
	return !q.FoldersOnly
}

// KeepInfoType reports whether a file's type is one the request asked for.
//
// Applied only to the files named directly in a request. Files found by walking a folder are
// filtered by the catalog query instead, which is cheaper and gives the same answer.
func KeepInfoType(infoType string, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, candidate := range wanted {
		if candidate == infoType {
			return true
		}
	}
	return false
}

// compilePattern turns the name filter into a matcher, or nil when there is none.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, nil
	}

	// A partial match, not an anchored one: the reference uses re-find, so a pattern
	// matching anywhere in the name keeps the entry.
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("the name pattern is not a valid regular expression: %w", err)
	}
	return compiled, nil
}

// ListingEntityType decides what a folder walk asks the catalog for.
//
// A recursive listing has to see folders as well as files even when only files are wanted,
// because the folders are how it reaches them.
func ListingEntityType(recursive, foldersOnly bool) icat.EntityType {
	switch {
	case recursive && !foldersOnly:
		return icat.EntityAny
	case foldersOnly:
		return icat.EntityFolder
	default:
		return icat.EntityFile
	}
}
