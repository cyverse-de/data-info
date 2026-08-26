package service

import (
	"context"
	"testing"

	"github.com/cyverse-de/data-info/internal/lazy"
	"github.com/cyverse-de/data-info/internal/rods"
)

// shareStore extends the permission store with what sharing also reads and writes.
type shareStore struct {
	permStore

	types    map[string]rods.ObjectType
	inheritC []string
}

func (s *shareStore) Stat(_ context.Context, path string) *lazy.Value[rods.Stat] {
	kind, ok := s.types[path]
	if !ok {
		kind = rods.ObjectTypeFile
	}
	return lazy.Resolved(rods.Stat{Exists: true, Path: path, Type: kind})
}

func (s *shareStore) SetInherit(_ context.Context, path string, inherit, _ bool) error {
	s.inheritC = append(s.inheritC, path+"="+boolText(inherit))
	return nil
}

func boolText(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func shareReq(owner, sharee, path string, level rods.Permission) ShareRequest {
	return ShareRequest{
		Owner:      owner,
		Sharee:     sharee,
		Path:       path,
		Permission: level,
		AdminUsers: map[string]bool{"rodsadmin": true},
		ProxyUser:  permProxy,
		Layout:     permLayout(),
	}
}

func TestShareSkips(t *testing.T) {
	cases := []struct {
		name   string
		owner  string
		sharee string
		path   string
		held   rods.Permission
		want   string
	}{
		{
			name: "sharing with yourself", owner: permUser, sharee: permUser,
			path: "/iplant/home/wregglej/a", want: SkipShareWithSelf,
		},
		{
			// The trash is per-user, so the sharee could not reach it even if it were
			// shared, and it is on its way out anyway.
			name: "sharing something in the trash", owner: permUser, sharee: "friend",
			path: "/iplant/trash/home/wregglej/a", want: SkipShareFromTrash,
		},
		{
			name: "already shared at exactly that level", owner: permUser, sharee: "friend",
			path: "/iplant/home/wregglej/a", held: rods.PermissionWrite, want: SkipAlreadyShared,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &shareStore{permStore: permStore{
				acls:     map[string]map[string]rods.Permission{tc.path: {tc.sharee: tc.held}},
				children: map[string][]string{},
				inherits: map[string]bool{},
			}}

			reason, err := Share(context.Background(), store, shareReq(tc.owner, tc.sharee, tc.path, rods.PermissionWrite))
			if err != nil {
				t.Fatalf("Share: %v", err)
			}
			if reason != tc.want {
				t.Errorf("reason = %q, want %q", reason, tc.want)
			}
			if len(store.changes) != 0 {
				t.Errorf("a skipped share still changed permissions: %v", store.changes)
			}
		})
	}
}

// Sharing something buried in a tree has to grant read on the way to it, or the sharee cannot
// navigate there and the file looks orphaned in the UI.
func TestShareGrantsAccessAlongThePath(t *testing.T) {
	const (
		home    = "/iplant/home/wregglej"
		project = "/iplant/home/wregglej/project"
		nested  = "/iplant/home/wregglej/project/nested"
		target  = "/iplant/home/wregglej/project/nested/thing"
	)

	store := &shareStore{permStore: permStore{
		acls:     map[string]map[string]rods.Permission{},
		children: map[string][]string{},
		inherits: map[string]bool{},
	}}

	reason, err := Share(context.Background(), store, shareReq(permUser, "friend", target, rods.PermissionWrite))
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want the share to have been performed", reason)
	}

	for _, path := range []string{home, project, nested} {
		if store.acls[path]["friend"] != rods.PermissionRead {
			t.Errorf("%s = %q, want read so the sharee can reach the target",
				path, store.acls[path]["friend"])
		}
	}
	if store.acls[target]["friend"] != rods.PermissionWrite {
		t.Errorf("target = %q, want write", store.acls[target]["friend"])
	}
}

// A collection that is shared gets its inherit bit set, or anything put into it afterwards
// would be private and the share would look like it had stopped working.
func TestSharingACollectionSetsInherit(t *testing.T) {
	const target = "/iplant/home/wregglej/project"

	store := &shareStore{
		permStore: permStore{
			acls:     map[string]map[string]rods.Permission{},
			children: map[string][]string{},
			inherits: map[string]bool{},
		},
		types: map[string]rods.ObjectType{target: rods.ObjectTypeDir},
	}

	if _, err := Share(context.Background(), store, shareReq(permUser, "friend", target, rods.PermissionRead)); err != nil {
		t.Fatalf("Share: %v", err)
	}

	if len(store.inheritC) != 1 || store.inheritC[0] != target+"=on" {
		t.Errorf("inherit changes = %v, want it turned on for %s", store.inheritC, target)
	}
}

