package service

import (
	"context"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/lazy"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
)

// restoreStore is a stand-in for the parts of the data store a restore plan reads.
type restoreStore struct {
	stats map[string]rods.Stat
	avus  map[string][]rods.AVU
}

func (r *restoreStore) Stat(_ context.Context, path string) *lazy.Value[rods.Stat] {
	return lazy.Resolved(r.stats[path])
}

func (r *restoreStore) AVUs(_ context.Context, path string) *lazy.Value[[]rods.AVU] {
	return lazy.Resolved(r.avus[path])
}

func trashLayout() paths.Layout {
	return paths.Layout{Zone: "iplant", Home: "/iplant/home", CommunityData: "/iplant/home/shared"}
}

// The suffix exists so that two things with the same name deleted from different collections
// do not collide in the trash.
func TestTrashPathFor(t *testing.T) {
	layout := trashLayout()

	first, err := TrashPathFor(layout, "wregglej", "/iplant/home/wregglej/a/results.csv")
	if err != nil {
		t.Fatalf("TrashPathFor: %v", err)
	}
	second, err := TrashPathFor(layout, "wregglej", "/iplant/home/wregglej/b/results.csv")
	if err != nil {
		t.Fatalf("TrashPathFor: %v", err)
	}

	if first == second {
		t.Fatalf("two trash paths collided: %q", first)
	}

	for _, got := range []string{first, second} {
		if dir := paths.Dir(got); dir != layout.UserTrash("wregglej") {
			t.Errorf("collection = %q, want the user's trash", dir)
		}
		name := paths.Base(got)
		if !strings.HasPrefix(name, "results.csv.") {
			t.Errorf("name = %q, want the original name plus a suffix", name)
		}
		if suffix := strings.TrimPrefix(name, "results.csv."); len(suffix) != trashSuffixLength {
			t.Errorf("suffix = %q, want %d characters", suffix, trashSuffixLength)
		}
	}
}

func TestPlanRestore(t *testing.T) {
	const (
		trashed = "/iplant/trash/home/wregglej/results.csv.AbC1234"
		origin  = "/iplant/home/wregglej/project/results.csv"
	)

	cases := []struct {
		name        string
		avus        []rods.AVU
		stats       map[string]rods.Stat
		wantPath    string
		wantPartial bool
	}{
		{
			name:     "back where it came from",
			avus:     []rods.AVU{{Attribute: TrashOriginAttribute, Value: origin}},
			stats:    map[string]rods.Stat{"/iplant/home/wregglej/project": {Exists: true, Permission: rods.PermissionOwn}},
			wantPath: origin,
		},
		{
			// Nothing recorded where it came from, which happens for things trashed before
			// the attribute existed or by something other than this service.
			name:        "nowhere recorded to go back to",
			stats:       map[string]rods.Stat{},
			wantPath:    "/iplant/home/wregglej/results.csv.AbC1234",
			wantPartial: true,
		},
		{
			// Shared with them once and not any more. Putting it back would mean writing
			// somewhere they cannot.
			name: "the collection it came from is no longer writeable",
			avus: []rods.AVU{{Attribute: TrashOriginAttribute, Value: origin}},
			stats: map[string]rods.Stat{
				"/iplant/home/wregglej/project": {Exists: true, Permission: rods.PermissionRead},
			},
			wantPath:    "/iplant/home/wregglej/results.csv.AbC1234",
			wantPartial: true,
		},
		{
			// A missing parent is recreated by the job, so it is not a reason to divert.
			name:     "the collection it came from is gone",
			avus:     []rods.AVU{{Attribute: TrashOriginAttribute, Value: origin}},
			stats:    map[string]rods.Stat{},
			wantPath: origin,
		},
		{
			// Restoring must never overwrite, so a name in use gets a number.
			name: "something is already using the name",
			avus: []rods.AVU{{Attribute: TrashOriginAttribute, Value: origin}},
			stats: map[string]rods.Stat{
				"/iplant/home/wregglej/project": {Exists: true, Permission: rods.PermissionOwn},
				origin:                          {Exists: true},
				origin + ".0":                   {Exists: true},
			},
			wantPath: origin + ".1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &restoreStore{stats: tc.stats, avus: map[string][]rods.AVU{trashed: tc.avus}}

			plan, err := PlanRestore(context.Background(), store, trashLayout(), "wregglej", trashed)
			if err != nil {
				t.Fatalf("PlanRestore: %v", err)
			}

			if plan.RestoredPath != tc.wantPath {
				t.Errorf("restored path = %q, want %q", plan.RestoredPath, tc.wantPath)
			}
			if plan.PartialRestore != tc.wantPartial {
				t.Errorf("partial restore = %v, want %v", plan.PartialRestore, tc.wantPartial)
			}
		})
	}
}
