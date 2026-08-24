package apierror

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
)

// styleContextKey names the per-route error style stored in the echo context.
const styleContextKey = "apierror.style"

// WithStyle marks a route or group as reproducing the given error path. Routes are
// StyleTrap unless marked otherwise; see Style for which groups need StyleOK.
func WithStyle(s Style) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(styleContextKey, s)
			return next(c)
		}
	}
}

// styleOf reports the error style registered for the matched route.
func styleOf(c echo.Context) Style {
	if s, ok := c.Get(styleContextKey).(Style); ok {
		return s
	}
	return StyleTrap
}

// unrecognizedPath is the body the Clojure service returns for an unmatched route. It is
// deliberately not the error_code envelope -- data_info.util.service/unrecognized-path-response
// builds this shape instead, and it is a wire contract like any other.
const unrecognizedPath = `{"success":false,"reason":"unrecognized service path"}`

// HTTPErrorHandler renders errors as the DE error envelope. Install it as
// echo.Echo.HTTPErrorHandler.
//
// logErr receives the error alongside the request context so callers can log with their
// own logger. It is also called if writing the response itself fails, with the write
// error as the third argument. It may be nil.
func HTTPErrorHandler(logErr func(echo.Context, *Error, error)) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}

		// The client hung up. There is nobody to answer and nothing worth logging as a
		// failure of ours.
		if errors.Is(err, context.Canceled) {
			return
		}

		apiErr, raw := toAPIError(err, styleOf(c))

		if logErr != nil {
			logErr(c, apiErr, err)
		}

		status := apiErr.HTTPStatus()

		// A few routes contract on the status alone. HEAD has no body by definition.
		if c.Request().Method == http.MethodHead {
			report(logErr, c, apiErr, c.NoContent(status))
			return
		}

		if raw == "" {
			body, marshalErr := apiErr.MarshalJSON()
			if marshalErr != nil {
				// An extra key held something unserializable. Answer with the bare code
				// rather than an empty body, and surface the marshalling failure.
				report(logErr, c, apiErr, marshalErr)
				raw = `{"error_code":"` + string(apiErr.Code) + `"}`
			} else {
				raw = string(body)
			}
		}

		// Blob rather than JSON or JSONBlob, for two reasons. echo's JSON appends a
		// trailing newline where cheshire/encode does not, and these bodies are compared
		// byte for byte; and the content type is not always JSON here, or even present.
		if contentType := contentTypeFor(apiErr, raw); contentType != "" {
			c.Response().Header().Set(echo.HeaderContentType, contentType)
			report(logErr, c, apiErr, c.Blob(status, contentType, []byte(raw)))
			return
		}

		// No content type at all, which is not an oversight. An error that reaches the
		// reference's default exception handler is written without one, and a caller
		// sniffing the body is what that produces today.
		c.Response().WriteHeader(status)
		_, writeErr := c.Response().Write([]byte(raw))
		report(logErr, c, apiErr, writeErr)
	}
}

// report forwards a secondary failure -- one that happened while answering -- to the
// logger, if there is one. There is nothing else to be done with it at this point.
func report(logErr func(echo.Context, *Error, error), c echo.Context, apiErr *Error, err error) {
	if err != nil && logErr != nil {
		logErr(c, apiErr, err)
	}
}

// Envelope renders an error as the fields a response body would carry.
//
// It is for the endpoints that report a failure per item inside a successful response: a
// caller reading one of those should not have to parse a different shape from the one a whole
// failed request produces.
func Envelope(err error) map[string]any {
	apiErr, _ := toAPIError(err, StyleTrap)

	out := make(map[string]any, len(apiErr.Extra)+1)
	out["error_code"] = string(apiErr.Code)
	for key, value := range apiErr.Extra {
		out[key] = value
	}
	return out
}

// toAPIError normalises any error into the envelope. The second return value, when
// non-empty, is a verbatim body to send instead of the marshalled envelope.
func toAPIError(err error, style Style) (*Error, string) {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		// Handlers construct errors without knowing their route's style, so fill it in
		// here unless the handler pinned one.
		if apiErr.Style == StyleTrap && style != StyleTrap {
			apiErr = apiErr.WithStyle(style)
		}
		return apiErr, ""
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return New(ErrUnavailable).WithStyle(style).With("reason", err.Error()).WithCause(err), ""
	}

	var he *echo.HTTPError
	if errors.As(err, &he) {
		switch he.Code {
		case http.StatusNotFound:
			return New(ErrNotFound).WithStatus(http.StatusNotFound).WithCause(err), unrecognizedPath
		case http.StatusMethodNotAllowed:
			// A 404 carrying the unrecognised-path body, not a 405. compojure matches a
			// route on its method and path together, so a request with the wrong method
			// simply does not match and falls through to route/not-found -- verified
			// against the running service, which answers DELETE on a GET route with a 404
			// and the same body an unknown path gets.
			return New(ErrNotFound).WithStatus(http.StatusNotFound).WithCause(err), unrecognizedPath
		case http.StatusRequestEntityTooLarge:
			return New(ErrBadRequest).WithStatus(http.StatusRequestEntityTooLarge).
				With("reason", "request body too large").WithCause(err), ""
		case http.StatusBadRequest:
			// Request binding and schema coercion failures. compojure-api reports these
			// as ERR_ILLEGAL_ARGUMENT with a 400, on every route regardless of style.
			return New(ErrIllegalArgument).WithStatus(http.StatusBadRequest).
				With("reason", messageOf(he)).WithCause(err), ""
		default:
			return New(ErrRequestFailed).WithStatus(he.Code).
				With("reason", messageOf(he)).WithCause(err), ""
		}
	}

	// clojure-commons' unchecked-handler shape: a 500 naming the underlying failure.
	return New(ErrUncheckedException).WithStatus(http.StatusInternalServerError).
		With("reason", err.Error()).WithCause(err), ""
}

func messageOf(he *echo.HTTPError) string {
	if s, ok := he.Message.(string); ok {
		return s
	}
	if he.Internal != nil {
		return he.Internal.Error()
	}
	return http.StatusText(he.Code)
}

// JSONContentType is what every JSON response carries, successes and errors alike.
//
// Spelled without a space and with a lowercase charset because that is the byte sequence
// ring emits, and the header is compared exactly against it.
const JSONContentType = "application/json;charset=utf-8"

// unrecognizedPathContentType is what the reference labels its unrecognised-path body with.
//
// It is text/html for a body that is plainly JSON, because the body comes from
// compojure's route/not-found and nothing overrides the default. Callers see this on every
// unknown path and every wrong method, so it is contract however wrong it looks.
const unrecognizedPathContentType = "text/html;charset=utf-8"

// contentTypeFor reports the content type an error response carries, or "" for none.
//
// Three answers, and all three were measured against the running service rather than
// reasoned about:
//
//   - An unrecognised path, or a request with the wrong method, is labelled text/html.
//   - An error that reached the reference's default exception handler -- a thrown code on a
//     route written as (ok ...) -- carries no content type at all.
//   - Everything else, including a schema failure on one of those same routes, is JSON.
//
// The middle case is why Error carries Schema: on an ok-style route the two are told apart
// by which middleware rendered them, not by the code or the status.
func contentTypeFor(apiErr *Error, raw string) string {
	if raw == unrecognizedPath {
		return unrecognizedPathContentType
	}
	if apiErr.Style == StyleOK && !apiErr.Schema {
		return ""
	}
	return JSONContentType
}
