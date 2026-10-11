package home

// Request-log (usage) ingestion through a Redis stream.
//
// When enabled (HOME_USAGE_STREAM=1 and Redis healthy) every Home XADDs each
// usage payload to one shared stream and every Home runs a consumer in one
// consumer group that batch-writes entries to PostgreSQL and XACKs them only
// after the transaction committed. Effects:
//   - a Home crash or restart no longer loses its in-memory backlog: unacked
//     entries are re-read (own consumer) or XAUTOCLAIMed by a live Home;
//   - a PostgreSQL outage is buffered in Redis (bounded by MAXLEN) instead of in
//     Home memory, and drained by all Homes when PostgreSQL returns.
// Redelivered entries are de-duplicated by stream id (usage.stream_id), so a
// crash between commit and XACK does not double-bill. The check is safe because
// an entry is only claimed from another consumer after usageStreamClaimIdle
// (60s), while a consumer's own write is cancelled after usageWriterDrainLimit
// (20s): by the time an entry is claimed its original writer has either
// committed (then the claim sees the row) or rolled back.
// When Redis is disabled or XADD fails, the payload goes to the in-memory
// queue -> PostgreSQL path as before (fallback). Redis is never required.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
)

const (
	usageStreamName             = "usage:stream"
	usageStreamGroup            = "usage-writers"
	usageStreamBatch            = 500
	usageStreamBlock            = time.Second
	usageStreamClaimIdleDefault = time.Minute
	usageStreamClaimEvery       = 15 * time.Second
	usageStreamConsumerIdle     = time.Hour
	usageStreamDefaultMax       = 1_000_000
)

// usageStreamClaimIdle is how long an entry sits unacked on another consumer
// before it is claimed (var for tests).
var usageStreamClaimIdle = usageStreamClaimIdleDefault

// usageItemsStore writes usage items (with stream ids) in one batch.
type usageItemsStore interface {
	StoreUsageStreamItems(ctx context.Context, payloads []string, receivedAt []time.Time, streamIDs []string, redelivered bool) (int, error)
}

// UsageStreamStatus is shown on the panel.
type UsageStreamStatus struct {
	Enabled     bool      `json:"enabled"`
	Active      bool      `json:"active"` // Redis healthy, stream path in use
	Consumer    string    `json:"consumer"`
	Length      int64     `json:"length"`
	Pending     int64     `json:"pending"`
	Consumers   int64     `json:"consumers"`
	Appended    int64     `json:"appended"`      // by this Home
	Flushed     int64     `json:"flushed"`       // rows written by this Home
	Fallback    int64     `json:"fallback"`      // payloads sent to the in-memory path
	Redelivered int64     `json:"redelivered"`   // entries re-read or claimed
	LastFlushAt time.Time `json:"last_flush_at"` // zero: never
	LastError   string    `json:"last_error,omitempty"`
	MaxLen      int64     `json:"max_len"`
}

type usageStream struct {
	rc       *redisx.Client
	store    usageItemsStore
	consumer string
	maxLen   int64

	appended, flushed, fallback, redelivered atomic.Int64
	lastFlush                                atomic.Int64
	mu                                       sync.Mutex
	lastErr                                  string
}

func usageStreamEnabled() bool {
	v := strings.TrimSpace(os.Getenv("HOME_USAGE_STREAM"))
	return v == "1" || strings.EqualFold(v, "true")
}

func newUsageStream(rc *redisx.Client, store usageItemsStore, consumer string) *usageStream {
	maxLen := int64(usageStreamDefaultMax)
	if v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("HOME_USAGE_STREAM_MAXLEN")), 10, 64); err == nil && v > 0 {
		maxLen = v
	}
	if consumer == "" {
		// Unique per process: two Homes sharing a consumer name would both
		// re-read (and store) the same pending entries.
		host, _ := os.Hostname()
		var b [4]byte
		_, _ = rand.Read(b[:])
		consumer = host + "-" + hex.EncodeToString(b[:])
	}
	return &usageStream{rc: rc, store: store, consumer: consumer, maxLen: maxLen}
}

func (s *usageStream) key() string { return s.rc.Key(usageStreamName) }

