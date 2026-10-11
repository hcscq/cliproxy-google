// Package usageerr holds usage-persistence errors shared by the Home runtime
// and the RESP push handlers (which cannot import each other's packages).
package usageerr

import "errors"

// ErrUnavailable means a usage payload could not be stored durably right now
// (the Home is shutting down); the sender should resend it later.
var ErrUnavailable = errors.New("usage store unavailable")
