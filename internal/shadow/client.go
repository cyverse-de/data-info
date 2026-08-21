package shadow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ServiceClient performs the few operations the harness itself needs, as opposed to the
// requests it is comparing.
//
// It talks to the reference service on purpose. Building a fixture through the service under
// test would mean a comparison whose setup already assumed the thing being tested was
// correct; if the reference cannot build it, the case cannot be run at all, which is the
// honest outcome.
type ServiceClient struct {
	base   string
	client *http.Client
}

// NewServiceClient returns a client for a service.
func NewServiceClient(base string) *ServiceClient {
	return &ServiceClient{
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: 2 * time.Minute},
	}
}

// CreateDirectory creates a collection and any missing parents.
func (c *ServiceClient) CreateDirectory(ctx context.Context, user, path string) error {
	return c.post(ctx, "/data/directories", user, map[string]any{"paths": []string{path}})
}

// DeletePath removes a path, moving it to trash as the service normally would.
func (c *ServiceClient) DeletePath(ctx context.Context, user, path string) error {
	return c.post(ctx, "/deleter", user, map[string]any{"paths": []string{path}})
}

// Exists reports whether a path is visible to a user.
func (c *ServiceClient) Exists(ctx context.Context, user, path string) (bool, error) {
	body, err := c.request(ctx, http.MethodPost, "/existence-marker", user,
		map[string]any{"paths": []string{path}})
	if err != nil {
		return false, err
	}

	var resp struct {
		Paths map[string]bool `json:"paths"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, fmt.Errorf("decoding the existence response: %w", err)
	}
	return resp.Paths[path], nil
}

// UploadFile creates a data object with the given contents, for cases that need something
// to already be there.
func (c *ServiceClient) UploadFile(ctx context.Context, user, dir, name, content string) error {
	encoded, contentType, err := multipartBody(&Upload{Filename: name, Content: content})
	if err != nil {
		return err
	}

	query := url.Values{"user": {user}, "dest": {dir}}
	_, err = c.send(ctx, http.MethodPost, "/data?"+query.Encode(), contentType, bytes.NewReader(encoded))
	return err
}

// UUIDForPath resolves a path to the id the by-id endpoints take.
func (c *ServiceClient) UUIDForPath(ctx context.Context, user, path string) (string, error) {
	query := url.Values{"user": {user}, "path": {path}}

	body, err := c.send(ctx, http.MethodGet, "/data/uuid?"+query.Encode(), "", nil)
	if err != nil {
		return "", err
	}

	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decoding the uuid response: %w", err)
	}
	if resp.ID == "" {
		return "", fmt.Errorf("no id was returned for %q", path)
	}
	return resp.ID, nil
}

// StateOf reads a subtree's observable state, for comparing what a write left behind.
//
// It reads through the reference service on both sides. The point is to compare the two
// services' effects, and reading each through itself would compare their reads as well,
// which the read cases already cover -- a difference here would then be ambiguous.
func (c *ServiceClient) StateOf(ctx context.Context, user, root string) ([]byte, error) {
	return c.request(ctx, http.MethodPost, "/path-info", user, map[string]any{"paths": []string{root}})
}

func (c *ServiceClient) post(ctx context.Context, path, user string, body any) error {
	_, err := c.request(ctx, http.MethodPost, path, user, body)
	return err
}

func (c *ServiceClient) request(ctx context.Context, method, path, user string, body any) ([]byte, error) {
	target := path + "?" + url.Values{"user": {user}}.Encode()

	var (
		reader      io.Reader
		contentType string
	)
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader, contentType = bytes.NewReader(encoded), "application/json"
	}

	return c.send(ctx, method, target, contentType, reader)
}

// send performs one request against the service, treating any 4xx or 5xx as a failure of
// the harness's own setup rather than something to compare.
func (c *ServiceClient) send(
	ctx context.Context,
	method, pathAndQuery, contentType string,
	body io.Reader,
) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+pathAndQuery, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // nothing actionable on close

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s returned %d: %s", method, pathAndQuery, resp.StatusCode, truncate(raw))
	}
	return raw, nil
}

// truncate keeps an error message readable when a service answers with a long body.
func truncate(raw []byte) string {
	const limit = 300
	if len(raw) <= limit {
		return string(raw)
	}
	return string(raw[:limit]) + "..."
}
