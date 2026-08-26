package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/metadatafiles"
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

	Metadata []service.AVU `json:"metadata"`
	Folders  []savedItem   `json:"folders,omitempty"`
	Files    []savedItem   `json:"files,omitempty"`
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

	// The singular validators throughout: that is the family the Clojure service uses on this
	// route.
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

// collect gathers one item's metadata, and its children's when the request asks for it.
//
// The visited count is carried through the walk and checked against the request limit. Each
// item costs a stat, an access list and an HTTP call to the metadata service, so a recursive
// export of a large collection would otherwise hold a request -- and an iRODS connection --
// open for thousands of round trips. The Clojure service counts what is under the folder
// before starting; counting as it goes reaches the same limit without a second recursive
// query.
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

	item := savedItem{Stat: described, Metadata: service.VisibleAVUs(held, false)}

	// Both kinds of metadata, merged. The exported file is meant to stand on its own, so
	// leaving out the half this service does not store would make it useless.
	template, err := a.deps.Metadata.ListAVUs(ctx, user,
		service.MetadataTargetType(stat.Type), described.ID)
	if err != nil {
		return savedItem{}, err
	}
	item.Metadata = append(item.Metadata, templateAVUs(template)...)

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

// templateAVUs pulls the AVU list out of the metadata service's response, which is otherwise
// passed through untouched.
func templateAVUs(response map[string]any) []service.AVU {
	raw, ok := response["avus"].([]any)
	if !ok {
		return nil
	}

	out := make([]service.AVU, 0, len(raw))
	for _, item := range raw {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}

		attribute, _ := object["attr"].(string)
		value, _ := object["value"].(string)
		unit, _ := object["unit"].(string)
		out = append(out, service.AVU{Attribute: attribute, Value: value, Unit: unit})
	}
	return out
}

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
	if len(records) < 2 {
		return schemaError("the source file needs a header row and at least one row of values")
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

// SaveORE handles POST /data/{data-id}/ore/save.
//
// It writes the two files DataONE reads: a resource map saying what the data set contains, and
// a DataCite record describing it. Both go in a directory beside the data set rather than in
// it, so that harvesting the data set does not harvest its own metadata.
func (a *AVUs) SaveORE(c echo.Context) error {
	ctx := c.Request().Context()

	user, err := requireUser(c)
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

	dataSet, err := a.readableItem(ctx, scope, user, c.Param("data-id"))
	if err != nil {
		return err
	}
	if dataSet.Type != rods.ObjectTypeDir {
		return apierror.New(apierror.ErrNotAFolder).With("path", dataSet.Path)
	}
	if err := requireWriteable(ctx, scope, dataSet.Path); err != nil {
		return err
	}

	metadataDir := a.metadataDirFor(dataSet.Path)
	orePath := paths.Join(metadataDir, "ore.xml")
	dataCitePath := paths.Join(metadataDir, "cyverse-metadata.xml")

	response, err := a.deps.Metadata.ListAVUs(ctx, user, "folder", c.Param("data-id"))
	if err != nil {
		return err
	}
	avus := templateAVUs(response)

	document, err := metadatafiles.BuildDataCite(dataCiteAVUs(avus))
	if err != nil {
		return apierror.New(apierror.ErrBadOrMissingField).
			With("path", dataSet.Path).
			With("reason", err.Error())
	}

	if err := scope.MakeDir(ctx, metadataDir, true); err != nil {
		return err
	}

	// Both files are created before the resource map is built, even though the resource map
	// is what is about to be written. It refers to itself and to the DataCite file by the
	// identifiers iRODS assigns, and an object has no identifier until it exists -- so
	// building first would produce a map naming itself as the empty string, and only a second
	// save of the same data set would come out right. The Clojure service creates them both
	// up front for exactly this reason.
	if _, err := scope.WriteFile(ctx, dataCitePath, strings.NewReader(document)); err != nil {
		return err
	}
	if err := a.ensureExists(ctx, scope, orePath); err != nil {
		return err
	}

	resourceMap, err := a.buildResourceMap(ctx, scope, dataSet.Path, metadataDir, orePath, dataCitePath, avus)
	if err != nil {
		return err
	}
	if _, err := scope.WriteFile(ctx, orePath, strings.NewReader(resourceMap)); err != nil {
		return err
	}

	if err := a.recordOREMetadata(ctx, orePath, dataSet.Path, metadataDir); err != nil {
		return err
	}

	return c.NoContent(200)
}

// buildResourceMap assembles the resource map from what is now in the metadata directory.
func (a *AVUs) buildResourceMap(
	ctx context.Context,
	scope *rods.Scope,
	dataSetPath, metadataDir, orePath, dataCitePath string,
	avus []service.AVU,
) (string, error) {
	archived, err := a.archivedFiles(ctx, scope, dataSetPath, metadataDir, orePath, dataCitePath)
	if err != nil {
		return "", err
	}

	aggregation, err := a.objectURI(ctx, scope, dataSetPath)
	if err != nil {
		return "", err
	}

	return metadatafiles.BuildORE(metadatafiles.OREInput{
		AggregationURI: aggregation,
		ResourceMap:    archived.resourceMap,
		Metadata:       archived.dataCite,
		Files:          archived.files,
		AVUs:           dataCiteAVUs(avus),
	}), nil
}

// ensureExists creates an empty data object when there is nothing at the path.
//
// Rewriting one that is already there would change its identifier, and the resource map from
// a previous save refers to it by that identifier.
func (a *AVUs) ensureExists(ctx context.Context, scope *rods.Scope, path string) error {
	present, err := scope.FileExists(ctx, path)
	if err != nil {
		return err
	}
	if present {
		return nil
	}

	_, err = scope.WriteFile(ctx, path, strings.NewReader(""))
	return err
}

// oreFiles are the objects a resource map refers to.
type oreFiles struct {
	resourceMap metadatafiles.ArchivedFile
	dataCite    metadatafiles.ArchivedFile
	files       []metadatafiles.ArchivedFile
}

// archivedFiles collects the data set's objects and the two metadata files, keeping the two
// apart: a resource map must not list itself or the record describing it among the data.
func (a *AVUs) archivedFiles(
	ctx context.Context,
	scope *rods.Scope,
	dataSetPath, metadataDir, orePath, dataCitePath string,
) (oreFiles, error) {
	var out oreFiles

	for _, dir := range []string{dataSetPath, metadataDir} {
		// Everything underneath, not just what the collection directly holds. A data set
		// with subfolders would otherwise publish a resource map that under-declares what
		// DataONE should harvest, which is worse than failing: the harvest succeeds and
		// silently takes less than the data set contains.
		files, err := a.filesUnder(ctx, scope, dir)
		if err != nil {
			return oreFiles{}, err
		}

		for _, path := range files {
			stat, err := scope.Stat(ctx, path).Get(ctx)
			if err != nil {
				return oreFiles{}, err
			}

			file := metadatafiles.ArchivedFile{ID: stat.UUID, URI: a.uriFor(stat.UUID)}
			switch path {
			case orePath:
				out.resourceMap = file
			case dataCitePath:
				out.dataCite = file
			default:
				out.files = append(out.files, file)
			}
		}
	}

	return out, nil
}

// filesUnder lists every data object at or below a collection.
func (a *AVUs) filesUnder(ctx context.Context, scope *rods.Scope, path string) ([]string, error) {
	entries, err := scope.ChildEntries(ctx, path)
	if err != nil {
		return nil, err
	}

	var out []string
	for _, entry := range entries {
		if !entry.IsDir {
			out = append(out, entry.Path)
			continue
		}

		below, err := a.filesUnder(ctx, scope, entry.Path)
		if err != nil {
			return nil, err
		}
		out = append(out, below...)
	}
	return out, nil
}

// recordOREMetadata marks the resource map as one, and points the data set at its metadata
// directory so that whoever harvests it can find the pair.
func (a *AVUs) recordOREMetadata(ctx context.Context, orePath, dataSetPath, metadataDir string) error {
	proxy, err := a.deps.OpenProxyScope(ctx)
	if err != nil {
		return err
	}
	defer proxy.Close()

	writes := []struct {
		path      string
		attribute string
		value     string
	}{
		{orePath, a.deps.DataONE.OREAttribute, "true"},
		{orePath, a.deps.DataONE.FormatIDAttribute, metadatafiles.OREFormatID},
		{dataSetPath, a.deps.DataONE.MetadataDirAttribute, metadataDir},
	}

	for _, write := range writes {
		err := proxy.SetAVU(ctx, write.path, rods.AVU{
			Attribute: write.attribute,
			Value:     write.value,
			Unit:      "",
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// metadataDirFor names where a data set's metadata files live.
//
// Beside the data set's parent rather than inside the data set, so that harvesting the data
// set does not harvest its own metadata: /zone/home/u/repo/curated/set puts them in
// /zone/home/u/repo/curated_metadata/set.
func (a *AVUs) metadataDirFor(dataSetPath string) string {
	grandparent := paths.Dir(paths.Dir(dataSetPath))
	return paths.Join(grandparent, a.deps.DataONE.MetadataDirname, paths.Base(dataSetPath))
}

// objectURI names where the member node serves a path.
func (a *AVUs) objectURI(ctx context.Context, scope *rods.Scope, path string) (string, error) {
	stat, err := scope.Stat(ctx, path).Get(ctx)
	if err != nil {
		return "", err
	}
	return a.uriFor(stat.UUID), nil
}

// uriFor builds a member node object address.
func (a *AVUs) uriFor(id string) string {
	return strings.TrimRight(a.deps.DataONE.MemberNodeBase, "/") + "/v1/object/" + id
}

// dataCiteAVUs converts AVUs into what the document builders take.
func dataCiteAVUs(avus []service.AVU) []metadatafiles.AVU {
	out := make([]metadatafiles.AVU, 0, len(avus))
	for _, avu := range avus {
		out = append(out, metadatafiles.AVU{Attribute: avu.Attribute, Value: avu.Value})
	}
	return out
}
