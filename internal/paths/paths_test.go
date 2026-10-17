package paths

import "testing"

func TestIsBasePath(t *testing.T) {
	layout := Layout{Zone: "iplant", Home: "/iplant/home", CommunityData: "/iplant/home/shared"}

	cases := []struct {
		path string
		want bool
	}{
		{"/iplant/home", true},
		{"/iplant/home/shared", true},
		{"/iplant/trash/home", true},
		{"/iplant/trash/home/wregglej", true},
		{"/iplant/home/wregglej", false},
		{"/iplant/home/wregglej/data", false},
		{"/iplant/home/someone-else", false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := layout.IsBasePath("wregglej", tc.path); got != tc.want {
				t.Errorf("IsBasePath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsWithin(t *testing.T) {
	cases := []struct {
		path, base string
		want       bool
	}{
		{"/iplant/trash/home", "/iplant/trash/home", true},
		{"/iplant/trash/home/", "/iplant/trash/home", true},
		{"/iplant/trash/home/u/x", "/iplant/trash/home", true},
		{"/iplant/trash/homework", "/iplant/trash/home", false},
		{"/iplant/trash", "/iplant/trash/home", false},
		{"/iplant/home/u", "/iplant/trash/home", false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := IsWithin(tc.path, tc.base); got != tc.want {
				t.Errorf("IsWithin(%q, %q) = %v, want %v", tc.path, tc.base, got, tc.want)
			}
		})
	}
}

func TestInTrash(t *testing.T) {
	layout := Layout{Zone: "iplant", Home: "/iplant/home", CommunityData: "/iplant/home/shared"}

	cases := []struct {
		path string
		want bool
	}{
		{"/iplant/trash/home/u/deleted.txt", true},
		{"/iplant/trash/home", true},
		{"/iplant/trash/homework/x", false},
		{"/iplant/home/u/trash", false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := layout.InTrash(tc.path); got != tc.want {
				t.Errorf("InTrash(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
