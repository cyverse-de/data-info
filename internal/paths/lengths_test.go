package paths

import (
	"strings"
	"testing"
)

func TestCheckLength(t *testing.T) {
	cases := []struct {
		name string
		path string
		want LengthViolation
	}{
		{"ordinary path", "/iplant/home/wregglej/report.csv", LengthOK},
		{
			// The limits do not compose: a collection and a name that are each exactly at
			// their own limit make a path one character over the whole-path one.
			name: "every limit exactly met",
			path: "/" + strings.Repeat("d", MaxDirLength-2) + "/" + strings.Repeat("f", MaxFilenameLength),
			want: LengthOK,
		},
		{
			name: "whole path too long is reported first",
			path: "/iplant/home/" + strings.Repeat("a", MaxPathLength),
			want: LengthPath,
		},
		{
			name: "collection too long",
			path: "/" + strings.Repeat("d", MaxDirLength) + "/f.txt",
			want: LengthDir,
		},
		{
			name: "last component too long",
			path: "/iplant/home/" + strings.Repeat("f", MaxFilenameLength+1),
			want: LengthBasename,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckLength(tc.path); got != tc.want {
				t.Errorf("CheckLength(%d characters) = %q, want %q", len(tc.path), got, tc.want)
			}
		})
	}
}
