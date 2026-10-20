// Package paths knows the shape of the DE's iRODS namespace: where a user's home and trash
// live, which collections are roots the service protects, and what to call them.
package paths

import (
	"path"
	"strings"
)

// Layout describes the zone's namespace.
type Layout struct {
	// Zone is the iRODS zone name.
	Zone string

	// Home is the collection user homes live under, such as /iplant/home.
	Home string

	// CommunityData is the community data collection.
	CommunityData string
}

// UserHome is a user's home collection.
func (l Layout) UserHome(user string) string { return join(l.Home, user) }

// TrashBase is the collection all trash lives under: /<zone>/trash/home, a sibling of Home
// rather than a child, so it is derived from the zone.
func (l Layout) TrashBase() string { return join("/"+l.Zone, "trash", "home") }

// UserTrash is a user's trash collection.
func (l Layout) UserTrash(user string) string { return join(l.TrashBase(), user) }

// IsUserTrash reports whether a path is a user's own trash collection.
func (l Layout) IsUserTrash(user, p string) bool { return equal(p, l.UserTrash(user)) }

// IsTrashBase reports whether a path is the collection all trash lives under.
func (l Layout) IsTrashBase(p string) bool { return equal(p, l.TrashBase()) }

// IsSharing reports whether a path is the home collection itself, which the DE presents as
// the place shared data appears rather than as a folder.
func (l Layout) IsSharing(p string) bool { return equal(p, l.Home) }

// IsCommunityData reports whether a path is the community data collection.
func (l Layout) IsCommunityData(p string) bool { return equal(p, l.CommunityData) }

// InTrash reports whether a path is anywhere under the trash collection.
func (l Layout) InTrash(p string) bool { return IsWithin(p, l.TrashBase()) }

// Label is what the UI calls a path. Special collections get names because their basenames
// mean nothing to a user.
func (l Layout) Label(user, p string) string {
	switch {
	case l.IsUserTrash(user, p):
		return "Trash"
	case l.IsSharing(p):
		return "Shared With Me"
	case l.IsCommunityData(p):
		return "Community Data"
	default:
		return Base(p)
	}
}

// IsBasePath reports whether a path is one of the special collections a path list may not be
// built from. The user's own home is not one.
func (l Layout) IsBasePath(user, p string) bool {
	return l.IsUserTrash(user, p) ||
		l.IsTrashBase(p) ||
		l.IsSharing(p) ||
		l.IsCommunityData(p)
}

// Base is the last element of an iRODS path. This package uses path, not filepath, because
// iRODS paths always use forward slashes.
func Base(p string) string { return path.Base(strings.TrimRight(p, "/")) }

// Dir is everything but the last element of an iRODS path.
func Dir(p string) string { return path.Dir(strings.TrimRight(p, "/")) }

// Clean canonicalises a path the way the iRODS client does before acting on one, so that the
// path this service validates is the path the server is asked about.
func Clean(p string) string { return path.Clean(p) }

// Join builds an iRODS path from its elements.
func Join(elements ...string) string { return join(elements...) }

func join(elements ...string) string { return path.Join(elements...) }

// IsWithin reports whether p is base or lies under it, matching whole components so that
// /a/bc is not within /a/b.
func IsWithin(p, base string) bool {
	p, base = strings.TrimRight(p, "/"), strings.TrimRight(base, "/")
	return p == base || strings.HasPrefix(p, base+"/")
}

// equal compares two paths ignoring a trailing slash on either.
func equal(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// DestinationsUnder names where each source lands inside destination; these, not destination
// itself, are the paths a move locks.
func DestinationsUnder(destination string, sources []string) []string {
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		out = append(out, Join(destination, Base(source)))
	}
	return out
}
