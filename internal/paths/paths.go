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

// TrashBase is the collection all trash lives under.
//
// iRODS puts it at /<zone>/trash/home, which is a sibling of the home collection rather
// than a child, so it is derived from the zone rather than from Home.
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
func (l Layout) InTrash(p string) bool { return strings.HasPrefix(p, l.TrashBase()) }

// Label is what the UI calls a path.
//
// The three special collections get names rather than their basename, because their
// basenames are meaningless to a user: a user's trash is named after them, and the home
// collection is where other people's shares appear.
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

// IsBasePath reports whether a path is one of the roots the service protects from being
// moved, renamed or deleted.
func (l Layout) IsBasePath(user, p string) bool {
	return equal(p, l.UserHome(user)) ||
		l.IsUserTrash(user, p) ||
		l.IsTrashBase(p) ||
		l.IsSharing(p) ||
		l.IsCommunityData(p)
}

// Base is the last element of an iRODS path.
//
// path rather than filepath throughout this package: iRODS paths always use forward
// slashes, so on a platform with a different separator filepath would give wrong answers.
func Base(p string) string { return path.Base(strings.TrimRight(p, "/")) }

// Dir is everything but the last element of an iRODS path.
func Dir(p string) string { return path.Dir(strings.TrimRight(p, "/")) }

// Join builds an iRODS path from its elements.
func Join(elements ...string) string { return join(elements...) }

func join(elements ...string) string { return path.Join(elements...) }

// equal compares two paths ignoring a trailing slash on either.
func equal(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}
