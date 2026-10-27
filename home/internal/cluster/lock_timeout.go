package cluster

import "gorm.io/gorm"

// leaseLockTimeout bounds how long heartbeat/ownership transactions wait on
// row locks. A stalled DB (e.g. a host fsfreeze) must surface as a quick,
// retryable error instead of piling up blocked transactions.
const leaseLockTimeout = "5s"

// setLocalLockTimeout applies SET LOCAL lock_timeout inside a transaction on
// PostgreSQL. It is a no-op on other dialects.
func setLocalLockTimeout(tx *gorm.DB) error {
	if tx == nil || tx.Dialector == nil || tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SET LOCAL lock_timeout = '" + leaseLockTimeout + "'").Error
}
