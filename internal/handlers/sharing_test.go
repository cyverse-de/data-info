package handlers

import (
	"testing"

	"github.com/cyverse-de/data-info/internal/rods"
)

// An unrecognised permission maps to iRODS' null access, which removes what the sharee had.
// A typo in this field would therefore quietly unshare something and report success, so it is
// rejected rather than passed through.
func TestSharePermissionRejectsAnythingUnrecognised(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    rods.Permission
		wantErr bool
	}{
		{name: "read", in: "read", want: rods.PermissionRead},
		{name: "write", in: "write", want: rods.PermissionWrite},
		{name: "own", in: "own", want: rods.PermissionOwn},
		{name: "surrounding space", in: " read ", want: rods.PermissionRead},
		{name: "a plausible typo", in: "readonly", wantErr: true},
		{name: "absent", in: "", wantErr: true},
		{name: "the level iRODS uses for no access", in: "null", wantErr: true},
		{name: "the wrong case", in: "Read", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sharePermission(tc.in)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("sharePermission(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("sharePermission(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("sharePermission(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
