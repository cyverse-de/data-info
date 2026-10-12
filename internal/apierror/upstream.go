package apierror

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// UpstreamError is an error response from another DE service, relayed to the caller as it was
// received. clojure-commons' trap returns a clj-http error response as the ring response
// itself, so on a StyleTrap route the caller sees the other service's status and body.
type UpstreamError struct {
	Service     string
	Status      int
	ContentType string
	Body        []byte
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("%s answered %d: %s", e.Service, e.Status, bytes.TrimSpace(e.Body))
}

// Code reports the error_code in the relayed body, or ErrRequestFailed when it carries none.
func (e *UpstreamError) Code() Code {
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	if json.Unmarshal(e.Body, &body) != nil || body.ErrorCode == "" {
		return ErrRequestFailed
	}
	return Code(body.ErrorCode)
}
