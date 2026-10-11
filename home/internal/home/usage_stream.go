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
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
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
	usageStreamBuffer           = 65536 // payloads waiting for XADD in this Home
	usageStreamAppendBatch      = 256
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
	Deduped     int64     `json:"deduped"`       // resent payloads dropped as duplicates (usage_id seen)
	LastFlushAt time.Time `json:"last_flush_at"` // zero: never
	LastError   string    `json:"last_error,omitempty"`
	MaxLen      int64     `json:"max_len"`
}

type usageStream struct {
	rc       *redisx.Client
	store    usageItemsStore
	consumer string
	maxLen   int64

	// Append hands payloads to an in-process buffer; one appender pipelines
	// XADDs (a synchronous XADD per proxy LPUSH made proxies' sequential usage
	// forwarders fall behind). Failures go to fallbackPush (in-memory -> PG).
	buf          chan streamItem
	fallbackPush func(payload string, receivedAt time.Time) bool
	appendMu     sync.RWMutex
	closed       bool
	appenderDone chan struct{}

	appended, flushed, fallback, redelivered, deduped atomic.Int64
	lastFlush                                         atomic.Int64
	mu                                                sync.Mutex
	lastErr                                           string
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
	return &usageStream{rc: rc, store: store, consumer: consumer, maxLen: maxLen,
		buf: make(chan streamItem, usageStreamBuffer), appenderDone: make(chan struct{})}
}

type streamItem struct {
	payload    string
	receivedAt time.Time
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

// Append queues one payload for the stream; false means the caller must use
// the fallback path (Redis unhealthy, buffer full or stream stopped).
func (s *usageStream) Append(ctx context.Context, payload string, receivedAt time.Time) bool {
	if s == nil {
		return false
	}
	if !s.rc.Healthy() {
		s.fallback.Add(1)
		return false
	}
	s.appendMu.RLock()
	defer s.appendMu.RUnlock()
	if s.closed {
		s.fallback.Add(1)
		return false
	}
	select {
	case s.buf <- streamItem{payload: payload, receivedAt: receivedAt.UTC()}:
		return true
	default:
		s.fallback.Add(1)
		return false
	}
}

func (s *usageStream) isClosed() bool {
	s.appendMu.RLock()
	defer s.appendMu.RUnlock()
	return s.closed
}

// runAppender pipelines buffered payloads into the stream until Close.
func (s *usageStream) runAppender() {
	defer close(s.appenderDone)
	batch := make([]streamItem, 0, usageStreamAppendBatch)
	for {
		item, ok := <-s.buf
		if !ok {
			return
		}
		batch = append(batch[:0], item)
	fill:
		for len(batch) < usageStreamAppendBatch {
			select {
			case it, ok := <-s.buf:
				if !ok {
					break fill
				}
				batch = append(batch, it)
			default:
				break fill
			}
		}
		s.xaddBatch(batch)
	}
}

// usageStreamAddOnce adds an entry unless its usage id was already seen:
// proxies resend a batch whose LPUSH reply was lost, so the "seen" marker and
// the XADD must be atomic (a marker without the entry would lose the resend).
const usageStreamAddOnce = `
if redis.call('SET', KEYS[2], '1', 'NX', 'EX', ARGV[4]) then
  redis.call('XADD', KEYS[1], 'MAXLEN', '~', ARGV[1], '*', 'p', ARGV[2], 't', ARGV[3])
  return 1
end
return 0`

func usageDedupeTTL() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv("HOME_USAGE_DEDUPE_TTL"))); err == nil && d > 0 {
		return d
	}
	return 15 * time.Minute
}

