// Package notifications tells the notification agent that a long-running data operation
// finished, so the user sees it in the DE rather than having to watch a folder.
//
// The message bodies are user-visible text and the DE renders them verbatim, so the wording
// here is copied from the Clojure service rather than improved on. Only the notification
// agent reads the payload, and it keys on the action.
package notifications

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

	"github.com/cyverse-de/data-info/internal/paths"
)

// Actions the DE distinguishes. They appear in the payload and the UI switches on them.
const (
	ActionMove       = "move"
	ActionRename     = "rename"
	ActionTrash      = "trash"
	ActionDelete     = "delete"
	ActionRestore    = "restore"
	ActionEmptyTrash = "empty_trash"
)

// notificationType is the only kind this service sends.
const notificationType = "data"

// Message is one notification.
type Message struct {
	Type    string  `json:"type"`
	User    string  `json:"user"`
	Subject string  `json:"subject"`
	Message string  `json:"message"`
	Payload Payload `json:"payload"`
}

// Payload is what the DE's UI reads to decide what to refresh.
type Payload struct {
	Action string   `json:"action"`
	Paths  []string `json:"paths"`
}

// Client posts notifications.
type Client struct {
	base *url.URL
	http *http.Client
}

// New builds a client for the notification agent at base.
func New(base string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("notifications: base URL %q is not usable: %w", base, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("notifications: base URL %q needs a scheme and a host", base)
	}

	return &Client{base: parsed, http: &http.Client{Timeout: timeout}}, nil
}

// Send posts one notification.
func (c *Client) Send(ctx context.Context, message Message) error {
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("notifications: encoding a message: %w", err)
	}

	target := c.base.JoinPath("notification").String()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notifications: building a request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("notifications: posting a %s notification: %w", message.Payload.Action, err)
	}
	defer resp.Body.Close() //nolint:errcheck // the body is drained below

	if resp.StatusCode >= 400 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) //nolint:errcheck // the request already failed
		return fmt.Errorf("notifications: posting a %s notification returned %d: %s",
			message.Payload.Action, resp.StatusCode, bytes.TrimSpace(detail))
	}

	_, _ = io.Copy(io.Discard, resp.Body) //nolint:errcheck // nothing actionable
	return nil
}

// outcome is the word every subject and message opens with.
func outcome(failed bool) string {
	if failed {
		return "Failed"
	}
	return "Finished"
}

// Move reports a completed move.
func Move(user string, sources, destinations []string, failed bool) Message {
	var into string
	if len(destinations) > 0 {
		into = paths.Dir(destinations[0])
	}

	return Message{
		Type:    notificationType,
		User:    user,
		Subject: fmt.Sprintf("%s moving %d file(s)/folder(s) to %s", outcome(failed), len(sources), into),
		Message: fmt.Sprintf("%s moving %s to %s",
			outcome(failed), strings.Join(sources, ", "), strings.Join(destinations, ", ")),
		Payload: Payload{Action: ActionMove, Paths: destinations},
	}
}

// Rename reports a completed rename.
//
// Both lists hold exactly one path, and the wording says so; the signature keeps the lists
// because the Clojure service's does, and because a rename is a move of one thing.
func Rename(user string, sources, destinations []string, failed bool) Message {
	source, destination := first(sources), first(destinations)

	return Message{
		Type:    notificationType,
		User:    user,
		Subject: fmt.Sprintf("%s renaming %s to %s", outcome(failed), source, paths.Base(destination)),
		Message: fmt.Sprintf("%s renaming %s to %s", outcome(failed), source, destination),
		Payload: Payload{Action: ActionRename, Paths: destinations},
	}
}

// Trash reports paths moved to the trash.
func Trash(user string, sources, destinations []string, failed bool) Message {
	return Message{
		Type:    notificationType,
		User:    user,
		Subject: fmt.Sprintf("%s moving %d files(s)/folder(s) to trash", outcome(failed), len(sources)),
		Message: fmt.Sprintf("%s moving %s to trash at %s",
			outcome(failed), strings.Join(sources, ", "), strings.Join(destinations, ", ")),
		Payload: Payload{Action: ActionTrash, Paths: destinations},
	}
}

// Delete reports paths removed outright.
//
// The payload names the user's trash rather than what was deleted, because the paths in a
// payload tell the UI what to refresh and the deleted ones no longer exist.
func Delete(user string, sources []string, layout paths.Layout, failed bool) Message {
	return Message{
		Type:    notificationType,
		User:    user,
		Subject: fmt.Sprintf("%s deleting %d file(s)/folder(s)", outcome(failed), len(sources)),
		Message: fmt.Sprintf("%s deleting %s", outcome(failed), strings.Join(sources, ", ")),
		Payload: Payload{Action: ActionDelete, Paths: []string{layout.UserTrash(user)}},
	}
}

// Restore reports paths taken back out of the trash.
func Restore(user string, sources, destinations []string, failed bool) Message {
	return Message{
		Type: notificationType,
		User: user,
		Subject: fmt.Sprintf("%s restoring %d files(s)/folder(s) to their original locations",
			outcome(failed), len(sources)),
		Message: fmt.Sprintf("%s restoring %s to %s",
			outcome(failed), strings.Join(sources, ", "), strings.Join(destinations, ", ")),
		Payload: Payload{Action: ActionRestore, Paths: destinations},
	}
}

// EmptyTrash reports an emptied trash.
func EmptyTrash(user string, layout paths.Layout, failed bool) Message {
	return Message{
		Type:    notificationType,
		User:    user,
		Subject: outcome(failed) + " emptying trash",
		Message: outcome(failed) + " emptying trash",
		Payload: Payload{Action: ActionEmptyTrash, Paths: []string{layout.UserTrash(user)}},
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
