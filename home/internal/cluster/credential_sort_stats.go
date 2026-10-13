package cluster

import (
	"context"
	"strings"
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
	if db.Dialector.Name() == "postgres" {
		// One round trip: a per-id index probe (auth_index, timestamp DESC)
		// instead of N queries (10k credentials = 10k round trips).
		for start := 0; start < len(ids); start += 5000 {
			end := start + 5000
			if end > len(ids) {
				end = len(ids)
			}
			var rows []struct {
				ID   string
				Last *string
			}
			if err := db.WithContext(ctx).Raw(`SELECT x.id, CAST(l.last AS TEXT) AS last FROM unnest(CAST(? AS text[])) AS x(id)
				CROSS JOIN LATERAL (SELECT MAX(u.timestamp) AS last FROM usage u WHERE u.auth_index = x.id AND u.timestamp > ?) l`,
				pgTextArray(ids[start:end]), since).Scan(&rows).Error; err != nil {
				return out, err
			}
			for _, row := range rows {
				if row.Last != nil {
					if t, ok := parseDBTime(*row.Last); ok {
						out.LastUsed[row.ID] = t
					}
				}
			}
		}
	} else {
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

// pgTextArray renders a Postgres text[] literal.
func pgTextArray(v []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
