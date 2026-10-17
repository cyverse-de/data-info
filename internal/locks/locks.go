// Package locks decides whether a path is held by unfinished async work. There is no lock
// table: a path is locked while an unfinished task names it, so the check is advisory and racy
// between checking and creating the task. An in-process mutex would not help across replicas.
package locks

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/paths"
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

// farFuture, with IncludeNullEnd, selects unfinished tasks; the service's filter cannot ask
// for a null end date alone.
var farFuture = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

// Reader is the part of the async-tasks client this package needs.
type Reader interface {
	ByFilter(ctx context.Context, filter asynctasks.Filter) ([]asynctasks.Task, error)
}

// Validate rejects paths that unfinished work already holds, naming every conflicting path so
// the caller knows which to retry.
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
		// The cause names an internal service and its reply, so it is logged but kept out
		// of the response.
		return apierror.New(apierror.ErrUnavailable).
			With("reason", "could not determine whether these paths are in use").
			WithCause(err)
	}

	locked := LockedPaths(tasks)
	if len(locked) == 0 {
		return nil
	}

	var conflicting []string
	for _, path := range paths {
		if strings.TrimRight(path, "/") == "" {
			continue
		}
		if conflictsAny(path, locked) {
			conflicting = append(conflicting, path)
		}
	}

	if len(conflicting) == 0 {
		return nil
	}
	return apierror.New(apierror.ErrConflict).With("paths", conflicting)
}

// LockedPaths collects every path the given tasks hold. Which data keys hold paths depends on
// the task type.
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
		// A move locks its sources and where each lands, not the destination collection.
		sources := stringsAt(data, "sources")
		destination := stringAt(data, "destination")

		out := append([]string{}, sources...)
		if destination == "" {
			return out
		}
		named := slices.DeleteFunc(slices.Clone(sources), func(s string) bool { return s == "" })
		return append(out, paths.DestinationsUnder(destination, named)...)

	case asynctasks.TypeRename:
		return []string{stringAt(data, "source"), stringAt(data, "destination")}

	case asynctasks.TypeDelete:
		return append(stringsAt(data, "paths"), valuesOf(data, "trash-paths")...)

	case asynctasks.TypeDeleteTrash:
		// A list here, where a delete stores a map; the two were never made to agree.
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

// Conflicts reports whether two paths are the same or one contains the other, matching on
// whole components so /a and /ab do not conflict (docs/deferred-fixes.md #15).
func Conflicts(a, b string) bool {
	a, b = strings.TrimRight(a, "/"), strings.TrimRight(b, "/")

	// An empty string is not a path; without this it would prefix-match everything.
	if a == "" || b == "" {
		return false
	}

	return paths.IsWithin(a, b) || paths.IsWithin(b, a)
}

// stringAt reads one string out of a task's data.
func stringAt(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return value
}

// stringsAt reads a list of strings from task data, skipping non-strings rather than failing:
// a lock check broken by one odd task would block every write.
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
