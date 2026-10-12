package redisqueue

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultRetentionSeconds int64 = 600 // keep unsent usage through a long Home/PG outage
	maxRetentionSeconds     int64 = 3600
	usageSubscriberBuffer         = 256
	errorSubscriberBuffer         = 256

	usageSupportRefreshPayload = `{"support_refresh":true}`
	usageRefreshPayload        = `{"refresh":true}`
)

type queueItem struct {
	enqueuedAt time.Time
	payload    []byte
}

type queue struct {
	mu               sync.Mutex
	items            []queueItem
	head             int
	bytes            int64 // payload bytes in items[head:]
	subscribers      map[uint64]chan []byte
	nextSubscriberID uint64
}

var (
	enabled          atomic.Bool
	retentionSeconds atomic.Int64
	global           queue
	errorGlobal      queue
)

// Bounds on unsent usage (oldest dropped first, counted in Dropped):
// PROXY_USAGE_QUEUE_MAX entries, PROXY_USAGE_QUEUE_MAX_BYTES payload bytes.
var (
	maxQueueItems = envInt64("PROXY_USAGE_QUEUE_MAX", 100000)
	maxQueueBytes = envInt64("PROXY_USAGE_QUEUE_MAX_BYTES", 128<<20)
	droppedAge    atomic.Int64
	droppedFull   atomic.Int64
	lastDropLogNS atomic.Int64
)

func envInt64(name string, def int64) int64 {
	if v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64); err == nil && v > 0 {
		return v
	}
	return def
}

// Stats is the unsent usage queue state.
type Stats struct {
	Queued      int   `json:"queued"`
	Bytes       int64 `json:"bytes"`
	DroppedAge  int64 `json:"dropped_age"`  // older than the retention window
	DroppedFull int64 `json:"dropped_full"` // count / byte bound reached
}

// UsageStats reports the unsent usage queue.
func UsageStats() Stats {
	global.mu.Lock()
	defer global.mu.Unlock()
	return Stats{Queued: len(global.items) - global.head, Bytes: global.bytes,
		DroppedAge: droppedAge.Load(), DroppedFull: droppedFull.Load()}
}

func noteDropped(counter *atomic.Int64, n int, why string) {
	if n <= 0 {
		return
	}
	total := counter.Add(int64(n))
	now := time.Now().UnixNano()
	if last := lastDropLogNS.Load(); now-last > int64(10*time.Second) && lastDropLogNS.CompareAndSwap(last, now) {
		log.Warnf("usage queue: dropped %d unsent usage records (%s; total %d)", n, why, total)
	}
}

func init() {
	if v := envInt64("PROXY_USAGE_RETENTION_SECONDS", 0); v > 0 {
		retentionSeconds.Store(min(v, maxRetentionSeconds))
	} else {
		retentionSeconds.Store(defaultRetentionSeconds)
	}
}

// RequeueFront puts payloads that could not be delivered back at the head of
// the queue in their original order, so they are resent first.
func RequeueFront(payloads [][]byte) {
	if !Enabled() || len(payloads) == 0 {
		return
	}
	global.requeueFront(payloads)
}

func SetEnabled(value bool) {
	enabled.Store(value)
	if !value {
		global.clear()
		errorGlobal.clear()
	}
}

func Enabled() bool {
	return enabled.Load()
}

func SetRetentionSeconds(value int) {
	normalized := int64(value)
	if normalized <= 0 {
		normalized = defaultRetentionSeconds
	} else if normalized > maxRetentionSeconds {
		normalized = maxRetentionSeconds
	}
	// Floor (PROXY_USAGE_RETENTION_SECONDS, default 600): older Homes send 60.
	if floor := envInt64("PROXY_USAGE_RETENTION_SECONDS", defaultRetentionSeconds); floor > normalized {
		normalized = min(floor, maxRetentionSeconds)
	}
	retentionSeconds.Store(normalized)
}

func Enqueue(payload []byte) {
	if !Enabled() {
		return
	}
	if len(payload) == 0 {
		return
	}
	if global.publishToSubscribers(payload) {
		return
	}
	global.enqueue(payload)
}

func EnqueueError(payload []byte) {
	if !Enabled() {
		return
	}
	if len(payload) == 0 {
		return
	}
	errorGlobal.publishToSubscribers(payload)
}

func PopOldest(count int) [][]byte {
	if !Enabled() {
		return nil
	}
	if count <= 0 {
		return nil
	}
	return global.popOldest(count)
}

func SubscribeUsage() (<-chan []byte, func()) {
	return global.subscribe(usageSubscriberBuffer, []byte(usageSupportRefreshPayload))
}

func SubscribeErrors() (<-chan []byte, func()) {
	return errorGlobal.subscribe(errorSubscriberBuffer, nil)
}

