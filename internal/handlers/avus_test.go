package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

func TestBindAVUChange(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		avusRequired bool
		want         []service.AVU
		wantRest     []string
		wantErr      bool
	}{
		{
			name:         "set without irods-avus is refused rather than clearing every AVU",
			body:         `{"avus":[]}`,
			avusRequired: true,
			wantErr:      true,
		},
		{
			name:         "set with an empty body is refused",
			body:         ``,
			avusRequired: true,
			wantErr:      true,
		},
		{
			name:         "set with null irods-avus is refused",
			body:         `{"irods-avus":null}`,
			avusRequired: true,
			wantErr:      true,
		},
		{
			name:         "set with an empty list clears the AVUs",
			body:         `{"irods-avus":[]}`,
			avusRequired: true,
		},
		{
			name:     "add without irods-avus changes only the metadata service",
			body:     `{"avus":[]}`,
			wantRest: []string{"avus"},
		},
		{
			name:    "add with null irods-avus is refused",
			body:    `{"irods-avus":null}`,
			wantErr: true,
		},
		{
			name:     "a full AVU is kept and the rest is forwarded",
			body:     `{"irods-avus":[{"attr":"a","value":"v","unit":""}],"avus":[]}`,
			want:     []service.AVU{{Attribute: "a", Value: "v", Unit: ""}},
			wantRest: []string{"avus"},
		},
		{
			name:    "a missing value is refused",
			body:    `{"irods-avus":[{"attr":"a","unit":""}]}`,
			wantErr: true,
		},
		{
			name:    "a missing unit is refused",
			body:    `{"irods-avus":[{"attr":"a","value":"v"}]}`,
			wantErr: true,
		},
		{
			name:    "a numeric value is refused",
			body:    `{"irods-avus":[{"attr":"a","value":1,"unit":""}]}`,
			wantErr: true,
		},
		{
			name:    "a blank attr is refused",
			body:    `{"irods-avus":[{"attr":" ","value":"v","unit":""}]}`,
			wantErr: true,
		},
		{
			name:    "irods-avus that is not a list is refused",
			body:    `{"irods-avus":{"attr":"a"}}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(tc.body))
			c := echo.New().NewContext(req, httptest.NewRecorder())

			got, err := bindAVUChange(c, tc.avusRequired)
			if tc.wantErr {
				var apiErr *apierror.Error
				if !errors.As(err, &apiErr) || apiErr.HTTPStatus() != http.StatusBadRequest {
					t.Fatalf("err = %v, want a 400 schema failure", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !slices.Equal(got.IRODSAVUs, tc.want) {
				t.Errorf("IRODSAVUs = %v, want %v", got.IRODSAVUs, tc.want)
			}
			for _, key := range tc.wantRest {
				if _, ok := got.Rest[key]; !ok {
					t.Errorf("Rest is missing %q: %v", key, got.Rest)
				}
			}
			if len(got.Rest) != len(tc.wantRest) {
				t.Errorf("Rest = %v, want keys %v", got.Rest, tc.wantRest)
			}
		})
	}
}
