package shadow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Tier says how a case may be run, which decides what it is safe to do with it.
type Tier string

const (
	// TierRead only reads. Both services can be pointed at the same paths.
	TierRead Tier = "read"

	// TierWrite changes something, so each service gets its own copy of the fixture and
	// the results are compared after canonicalising which copy was touched.
	TierWrite Tier = "write"

	// TierAsync changes something in the background and returns a task id, so the
	// comparison has to wait for the task to finish before reading the outcome.
	TierAsync Tier = "async"
)

// Case is one request to compare.
type Case struct {
	// ID names the case in reports. It must be unique.
	ID string `yaml:"id"`

	// Tier decides how the case is run.
	Tier Tier `yaml:"tier"`

	Method string            `yaml:"method"`
	Path   string            `yaml:"path"`
	Query  map[string]string `yaml:"query"`
	Body   any               `yaml:"body"`

	// Upload makes the case a multipart file upload rather than a JSON request. The two
	// are mutually exclusive.
	Upload *Upload `yaml:"upload"`

	// Seed puts a file in each side's fixture before the case runs, and exposes it as
	// {{.SeedPath}} and {{.SeedID}}. It is what lets a by-id case name something that
	// exists.
	Seed *Seed `yaml:"seed"`

	// Before puts the fixture into the state the case needs, one step at a time. Each step
	// runs against both sides independently, so the state a case starts from is the state
	// that side's own service produced -- which is the point for a case like restore,
	// where what is being compared depends on where the preceding delete put things.
	//
	// Steps are setup, not subjects: their responses are not compared, and a step that
	// fails fails the case rather than being reported as a difference.
	Before []Step `yaml:"before"`

	// Skip records why a case is not run, so that a gap is visible in the report rather
	// than silently absent.
	Skip string `yaml:"skip"`
}

// Step is one action taken before the case's own request.
//
// Exactly one of Seed or Method is set: a step either places a file or sends a request.
type Step struct {
	// Seed places a file, exactly as a case's own seed does, and rebinds {{.SeedPath}}
	// and {{.SeedID}} for the steps and the request that follow. Re-seeding a path a
	// previous step emptied is how a case sets up a collision.
	Seed *Seed `yaml:"seed"`

	Method string            `yaml:"method"`
	Path   string            `yaml:"path"`
	Query  map[string]string `yaml:"query"`
	Body   any               `yaml:"body"`

	// Await waits for the task the step creates before the next step runs. Without it a
	// step whose work happens in the background would still be running when the case's
	// own request arrives, which is the race the async tier exists to remove.
	Await bool `yaml:"await"`
}

// describe names a step in an error, since steps have no ids of their own.
func (s Step) describe() string {
	if s.Seed != nil {
		return "seed " + s.Seed.Filename
	}
	return s.Method + " " + s.Path
}

// Upload is the file an upload case sends.
type Upload struct {
	// Field is the multipart field name, which the endpoints declare as "file".
	Field string `yaml:"field"`

	// Filename names the object the upload creates, so it is what the destination is
	// named after on POST /data.
	Filename string `yaml:"filename"`

	// Content is the body. It is written literally, so a case can pin a media type or a
	// checksum by choosing bytes rather than by asserting on them.
	Content string `yaml:"content"`
}

// Seed is a file placed in a write case's fixture before the case runs.
type Seed struct {
	// Filename names the object inside the side's fixture root.
	Filename string `yaml:"filename"`

	// Content is what the object holds, so a case that replaces it can tell the old
	// contents from the new.
	Content string `yaml:"content"`
}

// Group is a file of cases.
type Group struct {
	Group string `yaml:"group"`
	Cases []Case `yaml:"cases"`
}

// Catalog is every case to run.
type Catalog struct {
	Groups []Group
}

