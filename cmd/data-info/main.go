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
	"github.com/cyverse-de/data-info/internal/handlers"
	"github.com/cyverse-de/data-info/internal/icat"
	"github.com/cyverse-de/data-info/internal/irodsclient"
	"github.com/cyverse-de/data-info/internal/worker"
	"github.com/cyverse-de/go-mod/logging"
	"github.com/cyverse-de/go-mod/otelutils"
	"github.com/sirupsen/logrus"
)

// version is what GET / and --version report. It is a constant in the source rather than a
// linker flag: project.clj declares the Clojure service's the same way, and what that
// service actually reports is the defproject string, not anything the build stamps in.
// Bump it here when the port's version moves.
const version = "3.0.2-SNAPSHOT"

// serverGrace bounds how long in-flight requests have to finish, and drainGrace how long the
// background jobs then have to stop and report. Their sum has to stay under the deployment's
// terminationGracePeriodSeconds, or the kubelet sends SIGKILL while a job is still trying to
// release its paths -- which is the leak the drain exists to prevent.
//
// The manifest asks for 120 seconds and spends 5 of them in a preStop sleep, leaving 115.
// These use 90 of that, so a drain that runs right to its deadline still has room to log
// what it gave up on before the kubelet loses patience.
const (
	serverGrace = 30 * time.Second
	drainGrace  = 60 * time.Second
)

// asyncTasksTimeout bounds one call to the async-tasks service.
//
// It is short on purpose, despite the call that matters most being the one that records a
// task's final status and releases its paths. During shutdown the whole drain has to finish
// inside drainGrace, so an attempt that could outlast that window would waste it rather than
// use it: several quick tries beat one long one that never returns.
const asyncTasksTimeout = 5 * time.Second

// notificationsTimeout bounds one call to the notification agent. A notification is the last
// thing a job does and the work is already committed by then, so waiting long for one would
// only hold a job's goroutine open.
const notificationsTimeout = 10 * time.Second

// metadataTimeout bounds one call to the metadata service. It sits inside a request rather
// than after one, so it has to stay well under the request timeout.
const metadataTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet if configuration failed, so report to stderr too.
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

	logging.SetupLogging(*logLevel)
	log := logging.Log.WithFields(logrus.Fields{"service": handlers.ServiceName})

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

	// Not ctx: otelutils derives the shutdown flush timeout from whatever context it is
	// given, and ctx is already cancelled by the time the deferred shutdown runs on
	// SIGTERM. Handing it the signal context would drop every span from the final batch
	// window on every rollout.
	shutdownTracing := otelutils.TracerProviderFromEnv(context.Background(), handlers.ServiceName, func(e error) {
		log.WithError(e).Error("tracing failed; continuing without it")
	})
	defer shutdownTracing()

	// Set when jobs are still running at exit, so their connections are left alone. Closing
	// a pool out from under an in-flight job would make it fail against a closed handle
	// instead of reporting itself -- and its report is what releases its paths.
	skipClose := false

	pool, err := irodsclient.NewPool(irodsPoolConfig(cfg))
	if err != nil {
		return fmt.Errorf("building the iRODS client: %w", err)
	}
	defer func() {
		if !skipClose {
			pool.Close()
		}
	}()

	store, err := icat.Open(icatConfig(cfg))
	if err != nil {
		return fmt.Errorf("connecting to the iRODS catalog: %w", err)
	}
	defer func() {
		if skipClose {
			return
		}
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

	// Logged rather than returned. Shutdown reports a deadline overrun whenever a request
	// outlasts the grace -- which is exactly when the pod is busy and jobs are most likely
	// running -- and returning here would skip the drain below, locking their paths for
	// good over what is only a slow request.
	if err := srv.Shutdown(serverCtx); err != nil {
		log.WithError(err).Error("the listener did not close cleanly; draining jobs anyway")
	}

	// After the listener, not before: a request already in flight may still start a job,
	// and a runner that had stopped accepting them would refuse it.
	//
	// Its own budget, not what the server left over. Sharing one deadline would give the
	// runner whatever a slow request did not use, and a single terminal post can take
	// longer than that -- so the drain would time out precisely when it matters. The two
	// budgets together have to stay under the deployment's terminationGracePeriodSeconds.
	//
	// This is the whole reason the runner exists in this shape. Each job is cancelled,
	// returns, and is recorded as failed, which sets the task's end date and releases the
	// paths it held. Without it a rollout leaves those paths locked -- and nothing else
	// releases them, because the stall timeout this service registers does not complete the
	// task. See docs/deferred-fixes.md.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), drainGrace)
	defer cancelDrain()

	if err := runner.Shutdown(drainCtx); err != nil {
		// The jobs are still running against the pool and the catalog. Closing those now
		// would pull the connections out from under them, so they are left open and the
		// process exits with them -- which loses nothing, since it is exiting anyway, and
		// gives each job its best chance of reporting before the kubelet kills it.
		log.WithError(err).Error("could not drain the background jobs before exiting; " +
			"leaving the backend connections open so they can still report")
		skipClose = true
	}

	log.Info("stopped")
	return nil
}
