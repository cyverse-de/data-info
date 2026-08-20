//go:build integration

// Integration tests against a live iRODS server.
//
// These are excluded from `go test ./...` by the build tag and skipped unless the
// connection variables are set, so neither a stray tag nor a stray environment is enough
// on its own to run them.
//
//	DATA_INFO_IT_IRODS_HOST=... DATA_INFO_IT_IRODS_USER=... DATA_INFO_IT_IRODS_PASSWORD=... \
//	DATA_INFO_IT_IRODS_ZONE=... go test -tags=integration ./internal/irodsclient/
//
// Everything here is read-only. Tests that create or delete anything belong with the write
// path and must carry the scratch-collection guardrails described in the plan, because the
// zone these run against is shared.
package irodsclient

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// productionHosts must never be touched by a test, whatever the environment says.
var productionHosts = []string{"data.cyverse.org"}

// One pool for the whole package. Each test building its own would authenticate again and
// churn connections against a server other people share, which is enough on its own to get
// new connections rejected part way through a run.
var (
	sharedPoolOnce sync.Once
	sharedPool     *Pool
	sharedPoolErr  error
	sharedPoolSkip string
)

func itPool(t *testing.T) *Pool {
	t.Helper()

	sharedPoolOnce.Do(func() { sharedPool, sharedPoolErr, sharedPoolSkip = buildITPool() })

	if sharedPoolSkip != "" {
		t.Skip(sharedPoolSkip)
	}
	if sharedPoolErr != nil {
		t.Fatalf("building the shared pool: %v", sharedPoolErr)
	}
	return sharedPool
}

// buildITPool returns the pool, a fatal error, or a reason to skip.
func buildITPool() (*Pool, error, string) {
	host := os.Getenv("DATA_INFO_IT_IRODS_HOST")
	if host == "" {
		return nil, nil, "DATA_INFO_IT_IRODS_HOST is not set"
	}

	// A refusal rather than a skip: if someone points these at production, they should
	// find out loudly rather than see a green run that quietly did nothing.
	for _, forbidden := range productionHosts {
		if strings.EqualFold(host, forbidden) {
			return nil, fmt.Errorf("refusing to run against %s", host), ""
		}
	}

	port := 1247
	if raw := os.Getenv("DATA_INFO_IT_IRODS_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("DATA_INFO_IT_IRODS_PORT is not a number: %w", err), ""
		}
		port = parsed
	}

	cfg := Config{
		Host:          host,
		Port:          port,
		Zone:          os.Getenv("DATA_INFO_IT_IRODS_ZONE"),
		ProxyUser:     os.Getenv("DATA_INFO_IT_IRODS_USER"),
		ProxyPassword: os.Getenv("DATA_INFO_IT_IRODS_PASSWORD"),
		AppName:       "data-info-integration-test",
	}
	if cfg.Zone == "" || cfg.ProxyUser == "" || cfg.ProxyPassword == "" {
		return nil, nil, "DATA_INFO_IT_IRODS_ZONE, _USER and _PASSWORD are all required"
	}

	pool, err := NewPool(cfg)
	if err != nil {
		return nil, err, ""
	}
	return pool, nil, ""
}

// TestMain tears the shared pool down once, after every test has finished with it.
func TestMain(m *testing.M) {
	code := m.Run()
	if sharedPool != nil {
		sharedPool.Close()
	}
	os.Exit(code)
}

func itContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestIntegrationProbe(t *testing.T) {
	pool := itPool(t)
	if err := pool.Probe(itContext(t)); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}

func TestIntegrationServerVersion(t *testing.T) {
	pool := itPool(t)
	ctx := itContext(t)

	s, err := pool.Admin(ctx)
	if err != nil {
		t.Fatalf("Admin: %v", err)
	}
	defer s.Close()

	version, err := ServerVersion(ctx, s)
	if err != nil {
		t.Fatalf("ServerVersion: %v", err)
	}
	if version == "" {
		t.Error("empty server version")
	}
	t.Logf("iRODS %s", version)
}

// TestIntegrationStatMissingPathIsNotAnError covers the accessor contract the listing code
// depends on: a path that is not there reports as absent rather than failing.
func TestIntegrationStatMissingPathIsNotAnError(t *testing.T) {
	pool := itPool(t)
	ctx := itContext(t)

	s, err := pool.Admin(ctx)
	if err != nil {
		t.Fatalf("Admin: %v", err)
	}
	defer s.Close()

	zone := os.Getenv("DATA_INFO_IT_IRODS_ZONE")
	entry, err := Stat(ctx, s, "/"+zone+"/home/definitely-not-here-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if entry.Type != ObjectTypeNone {
		t.Errorf("type = %q, want %q", entry.Type, ObjectTypeNone)
	}
}

// TestIntegrationSessionsAreKeyedByUser checks the reason the pool exists: the client user
// is fixed when a connection is established, so two users must not share a session.
func TestIntegrationSessionsAreKeyedByUser(t *testing.T) {
	pool := itPool(t)
	ctx := itContext(t)

	admin, err := pool.Admin(ctx)
	if err != nil {
		t.Fatalf("Admin: %v", err)
	}
	defer admin.Close()

	proxyUser := os.Getenv("DATA_INFO_IT_IRODS_USER")
	asUser, err := pool.ForUser(ctx, proxyUser)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	defer asUser.Close()

	if admin.FileSystem() == asUser.FileSystem() {
		t.Error("the admin and client-user sessions share a FileSystem; the client user cannot differ")
	}
	if admin.ClientUser() != "" {
		t.Errorf("admin client user = %q, want empty", admin.ClientUser())
	}
	if asUser.ClientUser() != proxyUser {
		t.Errorf("client user = %q, want %q", asUser.ClientUser(), proxyUser)
	}
}

// TestIntegrationSessionsAreReused checks that a second request for the same user gets the
// pooled session rather than paying for another authentication round trip.
func TestIntegrationSessionsAreReused(t *testing.T) {
	pool := itPool(t)
	ctx := itContext(t)

	first, err := pool.Admin(ctx)
	if err != nil {
		t.Fatalf("Admin: %v", err)
	}
	fs := first.FileSystem()
	first.Close()

	second, err := pool.Admin(ctx)
	if err != nil {
		t.Fatalf("Admin: %v", err)
	}
	defer second.Close()

	if second.FileSystem() != fs {
		t.Error("the session was rebuilt rather than reused")
	}
}
