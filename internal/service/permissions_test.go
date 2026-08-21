package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/lazy"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
)

// permStore is an in-memory stand-in for the parts of the data store a repair touches.
type permStore struct {
	// acls maps a path to who can see it and how.
	acls map[string]map[string]rods.Permission

	// inherits names the collections that pass their access list down.
	inherits map[string]bool

	// children maps a collection to what it holds.
	children map[string][]string

	// changes records every permission change, in order, so a test can assert on what was
	// done rather than only on the end state.
	changes []string
}

func (p *permStore) ACL(_ context.Context, path string) *lazy.Value[[]rods.ACLEntry] {
	entries := make([]rods.ACLEntry, 0, len(p.acls[path]))
	for user, level := range p.acls[path] {
		entries = append(entries, rods.ACLEntry{User: user, Permission: level})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].User < entries[j].User })

	return lazy.Resolved(entries)
}

func (p *permStore) ACLs(_ context.Context, requested []string) *lazy.Value[map[string][]rods.ACLEntry] {
	out := map[string][]rods.ACLEntry{}
	for _, path := range requested {
		for user, level := range p.acls[path] {
			out[path] = append(out[path], rods.ACLEntry{User: user, Permission: level})
		}
	}
	return lazy.Resolved(out)
}

func (p *permStore) Inherits(_ context.Context, path string) (bool, error) {
	return p.inherits[path], nil
}

func (p *permStore) Children(_ context.Context, path string) ([]string, error) {
	return p.children[path], nil
}

func (p *permStore) SetPermission(_ context.Context, path, user string, level rods.Permission, recurse bool) error {
	p.changes = append(p.changes, fmt.Sprintf("%s %s=%s recurse=%v", path, user, level, recurse))

	if p.acls[path] == nil {
		p.acls[path] = map[string]rods.Permission{}
	}
	if level == rods.PermissionNone {
		delete(p.acls[path], user)
		return nil
	}
	p.acls[path][user] = level
	return nil
}

const (
	permZone  = "iplant"
	permUser  = "wregglej"
	permProxy = "rods"
)

func permLayout() paths.Layout {
	return paths.Layout{Zone: permZone, Home: "/iplant/home", CommunityData: "/iplant/home/shared"}
}

func permMove(source, destination string) MoveContext {
	return MoveContext{
		Source:      source,
		Destination: destination,
		User:        permUser,
		ProxyUser:   permProxy,
		AdminUsers:  map[string]bool{"rodsadmin": true},
		Layout:      permLayout(),
	}
}

// The four cases are not symmetric, and which one applies is decided by the two collections'
// inheritance flags rather than by anything about the object being moved.
func TestRepairPermissionsByInheritance(t *testing.T) {
	const (
		from = "/iplant/home/wregglej/from"
		to   = "/iplant/home/wregglej/to"
		item = "/iplant/home/wregglej/to/thing"
	)

	cases := []struct {
		name              string
		sourceInherits    bool
		destInherits      bool
		wantOnItem        map[string]rods.Permission
		wantAncestorGrant bool
	}{
		{
			// The destination hands out its own list, so what arrived is replaced by it.
			name:           "both inherit",
			sourceInherits: true,
			destInherits:   true,
			wantOnItem:     map[string]rods.Permission{"friend": rods.PermissionWrite},
		},
		{
			// It arrived carrying the source's list and the destination grants nothing, so
			// nobody else should be left on it.
			name:           "the source inherits and the destination does not",
			sourceInherits: true,
			wantOnItem:     map[string]rods.Permission{},
		},
		{
			name:         "the destination inherits and the source does not",
			destInherits: true,
			wantOnItem:   map[string]rods.Permission{"friend": rods.PermissionWrite},
		},
		{
			// Neither grants anything, so the object keeps who it arrived with -- and they
			// need to be able to reach it.
			name:              "neither inherits",
			wantOnItem:        map[string]rods.Permission{"stranger": rods.PermissionRead},
			wantAncestorGrant: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &permStore{
				acls: map[string]map[string]rods.Permission{
					// Who the object arrived carrying.
					item: {"stranger": rods.PermissionRead, permUser: rods.PermissionOwn},
					// Who can see the destination collection.
					to: {"friend": rods.PermissionWrite, permUser: rods.PermissionOwn},
					// Who could see where it came from.
					from: {"stranger": rods.PermissionRead, permUser: rods.PermissionOwn},
				},
				inherits: map[string]bool{from: tc.sourceInherits, to: tc.destInherits},
				children: map[string][]string{},
			}

			err := RepairPermissions(context.Background(), store, permMove(from+"/thing", item))
			if err != nil {
				t.Fatalf("RepairPermissions: %v", err)
			}

			for user, want := range tc.wantOnItem {
				if got := store.acls[item][user]; got != want {
					t.Errorf("%s on %s = %q, want %q (changes: %v)", user, item, got, want, store.changes)
				}
			}
			// The requesting user's own access is never touched.
			if store.acls[item][permUser] != rods.PermissionOwn {
				t.Errorf("the requesting user's own access was changed: %v", store.changes)
			}

			granted := false
			for _, change := range store.changes {
				if strings.HasPrefix(change, "/iplant/home/wregglej ") {
					granted = true
				}
			}
			if granted != tc.wantAncestorGrant {
				t.Errorf("granted access on an ancestor = %v, want %v (changes: %v)",
					granted, tc.wantAncestorGrant, store.changes)
			}
		})
	}
}

