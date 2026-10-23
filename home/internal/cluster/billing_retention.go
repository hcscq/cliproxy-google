package cluster

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultBillingChargeRetentionDays = 7
	billingChargePruneBatch           = 5000
	billingChargePruneMaxBatches      = 400
	billingChargeRetentionLockKey     = 7402117302
)

// BillingChargeRetentionDays normalizes the configured billing_charge
// retention: 0 = default (7 days), negative = keep forever (returns 0).
func BillingChargeRetentionDays(configured int) int {
	switch {
	case configured < 0:
		return 0
	case configured == 0:
		return defaultBillingChargeRetentionDays
	default:
		return configured
	}
}

// PruneBillingCharges deletes billing_charge rows older than retentionDays in
// small batches (short row locks, no table lock). On Postgres only one Home
// replica runs it at a time (advisory lock).
func (r *Repository) PruneBillingCharges(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	db, err := r.database()
	if err != nil {
		return 0, err
	}
	ctx = contextOrBackground(ctx)
	isPG := db.Dialector != nil && db.Dialector.Name() == "postgres"
	if isPG {
		sqlDB, errDB := db.DB()
		if errDB != nil {
			return 0, errDB
		}
		conn, errConn := sqlDB.Conn(ctx)
		if errConn != nil {
			return 0, errConn
		}
		defer conn.Close()
		var got bool
		if errLock := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, billingChargeRetentionLockKey).Scan(&got); errLock != nil {
			return 0, errLock
		}
		if !got {
			return 0, nil
		}
		defer func() {
			_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, billingChargeRetentionLockKey)
		}()
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	query := `DELETE FROM "billing_charge" WHERE id IN (SELECT id FROM "billing_charge" WHERE created_at < ? LIMIT ?)`
	if isPG {
		query = `DELETE FROM "billing_charge" WHERE ctid = ANY(ARRAY(SELECT ctid FROM "billing_charge" WHERE created_at < ? LIMIT ?))`
	}
	var total int64
	for i := 0; i < billingChargePruneMaxBatches; i++ {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		res := db.WithContext(ctx).Exec(query, cutoff, billingChargePruneBatch)
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < billingChargePruneBatch {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return total, nil
}

// StartBillingChargeRetention prunes billing_charge at startup and hourly.
func (r *Repository) StartBillingChargeRetention(ctx context.Context, retentionDays func() int) {
	go func() {
		run := func() {
			mctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			days := retentionDays()
			if n, err := r.PruneBillingCharges(mctx, days); err != nil && ctx.Err() == nil {
				log.WithError(err).Warn("billing charge retention failed")
			} else if n > 0 {
				log.WithFields(log.Fields{"deleted_rows": n, "retention_days": days}).Info("billing charge retention applied")
			}
		}
		run()
		t := time.NewTicker(usageMaintenanceTicker)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				run()
			}
		}
	}()
}