func NotifyUsageRefresh() {
	global.publishToSubscribers([]byte(usageRefreshPayload))
}

func (q *queue) clear() {
	q.mu.Lock()

	subscribers := make([]chan []byte, 0, len(q.subscribers))
	for _, subscriber := range q.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	q.items = nil
	q.head = 0
	q.bytes = 0
	q.subscribers = nil
	q.mu.Unlock()

	for _, subscriber := range subscribers {
		close(subscriber)
	}
}

func (q *queue) enqueue(payload []byte) {
	now := time.Now()

	q.mu.Lock()
	defer q.mu.Unlock()

	q.pruneLocked(now)
	q.items = append(q.items, queueItem{
		enqueuedAt: now,
		payload:    append([]byte(nil), payload...),
	})
	q.bytes += int64(len(payload))
	q.enforceBoundsLocked()
	q.maybeCompactLocked()
}

// requeueFront re-inserts undelivered payloads at the head. Their age restarts
// (the original enqueue time is not tracked past PopOldest); bounds still apply.
func (q *queue) requeueFront(payloads [][]byte) {
	now := time.Now()
	q.mu.Lock()
	defer q.mu.Unlock()
	front := make([]queueItem, len(payloads))
	for i, p := range payloads {
		front[i] = queueItem{enqueuedAt: now, payload: p}
		q.bytes += int64(len(p))
	}
	q.items = append(front, q.items[q.head:]...)
	q.head = 0
	q.enforceBoundsLocked()
}

func (q *queue) enforceBoundsLocked() {
	dropped := 0
	for q.head < len(q.items) && (int64(len(q.items)-q.head) > maxQueueItems || q.bytes > maxQueueBytes) {
		q.bytes -= int64(len(q.items[q.head].payload))
		q.items[q.head] = queueItem{}
		q.head++
		dropped++
	}
	noteDropped(&droppedFull, dropped, "queue bound reached")
}

func (q *queue) publishToSubscribers(payload []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.subscribers) == 0 {
		return false
	}

	for id, subscriber := range q.subscribers {
		cloned := append([]byte(nil), payload...)
		select {
		case subscriber <- cloned:
		default:
			delete(q.subscribers, id)
			close(subscriber)
		}
	}

	return true
}

func (q *queue) subscribe(buffer int, initialPayload []byte) (<-chan []byte, func()) {
	subscriber := make(chan []byte, buffer)
	if len(initialPayload) > 0 {
		subscriber <- append([]byte(nil), initialPayload...)
	}

	q.mu.Lock()
	if q.subscribers == nil {
		q.subscribers = make(map[uint64]chan []byte)
	}
	q.nextSubscriberID++
	id := q.nextSubscriberID
	q.subscribers[id] = subscriber
	q.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			q.unsubscribe(id)
		})
	}
	return subscriber, unsubscribe
}

func (q *queue) unsubscribe(id uint64) {
	q.mu.Lock()
	subscriber, ok := q.subscribers[id]
	if ok {
		delete(q.subscribers, id)
	}
	q.mu.Unlock()

	if ok {
		close(subscriber)
	}
}

func (q *queue) popOldest(count int) [][]byte {
	now := time.Now()

	q.mu.Lock()
	defer q.mu.Unlock()

	q.pruneLocked(now)
	available := len(q.items) - q.head
	if available <= 0 {
		q.items = nil
		q.head = 0
		return nil
	}
	if count > available {
		count = available
	}

	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		item := q.items[q.head+i]
		out = append(out, item.payload)
		q.bytes -= int64(len(item.payload))
		q.items[q.head+i] = queueItem{}
	}
	q.head += count
	q.maybeCompactLocked()
	return out
}

func (q *queue) pruneLocked(now time.Time) {
	if q.head >= len(q.items) {
		q.items = nil
		q.head = 0
		q.bytes = 0
		return
	}

	windowSeconds := retentionSeconds.Load()
	if windowSeconds <= 0 {
		windowSeconds = defaultRetentionSeconds
	}
	cutoff := now.Add(-time.Duration(windowSeconds) * time.Second)
	dropped := 0
	for q.head < len(q.items) && q.items[q.head].enqueuedAt.Before(cutoff) {
		q.bytes -= int64(len(q.items[q.head].payload))
		q.items[q.head] = queueItem{}
		q.head++
		dropped++
	}
	noteDropped(&droppedAge, dropped, "older than retention")
}

func (q *queue) maybeCompactLocked() {
	if q.head == 0 {
		return
	}
	if q.head >= len(q.items) {
		q.items = nil
		q.head = 0
		return
	}
	if q.head < 1024 && q.head*2 < len(q.items) {
		return
	}
	q.items = append([]queueItem(nil), q.items[q.head:]...)
	q.head = 0
}
