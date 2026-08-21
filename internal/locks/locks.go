// Package locks decides whether a path is already caught up in long-running work.
//
// There is no lock table. A path is "locked" when some async task that has not finished yet
// names it, so the whole mechanism is a query plus a comparison -- which means it is
// advisory, shared with every replica and with the Clojure service, and racy between the
// check and the task that follows it. The reference has the same race; an in-process mutex
// would narrow it without closing it, because the other replica is a different process.
package locks

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
)

// lockingTypes are the task types whose paths are held while they run. A type absent from
// this list locks nothing, which is why an upload's cleanup task does not block anything.
var lockingTypes = []string{
	asynctasks.TypeMove,
	asynctasks.TypeRename,
	asynctasks.TypeDelete,
	asynctasks.TypeDeleteTrash,
	asynctasks.TypeRestore,
}

// farFuture is the end date the filter asks for.
//
// Together with IncludeNullEnd it selects "everything that has not finished", since no real
// task ends after this. Asking for unfinished work directly is not something the service's
// filter can express.
var farFuture = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

// Reader is the part of the async-tasks client this package needs.
type Reader interface {
	ByFilter(ctx context.Context, filter asynctasks.Filter) ([]asynctasks.Task, error)
}

// Validate rejects paths that unfinished work already holds.
//
// It reports every conflicting path rather than the first, because a caller moving several
// things wants to know which of them to retry.
func Validate(ctx context.Context, reader Reader, paths []string) error {
	if len(paths) == 0 {
		return nil
	}

	tasks, err := reader.ByFilter(ctx, asynctasks.Filter{
		Types:          lockingTypes,
		IncludeNullEnd: true,
		EndDateSince:   &farFuture,
	})
	if err != nil {
		return err
	}

	locked := LockedPaths(tasks)
	if len(locked) == 0 {
		return nil
	}

	var conflicting []string
	for _, path := range paths {
		if conflictsAny(path, locked) {
			conflicting = append(conflicting, path)
		}
	}

	if len(conflicting) == 0 {
		return nil
	}
	return apierror.New(apierror.ErrConflict).With("paths", conflicting)
}

// LockedPaths collects every path the given tasks hold.
//
// Which keys hold paths differs by task type, and the keys are a contract with whoever wrote
// the task -- including a Clojure replica during a rollback -- so they are spelled here
// exactly as they are stored.
func LockedPaths(tasks []asynctasks.Task) []string {
	seen := map[string]bool{}

	for _, task := range tasks {
		for _, path := range pathsOf(task) {
			if path != "" {
				seen[path] = true
			}
		}
	}

	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// pathsOf pulls the paths out of one task's data.
func pathsOf(task asynctasks.Task) []string {
	data := task.Data

	switch task.Type {
	case asynctasks.TypeMove:
		// A move locks its sources and where each of them will land, which is the
		// destination collection joined to each source's own name -- not the destination
		// itself, which usually already exists and is not being moved.
		sources := stringsAt(data, "sources")
		destination := stringAt(data, "destination")

		out := append([]string{}, sources...)
		for _, source := range sources {
			out = append(out, joinBase(destination, source))
		}
		return out

	case asynctasks.TypeRename:
		return []string{stringAt(data, "source"), stringAt(data, "destination")}

	case asynctasks.TypeDelete:
		return append(stringsAt(data, "paths"), valuesOf(data, "trash-paths")...)

	case asynctasks.TypeDeleteTrash:
		// A list here, where a delete stores a map. The two are written by different code
		// paths and were never made to agree.
		return stringsAt(data, "trash-paths")

	case asynctasks.TypeRestore:
		return append(stringsAt(data, "paths"), fieldOfValues(data, "restoration-paths", "restored-path")...)
	}

	return nil
}

// conflictsAny reports whether a path collides with any locked path.
func conflictsAny(path string, locked []string) bool {
	for _, other := range locked {
		if Conflicts(path, other) {
			return true
		}
	}
	return false
}

// Conflicts reports whether two paths cannot be worked on at the same time.
//
// They cannot when they are the same path, or when one contains the other: moving a
// collection while something inside it is being moved separately leaves both operations
// working on a tree that is changing underneath them.
//
// This is deliberately not what the reference computes. Its two prefix tests compare a path
// against another path plus a slash without requiring the match to end on a component
// boundary, so "/a" collides with a locked "/ab" and refuses a request it should allow. The
// version here is strictly more permissive than the reference, never less, so it cannot
// allow a pair the reference would have refused for a real reason -- but it is a behaviour
// change and is recorded as one in docs/deferred-fixes.md.
func Conflicts(a, b string) bool {
	a, b = strings.TrimRight(a, "/"), strings.TrimRight(b, "/")

	return a == b ||
		strings.HasPrefix(a, b+"/") ||
		strings.HasPrefix(b, a+"/")
}

// joinBase names what a source will be called under a destination collection.
func joinBase(destination, source string) string {
	if destination == "" || source == "" {
		return ""
	}

	trimmed := strings.TrimRight(source, "/")
	base := trimmed
	if at := strings.LastIndex(trimmed, "/"); at >= 0 {
		base = trimmed[at+1:]
	}

	return strings.TrimRight(destination, "/") + "/" + base
}

// stringAt reads one string out of a task's data.
func stringAt(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return value
}

// stringsAt reads a list of strings out of a task's data.
//
// Task data arrives as decoded JSON, so a list is []any of strings rather than []string, and
// anything that is not a string is skipped rather than failing the whole check: a lock that
// refuses to answer because one task was written oddly would block every write in the DE.
func stringsAt(data map[string]any, key string) []string {
	raw, ok := data[key].([]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if value, ok := item.(string); ok {
			out = append(out, value)
		}
	}
	return out
}

// valuesOf reads the values of a map held under a key, ignoring its keys.
func valuesOf(data map[string]any, key string) []string {
	raw, ok := data[key].(map[string]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if value, ok := item.(string); ok {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

// fieldOfValues reads one field out of each value of a map of objects.
func fieldOfValues(data map[string]any, key, field string) []string {
	raw, ok := data[key].(map[string]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(raw))
	for _, item := range raw {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if value, ok := object[field].(string); ok {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
