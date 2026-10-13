package cluster

import (
	"context"
	"time"
)

// CredentialSortStats are per-credential values the panel sorts by.
type CredentialSortStats struct {
	LastUsed     map[string]time.Time
	MinRemaining map[string]float64 // lowest remaining_ratio across quota windows
}

// LoadCredentialSortStats reads last-used (index-backed per credential, recent
// partitions only) and lowest quota remaining for the given credential ids.
func (r *Repository) LoadCredentialSortStats(ctx context.Context, ids []string, since time.Time) (CredentialSortStats, error) {
	out := CredentialSortStats{LastUsed: map[string]time.Time{}, MinRemaining: map[string]float64{}}
	db, err := r.database()
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		var row struct{ Last *string }
		if err := db.WithContext(ctx).Raw(`SELECT CAST(MAX(timestamp) AS TEXT) AS last FROM usage WHERE auth_index = ? AND timestamp > ?`, id, since).Scan(&row).Error; err != nil {
			return out, err
		}
		if row.Last != nil {
			if t, ok := parseDBTime(*row.Last); ok {
				out.LastUsed[id] = t
			}
		}
	}
	var rows []struct {
		CredentialID string
		Min          *float64
	}
	if err := db.WithContext(ctx).Raw(`SELECT credential_id, MIN(remaining_ratio) AS min FROM quota_window WHERE remaining_ratio IS NOT NULL GROUP BY credential_id`).Scan(&rows).Error; err != nil {
		return out, err
	}
	for _, r := range rows {
		if r.Min != nil {
			out.MinRemaining[r.CredentialID] = *r.Min
		}
	}
	return out, nil
}

func parseDBTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999-07", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
