// Package service holds data-info's business logic, one file per Clojure services/*
// namespace.
package service

import "strings"

// StatField names a field of a stat object.
type StatField string

// The fields a stat object can carry. These names are the JSON keys, and they are a wire
// contract: callers pass them to filter-include and filter-exclude.
//
// Note the inconsistent spelling. Most are hyphenated, but infoType is camel case and id,
// path, type, label, permission and md5 are bare. That is what the Clojure service emits,
// so it is what these have to be.
const (
	FieldID           StatField = "id"
	FieldPath         StatField = "path"
	FieldType         StatField = "type"
	FieldLabel        StatField = "label"
	FieldDateCreated  StatField = "date-created"
	FieldDateModified StatField = "date-modified"
	FieldPermission   StatField = "permission"
	FieldShareCount   StatField = "share-count"
	FieldFileCount    StatField = "file-count"
	FieldDirCount     StatField = "dir-count"
	FieldFileSize     StatField = "file-size"
	FieldContentType  StatField = "content-type"
	FieldInfoType     StatField = "infoType"
	FieldMD5          StatField = "md5"
)

// AllStatFields is every field, which is what a request that filters nothing gets.
var AllStatFields = []StatField{
	FieldID, FieldPath, FieldType, FieldLabel, FieldDateCreated, FieldDateModified,
	FieldPermission, FieldShareCount, FieldFileCount, FieldDirCount, FieldFileSize,
	FieldContentType, FieldInfoType, FieldMD5,
}

// FieldSet is the set of fields a response should carry.
type FieldSet map[StatField]bool

// ParseFieldSet resolves the filter-include and filter-exclude parameters into the fields
// to emit.
//
// Includes are applied first and excludes second, so a request naming a field in both gets
// neither. Unknown names are dropped rather than rejected, matching the Clojure
// implementation, which intersected against the known set.
func ParseFieldSet(include, exclude string) FieldSet {
	known := make(FieldSet, len(AllStatFields))
	for _, f := range AllStatFields {
		known[f] = true
	}

	included := known
	if strings.TrimSpace(include) != "" {
		included = make(FieldSet)
		for _, name := range splitFields(include) {
			if known[name] {
				included[name] = true
			}
		}
	}

	if strings.TrimSpace(exclude) != "" {
		out := make(FieldSet, len(included))
		for f := range included {
			out[f] = true
		}
		for _, name := range splitFields(exclude) {
			delete(out, name)
		}
		return out
	}

	return included
}

func splitFields(list string) []StatField {
	parts := strings.Split(list, ",")
	out := make([]StatField, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, StatField(trimmed))
		}
	}
	return out
}

// Has reports whether a field should be emitted.
func (fs FieldSet) Has(f StatField) bool { return fs[f] }

// HasAny reports whether any of the fields is emitted.
//
// It exists for the fields whose computation costs a query: a directory's child counts are
// only worth fetching if at least one of them is being reported.
//
// There is no separate notion of a field being computed but not emitted. The Clojure service
// has one, because it decides what to gather before it gathers it; here a catalog row already
// carries the type and the permission, so those are always available and the only question is
// whether they are written out.
func (fs FieldSet) HasAny(fields ...StatField) bool {
	for _, f := range fields {
		if fs[f] {
			return true
		}
	}
	return false
}
