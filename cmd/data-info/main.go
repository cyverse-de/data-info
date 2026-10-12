// Command data-info serves the DE's HTTP API for the iRODS data store.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cyverse-de/data-info/internal/amqp"
	"github.com/cyverse-de/data-info/internal/clients/asynctasks"
	"github.com/cyverse-de/data-info/internal/clients/metadata"
	"github.com/cyverse-de/data-info/internal/clients/notifications"
	"github.com/cyverse-de/data-info/internal/config"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/irodsclient"
	"github.com/cyverse-de/data-info/internal/worker"
)

// version is what GET / and --version report.
const version = "3.0.2"

// serverGrace bounds how long in-flight requests have to finish, and drainGrace how long the
// background jobs then have to stop and report. With the preStop sleep, their sum has to stay
// under the deployment's terminationGracePeriodSeconds; see k8s/data-info.yml.
const (
	serverGrace = 30 * time.Second
	drainGrace  = 75 * time.Second
)

// asyncTasksTimeout bounds one call to the async-tasks service. It is short so that several
// retries of a task's final status fit inside drainGrace.
const asyncTasksTimeout = 5 * time.Second

// notificationsTimeout bounds one call to the notification agent.
const notificationsTimeout = 10 * time.Second

// metadataTimeout bounds one call to the metadata service. It sits inside a request rather
// than after one, so it has to stay well under the request timeout.
const metadataTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "data-info: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "", "path to the YAML configuration file")
		dotEnvPath  = flag.String("dotenv-path", "", "path to a dotenv file")
		envPrefix   = flag.String("env-prefix", config.DefaultEnvPrefix, "environment variable prefix")
		logLevel    = flag.String("log-level", "info", "logging level")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	log, err := newLogger(*logLevel)
	if err != nil {
		return err
	}

	cfg, err := config.Load(config.Settings{
		ConfigPath: *configPath,
		DotEnvPath: *dotEnvPath,
		EnvPrefix:  *envPrefix,
	})
	if err != nil {
		return fmt.Errorf("configuration is not usable: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := irodsclient.NewPool(irodsPoolConfig(cfg))
	if err != nil {
		return fmt.Errorf("building the iRODS client: %w", err)
	}
	defer pool.Close()

	store, err := icat.Open(icatConfig(cfg))
	if err != nil {
		return fmt.Errorf("connecting to the iRODS catalog: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.WithError(err).Error("closing the catalog connection")
		}
	}()

	tasks, err := asynctasks.New(cfg.Services.AsyncTasks, asyncTasksTimeout)
	if err != nil {
		return fmt.Errorf("building the async-tasks client: %w", err)
	}

	runner := worker.NewRunner(tasks, log, worker.InstanceID())

	notifier, err := notifications.New(cfg.Services.NotificationAgent, notificationsTimeout)
	if err != nil {
		return fmt.Errorf("building the notifications client: %w", err)
	}

	publisher, err := amqp.New(amqpConfig(cfg), log)
	if err != nil {
		return fmt.Errorf("building the AMQP publisher: %w", err)
	}
	defer publisher.Close()

	metadataClient, err := metadata.New(cfg.Services.Metadata, metadataTimeout)
	if err != nil {
		return fmt.Errorf("building the metadata client: %w", err)
	}

	deps := networkDeps(pool, store)
	deps.Metadata = metadataClient
	deps.Tasks = tasks
	deps.Worker = runner
	deps.Notifier = notifier
	deps.Publisher = publisher

	srv := newHTTPServer(cfg, buildServer(cfg, version, log, deps))

	errs := make(chan error, 1)
	go func() {
		log.WithField("port", cfg.Port).Info("listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		if err != nil {
			return fmt.Errorf("serving: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Info("shutting down")
	}

	serverCtx, cancelServer := context.WithTimeout(context.Background(), serverGrace)
	defer cancelServer()

	// Just log the error if the shutdown fails, so that active jobs can still be drained
	// before the process exits.
	if err := srv.Shutdown(serverCtx); err != nil {
		log.WithError(err).Error("the listener did not close cleanly; draining jobs anyway")
	}

	// Cancel running async tasks so that they're marked as failed in the database, which
	// releases their paths. After the listener, because a request in flight may still start
	// one. See docs/deferred-fixes.md.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), drainGrace)
	defer cancelDrain()

	if err := runner.Shutdown(drainCtx); err != nil {
		log.WithError(err).Errorf("could not drain the background jobs before exiting; "+
			"their tasks stay unfinished until the %s stall timeout completes them", asynctasks.StallTimeout)
	}

	log.Info("stopped")
	return nil
}
