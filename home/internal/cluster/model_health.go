package cluster

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ModelHealthCheckRecord stores the latest manual/automatic probe result for a
// model (shared by all Home replicas). Details holds per-credential results as
// JSON.
type ModelHealthCheckRecord struct {
	Model      string    `gorm:"column:model;primaryKey;size:255"`
	Outcome    string    `gorm:"column:outcome;not null;default:''"`
	HTTPStatus int       `gorm:"column:http_status;not null;default:0"`
	Message    string    `gorm:"column:message;type:text;not null;default:''"`
	Details    string    `gorm:"column:details;type:text;not null;default:''"`
	CheckedAt  time.Time `gorm:"column:checked_at;not null"`
}

// TableName returns the database table name.
func (ModelHealthCheckRecord) TableName() string { return "model_health_checks" }

// ensureModelHealthTable creates model_health_checks when missing. Idempotent;
// PostgreSQL and SQLite.
func ensureModelHealthTable(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	tx := db.WithContext(contextOrBackground(ctx))
	if tx.Migrator().HasTable(&ModelHealthCheckRecord{}) {
		return nil
	}
	return tx.Migrator().CreateTable(&ModelHealthCheckRecord{})
}

// UpsertModelHealthCheck stores the latest probe result for a model.
func (r *Repository) UpsertModelHealthCheck(ctx context.Context, record ModelHealthCheckRecord) error {
	if r == nil || r.db == nil {
		return errors.New("repository unavailable")
	}
	record.Model = strings.TrimSpace(record.Model)
	if record.Model == "" {
		return errors.New("model is required")
	}
	if record.CheckedAt.IsZero() {
		record.CheckedAt = time.Now().UTC()
	}
	if err := ensureModelHealthTable(ctx, r.db); err != nil {
		return err
	}
	return r.db.WithContext(contextOrBackground(ctx)).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "model"}},
		DoUpdates: clause.AssignmentColumns([]string{"outcome", "http_status", "message", "details", "checked_at"}),
	}).Create(&record).Error
}

// ListModelHealthChecks returns all stored probe results keyed by model.
func (r *Repository) ListModelHealthChecks(ctx context.Context) (map[string]ModelHealthCheckRecord, error) {
	out := map[string]ModelHealthCheckRecord{}
	if r == nil || r.db == nil {
		return out, nil
	}
	if err := ensureModelHealthTable(ctx, r.db); err != nil {
		return out, err
	}
	var rows []ModelHealthCheckRecord
	if err := r.db.WithContext(contextOrBackground(ctx)).Find(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		out[row.Model] = row
	}
	return out, nil
}