func (s *usageStream) setErr(err error) {
	s.mu.Lock()
	if err == nil {
		s.lastErr = ""
	} else {
		s.lastErr = err.Error()
	}
	s.mu.Unlock()
}

// Append adds one payload to the stream; false means the caller must use the
// fallback path.
func (s *usageStream) Append(ctx context.Context, payload string, receivedAt time.Time) bool {
	if s == nil || !s.rc.Healthy() {
		if s != nil {
			s.fallback.Add(1)
		}
		return false
	}
	cctx, cancel := s.rc.Ctx(ctx)
	defer cancel()
	err := s.rc.Raw().XAdd(cctx, &redis.XAddArgs{
		Stream: s.key(),
		MaxLen: s.maxLen,
		Approx: true,
		Values: []any{"p", payload, "t", strconv.FormatInt(receivedAt.UTC().UnixNano(), 10)},
	}).Err()
	s.rc.Observe(err)
	if err != nil {
		s.fallback.Add(1)
		return false
	}
	s.appended.Add(1)
	return true
}

func (s *usageStream) ensureGroup(ctx context.Context) error {
	err := s.rc.Bulk().XGroupCreateMkStream(ctx, s.key(), usageStreamGroup, "0").Err()
	if err != nil && strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return err
}

// Run consumes the stream until ctx is done. Unacked entries stay in Redis on
// exit and are picked up by this or another Home.
func (s *usageStream) Run(ctx context.Context) {
	groupReady := false
	lastClaim := time.Time{}
	backoff := time.Duration(0)
	sleep := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	for ctx.Err() == nil {
		if backoff > 0 && !sleep(backoff) {
			return
		}
		if !s.rc.Healthy() {
			groupReady = false
			backoff = time.Second
			continue
		}
		if !groupReady {
			gctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := s.ensureGroup(gctx)
			cancel()
			if err != nil {
				s.setErr(err)
				backoff = 2 * time.Second
				continue
			}
			groupReady = true
		}
		// 1) own pending entries (read before, not acked: e.g. PG failed or we restarted)
		n, err := s.readAndFlush(ctx, "0", true)
		if err == nil && n == 0 && time.Since(lastClaim) >= usageStreamClaimEvery {
			// 2) entries stuck on dead consumers
			lastClaim = time.Now()
			n, err = s.claimAndFlush(ctx)
			s.pruneConsumers(ctx)
		}
		if err == nil && n == 0 {
			// 3) new entries
			n, err = s.readAndFlush(ctx, ">", false)
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if isNoGroup(err) {
				groupReady = false
			}
			s.setErr(err)
			if backoff < time.Second {
				backoff = time.Second
			} else if backoff < 10*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 0
	}
}

func isNoGroup(err error) bool { return err != nil && strings.Contains(err.Error(), "NOGROUP") }

func (s *usageStream) readAndFlush(ctx context.Context, start string, redelivered bool) (int, error) {
	block := usageStreamBlock
	if start != ">" {
		block = -1 // no BLOCK for pending reads
	}
	rctx, cancel := context.WithTimeout(ctx, usageStreamBlock+3*time.Second)
	res, err := s.rc.Bulk().XReadGroup(rctx, &redis.XReadGroupArgs{
		Group: usageStreamGroup, Consumer: s.consumer,
		Streams: []string{s.key(), start}, Count: usageStreamBatch, Block: block,
	}).Result()
	cancel()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		s.rc.Observe(err)
		return 0, err
	}
	var msgs []redis.XMessage
	for _, st := range res {
		msgs = append(msgs, st.Messages...)
	}
	if start == ">" && len(msgs) > 0 && len(msgs) < usageStreamBatch {
		// Let a small batch fill a little so bursts become one transaction.
		if !sleepCtx(ctx, usageWriterLinger) {
			return 0, ctx.Err()
		}
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		more, errMore := s.rc.Bulk().XReadGroup(rctx, &redis.XReadGroupArgs{
			Group: usageStreamGroup, Consumer: s.consumer,
			Streams: []string{s.key(), ">"}, Count: int64(usageStreamBatch - len(msgs)), Block: -1,
		}).Result()
		cancel()
		if errMore == nil {
			for _, st := range more {
				msgs = append(msgs, st.Messages...)
			}
		}
	}
	return s.flush(ctx, msgs, redelivered)
}

