package apierror

import (
	"context"
	"errors"
	"net/http"

	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/labstack/echo/v4"
)

// styleContextKey names the per-route error style stored in the echo context.
const styleContextKey = "apierror.style"

// WithStyle sets the error style of a route or group. Routes are StyleTrap unless marked.
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

// unrecognizedPath is the body for an unmatched route. It is deliberately not the error
// envelope; callers depend on this shape.
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

		var upstream *UpstreamError
		if errors.As(err, &upstream) {
			relay(logErr, c, upstream, err)
			return
		}

		apiErr, raw := toAPIError(err, styleOf(c))

		if logErr != nil {
			logErr(c, apiErr, err)
		}

		status := apiErr.HTTPStatus()

		// HEAD answers with a bare status. Only a request rejected by validation carries a
		// Content-Type; one the handler rejects carries none.
		if c.Request().Method == http.MethodHead {
			var contentType string
			if apiErr.Schema {
				contentType = JSONContentType
			}
			setContentType(c, contentType)
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

		// Blob rather than JSON: echo's JSON appends a newline the body must not carry, and
		// the content type here isn't always JSON.
		contentType := contentTypeFor(apiErr, raw)
		setContentType(c, contentType)

		if contentType != "" {
			report(logErr, c, apiErr, c.Blob(status, contentType, []byte(raw)))
			return
		}

		// StyleOK handler errors deliberately carry no Content-Type; see contentTypeFor.
		c.Response().WriteHeader(status)
		_, writeErr := c.Response().Write([]byte(raw))
		report(logErr, c, apiErr, writeErr)
	}
}

// relay writes another service's error response through unchanged.
func relay(logErr func(echo.Context, *Error, error), c echo.Context, upstream *UpstreamError, err error) {
	apiErr := New(upstream.Code()).WithStatus(upstream.Status).WithCause(err)
	if logErr != nil {
		logErr(c, apiErr, err)
	}

	setContentType(c, upstream.ContentType)
	c.Response().WriteHeader(upstream.Status)
	_, writeErr := c.Response().Write(upstream.Body)
	report(logErr, c, apiErr, writeErr)
}

// report forwards a secondary failure -- one that happened while answering -- to the
// logger, if there is one. There is nothing else to be done with it at this point.
func report(logErr func(echo.Context, *Error, error), c echo.Context, apiErr *Error, err error) {
	if err != nil && logErr != nil {
		logErr(c, apiErr, err)
	}
}

// Envelope renders an error as envelope fields, for endpoints that report per-item failures
// inside a successful response in the same shape a failed request has.
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
		return New(ErrUnavailable).WithStyle(style).With("reason", publicMessage(err)).WithCause(err), ""
	}

	var he *echo.HTTPError
	if errors.As(err, &he) {
		switch he.Code {
		case http.StatusNotFound:
			return New(ErrNotFound).WithStatus(http.StatusNotFound).WithCause(err), unrecognizedPath
		case http.StatusMethodNotAllowed:
			// A wrong method gets the unknown-path 404, not a 405; callers depend on that.
			return New(ErrNotFound).WithStatus(http.StatusNotFound).WithCause(err), unrecognizedPath
		case http.StatusRequestEntityTooLarge:
			return New(ErrBadRequest).WithStatus(http.StatusRequestEntityTooLarge).
				With("reason", "request body too large").WithCause(err), ""
		case http.StatusBadRequest:
			// Binding and schema failures answer ERR_ILLEGAL_ARGUMENT with a 400, whatever
			// the route's style.
			return New(ErrIllegalArgument).WithStatus(http.StatusBadRequest).
				With("reason", messageOf(he)).WithCause(err), ""
		default:
			return New(ErrRequestFailed).WithStatus(he.Code).
				With("reason", messageOf(he)).WithCause(err), ""
		}
	}

	// An unrecognised error is a 500. Its detail key differs by style only to match the
	// legacy Clojure service.
	key := "message"
	if style == StyleOK {
		key = "reason"
	}
	return New(ErrUncheckedException).WithStatus(http.StatusInternalServerError).
		With(key, publicMessage(err)).WithCause(err), ""
}

// publicMessage is what a response may say about an unexpected error. A failed catalog
// query carries the database's own text, which names its tables and columns, so it is
// replaced by a fixed message; the full error is still logged with the request.
func publicMessage(err error) string {
	var query *icat.QueryError
	if errors.As(err, &query) {
		return "a query against the iRODS catalog failed"
	}
	return err.Error()
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

// JSONContentType is the Content-Type of every JSON response. Its exact spelling (no space,
// lowercase charset) is part of the contract.
const JSONContentType = "application/json;charset=utf-8"

// unrecognizedPathContentType labels the unknown-path body text/html even though it is JSON.
// Callers see it on every unknown path, so it is contract however wrong it looks.
const unrecognizedPathContentType = "text/html;charset=utf-8"

// contentTypeFor reports an error response's Content-Type, or "" for none: text/html for an
// unknown path or wrong method, none for a StyleOK handler error, and JSON otherwise
// (including a StyleOK schema failure, which is why Error carries Schema).
func contentTypeFor(apiErr *Error, raw string) string {
	if raw == unrecognizedPath {
		return unrecognizedPathContentType
	}
	if apiErr.Style == StyleOK && !apiErr.Schema {
		return ""
	}
	return JSONContentType
}

// setContentType sets the Content-Type, or suppresses it when empty: a nil header value stops
// net/http from sniffing one in. It also drops the Allow header echo sets on a wrong method,
// which answers as an unknown path.
func setContentType(c echo.Context, contentType string) {
	header := c.Response().Header()
	header.Del("Allow")

	if contentType == "" {
		header["Content-Type"] = nil
		return
	}
	header.Set(echo.HeaderContentType, contentType)
}
