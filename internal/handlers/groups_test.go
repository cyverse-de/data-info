package handlers

import (
	"net/http"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
)

func TestQualification(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		qualified string
		bare      string
	}{
		{name: "a bare name", in: "wregglej", qualified: "wregglej#iplant", bare: "wregglej"},
		{name: "already qualified", in: "wregglej#iplant", qualified: "wregglej#iplant", bare: "wregglej"},
		{
			// Another zone's account stays qualified: the suffix is not this zone's, so
			// removing it would name a different account.
			name: "an account from another zone",
			in:   "someone#other", qualified: "someone#other", bare: "someone#other",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := qualify(tc.in, testZone); got != tc.qualified {
				t.Errorf("qualify(%q) = %q, want %q", tc.in, got, tc.qualified)
			}
			if got := unqualify(qualify(tc.in, testZone), testZone); got != tc.bare {
				t.Errorf("unqualify(qualify(%q)) = %q, want %q", tc.in, got, tc.bare)
			}
		})
	}
}

// Administering groups is not something an ordinary account may do, and the route documents a
// 403 for it rather than the 500 its other errors get.
func TestGroupAdministrationIsRefusedToOrdinaryUsers(t *testing.T) {
	deps, fake := testDeps(t)
	fake.AddUser("groupadmin", icat.UserKindGroupAdmin)
	fake.AddUser("admin", icat.UserKindAdmin)
	groups := NewGroups(deps)

	cases := []struct {
		name          string
		user          string
		wantForbidden bool
	}{
		{name: "an ordinary user", user: testUser, wantForbidden: true},
		{name: "a group administrator", user: "groupadmin"},
		{name: "an iRODS administrator", user: "admin"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveWithBody(t, http.MethodDelete, "/groups/:group-name",
				"/groups/nosuchgroup?user="+tc.user, groups.Delete, "")

			forbidden := rec.Code == http.StatusForbidden
			if forbidden != tc.wantForbidden {
				t.Fatalf("forbidden = %v, want %v (status %d, body %s)",
					forbidden, tc.wantForbidden, rec.Code, rec.Body.String())
			}

			if tc.wantForbidden {
				if got := errorCodeOf(t, rec); got != string(apierror.ErrForbidden) {
					t.Errorf("error_code = %q, want %q", got, apierror.ErrForbidden)
				}
				return
			}
			// Past the permission check is as far as this can go without a data store:
			// what follows talks to iRODS, which these tests deliberately cannot reach.
			if got := errorCodeOf(t, rec); got != string(apierror.ErrUnavailable) {
				t.Errorf("error_code = %q, want the request to have reached the data store", got)
			}
		})
	}
}
