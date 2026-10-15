package handlers

import (
	"context"
	"encoding/csv"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

// ParseCSV handles POST /data/{data-id}/metadata/csv-parser.
//
// The first column names paths and the rest carry values, with the attributes in the header
// row. A path that is not absolute is taken as relative to the item named in the URL, which
// is what lets one file describe a whole folder's contents.
func (a *AVUs) ParseCSV(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
	if err != nil {
		return err
	}

	src := strings.TrimSpace(c.QueryParam("src"))
	if src == "" {
		return schemaError("src must be a non-blank string")
	}

	separator, err := csvSeparator(c.QueryParam("separator"))
	if err != nil {
		return err
	}

	scope, err := a.deps.OpenScope(ctx, user)
	if err != nil {
		return err
	}
	defer scope.Close()

	if err := requireKnownUser(ctx, scope, user, false); err != nil {
		return err
	}

	destination, err := a.readableItem(ctx, scope, user, c.Param("data-id"))
	if err != nil {
		return err
	}

	records, err := a.readCSV(ctx, scope, src, separator)
	if err != nil {
		return err
	}
	// Nothing to apply, which the Clojure service reports as an empty result, not an error.
	if len(records) < 2 {
		return writeJSONOK(c, map[string]any{"path-metadata": []map[string]any{}})
	}

	attributes := records[0][1:]

	// Every path is resolved and checked before anything is written, so a file naming one
	// path the caller cannot write to does not apply half its metadata.
	rows := records[1:]
	targets := make([]string, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, resolveCSVPath(destination.Path, row[0]))
	}
	if err := requireAllWriteable(ctx, scope, user, targets); err != nil {
		return err
	}

	applied := make([]map[string]any, 0, len(rows))
	for i, row := range rows {
		avus := csvAVUs(attributes, row[1:])

		if len(avus) > 0 {
			stat, err := scope.Stat(ctx, targets[i]).Get(ctx)
			if err != nil {
				return err
			}

			body := map[string]any{"avus": avus}
			targetType := service.MetadataTargetType(stat.Type)
			if err := a.deps.Metadata.UpdateAVUs(ctx, user, targetType, stat.UUID, body); err != nil {
				return err
			}
		}

		applied = append(applied, map[string]any{"path": targets[i], "avus": avus})
	}

	return writeJSONOK(c, map[string]any{"path-metadata": applied})
}

// readCSV reads and parses the source file.
func (a *AVUs) readCSV(ctx context.Context, scope *rods.Scope, src string, separator rune) ([][]string, error) {
	contents, err := scope.ReadFile(ctx, src)
	if err != nil {
		return nil, err
	}

	reader := csv.NewReader(strings.NewReader(string(contents)))
	reader.Comma = separator
	// Rows may be ragged: a path with fewer values than the header has attributes is not an
	// error, it just has fewer AVUs.
	reader.FieldsPerRecord = -1

	records, err := reader.ReadAll()
	if err != nil {
		return nil, apierror.New(apierror.ErrBadOrMissingField).
			With("path", src).
			With("reason", "the source file could not be parsed as delimited text")
	}

	for _, record := range records {
		for i := range record {
			record[i] = strings.TrimSpace(record[i])
		}
	}
	return records, nil
}

// csvAVUs pairs the header's attributes with one row's values.
//
// Always a slice, never nil: the response declares an array and a nil one marshals to null,
// which breaks a client counting the entries. A row with fewer values than the header has
// attributes simply carries fewer AVUs -- that is not an error, and the Clojure service zips
// the two the same way.
func csvAVUs(attributes, values []string) []service.AVU {
	out := make([]service.AVU, 0, len(attributes))

	for i, attribute := range attributes {
		if i >= len(values) {
			break
		}
		out = append(out, service.AVU{Attribute: attribute, Value: values[i]})
	}
	return out
}

// resolveCSVPath turns a path from the file into an absolute one.
func resolveCSVPath(base, path string) string {
	if strings.HasPrefix(path, "/") {
		return strings.TrimRight(path, "/")
	}
	return strings.TrimRight(paths.Join(base, path), "/")
}

// csvSeparator reads the delimiter, which arrives URL-encoded because a comma cannot be sent
// in a query parameter unescaped.
func csvSeparator(raw string) (rune, error) {
	if raw == "" {
		return ',', nil
	}

	// Not decoded again. echo has already percent-decoded the query parameter, so a second
	// pass would reject a literal percent sign -- which the Clojure service accepts.
	runes := []rune(raw)
	if len(runes) != 1 {
		return 0, schemaError("separator must be a single character")
	}
	return runes[0], nil
}
