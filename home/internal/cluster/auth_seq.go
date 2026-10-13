package cluster

import (
	"context"

	"gorm.io/gorm"
)

// Credential sequence numbers: a stable, human-friendly "#N" per credential,
// assigned in creation order by a database sequence (unique across Home
// replicas) and never reused, since soft-deleted rows keep theirs and the
// sequence only moves forward.

func ensureAuthSeq(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	tx := db.WithContext(contextOrBackground(ctx))
	if !tx.Migrator().HasTable("auth") {
		return nil
	}
	if tx.Dialector.Name() == "postgres" {
		var ready int64
		if err := tx.Raw(`SELECT COUNT(*) FROM information_schema.columns c
			WHERE c.table_schema = current_schema() AND c.table_name = 'auth' AND c.column_name = 'seq'
			  AND c.column_default LIKE 'nextval(%auth_seq_seq%'
			  AND EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_auth_seq')`).Scan(&ready).Error; err != nil {
			return err
		}
		if ready > 0 {
			return nil // already migrated; avoid ALTER locks on every start
		}
		for _, stmt := range []string{
			`CREATE SEQUENCE IF NOT EXISTS "auth_seq_seq"`,
			`ALTER TABLE "auth" ADD COLUMN IF NOT EXISTS "seq" bigint`,
			// Backfill in creation order (deleted rows too, so numbers are never reused).
			`WITH todo AS (SELECT "uuid" FROM "auth" WHERE "seq" IS NULL ORDER BY "created_at" NULLS LAST, "uuid"),
			      numbered AS (SELECT "uuid", nextval('auth_seq_seq') AS n FROM todo)
			 UPDATE "auth" a SET "seq" = numbered.n FROM numbered WHERE a."uuid" = numbered."uuid"`,
			`SELECT setval('auth_seq_seq', GREATEST((SELECT COALESCE(MAX("seq"),0) FROM "auth"), (SELECT last_value FROM "auth_seq_seq"), 1))`,
			`ALTER SEQUENCE "auth_seq_seq" OWNED BY "auth"."seq"`,
			`ALTER TABLE "auth" ALTER COLUMN "seq" SET DEFAULT nextval('auth_seq_seq')`,
			`CREATE UNIQUE INDEX IF NOT EXISTS "idx_auth_seq" ON "auth" ("seq")`,
		} {
			if err := tx.Exec(stmt).Error; err != nil {
				return err
			}
		}
		return nil
	}
	// SQLite (dev/tests): column + trigger handing out MAX+1.
	if !tx.Migrator().HasColumn("auth", "seq") {
		if err := tx.Exec(`ALTER TABLE "auth" ADD COLUMN "seq" integer`).Error; err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		`UPDATE "auth" SET "seq" = t.n FROM (SELECT "uuid", ROW_NUMBER() OVER (ORDER BY "created_at", "uuid") + (SELECT COALESCE(MAX("seq"),0) FROM "auth") AS n FROM "auth" WHERE "seq" IS NULL) t WHERE "auth"."uuid" = t."uuid"`,
		`CREATE TRIGGER IF NOT EXISTS "trg_auth_seq" AFTER INSERT ON "auth" WHEN NEW."seq" IS NULL BEGIN UPDATE "auth" SET "seq" = (SELECT COALESCE(MAX("seq"),0)+1 FROM "auth") WHERE "uuid" = NEW."uuid"; END`,
	} {
		if err := tx.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// AuthSeqs returns uuid -> sequence number for all live credentials.
func (r *Repository) AuthSeqs(ctx context.Context) (map[string]int64, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	var rows []struct {
		UUID string `gorm:"column:uuid"`
		Seq  int64  `gorm:"column:seq"`
	}
	if err := db.WithContext(contextOrBackground(ctx)).Table("auth").Select(`"uuid", COALESCE("seq",0) AS "seq"`).Where(`"deleted_at" IS NULL`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.UUID] = r.Seq
	}
	return out, nil
}
