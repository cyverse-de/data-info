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

// One session for every test that performs an operation.
//
// QA's iRODS is shared -- a local DE deployment uses the same proxy account -- and it
// refuses new connections once its agents are committed. Opening a FileSystem per test
// reproduced that reliably: the first connected and the rest were rejected outright. These
// tests are not the place to find out how many connections are spare, so they use one.
var (
	sharedSessionOnce sync.Once
	sharedSession     *Session
	sharedSessionErr  error
)

func itSession(t *testing.T) *Session {
	t.Helper()

	pool := itPool(t)
	sharedSessionOnce.Do(func() {
		sharedSession, sharedSessionErr = pool.Admin(context.Background())
	})
	if sharedSessionErr != nil {
		t.Fatalf("acquiring the shared session: %v", sharedSessionErr)
	}
	return sharedSession
}

func itContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestIntegrationServerVersion(t *testing.T) {
	ctx := itContext(t)
	s := itSession(t)

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
	ctx := itContext(t)
	s := itSession(t)

	zone := os.Getenv("DATA_INFO_IT_IRODS_ZONE")
	entry, err := Stat(ctx, s, "/"+zone+"/home/definitely-not-here-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if entry.Type != ObjectTypeNone {
		t.Errorf("type = %q, want %q", entry.Type, ObjectTypeNone)
	}
}

// TestIntegrationListHome exercises a real catalog read on the shared session.
func TestIntegrationListHome(t *testing.T) {
	ctx := itContext(t)
	s := itSession(t)

	zone := os.Getenv("DATA_INFO_IT_IRODS_ZONE")
	entries, err := List(ctx, s, "/"+zone+"/home")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	t.Logf("%d entries under /%s/home", len(entries), zone)
}

// TestIntegrationListUserGroups covers the group lookup, which 4.3 changed by moving
// groups into r_user_main as rodsgroup rows.
func TestIntegrationListUserGroups(t *testing.T) {
	ctx := itContext(t)
	s := itSession(t)

	groups, err := ListUserGroups(ctx, s, os.Getenv("DATA_INFO_IT_IRODS_USER"), os.Getenv("DATA_INFO_IT_IRODS_ZONE"))
	if err != nil {
		t.Fatalf("ListUserGroups: %v", err)
	}
	if len(groups) == 0 {
		t.Error("the proxy account reports no groups; every account belongs to at least public")
	}
}

// TestIntegrationProbe runs last: it opens its own session by design, and on a server that
// is at its connection limit that is the call most likely to be refused.
func TestIntegrationProbe(t *testing.T) {
	pool := itPool(t)
	if err := pool.Probe(itContext(t)); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}