func (s *usageStream) xaddBatch(batch []streamItem) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ttl := strconv.FormatInt(int64(usageDedupeTTL()/time.Second), 10)
	maxLen := strconv.FormatInt(s.maxLen, 10)
	pipe := s.rc.Bulk().Pipeline()
	cmds := make([]redis.Cmder, len(batch))
	for i, it := range batch {
		t := strconv.FormatInt(it.receivedAt.UnixNano(), 10)
		if id := gjson.Get(it.payload, "usage_id").String(); id != "" {
			cmds[i] = pipe.Eval(ctx, usageStreamAddOnce, []string{s.key(), s.key() + ":seen:" + id}, maxLen, it.payload, t, ttl)
			continue
		}
		cmds[i] = pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: s.key(),
			MaxLen: s.maxLen,
			Approx: true,
			Values: []any{"p", it.payload, "t", t},
		})
	}
	_, err := pipe.Exec(ctx)
	if err != nil && errors.Is(err, redis.Nil) {
		err = nil
	}
	s.rc.Observe(err)
	for i, c := range cmds {
		if c.Err() == nil {
			if ic, ok := c.(*redis.Cmd); ok {
				if n, _ := ic.Int64(); n == 0 {
					s.deduped.Add(1)
					continue
				}
			}
			s.appended.Add(1)
			continue
		}
		s.fallback.Add(1)
		if s.fallbackPush == nil || !s.fallbackPush(batch[i].payload, batch[i].receivedAt) {
			log.Warn("usage stream: payload dropped (XADD and fallback queue both failed)")
		}
	}
}

// Close stops accepting payloads and waits (bounded) until buffered ones are
// in the stream or handed to the fallback.
func (s *usageStream) Close(timeout time.Duration) {
	if s == nil {
		return
	}
	s.appendMu.Lock()
	if !s.closed {
		s.closed = true
		close(s.buf)
	}
	s.appendMu.Unlock()
	select {
	case <-s.appenderDone:
	case <-time.After(timeout):
		log.Warnf("usage stream: %d buffered payloads not flushed before shutdown", len(s.buf))
	}
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
func (s *usageStream) Run(ctx context.Context) { s.runWorker(ctx, s.consumer) }

// usageStreamWorkers is the number of parallel consumers per Home
// (HOME_USAGE_STREAM_WORKERS, default 4).
func usageStreamWorkers() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("HOME_USAGE_STREAM_WORKERS"))); err == nil && v > 0 && v <= 32 {
		return v
	}
	return 4
}

// runWorker is one consumer loop; consumer names must be unique.
func (s *usageStream) runWorker(ctx context.Context, consumer string) {
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
		n, err := s.readAndFlush(ctx, consumer, "0", true)
		if err == nil && n == 0 && time.Since(lastClaim) >= usageStreamClaimEvery {
			// 2) entries stuck on dead consumers
			lastClaim = time.Now()
			n, err = s.claimAndFlush(ctx, consumer)
			s.pruneConsumers(ctx, consumer)
		}
		if err == nil && n == 0 {
			// 3) new entries
			n, err = s.readAndFlush(ctx, consumer, ">", false)
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

func (s *usageStream) readAndFlush(ctx context.Context, consumer, start string, redelivered bool) (int, error) {
	block := usageStreamBlock
	if start != ">" {
		block = -1 // no BLOCK for pending reads
	}
	rctx, cancel := context.WithTimeout(ctx, usageStreamBlock+3*time.Second)
	res, err := s.rc.Bulk().XReadGroup(rctx, &redis.XReadGroupArgs{
		Group: usageStreamGroup, Consumer: consumer,
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
			Group: usageStreamGroup, Consumer: consumer,
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

func (s *usageStream) claimAndFlush(ctx context.Context, consumer string) (int, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	msgs, _, err := s.rc.Bulk().XAutoClaim(cctx, &redis.XAutoClaimArgs{
		Stream: s.key(), Group: usageStreamGroup, Consumer: consumer,
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
func (s *usageStream) pruneConsumers(ctx context.Context, self string) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cs, err := s.rc.Bulk().XInfoConsumers(cctx, s.key(), usageStreamGroup).Result()
	if err != nil {
		return
	}
	for _, c := range cs {
		if !strings.HasPrefix(c.Name, s.consumer) && c.Name != self && c.Pending == 0 && c.Idle >= usageStreamConsumerIdle {
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
		Redelivered: s.redelivered.Load(), Deduped: s.deduped.Load()}
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
