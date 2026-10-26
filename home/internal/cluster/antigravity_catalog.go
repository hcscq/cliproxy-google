package cluster

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AntigravityCatalogRecord stores the latest upstream model catalog fetched for
// one Antigravity credential. The Home leader writes it; every replica reads it,
// so all replicas route with the same model list.
type AntigravityCatalogRecord struct {
	AuthID      string    `gorm:"column:auth_id;primaryKey;size:255"`
	Raw         string    `gorm:"column:raw;type:text;not null;default:''"`
	FetchedAt   time.Time `gorm:"column:fetched_at"`
	AttemptedAt time.Time `gorm:"column:attempted_at"`
	Status      string    `gorm:"column:status;not null;default:''"`
	Error       string    `gorm:"column:error;type:text;not null;default:''"`
	HTTPStatus  int       `gorm:"column:http_status;not null;default:0"`
	ModelCount  int       `gorm:"column:model_count;not null;default:0"`
	Added       string    `gorm:"column:added;type:text;not null;default:''"`
	Removed     string    `gorm:"column:removed;type:text;not null;default:''"`
	Trigger     string    `gorm:"column:trigger_reason;not null;default:''"`
	FetchedBy   string    `gorm:"column:fetched_by;not null;default:''"`
}

// TableName returns the database table name.
func (AntigravityCatalogRecord) TableName() string { return "antigravity_catalogs" }

func ensureAntigravityCatalogTable(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	tx := db.WithContext(contextOrBackground(ctx))
	if tx.Migrator().HasTable(&AntigravityCatalogRecord{}) {
		return nil
	}
	return tx.Migrator().CreateTable(&AntigravityCatalogRecord{})
}

// UpsertAntigravityCatalog stores the catalog row for one credential.
func (r *Repository) UpsertAntigravityCatalog(ctx context.Context, rec AntigravityCatalogRecord) error {
	if r == nil || r.db == nil {
		return errors.New("repository unavailable")
	}
	rec.AuthID = strings.TrimSpace(rec.AuthID)
	if rec.AuthID == "" {
		return errors.New("auth id is required")
	}
	if err := ensureAntigravityCatalogTable(ctx, r.db); err != nil {
		return err
	}
	return r.db.WithContext(contextOrBackground(ctx)).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "auth_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"raw", "fetched_at", "attempted_at", "status", "error", "http_status", "model_count", "added", "removed", "trigger_reason", "fetched_by"}),
	}).Create(&rec).Error
}

// ListAntigravityCatalogs returns all stored catalog rows keyed by auth ID.
func (r *Repository) ListAntigravityCatalogs(ctx context.Context) (map[string]AntigravityCatalogRecord, error) {
	out := map[string]AntigravityCatalogRecord{}
	if r == nil || r.db == nil {
		return out, nil
	}
	if err := ensureAntigravityCatalogTable(ctx, r.db); err != nil {
		return out, err
	}
	var rows []AntigravityCatalogRecord
	if err := r.db.WithContext(contextOrBackground(ctx)).Find(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		out[row.AuthID] = row
	}
	return out, nil
}

// ListAntigravityCatalogsSince returns rows attempted or fetched after since.
func (r *Repository) ListAntigravityCatalogsSince(ctx context.Context, since time.Time) (map[string]AntigravityCatalogRecord, error) {
	out := map[string]AntigravityCatalogRecord{}
	if r == nil || r.db == nil {
		return out, nil
	}
	var rows []AntigravityCatalogRecord
	if err := r.db.WithContext(contextOrBackground(ctx)).Where("attempted_at > ? OR fetched_at > ?", since, since).Find(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		out[row.AuthID] = row
	}
	return out, nil
}

func catalogRecordToRow(rec AntigravityCatalogRecord) home.AntigravityCatalogRow {
	return home.AntigravityCatalogRow{
		AuthID: rec.AuthID, Raw: []byte(rec.Raw), FetchedAt: rec.FetchedAt, AttemptedAt: rec.AttemptedAt,
		Status: rec.Status, Error: rec.Error, HTTPStatus: rec.HTTPStatus, ModelCount: rec.ModelCount,
		Added: rec.Added, Removed: rec.Removed, Trigger: rec.Trigger, FetchedBy: rec.FetchedBy,
	}
}

func catalogRowToRecord(row home.AntigravityCatalogRow) AntigravityCatalogRecord {
	return AntigravityCatalogRecord{
		AuthID: row.AuthID, Raw: string(row.Raw), FetchedAt: row.FetchedAt, AttemptedAt: row.AttemptedAt,
		Status: row.Status, Error: row.Error, HTTPStatus: row.HTTPStatus, ModelCount: row.ModelCount,
		Added: row.Added, Removed: row.Removed, Trigger: row.Trigger, FetchedBy: row.FetchedBy,
	}
}

// LoadAntigravityCatalogs implements home.AntigravityCatalogStore.
func (a *RuntimeAdapter) LoadAntigravityCatalogs(ctx context.Context) (map[string]home.AntigravityCatalogRow, error) {
	out := map[string]home.AntigravityCatalogRow{}
	if a == nil || a.repo == nil {
		return out, errors.New("cluster repository unavailable")
	}
	recs, err := a.repo.ListAntigravityCatalogs(ctx)
	if err != nil {
		return out, err
	}
	for id, rec := range recs {
		out[id] = catalogRecordToRow(rec)
	}
	return out, nil
}

// LoadAntigravityCatalogsSince implements home.AntigravityCatalogIncrementalStore.
func (a *RuntimeAdapter) LoadAntigravityCatalogsSince(ctx context.Context, since time.Time) (map[string]home.AntigravityCatalogRow, error) {
	out := map[string]home.AntigravityCatalogRow{}
	if a == nil || a.repo == nil {
		return out, errors.New("cluster repository unavailable")
	}
	recs, err := a.repo.ListAntigravityCatalogsSince(ctx, since)
	if err != nil {
		return out, err
	}
	for id, rec := range recs {
		out[id] = catalogRecordToRow(rec)
	}
	return out, nil
}

// SaveAntigravityCatalog implements home.AntigravityCatalogStore.
func (a *RuntimeAdapter) SaveAntigravityCatalog(ctx context.Context, row home.AntigravityCatalogRow) error {
	if a == nil || a.repo == nil {
		return errors.New("cluster repository unavailable")
	}
	return a.repo.UpsertAntigravityCatalog(ctx, catalogRowToRecord(row))
}

// AntigravityCatalogContains reports, for each given auth with a fetched
// catalog, whether its raw catalog mentions model. Auths without a fetched
// catalog are absent from the result (unknown).
func (r *Repository) AntigravityCatalogContains(ctx context.Context, ids []string, model string) (map[string]bool, error) {
	out := map[string]bool{}
	if r == nil || r.db == nil || len(ids) == 0 || strings.TrimSpace(model) == "" {
		return out, nil
	}
	if err := ensureAntigravityCatalogTable(ctx, r.db); err != nil {
		return out, err
	}
	var rows []struct {
		AuthID string
		Has    bool
	}
	err := r.db.WithContext(contextOrBackground(ctx)).Model(&AntigravityCatalogRecord{}).
		Select("auth_id, strpos(raw, ?) > 0 AS has", model).
		Where("auth_id IN ? AND model_count > 0", ids).Scan(&rows).Error
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		out[row.AuthID] = row.Has
	}
	return out, nil
}
