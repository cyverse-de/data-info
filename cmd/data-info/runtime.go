package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// memoryLimitPercent is the share of the container's memory limit given to the Go runtime as
// its soft limit, leaving headroom for what the runtime does not count, such as the mapped
// binary and kernel memory charged to the container.
const memoryLimitPercent = 85

// cgroupLimitFiles hold the container's memory limit under cgroup v2 and v1.
var cgroupLimitFiles = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory/memory.limit_in_bytes",
}

// applyMemoryLimit sets the Go runtime's soft memory limit below the container's, which the
// runtime cannot see by itself: without one the heap may grow to twice what is live before a
// collection, and the kernel kills the container first. GOMEMLIMIT, when set, wins.
func applyMemoryLimit(log *logrus.Entry, readFile func(string) ([]byte, error)) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}

	for _, path := range cgroupLimitFiles {
		contents, err := readFile(path)
		if err != nil {
			continue
		}
		limit, ok := parseCgroupLimit(string(contents))
		if !ok {
			continue
		}
		soft := limit / 100 * memoryLimitPercent
		debug.SetMemoryLimit(soft)
		log.WithFields(logrus.Fields{"container_limit": limit, "soft_limit": soft}).
			Info("set the Go runtime's memory limit from the container's")
		return
	}

	log.Info("found no container memory limit, so the Go runtime has no soft limit; this is " +
		"expected outside a container, and otherwise means the cgroup files are not where they were looked for")
}

// parseCgroupLimit reads a cgroup memory limit, reporting false when there is none: cgroup v2
// writes "max", and v1 a figure near the largest int64.
func parseCgroupLimit(contents string) (int64, bool) {
	limit, err := strconv.ParseInt(strings.TrimSpace(contents), 10, 64)
	if err != nil || limit <= 0 || limit >= 1<<62 {
		return 0, false
	}
	return limit, true
}

// startProfiler serves net/http/pprof on its own listener, reached with kubectl port-forward,
// and returns its server, or nil when the port could not be bound. The configuration only
// accepts a loopback address, since the endpoints have no authentication.
func startProfiler(addr string, log *logrus.Entry) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// Bind before returning, so that a taken port is reported at startup rather than later.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.WithError(err).WithField("addr", addr).Error("could not serve pprof; this usually means " +
			"another process in the pod holds the port. The service runs on without profiling")
		return nil
	}

	// No WriteTimeout: the CPU profile and the trace stream for as many seconds as are asked for.
	srv := &http.Server{Addr: ln.Addr().String(), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.WithField("addr", srv.Addr).Info("serving pprof")
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.WithError(err).Error("the pprof listener stopped accepting connections; " +
				"profiles are unavailable until a restart")
		}
	}()
	return srv
}
