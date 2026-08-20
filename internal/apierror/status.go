package apierror

import "net/http"

// Style selects which of the Clojure service's two error paths an endpoint reproduces.
//
// The Clojure routes are written in two shapes, and they answer differently for the same
// error_code. Handlers wrapped in svc/trap go through clojure-commons'
// error-codes/get-http-status, which maps a small set of codes onto 4xx and defaults the
// rest to 500. Handlers that instead return (ok ...) let the thrown map escape to
// wrap-exceptions; data-info throws bare {:error_code ...} maps with no :type key, so they
// match none of clojure-commons.exception's typed handlers and land on ::ex/default. That
// is unchecked-handler, whose first branch tests (ec/error? obj) and answers
// internal-server-error unconditionally -- so on those routes every error_code is a 500.
//
// Both behaviours are preserved deliberately. See statusFor for why.
type Style int

const (
	// StyleTrap reproduces svc/trap: the get-http-status table, defaulting to 500.
	// This is the majority of endpoints and the zero value.
	StyleTrap Style = iota

	// StyleOK reproduces the (ok ...) routes, where every error_code answers 500:
	// /existence-marker, /creatability-marker, the /groups routes,
	// GET /navigation/root, GET /navigation/path/{zone}/*, /stat-gatherer, /path-info,
	// /stat-lister, /tickets, /ticket-lister and /ticket-deleter.
	StyleOK
)

// statusFor reproduces clojure-commons' http-status-for table exactly, including its
// 500 default. Several entries are wrong on their face: a missing path answers 500
// rather than 404, and an unreadable one answers 500 rather than 403.
//
// That is deliberate, and it must stay that way until the consumers are fixed first.
// The apps service branches on the literal status 500 in four places, and each would
// break the day data-info started answering 404 or 403:
//
//   - apps/src/apps/service/apps/jobs/util.clj      create-output-dir
//     (job submission into a new output folder depends on catching 500 and reading
//     ERR_DOES_NOT_EXIST out of the body; it even carries a FIXME about this)
//   - apps/src/apps/service/apps/util.clj           paths-accessible?
//   - apps/src/apps/service/apps/de/job_view.clj    validate-hidden-inputs
//   - apps/src/apps/service/apps/de/jobs/io_tickets.clj  delete-tickets
//
// Terrain is unaffected: it relays status and body verbatim and keys on error_code.
//
// To correct this later, make apps status-agnostic first (catch any numeric :status
// and dispatch on error_code, which works against both regimes), soak that, and only
// then change the table here. TestStatusFor asserts the whole map so it cannot drift
// by accident.
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
		// Everything else, including ERR_DOES_NOT_EXIST, ERR_NOT_READABLE,
		// ERR_NOT_WRITEABLE, ERR_NOT_A_USER, ERR_EXISTS and ERR_UNAVAILABLE.
		return http.StatusInternalServerError
	}
}
