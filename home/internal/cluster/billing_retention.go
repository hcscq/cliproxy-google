package cluster

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	defaultBillingChargeRetentionDays = 7
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
// small batches (short row locks, no table lock). Only the cluster master
// runs it; a second Home skips. On Postgres only one replica can hold the
// advisory lock at a time. Each batch runs in its own short transaction with
// a statement_timeout; a timeout stops this run and leaves leftovers for the
// next hour. Batch size (CPA_PRUNE_BILLING_BATCH_SIZE), inter-batch sleep,
// per-run row/duration caps, and statement timeout come from pruneSettings.
func (r *Repository) PruneBillingCharges(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	if !r.isMaintenanceLeader() {
		log.Info("billing charge retention skipped; not cluster master")
		return 0, nil
	}
	db, err := r.database()
	if err != nil {
		return 0, err
	}
	ctx = contextOrBackground(ctx)
	s := loadPruneSettings()
	batchSize := s.billingBatchSize()
	// Primary-key subquery (no ORDER BY): EXPLAIN shows a seq scan finds the
	// first N expired rows in ~100ms. ORDER BY created_at would sort the
	// matching set without a standalone created_at index; the 37s cost was
	// index/WAL maintenance of 1000 deletes on a bloated heap, not the scan.
	query := `DELETE FROM "billing_charge" WHERE id IN (SELECT id FROM "billing_charge" WHERE created_at < ? LIMIT ?)`
	var total int64
	held, errLock := withPruneAdvisoryLock(ctx, db, billingChargeRetentionLockKey, func(connDB *gorm.DB) error {
		cutoff := time.Now().AddDate(0, 0, -retentionDays)
		start := time.Now()
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if total > 0 && pruneBudgetExhausted(start, total, s) {
				log.WithFields(log.Fields{"deleted_rows": total, "max_rows": s.MaxRows, "max_duration": s.MaxDuration.String()}).
					Info("billing charge retention paused; leftover will retry next hour")
				return nil
			}
			n, errDel := execPruneBatch(ctx, connDB, s.StatementTimeout, query, cutoff, batchSize)
			if errDel != nil {
				if isStatementTimeoutErr(errDel) {
					log.WithError(errDel).WithFields(log.Fields{"deleted_rows": total, "batch_size": batchSize, "statement_timeout": s.StatementTimeout.String()}).
						Info("billing charge retention paused after statement timeout; leftover will retry next hour")
					return nil
				}
				return errDel
			}
			total += n
			if n < int64(batchSize) {
				return nil
			}
			if s.BatchSleep > 0 {
				timer := time.NewTimer(s.BatchSleep)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
	})
	if errLock != nil {
		return total, errLock
	}
	if !held {
		log.Info("billing charge retention skipped; advisory lock held by another Home")
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
		sleepPruneStartupJitter(ctx)
		if ctx.Err() != nil {
			return
		}
		_ = waitForMaintenanceLeader(ctx, r)
		if ctx.Err() != nil {
			return
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
