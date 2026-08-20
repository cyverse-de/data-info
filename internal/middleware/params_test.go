package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestLowercaseQueryParams(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  url.Values
	}{
		{
			name:  "mixed case names are lowered",
			query: "User=wregglej&Limit=10&INFO-TYPE=csv",
			want:  url.Values{"user": {"wregglej"}, "limit": {"10"}, "info-type": {"csv"}},
		},
		{
			name:  "values keep their case",
			query: "USER=Wregglej&PATH=/iplant/home/Shared",
			want:  url.Values{"user": {"Wregglej"}, "path": {"/iplant/home/Shared"}},
		},
		{
			name:  "repeated params collapse onto one lowered name",
			query: "info-type=csv&INFO-TYPE=bam",
			want:  url.Values{"info-type": {"csv", "bam"}},
		},
		{
			name:  "already lowercase is untouched",
			query: "user=wregglej&limit=10",
			want:  url.Values{"user": {"wregglej"}, "limit": {"10"}},
		},
		{
			name:  "plus decodes to a space, as clj-http's url-decode does",
			query: "Path=/iplant/home/a+b",
			want:  url.Values{"path": {"/iplant/home/a b"}},
		},
		{
			name:  "encoded characters survive the round trip",
			query: "Path=%2Fiplant%2Fhome%2Fa%23b",
			want:  url.Values{"path": {"/iplant/home/a#b"}},
		},
		{
			name:  "empty query",
			query: "",
			want:  url.Values{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			var got url.Values
			e.Pre(LowercaseQueryParams())
			e.GET("/x", func(c echo.Context) error {
				got = c.QueryParams()
				return c.NoContent(http.StatusOK)
			})

			target := "/x"
			if tt.query != "" {
				target += "?" + tt.query
			}
			e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))

			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, wantVals := range tt.want {
				gotVals := got[k]
				if len(gotVals) != len(wantVals) {
					t.Errorf("%s: got %v, want %v", k, gotVals, wantVals)
					continue
				}
				for i := range wantVals {
					if gotVals[i] != wantVals[i] {
						t.Errorf("%s[%d]: got %q, want %q", k, i, gotVals[i], wantVals[i])
					}
				}
			}
		})
	}
}

// TestHasUpperIgnoresValues documents why the fast path only inspects names.
func TestHasUpperIgnoresValues(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"user=Wregglej", false},
		{"path=/iplant/home/Shared", false},
		{"User=wregglej", true},
		{"a=1&B=2", true},
		{"noequalsign", false},
		{"NOEQUALSIGN", true},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := hasUpper(tt.raw); got != tt.want {
				t.Errorf("hasUpper(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
