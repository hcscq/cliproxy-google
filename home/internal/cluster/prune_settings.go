package cluster

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	defaultPruneBatchSize        = 1000
	defaultPruneBillingBatchSize = 25
	defaultPruneBatchSleep       = 200 * time.Millisecond
	defaultPruneMaxDuration      = 30 * time.Second
	defaultPruneMaxRows          = int64(50000)
	defaultPruneStatementTimeout = 5 * time.Second
	defaultPruneStartupJitterMin = 5 * time.Second
	defaultPruneStartupJitterMax = 45 * time.Second
	appLogRetentionLockKey       = int64(7402117303)
	affinityRetentionLockKey     = int64(7402117304)
	envPruneBatchSize            = "CPA_PRUNE_BATCH_SIZE"
	envPruneBillingBatchSize     = "CPA_PRUNE_BILLING_BATCH_SIZE"
	envPruneBatchSleep           = "CPA_PRUNE_BATCH_SLEEP"
	envPruneMaxDuration          = "CPA_PRUNE_MAX_DURATION"
	envPruneMaxRows              = "CPA_PRUNE_MAX_ROWS"
	envPruneStatementTimeout     = "CPA_PRUNE_STATEMENT_TIMEOUT"
	envPruneStartupJitter        = "CPA_PRUNE_STARTUP_JITTER"
)

// pruneSettings controls log, billing_charge, and request_affinity cleanup
// batching. Defaults are the production values; environment variables override
// them (0 duration/rows means "no cap" except the caller's context).
type pruneSettings struct {
	BatchSize        int
	BillingBatchSize int
	BatchSleep       time.Duration
	MaxDuration      time.Duration
	MaxRows          int64
	StatementTimeout time.Duration
	StartupJitterMin time.Duration
	StartupJitterMax time.Duration
}

func defaultPruneSettings() pruneSettings {
	return pruneSettings{
		BatchSize:        defaultPruneBatchSize,
		BillingBatchSize: defaultPruneBillingBatchSize,
		BatchSleep:       defaultPruneBatchSleep,
		MaxDuration:      defaultPruneMaxDuration,
		MaxRows:          defaultPruneMaxRows,
		StatementTimeout: defaultPruneStatementTimeout,
		StartupJitterMin: defaultPruneStartupJitterMin,
		StartupJitterMax: defaultPruneStartupJitterMax,
	}
}

var loadPruneSettings = loadPruneSettingsFromEnv

func loadPruneSettingsFromEnv() pruneSettings {
	s := defaultPruneSettings()
	if n, ok := pruneEnvInt(envPruneBatchSize); ok {
		if n < 1 {
			n = 1
		}
		if n > 10000 {
			n = 10000
		}
		s.BatchSize = n
	}
	if d, ok := pruneEnvDuration(envPruneBatchSleep); ok {
		if d < 0 {
			d = 0
		}
		s.BatchSleep = d
	}
	if d, ok := pruneEnvDuration(envPruneMaxDuration); ok {
		if d < 0 {
			d = 0
		}
		s.MaxDuration = d
	}
	if n, ok := pruneEnvInt(envPruneMaxRows); ok {
		if n < 0 {
			n = 0
		}
		s.MaxRows = int64(n)
	}
	if n, ok := pruneEnvInt(envPruneBillingBatchSize); ok {
		if n < 1 {
			n = 1
		}
		if n > 10000 {
			n = 10000
		}
		s.BillingBatchSize = n
	}
	if d, ok := pruneEnvDuration(envPruneStatementTimeout); ok {
		if d < 0 {
			d = 0
		}
		s.StatementTimeout = d
	}
	if d, ok := pruneEnvDuration(envPruneStartupJitter); ok {
		if d <= 0 {
			s.StartupJitterMin = 0
			s.StartupJitterMax = 0
		} else {
			s.StartupJitterMin = 0
			s.StartupJitterMax = d
		}
	}
	return s
}

// billingBatchSize returns the billing_charge batch size, falling back to
// BatchSize when the billing-specific value is unset (tests).
func (s pruneSettings) billingBatchSize() int {
	if s.BillingBatchSize > 0 {
		return s.BillingBatchSize
	}
	if s.BatchSize > 0 {
		return s.BatchSize
	}
	return defaultPruneBillingBatchSize
}

func pruneEnvInt(name string) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}

