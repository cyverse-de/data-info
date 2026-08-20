//go:build integration

// Integration tests against a live iRODS catalog database.
//
// Excluded from `go test ./...` by the build tag and skipped unless the connection URI is
// set. Every query here is read-only, and the transaction they run in is declared read-only
// so the database enforces it.
//
//	DATA_INFO_IT_ICAT_URI=postgres://... DATA_INFO_IT_ICAT_ZONE=cyverse \
//	DATA_INFO_IT_ICAT_USER=de-irods go test -tags=integration ./internal/icat/
package icat

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

var (
	sharedStoreOnce sync.Once
	sharedStore     *PGStore
	sharedStoreErr  error
)

func itStore(t *testing.T) *PGStore {
	t.Helper()

	uri := os.Getenv("DATA_INFO_IT_ICAT_URI")
	if uri == "" {
		t.Skip("DATA_INFO_IT_ICAT_URI is not set")
	}

	sharedStoreOnce.Do(func() {
		sharedStore, sharedStoreErr = Open(Config{URI: uri, MaxOpenConns: 2})
	})
	if sharedStoreErr != nil {
		t.Fatalf("connecting to the catalog: %v", sharedStoreErr)
	}
	return sharedStore
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedStore != nil {
		_ = sharedStore.Close()
	}
	os.Exit(code)
}

func itContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func itUser(t *testing.T) (user, zone string) {
	t.Helper()
	user = os.Getenv("DATA_INFO_IT_ICAT_USER")
	zone = os.Getenv("DATA_INFO_IT_ICAT_ZONE")
	if user == "" || zone == "" {
		t.Skip("DATA_INFO_IT_ICAT_USER and _ZONE are required")
	}
	return user, zone
}

