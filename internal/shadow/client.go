package shadow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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

// stateWalkDepth bounds how far below a fixture root the state probe descends. Fixtures are
// shallow by construction; the bound is here so that a case which accidentally creates a deep
// tree cannot turn the probe into an unbounded crawl.
const stateWalkDepth = 6

// stateListingLimit bounds one page of the walk. A fixture with more entries than this would
// be compared incompletely, which is why write cases keep their trees small.
const stateListingLimit = 500

// StateOf reads a subtree's observable state, for comparing what a write left behind.
//
// It walks the tree rather than statting the root alone: a write that created the right
// number of things in the wrong place, or nothing at all below the top, looks identical from
// the root. Everything found is then statted in one request, so the comparison covers each
// path's type, size, checksum, timestamps and the requesting user's permission at every
// level.
//
// What it still does not cover is other users' access. `/path-info` reports the caller's own
// permission and a share count, not the access list, so a grant made to the wrong account is
// only visible here if it changes that count. Sharing cases need a probe that reads
// permissions directly.
//
// It reads through the reference service on both sides. The point is to compare the two
// services' effects, and reading each through itself would compare their reads as well,
// which the read cases already cover -- a difference here would then be ambiguous.
func (c *ServiceClient) StateOf(ctx context.Context, user, root string) ([]byte, error) {
	found, err := c.walk(ctx, user, root, stateWalkDepth)
	if err != nil {
		return nil, err
	}

	all := append([]string{root}, found...)
	return c.request(ctx, http.MethodPost, "/path-info", user, map[string]any{"paths": all})
}

// walk lists everything below a collection, depth first.
//
// A path that cannot be listed is not an error: a fixture may hold a data object, or a
// collection the user cannot see, and the stat of the parent is still worth comparing. What
// matters is that both sides are walked the same way.
func (c *ServiceClient) walk(ctx context.Context, user, root string, depth int) ([]string, error) {
	if depth <= 0 {
		return nil, nil
	}

	children, err := c.childrenOf(ctx, user, root)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(children.files)+len(children.folders))
	out = append(out, children.files...)

	for _, folder := range children.folders {
		out = append(out, folder)

		below, err := c.walk(ctx, user, folder, depth-1)
		if err != nil {
			return nil, err
		}
		out = append(out, below...)
	}

	sort.Strings(out)
	return out, nil
}

// entryPath is the one field of a listing entry the state probe needs; the rest is compared
// by the stat request that follows.
type entryPath struct {
	Path string `json:"path"`
}

// listing is what one collection directly holds.
type listing struct {
	files   []string
	folders []string
}

// childrenOf lists a collection, returning nothing when the path cannot be listed.
func (c *ServiceClient) childrenOf(ctx context.Context, user, root string) (listing, error) {
	zone, rest, ok := splitZone(root)
	if !ok {
		return listing{}, fmt.Errorf("shadow: %q is not a path under a zone", root)
	}

	query := url.Values{"user": {user}, "limit": {strconv.Itoa(stateListingLimit)}}
	target := "/data/path/" + zone + "/" + rest + "?" + query.Encode()

	body, err := c.send(ctx, http.MethodGet, target, "", nil)
	if err != nil {
		// A data object, or something the user cannot list. Either way there is nothing
		// below it to compare.
		return listing{}, nil
	}

	var resp struct {
		Files   []entryPath `json:"files"`
		Folders []entryPath `json:"folders"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return listing{}, fmt.Errorf("decoding the listing of %q: %w", root, err)
	}

	out := listing{
		files:   make([]string, 0, len(resp.Files)),
		folders: make([]string, 0, len(resp.Folders)),
	}
	for _, f := range resp.Files {
		out.files = append(out.files, f.Path)
	}
	for _, f := range resp.Folders {
		out.folders = append(out.folders, f.Path)
	}
	return out, nil
}

// splitZone separates a path's zone from the rest, which is how the listing route takes it.
func splitZone(p string) (zone, rest string, ok bool) {
	zone, rest, _ = strings.Cut(strings.TrimLeft(p, "/"), "/")
	if zone == "" || rest == "" {
		return "", "", false
	}
	return zone, rest, true
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
