package cluster

import (
	"context"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Cluster events (cooldowns, credential changes, config publishes) are polled
// from PostgreSQL every event-poll-interval. With Redis, the Home that writes
// an event also publishes a wake-up so every Home polls within milliseconds;
// PostgreSQL stays the source of truth and polling remains the fallback.

const eventWakeChannelSuffix = "events"

var (
	eventWakeOnce   sync.Once
	eventWakeSignal = make(chan struct{}, 1)
)

// registerEventWakeCallback hooks every insert into cluster_events.
func registerEventWakeCallback(db *gorm.DB) {
	if db == nil {
		return
	}
	_ = db.Callback().Create().After("gorm:create").Register("cpg:event_wake", func(tx *gorm.DB) {
		if tx.Error == nil && tx.Statement != nil && tx.Statement.Table == (ClusterEventRecord{}).TableName() {
			select {
			case eventWakeSignal <- struct{}{}:
			default:
			}
		}
	})
	eventWakeOnce.Do(func() { go runEventWakePublisher() })
}

// runEventWakePublisher coalesces signals and publishes outside transactions.
func runEventWakePublisher() {
	for range eventWakeSignal {
		rc := redisx.Default()
		if !rc.Healthy() {
			continue
		}
		ctx, cancel := rc.Ctx(context.Background())
		rc.Observe(rc.Raw().Publish(ctx, rc.Key(eventWakeChannelSuffix), "1").Err())
		cancel()
	}
}

// SubscribeEventWake calls wake for every published event wake-up until ctx
// ends; it re-subscribes across Redis failovers and returns immediately when
// Redis is off.
func SubscribeEventWake(ctx context.Context, wake func()) {
	rc := redisx.Default()
	if rc == nil {
		return
	}
	go func() {
		for ctx.Err() == nil {
			sub := rc.Raw().Subscribe(ctx, rc.Key(eventWakeChannelSuffix))
			ch := sub.Channel()
			// A (re)subscription may have missed wake-ups: poll once.
			wake()
		recv:
			for {
				select {
				case <-ctx.Done():
					break recv
				case _, ok := <-ch:
					if !ok {
						break recv
					}
					wake()
				}
			}
			_ = sub.Close()
			if ctx.Err() == nil {
				log.Debug("redis: event wake subscription ended; resubscribing")
				time.Sleep(time.Second)
			}
		}
	}()
}