// LoadCatalog reads every .yaml file in a directory.
func LoadCatalog(dir string) (*Catalog, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading the catalog directory: %w", err)
	}

	catalog := &Catalog{}
	seen := map[string]string{}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		var group Group
		if err := yaml.Unmarshal(raw, &group); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}

		for _, c := range group.Cases {
			if c.ID == "" {
				return nil, fmt.Errorf("%s: a case has no id", path)
			}
			// Duplicate ids would make a report ambiguous about which case failed.
			if where, ok := seen[c.ID]; ok {
				return nil, fmt.Errorf("%s: case %q is already defined in %s", path, c.ID, where)
			}
			seen[c.ID] = path

			if c.Body != nil && c.Upload != nil {
				return nil, fmt.Errorf("%s: case %q has both a body and an upload, so its request shape is ambiguous", path, c.ID)
			}
			if c.Seed != nil && c.Tier != TierWrite && c.Tier != TierAsync {
				return nil, fmt.Errorf("%s: case %q seeds a fixture but is not a write case, so it has no fixture to seed", path, c.ID)
			}
			if len(c.Before) > 0 && c.Tier != TierWrite && c.Tier != TierAsync {
				return nil, fmt.Errorf("%s: case %q has setup steps but is not a write case, so it has no fixture to set up", path, c.ID)
			}
			for i, step := range c.Before {
				switch {
				case step.Seed != nil && step.Method != "":
					return nil, fmt.Errorf("%s: case %q step %d both seeds and sends a request", path, c.ID, i+1)
				case step.Seed == nil && step.Method == "":
					return nil, fmt.Errorf("%s: case %q step %d neither seeds nor sends a request", path, c.ID, i+1)
				case step.Seed != nil && step.Await:
					// Seeding is synchronous, so awaiting one would wait for a task
					// that is never created and fail the case after the timeout.
					return nil, fmt.Errorf("%s: case %q step %d awaits a seed, which creates no task", path, c.ID, i+1)
				}
			}
		}

		catalog.Groups = append(catalog.Groups, group)
	}

	return catalog, nil
}

// Cases returns every case, flattened.
func (c *Catalog) Cases() []Case {
	var out []Case
	for _, g := range c.Groups {
		out = append(out, g.Cases...)
	}
	return out
}

// Expand substitutes the run's variables into a case.
//
// Templates are deliberately simple: a case names {{.User}}, {{.Zone}} or {{.Root}}, and
// nothing else. A richer template language would let a case express things the runner
// cannot reason about when deciding whether it is safe to run.
func (c Case) Expand(vars map[string]string) Case {
	expanded := c
	expanded.Path = substitute(c.Path, vars)

	if len(c.Query) > 0 {
		expanded.Query = make(map[string]string, len(c.Query))
		for k, v := range c.Query {
			expanded.Query[k] = substitute(v, vars)
		}
	}

	expanded.Body = substituteValue(c.Body, vars)

	if c.Upload != nil {
		upload := *c.Upload
		upload.Filename = substitute(upload.Filename, vars)
		upload.Content = substitute(upload.Content, vars)
		expanded.Upload = &upload
	}

	return expanded
}

// Expand substitutes the run's variables into a step, the same way a case is expanded.
func (s Step) Expand(vars map[string]string) Step {
	expanded := s
	expanded.Path = substitute(s.Path, vars)

	if len(s.Query) > 0 {
		expanded.Query = make(map[string]string, len(s.Query))
		for k, v := range s.Query {
			expanded.Query[k] = substitute(v, vars)
		}
	}

	expanded.Body = substituteValue(s.Body, vars)
	return expanded
}

func substitute(s string, vars map[string]string) string {
	for name, value := range vars {
		s = strings.ReplaceAll(s, "{{."+name+"}}", value)
	}
	return s
}

func substituteValue(value any, vars map[string]string) any {
	switch typed := value.(type) {
	case string:
		return substitute(typed, vars)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = substituteValue(v, vars)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, v := range typed {
			out = append(out, substituteValue(v, vars))
		}
		return out
	default:
		return value
	}
}
