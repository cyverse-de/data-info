// Package apierror reproduces the DE error envelope: a JSON object whose error_code
// names the failure and whose remaining keys carry failure-specific detail.
//
// The envelope, the error_code vocabulary and the HTTP statuses are all wire contract
// shared with terrain, apps, analyses and search. This package is a deliberate,
// behaviour-preserving port of the Clojure implementation, warts included; see statusFor.
package apierror

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Error is a DE error response. Extra holds the failure-specific keys, which are
// emitted at the top level of the JSON object alongside error_code.
//
// Extra is an open map rather than a struct because the key names are inherited from
// Clojure and include hyphenated ones such as "ticket-id", and because different
// endpoints attach different keys for the same code. Reproducing them exactly matters:
// callers read them. Notably util/validators.clj and clj_irods/validate.clj disagree
// about "path" versus "paths" for the same condition, and both are live on different
// endpoints, so do not normalise them here.
type Error struct {
	Code   Code
	Style  Style
	Extra  map[string]any
	Status int // when non-zero, overrides the status implied by Code and Style
	Cause  error

	// Schema marks a request that failed the shape its endpoint declares, as opposed to one
	// whose handler threw. The Clojure service renders the two through different middleware
	// and they answer with different headers, so the distinction has to survive to the point
	// the response is written. See contentTypeFor.
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

// WithStyle selects the error path this endpoint reproduces. Handlers rarely call this;
// the router sets it per route group.
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

// MarshalJSON emits error_code first, then the extra keys in sorted order. It is written
// by hand because the extra keys are an open set with names Go struct tags cannot express
// alongside one another, and because byte-level stability is asserted in tests.
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
