package cluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EgressBDRecord is one Bright Data ISP static IP. Binding information stays
// on the credential (auth_json metadata); this table is only the inventory.
// The Bright Data zone password is never stored here.
type EgressBDRecord struct {
	ID             string    `gorm:"column:id;primaryKey"`
	ExpectedIP     string    `gorm:"column:expected_ip;not null;uniqueIndex"`
	Enabled        bool      `gorm:"column:enabled;not null;default:true"`
	Draining       bool      `gorm:"column:draining;not null;default:false"`
	MaxCredentials int       `gorm:"column:max_credentials;not null;default:1"`
	ChainRef       string    `gorm:"column:chain_ref"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (EgressBDRecord) TableName() string { return "egress_bd" }

func migrateEgressBDTable(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database connection is nil")
	}
	// ADD-only: CREATE TABLE IF NOT EXISTS. Never alter or drop existing tables.
	switch db.Dialector.Name() {
	case "postgres":
		return db.Exec(`CREATE TABLE IF NOT EXISTS "egress_bd" (
			"id" text PRIMARY KEY,
			"expected_ip" text NOT NULL,
			"enabled" boolean NOT NULL DEFAULT true,
			"draining" boolean NOT NULL DEFAULT false,
			"max_credentials" integer NOT NULL DEFAULT 1,
			"chain_ref" text,
			"created_at" timestamptz NOT NULL DEFAULT now(),
			"updated_at" timestamptz NOT NULL DEFAULT now(),
			CONSTRAINT egress_bd_expected_ip_unique UNIQUE ("expected_ip")
		)`).Error
	default:
		return db.Exec(`CREATE TABLE IF NOT EXISTS "egress_bd" (
			"id" text PRIMARY KEY,
			"expected_ip" text NOT NULL,
			"enabled" integer NOT NULL DEFAULT 1,
			"draining" integer NOT NULL DEFAULT 0,
			"max_credentials" integer NOT NULL DEFAULT 1,
			"chain_ref" text,
			"created_at" datetime,
			"updated_at" datetime
		)`).Error
	}
}

func (r *Repository) ListEgressBD(ctx context.Context) ([]EgressBDRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("repository is not configured")
	}
	var rows []EgressBDRecord
	err := r.db.WithContext(ctx).Order("expected_ip").Find(&rows).Error
	return rows, err
}

func (r *Repository) UpsertEgressBD(ctx context.Context, row EgressBDRecord) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("repository is not configured")
	}
	row.ID = strings.TrimSpace(row.ID)
	row.ExpectedIP = strings.TrimSpace(row.ExpectedIP)
	row.ChainRef = strings.TrimSpace(row.ChainRef)
	if row.ID == "" || row.ExpectedIP == "" {
		return fmt.Errorf("id and expected_ip are required")
	}
	now := time.Now().UTC()
	row.UpdatedAt = now
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"expected_ip", "enabled", "draining", "max_credentials", "chain_ref", "updated_at",
		}),
	}).Create(&row).Error
}

func (r *Repository) DeleteEgressBD(ctx context.Context, id string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("repository is not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id is required")
	}
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&EgressBDRecord{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("bd egress %q not found", id)
	}
	return nil
}
