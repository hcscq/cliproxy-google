package home

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
)

type usageRollupMaintainer interface {
	MaintainUsageRollup(ctx context.Context, now time.Time) error
}

const usageRollupEvery = time.Minute

// startUsageRollup keeps the usage overview rollup fresh on the catalog leader.
func (r *Runtime) startUsageRollup(ctx context.Context) {
	m, ok := r.clusterAdapter.(usageRollupMaintainer)
	if !ok {
		return
	}
	go func() {
		t := time.NewTicker(usageRollupEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !r.catalogLeader() {
					continue
				}
				tickCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				if err := m.MaintainUsageRollup(tickCtx, time.Now()); err != nil {
					log.WithError(err).Warn("usage rollup maintenance failed")
				}
				cancel()
			}
		}
	}()
}
