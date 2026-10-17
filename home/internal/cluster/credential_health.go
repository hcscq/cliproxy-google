package cluster

import (
	"context"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Import health check (导入体检) results and import batch records live in their own
// tables, never in auth_json (no light runtime metadata persistence involved).

// CredentialHealthCheck is the latest health check of one credential.
type CredentialHealthCheck struct {
	AuthUUID   string     `gorm:"column:auth_uuid;primaryKey" json:"auth_id"`
	Status     string     `gorm:"column:status;not null" json:"status"` // pass | fail | running
	Categories string     `gorm:"column:categories" json:"-"`
	Reasons    string     `gorm:"column:reasons" json:"-"`
	Evidence   string     `gorm:"column:evidence" json:"evidence,omitempty"`
	Probe      string     `gorm:"column:probe" json:"-"`
	Tier       string     `gorm:"column:tier" json:"tier,omitempty"`
	Model      string     `gorm:"column:model" json:"model,omitempty"`
	CheckedAt  time.Time  `gorm:"column:checked_at;not null" json:"checked_at"`
	BatchID    string     `gorm:"column:batch_id" json:"batch_id,omitempty"`
	ImportedAt *time.Time `gorm:"column:imported_at" json:"imported_at,omitempty"`
}

// TableName returns the database table name.
func (CredentialHealthCheck) TableName() string { return "credential_health_check" }

// CategoryList splits the stored categories.
func (c CredentialHealthCheck) CategoryList() []string { return splitNonEmpty(c.Categories, ",") }

// ReasonList splits the stored reasons.
func (c CredentialHealthCheck) ReasonList() []string { return splitNonEmpty(c.Reasons, "\n") }

func splitNonEmpty(s, sep string) []string {
	out := []string{}
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// CredentialImport records which import batch created a credential.
type CredentialImport struct {
	AuthUUID   string    `gorm:"column:auth_uuid;primaryKey"`
	BatchID    string    `gorm:"column:batch_id;not null;index"`
	ImportedAt time.Time `gorm:"column:imported_at;not null"`
	Source     string    `gorm:"column:source"`
}

// TableName returns the database table name.
func (CredentialImport) TableName() string { return "credential_import" }

func ensureCredentialHealthTables(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	tx := db.WithContext(contextOrBackground(ctx))
	if tx.Dialector.Name() == "postgres" {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS "credential_health_check" ("auth_uuid" text PRIMARY KEY, "status" text NOT NULL, "categories" text, "reasons" text, "evidence" text, "probe" text, "tier" text, "model" text, "checked_at" timestamptz NOT NULL DEFAULT now(), "batch_id" text, "imported_at" timestamptz)`,
			`CREATE TABLE IF NOT EXISTS "credential_import" ("auth_uuid" text PRIMARY KEY, "batch_id" text NOT NULL, "imported_at" timestamptz NOT NULL DEFAULT now(), "source" text)`,
			`CREATE INDEX IF NOT EXISTS "idx_credential_import_batch" ON "credential_import" ("batch_id")`,
		} {
			if err := tx.Exec(stmt).Error; err != nil {
				return err
			}
		}
		return nil
	}
	return tx.AutoMigrate(&CredentialHealthCheck{}, &CredentialImport{})
}

// RecordCredentialImports stores the import batch of newly created credentials.
func (r *Repository) RecordCredentialImports(ctx context.Context, ids []string, batchID string, at time.Time, source string) error {
	db, err := r.database()
	if err != nil {
		return err
	}
	rows := make([]CredentialImport, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			rows = append(rows, CredentialImport{AuthUUID: id, BatchID: batchID, ImportedAt: at.UTC(), Source: source})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return db.WithContext(contextOrBackground(ctx)).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// ListCredentialImports returns credential -> import record.
func (r *Repository) ListCredentialImports(ctx context.Context) (map[string]CredentialImport, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var rows []CredentialImport
	if err := db.WithContext(contextOrBackground(ctx)).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]CredentialImport, len(rows))
	for _, x := range rows {
		out[x.AuthUUID] = x
	}
	return out, nil
}

// SaveHealthCheck upserts the latest check of a credential.
func (r *Repository) SaveHealthCheck(ctx context.Context, rec CredentialHealthCheck) error {
	db, err := r.database()
	if err != nil {
		return err
	}
	return db.WithContext(contextOrBackground(ctx)).Save(&rec).Error
}

// ListHealthChecks returns credential -> latest check.
func (r *Repository) ListHealthChecks(ctx context.Context) (map[string]CredentialHealthCheck, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var rows []CredentialHealthCheck
	if err := db.WithContext(contextOrBackground(ctx)).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]CredentialHealthCheck, len(rows))
	for _, x := range rows {
		out[x.AuthUUID] = x
	}
	return out, nil
}

// HealthInputs reads quota freshness and catalog state of one credential; fresh counts
// quota windows observed at or after since.
func (r *Repository) HealthInputs(ctx context.Context, id string, since time.Time) (home.HealthInputs, int, error) {
	var in home.HealthInputs
	db, err := r.database()
	if err != nil {
		return in, 0, err
	}
	tx := db.WithContext(contextOrBackground(ctx))
	var q struct {
		N     int64
		Fresh int64
		Min   *float64
	}
	qs := `SELECT COUNT(*) AS n, COUNT(*) FILTER (WHERE observed_at >= ?) AS fresh, MIN(remaining_ratio)::float8 AS min
		FROM quota_window WHERE credential_id = ? AND remaining_ratio IS NOT NULL AND COALESCE(is_unlimited,false) = false`
	if tx.Dialector.Name() != "postgres" {
		qs = `SELECT COUNT(*) AS n, SUM(CASE WHEN observed_at >= ? THEN 1 ELSE 0 END) AS fresh, MIN(remaining_ratio) AS min
		FROM quota_window WHERE credential_id = ? AND remaining_ratio IS NOT NULL AND COALESCE(is_unlimited,false) = false`
	}
	if err := tx.Raw(qs, since.UTC(), id).Scan(&q).Error; err != nil {
		return in, 0, err
	}
	in.QuotaWindows = int(q.N)
	if q.Min != nil {
		in.QuotaMin = *q.Min
	}
	var c struct {
		Status     string
		ModelCount int
	}
	if err := tx.Raw(`SELECT COALESCE(status,'') AS status, COALESCE(model_count,0) AS model_count FROM antigravity_catalogs WHERE auth_id = ?`, id).Scan(&c).Error; err == nil {
		in.CatalogOK = c.Status == "ok"
		in.CatalogCount = c.ModelCount
	}
	return in, int(q.Fresh), nil
}

// CredentialEmails returns credential -> email for the given ids (export).
func (r *Repository) CredentialEmails(ctx context.Context) (map[string]string, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var rows []struct {
		UUID  string
		Email string
		Seq   int64
	}
	if err := db.WithContext(contextOrBackground(ctx)).Raw(`SELECT uuid, COALESCE(auth_json->'metadata'->>'email','') AS email, COALESCE(seq,0) AS seq FROM auth WHERE deleted_at IS NULL`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, x := range rows {
		out[x.UUID] = x.Email
	}
	return out, nil
}

// ListHealthFailedIDs returns credentials whose latest check failed (block option).
func (r *Repository) ListHealthFailedIDs(ctx context.Context) (map[string]struct{}, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := db.WithContext(contextOrBackground(ctx)).Model(&CredentialHealthCheck{}).Where("status = ?", "fail").Pluck("auth_uuid", &ids).Error; err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out, nil
}

// ListHealthFailedIDs exposes the lookup to the Home runtime.
func (a *RuntimeAdapter) ListHealthFailedIDs(ctx context.Context) (map[string]struct{}, error) {
	r, err := a.reserveRepo()
	if err != nil {
		return nil, err
	}
	return r.ListHealthFailedIDs(ctx)
}
