package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/handlers"
	"github.com/cyverse-de/data-info/internal/panics"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sirupsen/logrus"
)

// errorCodeKey carries a failed request's error_code from the error handler to the request
// logger, so the request's single log line can name it.
const errorCodeKey = "log.error_code"

// probePaths are the kubelet's probes. A passing probe is logged at all only at debug, or
// the log would be mostly probes.
var probePaths = map[string]bool{
	"/healthz": true,
	"/readyz":  true,
}

// newLogger builds the service's logger: one JSON object per line, at the given level.
func newLogger(level string) (*logrus.Entry, error) {
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", level, err)
	}

	logger := logrus.New()
	logger.SetLevel(lvl)
	logger.SetFormatter(&logrus.JSONFormatter{TimestampFormat: time.RFC3339Nano})

	return logger.WithFields(serviceFields()), nil
}

func serviceFields() logrus.Fields {
	return logrus.Fields{
		"project": "discoenv",
		"service": handlers.ServiceName,
	}
}

// configureLibraryLogger makes logrus's global logger, which go-irodsclient writes to, log
// JSON lines carrying the service's fields. At the service's default level of info it logs
// only warnings and worse, because go-irodsclient logs routine events for every pooled
// connection at info.
func configureLibraryLogger(logger *logrus.Logger, serviceLevel logrus.Level) {
	level := serviceLevel
	if level == logrus.InfoLevel {
		level = logrus.WarnLevel
	}

	logger.SetLevel(level)
	logger.SetFormatter(&logrus.JSONFormatter{TimestampFormat: time.RFC3339Nano})
	logger.AddHook(fieldsHook(serviceFields()))
}

// fieldsHook adds fixed fields to every entry that does not already set them.
type fieldsHook logrus.Fields

func (fieldsHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h fieldsHook) Fire(entry *logrus.Entry) error {
	for key, value := range h {
		if _, ok := entry.Data[key]; !ok {
			entry.Data[key] = value
		}
	}
	return nil
}

// requestLogger writes one line per request, without bodies.
func requestLogger(log *logrus.Entry) echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogMethod:   true,
		LogURI:      true,
		LogStatus:   true,
		LogLatency:  true,
		LogError:    true,
		LogRemoteIP: true,
		HandleError: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			fields := logrus.Fields{
				"method":     v.Method,
				"route":      c.Path(),
				"uri":        v.URI,
				"status":     v.Status,
				"latency_ms": float64(v.Latency.Microseconds()) / 1000,
				"remote":     v.RemoteIP,
				// The caller's identity is a query parameter, not a header or a token.
				"user": c.QueryParam("user"),
			}
			if code, ok := c.Get(errorCodeKey).(apierror.Code); ok {
				fields["error_code"] = string(code)
			}
			// The error handler writes nothing for a client that hung up, so the status
			// recorded is not one anybody received.
			if errors.Is(v.Error, context.Canceled) {
				fields["client_disconnected"] = true
			}

			entry := log.WithFields(fields)
			if probePaths[c.Path()] && v.Status < http.StatusBadRequest {
				entry.Debug("request")
				return nil
			}
			entry.Info("request")
			return nil
		},
	})
}

// recoverer turns a handler panic into an error for the error handler, logging the stack
// through the service's logger rather than echo's plain-text one.
func recoverer(log *logrus.Entry) echo.MiddlewareFunc {
	return middleware.RecoverWithConfig(middleware.RecoverConfig{
		LogErrorFunc: func(c echo.Context, err error, stack []byte) error {
			log.WithError(err).WithFields(logrus.Fields{
				"method": c.Request().Method,
				"route":  c.Path(),
				"stack":  string(stack),
			}).Error("handler panicked; this is a bug in data-info")
			return err
		},
	})
}

// errorLogger records the error_code for the request line, and logs the underlying cause.
//
// The level follows the code rather than the HTTP status, because the status table answers
// 500 for caller errors such as a missing path; see apierror.Code.IsServerFault.
func errorLogger(log *logrus.Entry) func(echo.Context, *apierror.Error, error) {
	return func(c echo.Context, apiErr *apierror.Error, err error) {
		c.Set(errorCodeKey, apiErr.Code)

		entry := log.WithFields(logrus.Fields{
			"method":     c.Request().Method,
			"route":      c.Path(),
			"uri":        c.Request().RequestURI,
			"error_code": string(apiErr.Code),
			"status":     apiErr.HTTPStatus(),
		}).WithError(err)

		// A panic recovered off the handler's goroutine, in a lookup or an iRODS call.
		var panicked *panics.Error
		if errors.As(err, &panicked) {
			entry = entry.WithField("stack", string(panicked.Stack))
		}

		if apiErr.Code.IsServerFault() {
			entry.Error("request failed")
			return
		}
		entry.Debug("request refused")
	}
}
