// Package metadata talks to the metadata service, which holds the template AVUs the DE
// manages.
//
// Those are separate from the AVUs iRODS itself stores. A data item has both, this service
// owns neither half completely, and several endpoints return them merged -- which is why the
// two are named apart everywhere here: irods-avus for what iRODS holds, and everything else
// for what the metadata service does.
package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Target types the metadata service knows. A collection is a "folder" there and a "dir" in
// iRODS, which is why the two have to be translated rather than passed through.
const (
	TargetFile   = "file"
	TargetFolder = "folder"
)

// CopyTarget names one item metadata is being copied to.
type CopyTarget struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Client is a metadata service HTTP client.
type Client struct {
	base *url.URL
	http *http.Client
}

// New builds a client for the metadata service at base.
func New(base string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("metadata: base URL %q is not usable: %w", base, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("metadata: base URL %q needs a scheme and a host", base)
	}

	return &Client{base: parsed, http: &http.Client{Timeout: timeout}}, nil
}

// ListAVUs returns the metadata service's view of an item, decoded only as far as is needed
// to merge iRODS' own AVUs into it.
//
// The body is passed through rather than modelled. Its shape is the metadata service's to
// change, several of its fields mean nothing here, and a struct would silently drop whatever
// this service has not been taught about.
func (c *Client) ListAVUs(ctx context.Context, user, targetType, targetID string) (map[string]any, error) {
	body, err := c.do(ctx, http.MethodGet, c.avusURL(user, targetType, targetID), nil)
	if err != nil {
		return nil, err
	}

	out := map[string]any{}
	if len(bytes.TrimSpace(body)) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("metadata: decoding the AVUs of %s %s: %w", targetType, targetID, err)
	}
	return out, nil
}

// UpdateAVUs adds or updates template AVUs on an item.
func (c *Client) UpdateAVUs(ctx context.Context, user, targetType, targetID string, body any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("metadata: encoding an AVU update: %w", err)
	}

	_, err = c.do(ctx, http.MethodPost, c.avusURL(user, targetType, targetID), encoded)
	return err
}

// SetAVUs replaces an item's template AVUs. Anything not sent is removed.
func (c *Client) SetAVUs(ctx context.Context, user, targetType, targetID string, body any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("metadata: encoding an AVU set: %w", err)
	}

	_, err = c.do(ctx, http.MethodPut, c.avusURL(user, targetType, targetID), encoded)
	return err
}

// CopyAVUs copies an item's template AVUs onto others.
func (c *Client) CopyAVUs(ctx context.Context, user, targetType, targetID string, targets []CopyTarget) error {
	encoded, err := json.Marshal(map[string]any{"targets": targets})
	if err != nil {
		return fmt.Errorf("metadata: encoding an AVU copy: %w", err)
	}

	target := c.avusURL(user, targetType, targetID)
	target.Path += "/copy"

	_, err = c.do(ctx, http.MethodPost, target, encoded)
	return err
}

// avusURL builds the address of one item's AVUs.
func (c *Client) avusURL(user, targetType, targetID string) *url.URL {
	out := c.base.JoinPath("avus", targetType, targetID)
	out.RawQuery = url.Values{"user": {user}}.Encode()
	return out
}

// do performs one request, treating any 4xx or 5xx as an error.
func (c *Client) do(ctx context.Context, method string, target *url.URL, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("metadata: building a request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metadata: %s %s: %w", method, target.Path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // the body is read below

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("metadata: reading the response to %s %s: %w", method, target.Path, err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("metadata: %s %s returned %d: %s",
			method, target.Path, resp.StatusCode, bytes.TrimSpace(raw))
	}
	return raw, nil
}
