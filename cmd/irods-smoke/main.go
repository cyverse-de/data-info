// Command irods-smoke exercises the iRODS client against a live server and times the
// operations the service's bulk endpoints depend on.
//
// It exists to answer one question early: whether reads that the plan moves from catalog
// SQL onto the iRODS protocol are fast enough at the sizes data-info actually sees.
// /path-info and /permissions-gatherer accept up to max_paths_in_request paths, so a
// per-path round trip that looks fine once is not obviously fine a thousand times. Finding
// that out here is much cheaper than finding it out after the endpoints are written.
//
// It only reads. Nothing it does creates, modifies or deletes anything.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/cyverse-de/data-info/internal/config"
	"github.com/cyverse-de/data-info/internal/irodsclient"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "irods-smoke: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("config", "", "path to the YAML configuration file")
		dotEnvPath = flag.String("dotenv-path", "", "path to a dotenv file")
		envPrefix  = flag.String("env-prefix", config.DefaultEnvPrefix, "environment variable prefix")
		user       = flag.String("user", "", "iRODS user to act as (defaults to the proxy account)")
		path       = flag.String("path", "", "collection to exercise (defaults to the user's home)")
		bulk       = flag.Int("bulk", 100, "how many children to time per-path operations over")
		asJSON     = flag.Bool("json", false, "emit results as JSON")
		parallel   = flag.Int("parallel", 1, "how many per-path operations to run concurrently")
	)
	flag.Parse()

	cfg, err := config.Load(config.Settings{
		ConfigPath: *configPath,
		DotEnvPath: *dotEnvPath,
		EnvPrefix:  *envPrefix,
	})
	if err != nil {
		return fmt.Errorf("configuration is not usable: %w", err)
	}

	pool, err := irodsclient.NewPool(irodsclient.Config{
		Host:          cfg.IRODS.Host,
		Port:          cfg.IRODS.Port,
		Zone:          cfg.IRODS.Zone,
		ProxyUser:     cfg.IRODS.User,
		ProxyPassword: cfg.IRODS.Password,
		Resource:      cfg.IRODS.Resource,
		AppName:       "irods-smoke",
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	session, err := openSession(ctx, pool, *user)
	if err != nil {
		return err
	}
	defer session.Close()

	target := *path
	if target == "" {
		target = cfg.IRODS.Home + "/" + effectiveUser(cfg, *user)
	}

	results, err := measure(ctx, session, cfg, target, *bulk, *parallel)
	if err != nil {
		return err
	}

	return report(results, *asJSON)
}

func openSession(ctx context.Context, pool *irodsclient.Pool, user string) (*irodsclient.Session, error) {
	if user == "" {
		return pool.Admin(ctx)
	}
	return pool.ForUser(ctx, user)
}

func effectiveUser(cfg *config.Config, user string) string {
	if user == "" {
		return cfg.IRODS.User
	}
	return user
}

// step is one timed operation.
type step struct {
	Name    string        `json:"name"`
	Detail  string        `json:"detail,omitempty"`
	Elapsed time.Duration `json:"-"`
	Millis  float64       `json:"millis"`
	Err     string        `json:"error,omitempty"`
}

func measure(ctx context.Context, s *irodsclient.Session, cfg *config.Config, target string, bulk, parallel int) ([]step, error) {
	var steps []step

	timed := func(name string, fn func() (string, error)) {
		start := time.Now()
		detail, err := fn()
		st := step{Name: name, Detail: detail, Elapsed: time.Since(start)}
		st.Millis = float64(st.Elapsed) / float64(time.Millisecond)
		if err != nil {
			st.Err = err.Error()
		}
		steps = append(steps, st)
	}

	timed("server version", func() (string, error) {
		return irodsclient.ServerVersion(ctx, s)
	})

	timed("stat collection", func() (string, error) {
		e, err := irodsclient.Stat(ctx, s, target)
		if err != nil {
			return "", err
		}
		return string(e.Type), nil
	})

	var children []*irodsclient.Entry
	timed("list collection", func() (string, error) {
		var err error
		children, err = irodsclient.List(ctx, s, target)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d entries", len(children)), nil
	})

	timed("list ACLs", func() (string, error) {
		acls, err := irodsclient.ListACLs(ctx, s, target)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d entries", len(acls)), nil
	})

	timed("list AVUs", func() (string, error) {
		avus, err := irodsclient.ListAVUs(ctx, s, target)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d AVUs", len(avus)), nil
	})

	timed("list user groups", func() (string, error) {
		groups, err := irodsclient.ListUserGroups(ctx, s, cfg.IRODS.User, cfg.IRODS.Zone)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d groups", len(groups)), nil
	})

	// The bulk shapes. These are the ones that decide whether reads can move off catalog
	// SQL: /path-info and /permissions-gatherer accept up to max_paths_in_request paths,
	// and each becomes a round trip here where SQL answered the whole set at once.
	sample := children
	if len(sample) > bulk {
		sample = sample[:bulk]
	}

	if len(sample) > 0 {
		timed(fmt.Sprintf("stat %d paths (parallel %d)", len(sample), parallel), func() (string, error) {
			return perPath(len(sample)), forEach(ctx, sample, parallel, func(p string) error {
				_, err := irodsclient.Stat(ctx, s, p)
				return err
			})
		})

		timed(fmt.Sprintf("list ACLs for %d paths (parallel %d)", len(sample), parallel), func() (string, error) {
			return perPath(len(sample)), forEach(ctx, sample, parallel, func(p string) error {
				_, err := irodsclient.ListACLs(ctx, s, p)
				return err
			})
		})
	}

	timed("stat a path that does not exist", func() (string, error) {
		e, err := irodsclient.Stat(ctx, s, target+"/does-not-exist-"+fmt.Sprint(time.Now().UnixNano()))
		if err != nil {
			return "", err
		}
		return string(e.Type), nil
	})

	return steps, nil
}

func perPath(n int) string { return fmt.Sprintf("%d paths", n) }

// forEach runs fn over every entry, at most parallel at a time. Sequential round trips are
// the worst case for these shapes, so the concurrency is worth measuring: it is the only
// lever available if a per-path operation has to stay on the iRODS protocol.
func forEach(ctx context.Context, entries []*irodsclient.Entry, parallel int, fn func(string) error) error {
	if parallel < 1 {
		parallel = 1
	}

	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for _, e := range entries {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := fn(path); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(e.Path)
	}
	wg.Wait()
	return firstErr
}

func report(steps []step, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(steps)
	}

	width := 0
	for _, s := range steps {
		if len(s.Name) > width {
			width = len(s.Name)
		}
	}

	var failed int
	for _, s := range steps {
		status := s.Detail
		if s.Err != "" {
			status = "FAILED: " + s.Err
			failed++
		}
		fmt.Printf("%-*s  %9.1fms  %s\n", width, s.Name, s.Millis, status)
	}

	slowest := append([]step(nil), steps...)
	sort.Slice(slowest, func(i, j int) bool { return slowest[i].Elapsed > slowest[j].Elapsed })
	if len(slowest) > 0 {
		fmt.Printf("\nslowest: %s (%.1fms)\n", slowest[0].Name, slowest[0].Millis)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d operations failed", failed, len(steps))
	}
	return nil
}
