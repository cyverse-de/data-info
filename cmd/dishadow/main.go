// Command dishadow compares this service's responses against the Clojure service it
// replaces.
//
// It is the oracle the port is verified against: point it at both services, give it a
// catalog of requests, and it reports every difference. Run it from a workstation with two
// port-forwards, or in the cluster during a soak.
//
// Read-only cases need only the two services. Cases that change something need --scratch: a
// collection the harness may create and delete under, so each service gets its own copy of the
// fixture and the two never contend for the same object. Without it those cases are reported as
// skipped rather than run against a shared tree, because a difference produced by the harness
// itself is worse than no result.
//
// The scratch collection is checked before anything is created: it must be absolute, several
// levels deep, and name itself as scratch, and every path derived from it is re-checked. This
// writes to a zone other people share, so a mistyped flag should stop the run rather than
// delete somebody's data.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cyverse-de/data-info/internal/shadow"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "dishadow: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		reference = flag.String("reference", "http://localhost:8081", "base URL of the service being replaced")
		candidate = flag.String("candidate", "http://localhost:8082", "base URL of the service under test")
		catalogIn = flag.String("catalog", "test/shadow/catalog", "directory of case files")
		user      = flag.String("user", "", "iRODS user to run as")
		zone      = flag.String("zone", "", "iRODS zone")
		root      = flag.String("root", "", "collection the cases operate under")
		runID     = flag.String("run-id", "", "identifier canonicalised out of responses (defaults to a timestamp)")
		scratch   = flag.String("scratch", "", "collection write cases may create and delete under; without it they are skipped")
		keep      = flag.Bool("keep", false, "leave the run's fixtures in place instead of removing them")
	)
	flag.Parse()

	if *user == "" || *zone == "" {
		return fmt.Errorf("--user and --zone are required")
	}

	id := *runID
	if id == "" {
		id = time.Now().UTC().Format("20060102T150405")
	}

	catalog, err := shadow.LoadCatalog(*catalogIn)
	if err != nil {
		return err
	}
	if len(catalog.Cases()) == 0 {
		return fmt.Errorf("no cases found in %s", *catalogIn)
	}

	runner := shadow.NewRunner(*reference, *candidate, id)
	runner.User = *user
	runner.Vars = map[string]string{
		"User": *user,
		"Zone": *zone,
		"Root": defaulted(*root, "/"+*zone+"/home/"+*user),
	}

	if *scratch != "" {
		guard, err := shadow.NewScratchGuard(*scratch)
		if err != nil {
			return err
		}

		// Fixtures are built through the reference service, so a comparison never depends
		// on the service under test already being correct.
		runner.Reader = shadow.NewServiceClient(*reference)
		runner.Fixtures = shadow.NewFixtures(guard, id, runner.Reader)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	results := runner.Run(ctx, catalog)

	if runner.Fixtures != nil && !*keep {
		// Best effort, but reported: what a failed cleanup leaves behind is real data in a
		// shared zone, and someone has to know it is there.
		if err := runner.Fixtures.Remove(context.WithoutCancel(ctx), *user); err != nil {
			fmt.Fprintf(os.Stderr, "dishadow: could not remove this run's fixtures under %s: %v\n", *scratch, err)
		}
	}

	matched, err := shadow.Report(os.Stdout, results)
	if err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}
	if !matched {
		return fmt.Errorf("responses differ")
	}
	return nil
}

func defaulted(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
