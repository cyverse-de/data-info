package service

import (
	"database/sql"
	"testing"

	"github.com/cyverse-de/data-info/internal/icat"
)

func TestEntryOfInfoType(t *testing.T) {
	cases := []struct {
		name     string
		typ      icat.ObjectType
		infoType sql.NullString
		want     any
	}{
		{"a typed file", icat.ObjectTypeDataObject, sql.NullString{String: "csv", Valid: true}, "csv"},
		{"an untyped file", icat.ObjectTypeDataObject, sql.NullString{}, UnknownInfoType},
		{"a file with an empty type", icat.ObjectTypeDataObject, sql.NullString{Valid: true}, UnknownInfoType},
		{"a folder", icat.ObjectTypeCollection, sql.NullString{}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := icat.ListingRow{Type: tc.typ, InfoType: tc.infoType}
			if got := EntryOf(row, BadNameRule{}).InfoType; got != tc.want {
				t.Errorf("InfoType = %#v, want %#v", got, tc.want)
			}
		})
	}
}
