package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// metadataSaveRequest is the body of POST /data/{data-id}/metadata/save.
type metadataSaveRequest struct {
	Dest      string `json:"dest"`
	Recursive bool   `json:"recursive"`
}

// savedItem is one entry of an exported metadata file.
//
// The exported shape follows a stat, with the item's AVUs alongside and its children nested,
// because whoever reads the file back expects the two to line up.
type savedItem struct {
	service.Stat

	// Metadata mixes iRODS AVUs with the metadata service's AVU objects, written whole with
	// their nested AVUs.
	Metadata []any       `json:"metadata"`
	Folders  []savedItem `json:"folders,omitempty"`
	Files    []savedItem `json:"files,omitempty"`
}

// Save handles POST /data/{data-id}/metadata/save.
//
// It writes a data item's metadata into the data store as a file, so that it can be moved
// with the data or read by something that does not speak to this service.
func (a *AVUs) Save(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	var body metadataSaveRequest
	if err := bindBody(c, &body); err != nil {
		return err
	}
	dest := strings.TrimRight(strings.TrimSpace(body.Dest), "/")
	if dest == "" {
		return schemaError("dest must be a non-blank string")
	}

	scope, err := a.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	source, err := a.readableItem(ctx, scope, user, c.Param("data-id"))
	if err != nil {
		return err
	}

	// These validators report a singular "path" key.
	destDir := paths.Dir(dest)
	if err := requirePathExists(ctx, scope, destDir); err != nil {
		return err
	}
	if err := requireWriteable(ctx, scope, destDir); err != nil {
		return err
	}
	if err := requireDoesNotExist(ctx, scope, dest); err != nil {
		return err
	}

	item, err := a.collect(ctx, scope, user, source, body.Recursive, new(int))
	if err != nil {
		return err
	}

	// Indented, because the file is meant to be read by a person as well as by a program.
	encoded, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the metadata export: %w", err)
	}

	if _, err := scope.WriteFile(ctx, dest, strings.NewReader(string(encoded))); err != nil {
		return err
	}
	if _, err := scope.Checksum(ctx, dest); err != nil {
		return err
	}

	stat, err := service.StatOf(ctx, scope, user, dest, service.StatOptions{
		Fields:      service.ParseFieldSet("", ""),
		Layout:      a.deps.Layout,
		PermsFilter: a.deps.PermsFilter,
	})
	if err != nil {
		return err
	}

	return writeJSONOK(c, uploadResponse{File: stat})
}

// collect gathers one item's metadata, and its children's when asked, checking the visited
// count against the limit as it goes because each item costs a stat, an ACL query and a
// metadata-service call.
func (a *AVUs) collect(
	ctx context.Context,
	scope *rods.Scope,
	user string,
	stat rods.Stat,
	recursive bool,
	visited *int,
) (savedItem, error) {
	*visited++
	if err := a.deps.CheckPathCount(*visited); err != nil {
		return savedItem{}, err
	}

	described, err := service.StatOf(ctx, scope, user, stat.Path, service.StatOptions{
		Fields:      service.ParseFieldSet("", ""),
		Layout:      a.deps.Layout,
		PermsFilter: a.deps.PermsFilter,
	})
	if err != nil {
		return savedItem{}, err
	}

	held, err := scope.AVUs(ctx, stat.Path).Get(ctx)
	if err != nil {
		return savedItem{}, err
	}

	visible := service.VisibleAVUs(held, false)
	item := savedItem{Stat: described, Metadata: make([]any, 0, len(visible))}
	for _, avu := range visible {
		item.Metadata = append(item.Metadata, avu)
	}

	// Both kinds of metadata, merged. The exported file is meant to stand on its own, so
	// leaving out the half this service does not store would make it useless.
	template, err := a.deps.Metadata.ListAVUs(ctx, user,
		service.MetadataTargetType(stat.Type), described.ID)
	if err != nil {
		return savedItem{}, err
	}
	if raw, ok := template["avus"].([]any); ok {
		item.Metadata = append(item.Metadata, raw...)
	}

	if !recursive || stat.Type != rods.ObjectTypeDir {
		return item, nil
	}

	children, err := scope.Children(ctx, stat.Path)
	if err != nil {
		return savedItem{}, err
	}

	for _, path := range children {
		childStat, err := scope.Stat(ctx, path).Get(ctx)
		if err != nil {
			return savedItem{}, err
		}

		child, err := a.collect(ctx, scope, user, childStat, recursive, visited)
		if err != nil {
			return savedItem{}, err
		}

		if childStat.Type == rods.ObjectTypeDir {
			item.Folders = append(item.Folders, child)
			continue
		}
		item.Files = append(item.Files, child)
	}

	return item, nil
}