// A move within one collection changes nothing about who can reach what, so it must not cost
// a single permission call -- an upload staging through a temporary name relies on that.
func TestRepairPermissionsSkipsAMoveInsideOneCollection(t *testing.T) {
	store := &permStore{
		acls:     map[string]map[string]rods.Permission{},
		inherits: map[string]bool{},
		children: map[string][]string{},
	}

	move := permMove("/iplant/home/wregglej/a", "/iplant/home/wregglej/b")
	if err := RepairPermissions(context.Background(), store, move); err != nil {
		t.Fatalf("RepairPermissions: %v", err)
	}

	if len(store.changes) != 0 {
		t.Errorf("changes = %v, want none", store.changes)
	}
}

// Access granted only so somebody could reach the thing that moved is taken back; access
// they hold for their own sake is not.
func TestObsoleteAccessIsRemovedOnlyWhenItIsMerelyRead(t *testing.T) {
	const (
		project = "/iplant/home/wregglej/project"
		nested  = "/iplant/home/wregglej/project/nested"
		source  = "/iplant/home/wregglej/project/nested/thing"
		dest    = "/iplant/home/other/thing"
	)

	store := &permStore{
		acls: map[string]map[string]rods.Permission{
			dest:                 {"reader": rods.PermissionRead, "editor": rods.PermissionWrite},
			nested:               {"reader": rods.PermissionRead, "editor": rods.PermissionWrite},
			project:              {"reader": rods.PermissionRead, "editor": rods.PermissionWrite},
			"/iplant/home/other": {},
		},
		inherits: map[string]bool{},
		children: map[string][]string{nested: {}, project: {}},
	}

	move := permMove(source, dest)
	if err := RepairPermissions(context.Background(), store, move); err != nil {
		t.Fatalf("RepairPermissions: %v", err)
	}

	// The reader could only ever reach the moved object, and there is nothing else left
	// they can see, so their read on both collections goes.
	if _, present := store.acls[nested]["reader"]; present {
		t.Errorf("the reader kept access to %s: %v", nested, store.changes)
	}
	if _, present := store.acls[project]["reader"]; present {
		t.Errorf("the reader kept access to %s: %v", project, store.changes)
	}

	// The editor's write was granted for its own sake and survives.
	if store.acls[nested]["editor"] != rods.PermissionWrite {
		t.Errorf("the editor's write on %s was removed: %v", nested, store.changes)
	}
}

// The walk stops at the first collection that still holds something the sharee can see:
// above that point their access is doing work for something else.
func TestObsoleteAccessStopsWhereSomethingElseIsVisible(t *testing.T) {
	const (
		project = "/iplant/home/wregglej/project"
		nested  = "/iplant/home/wregglej/project/nested"
		sibling = "/iplant/home/wregglej/project/nested/other"
		source  = "/iplant/home/wregglej/project/nested/thing"
		dest    = "/iplant/home/other/thing"
	)

	store := &permStore{
		acls: map[string]map[string]rods.Permission{
			dest:    {"reader": rods.PermissionRead},
			nested:  {"reader": rods.PermissionRead},
			project: {"reader": rods.PermissionRead},
			sibling: {"reader": rods.PermissionRead},
		},
		inherits: map[string]bool{},
		children: map[string][]string{nested: {sibling}, project: {nested}},
	}

	move := permMove(source, dest)
	if err := RepairPermissions(context.Background(), store, move); err != nil {
		t.Fatalf("RepairPermissions: %v", err)
	}

	if store.acls[nested]["reader"] != rods.PermissionRead {
		t.Errorf("access to %s was removed although the reader can still see something in it: %v",
			nested, store.changes)
	}
	if store.acls[project]["reader"] != rods.PermissionRead {
		t.Errorf("the walk climbed past a collection it should have stopped at: %v", store.changes)
	}
}

// Reaching something inside the user's own home is the point of the grant, so that walk does
// not stop there -- but taking access away does, or it would undo sharing set up on purpose.
func TestAncestorWalksStopInDifferentPlaces(t *testing.T) {
	home := permLayout().UserHome(permUser)

	granting := ancestorsOf("/iplant/home/wregglej/a/b", func(path string) bool {
		return permLayout().IsSharing(path) || permLayout().IsTrashBase(path)
	})
	if len(granting) == 0 || granting[len(granting)-1] != home {
		t.Errorf("granting walk = %v, want it to reach %s", granting, home)
	}

	removing := ancestorsOf("/iplant/home/wregglej/a/b", func(path string) bool {
		return permLayout().IsSharing(path) || permLayout().IsTrashBase(path) || path == home
	})
	for _, path := range removing {
		if path == home {
			t.Errorf("removing walk reached %s, which it must not strip", home)
		}
	}
}

// Accounts whose access is structural rather than shared are left alone everywhere.
func TestStructuralAccountsAreNeverTouched(t *testing.T) {
	const (
		from = "/iplant/home/wregglej/from"
		item = "/iplant/home/wregglej/to/thing"
		to   = "/iplant/home/wregglej/to"
	)

	store := &permStore{
		acls: map[string]map[string]rods.Permission{
			item: {permProxy: rods.PermissionOwn, "rodsadmin": rods.PermissionOwn, permUser: rods.PermissionOwn},
			to:   {permProxy: rods.PermissionOwn},
			from: {permProxy: rods.PermissionOwn},
		},
		inherits: map[string]bool{to: true},
		children: map[string][]string{},
	}

	if err := RepairPermissions(context.Background(), store, permMove(from+"/thing", item)); err != nil {
		t.Fatalf("RepairPermissions: %v", err)
	}

	if len(store.changes) != 0 {
		t.Errorf("changes = %v, want none: every entry belonged to a structural account", store.changes)
	}
}
