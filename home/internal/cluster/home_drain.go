package cluster

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Graceful Home drain.
//
// On SIGTERM a Home records itself as draining; the assignment rebalance then
// treats it as gone, so its proxies are reassigned and move (they hold
// requests while switching instead of failing them). The Home waits until no
// proxy is connected to it (bounded) before it stops serving.

// HomeDrainRecord marks a Home as draining until Until.
type HomeDrainRecord struct {
	IP    string    `gorm:"column:ip;primaryKey"`
	Port  int       `gorm:"column:port;primaryKey"`
	Until time.Time `gorm:"column:until;not null"`
}

// TableName returns the database table name.
func (HomeDrainRecord) TableName() string { return "home_drain" }

func ensureHomeDrainTable(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	tx := db.WithContext(contextOrBackground(ctx))
	if tx.Dialector.Name() == "postgres" {
		// IF NOT EXISTS: Homes starting together must not race.
		return tx.Exec(`CREATE TABLE IF NOT EXISTS "home_drain" ("ip" text NOT NULL, "port" bigint NOT NULL, "until" timestamptz NOT NULL, PRIMARY KEY ("ip","port"))`).Error
	}
	if tx.Migrator().HasTable(&HomeDrainRecord{}) {
		return nil
	}
	return tx.Migrator().CreateTable(&HomeDrainRecord{})
}

// BeginHomeDrain marks ip:port as draining for ttl.
func (r *Repository) BeginHomeDrain(ctx context.Context, ip string, port int, ttl time.Duration) error {
	db, err := r.database()
	if err != nil {
		return err
	}
	now, err := DatabaseNow(ctx, db)
	if err != nil {
		return err
	}
	rec := HomeDrainRecord{IP: ip, Port: port, Until: now.Add(ttl)}
	return db.WithContext(contextOrBackground(ctx)).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "ip"}, {Name: "port"}},
		DoUpdates: clause.AssignmentColumns([]string{"until"}),
	}).Create(&rec).Error
}

// drainingHomes returns the keys of Homes draining at now.
func drainingHomes(tx *gorm.DB, now time.Time) (map[string]bool, error) {
	var rows []HomeDrainRecord
	if err := tx.Where("until > ?", now).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[homeKey(r.IP, r.Port)] = true
	}
	return out, nil
}

// ConnectedProxyCount counts proxies with a live membership on ip:port.
func (r *Repository) ConnectedProxyCount(ctx context.Context, ip string, port int, within time.Duration) (int64, error) {
	db, err := r.database()
	if err != nil {
		return 0, err
	}
	now, err := DatabaseNow(ctx, db)
	if err != nil {
		return 0, err
	}
	var n int64
	err = db.WithContext(contextOrBackground(ctx)).Model(&CPANodeMembershipRecord{}).
		Where("home_ip = ? AND home_port = ? AND state = ? AND last_seen_at >= ?", ip, port, "active", now.Add(-within)).
		Count(&n).Error
	return n, err
}

// withoutDrainingHomes drops draining Homes from a live list so proxies stop
// treating them as candidates (all kept if every Home is draining). Errors keep
// the list unchanged.
func (r *Repository) withoutDrainingHomes(ctx context.Context, nodes []ClusterNodeRecord, now time.Time) []ClusterNodeRecord {
	db, err := r.database()
	if err != nil || len(nodes) == 0 {
		return nodes
	}
	draining, err := drainingHomes(db.WithContext(contextOrBackground(ctx)), now)
	if err != nil || len(draining) == 0 {
		return nodes
	}
	out := make([]ClusterNodeRecord, 0, len(nodes))
	for _, n := range nodes {
		if !draining[homeKey(n.IP, n.Port)] {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nodes
	}
	return out
}

// ClearHomeDrain removes ip:port's drain mark. A Home calls it at startup: a
// restart at the same address (container restart in the same pod) must not
// keep looking like it is draining.
func (r *Repository) ClearHomeDrain(ctx context.Context, ip string, port int) error {
	db, err := r.database()
	if err != nil {
		return err
	}
	return db.WithContext(contextOrBackground(ctx)).Where("ip = ? AND port = ?", ip, port).Delete(&HomeDrainRecord{}).Error
}
