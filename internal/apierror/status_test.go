package apierror

import (
	"net/http"
	"testing"
)

// statusTable pins the whole status table so its deliberate 500s can't change by accident.
// Read statusFor before changing it.
var statusTable = []struct {
	code Code
	trap int
	ok   int
}{
	// Codes mapped off the 500 default.
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

	// Codes that fall to the 500 default.
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

	// Codes outside the shared vocabulary: always 500.
	{ErrTooManyPaths, http.StatusInternalServerError, http.StatusInternalServerError},
	{ErrBadPathLength, http.StatusInternalServerError, http.StatusInternalServerError},
	{ErrBadDirnameLength, http.StatusInternalServerError, http.StatusInternalServerError},
	{ErrBadBasenameLength, http.StatusInternalServerError, http.StatusInternalServerError},
	{ErrPageNotPos, http.StatusInternalServerError, http.StatusInternalServerError},
	{ErrInvalidPage, http.StatusInternalServerError, http.StatusInternalServerError},
	{ErrChunkTooSmall, http.StatusInternalServerError, http.StatusInternalServerError},
}

func TestStatusFor(t *testing.T) {
	for _, tt := range statusTable {
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

// TestStatusForCoversEveryEmittedCode fails if EmittedCodes gains a code statusTable doesn't
// pin.
func TestStatusForCoversEveryEmittedCode(t *testing.T) {
	covered := make(map[Code]bool, len(statusTable))
	for _, tt := range statusTable {
		covered[tt.code] = true
	}

	for _, c := range EmittedCodes() {
		if !covered[c] {
			t.Errorf("code %s is in EmittedCodes but has no status assertion", c)
		}
	}
}
