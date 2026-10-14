package cluster

import (
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// UsagePayloadItem is one queued usage payload.
type UsagePayloadItem struct {
	Payload  string
	Metadata UsageRuntimeMetadata
	// Redelivered marks a Redis stream entry that may already be stored (it was
	// read before but not acknowledged); it is skipped when its stream id exists.
	Redelivered bool
}

// AppendUsageBatch stores many usage payloads with one transaction for the
// usage rows + billing charges, and one quota upsert per credential (the
// latest observation wins, as it would sequentially). If the batch
// transaction fails, rows are retried one by one so a single bad payload
// cannot drop the others. Returns the number of rows stored.
func (r *Repository) AppendUsageBatch(ctx context.Context, items []UsagePayloadItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	db, errDB := r.database()
	if errDB != nil {
		return 0, errDB
	}
	db = bulkDBFor(db)
	ctx = contextOrBackground(ctx)
	type built struct {
		record   *UsageRecord
		payload  string
		metadata UsageRuntimeMetadata
	}
	type quotaKey struct{ authIndex, authType string }
	type quotaResolved struct {
		uuid, key string
		version   int64
		found     bool
	}
	quotaCache := map[quotaKey]quotaResolved{}
	rows := make([]built, 0, len(items))
	for _, it := range items {
		if strings.TrimSpace(it.Payload) == "" {
			continue
		}
		record, err := UsageRecordFromPayloadWithRuntime(it.Payload, it.Metadata)
		if err != nil {
			log.WithError(err).Warn("usage payload rejected")
			continue
		}
		if record.ParentSessionID != "" && (record.RootSessionID == "" || record.RootSessionID == record.ParentSessionID) {
			record.RootSessionID = r.resolveRootSessionID(ctx, db, record.SessionID, record.ParentSessionID)
		}
		record.QuotaCredentialID, record.QuotaIdentityVersion, record.QuotaIdentityKey = "", 0, ""
		k := quotaKey{record.AuthIndex, record.AuthType}
		q, ok := quotaCache[k]
		if !ok {
			resolved, identityKey, found, errResolve := resolveQuotaUsageCredential(ctx, db, record.AuthIndex, record.AuthType)
			if errResolve != nil {
				log.WithError(errResolve).Warn("usage quota credential resolution ignored")
			} else if found {
				q = quotaResolved{uuid: resolved.UUID, key: identityKey, version: quotaCredentialIdentityVersion(resolved), found: true}
			}
			quotaCache[k] = q
		}
		if q.found {
			record.QuotaCredentialID, record.QuotaIdentityVersion, record.QuotaIdentityKey = q.uuid, q.version, q.key
		}
		if it.Redelivered && record.StreamID != "" && usageStreamEntryStored(ctx, db, record) {
			continue
		}
		rows = append(rows, built{record: record, payload: it.Payload, metadata: it.Metadata})
	}
	if len(rows) == 0 {
		return 0, nil
	}
	allRecords := make([]*UsageRecord, len(rows))
	for i := range rows {
		allRecords[i] = rows[i].record
	}
	retryCandidates := usageRetryMarkBefore(ctx, allRecords)
	errTx := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		records := allRecords
		if err := tx.CreateInBatches(records, 200).Error; err != nil {
			return err
		}
		for _, b := range rows {
			if err := r.createBillingChargeForUsageTx(ctx, tx, b.record, b.payload); err != nil {
				return err
			}
		}
		return nil
	})
	stored := len(rows)
	if errTx != nil {
		// PostgreSQL unreachable: report the failure so a stream consumer does
		// not acknowledge (and delete) entries that were never written.
		if !databaseReachable(ctx, db) {
			return 0, fmt.Errorf("usage batch not stored, database unreachable: %w", errTx)
		}
		log.WithError(errTx).WithField("rows", len(rows)).Warn("usage batch failed; retrying rows individually")
		stored = 0
		for _, b := range rows {
			b.record.ID = 0
			if _, err := r.AppendUsageWithRuntime(ctx, b.payload, b.metadata); err != nil {
				if !databaseReachable(ctx, db) {
					// Rows stored so far are redelivered and skipped by stream id.
					return stored, fmt.Errorf("usage batch partly stored, database unreachable: %w", err)
				}
				// A row PostgreSQL rejects on its own: skip it, do not block the rest.
				log.WithError(err).Error("usage row write failed")
				continue
			}
			stored++
		}
		return stored, nil // individual path already did quota upserts
	}
	usageRetryMarkAfter(ctx, db, retryCandidates)
	latest := map[string]built{}
	order := []string{}
	for _, b := range rows {
		id := b.record.QuotaCredentialID
		if id == "" {
			continue
		}
		if _, seen := latest[id]; !seen {
			order = append(order, id)
		}
		latest[id] = b
	}
	for _, id := range order {
		b := latest[id]
		if err := upsertQuotaFromUsageRecord(ctx, db, b.record, b.metadata); err != nil {
			log.WithError(err).Warn("usage quota observation ignored")
		}
	}
	return stored, nil
}

// usageStreamEntryStored reports whether a row for this stream entry already
// exists. The lookup is bounded to the record's own timestamp so it prunes to
// one partition and uses the timestamp index (no index on stream_id needed).
// On a lookup error it reports false: a rare duplicate beats a lost row.
func usageStreamEntryStored(ctx context.Context, db *gorm.DB, record *UsageRecord) bool {
	var n int64
	err := db.WithContext(ctx).Model(&UsageRecord{}).
		Where("timestamp >= ? AND timestamp <= ? AND stream_id = ?",
			record.Timestamp.Add(-time.Millisecond), record.Timestamp.Add(time.Millisecond), record.StreamID).
		Limit(1).Count(&n).Error
	if err != nil {
		log.WithError(err).Warn("usage stream dedupe lookup failed")
		return false
	}
	return n > 0
}

// databaseReachable reports whether a trivial query succeeds.
func databaseReachable(ctx context.Context, db *gorm.DB) bool {
	pctx, cancel := context.WithTimeout(contextOrBackground(ctx), 3*time.Second)
	defer cancel()
	return db.WithContext(pctx).Exec("SELECT 1").Error == nil
}
