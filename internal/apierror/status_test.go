package apierror

import (
	"net/http"
	"testing"
)

// TestStatusFor asserts the whole status table. It exists so that the preserved
// clojure-commons behaviour -- notably ERR_DOES_NOT_EXIST answering 500 rather than 404 --
// cannot be "fixed" by accident. If you are here because this test failed, read the
// comment on statusFor before changing anything.
func TestStatusFor(t *testing.T) {
	tests := []struct {
		code Code
		trap int
		ok   int
	}{
		// Codes clojure-commons maps off the 500 default.
		{ErrBadOrMissingField, http.StatusBadRequest, http.StatusInternalServerError},
		{ErrIllegalArgument, http.StatusBadRequest, http.StatusInternalServerError},
		{ErrInvalidJSON, http.StatusBadRequest, http.StatusInternalServerError},
		{ErrBadRequest, http.StatusBadRequest, http.StatusInternalServerError},
		{ErrBadQueryParameter, http.StatusBadRequest, http.StatusInternalServerError},
		{ErrMissingQueryParam, http.StatusBadRequest, http.StatusInternalServerError},
		{ErrNotAuthorized, http.StatusUnauthorized, http.StatusInternalServerError},
		{ErrNotOwner, http.StatusForbidden, http.StatusInternalServerError},
		{ErrForbidden, http.StatusForbidden, http.StatusInternalServerError},
		{ErrNotFound, http.StatusNotFound, http.StatusInternalServerError},
		{ErrConflict, http.StatusConflict, http.StatusInternalServerError},

		// Codes that fall to the 500 default. These are the preserved warts.
		{ErrDoesNotExist, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrNotReadable, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrNotWriteable, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrNotAUser, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrNotAFile, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrNotAFolder, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrExists, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrUnavailable, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrTooManyResults, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrTicketExists, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrTicketDoesNotExist, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrIncompleteRename, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrRequestFailed, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrUncheckedException, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrConfigInvalid, http.StatusInternalServerError, http.StatusInternalServerError},

		// Codes clojure-commons never defined, so http-status-for has no entry and
		// they can only ever be 500.
		{ErrTooManyPaths, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrBadPathLength, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrBadDirnameLength, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrBadBasenameLength, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrPageNotPos, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrInvalidPage, http.StatusInternalServerError, http.StatusInternalServerError},
		{ErrChunkTooSmall, http.StatusInternalServerError, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			if got := statusFor(tt.code, StyleTrap); got != tt.trap {
				t.Errorf("statusFor(%s, StyleTrap) = %d, want %d", tt.code, got, tt.trap)
			}
			if got := statusFor(tt.code, StyleOK); got != tt.ok {
				t.Errorf("statusFor(%s, StyleOK) = %d, want %d", tt.code, got, tt.ok)
			}
		})
	}
}

// TestStatusForCoversEveryEmittedCode fails if a code is declared without a decision
// being recorded in TestStatusFor above.
func TestStatusForCoversEveryEmittedCode(t *testing.T) {
	covered := map[Code]bool{}
	for _, c := range []Code{
		ErrBadOrMissingField, ErrIllegalArgument, ErrInvalidJSON, ErrBadRequest,
		ErrBadQueryParameter, ErrMissingQueryParam, ErrNotAuthorized, ErrNotOwner,
		ErrForbidden, ErrNotFound, ErrConflict, ErrDoesNotExist, ErrNotReadable,
		ErrNotWriteable, ErrNotAUser, ErrNotAFile, ErrNotAFolder, ErrExists,
		ErrUnavailable, ErrTooManyResults, ErrTicketExists, ErrTicketDoesNotExist,
		ErrIncompleteRename, ErrRequestFailed, ErrUncheckedException, ErrConfigInvalid,
		ErrTooManyPaths, ErrBadPathLength, ErrBadDirnameLength, ErrBadBasenameLength,
		ErrPageNotPos, ErrInvalidPage, ErrChunkTooSmall,
	} {
		covered[c] = true
	}

	// The 31 codes the Clojure service emits, taken from a grep of its source.
	for _, c := range EmittedCodes() {
		if !covered[c] {
			t.Errorf("code %s is emitted by the Clojure service but has no status assertion", c)
		}
	}
}
