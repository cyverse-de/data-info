package apierror

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestMarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want string
	}{
		{
			name: "code only",
			err:  New(ErrDoesNotExist),
			want: `{"error_code":"ERR_DOES_NOT_EXIST"}`,
		},
		{
			name: "singular path key, as util/validators.clj throws it",
			err:  New(ErrNotReadable).With("path", "/iplant/home/wregglej/a").With("user", "wregglej"),
			want: `{"error_code":"ERR_NOT_READABLE","path":"/iplant/home/wregglej/a","user":"wregglej"}`,
		},
		{
			name: "plural paths key, as clj_irods/validate.clj throws it",
			err:  New(ErrDoesNotExist).With("paths", []string{"/a", "/b"}),
			want: `{"error_code":"ERR_DOES_NOT_EXIST","paths":["/a","/b"]}`,
		},
		{
			name: "hyphenated key survives",
			err:  New(ErrTicketDoesNotExist).With("ticket-id", "ABC123"),
			want: `{"error_code":"ERR_TICKET_DOES_NOT_EXIST","ticket-id":"ABC123"}`,
		},
		{
			name: "numeric extras are not stringified",
			err:  New(ErrTooManyResults).With("count", 1234).With("limit", 1000),
			want: `{"error_code":"ERR_TOO_MANY_RESULTS","count":1234,"limit":1000}`,
		},
		{
			name: "cause is never serialized",
			err:  New(ErrRequestFailed).WithCause(errors.New("boom")),
			want: `{"error_code":"ERR_REQUEST_FAILED"}`,
		},
		{
			name: "an error_code in Extra cannot shadow the real one",
			err:  New(ErrConflict).With("error_code", "NOPE"),
			want: `{"error_code":"ERR_CONFLICT"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.err)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestHTTPStatus(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want int
	}{
		{"trap style honours the table", New(ErrNotOwner), http.StatusForbidden},
		{"ok style is always 500", New(ErrNotOwner).WithStyle(StyleOK), http.StatusInternalServerError},
		{"preserved wart", New(ErrDoesNotExist), http.StatusInternalServerError},
		{"explicit status wins", New(ErrDoesNotExist).WithStatus(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.HTTPStatus(); got != tt.want {
				t.Errorf("HTTPStatus() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestErrorsAsAndUnwrap(t *testing.T) {
	cause := errors.New("underlying")
	wrapped := error(New(ErrUnavailable).WithCause(cause))

	var target *Error
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As did not match *Error")
	}
	if target.Code != ErrUnavailable {
		t.Errorf("Code = %s, want %s", target.Code, ErrUnavailable)
	}
	if !errors.Is(wrapped, cause) {
		t.Error("errors.Is did not find the cause")
	}
}
