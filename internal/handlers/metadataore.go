package handlers

import (
	"context"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/metadatafiles"
	"github.com/cyverse-de/data-info/internal/paths"
	"github.com/cyverse-de/data-info/internal/rods"
	"github.com/cyverse-de/data-info/internal/service"
	"github.com/labstack/echo/v4"
)

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
	if _, err := scope.Checksum(ctx, dataCitePath); err != nil {
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
	// As every other write here does: Jargon checksummed each object it wrote, and an
	// object without one reports an empty md5 for the rest of its life.
	if _, err := scope.Checksum(ctx, orePath); err != nil {
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
