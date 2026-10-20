package service

import (
	"testing"

	"github.com/cyverse-de/data-info/internal/rods"
)

func TestIsReservedAVU(t *testing.T) {
	cases := []struct {
		attribute string
		want      bool
	}{
		{"ipc-trash-origin", true},
		{"ipc_UUID", true},
		{"IPC-Filetype", true},
		{"ipc", true},
		{"ipcsomething", true},
		{"author", false},
		{"my-ipc-notes", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.attribute, func(t *testing.T) {
			if got := IsReservedAVU(tc.attribute); got != tc.want {
				t.Errorf("IsReservedAVU(%q) = %v, want %v", tc.attribute, got, tc.want)
			}
		})
	}
}

func TestBlankUnitsRoundTrip(t *testing.T) {
	cases := []struct {
		name       string
		in         AVU
		wantStored string
		wantBack   string
	}{
		{name: "a blank unit", in: AVU{Attribute: "a", Value: "b"}, wantStored: ReservedUnit},
		{name: "only whitespace", in: AVU{Attribute: "a", Value: "b", Unit: "  "}, wantStored: ReservedUnit},
		{
			name: "a real unit", in: AVU{Attribute: "a", Value: "b", Unit: "kg"},
			wantStored: "kg", wantBack: "kg",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := StoredAVU(tc.in)
			if stored.Unit != tc.wantStored {
				t.Errorf("stored unit = %q, want %q", stored.Unit, tc.wantStored)
			}
			if back := VisibleAVU(stored); back.Unit != tc.wantBack {
				t.Errorf("reported unit = %q, want %q", back.Unit, tc.wantBack)
			}
		})
	}
}

func TestVisibleAVUs(t *testing.T) {
	held := []rods.AVU{
		{Attribute: "author", Value: "someone", Unit: ReservedUnit},
		{Attribute: "ipc-trash-origin", Value: "/z/home/u/a", Unit: SystemUnit},
		{Attribute: "size", Value: "10", Unit: "kg"},
	}

	visible := VisibleAVUs(held, false)
	if len(visible) != 2 {
		t.Fatalf("visible = %+v, want the two that are not the DE's", visible)
	}
	if visible[0].Unit != "" {
		t.Errorf("the reserved unit was reported as %q, want it blank", visible[0].Unit)
	}

	if all := VisibleAVUs(held, true); len(all) != 3 {
		t.Errorf("with reserved included = %+v, want all three", all)
	}
}

func TestMetadataTargetType(t *testing.T) {
	if got := MetadataTargetType(rods.ObjectTypeDir); got != "folder" {
		t.Errorf("a collection maps to %q, want %q", got, "folder")
	}
	if got := MetadataTargetType(rods.ObjectTypeFile); got != "file" {
		t.Errorf("a data object maps to %q, want %q", got, "file")
	}
}
