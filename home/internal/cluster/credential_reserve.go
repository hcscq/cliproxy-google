package cluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"gorm.io/gorm"
)

// Credential reserve (凭据储备) state lives in its own tables, never in auth_json, so
// it is not subject to light runtime metadata persistence. credential_reserve holds
// the current state; credential_reserve_event is the audit trail (reserve, unreserve,
// promote, ban with was_reserved) used to compare ban rates of reserved vs active.

// CredentialReserve is the current reserve state of one credential.
type CredentialReserve struct {
	AuthUUID      string     `gorm:"column:auth_uuid;primaryKey"`
	Reserved      bool       `gorm:"column:reserved;not null"`
	ReservedSince *time.Time `gorm:"column:reserved_since"`
	PromotedAt    *time.Time `gorm:"column:promoted_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;not null"`
}

// TableName returns the database table name.
func (CredentialReserve) TableName() string { return "credential_reserve" }

// CredentialReserveEvent is one audit row.
type CredentialReserveEvent struct {
	ID            int64      `gorm:"column:id;primaryKey;autoIncrement"`
	AuthUUID      string     `gorm:"column:auth_uuid;not null;index"`
	Kind          string     `gorm:"column:kind;not null"`
	At            time.Time  `gorm:"column:at;not null;index"`
	WasReserved   bool       `gorm:"column:was_reserved;not null"`
	ReservedSince *time.Time `gorm:"column:reserved_since"`
	Detail        string     `gorm:"column:detail"`
}

// TableName returns the database table name.
func (CredentialReserveEvent) TableName() string { return "credential_reserve_event" }