func pruneEnvDuration(name string) (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, false
	}
	return d, true
}

// SetMaintenanceLeader registers the cluster-master check used by hourly
// cleanup. When fn is set and returns false, PruneAppLogs, PruneBillingCharges,
// and PurgeAffinity skip the run so only one Home executes them. A nil fn
// (tests, or before the coordinator is wired) does not skip.
func (r *Repository) SetMaintenanceLeader(fn func() bool) {
	if r == nil {
		return
	}
	r.maintenanceLeaderMu.Lock()
	r.maintenanceLeader = fn
	r.maintenanceLeaderMu.Unlock()
}

func (r *Repository) isMaintenanceLeader() bool {
	if r == nil {
		return true
	}
	r.maintenanceLeaderMu.RLock()
	fn := r.maintenanceLeader
	r.maintenanceLeaderMu.RUnlock()
	if fn == nil {
		return true
	}
	return fn()
}

func sleepPruneStartupJitter(ctx context.Context) {
	s := loadPruneSettings()
	d := pickPruneStartupJitter(s)
	if d <= 0 {
		return
	}
	log.WithField("jitter", d.String()).Info("usage/billing prune startup jitter")
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
}

func pickPruneStartupJitter(s pruneSettings) time.Duration {
	min := s.StartupJitterMin
	max := s.StartupJitterMax
	if max <= 0 || max < min {
		return 0
	}
	if min < 0 {
		min = 0
	}
	span := max - min
	if span <= 0 {
		return min
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(span)+1))
	if err != nil {
		return min
	}
	return min + time.Duration(n.Int64())
}

func pruneBudgetExhausted(start time.Time, total int64, s pruneSettings) bool {
	if s.MaxRows > 0 && total >= s.MaxRows {
		return true
	}
	if s.MaxDuration > 0 && time.Since(start) >= s.MaxDuration {
		return true
	}
	return false
}

const maintenanceLeaderWait = 20 * time.Second

// waitForMaintenanceLeader returns true once this Home is the cluster master,
// or if no leader func is registered. After a rolling start, Initialize may
// still be in progress when the startup jitter ends, so we wait briefly
// instead of skipping the first hourly run for a full hour.
func waitForMaintenanceLeader(ctx context.Context, r *Repository) bool {
	if r.isMaintenanceLeader() {
		return true
	}
	timer := time.NewTimer(maintenanceLeaderWait)
	defer timer.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return r.isMaintenanceLeader()
		case <-tick.C:
			if r.isMaintenanceLeader() {
				return true
			}
		}
	}
}

// withPruneAdvisoryLock holds a session-level Postgres advisory lock for the
// duration of fn. If the lock is already taken, fn is not called and held is
// false. Non-Postgres dialects call fn directly.
func withPruneAdvisoryLock(ctx context.Context, db *gorm.DB, key int64, fn func() error) (held bool, err error) {
	if db == nil || db.Dialector == nil || db.Dialector.Name() != "postgres" {
		return true, fn()
	}
	sqlDB, errDB := db.DB()
	if errDB != nil {
		return false, errDB
	}
	conn, errConn := sqlDB.Conn(ctx)
	if errConn != nil {
		return false, errConn
	}
	defer conn.Close()
	var got bool
	if errLock := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); errLock != nil {
		return false, errLock
	}
	if !got {
		return false, nil
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
	}()
	return true, fn()
}

// execPruneBatch runs a single DELETE in its own short transaction. On
// Postgres it also sets a statement-level timeout (SET LOCAL) so a slow
// statement is cancelled and rolled back instead of holding the database.
// A zero timeout skips SET LOCAL.
func execPruneBatch(ctx context.Context, db *gorm.DB, timeout time.Duration, query string, args ...any) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("prune: nil db")
	}
	var n int64
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if timeout > 0 && tx.Dialector != nil && tx.Dialector.Name() == "postgres" {
			ms := timeout.Milliseconds()
			if ms < 1 {
				ms = 1
			}
			if err := tx.Exec(fmt.Sprintf("SET LOCAL statement_timeout = %d", ms)).Error; err != nil {
				return err
			}
		}
		res := tx.Exec(query, args...)
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

func isStatementTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	var st sqlStateError
	if errors.As(err, &st) && st.SQLState() == "57014" {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "statement timeout") ||
		strings.Contains(msg, "canceling statement due to statement timeout")
}