// Access someone holds for their own sake is not downgraded by a share that only needed them
// to be able to reach something.
func TestShareDoesNotDowngradeExistingAccess(t *testing.T) {
	const (
		project = "/iplant/home/wregglej/project"
		target  = "/iplant/home/wregglej/project/thing"
	)

	store := &shareStore{permStore: permStore{
		acls: map[string]map[string]rods.Permission{
			project: {"friend": rods.PermissionWrite},
		},
		children: map[string][]string{},
		inherits: map[string]bool{},
	}}

	if _, err := Share(context.Background(), store, shareReq(permUser, "friend", target, rods.PermissionRead)); err != nil {
		t.Fatalf("Share: %v", err)
	}

	if store.acls[project]["friend"] != rods.PermissionWrite {
		t.Errorf("%s = %q, want the existing write to survive", project, store.acls[project]["friend"])
	}
}

func TestUnshare(t *testing.T) {
	const (
		home    = "/iplant/home/wregglej"
		project = "/iplant/home/wregglej/project"
		target  = "/iplant/home/wregglej/project/thing"
	)

	store := &shareStore{
		permStore: permStore{
			acls: map[string]map[string]rods.Permission{
				target:  {"friend": rods.PermissionWrite},
				project: {"friend": rods.PermissionRead},
				home:    {"friend": rods.PermissionRead},
			},
			children: map[string][]string{project: {target}, home: {project}},
			inherits: map[string]bool{},
		},
		types: map[string]rods.ObjectType{target: rods.ObjectTypeDir},
	}

	reason, err := Unshare(context.Background(), store, shareReq(permUser, "friend", target, rods.PermissionNone))
	if err != nil {
		t.Fatalf("Unshare: %v", err)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want the unshare to have been performed", reason)
	}

	if _, present := store.acls[target]["friend"]; present {
		t.Errorf("access to the target survived: %v", store.changes)
	}
	if _, present := store.acls[project]["friend"]; present {
		t.Errorf("read on %s survived although nothing in it is visible any more: %v", project, store.changes)
	}

	// Nobody else can see it now, so it should stop handing its access list to new
	// children.
	if len(store.inheritC) != 1 || store.inheritC[0] != target+"=off" {
		t.Errorf("inherit changes = %v, want it turned off for %s", store.inheritC, target)
	}
}

func TestUnshareSkips(t *testing.T) {
	cases := []struct {
		name   string
		owner  string
		sharee string
		held   rods.Permission
		want   string
	}{
		{name: "unsharing from yourself", owner: permUser, sharee: permUser, want: SkipUnshareWithSelf},
		{name: "not shared in the first place", owner: permUser, sharee: "stranger", want: SkipNotShared},
	}

	const target = "/iplant/home/wregglej/thing"

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &shareStore{permStore: permStore{
				acls:     map[string]map[string]rods.Permission{target: {tc.sharee: tc.held}},
				children: map[string][]string{},
				inherits: map[string]bool{},
			}}

			reason, err := Unshare(context.Background(), store, shareReq(tc.owner, tc.sharee, target, rods.PermissionNone))
			if err != nil {
				t.Fatalf("Unshare: %v", err)
			}
			if reason != tc.want {
				t.Errorf("reason = %q, want %q", reason, tc.want)
			}
			if len(store.changes) != 0 {
				t.Errorf("a skipped unshare still changed permissions: %v", store.changes)
			}
		})
	}
}

// The home a share has to reach through is the one the path lives under, not the requesting
// user's -- sharing something out of somebody else's collection has to grant access to theirs.
func TestOwnerHomeOf(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/iplant/home/wregglej/a/b", "/iplant/home/wregglej"},
		{"/iplant/home/other/a", "/iplant/home/other"},
		{"/iplant/home/shared/community/x", "/iplant/home/shared"},
		{"/iplant/home/wregglej", "/iplant/home/wregglej"},
		{"/iplant/home", "/iplant/home"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := ownerHomeOf(tc.path); got != tc.want {
				t.Errorf("ownerHomeOf(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
