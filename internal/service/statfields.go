// Package service holds data-info's business logic, independent of HTTP.
package service

import "strings"

// StatField names a field of a stat object.
type StatField string

// The fields a stat object can carry. The names are the JSON keys callers pass to
// filter-include and filter-exclude, so their inconsistent spelling is part of the wire
// contract.
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
// to emit. Excludes apply after includes, so a field named in both is dropped. Unknown names
// are ignored rather than rejected, as in the Clojure service.
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

// HasAny reports whether any of the fields is emitted, for fields that share an expensive
// query such as child counts.
func (fs FieldSet) HasAny(fields ...StatField) bool {
	for _, f := range fields {
		if fs[f] {
			return true
		}
	}
	return false
}
