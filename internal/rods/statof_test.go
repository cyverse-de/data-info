package rods

import (
	"database/sql"
	"testing"

	"github.com/cyverse-de/data-info/internal/icat"
)

// TestStatOfKeepsAnEmptyChecksum covers the difference terrain depends on: a file with an
// empty catalog checksum still reports md5, as clj-irods did, and only a NULL omits it.
func TestStatOfKeepsAnEmptyChecksum(t *testing.T) {
	tests := []struct {
		name     string
		checksum sql.NullString
		want     *string
	}{
		{"NULL", sql.NullString{}, nil},
		{"empty", sql.NullString{Valid: true}, new("")},
		{"present", sql.NullString{String: "abc123", Valid: true}, new("abc123")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statOf(icat.Row{FullPath: "/z/home/u/f", CreateTS: "0", ModifyTS: "0", DataChecksum: tt.checksum}).Checksum
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("Checksum = %q, want nil", *got)
			case tt.want != nil && (got == nil || *got != *tt.want):
				t.Errorf("Checksum = %v, want %q", got, *tt.want)
			}
		})
	}
}
