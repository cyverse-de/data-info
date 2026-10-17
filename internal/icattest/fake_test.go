package icattest

import (
	"context"
	"testing"

	"github.com/cyverse-de/data-info/internal/icat"
)

func TestPagedFolderLimits(t *testing.T) {
	fake := New()
	fake.AddCollection("/z/home/u", icat.AccessOwn)
	for _, name := range []string{"a", "b", "c"} {
		fake.AddDataObject("/z/home/u/"+name, 1, icat.AccessOwn)
	}

	tests := []struct {
		name          string
		limit, offset int
		want          int
		wantErr       bool
	}{
		{name: "a page", limit: 2, want: 2},
		{name: "the last page", limit: 2, offset: 2, want: 1},
		{name: "a negative limit returns everything", limit: -1, want: 3},
		{name: "a negative limit honours the offset", limit: -1, offset: 1, want: 2},
		{name: "an offset past the end", limit: 2, offset: 5, want: 0},
		{name: "a zero limit is refused", limit: 0, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := fake.PagedFolder(context.Background(), icat.ListingQuery{
				Path: "/z/home/u", SortColumn: "full_path", Limit: tt.limit, Offset: tt.offset,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatal("PagedFolder succeeded, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("PagedFolder: %v", err)
			}
			if len(rows) != tt.want {
				t.Errorf("rows = %d, want %d", len(rows), tt.want)
			}
		})
	}
}
