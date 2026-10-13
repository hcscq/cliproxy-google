package cluster

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// PostgreSQL connection pooler (pgbouncer, transaction mode).
//
// HOME_PG_POOLER_ADDR=host:port routes the main pool through the pooler while
// the small leader-lease pool stays on a direct connection, so leadership and
// fencing never depend on the pooler. Home uses only transaction-scoped
// advisory locks and no LISTEN/NOTIFY, so transaction pooling is safe;
// pgx's prepared statements need pgbouncer >= 1.21 with max_prepared_statements.
// If the pooler cannot be reached at startup Home falls back to the direct DSN.

func pgPoolerAddr() string { return strings.TrimSpace(os.Getenv("HOME_PG_POOLER_ADDR")) }

// dsnWithHostPort returns dsn pointed at hostPort (URL or key=value form).
func dsnWithHostPort(dsn, hostPort string) (string, bool) {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil || host == "" || port == "" {
		return dsn, false
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return dsn, false
		}
		u.Host = net.JoinHostPort(host, port)
		return u.String(), true
	}
	// key=value: later keys win in pgx's parser.
	return strings.TrimSpace(dsn) + " host=" + host + " port=" + port, true
}

// PoolStatus is shown on the panel (Nodes / Settings).
type PoolStatus struct {
	PoolerAddr  string       `json:"pooler_addr,omitempty"`
	ViaPooler   bool         `json:"via_pooler"`
	PoolerError string       `json:"pooler_error,omitempty"`
	Main        sql.DBStats  `json:"main"`
	Lease       *sql.DBStats `json:"lease,omitempty"`
	Bulk        *sql.DBStats `json:"bulk,omitempty"`
	PingMS      float64      `json:"ping_ms"`
	PingError   string       `json:"ping_error,omitempty"`
	CheckedAt   time.Time    `json:"checked_at"`
}

var poolerState sync.Map // *gorm.DB (main) -> PoolStatus (addr / via / error)

func recordPoolerState(db *gorm.DB, st PoolStatus) { poolerState.Store(db, st) }

// DatabasePoolStatus reports pool usage and whether the pooler is in use.
func DatabasePoolStatus(ctx context.Context, db *gorm.DB) PoolStatus {
	var st PoolStatus
	if v, ok := poolerState.Load(db); ok {
		st = v.(PoolStatus)
	}
	st.CheckedAt = time.Now().UTC()
	if db == nil {
		return st
	}
	if sqlDB, err := db.DB(); err == nil {
		st.Main = sqlDB.Stats()
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		start := time.Now()
		if err := sqlDB.PingContext(pctx); err != nil {
			st.PingError = err.Error()
		}
		st.PingMS = float64(time.Since(start).Microseconds()) / 1000
		cancel()
	}
	if lease := leaseDBFor(db); lease != nil {
		if leaseSQL, err := lease.DB(); err == nil {
			s := leaseSQL.Stats()
			st.Lease = &s
		}
	}
	if b := bulkDBFor(db); b != db {
		if bulkSQL, err := b.DB(); err == nil {
			s := bulkSQL.Stats()
			st.Bulk = &s
		}
	}
	return st
}

// PoolStatus reports this Home's PostgreSQL pools.
func (r *Repository) PoolStatus(ctx context.Context) PoolStatus {
	db, err := r.database()
	if err != nil {
		return PoolStatus{PingError: err.Error()}
	}
	return DatabasePoolStatus(ctx, db)
}

