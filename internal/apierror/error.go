// Package apierror renders the DE error envelope: a JSON object whose error_code names the
// failure and whose other keys carry detail. The envelope, codes and statuses are a wire
// contract with terrain, apps, analyses and search.
package apierror

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Error is a DE error response. Extra's keys are emitted at the top level alongside
// error_code; callers read them, and endpoints differ on names for the same code (some
// hyphenated, "path" versus "paths"), so don't normalise them.
type Error struct {
	Code   Code
	Style  Style
	Extra  map[string]any
	Status int // when non-zero, overrides the status implied by Code and Style
	Cause  error

	// Schema marks a request-validation failure rather than a handler error; the two answer
	// with different headers (see contentTypeFor).
	Schema bool
}

// New returns an Error carrying the given code.
func New(code Code) *Error {
	return &Error{Code: code}
}

// With attaches a failure-specific key to the envelope.
func (e *Error) With(key string, val any) *Error {
	if e.Extra == nil {
		e.Extra = make(map[string]any, 4)
	}
	e.Extra[key] = val
	return e
}

// WithStyle sets the error's Style. Handlers rarely call this; the router sets it per route.
func (e *Error) WithStyle(s Style) *Error {
	e.Style = s
	return e
}

// WithStatus pins an explicit status, for the few endpoints whose contract is the status
// rather than the body -- HEAD /data/{data-id} answers a bare 422 for an unparseable id.
func (e *Error) WithStatus(status int) *Error {
	e.Status = status
	return e
}

// AsSchemaFailure marks the error as a request-validation failure rather than a handler
// failure, which decides how the response is framed.
func (e *Error) AsSchemaFailure() *Error {
	e.Schema = true
	return e
}

// WithCause attaches the underlying error. It is logged, never serialized.
func (e *Error) WithCause(err error) *Error {
	e.Cause = err
	return e
}

// HTTPStatus reports the status this error serializes with.
func (e *Error) HTTPStatus() int {
	if e.Status != 0 {
		return e.Status
	}
	return statusFor(e.Code, e.Style)
}

func (e *Error) Error() string {
	if len(e.Extra) == 0 {
		return string(e.Code)
	}
	return fmt.Sprintf("%s %v", e.Code, e.Extra)
}

func (e *Error) Unwrap() error { return e.Cause }

// MarshalJSON emits error_code first, then Extra's keys sorted, so bodies are byte-stable.
func (e *Error) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')

	code, err := json.Marshal(string(e.Code))
	if err != nil {
		return nil, err
	}
	buf.WriteString(`"error_code":`)
	buf.Write(code)

	keys := make([]string, 0, len(e.Extra))
	for k := range e.Extra {
		if k == "error_code" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		name, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		val, err := json.Marshal(e.Extra[k])
		if err != nil {
			return nil, fmt.Errorf("marshaling %q of %s: %w", k, e.Code, err)
		}
		buf.WriteByte(',')
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(val)
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}
