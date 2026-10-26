package cluster

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// CredentialImportJob is a server-side credential import run. Inputs (tokens)
// are never stored; only progress and per-item results (masked token hints).
type CredentialImportJob struct {
	ID        string    `gorm:"column:id;primaryKey" json:"id"`
	Source    string    `gorm:"column:source" json:"source"`
	Status    string    `gorm:"column:status;not null" json:"status"` // running | done | failed | interrupted
	Total     int       `gorm:"column:total;not null;default:0" json:"total"`
	Done      int       `gorm:"column:done;not null;default:0" json:"done"`
	Created   int       `gorm:"column:created_n;not null;default:0" json:"created"`
	Updated   int       `gorm:"column:updated_n;not null;default:0" json:"updated"`
	Failed    int       `gorm:"column:failed_n;not null;default:0" json:"failed"`
	Reserve   bool      `gorm:"column:reserve;not null;default:false" json:"reserve"`
	BatchID   string    `gorm:"column:batch_id" json:"batch_id"`
	HealthID  string    `gorm:"column:health_batch_id" json:"health_batch_id,omitempty"`
	Error     string    `gorm:"column:error" json:"error,omitempty"`
	Results   string    `gorm:"column:results;type:text" json:"-"`
	StartedAt time.Time `gorm:"column:started_at;not null" json:"started_at"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null" json:"updated_at"`
}

// TableName returns the database table name.
func (CredentialImportJob) TableName() string { return "credential_import_job" }

func ensureImportJobTable(ctx context.Context, db *gorm.DB) error {
	tx := db.WithContext(contextOrBackground(ctx))
	if tx.Dialector.Name() == "postgres" {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS "credential_import_job" ("id" text PRIMARY KEY, "source" text, "status" text NOT NULL, "total" integer NOT NULL DEFAULT 0, "done" integer NOT NULL DEFAULT 0, "created_n" integer NOT NULL DEFAULT 0, "updated_n" integer NOT NULL DEFAULT 0, "failed_n" integer NOT NULL DEFAULT 0, "reserve" boolean NOT NULL DEFAULT false, "batch_id" text, "health_batch_id" text, "error" text, "results" text, "started_at" timestamptz NOT NULL DEFAULT now(), "updated_at" timestamptz NOT NULL DEFAULT now())`,
			`CREATE INDEX IF NOT EXISTS "idx_credential_import_job_started" ON "credential_import_job" ("started_at")`,
		} {
			if err := tx.Exec(stmt).Error; err != nil {
				return err
			}
		}
		return nil
	}
	return tx.AutoMigrate(&CredentialImportJob{})
}

// SaveImportJob upserts a job row.
func (r *Repository) SaveImportJob(ctx context.Context, job *CredentialImportJob) error {
	db, err := r.database()
	if err != nil {
		return err
	}
	if err := ensureImportJobTable(ctx, db); err != nil {
		return err
	}
	return db.WithContext(contextOrBackground(ctx)).Save(job).Error
}

// GetImportJob returns one job (nil when missing).
func (r *Repository) GetImportJob(ctx context.Context, id string) (*CredentialImportJob, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	if err := ensureImportJobTable(ctx, db); err != nil {
		return nil, err
	}
	var job CredentialImportJob
	res := db.WithContext(contextOrBackground(ctx)).Where("id = ?", id).Limit(1).Find(&job)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	return &job, nil
}

// ListImportJobs returns the most recent jobs (without results) and prunes rows older than 30 days.
func (r *Repository) ListImportJobs(ctx context.Context, limit int) ([]CredentialImportJob, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	if err := ensureImportJobTable(ctx, db); err != nil {
		return nil, err
	}
	tx := db.WithContext(contextOrBackground(ctx))
	_ = tx.Where("started_at < ?", time.Now().Add(-30*24*time.Hour)).Delete(&CredentialImportJob{}).Error
	var jobs []CredentialImportJob
	err = tx.Omit("results").Order("started_at DESC").Limit(limit).Find(&jobs).Error
	return jobs, err
}
