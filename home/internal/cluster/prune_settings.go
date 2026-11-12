package cluster

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/driver/postgres"
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
// false. Non-Postgres dialects call fn with db directly.
//
// Session locks cannot go through pgbouncer transaction pooling: unlock would
// often land on a different server connection and leave the lock on an idle
// backend. pg_try_advisory_xact_lock is also a poor fit because cleanup
// commits each batch in its own short transaction (plus inter-batch sleep),
// so an xact lock would drop between batches. Instead the lock, every DELETE
// batch, and unlock all run on one connection taken from a direct (non-pooler)
// pool. Unlock is issued on that same session; if unlock fails the connection
// is discarded so a locked session cannot return to the pool.
func withPruneAdvisoryLock(ctx context.Context, db *gorm.DB, key int64, fn func(connDB *gorm.DB) error) (held bool, err error) {
	if !pruneLockSessionRequired(db) {
		return true, fn(db)
	}
	sess, errOpen := newPruneLockSession(ctx, db)
	if errOpen != nil {
		return false, errOpen
	}
	defer func() {
		if closeErr := sess.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	got, errLock := sess.TryLock(ctx, key)
	if errLock != nil {
		return false, errLock
	}
	if !got {
		return false, nil
	}
	return true, fn(sess.DB())
}

// newPruneLockSession opens a prune lock session. Tests may replace it.
var newPruneLockSession = openDirectPruneLockSession

// pruneLockSessionRequired reports whether this dialect must take a session
// advisory lock. Postgres always does; tests may force it on SQLite.
var pruneLockSessionRequired = func(db *gorm.DB) bool {
	return db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres"
}

// pruneLockSession is one Postgres (or test) session that can hold an
// advisory lock and run the batched DELETEs.
type pruneLockSession interface {
	TryLock(ctx context.Context, key int64) (bool, error)
	Unlock(ctx context.Context, key int64) error
	DB() *gorm.DB
	Close() error
}

// pruneDirectDB prefers the direct bulk-write pool, then the lease pool, then
// db itself. Session advisory locks must not use the pgbouncer transaction
// pool (the main pool when HOME_PG_POOLER_ADDR is set).
//
// Lookups are by the underlying *sql.DB, not the *gorm.DB pointer: callers
// often pass db.WithContext(...) which is a new session object that would
// miss the pool maps keyed on the Open() instance and silently fall back
// to pgbouncer.
func pruneDirectDB(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	if direct := directPoolFor(db, &bulkDBs); direct != nil {
		return direct
	}
	if direct := directPoolFor(db, &leaseDBs); direct != nil {
		return direct
	}
	return db
}

func directPoolFor(db *gorm.DB, pools *sync.Map) *gorm.DB {
	if v, ok := pools.Load(db); ok && v != nil {
		if direct, ok := v.(*gorm.DB); ok && direct != nil {
			return direct
		}
	}
	sqlDB := gormSQLDB(db)
	if sqlDB == nil {
		return nil
	}
	var found *gorm.DB
	pools.Range(func(k, v any) bool {
		main, _ := k.(*gorm.DB)
		direct, _ := v.(*gorm.DB)
		if main == nil || direct == nil {
			return true
		}
		if gormSQLDB(main) != sqlDB {
			return true
		}
		found = direct
		return false
	})
	return found
}

func gormSQLDB(db *gorm.DB) (sqlDB *sql.DB) {
	if db == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			sqlDB = nil
		}
	}()
	d, err := db.DB()
	if err != nil {
		return nil
	}
	return d
}

type directPruneLockSession struct {
	conn   *sql.Conn
	gormDB *gorm.DB
	held   int64 // 0 = none
}

func openDirectPruneLockSession(ctx context.Context, db *gorm.DB) (pruneLockSession, error) {
	src := pruneDirectDB(db)
	if src == nil {
		return nil, fmt.Errorf("prune: nil db")
	}
	sqlDB, errDB := src.DB()
	if errDB != nil {
		return nil, errDB
	}
	conn, errConn := sqlDB.Conn(ctx)
	if errConn != nil {
		return nil, errConn
	}
	cfg := &gorm.Config{}
	if src.Config != nil {
		cfg.Logger = src.Config.Logger
		cfg.NowFunc = src.Config.NowFunc
	}
	pinned, errOpen := gorm.Open(postgres.New(postgres.Config{Conn: conn}), cfg)
	if errOpen != nil {
		_ = conn.Close()
		return nil, errOpen
	}
	return &directPruneLockSession{conn: conn, gormDB: pinned}, nil
}

func (s *directPruneLockSession) DB() *gorm.DB {
	if s == nil {
		return nil
	}
	return s.gormDB
}

func (s *directPruneLockSession) TryLock(ctx context.Context, key int64) (bool, error) {
	var got bool
	if err := s.conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
		return false, err
	}
	if got {
		s.held = key
	}
	return got, nil
}

func (s *directPruneLockSession) Unlock(ctx context.Context, key int64) error {
	_, err := s.conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, key)
	if err == nil && s.held == key {
		s.held = 0
	}
	return err
}

func (s *directPruneLockSession) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	var unlockErr error
	if s.held != 0 {
		unlockErr = s.Unlock(context.Background(), s.held)
	}
	if unlockErr != nil {
		// Do not return a still-locked session to the Go pool.
		_ = s.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	closeErr := s.conn.Close()
	s.conn = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
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
