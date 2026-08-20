package rods

import (
	"context"
	"errors"
	"testing"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/icattest"
	"github.com/cyverse-de/data-info/internal/lazy"
)

const (
	testZone = "iplant"
	testUser = "wregglej"
	testHome = "/iplant/home/wregglej"
)

func testScope(t *testing.T) (*Scope, *icattest.Fake) {
	t.Helper()

	fake := icattest.New()
	fake.AddCollection(testHome, icat.AccessOwn)
	fake.AddDataObject(testHome+"/a.txt", 100, icat.AccessRead)
	fake.AddDataObject(testHome+"/b.txt", 200, icat.AccessWrite)

	scope := Open(Deps{ICAT: fake, Zone: testZone}, Options{User: testUser})
	t.Cleanup(scope.Close)
	return scope, fake
}

func TestStat(t *testing.T) {
	scope, _ := testScope(t)
	ctx := context.Background()

	st, err := scope.Stat(ctx, testHome+"/a.txt").Get(ctx)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"exists", st.Exists, true},
		{"type", st.Type, ObjectTypeFile},
		{"size", st.Size, int64(100)},
		{"permission", st.Permission, icat.PermissionRead},
		{"created", st.CreatedMS, int64(1700000000000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

// TestStatOfAbsentPathIsNotAnError covers the contract the listing code depends on: asking
// about a path that is not there is an ordinary answer, not a failure.
func TestStatOfAbsentPathIsNotAnError(t *testing.T) {
	scope, _ := testScope(t)
	ctx := context.Background()

	st, err := scope.Stat(ctx, testHome+"/missing.txt").Get(ctx)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Exists {
		t.Error("a path that does not exist reported as existing")
	}
	if st.Type != ObjectTypeNone {
		t.Errorf("type = %q, want %q", st.Type, ObjectTypeNone)
	}
}

// TestFactsAboutOnePathCostOneQuery is the point of the memo. Formatting a listing entry
// asks for half a dozen facts about the same path; each triggering its own query would
// multiply the cost of every listing.
func TestFactsAboutOnePathCostOneQuery(t *testing.T) {
	scope, fake := testScope(t)
	ctx := context.Background()

	path := testHome + "/a.txt"

	stat := scope.Stat(ctx, path)
	objectType := scope.ObjectType(ctx, path)
	permission := scope.Permission(ctx, path)
	uuid := scope.UUID(ctx, path)

	if err := lazy.Await(ctx,
		lazy.Wait(stat), lazy.Wait(objectType), lazy.Wait(permission), lazy.Wait(uuid),
	); err != nil {
		t.Fatalf("resolving: %v", err)
	}

	if n := fake.GetItemsCalls.Load(); n != 1 {
		t.Errorf("four facts about one path cost %d queries, want 1", n)
	}
}

// TestBatchedStatsIsOneQuery is what makes the bulk endpoints viable. A thousand paths
// resolved one at a time would be a thousand round trips.
func TestBatchedStatsIsOneQuery(t *testing.T) {
	scope, fake := testScope(t)
	ctx := context.Background()

	paths := []string{testHome, testHome + "/a.txt", testHome + "/b.txt"}

	stats, err := scope.Stats(ctx, paths).Get(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if len(stats) != len(paths) {
		t.Errorf("resolved %d of %d paths", len(stats), len(paths))
	}
	if n := fake.GetItemsCalls.Load(); n != 1 {
		t.Errorf("%d paths cost %d queries, want 1", len(paths), n)
	}
	if got := fake.PathsPerGetItems[0]; got != len(paths) {
		t.Errorf("the query carried %d paths, want %d", got, len(paths))
	}
}

// TestPerPathLookupsAfterABatchAreFree is the other half: a batched load publishes its
// rows, so the per-path accessors a handler runs afterwards answer from them.
func TestPerPathLookupsAfterABatchAreFree(t *testing.T) {
	scope, fake := testScope(t)
	ctx := context.Background()

	paths := []string{testHome, testHome + "/a.txt", testHome + "/b.txt"}
	if _, err := scope.Stats(ctx, paths).Get(ctx); err != nil {
		t.Fatalf("Stats: %v", err)
	}

	before := fake.GetItemsCalls.Load()

	for _, p := range paths {
		st, err := scope.Stat(ctx, p).Get(ctx)
		if err != nil {
			t.Fatalf("Stat(%s): %v", p, err)
		}
		if !st.Exists {
			t.Errorf("%s did not resolve from the batch", p)
		}
	}

	if after := fake.GetItemsCalls.Load(); after != before {
		t.Errorf("per-path lookups after a batch cost %d extra queries, want 0", after-before)
	}
}

// TestGroupIDsAreResolvedOnce guards a lookup every permission-filtered query needs.
func TestGroupIDsAreResolvedOnce(t *testing.T) {
	scope, fake := testScope(t)
	ctx := context.Background()

	for _, p := range []string{testHome, testHome + "/a.txt", testHome + "/b.txt"} {
		if _, err := scope.Stat(ctx, p).Get(ctx); err != nil {
			t.Fatalf("Stat(%s): %v", p, err)
		}
	}

	if n := fake.UserGroupIDsCalls.Load(); n != 1 {
		t.Errorf("group ids were resolved %d times, want 1", n)
	}
}

func TestACL(t *testing.T) {
	scope, fake := testScope(t)
	ctx := context.Background()

	fake.AddPerm(testHome, "wregglej", testZone, icat.AccessOwn)
	fake.AddPerm(testHome, "someone", testZone, icat.AccessRead)

	acl, err := scope.ACL(ctx, testHome).Get(ctx)
	if err != nil {
		t.Fatalf("ACL: %v", err)
	}
	if len(acl) != 2 {
		t.Fatalf("got %d entries, want 2", len(acl))
	}

	byUser := map[string]Permission{}
	for _, e := range acl {
		byUser[e.User] = e.Permission
	}
	if byUser["wregglej"] != icat.PermissionOwn {
		t.Errorf("wregglej = %q, want own", byUser["wregglej"])
	}
	if byUser["someone"] != icat.PermissionRead {
		t.Errorf("someone = %q, want read", byUser["someone"])
	}
}

func TestACLsIsOneQuery(t *testing.T) {
	scope, fake := testScope(t)
	ctx := context.Background()

	paths := []string{testHome, testHome + "/a.txt"}
	for _, p := range paths {
		fake.AddPerm(p, "wregglej", testZone, icat.AccessOwn)
	}

	acls, err := scope.ACLs(ctx, paths).Get(ctx)
	if err != nil {
		t.Fatalf("ACLs: %v", err)
	}
	if len(acls) != len(paths) {
		t.Errorf("got %d paths, want %d", len(acls), len(paths))
	}
	if n := fake.PermsCalls.Load(); n != 1 {
		t.Errorf("%d paths cost %d queries, want 1", len(paths), n)
	}
}

// TestRepeatedACLLookupsAreMemoized covers the other memoized kind.
func TestRepeatedACLLookupsAreMemoized(t *testing.T) {
	scope, fake := testScope(t)
	ctx := context.Background()
	fake.AddPerm(testHome, "wregglej", testZone, icat.AccessOwn)

	for i := 0; i < 3; i++ {
		if _, err := scope.ACL(ctx, testHome).Get(ctx); err != nil {
			t.Fatalf("ACL: %v", err)
		}
	}
	if n := fake.PermsCalls.Load(); n != 1 {
		t.Errorf("three lookups cost %d queries, want 1", n)
	}
}

func TestErrorsPropagate(t *testing.T) {
	fake := icattest.New()
	fake.Err = errors.New("catalog is down")

	scope := Open(Deps{ICAT: fake, Zone: testZone}, Options{User: testUser})
	defer scope.Close()

	ctx := context.Background()
	if _, err := scope.Stat(ctx, testHome).Get(ctx); err == nil {
		t.Error("Stat succeeded against a failing catalog")
	}
}

// TestLookupsRunConcurrently is why the accessors start their work immediately. If each
// only began when it was read, asking for several facts about different paths would
// serialise them.
func TestLookupsRunConcurrently(t *testing.T) {
	scope, _ := testScope(t)
	ctx := context.Background()

	// Dispatch first, await second. If dispatch blocked, this would deadlock rather than
	// merely being slow.
	a := scope.Stat(ctx, testHome+"/a.txt")
	b := scope.Stat(ctx, testHome+"/b.txt")
	c := scope.Stat(ctx, testHome)

	if err := lazy.Await(ctx, lazy.Wait(a), lazy.Wait(b), lazy.Wait(c)); err != nil {
		t.Fatalf("resolving: %v", err)
	}
}
