package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/paths"
)

func TestUniquePaths(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    []string
		wantErr bool
	}{
		{
			name: "duplicates collapse and order is kept",
			in:   []string{"/z/home/u/b", "/z/home/u/a", "/z/home/u/b"},
			want: []string{"/z/home/u/b", "/z/home/u/a"},
		},
		{
			name: "a trailing slash is not a different path",
			in:   []string{"/z/home/u/a/", "/z/home/u/a"},
			want: []string{"/z/home/u/a"},
		},
		{
			// The client cleans before acting, so an uncleaned path would be checked in
			// one place and created in another.
			name: "paths are canonicalised before anything else looks at them",
			in:   []string{"/z/home/me/../other/new"},
			want: []string{"/z/home/other/new"},
		},
		{
			name: "repeated separators collapse",
			in:   []string{"/z//home///u/a"},
			want: []string{"/z/home/u/a"},
		},
		{
			name:    "a blank entry is refused rather than dropped",
			in:      []string{""},
			wantErr: true,
		},
		{
			name:    "a blank entry is refused even beside a real one",
			in:      []string{"/z/home/u/a", "   "},
			wantErr: true,
		},
		{
			name:    "the root is not a collection anyone can create",
			in:      []string{"/"},
			wantErr: true,
		},
		{
			name:    "a path that climbs out to the root is refused too",
			in:      []string{"/z/../.."},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := uniquePaths(tc.in)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("uniquePaths(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("uniquePaths(%q): %v", tc.in, err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("uniquePaths(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Path lengths are refused before the collection is looked for, so a name iRODS could never
// hold does not cost a catalog round trip -- and the code reports which limit was broken,
// because the Clojure service does.
func TestCreateDirectoriesRejectsBadRequests(t *testing.T) {
	deps, _ := testDeps(t)
	writes := NewWrites(deps)

	longName := testHome + "/" + strings.Repeat("n", paths.MaxFilenameLength+1)
	longDir := "/" + strings.Repeat("d", paths.MaxDirLength) + "/x"

	cases := []struct {
		name      string
		body      string
		wantCode  int
		wantErr   string
		wantExtra map[string]any
	}{
		{
			name:     "no paths at all",
			body:     `{"paths":[]}`,
			wantCode: http.StatusBadRequest,
			wantErr:  string(apierror.ErrIllegalArgument),
		},
		{
			name:     "a blank path",
			body:     `{"paths":[""]}`,
			wantCode: http.StatusBadRequest,
			wantErr:  string(apierror.ErrIllegalArgument),
		},
		{
			name:     "a blank path beside a real one",
			body:     `{"paths":["` + testHome + `/a","  "]}`,
			wantCode: http.StatusBadRequest,
			wantErr:  string(apierror.ErrIllegalArgument),
		},
		{
			name:     "the zone root",
			body:     `{"paths":["/"]}`,
			wantCode: http.StatusBadRequest,
			wantErr:  string(apierror.ErrIllegalArgument),
		},
		{
			name:      "a name longer than iRODS allows",
			body:      `{"paths":["` + longName + `"]}`,
			wantCode:  http.StatusInternalServerError,
			wantErr:   string(apierror.ErrBadBasenameLength),
			wantExtra: map[string]any{"file-path": paths.Base(longName), "full-path": longName},
		},
		{
			name:      "a collection longer than iRODS allows",
			body:      `{"paths":["` + longDir + `"]}`,
			wantCode:  http.StatusInternalServerError,
			wantErr:   string(apierror.ErrBadDirnameLength),
			wantExtra: map[string]any{"dir-path": paths.Dir(longDir), "full-path": longDir},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, apierror.StyleTrap, http.MethodPost,
				"/data/directories?user="+testUser, tc.body, writes.CreateDirectories)

			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}

			var envelope map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decoding %q: %v", rec.Body.String(), err)
			}
			if envelope["error_code"] != tc.wantErr {
				t.Errorf("error_code = %v, want %s", envelope["error_code"], tc.wantErr)
			}
			for key, want := range tc.wantExtra {
				if envelope[key] != want {
					t.Errorf("%s = %v, want %v", key, envelope[key], want)
				}
			}
		})
	}
}