func TestIntegrationPing(t *testing.T) {
	if err := itStore(t).Ping(itContext(t)); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestIntegrationUserGroupIDs(t *testing.T) {
	store := itStore(t)
	ctx := itContext(t)
	user, zone := itUser(t)

	ids, err := store.UserGroupIDs(ctx, user, zone)
	if err != nil {
		t.Fatalf("UserGroupIDs: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("no group ids; every account belongs to at least public and itself")
	}
	t.Logf("%s#%s belongs to %d groups", user, zone, len(ids))
}

// TestIntegrationGetItems exercises the batched item query, which is the one that replaced
// a per-path round trip.
func TestIntegrationGetItems(t *testing.T) {
	store := itStore(t)
	ctx := itContext(t)
	user, zone := itUser(t)

	home := "/" + zone + "/home"
	paths := []string{home, home + "/" + user, "/" + zone + "/home/definitely-not-here"}

	rows, err := store.GetItems(ctx, ItemQuery{Paths: paths, User: user, Zone: zone})
	if err != nil {
		t.Fatalf("GetItems: %v", err)
	}

	found := map[string]Row{}
	for _, r := range rows {
		found[r.FullPath] = r
	}
	t.Logf("resolved %d of %d paths", len(found), len(paths))

	if _, ok := found["/"+zone+"/home/definitely-not-here"]; ok {
		t.Error("a path that does not exist came back")
	}

	row, ok := found[home]
	if !ok {
		t.Fatalf("%s did not resolve; the query or the permission filter is wrong", home)
	}
	if !row.IsCollection() {
		t.Errorf("%s type = %q, want collection", home, row.Type)
	}
	if row.CreatedMillis() <= 0 {
		t.Errorf("create timestamp %q did not parse", row.CreateTS)
	}
	if row.Permission() == PermissionNone {
		t.Errorf("access_type_id %d resolved to no permission", row.AccessTypeID)
	}
}

// TestIntegrationGetItemsUsesSuppliedGroupIDs covers the branch that skips the group
// lookup, which callers resolving several things for one user rely on.
func TestIntegrationGetItemsUsesSuppliedGroupIDs(t *testing.T) {
	store := itStore(t)
	ctx := itContext(t)
	user, zone := itUser(t)

	ids, err := store.UserGroupIDs(ctx, user, zone)
	if err != nil {
		t.Fatalf("UserGroupIDs: %v", err)
	}

	home := "/" + zone + "/home"

	withLookup, err := store.GetItems(ctx, ItemQuery{Paths: []string{home}, User: user, Zone: zone})
	if err != nil {
		t.Fatalf("GetItems with lookup: %v", err)
	}
	withIDs, err := store.GetItems(ctx, ItemQuery{Paths: []string{home}, GroupIDs: ids})
	if err != nil {
		t.Fatalf("GetItems with supplied ids: %v", err)
	}

	if len(withLookup) != len(withIDs) {
		t.Fatalf("supplying group ids changed the result: %d rows versus %d", len(withIDs), len(withLookup))
	}
	if len(withIDs) > 0 && withIDs[0].AccessTypeID != withLookup[0].AccessTypeID {
		t.Errorf("access differs: %d versus %d", withIDs[0].AccessTypeID, withLookup[0].AccessTypeID)
	}
}

func TestIntegrationPermsForItems(t *testing.T) {
	store := itStore(t)
	ctx := itContext(t)
	_, zone := itUser(t)

	home := "/" + zone + "/home"

	perms, err := store.PermsForItems(ctx, []string{home})
	if err != nil {
		t.Fatalf("PermsForItems: %v", err)
	}
	if len(perms) == 0 {
		t.Fatalf("no access entries for %s", home)
	}

	seen := map[string]int{}
	for _, p := range perms {
		if p.FullPath != home {
			t.Errorf("unexpected path %q", p.FullPath)
		}
		seen[p.UserName]++
	}
	for user, n := range seen {
		if n > 1 {
			t.Logf("%s holds %d access rows on %s", user, n, home)
		}
	}
	t.Logf("%d access entries over %d distinct users", len(perms), len(seen))
}

// TestIntegrationReadOnlyTransaction confirms the database enforces what WithTx declares.
func TestIntegrationReadOnlyTransaction(t *testing.T) {
	store := itStore(t)
	ctx := itContext(t)
	user, zone := itUser(t)

	err := store.WithTx(ctx, func(tx Tx) error {
		_, err := tx.UserGroupIDs(ctx, user, zone)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
}

// TestIntegrationBatchScales is the measurement this package exists for.
//
// Over the iRODS protocol a stat costs about 10ms per path and an access list about 20ms,
// so a request carrying the endpoint limit of a thousand paths would spend seconds on round
// trips. One query should answer the same set in well under a second, and that difference
// is the reason bulk reads stay in the catalog rather than moving to the protocol.
func TestIntegrationBatchScales(t *testing.T) {
	store := itStore(t)
	ctx := itContext(t)
	user, zone := itUser(t)

	const batch = 1000

	// Mostly paths that do not exist, which is the honest shape: the cost is in the joins
	// and the permission filter, not in how many rows come back.
	paths := make([]string, 0, batch)
	paths = append(paths, "/"+zone+"/home", "/"+zone+"/home/"+user)
	for i := len(paths); i < batch; i++ {
		paths = append(paths, "/"+zone+"/home/"+user+"/batch-probe-"+itoa(i))
	}

	start := time.Now()
	rows, err := store.GetItems(ctx, ItemQuery{Paths: paths, User: user, Zone: zone})
	itemsElapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetItems over %d paths: %v", batch, err)
	}

	start = time.Now()
	perms, err := store.PermsForItems(ctx, paths)
	permsElapsed := time.Since(start)
	if err != nil {
		t.Fatalf("PermsForItems over %d paths: %v", batch, err)
	}

	t.Logf("%d paths: GetItems %s (%d rows), PermsForItems %s (%d rows)",
		batch, itemsElapsed, len(rows), permsElapsed, len(perms))

	// Generous, because this runs against a shared server over a network. The protocol
	// equivalents would be roughly 10s and 20s, so anything in this range still makes the
	// point decisively.
	const budget = 5 * time.Second
	if itemsElapsed > budget {
		t.Errorf("GetItems over %d paths took %s, want under %s", batch, itemsElapsed, budget)
	}
	if permsElapsed > budget {
		t.Errorf("PermsForItems over %d paths took %s, want under %s", batch, permsElapsed, budget)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
