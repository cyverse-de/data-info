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

	"github.com/cyverse-de/data-info/internal/config"
	"github.com/cyverse-de/data-info/internal/handlers"
	"github.com/cyverse-de/go-mod/logging"
	"github.com/cyverse-de/go-mod/otelutils"
	"github.com/sirupsen/logrus"
)

// version is injected at build time with -ldflags "-X main.version=...". It is reported by
// GET / and by --version.
var version = "dev"

// shutdownGrace bounds how long in-flight requests have to finish after SIGTERM. It has to
// stay under the deployment's terminationGracePeriodSeconds, which is Kubernetes' default
// of 30s unless a manifest says otherwise, or the kubelet sends SIGKILL at the same moment
// this deadline expires and shutdown never completes.
const shutdownGrace = 20 * time.Second

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

	srv := newHTTPServer(cfg, buildServer(cfg, version, log, networkDeps(cfg)))

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	log.Info("stopped")
	return nil
}
