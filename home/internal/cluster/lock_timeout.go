package cluster

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// leaseLockTimeoutDuration bounds how long heartbeat, ownership, and
// subscription-liveness transactions wait on row locks. A stalled DB (for
// example a checkpoint or host fsfreeze) must surface as a quick, retryable
// error instead of piling up blocked transactions.
const leaseLockTimeoutDuration = 5 * time.Second

// setLocalLockTimeoutFn is the lock_timeout hook used by transactions. Tests
// may wrap it to observe that a path armed the timeout.
var setLocalLockTimeoutFn = setLocalLockTimeout

// setLocalLockTimeout applies SET LOCAL lock_timeout inside a transaction on
// PostgreSQL. It is a no-op on other dialects.
func setLocalLockTimeout(tx *gorm.DB) error {
	if tx == nil || tx.Dialector == nil || tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SET LOCAL lock_timeout = '" + leaseLockTimeoutDuration.String() + "'").Error
}

type sqlStateError interface {
	SQLState() string
}

// IsRetriableLockError reports whether err is a lock wait, deadlock, or
// serialization failure that a short retry can survive. Context cancellation
// is not retriable: the caller is already going away.
func IsRetriableLockError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var state sqlStateError
	if errors.As(err, &state) && state != nil {
		switch state.SQLState() {
		case "55P03", "40P01", "40001":
			return true
		}
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "lock timeout") ||
		strings.Contains(message, "could not obtain lock") ||
		strings.Contains(message, "deadlock detected")
}
