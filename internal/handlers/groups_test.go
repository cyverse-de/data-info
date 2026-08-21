package handlers

import (
	"net/http"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
)

// Two spellings of the same account have to compare equal, or every membership update would
// remove everyone and add them straight back.
func TestQualification(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		qualified   string
		wantAccount string
		wantZone    string
	}{
		{
			name: "a bare name", in: "wregglej",
			qualified: "wregglej#iplant", wantAccount: "wregglej", wantZone: testZone,
		},
		{
			name: "already qualified", in: "wregglej#iplant",
			qualified: "wregglej#iplant", wantAccount: "wregglej", wantZone: testZone,
		},
		{
			// iRODS takes the name and the zone separately, so an account from another
			// zone has to keep its own -- passing the whole string as a name would look it
			// up locally under a name containing a hash.
			name: "an account from another zone", in: "someone#other",
			qualified: "someone#other", wantAccount: "someone", wantZone: "other",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := qualify(tc.in, testZone); got != tc.qualified {
				t.Errorf("qualify(%q) = %q, want %q", tc.in, got, tc.qualified)
			}

			account, zone := splitAccount(qualify(tc.in, testZone), testZone)
			if account != tc.wantAccount || zone != tc.wantZone {
				t.Errorf("splitAccount(%q) = (%q, %q), want (%q, %q)",
					tc.in, account, zone, tc.wantAccount, tc.wantZone)
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
