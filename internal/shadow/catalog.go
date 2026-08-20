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

	// Skip records why a case is not run, so that a gap is visible in the report rather
	// than silently absent.
	Skip string `yaml:"skip"`
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
