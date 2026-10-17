package cluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

// ListRequestCapMeta returns tier and cap override for every active, enabled auth.
func (r *Repository) ListRequestCapMeta(ctx context.Context) (map[string]home.RequestCapMeta, error) {
	db, errDB := r.database()
	if errDB != nil {
		return nil, errDB
	}
	type row struct {
		UUID   string
		Tier   string
		Mode   string
		Hourly string
		Daily  string
	}
	var rows []row
	err := db.WithContext(contextOrBackground(ctx)).Raw(`SELECT uuid,
		COALESCE(auth_json->'metadata'->>'subscription_tier','') AS tier,
		COALESCE(auth_json->'metadata'->>'request_cap_mode','') AS mode,
		COALESCE(auth_json->'metadata'->>'request_cap_hourly','') AS hourly,
		COALESCE(auth_json->'metadata'->>'request_cap_daily','') AS daily
		FROM auth WHERE deleted_at IS NULL AND COALESCE(disabled,false) = false`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]home.RequestCapMeta, len(rows))
	for _, x := range rows {
		out[strings.TrimSpace(x.UUID)] = home.RequestCapMeta{Tier: x.Tier, Mode: x.Mode, Hourly: x.Hourly, Daily: x.Daily}
	}
	return out, nil
}

// ListRequestCapMeta exposes the repository lookup to the Home runtime.
func (a *RuntimeAdapter) ListRequestCapMeta(ctx context.Context) (map[string]home.RequestCapMeta, error) {
	if !a.Enabled() {
		return nil, fmt.Errorf("cluster runtime adapter is disabled")
	}
	return a.repo.ListRequestCapMeta(ctx)
}
