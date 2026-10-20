package apierror

import "net/http"

// Style selects how an endpoint maps error codes onto HTTP statuses.
type Style int

const (
	// StyleTrap maps codes through statusFor's table. It is the zero value.
	StyleTrap Style = iota

	// StyleOK answers 500 for every error_code; see statusFor.
	StyleOK
)

// statusFor answers 500 for every StyleOK error and for most codes otherwise, because apps
// matches on a literal 500; see docs/deferred-fixes.md #1 before changing it.
func statusFor(c Code, s Style) int {
	if s == StyleOK {
		return http.StatusInternalServerError
	}

	switch c {
	case ErrBadOrMissingField,
		ErrIllegalArgument,
		ErrInvalidJSON,
		ErrBadRequest,
		ErrBadQueryParameter,
		ErrMissingQueryParam:
		return http.StatusBadRequest
	case ErrNotAuthorized:
		return http.StatusUnauthorized
	case ErrNotOwner, ErrForbidden:
		return http.StatusForbidden
	case ErrNotFound:
		return http.StatusNotFound
	case ErrConflict:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
