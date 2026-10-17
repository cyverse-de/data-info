package jobs

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/icattest"
	"github.com/cyverse-de/data-info/internal/irodsclient"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
)

const (
	testZone = "iplant"
	testUser = "someone"
	testHome = "/iplant/home/someone"
)

// testScope opens a scope whose iRODS server does not exist, so any job step that reaches
// iRODS fails with a connection error rather than succeeding.
func testScope(t *testing.T) *rods.Scope {
	t.Helper()

	fake := icattest.New()
	fake.AddUser(testUser, icat.UserKindUser)
	fake.AddCollection(testHome, icat.AccessOwn)

	pool, err := irodsclient.NewPool(irodsclient.Config{
		Host: "irods.invalid", Port: 1247, Zone: testZone, ProxyUser: "rods",
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	scope, err := rods.Open(context.Background(), rods.Deps{ICAT: fake, IRODS: pool, Zone: testZone},
		rods.Options{User: testUser})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(scope.Close)
	return scope
}

func TestMoveChecksEveryDestinationFirst(t *testing.T) {
	long := strings.Repeat("n", paths.MaxFilenameLength+1)
	sources := []string{testHome + "/fine.txt", testHome + "/" + long}
	destinations := DestinationsUnder(testHome+"/dest", sources)

	var trail []string
	progress := func(path, action string) { trail = append(trail, path+": "+action) }

	err := Move{}.move(context.Background(), testScope(t), testUser, sources, destinations, progress)

	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("move = %v, want the over-long destination refused", err)
	}
	if want := []string{pathsetSeveral + ": " + actionBegin}; !slices.Equal(trail, want) {
		t.Errorf("progress = %q, want %q: nothing should move once a destination is too long", trail, want)
	}
}
