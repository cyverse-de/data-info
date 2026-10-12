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
