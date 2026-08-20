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

// Needs reports whether a field has to be computed, which is not the same as whether it is
// emitted.
//
// Two fields are needed to compute others even when the caller did not ask for them:
// permission decides whether a share count is reported at all, and type decides whether the
// file-only or folder-only fields apply. Both are dropped again before the response is
// written. Getting this wrong either loses a field the caller asked for or makes a cheap
// stat expensive.
func (fs FieldSet) Needs(f StatField) bool {
	if fs[f] {
		return true
	}

	switch f {
	case FieldPermission:
		return fs[FieldShareCount]
	case FieldType:
		return fs[FieldInfoType] || fs[FieldContentType] || fs[FieldFileCount] || fs[FieldDirCount]
	default:
		return false
	}
}

// NeedsAny reports whether any of the fields has to be computed.
func (fs FieldSet) NeedsAny(fields ...StatField) bool {
	for _, f := range fields {
		if fs.Needs(f) {
			return true
		}
	}
	return false
}
