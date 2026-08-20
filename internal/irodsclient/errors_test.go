package irodsclient

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	irodsfs "github.com/cyverse/go-irodsclient/fs"
	"github.com/cyverse/go-irodsclient/irods/common"
	"github.com/cyverse/go-irodsclient/irods/types"
)

func TestTranslate(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   apierror.Code
		wantStatus int
		wantExtra  map[string]any
	}{
		{
			name:       "missing path",
			err:        types.NewFileNotFoundError("/a/b"),
			wantCode:   apierror.ErrDoesNotExist,
			wantStatus: http.StatusInternalServerError, // preserved wart; see internal/apierror
			wantExtra:  map[string]any{"path": "/a/b"},
		},
		{
			name:      "path already exists",
			err:       types.NewFileAlreadyExistError("/a/b"),
			wantCode:  apierror.ErrExists,
			wantExtra: map[string]any{"path": "/a/b"},
		},
		{
			name:      "unknown account",
			err:       types.NewUserNotFoundError("nobody"),
			wantCode:  apierror.ErrNotAUser,
			wantExtra: map[string]any{"user": "wregglej"},
		},
		{
			name:     "catalog reports an unknown collection",
			err:      types.NewIRODSError(common.CAT_UNKNOWN_COLLECTION),
			wantCode: apierror.ErrDoesNotExist,
		},
		{
			name:     "catalog reports a name clash",
			err:      types.NewIRODSError(common.CAT_NAME_EXISTS_AS_DATAOBJ),
			wantCode: apierror.ErrExists,
		},
		{
			// Deliberately not ERR_FORBIDDEN: that would answer 403 where the Clojure
			// validators answer 500 for the same condition.
			name:     "catalog refuses access",
			err:      types.NewIRODSError(common.CAT_NO_ACCESS_PERMISSION),
			wantCode: apierror.ErrNotReadable,
		},
		{
			name:     "an unmapped catalog code",
			err:      types.NewIRODSError(common.SYS_INTERNAL_ERR),
			wantCode: apierror.ErrRequestFailed,
		},
		{
			name:     "a plain error",
			err:      errors.New("something else"),
			wantCode: apierror.ErrRequestFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Translate(tt.err, pathOf(tt.wantExtra), "wregglej")

			var apiErr *apierror.Error
			if !errors.As(got, &apiErr) {
				t.Fatalf("Translate returned %T, want *apierror.Error", got)
			}
			if apiErr.Code != tt.wantCode {
				t.Errorf("code = %s, want %s", apiErr.Code, tt.wantCode)
			}
			if tt.wantStatus != 0 && apiErr.HTTPStatus() != tt.wantStatus {
				t.Errorf("status = %d, want %d", apiErr.HTTPStatus(), tt.wantStatus)
			}
			for k, want := range tt.wantExtra {
				if apiErr.Extra[k] != want {
					t.Errorf("extra[%q] = %v, want %v", k, apiErr.Extra[k], want)
				}
			}
			if !errors.Is(got, tt.err) {
				t.Error("the original error is not reachable through the translated one")
			}
		})
	}
}

func pathOf(extra map[string]any) string {
	if p, ok := extra["path"].(string); ok {
		return p
	}
	return ""
}

// TestTranslateConnectionErrors covers the analogue of catch-jargon-io-exceptions: a
// failure to reach iRODS at all is ERR_UNAVAILABLE, not a failure of the request.
func TestTranslateConnectionErrors(t *testing.T) {
	for _, err := range []error{
		types.NewConnectionError(),
		types.NewConnectionPoolFullError(0, 0),
		types.NewAuthError(nil),
	} {
		got := Translate(err, "/a", "wregglej")
		if !IsUnavailable(got) {
			t.Errorf("%T translated to %v, want ERR_UNAVAILABLE", err, got)
		}
	}
}

// TestTranslateKeepsOurOwnErrors makes sure a validator's error passes through unchanged
// rather than being reclassified as a request failure.
func TestTranslateKeepsOurOwnErrors(t *testing.T) {
	original := apierror.New(apierror.ErrNotOwner).With("path", "/a")
	got := Translate(original, "/b", "someone")

	var apiErr *apierror.Error
	if !errors.As(got, &apiErr) {
		t.Fatalf("got %T", got)
	}
	if apiErr.Code != apierror.ErrNotOwner {
		t.Errorf("code = %s, want %s", apiErr.Code, apierror.ErrNotOwner)
	}
	if apiErr.Extra["path"] != "/a" {
		t.Errorf("path = %v, want /a; the original error was rewritten", apiErr.Extra["path"])
	}
}

