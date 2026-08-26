package shadow

import (
	"context"
	"fmt"
	"strings"
)

// Sides are the two copies a write case gets, one per service. Each service touches only
// its own, so the two never contend for the same object and a difference is a difference in
// behaviour rather than a race.
const (
	SideReference = "A"
	SideCandidate = "B"
)

// ScratchGuard refuses to operate outside a collection set aside for this purpose.
//
// The harness creates and deletes real collections in a zone other people share, so the root
// it works under is checked twice: it must sit under the configured scratch prefix, and every
// path derived from it is re-checked before it is used. A typo in a flag should stop the run,
// not delete somebody's data.
type ScratchGuard struct {
	// Root is the collection every fixture lives under.
	Root string
}

// NewScratchGuard validates a root and returns a guard for it.
func NewScratchGuard(root string) (*ScratchGuard, error) {
	trimmed := strings.TrimRight(root, "/")

	if !strings.HasPrefix(trimmed, "/") {
		return nil, fmt.Errorf("shadow: the scratch root %q is not an absolute path", root)
	}
	// A root of /zone or /zone/home would put every fixture beside real data.
	if strings.Count(trimmed, "/") < 4 {
		return nil, fmt.Errorf("shadow: the scratch root %q is too shallow; use a dedicated collection", root)
	}
	if !strings.Contains(trimmed, "shadow") {
		return nil, fmt.Errorf("shadow: the scratch root %q must name itself as scratch, so it cannot be confused with real data", root)
	}

	return &ScratchGuard{Root: trimmed}, nil
}

// Check reports whether a path is inside the scratch root.
func (g *ScratchGuard) Check(path string) error {
	trimmed := strings.TrimRight(path, "/")

	if trimmed != g.Root && !strings.HasPrefix(trimmed, g.Root+"/") {
		return fmt.Errorf("shadow: refusing to touch %q, which is outside %q", path, g.Root)
	}
	// A path that climbs out again is not inside, whatever its prefix says.
	if strings.Contains(trimmed, "/../") || strings.HasSuffix(trimmed, "/..") {
		return fmt.Errorf("shadow: refusing to touch %q, which escapes the scratch root", path)
	}
	return nil
}

// SidePath is where one service's copy of a case's fixture lives.
func (g *ScratchGuard) SidePath(runID, caseID, side string) string {
	return fmt.Sprintf("%s/%s/%s/%s", g.Root, runID, side, caseID)
}

// Fixtures creates and removes the trees a write case needs.
type Fixtures struct {
	guard *ScratchGuard
	runID string

	// client talks to whichever service is used to build and tear down fixtures. It is
	// deliberately the reference: the harness must not depend on the service under test
	// being correct in order to set up a comparison of it.
	client *ServiceClient
}

// NewFixtures returns a fixture builder.
func NewFixtures(guard *ScratchGuard, runID string, client *ServiceClient) *Fixtures {
	return &Fixtures{guard: guard, runID: runID, client: client}
}

// Prepare creates the pair of trees a case needs and returns their roots.
func (f *Fixtures) Prepare(ctx context.Context, c Case, user string) (reference, candidate string, err error) {
	reference = f.guard.SidePath(f.runID, c.ID, SideReference)
	candidate = f.guard.SidePath(f.runID, c.ID, SideCandidate)

	for _, path := range []string{reference, candidate} {
		if err := f.guard.Check(path); err != nil {
			return "", "", err
		}
		if err := f.client.CreateDirectory(ctx, user, path); err != nil {
			return "", "", fmt.Errorf("preparing %s: %w", path, err)
		}
	}

	return reference, candidate, nil
}

// Seed puts a case's seed file inside one side's fixture and returns the variables that
// name it.
//
// It goes in through the reference service, like every other fixture: a case that tested the
// candidate's uploads by using the candidate to build its own input would not be testing
// much.
func (f *Fixtures) Seed(ctx context.Context, seed *Seed, user, root string) (map[string]string, error) {
	path := root + "/" + seed.Filename
	if err := f.guard.Check(path); err != nil {
		return nil, err
	}

	if err := f.client.UploadFile(ctx, user, root, seed.Filename, seed.Content); err != nil {
		return nil, fmt.Errorf("seeding %s: %w", path, err)
	}

	id, err := f.client.UUIDForPath(ctx, user, path)
	if err != nil {
		return nil, fmt.Errorf("resolving the id of %s: %w", path, err)
	}

	return map[string]string{"SeedPath": path, "SeedID": id}, nil
}

// Remove deletes a run's trees.
//
// A failure here is reported rather than ignored: what it leaves behind is real data in a
// shared zone, and someone has to know it is there.
func (f *Fixtures) Remove(ctx context.Context, user string) error {
	root := f.guard.Root + "/" + f.runID
	if err := f.guard.Check(root); err != nil {
		return err
	}
	return f.client.DeletePath(ctx, user, root)
}
