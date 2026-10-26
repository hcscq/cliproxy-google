package cluster

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CredentialImportVerdict is the immutable first health-check verdict taken right
// after import. Later re-checks / quota refreshes update credential_health_check
// but never this row. Sticky marks a failed verdict the user has not cleared yet.
type CredentialImportVerdict struct {
	AuthUUID   string     `gorm:"column:auth_uuid;primaryKey" json:"-"`
	BatchID    string     `gorm:"column:batch_id" json:"batch_id,omitempty"`
	CheckedAt  time.Time  `gorm:"column:checked_at;not null" json:"checked_at"`
	Status     string     `gorm:"column:status;not null" json:"status"`
	Categories string     `gorm:"column:categories" json:"-"`
	Reasons    string     `gorm:"column:reasons" json:"-"`
	Tier       string     `gorm:"column:tier" json:"tier,omitempty"`
	Argon      *bool      `gorm:"column:argon" json:"argon,omitempty"`
	Sticky     bool       `gorm:"column:sticky;not null;default:false" json:"sticky"`
	ClearedAt  *time.Time `gorm:"column:cleared_at" json:"cleared_at,omitempty"`
	Note       string     `gorm:"column:note" json:"note,omitempty"`
}

// TableName returns the database table name.
func (CredentialImportVerdict) TableName() string { return "credential_import_verdict" }

// CategoryList splits stored categories.
func (v CredentialImportVerdict) CategoryList() []string { return splitNonEmpty(v.Categories, ",") }

// ReasonList splits stored reasons.
func (v CredentialImportVerdict) ReasonList() []string { return splitNonEmpty(v.Reasons, "\n") }

func ensureImportVerdictTable(ctx context.Context, db *gorm.DB) error {
	tx := db.WithContext(contextOrBackground(ctx))
	if tx.Dialector.Name() == "postgres" {
		return tx.Exec(`CREATE TABLE IF NOT EXISTS "credential_import_verdict" ("auth_uuid" text PRIMARY KEY, "batch_id" text, "checked_at" timestamptz NOT NULL, "status" text NOT NULL, "categories" text, "reasons" text, "tier" text, "argon" boolean, "sticky" boolean NOT NULL DEFAULT false, "cleared_at" timestamptz, "note" text)`).Error
	}
	return tx.AutoMigrate(&CredentialImportVerdict{})
}

// recordImportVerdict stores the first finished check of an imported credential; later calls are no-ops.
func recordImportVerdict(ctx context.Context, db *gorm.DB, rec CredentialHealthCheck) error {
	if rec.BatchID == "" || (rec.Status != "pass" && rec.Status != "fail") {
		return nil
	}
	if err := ensureImportVerdictTable(ctx, db); err != nil {
		return err
	}
	v := CredentialImportVerdict{AuthUUID: rec.AuthUUID, BatchID: rec.BatchID, CheckedAt: rec.CheckedAt.UTC(), Status: rec.Status,
		Categories: rec.Categories, Reasons: rec.Reasons, Tier: rec.Tier, Sticky: rec.Status == "fail"}
	tx := db.WithContext(contextOrBackground(ctx))
	var rows []struct{ Has bool }
	if err := tx.Model(&AntigravityCatalogRecord{}).Select(ArgonCatalogSQL + " AS has").Where("auth_id = ? AND model_count > 0", rec.AuthUUID).Limit(1).Scan(&rows).Error; err == nil && len(rows) > 0 {
		has := rows[0].Has
		v.Argon = &has
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&v).Error
}

// ListImportVerdicts returns auth -> import verdict.
func (r *Repository) ListImportVerdicts(ctx context.Context) (map[string]CredentialImportVerdict, error) {
	out := map[string]CredentialImportVerdict{}
	db, err := r.database()
	if err != nil {
		return out, err
	}
	if err := ensureImportVerdictTable(ctx, db); err != nil {
		return out, err
	}
	var rows []CredentialImportVerdict
	if err := db.WithContext(contextOrBackground(ctx)).Find(&rows).Error; err != nil {
		return out, err
	}
	for _, v := range rows {
		out[v.AuthUUID] = v
	}
	return out, nil
}

// ClearImportVerdictSticky clears the sticky flag (the verdict itself is kept).
func (r *Repository) ClearImportVerdictSticky(ctx context.Context, ids []string) (int64, error) {
	db, err := r.database()
	if err != nil {
		return 0, err
	}
	if err := ensureImportVerdictTable(ctx, db); err != nil {
		return 0, err
	}
	res := db.WithContext(contextOrBackground(ctx)).Model(&CredentialImportVerdict{}).Where("auth_uuid IN ? AND sticky", ids).
		Updates(map[string]any{"sticky": false, "cleared_at": time.Now().UTC()})
	return res.RowsAffected, res.Error
}