func TestPermissionMapping(t *testing.T) {
	tests := []struct {
		perm  Permission
		irods types.IRODSAccessLevelType
	}{
		{PermissionOwn, types.IRODSAccessLevelOwner},
		{PermissionWrite, types.IRODSAccessLevelModifyObject},
		{PermissionRead, types.IRODSAccessLevelReadObject},
		{PermissionNone, types.IRODSAccessLevelNull},
	}

	for _, tt := range tests {
		t.Run(string(tt.perm), func(t *testing.T) {
			if got := toIRODSAccessLevel(tt.perm); got != tt.irods {
				t.Errorf("toIRODSAccessLevel(%q) = %q, want %q", tt.perm, got, tt.irods)
			}
			if got := fromIRODSAccessLevel(tt.irods); got != tt.perm {
				t.Errorf("fromIRODSAccessLevel(%q) = %q, want %q", tt.irods, got, tt.perm)
			}
		})
	}
}

// TestPermissionMappingNormalisesSpellings covers the forms iRODS actually reports, which
// vary by server version and are not always the canonical constant.
func TestPermissionMappingNormalisesSpellings(t *testing.T) {
	tests := []struct {
		level string
		want  Permission
	}{
		{"read_object", PermissionRead},
		{"read object", PermissionRead},
		{"modify_object", PermissionWrite},
		{"modify object", PermissionWrite},
		{"own", PermissionOwn},
		{"null", PermissionNone},
		// Finer-grained levels the DE does not expose report as no access.
		{"read_metadata", PermissionNone},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			if got := fromIRODSAccessLevel(types.IRODSAccessLevelType(tt.level)); got != tt.want {
				t.Errorf("fromIRODSAccessLevel(%q) = %q, want %q", tt.level, got, tt.want)
			}
		})
	}
}

func TestNewPoolValidation(t *testing.T) {
	valid := Config{Host: "irods", Port: 1247, Zone: "iplant", ProxyUser: "rods"}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"valid", func(*Config) {}, ""},
		{"no host", func(c *Config) { c.Host = "" }, "host is required"},
		{"no port", func(c *Config) { c.Port = 0 }, "port is required"},
		{"no zone", func(c *Config) { c.Zone = "" }, "zone is required"},
		{"no proxy user", func(c *Config) { c.ProxyUser = "" }, "proxy user is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)

			pool, err := NewPool(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("NewPool: %v", err)
				}
				defer pool.Close()
				// Defaults have to be applied, or the timeouts fall back to
				// go-irodsclient's rather than ours.
				if pool.cfg.MaxSessions != DefaultMaxSessions {
					t.Errorf("MaxSessions = %d, want %d", pool.cfg.MaxSessions, DefaultMaxSessions)
				}
				if pool.cfg.OperationTimeout != DefaultOperationTimeout {
					t.Errorf("OperationTimeout = %s, want %s", pool.cfg.OperationTimeout, DefaultOperationTimeout)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestForUserRejectsBlankUser guards the distinction between the two session kinds. A
// blank client user would silently produce a proxy-account session, which enforces no
// permissions -- the opposite of what a caller asking for a user's session wants.
func TestForUserRejectsBlankUser(t *testing.T) {
	pool, err := NewPool(Config{Host: "irods", Port: 1247, Zone: "iplant", ProxyUser: "rods"})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	if _, err := pool.ForUser(context.Background(), ""); err == nil {
		t.Error("ForUser accepted a blank user; that would silently act as the proxy account")
	}
}

// TestSessionRespectsCancellation covers the cancellation Do adds to a library that takes
// no context, including that the session is poisoned so a half-read connection is not
// handed to the next caller.
func TestSessionRespectsCancellation(t *testing.T) {
	pool, err := NewPool(Config{Host: "irods", Port: 1247, Zone: "iplant", ProxyUser: "rods"})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	e := &entry{user: "", inUse: 1}
	s := &Session{pool: pool, entry: e}

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})

	go func() {
		<-started
		cancel()
	}()

	_, err = Do(ctx, s, func(*irodsfs.FileSystem) (int, error) {
		close(started)
		<-release
		return 1, nil
	})
	close(release)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if !s.poisoned.Load() {
		t.Error("session was not poisoned; a connection left mid-protocol would be reused")
	}
}
