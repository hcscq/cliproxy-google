package logging

import (
	"os"
	"strings"
	"sync/atomic"
)

// stdoutOnly forces all application logs to stdout and disables local request/error
// log files. It is enabled by the server entrypoint for container (k3s) deployments
// so that the root filesystem can be mounted read-only. Set CLIPROXY_FILE_LOGS=true
// to restore upstream file logging behaviour.
var stdoutOnly atomic.Bool

// SetStdoutOnly toggles stdout-only logging.
func SetStdoutOnly(enabled bool) { stdoutOnly.Store(enabled) }

// StdoutOnly reports whether stdout-only logging is active.
func StdoutOnly() bool { return stdoutOnly.Load() }

// StdoutOnlyFromEnv returns true unless CLIPROXY_FILE_LOGS is set to a truthy value.
func StdoutOnlyFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CLIPROXY_FILE_LOGS"))) {
	case "1", "true", "yes", "on":
		return false
	}
	return true
}
