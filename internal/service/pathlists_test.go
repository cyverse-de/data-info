package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/rods"
)

func entry(path, label string, kind rods.ObjectType, infoType string, perm rods.Permission) PathListEntry {
	return PathListEntry{Path: path, Label: label, Type: kind, InfoType: infoType, Permission: perm}
}

func TestBuildPathList(t *testing.T) {
	const header = "# application/vnd.de.path-list+csv; version=1"

	entries := []PathListEntry{
		entry("/z/home/u/a.txt", "a.txt", rods.ObjectTypeFile, "csv", rods.PermissionOwn),
		entry("/z/home/u/b.bam", "b.bam", rods.ObjectTypeFile, "bam", rods.PermissionRead),
		entry("/z/home/u/sub", "sub", rods.ObjectTypeDir, "", rods.PermissionOwn),
		// No permission means it is not really visible, whatever the listing returned.
		entry("/z/home/u/hidden.txt", "hidden.txt", rods.ObjectTypeFile, "csv", rods.PermissionNone),
	}

	cases := []struct {
		name  string
		query PathListQuery
		want  []string
	}{
		{
			name:  "files by default",
			query: PathListQuery{FileIdentifier: header},
			want:  []string{"/z/home/u/a.txt", "/z/home/u/b.bam"},
		},
		{
			// A list holds one kind or the other, never both.
			name:  "folders only",
			query: PathListQuery{FileIdentifier: header, FoldersOnly: true},
			want:  []string{"/z/home/u/sub"},
		},
		{
			name:  "a name pattern matches anywhere in the name",
			query: PathListQuery{FileIdentifier: header, NamePattern: `\.txt$`},
			want:  []string{"/z/home/u/a.txt"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildPathList(entries, tc.query)
			if err != nil {
				t.Fatalf("BuildPathList: %v", err)
			}

			lines := strings.Split(got, "\n")
			if lines[0] != header {
				t.Errorf("first line = %q, want the file identifier", lines[0])
			}
			if strings.Join(lines[1:], ",") != strings.Join(tc.want, ",") {
				t.Errorf("paths = %q, want %q", lines[1:], tc.want)
			}
		})
	}
}

// An empty path list is worse than a failure: an analysis given one runs over nothing and
// reports success, which is far harder to notice.
func TestBuildPathListRefusesToProduceAnEmptyList(t *testing.T) {
	_, err := BuildPathList(
		[]PathListEntry{entry("/z/home/u/a.txt", "a.txt", rods.ObjectTypeFile, "csv", rods.PermissionOwn)},
		PathListQuery{FileIdentifier: "#", NamePattern: "nothing-matches-this"},
	)

	if !errors.Is(err, ErrNoMatchingPaths) {
		t.Fatalf("BuildPathList returned %v, want ErrNoMatchingPaths", err)
	}
}

func TestBuildPathListRejectsABadPattern(t *testing.T) {
	_, err := BuildPathList(nil, PathListQuery{FileIdentifier: "#", NamePattern: "("})
	if err == nil {
		t.Fatal("BuildPathList accepted a pattern that is not a regular expression")
	}
}

// A recursive listing has to see folders even when only files are wanted, because the folders
// are how it reaches them.
func TestListingEntityType(t *testing.T) {
	cases := []struct {
		name                   string
		recursive, foldersOnly bool
		want                   icat.EntityType
	}{
		{name: "files, flat", want: icat.EntityFile},
		{name: "files, recursive", recursive: true, want: icat.EntityAny},
		{name: "folders, flat", foldersOnly: true, want: icat.EntityFolder},
		{name: "folders, recursive", recursive: true, foldersOnly: true, want: icat.EntityFolder},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ListingEntityType(tc.recursive, tc.foldersOnly); got != tc.want {
				t.Errorf("ListingEntityType(%v, %v) = %q, want %q",
					tc.recursive, tc.foldersOnly, got, tc.want)
			}
		})
	}
}

func TestKeepInfoType(t *testing.T) {
	if !KeepInfoType("anything", nil) {
		t.Error("an empty filter should keep everything")
	}
	if !KeepInfoType("csv", []string{"bam", "csv"}) {
		t.Error("a listed type should be kept")
	}
	if KeepInfoType("csv", []string{"bam"}) {
		t.Error("an unlisted type should be dropped")
	}
}
