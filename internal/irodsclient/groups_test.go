package irodsclient

import (
	"slices"
	"testing"
)

func TestWithoutGroup(t *testing.T) {
	tests := []struct {
		name    string
		members []string
		want    []string
	}{
		{"the group's own row is dropped", []string{"alice", "@grouper-x", "bob"}, []string{"alice", "bob"}},
		{"only the group itself", []string{"@grouper-x"}, []string{}},
		{"no own row", []string{"alice", "bob"}, []string{"alice", "bob"}},
		{"a member whose name contains the group's is kept", []string{"@grouper-x2"}, []string{"@grouper-x2"}},
		{"empty", []string{}, []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withoutGroup(tt.members, "@grouper-x"); !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
