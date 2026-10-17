package rods

import (
	"slices"
	"testing"
)

func TestTicketsUnder(t *testing.T) {
	all := []Ticket{
		{Name: "on-dir", Path: "/z/home/u/dir"},
		{Name: "inside", Path: "/z/home/u/dir/f.txt"},
		{Name: "deep", Path: "/z/home/u/dir/sub/g.txt"},
		{Name: "sibling-prefix", Path: "/z/home/u/dir2/h.txt"},
		{Name: "elsewhere", Path: "/z/home/v/f.txt"},
	}

	tests := []struct {
		name string
		path string
		want []string
	}{
		{"a collection takes everything inside it", "/z/home/u/dir", []string{"on-dir", "inside", "deep"}},
		{"a trailing slash changes nothing", "/z/home/u/dir/", []string{"on-dir", "inside", "deep"}},
		{"a file takes only its own", "/z/home/u/dir/f.txt", []string{"inside"}},
		{"a name prefix is not containment", "/z/home/u/dir2", []string{"sibling-prefix"}},
		{"nothing there", "/z/home/w", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, ticket := range TicketsUnder(all, tt.path) {
				got = append(got, ticket.Name)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("TicketsUnder(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