func (s *usageStream) claimAndFlush(ctx context.Context) (int, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	msgs, _, err := s.rc.Bulk().XAutoClaim(cctx, &redis.XAutoClaimArgs{
		Stream: s.key(), Group: usageStreamGroup, Consumer: s.consumer,
		MinIdle: usageStreamClaimIdle, Start: "0", Count: usageStreamBatch,
	}).Result()
	cancel()
	if err != nil {
		s.rc.Observe(err)
		return 0, err
	}
	return s.flush(ctx, msgs, true)
}

// pruneConsumers removes consumers of departed Homes that hold no entries.
func (s *usageStream) pruneConsumers(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cs, err := s.rc.Bulk().XInfoConsumers(cctx, s.key(), usageStreamGroup).Result()
	if err != nil {
		return
	}
	for _, c := range cs {
		if c.Name != s.consumer && c.Pending == 0 && c.Idle >= usageStreamConsumerIdle {
			_ = s.rc.Bulk().XGroupDelConsumer(cctx, s.key(), usageStreamGroup, c.Name).Err()
		}
	}
}

func (s *usageStream) flush(ctx context.Context, msgs []redis.XMessage, redelivered bool) (int, error) {
	if len(msgs) == 0 {
		return 0, nil
	}
	var payloads, sids []string
	var times []time.Time
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
		p, _ := m.Values["p"].(string)
		if strings.TrimSpace(p) == "" {
			continue // acked below: nothing to store
		}
		t := time.Now().UTC()
		if ts, _ := m.Values["t"].(string); ts != "" {
			if ns, err := strconv.ParseInt(ts, 10, 64); err == nil {
				t = time.Unix(0, ns).UTC()
			}
		}
		payloads, times, sids = append(payloads, p), append(times, t), append(sids, m.ID)
	}
	if redelivered {
		s.redelivered.Add(int64(len(msgs)))
	}
	if len(payloads) > 0 {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), usageWriterDrainLimit)
		stored, err := s.store.StoreUsageStreamItems(wctx, payloads, times, sids, redelivered)
		cancel()
		if err != nil {
			// Not acked: re-read from our pending list after the backoff.
			return 0, err
		}
		s.flushed.Add(int64(stored))
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	pipe := s.rc.Bulk().TxPipeline()
	pipe.XAck(actx, s.key(), usageStreamGroup, ids...)
	pipe.XDel(actx, s.key(), ids...)
	_, err := pipe.Exec(actx)
	cancel()
	if err != nil {
		// Rows are committed; the entries will be redelivered and de-duplicated.
		s.rc.Observe(err)
		return len(msgs), err
	}
	s.lastFlush.Store(time.Now().UnixNano())
	s.setErr(nil)
	return len(msgs), nil
}

// Status reports stream state for the panel.
func (s *usageStream) Status(ctx context.Context) UsageStreamStatus {
	st := UsageStreamStatus{Enabled: true, Consumer: s.consumer, MaxLen: s.maxLen,
		Appended: s.appended.Load(), Flushed: s.flushed.Load(), Fallback: s.fallback.Load(),
		Redelivered: s.redelivered.Load()}
	if ns := s.lastFlush.Load(); ns > 0 {
		st.LastFlushAt = time.Unix(0, ns).UTC()
	}
	s.mu.Lock()
	st.LastError = s.lastErr
	s.mu.Unlock()
	st.Active = s.rc.Healthy()
	if !st.Active {
		return st
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if n, err := s.rc.Bulk().XLen(cctx, s.key()).Result(); err == nil {
		st.Length = n
	}
	if gs, err := s.rc.Bulk().XInfoGroups(cctx, s.key()).Result(); err == nil {
		for _, g := range gs {
			if g.Name == usageStreamGroup {
				st.Pending, st.Consumers = g.Pending, g.Consumers
			}
		}
	}
	return st
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
