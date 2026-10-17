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

	// IncludeUnknown keeps files with no info type at all. It is set independently of
	// InfoTypes, because asking for only untyped files is a real request.
	IncludeUnknown bool

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

// ErrNoMatchingPaths is returned when the filters leave nothing. An analysis given an empty
// path list would run over nothing and report success, which is harder to notice.
var ErrNoMatchingPaths = fmt.Errorf("no paths matched the request")

// BuildPathList renders a path list file: the identifier line, then each entry that passes
// the filters.
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

// KeepInfoType reports whether a directly named file's info type was asked for; files found
// by walking a folder are filtered in the catalog query instead.
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

	// Unanchored: a pattern matching anywhere in the name keeps the entry.
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("the name pattern is not a valid regular expression: %w", err)
	}
	return compiled, nil
}

// ListingEntityType decides what a folder walk asks the catalog for. A recursive walk needs
// folders even when only files are wanted, to reach the files inside them.
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
