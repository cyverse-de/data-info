package service

import (
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/paths"
)

func TestTempUploadPath(t *testing.T) {
	longName := strings.Repeat("n", paths.MaxFilenameLength)
	deepDir := "/iplant/home/wregglej/" + strings.Repeat("d", paths.MaxDirLength-30)

	cases := []struct {
		name       string
		dest       string
		wantDir    string
		wantNamed  bool // whether the destination's own name appears in the temporary one
		wantMaxLen int
	}{
		{
			name:       "ordinary name is hidden and suffixed",
			dest:       "/iplant/home/wregglej/report.csv",
			wantDir:    "/iplant/home/wregglej",
			wantNamed:  true,
			wantMaxLen: paths.MaxPathLength,
		},
		{
			name:       "name already at the limit falls back to the bare suffix",
			dest:       "/iplant/home/wregglej/" + longName,
			wantDir:    "/iplant/home/wregglej",
			wantNamed:  false,
			wantMaxLen: paths.MaxPathLength,
		},
		{
			name:       "deep collection falls back rather than overflowing the path",
			dest:       deepDir + "/" + strings.Repeat("f", 400),
			wantDir:    deepDir,
			wantNamed:  false,
			wantMaxLen: paths.MaxPathLength,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TempUploadPath(tc.dest)

			if dir := paths.Dir(got); dir != tc.wantDir {
				t.Errorf("collection = %q, want %q", dir, tc.wantDir)
			}
			if !strings.Contains(got, TempUploadName) {
				t.Errorf("%q does not carry the %q marker", got, TempUploadName)
			}
			if !strings.HasPrefix(paths.Base(got), ".") {
				t.Errorf("%q is not hidden", got)
			}
			if named := strings.Contains(paths.Base(got), paths.Base(tc.dest)); named != tc.wantNamed {
				t.Errorf("carries the destination name = %v, want %v (%q)", named, tc.wantNamed, got)
			}
			if len(got) > tc.wantMaxLen {
				t.Errorf("path is %d characters, over the %d limit", len(got), tc.wantMaxLen)
			}
			if len(paths.Base(got)) > paths.MaxFilenameLength {
				t.Errorf("name is %d characters, over the %d limit",
					len(paths.Base(got)), paths.MaxFilenameLength)
			}
		})
	}
}

func TestTempUploadPathsAreDistinct(t *testing.T) {
	dest := "/iplant/home/wregglej/report.csv"

	first, second := TempUploadPath(dest), TempUploadPath(dest)
	if first == second {
		t.Fatalf("two temporary paths for the same destination collided: %q", first)
	}
}
