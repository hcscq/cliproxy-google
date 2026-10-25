package cluster

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/errorsamples"

	"gorm.io/gorm"
)

// ErrorSampleRecord is one captured failed-request context.
type ErrorSampleRecord = errorsamples.Record

func ensureErrorSampleTable(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	tx := db.WithContext(contextOrBackground(ctx))
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS "error_sample" ("id" bigserial PRIMARY KEY, "type" text NOT NULL, "created_at" timestamptz NOT NULL DEFAULT now(), "status" bigint, "requested_model" text, "request_id" text, "client_ip" text, "home_ip" text, "size" bigint, "data" bytea)`).Error; err != nil {
			return err
		}
		return tx.Exec(`CREATE INDEX IF NOT EXISTS "idx_error_sample_type_id" ON "error_sample" ("type", "id")`).Error
	}
	return tx.AutoMigrate(&ErrorSampleRecord{})
}

// InsertErrorSample stores a sample and prunes the type to keep rows and the
// table to maxTypes types (least recently seen types are evicted). Safe with
// several Home replicas: pruning is expressed purely in SQL.
func (r *Repository) InsertErrorSample(ctx context.Context, rec *ErrorSampleRecord, raw []byte, keep, maxTypes int) error {
	db, err := r.database()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	rec.Data = buf.Bytes()
	rec.Size = int64(len(raw))
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	return db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(rec).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM error_sample WHERE type = ? AND id NOT IN (SELECT id FROM error_sample WHERE type = ? ORDER BY id DESC LIMIT ?)`, rec.Type, rec.Type, keep).Error; err != nil {
			return err
		}
		limit := "LIMIT -1 "
		if tx.Dialector.Name() == "postgres" {
			limit = ""
		}
		return tx.Exec(`DELETE FROM error_sample WHERE type IN (SELECT type FROM error_sample GROUP BY type ORDER BY MAX(id) DESC `+limit+`OFFSET ?)`, maxTypes).Error
	})
}

// ErrorSampleType summarizes one error type.
type ErrorSampleType struct {
	Type     string    `json:"type"`
	Count    int64     `json:"count"`
	Bytes    int64     `json:"bytes"`
	LastSeen time.Time `json:"last_seen"`
	Status   int       `json:"status"`
}

// ListErrorSampleTypes returns the stored types, most recent first.
func (r *Repository) ListErrorSampleTypes(ctx context.Context) ([]ErrorSampleType, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	tx := db.WithContext(contextOrBackground(ctx))
	var agg []struct {
		Type   string
		Count  int64
		Bytes  int64
		LastID int64
		Status int
	}
	if err := tx.Raw(`SELECT type, COUNT(*) AS count, COALESCE(SUM(LENGTH(data)),0) AS bytes, MAX(id) AS last_id, MAX(status) AS status FROM error_sample GROUP BY type ORDER BY MAX(id) DESC`).Scan(&agg).Error; err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(agg))
	for _, a := range agg {
		ids = append(ids, a.LastID)
	}
	seen := map[int64]time.Time{}
	if len(ids) > 0 {
		var rows []ErrorSampleRecord
		if err := tx.Select("id", "created_at").Where("id IN ?", ids).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			seen[r.ID] = r.CreatedAt
		}
	}
	out := make([]ErrorSampleType, 0, len(agg))
	for _, a := range agg {
		out = append(out, ErrorSampleType{Type: a.Type, Count: a.Count, Bytes: a.Bytes, Status: a.Status, LastSeen: seen[a.LastID]})
	}
	return out, nil
}

// ListErrorSamples returns the stored samples of one type, newest first.
func (r *Repository) ListErrorSamples(ctx context.Context, typ string) ([]ErrorSampleRecord, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var rows []ErrorSampleRecord
	if err := db.WithContext(contextOrBackground(ctx)).Where("type = ?", typ).Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// DeleteErrorSamples deletes one type ("" = all).
func (r *Repository) DeleteErrorSamples(ctx context.Context, typ string) (int64, error) {
	db, err := r.database()
	if err != nil {
		return 0, err
	}
	tx := db.WithContext(contextOrBackground(ctx))
	var res *gorm.DB
	if typ == "" {
		res = tx.Exec(`DELETE FROM error_sample`)
	} else {
		res = tx.Exec(`DELETE FROM error_sample WHERE type = ?`, typ)
	}
	return res.RowsAffected, res.Error
}

// DecodeErrorSample returns the uncompressed sample JSON.
func DecodeErrorSample(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(io.LimitReader(zr, 64<<20))
}