func ensureCredentialReserveTables(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	tx := db.WithContext(contextOrBackground(ctx))
	if tx.Dialector.Name() == "postgres" {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS "credential_reserve" ("auth_uuid" text PRIMARY KEY, "reserved" boolean NOT NULL DEFAULT false, "reserved_since" timestamptz, "promoted_at" timestamptz, "updated_at" timestamptz NOT NULL DEFAULT now())`,
			`CREATE TABLE IF NOT EXISTS "credential_reserve_event" ("id" bigserial PRIMARY KEY, "auth_uuid" text NOT NULL, "kind" text NOT NULL, "at" timestamptz NOT NULL DEFAULT now(), "was_reserved" boolean NOT NULL DEFAULT false, "reserved_since" timestamptz, "detail" text)`,
			`CREATE INDEX IF NOT EXISTS "idx_credential_reserve_event_auth" ON "credential_reserve_event" ("auth_uuid", "kind")`,
			`CREATE INDEX IF NOT EXISTS "idx_credential_reserve_event_at" ON "credential_reserve_event" ("kind", "at")`,
		} {
			if err := tx.Exec(stmt).Error; err != nil {
				return err
			}
		}
		return nil
	}
	return tx.AutoMigrate(&CredentialReserve{}, &CredentialReserveEvent{})
}

// ListReservedCredentials returns reserved, live, enabled credentials -> reserved_since.
func (r *Repository) ListReservedCredentials(ctx context.Context) (map[string]time.Time, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var rows []CredentialReserve
	if err := db.WithContext(contextOrBackground(ctx)).
		Where("reserved = ? AND auth_uuid IN (SELECT uuid FROM auth WHERE deleted_at IS NULL AND COALESCE(disabled,false) = false)", true).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(rows))
	for _, x := range rows {
		var t time.Time
		if x.ReservedSince != nil {
			t = *x.ReservedSince
		}
		out[x.AuthUUID] = t
	}
	return out, nil
}

// SetCredentialsReserved moves ids into (reserved=true) or out of reserve and audits each
// change with kind (reserve | unreserve | promote). Returns how many changed.
func (r *Repository) SetCredentialsReserved(ctx context.Context, ids []string, reserved bool, kind, detail string) (int, error) {
	db, err := r.database()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	changed := 0
	err = db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		for _, raw := range ids {
			id := strings.TrimSpace(raw)
			if id == "" {
				continue
			}
			var cur CredentialReserve
			res := tx.Where("auth_uuid = ?", id).Limit(1).Find(&cur)
			if res.Error != nil {
				return res.Error
			}
			exists := res.RowsAffected > 0
			if exists && cur.Reserved == reserved || !exists && !reserved {
				continue
			}
			ev := CredentialReserveEvent{AuthUUID: id, Kind: kind, At: now, WasReserved: exists && cur.Reserved, Detail: detail}
			next := CredentialReserve{AuthUUID: id, Reserved: reserved, UpdatedAt: now}
			if reserved {
				next.ReservedSince = &now
			} else {
				next.ReservedSince = cur.ReservedSince
				next.PromotedAt = &now
				ev.ReservedSince = cur.ReservedSince
			}
			if err := tx.Save(&next).Error; err != nil {
				return err
			}
			if err := tx.Create(&ev).Error; err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	return changed, err
}

// ListReserveCandidates returns reserved, live, enabled credentials with ordering inputs.
func (r *Repository) ListReserveCandidates(ctx context.Context) ([]home.ReserveCandidate, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var rows []home.ReserveCandidate
	q := `SELECT a.uuid AS id, COALESCE(a.seq,0) AS seq, a.created_at AS created_at,
		COALESCE(a.auth_json->'metadata'->>'subscription_tier','') AS tier,
		(COALESCE(a.status,'') = 'active' AND COALESCE(a.unavailable,false) = false AND COALESCE(a.auth_json->>'status_message','') = '') AS healthy,
		COALESCE(h.status,'') AS health
		FROM auth a JOIN credential_reserve r ON r.auth_uuid = a.uuid
		LEFT JOIN credential_health_check h ON h.auth_uuid = a.uuid
		WHERE r.reserved AND a.deleted_at IS NULL AND COALESCE(a.disabled,false) = false`
	if db.Dialector.Name() != "postgres" {
		q = `SELECT a.uuid AS id, COALESCE(a.seq,0) AS seq, a.created_at AS created_at, '' AS tier,
			(COALESCE(a.status,'') = 'active' AND COALESCE(a.unavailable,false) = false) AS healthy,
			COALESCE(h.status,'') AS health
			FROM auth a JOIN credential_reserve r ON r.auth_uuid = a.uuid
			LEFT JOIN credential_health_check h ON h.auth_uuid = a.uuid
			WHERE r.reserved AND a.deleted_at IS NULL AND COALESCE(a.disabled,false) = false`
	}
	if err := db.WithContext(contextOrBackground(ctx)).Raw(q).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountActiveCredentials counts available credentials: live, enabled (bans and
// egress/error pauses disable), not reserved, status not error, with a refresh token,
// and not failed by the import health check. countCooled=false also excludes
// credentials that are wholly quota-cooled right now.
func (r *Repository) CountActiveCredentials(ctx context.Context, countCooled bool) (int, error) {
	db, err := r.database()
	if err != nil {
		return 0, err
	}
	q := `SELECT COUNT(*) FROM auth a WHERE a.deleted_at IS NULL AND COALESCE(a.disabled,false) = false
		AND COALESCE(a.status,'') <> 'error'
		AND a.uuid NOT IN (SELECT auth_uuid FROM credential_reserve WHERE reserved)
		AND a.uuid NOT IN (SELECT auth_uuid FROM credential_health_check WHERE status = 'fail')`
	if db.Dialector.Name() == "postgres" {
		q += ` AND COALESCE(a.auth_json->'metadata'->>'refresh_token','') <> ''`
		// Free-tier credentials do not count towards the reserve minimum.
		q += ` AND COALESCE(a.auth_json->'metadata'->>'subscription_tier','') NOT ILIKE 'free%'`
	}
	if !countCooled {
		q += ` AND NOT (COALESCE(a.unavailable,false) AND a.next_retry_after IS NOT NULL AND a.next_retry_after > CURRENT_TIMESTAMP)`
	}
	var n int64
	err = db.WithContext(contextOrBackground(ctx)).Raw(q).Scan(&n).Error
	return int(n), err
}

// CountReserveEvents counts audit rows of kind since t.
func (r *Repository) CountReserveEvents(ctx context.Context, kind string, since time.Time) (int, error) {
	db, err := r.database()
	if err != nil {
		return 0, err
	}
	var n int64
	err = db.WithContext(contextOrBackground(ctx)).Model(&CredentialReserveEvent{}).Where("kind = ? AND at >= ?", kind, since.UTC()).Count(&n).Error
	return int(n), err
}

// RecordReserveBans audits credentials newly disabled as banned (403 terms/disabled
// account), noting whether they were in reserve at the time. Postgres only.
func (r *Repository) RecordReserveBans(ctx context.Context) (int, error) {
	db, err := r.database()
	if err != nil {
		return 0, err
	}
	if db.Dialector.Name() != "postgres" {
		return 0, nil
	}
	res := db.WithContext(contextOrBackground(ctx)).Exec(`INSERT INTO credential_reserve_event (auth_uuid, kind, at, was_reserved, reserved_since, detail)
		SELECT a.uuid, 'ban', COALESCE(a.updated_at, now()), COALESCE(r.reserved,false), r.reserved_since, left(COALESCE(a.auth_json->>'status_message',''), 200)
		FROM auth a LEFT JOIN credential_reserve r ON r.auth_uuid = a.uuid
		WHERE COALESCE(a.disabled,false) AND COALESCE(a.auth_json->>'status_message','') LIKE '账号已停用%'
		AND NOT EXISTS (SELECT 1 FROM credential_reserve_event e WHERE e.auth_uuid = a.uuid AND e.kind = 'ban')`)
	return int(res.RowsAffected), res.Error
}

// ReserveStats summarises reserve state and ban attribution.
func (r *Repository) ReserveStats(ctx context.Context) (home.ReserveStats, error) {
	var st home.ReserveStats
	db, err := r.database()
	if err != nil {
		return st, err
	}
	tx := db.WithContext(contextOrBackground(ctx))
	type row struct {
		Reserved, EverReserved, BansReserved, BansActive, BansActiveEverReserved, Promotions24h int64
	}
	var x row
	err = tx.Raw(`SELECT
		(SELECT COUNT(*) FROM credential_reserve r JOIN auth a ON a.uuid = r.auth_uuid WHERE r.reserved AND a.deleted_at IS NULL AND COALESCE(a.disabled,false) = false) AS reserved,
		(SELECT COUNT(*) FROM credential_reserve) AS ever_reserved,
		(SELECT COUNT(*) FROM credential_reserve_event WHERE kind = 'ban' AND was_reserved) AS bans_reserved,
		(SELECT COUNT(*) FROM credential_reserve_event WHERE kind = 'ban' AND NOT was_reserved) AS bans_active,
		(SELECT COUNT(*) FROM credential_reserve_event e WHERE e.kind = 'ban' AND NOT e.was_reserved AND EXISTS (SELECT 1 FROM credential_reserve r WHERE r.auth_uuid = e.auth_uuid)) AS bans_active_ever_reserved,
		(SELECT COUNT(*) FROM credential_reserve_event WHERE kind = 'promote' AND at >= ?) AS promotions24h`, time.Now().UTC().Add(-24*time.Hour)).Scan(&x).Error
	st = home.ReserveStats{Reserved: int(x.Reserved), EverReserved: int(x.EverReserved), BansReserved: int(x.BansReserved),
		BansActive: int(x.BansActive), BansAfterPromotion: int(x.BansActiveEverReserved), Promotions24h: int(x.Promotions24h)}
	if err == nil {
		var last struct{ At *time.Time }
		if errLast := tx.Raw(`SELECT MAX(at) AS at FROM credential_reserve_event WHERE kind = 'promote'`).Scan(&last).Error; errLast == nil && last.At != nil {
			st.LastPromotionUnix = last.At.Unix()
		}
	}
	return st, err
}

// RecentReserveEvents returns the newest audit rows.
func (r *Repository) RecentReserveEvents(ctx context.Context, limit int) ([]CredentialReserveEvent, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []CredentialReserveEvent
	err = db.WithContext(contextOrBackground(ctx)).Order("at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

func (a *RuntimeAdapter) reserveRepo() (*Repository, error) {
	if !a.Enabled() {
		return nil, fmt.Errorf("cluster runtime adapter is disabled")
	}
	return a.repo, nil
}

// Runtime adapter pass-throughs (home.ReserveStore).

func (a *RuntimeAdapter) ListReservedCredentials(ctx context.Context) (map[string]time.Time, error) {
	r, err := a.reserveRepo()
	if err != nil {
		return nil, err
	}
	return r.ListReservedCredentials(ctx)
}

func (a *RuntimeAdapter) SetCredentialsReserved(ctx context.Context, ids []string, reserved bool, kind, detail string) (int, error) {
	r, err := a.reserveRepo()
	if err != nil {
		return 0, err
	}
	return r.SetCredentialsReserved(ctx, ids, reserved, kind, detail)
}

func (a *RuntimeAdapter) ListReserveCandidates(ctx context.Context) ([]home.ReserveCandidate, error) {
	r, err := a.reserveRepo()
	if err != nil {
		return nil, err
	}
	return r.ListReserveCandidates(ctx)
}

func (a *RuntimeAdapter) CountActiveCredentials(ctx context.Context, countCooled bool) (int, error) {
	r, err := a.reserveRepo()
	if err != nil {
		return 0, err
	}
	return r.CountActiveCredentials(ctx, countCooled)
}

func (a *RuntimeAdapter) CountReserveEvents(ctx context.Context, kind string, since time.Time) (int, error) {
	r, err := a.reserveRepo()
	if err != nil {
		return 0, err
	}
	return r.CountReserveEvents(ctx, kind, since)
}

func (a *RuntimeAdapter) RecordReserveBans(ctx context.Context) (int, error) {
	r, err := a.reserveRepo()
	if err != nil {
		return 0, err
	}
	return r.RecordReserveBans(ctx)
}

// ReserveModelCapacity reports, for credentials whose latest Antigravity catalog lists
// a model matching pattern (glob; "-disabled" placeholders excluded), which can serve
// it now: not exhausted per catalog quotaInfo and not cooling for that model. Active
// (non-reserved) rows also need the CountActiveCredentials filters with wholly cooled
// credentials excluded. Postgres only.
func (r *Repository) ReserveModelCapacity(ctx context.Context, pattern string) ([]home.ModelCapacityRow, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	if db.Dialector.Name() != "postgres" {
		return nil, nil
	}
	like, prefix := home.GlobToLike(pattern)
	q := `WITH m AS (
		SELECT a.uuid AS id, COALESCE(rv.reserved,false) AS reserved,
			COALESCE((k.value->'quotaInfo'->>'remainingFraction')::float8, CASE WHEN jsonb_exists(k.value, 'quotaInfo') THEN 0 ELSE 1 END) AS rem,
			NULLIF(k.value->'quotaInfo'->>'resetTime','')::timestamptz AS reset_at,
			a.auth_json->'model_states'->k.key AS ms
		FROM auth a
		JOIN antigravity_catalogs c ON c.auth_id = a.uuid AND c.model_count > 0 AND strpos(c.raw, ?) > 0
		LEFT JOIN credential_reserve rv ON rv.auth_uuid = a.uuid
		CROSS JOIN LATERAL jsonb_each(c.raw::jsonb->'models') k
		WHERE a.deleted_at IS NULL AND COALESCE(a.disabled,false) = false
			AND k.key LIKE ? ESCAPE '\' AND k.key NOT LIKE '%-disabled'
			AND (COALESCE(rv.reserved,false) OR (
				COALESCE(a.status,'') <> 'error'
				AND COALESCE(a.auth_json->'metadata'->>'refresh_token','') <> ''
				AND a.uuid NOT IN (SELECT auth_uuid FROM credential_health_check WHERE status = 'fail')
				AND NOT (COALESCE(a.unavailable,false) AND a.next_retry_after IS NOT NULL AND a.next_retry_after > CURRENT_TIMESTAMP)))
	), x AS (
		SELECT id, reserved, rem,
			NOT (rem <= 0 AND (reset_at IS NULL OR reset_at > now()))
			AND NOT (COALESCE((ms->>'unavailable')::bool,false) AND NULLIF(ms->>'next_retry_after','')::timestamptz > now()) AS ok
		FROM m
	)
	SELECT id, reserved, bool_or(ok) AS usable, COALESCE(MAX(rem) FILTER (WHERE ok), 0) AS remaining FROM x GROUP BY id, reserved`
	var rows []home.ModelCapacityRow
	err = db.WithContext(contextOrBackground(ctx)).Raw(q, `"`+prefix, like).Scan(&rows).Error
	return rows, err
}

func (a *RuntimeAdapter) ReserveModelCapacity(ctx context.Context, pattern string) ([]home.ModelCapacityRow, error) {
	r, err := a.reserveRepo()
	if err != nil {
		return nil, err
	}
	return r.ReserveModelCapacity(ctx, pattern)
}
