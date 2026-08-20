package service

import (
	"sort"
	"strings"
	"testing"
)

func TestParseFieldSet(t *testing.T) {
	tests := []struct {
		name    string
		include string
		exclude string
		want    []StatField
	}{
		{
			name: "no filters gives every field",
			want: AllStatFields,
		},
		{
			name:    "include narrows",
			include: "id,path",
			want:    []StatField{FieldID, FieldPath},
		},
		{
			name:    "exclude removes",
			exclude: "md5,share-count",
			want:    without(AllStatFields, FieldMD5, FieldShareCount),
		},
		{
			// Includes are applied first and excludes second, so a field named in both
			// is dropped. That order is the documented behaviour of the parameters.
			name:    "exclude wins over include",
			include: "id,path,md5",
			exclude: "md5",
			want:    []StatField{FieldID, FieldPath},
		},
		{
			name:    "unknown names are ignored rather than rejected",
			include: "id,not-a-field,path",
			want:    []StatField{FieldID, FieldPath},
		},
		{
			name:    "whitespace is tolerated",
			include: " id , path ",
			want:    []StatField{FieldID, FieldPath},
		},
		{
			name:    "excluding everything leaves nothing",
			exclude: strings.Join(names(AllStatFields), ","),
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseFieldSet(tt.include, tt.exclude)

			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", sorted(got), tt.want)
			}
			for _, f := range tt.want {
				if !got.Has(f) {
					t.Errorf("missing %q; got %v", f, sorted(got))
				}
			}
		})
	}
}

// TestFieldsAreEmittedOnlyWhenRequested covers the filtering. A catalog row already carries
// the type and the permission, so unlike the reference implementation nothing here has to be
// computed and then dropped -- the only question is whether a field is written out.
func TestFieldsAreEmittedOnlyWhenRequested(t *testing.T) {
	tests := []struct {
		name    string
		include string
		needs   StatField
		wantHas bool
	}{
		{
			name:    "share-count needs permission",
			include: "share-count",
			needs:   FieldPermission,
			wantHas: false, // needed to compute, but not emitted
		},
		{
			name:    "infoType needs type",
			include: "infoType",
			needs:   FieldType,
			wantHas: false,
		},
		{
			name:    "file-count needs type",
			include: "file-count",
			needs:   FieldType,
			wantHas: false,
		},
		{
			name:    "content-type needs type",
			include: "content-type",
			needs:   FieldType,
			wantHas: false,
		},
		{
			name:    "path alone needs neither",
			include: "path",
			needs:   FieldType,
			wantHas: false,
		},
		{
			name:    "an explicitly requested field is both needed and emitted",
			include: "type",
			needs:   FieldType,
			wantHas: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := ParseFieldSet(tt.include, "")
			if got := fs.Has(tt.needs); got != tt.wantHas {
				t.Errorf("Has(%q) = %v, want %v", tt.needs, got, tt.wantHas)
			}
		})
	}
}

func without(all []StatField, drop ...StatField) []StatField {
	dropped := map[StatField]bool{}
	for _, d := range drop {
		dropped[d] = true
	}
	out := make([]StatField, 0, len(all))
	for _, f := range all {
		if !dropped[f] {
			out = append(out, f)
		}
	}
	return out
}

func names(fields []StatField) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, string(f))
	}
	return out
}

func sorted(fs FieldSet) []string {
	out := make([]string, 0, len(fs))
	for f := range fs {
		out = append(out, string(f))
	}
	sort.Strings(out)
	return out
}
